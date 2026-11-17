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
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	metricpb "google.golang.org/genproto/googleapis/api/metric"
	monitoredrespb "google.golang.org/genproto/googleapis/api/monitoredres"
	"google.golang.org/protobuf/types/known/timestamppb"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
)

// fakeSeries answers each query from canned series keyed by metric type, and
// records the filters it was asked for. There is no Cloud Monitoring emulator,
// so this is the only place the reader's query plan can be asserted on.
type fakeSeries struct {
	byMetric map[string][]*monitoringpb.TimeSeries
	filters  []string
	windows  map[string]time.Duration
	err      error
}

func (f *fakeSeries) List(_ context.Context, req *monitoringpb.ListTimeSeriesRequest) ([]*monitoringpb.TimeSeries, error) {
	f.filters = append(f.filters, req.GetFilter())
	if f.windows == nil {
		f.windows = make(map[string]time.Duration)
	}
	f.windows[req.GetFilter()] = req.GetInterval().GetEndTime().AsTime().
		Sub(req.GetInterval().GetStartTime().AsTime())
	if f.err != nil {
		return nil, f.err
	}
	for metricType, series := range f.byMetric {
		if strings.Contains(req.GetFilter(), `metric.type="`+metricType+`"`) {
			return series, nil
		}
	}
	return nil, nil
}

// point builds a one-sample series.
func point(metricLabels, resourceLabels map[string]string, value int64, at time.Time) *monitoringpb.TimeSeries {
	return &monitoringpb.TimeSeries{
		Metric:   &metricpb.Metric{Labels: metricLabels},
		Resource: &monitoredrespb.MonitoredResource{Labels: resourceLabels},
		Points: []*monitoringpb.Point{{
			Interval: &monitoringpb.TimeInterval{EndTime: timestamppb.New(at)},
			Value:    &monitoringpb.TypedValue{Value: &monitoringpb.TypedValue_Int64Value{Int64Value: value}},
		}},
	}
}

func newReader(series TimeSeries) *QuotaReader {
	return NewQuotaReader("dev-project", series, slog.Default())
}

// A family the project has never run has no time series at all. That is the
// ordinary state of every dimensioned pool on the day this ships, so it must
// produce no row. A zero-limit row would mean a pool granted nothing, and
// every zone offering the family would become unplaceable.
func TestQuotaReaderAbsentSeriesYieldsNoRow(t *testing.T) {
	t.Parallel()
	reader := newReader(&fakeSeries{})

	rows, err := reader.Quotas(t.Context(), []string{"europe-west4"})
	if err != nil {
		t.Fatalf("Quotas: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none: an absent series is not a granted zero", rows)
	}
}

// The per-family CPU pool is assembled from its own limit and usage series,
// named by the vm_family label the flat region list cannot express.
func TestQuotaReaderPairsPerFamilyLimitAndUsage(t *testing.T) {
	t.Parallel()
	limitAt := time.Now().Add(-3 * time.Minute)
	usageAt := time.Now().Add(-time.Minute)
	fake := &fakeSeries{byMetric: map[string][]*monitoringpb.TimeSeries{
		cpusPerVMFamilyLimit: {
			point(map[string]string{"vm_family": "C4", "limit_name": "CPUS-PER-VM-FAMILY-per-project-region"},
				map[string]string{"location": "europe-west4"}, 300, limitAt),
		},
		cpusPerVMFamilyUsage: {
			point(map[string]string{"vm_family": "C4"},
				map[string]string{"location": "europe-west4"}, 96, usageAt),
		},
	}}
	reader := newReader(fake)

	rows, err := reader.Quotas(t.Context(), []string{"europe-west4"})
	if err != nil {
		t.Fatalf("Quotas: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want exactly the C4 pool", rows)
	}
	row := rows[0]
	if row.QuotaType != "CPUS_PER_VM_FAMILY:C4" || row.Region != "europe-west4" {
		t.Errorf("row identity = %s/%s, want europe-west4/CPUS_PER_VM_FAMILY:C4", row.Region, row.QuotaType)
	}
	if row.LimitValue != 300 || row.UsageValue != 96 {
		t.Errorf("row = limit %d usage %d, want 300/96", row.LimitValue, row.UsageValue)
	}
	// The pair is only as current as its stalest half.
	if row.LastSyncAt == nil || !row.LastSyncAt.Equal(limitAt.UTC()) {
		t.Errorf("sampled at %v, want the older of the two samples (%v)", row.LastSyncAt, limitAt.UTC())
	}
}

// Half a reading cannot be judged, so it is left unknown rather than paired
// with a zero for the missing half.
func TestQuotaReaderHalfAReadingYieldsNoRow(t *testing.T) {
	t.Parallel()
	fake := &fakeSeries{byMetric: map[string][]*monitoringpb.TimeSeries{
		cpusPerVMFamilyLimit: {
			point(map[string]string{"vm_family": "N4"},
				map[string]string{"location": "europe-west4"}, 300, time.Now()),
		},
	}}
	reader := newReader(fake)

	rows, err := reader.Quotas(t.Context(), []string{"europe-west4"})
	if err != nil {
		t.Fatalf("Quotas: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none: a limit with no usage is not a judgment", rows)
	}
}

// The hyperdisk pool comes off the shared consumer-quota metrics, where the
// same quota also carries a per-zone limit reported as unlimited. The query
// names the regional limit, and a zonal series that arrives anyway is dropped
// by the region filter rather than becoming a region's ceiling.
func TestQuotaReaderHyperdiskTakesTheRegionalLimit(t *testing.T) {
	t.Parallel()
	now := time.Now()
	fake := &fakeSeries{byMetric: map[string][]*monitoringpb.TimeSeries{
		consumerQuotaLimit: {
			point(map[string]string{"quota_metric": hyperdiskBalancedQuota, "limit_name": hdbRegionalLimit},
				map[string]string{"location": "europe-west4"}, 40960, now),
			point(map[string]string{"quota_metric": hyperdiskBalancedQuota, "limit_name": "HDB-TOTAL-GB-per-project-zone"},
				map[string]string{"location": "europe-west4-b"}, gcecatalog.LimitUnlimited, now),
		},
		consumerQuotaUsage: {
			point(map[string]string{"quota_metric": hyperdiskBalancedQuota},
				map[string]string{"location": "europe-west4"}, 800, now),
			point(map[string]string{"quota_metric": hyperdiskBalancedQuota},
				map[string]string{"location": "europe-west4-b"}, 800, now),
		},
	}}
	reader := newReader(fake)

	rows, err := reader.Quotas(t.Context(), []string{"europe-west4"})
	if err != nil {
		t.Fatalf("Quotas: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want exactly the regional hyperdisk pool", rows)
	}
	if rows[0].QuotaType != gcecatalog.QuotaTypeHDBTotalGB || rows[0].LimitValue != 40960 {
		t.Errorf("row = %+v, want %s limited to 40960", rows[0], gcecatalog.QuotaTypeHDBTotalGB)
	}
	var named bool
	for _, filter := range fake.filters {
		if strings.Contains(filter, `metric.label.limit_name="`+hdbRegionalLimit+`"`) {
			named = true
		}
	}
	if !named {
		t.Errorf("filters = %v, want the limit query to name the regional limit", fake.filters)
	}
}

// A region the fleet cannot place in is not read into the snapshot even when
// the project has quota there.
func TestQuotaReaderKeepsOnlyTheWantedRegions(t *testing.T) {
	t.Parallel()
	now := time.Now()
	fake := &fakeSeries{byMetric: map[string][]*monitoringpb.TimeSeries{
		cpusPerVMFamilyLimit: {
			point(map[string]string{"vm_family": "N4"}, map[string]string{"location": "us-east5"}, 300, now),
		},
		cpusPerVMFamilyUsage: {
			point(map[string]string{"vm_family": "N4"}, map[string]string{"location": "us-east5"}, 8, now),
		},
	}}
	reader := newReader(fake)

	rows, err := reader.Quotas(t.Context(), []string{"europe-west4"})
	if err != nil {
		t.Fatalf("Quotas: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none outside the enabled regions", rows)
	}
	if len(fake.filters) == 0 {
		t.Fatal("no query was issued at all")
	}
}

// A failed read is reported, so the caller can name the axes it has no answer
// for. It must never be mistaken for an answer of zero.
func TestQuotaReaderReadFailureIsAnError(t *testing.T) {
	t.Parallel()
	reader := newReader(&fakeSeries{err: errors.New("permission denied on monitoring.timeSeries.list")})

	rows, err := reader.Quotas(t.Context(), []string{"europe-west4"})
	if err == nil {
		t.Fatalf("Quotas returned %+v and no error on a failed read", rows)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %+v, want none alongside the error", rows)
	}
}

// The consumer-quota metrics are written every few hours, not every minute.
// A window sized for the per-family metrics finds no series for them at all,
// which reads as an axis that does not exist, so the two are read over
// different windows and must stay that way.
func TestQuotaReaderReadsTheTwoMetricFamiliesOverDifferentWindows(t *testing.T) {
	t.Parallel()
	fake := &fakeSeries{}
	reader := newReader(fake)

	if _, err := reader.Quotas(t.Context(), []string{"europe-west4"}); err != nil {
		t.Fatalf("Quotas: %v", err)
	}
	for filter, window := range fake.windows {
		want := resourceQuotaWindow
		if strings.Contains(filter, "serviceruntime.googleapis.com") {
			want = consumerQuotaWindow
		}
		// The interval is built from one clock reading per call, so the
		// difference is exact.
		if window != want {
			t.Errorf("window for %q = %v, want %v", filter, window, want)
		}
	}
	if consumerQuotaWindow <= time.Hour {
		t.Errorf("consumer-quota window = %v; the observed sample interval exceeds an hour", consumerQuotaWindow)
	}
}

// No enabled region means no query at all.
func TestQuotaReaderNoRegionsReadsNothing(t *testing.T) {
	t.Parallel()
	fake := &fakeSeries{}
	reader := newReader(fake)

	rows, err := reader.Quotas(t.Context(), nil)
	if err != nil || len(rows) != 0 {
		t.Fatalf("Quotas(nil) = %+v, %v; want no rows and no error", rows, err)
	}
	if len(fake.filters) != 0 {
		t.Errorf("filters = %v, want no query issued", fake.filters)
	}
}
