// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cloudmonitoring

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
)

// VisibilityLag is how long Cloud Monitoring may withhold a quota sample after
// taking it. It is documented for the whole quota metric group, so a reading is
// already this old the moment it can be read at all. Rows are therefore stamped
// with the sample's own time, and the freshness a judgment demands of them has
// to allow for the lag.
const VisibilityLag = 150 * time.Second

// How far back each kind of query looks for the newest sample. The two metric
// families are written on completely different clocks and one window cannot
// serve both. The per-family CPU metrics sample every 60 seconds, while the
// consumer-quota pair is written only every few hours. A window sized for the
// first finds no series at all for the second, which would read as an axis
// that does not exist. Every row carries the time of the sample it came from,
// so the wide window costs freshness nothing, because an old sample arrives
// saying how old it is.
const (
	resourceQuotaWindow = 10 * time.Minute
	consumerQuotaWindow = 24 * time.Hour
)

// hyperdiskBalancedQuota is the quota metric a hyperdisk-balanced disk's
// capacity is charged to, and hdbRegionalLimit the limit on it that is scoped
// to a region. The per-zone limit of the same quota is reported as unlimited,
// so reading limits without naming which one is wanted answers a pool with no
// ceiling for every region.
const (
	hyperdiskBalancedQuota = "compute.googleapis.com/hyperdisk_balanced_total_storage"
	hdbRegionalLimit       = "HDB-TOTAL-GB-per-project-region"
)

// Metric types. The per-family CPU pools are a resource-specific quota, one
// metric triple on Compute Engine's own Location resource, carrying the
// selecting dimension as a label. The hyperdisk pools are a consumer quota,
// which every service writes to the one shared serviceruntime metric pair with
// the pool named in a label.
const (
	cpusPerVMFamilyLimit = "compute.googleapis.com/quota/cpus_per_vm_family/limit"
	cpusPerVMFamilyUsage = "compute.googleapis.com/quota/cpus_per_vm_family/usage"
	consumerQuotaLimit   = "serviceruntime.googleapis.com/quota/limit"
	consumerQuotaUsage   = "serviceruntime.googleapis.com/quota/allocation/usage"
)

// Monitored resource types the two metric families are written against.
const (
	computeLocationResource = "compute.googleapis.com/Location"
	consumerQuotaResource   = "consumer_quota"
)

// QuotaReader answers the quota axes the Compute Engine region list cannot
// carry, in the catalog's own row shape.
type QuotaReader struct {
	project string
	series  TimeSeries
	logger  *slog.Logger
}

// NewQuotaReader builds the reader for a project.
func NewQuotaReader(project string, series TimeSeries, logger *slog.Logger) *QuotaReader {
	return &QuotaReader{project: project, series: series, logger: logger}
}

// query is one metric read: the series to fetch, and how to name the pool each
// returned series belongs to. A series naming no pool is skipped.
type query struct {
	metricType string
	resource   string
	labels     map[string]string
	window     time.Duration
	limit      bool // a limit series, else a usage one
	pool       func(*monitoringpb.TimeSeries) (region, quotaType string)
}

// pool is one quota axis being assembled from its limit and usage series. Both
// halves are required: a limit with no usage cannot be judged, and a usage with
// no limit even less so.
type pool struct {
	limit, usage         int64
	limitAt, usageAt     time.Time
	haveLimit, haveUsage bool
	region, quotaType    string
}

// Quotas reads the per-family CPU pools and the hyperdisk capacity pool for the
// given regions.
//
// An axis with no series produces no row rather than a zero one. A family the
// project has never run has no series at all, which is the ordinary state and
// not a failure. A zero limit would mean a pool granted nothing, which is what
// makes a region unplaceable.
func (r *QuotaReader) Quotas(ctx context.Context, regions []string) ([]*gcecatalog.RegionalQuota, error) {
	if len(regions) == 0 {
		return nil, nil
	}
	wanted := make(map[string]bool, len(regions))
	for _, region := range regions {
		wanted[region] = true
	}

	now := time.Now()
	pools := make(map[string]*pool)
	for _, q := range r.queries() {
		series, err := r.series.List(ctx, &monitoringpb.ListTimeSeriesRequest{
			Name:   "projects/" + r.project,
			Filter: q.filter(),
			Interval: &monitoringpb.TimeInterval{
				StartTime: timestamppb.New(now.Add(-q.window)),
				EndTime:   timestamppb.New(now),
			},
			View: monitoringpb.ListTimeSeriesRequest_FULL,
		})
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", q.metricType, err)
		}
		r.collect(pools, q, series, wanted)
	}
	return r.rows(pools), nil
}

// queries is the read plan: a limit and a usage series for each axis.
func (r *QuotaReader) queries() []query {
	perFamily := func(series *monitoringpb.TimeSeries) (string, string) {
		family := series.GetMetric().GetLabels()["vm_family"]
		if family == "" {
			return "", ""
		}
		return series.GetResource().GetLabels()["location"],
			gcecatalog.DimensionedQuotaType(gcecatalog.QuotaTypeCPUsPerVMFamily, family)
	}
	hyperdisk := func(series *monitoringpb.TimeSeries) (string, string) {
		return series.GetResource().GetLabels()["location"], gcecatalog.QuotaTypeHDBTotalGB
	}
	return []query{
		{
			metricType: cpusPerVMFamilyLimit, resource: computeLocationResource,
			window: resourceQuotaWindow, limit: true, pool: perFamily,
		},
		{
			metricType: cpusPerVMFamilyUsage, resource: computeLocationResource,
			window: resourceQuotaWindow, pool: perFamily,
		},
		{
			metricType: consumerQuotaLimit, resource: consumerQuotaResource,
			window: consumerQuotaWindow, limit: true, pool: hyperdisk,
			labels: map[string]string{"quota_metric": hyperdiskBalancedQuota, "limit_name": hdbRegionalLimit},
		},
		{
			metricType: consumerQuotaUsage, resource: consumerQuotaResource,
			window: consumerQuotaWindow, pool: hyperdisk,
			labels: map[string]string{"quota_metric": hyperdiskBalancedQuota},
		},
	}
}

// filter renders the query as a monitoring filter. Every term is an equality on
// a name this package holds as a constant, so nothing caller-supplied reaches
// the filter language.
func (q query) filter() string {
	terms := make([]string, 0, len(q.labels)+2)
	terms = append(terms,
		fmt.Sprintf("metric.type=%q", q.metricType),
		fmt.Sprintf("resource.type=%q", q.resource))
	for _, key := range slices.Sorted(maps.Keys(q.labels)) {
		terms = append(terms, fmt.Sprintf("metric.label.%s=%q", key, q.labels[key]))
	}
	return strings.Join(terms, " AND ")
}

// collect folds one query's series into the pools being assembled.
func (r *QuotaReader) collect(pools map[string]*pool, q query, series []*monitoringpb.TimeSeries, wanted map[string]bool) {
	for _, s := range series {
		region, quotaType := q.pool(s)
		if quotaType == "" || !wanted[region] {
			// A zonal series of the same quota, a region this fleet cannot
			// place in, or a series whose selecting label is missing.
			continue
		}
		value, at, ok := newestPoint(s)
		if !ok {
			continue
		}
		key := region + "\x00" + quotaType
		p := pools[key]
		if p == nil {
			p = &pool{region: region, quotaType: quotaType}
			pools[key] = p
		}
		if q.limit {
			p.limit, p.limitAt, p.haveLimit = value, at, true
			continue
		}
		p.usage, p.usageAt, p.haveUsage = value, at, true
	}
}

// rows renders the fully-read pools as catalog quota rows, region-ordered.
//
// A pool is stamped with the older of its two samples: the pair is only as
// current as its stalest half, and claiming the fresher one would let a
// judgment trust a usage figure the limit beside it never saw.
func (r *QuotaReader) rows(pools map[string]*pool) []*gcecatalog.RegionalQuota {
	out := make([]*gcecatalog.RegionalQuota, 0, len(pools))
	for _, key := range slices.Sorted(maps.Keys(pools)) {
		p := pools[key]
		if !p.haveLimit || !p.haveUsage {
			r.logger.Debug("a quota axis reported only half a reading; leaving it unknown",
				slog.String("region", p.region), slog.String("quota_type", p.quotaType))
			continue
		}
		sampledAt := p.limitAt
		if p.usageAt.Before(sampledAt) {
			sampledAt = p.usageAt
		}
		out = append(out, &gcecatalog.RegionalQuota{
			Region:     p.region,
			QuotaType:  p.quotaType,
			LimitValue: p.limit,
			UsageValue: p.usage,
			LastSyncAt: &sampledAt,
		})
	}
	return out
}

// newestPoint reads a series' most recent value and when it was sampled.
func newestPoint(series *monitoringpb.TimeSeries) (int64, time.Time, bool) {
	var value int64
	var at time.Time
	var found bool
	for _, point := range series.GetPoints() {
		sampledAt := point.GetInterval().GetEndTime().AsTime()
		if found && !sampledAt.After(at) {
			continue
		}
		v, ok := pointValue(point)
		if !ok {
			continue
		}
		value, at, found = v, sampledAt, true
	}
	return value, at, found
}

// pointValue reads a quota point's number. The quota metrics are declared
// INT64; a double is accepted so an unlimited pool arriving as a float cannot
// overflow into a negative ceiling on the way in.
func pointValue(point *monitoringpb.Point) (int64, bool) {
	switch v := point.GetValue().GetValue().(type) {
	case *monitoringpb.TypedValue_Int64Value:
		return v.Int64Value, true
	case *monitoringpb.TypedValue_DoubleValue:
		if v.DoubleValue >= float64(gcecatalog.LimitUnlimited) {
			return gcecatalog.LimitUnlimited, true
		}
		if v.DoubleValue < 0 {
			return 0, true
		}
		return int64(v.DoubleValue), true
	default:
		return 0, false
	}
}
