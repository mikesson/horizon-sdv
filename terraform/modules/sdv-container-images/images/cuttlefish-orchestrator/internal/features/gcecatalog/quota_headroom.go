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

package gcecatalogfeature

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/platform/cloudmonitoring"
	"cloud-cuttlefish-orchestrator/internal/platform/gce"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// Quota-judgment defaults; the margin is operator-tunable via
// placement.quota_margin_percent, the staleness bound via ValidatorConfig.
// The fallback bound tracks the interval that produces the rows (one missed
// refresh is jitter, two is a refresher that has stopped) plus the lag the
// quota telemetry withholds a sample for, since a row read from it carries the
// time of the sample rather than of the read.
const (
	defaultQuotaMarginPercent = 10
	defaultQuotaStale         = 2*gce.QuotaRefreshInterval + cloudmonitoring.VisibilityLag
)

// metricDemand is one regional quota axis a shape draws from, with how much
// one host consumes.
type metricDemand struct {
	quotaType string // gcecatalog.QuotaType* (already the preemptible twin for Spot)
	// fallback is the standard pool a Spot demand judges when the region has no
	// preemptible grant at all: GCE grants PREEMPTIBLE_* quotas separately, and
	// a project without them draws its Spot VMs from the standard pools, so
	// judging only the absent preemptible pool would leave a Spot spec
	// quota-blind. Empty = none.
	fallback string
	amount   int64
	// healthMetric is the zone_health binding metric this demand corresponds
	// to, so a regional quota cooldown only repels specs that actually draw
	// from the cooled axis.
	healthMetric string
}

// bindingDemand computes the regional quota axes one host of this shape draws
// from: the CPU pool its machine family is charged to, the matching GPU metric
// when GPUs attach, the capacity pool its boot disk type fills, and an in-use
// address when the deployment exposes device media on external IPs, with the
// PREEMPTIBLE_* twins substituted for Spot.
//
// The machine family decides which CPU pool a shape draws from. Charging every
// shape the one flat pool is correct for n1 alone: it judges twice the real
// headroom for n2, ten times it for c3, a pool n4/c4/c4a never touch, and for
// g2 a pool that family spends nothing from.
func (r *ZoneRanker) bindingDemand(req gcecatalog.ValidationRequest, mt *machinetype.MachineType) []metricDemand {
	spot := req.ProvisioningModel == provisioningModelSpot
	// For Spot the preemptible twin is primary and the standard pool is the
	// fallback (judged when the region has no preemptible grant at all).
	withSpot := func(quotaType string) (string, string) {
		if twin := gcecatalog.ToPreemptibleQuotaType(quotaType); spot && twin != quotaType {
			return twin, quotaType
		}
		return quotaType, ""
	}

	var demand []metricDemand
	if pool := gcecatalog.CPUQuotaTypeForFamily(machineFamilyOf(mt)); pool != "" {
		cpuQuota, cpuFallback := withSpot(pool)
		demand = append(demand, metricDemand{
			quotaType:    cpuQuota,
			fallback:     cpuFallback,
			amount:       mt.VCPUs,
			healthMetric: gcecatalog.ZoneMetricQuotaCPU,
		})
	}

	gpuCount := req.GPUCount
	gpuType := req.GPUType
	if mt.GPUMode == GPUModeIntegrated {
		gpuCount = mt.IntegratedGPUCount
		gpuType = mt.IntegratedGPUType
	}
	if gpuCount > 0 {
		if quotaType := gcecatalog.QuotaTypeForGPU(gpuType); quotaType != "" {
			gpuQuota, gpuFallback := withSpot(quotaType)
			demand = append(demand, metricDemand{
				quotaType:    gpuQuota,
				fallback:     gpuFallback,
				amount:       gpuCount,
				healthMetric: gcecatalog.ZoneMetricQuotaGPU,
			})
		}
	}
	// The boot disk is the axis that binds a growing fleet first, and the pool
	// it fills depends on its type: the newer families boot from
	// hyperdisk-balanced, which counts against the hyperdisk pool and against
	// no other, so their disks are invisible to the SSD pool the pd-* shapes
	// fill. Priced at the default size, because ranking answers for the fleet's
	// own hosts and a per-host override is a create-time choice this
	// shape-level question does not see.
	if pool := gcecatalog.BootDiskQuotaType(mt.BootDiskType); pool != "" {
		demand = append(demand, metricDemand{
			quotaType:    pool,
			amount:       host.DefaultBootDiskSizeGB,
			healthMetric: diskHealthMetric(pool),
		})
	}

	if r.config.UseExternalIP {
		demand = append(demand, metricDemand{
			quotaType:    gcecatalog.QuotaTypeInUseAddresses,
			amount:       1,
			healthMetric: gcecatalog.ZoneMetricQuotaIP,
		})
	}
	return demand
}

// diskHealthMetric is the zone_health axis a boot-disk pool cools. The two
// pools are raised separately, so a cooldown naming the wrong one sends an
// operator after the wrong increase.
func diskHealthMetric(quotaType string) string {
	if quotaType == gcecatalog.QuotaTypeHDBTotalGB {
		return gcecatalog.ZoneMetricQuotaHyperdisk
	}
	return gcecatalog.ZoneMetricQuotaSSD
}

// metricState classifies how trustworthy one demanded metric's quota row is.
type metricState int

const (
	// metricUnknown: no row in either pool, so pass advisorily.
	metricUnknown metricState = iota
	// metricStale: the row is older than the trust threshold.
	metricStale
	// metricZeroLimit: the row carries no limit to judge against.
	metricZeroLimit
	// metricJudged: the row is trustworthy; fits carries the verdict.
	metricJudged
)

// metricVerdict is one demanded metric's regional-quota judgment. It is the
// shared core of ranking's region filter and Validate's quota checks.
type metricVerdict struct {
	judgedType string                    // the quota type actually judged (after any fallback)
	row        *gcecatalog.RegionalQuota // nil when metricUnknown
	state      metricState
	fits       bool   // metricJudged only: available - demand covers the reserve
	reserve    int64  // metricJudged only
	note       string // fallback-substitution trace note ("" when none)
}

// judgeMetric evaluates one demanded metric against its region's quota row:
// the primary pool, or the fallback pool when the primary has no row or a zero
// limit. Unknown, stale, and zero-limit rows never carry a verdict, so the
// caller decides how to degrade.
func (r *ZoneRanker) judgeMetric(region string, d metricDemand, margin int64, staleAfter time.Duration) metricVerdict {
	q, judgedType, note := r.resolveQuotaRow(region, d)
	verdict := metricVerdict{judgedType: judgedType, row: q, note: note}
	switch {
	case q == nil:
		verdict.state = metricUnknown
	case q.LastSyncAt == nil || time.Since(*q.LastSyncAt) > staleAfter:
		verdict.state = metricStale
	case q.LimitValue <= 0:
		verdict.state = metricZeroLimit
	default:
		verdict.state = metricJudged
		// Divided before multiplied: a pool with no ceiling carries the largest
		// int64 there is, and taking a percentage of it the other way round
		// overflows into a negative reserve that every demand clears.
		verdict.reserve = q.LimitValue / 100 * margin
		verdict.fits = q.Available()-d.amount >= verdict.reserve
	}
	return verdict
}

// regionVerdict is one region's quota evaluation, shared by all its zones.
type regionVerdict struct {
	pass     bool
	frac     float64 // min availability fraction across judged metrics
	binding  string  // the metric the fraction came from
	notes    []string
	rejected string // the failing metric's detail when pass is false
}

// filterByQuota drops candidates whose region lacks headroom on any demanded
// metric, keeping the availability fraction for scoring and returning the
// zones it dropped with the failing metric's detail. Regions are judged once
// (quota is regional). Unknown or stale quota rows pass advisorily with a note,
// because placement must keep working when the quota refresh is behind.
func (r *ZoneRanker) filterByQuota(ctx context.Context, candidates []*rankedZone, demand []metricDemand) ([]*rankedZone, []zoneExclusion, error) {
	margin, err := r.quotaMarginPercent(ctx)
	if err != nil {
		return nil, nil, err
	}
	staleAfter := r.config.QuotaStaleThreshold
	if staleAfter <= 0 {
		staleAfter = defaultQuotaStale
	}

	verdicts := make(map[string]*regionVerdict)
	for _, c := range candidates {
		region := c.zone.Region
		if _, done := verdicts[region]; !done {
			verdicts[region] = r.judgeRegionQuota(region, demand, margin, staleAfter)
		}
	}

	var out []*rankedZone
	var excluded []zoneExclusion
	for _, c := range candidates {
		verdict := verdicts[c.zone.Region]
		if !verdict.pass {
			r.metrics.quotaRejected(ctx, c.zone.Zone)
			excluded = append(excluded, zoneExclusion{
				zone:   c.zone.Zone,
				reason: c.zone.Region + " quota headroom, " + verdict.rejected,
			})
			continue
		}
		c.headroomRaw = verdict.frac
		c.headroom = verdict.binding
		c.notes = append(c.notes, verdict.notes...)
		out = append(out, c)
	}
	return out, excluded, nil
}

// judgeRegionQuota evaluates one region's headroom across the demanded
// metrics against the reserve margin.
func (r *ZoneRanker) judgeRegionQuota(region string, demand []metricDemand, margin int64, staleAfter time.Duration) *regionVerdict {
	verdict := &regionVerdict{pass: true, frac: 1}
	judged := false
	for _, d := range demand {
		mv := r.judgeMetric(region, d, margin, staleAfter)
		if mv.note != "" {
			verdict.notes = append(verdict.notes, mv.note)
		}
		switch mv.state {
		case metricUnknown:
			verdict.notes = append(verdict.notes,
				fmt.Sprintf("%s quota unknown in %s; passing advisorily", d.quotaType, region))
			continue
		case metricStale:
			verdict.notes = append(verdict.notes,
				fmt.Sprintf("%s quota stale in %s; passing advisorily", mv.judgedType, region))
			continue
		case metricZeroLimit:
			// Post-fallback zero limit is a real no-grant, but ranking stays
			// availability-first: rejecting regions on a partial sync could
			// zero out auto-placement fleet-wide, and zone failover corrects
			// a bad pick. Validate, which is an admission verdict for one
			// explicit zone, hard-fails this same state.
			verdict.notes = append(verdict.notes,
				fmt.Sprintf("%s quota limit 0 in %s; passing advisorily", mv.judgedType, region))
			continue
		case metricJudged:
		}
		if !mv.fits {
			verdict.pass = false
			verdict.rejected = fmt.Sprintf("%s: need %d + reserve %d, %d free of %d",
				mv.judgedType, d.amount, mv.reserve, mv.row.Available(), mv.row.LimitValue)
			return verdict
		}
		frac := float64(mv.row.Available()) / float64(mv.row.LimitValue)
		if !judged || frac < verdict.frac {
			verdict.frac = frac
			verdict.binding = fmt.Sprintf("%s %d/%d free", strings.ToLower(mv.judgedType), mv.row.Available(), mv.row.LimitValue)
			judged = true
		}
	}
	if !judged {
		verdict.frac = 0.5 // nothing judged: neutral, the notes say why
	}
	return verdict
}

// resolveQuotaRow reads the quota row a demand judges: the primary pool, or
// the fallback pool when the primary has no row OR a zero limit and the region
// has no preemptible grant at all.
//
// The region decides the fallback, whatever the row says. A project with no
// preemptible quota in a region draws its Spot VMs from the standard pools, so
// the standard pool is what must be judged there. Once any preemptible quota is
// granted in the region every Spot VM in it counts against preemptible quota
// and cannot consume standard quota in its place, so on an axis granted zero
// in such a region, the zero is the answer, and substituting a roomy standard
// pool would promise capacity Compute Engine will refuse.
//
// It returns the row (nil when nothing judgeable is tracked), the quota type
// actually judged, and a trace note when the fallback substituted. A
// zero-limit row with no fallback is returned as-is for the caller to classify
// (an ungranted pool); a zero-limit primary whose fallback is
// untracked resolves to nil, since the pool that would actually be drawn from
// is unknown.
func (r *ZoneRanker) resolveQuotaRow(region string, d metricDemand) (*gcecatalog.RegionalQuota, string, string) {
	q := r.regionalQuota(region, d.quotaType)
	primaryTracked := q != nil
	if (primaryTracked && q.LimitValue > 0) || d.fallback == "" {
		return q, d.quotaType, ""
	}
	if gcecatalog.HasPreemptibleGrant(r.regionalQuotas(region)) {
		return q, d.quotaType, ""
	}
	fq := r.regionalQuota(region, d.fallback)
	if fq == nil {
		// Untracked fallback: with a zero-limit primary the pool Spot would
		// actually draw from is unknown, so nothing is judgeable either way.
		return nil, d.quotaType, ""
	}
	note := fmt.Sprintf("%s not tracked in %s; judging %s", d.quotaType, region, d.fallback)
	if primaryTracked {
		note = fmt.Sprintf("%s granted 0 in %s; judging %s", d.quotaType, region, d.fallback)
	}
	return fq, d.fallback, note
}

// regionalQuota reads one region's quota metric from the Compute Engine
// snapshot; an unwired snapshot answers nil, which reads as untracked and
// passes advisorily.
func (r *ZoneRanker) regionalQuota(region, quotaType string) *gcecatalog.RegionalQuota {
	if r.shapes == nil {
		return nil
	}
	return r.shapes.RegionalQuota(region, quotaType)
}

// regionalQuotas reads every quota row held for a region, which is what the
// region-wide questions (is there a preemptible grant here) are asked of.
func (r *ZoneRanker) regionalQuotas(region string) []*gcecatalog.RegionalQuota {
	if r.shapes == nil {
		return nil
	}
	return r.shapes.RegionalQuotas(region)
}

// quotaMarginPercent reads the operator's reserve margin; unset or malformed
// reads as the compiled default.
func (r *ZoneRanker) quotaMarginPercent(ctx context.Context) (int64, error) {
	raw, err := r.policyString(ctx, models.ConfigKeyQuotaMarginPercent)
	if err != nil {
		return 0, err
	}
	if margin, perr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); perr == nil && margin >= 0 && margin <= 90 {
		return margin, nil
	}
	if raw != "" {
		r.logger.Warn("invalid placement.quota_margin_percent; using default",
			slog.String("value", raw))
	}
	return defaultQuotaMarginPercent, nil
}
