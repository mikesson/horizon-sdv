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
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// Spot ranking defaults; the staleness bound is operator-tunable via
// ValidatorConfig.
const (
	defaultSpotAdviceStale = time.Hour

	provisioningModelSpot = "SPOT"
)

// PolicyReader reads operator placement policy from system_config.
type PolicyReader interface {
	Get(ctx context.Context, key string) (*models.SystemConfig, error)
}

// ZoneRanker ranks placement zones for a machine shape: hard availability and
// quota filters over the enabled catalog, zone_health cooldowns, Spot
// capacity advice, then normalized scoring. It is the placement half of the
// catalog brain; CatalogValidator is the admission half, and the two share
// the quota-judgment engine (quota_headroom.go).
type ZoneRanker struct {
	zones        EnabledZones
	machineTypes machinetype.Catalog
	config       ValidatorConfig
	logger       *slog.Logger

	// shapes is the Compute Engine snapshot; nil answers every cell "unknown",
	// which removes no zone.
	shapes ShapeCatalog

	// zoneHealth is the placement plane's reactive memory; nil ranks without
	// cooldowns (tests, harnesses).
	zoneHealth gcecatalog.ZoneHealthStore
	// spotAdvice is the synced Spot capacity advice; nil ranks Spot specs on
	// the quota/health signals alone.
	spotAdvice gcecatalog.SpotAdviceStore
	// policy reads the operator placement overrides (zone pin, quota margin);
	// nil reads as unset policy.
	policy  PolicyReader
	metrics *placementMetrics
}

// rankedZone accumulates one candidate's raw scoring terms before
// normalization.
type rankedZone struct {
	zone        *gcecatalog.Zone
	headroomRaw float64 // min availability fraction across demanded metrics, 0..1
	headroom    string  // the binding metric's detail for the reason trace
	healthRaw   float64 // 1/(1+recent failures)
	failures    int64
	geoRaw      float64 // negated distance to the preferred region
	geoKnown    bool
	obtainRaw   float64 // Spot obtainability 0..1 from fresh advice
	obtainKnown bool
	uptimeTier  int64 // estimated-uptime tier seconds (0 = unknown)
	cooled      bool
	notes       []string // degradations observed while evaluating this zone
}

// feasibility is one placement question, as the single filter below asks it.
type feasibility struct {
	zones       []*gcecatalog.Zone
	machineType string
	gpuType     string // separately attached accelerator, "" when none
	family      string // machine family, for zone_health matching
	pin         string
	exclude     string
	demand      []metricDemand
	spot        bool
}

// zoneExclusion is one zone that left the candidate set, and why.
type zoneExclusion struct {
	zone   string
	reason string
}

// RankZones returns the viable zones for the shape, best first. One
// feasibility filter (feasibleZones) applies the hard filters: the enabled
// catalog (which is also the network-topology guard, since subnets are regional
// and ranking never wanders past the admin-curated zone set), machine type and
// accelerator availability, regional quota headroom on the shape's binding
// metrics minus the operator's reserve margin (freshness-gated: stale quota
// degrades to pass-with-reason, a stalled refresh must not block
// provisioning), and zone_health cooldowns (soft: if every candidate is cooled
// the cooldowns are ignored, since they must never make the fleet
// unprovisionable).
// Spot specs with fresh capacity advice then filter on the estimated-uptime
// tier. Survivors are scored on normalized headroom, zone health, Spot
// obtainability, and proximity, deterministically (stable sort, zone-name
// tie-break).
func (r *ZoneRanker) RankZones(ctx context.Context, req gcecatalog.ValidationRequest, opts gcecatalog.RankOptions) ([]gcecatalog.ZoneCandidate, error) {
	out, err := r.rankZones(ctx, req, opts)
	if err == nil && req.ProvisioningModel == provisioningModelSpot && len(out) == 0 {
		// Each empty Spot ranking is a fallback (auto capacity re-ranks
		// standard) or an outright failure (explicit spot) at the caller.
		r.metrics.spotRankEmpty(ctx)
	}
	return out, err
}

func (r *ZoneRanker) rankZones(ctx context.Context, req gcecatalog.ValidationRequest, opts gcecatalog.RankOptions) ([]gcecatalog.ZoneCandidate, error) {
	enabled, err := r.zones.ListEnabledZones(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing enabled zones: %w", err)
	}
	zones := r.resolveZones(enabled)
	pin, err := r.policyString(ctx, models.ConfigKeyZonePin)
	if err != nil {
		return nil, err
	}
	mt, err := r.rankingShape(ctx, req.MachineType)
	if err != nil {
		return nil, err
	}
	spot := req.ProvisioningModel == provisioningModelSpot

	survivors, err := r.feasibleZones(ctx, feasibility{
		zones:       zones,
		machineType: mt.ID,
		gpuType:     attachedAccelerator(req, mt),
		family:      machineFamilyOf(mt),
		pin:         pin,
		exclude:     opts.ExcludeZone,
		demand:      r.bindingDemand(req, mt),
		spot:        spot,
	})
	if err != nil {
		return nil, err
	}
	if len(survivors) == 0 {
		return nil, nil
	}
	if spot {
		survivors = r.applySpotAdvice(ctx, survivors, mt.ID, opts)
		if len(survivors) == 0 {
			return nil, nil
		}
	}

	r.applyGeo(survivors, opts.PreferredRegion)
	ranked := scoreCandidates(survivors, opts, spot)
	if spot {
		r.metrics.spotRanked(ctx, survivors, ranked)
	}
	return ranked, nil
}

// applySpotAdvice folds fresh capacity advice into a Spot ranking: cells
// whose estimated-uptime tier undershoots the workload's minimum are filtered
// out (only when a fresh cell says so, since absent or stale advice never
// filters), and surviving candidates carry their obtainability for scoring.
// Everything degrades to the quota/health signals with a reason note.
func (r *ZoneRanker) applySpotAdvice(ctx context.Context, candidates []*rankedZone, machineType string, opts gcecatalog.RankOptions) []*rankedZone {
	if r.spotAdvice == nil {
		return candidates
	}
	cells, err := r.spotAdvice.ListCells(ctx)
	if err != nil {
		r.logger.Warn("spot advice unavailable; ranking without it", slog.Any("error", err))
		for _, c := range candidates {
			c.notes = append(c.notes, "spot advice unavailable")
		}
		return candidates
	}
	staleAfter := r.config.SpotAdviceStaleThreshold
	if staleAfter <= 0 {
		staleAfter = defaultSpotAdviceStale
	}
	byZone := make(map[string]*gcecatalog.SpotAdvice, len(cells))
	for _, cell := range cells {
		if cell.MachineType == machineType && time.Since(cell.SyncedAt) <= staleAfter {
			byZone[cell.Zone] = cell
		}
	}

	kept := make([]*rankedZone, 0, len(candidates))
	for _, c := range candidates {
		cell := byZone[c.zone.Zone]
		if cell == nil {
			c.notes = append(c.notes, "no fresh spot advice; obtainability scored neutrally")
			kept = append(kept, c)
			continue
		}
		if opts.SpotMinUptimeSeconds > 0 && cell.UptimeTierSeconds > 0 &&
			cell.UptimeTierSeconds < opts.SpotMinUptimeSeconds {
			// The filtered zone drops out of the resolution entirely, so this
			// log line is the only place the "why" survives.
			r.logger.Info("zone filtered out of Spot ranking by estimated uptime",
				slog.String(attrZone, c.zone.Zone),
				slog.String("machine_type", machineType),
				slog.Int64("uptime_tier_s", cell.UptimeTierSeconds),
				slog.Int64("min_uptime_s", opts.SpotMinUptimeSeconds))
			continue
		}
		c.obtainRaw = cell.Obtainability
		c.obtainKnown = true
		c.uptimeTier = cell.UptimeTierSeconds
		kept = append(kept, c)
	}
	return kept
}

// attachedAccelerator is the accelerator a zone has to stock separately for this
// shape, which is none at all for an integrated machine type, whose GPU ships
// welded to the machine. The machine-type availability check already answers the
// question there, and asking zone_accelerators a second time can only disagree
// with itself: an absent row reads as "not available", so a g2 host whose L4 rows
// have not synced would rank zero zones and fail over nowhere.
//
// It exists because the host row now STATES an integrated host's accelerator, and
// the failover and alternative-zone paths build their request from that row. Read
// naively, the stamp would have introduced a hard zone filter that no create ever
// applied before it. The quota side of this ranking already derives the same fact
// the same way (see bindingDemand).
func attachedAccelerator(req gcecatalog.ValidationRequest, mt *machinetype.MachineType) string {
	if mt.GPUMode == GPUModeIntegrated {
		return ""
	}
	return req.GPUType
}

// feasibleZones answers "can I get this shape in this zone" once, from every
// source that can say no: what Compute Engine structurally offers, whether the
// region has quota headroom, and what CCO empirically failed to get. Each is a
// different kind of evidence for the same question, and answering it in one
// place is what keeps the fail-open rule from having to be re-stated, and kept
// consistent by hand, at three filters.
//
// The order matters. Quota is judged on the structural survivors, and the
// cooldown drop runs last so that "every candidate is cooled, so ignore
// cooldowns" is decided over the zones a create could otherwise have used.
// Candidates come out zone-name-sorted so everything downstream is
// deterministic.
func (r *ZoneRanker) feasibleZones(ctx context.Context, q feasibility) ([]*rankedZone, error) {
	candidates := make([]*rankedZone, 0, len(q.zones))
	var excluded []zoneExclusion
	for _, z := range q.zones {
		if reason := r.structurallyInfeasible(z, q); reason != "" {
			excluded = append(excluded, zoneExclusion{zone: z.Zone, reason: reason})
			continue
		}
		candidates = append(candidates, &rankedZone{zone: z})
	}
	slices.SortFunc(candidates, func(a, b *rankedZone) int {
		return strings.Compare(a.zone.Zone, b.zone.Zone)
	})

	candidates, quotaExcluded, err := r.filterByQuota(ctx, candidates, q.demand)
	if err != nil {
		return nil, err
	}
	excluded = append(excluded, quotaExcluded...)

	r.applyZoneHealth(ctx, candidates, q.family, q.demand, q.spot)
	candidates, cooledOut := dropCooled(candidates)
	excluded = append(excluded, cooledOut...)

	if len(excluded) > 0 {
		// An excluded zone drops out of the resolution entirely, so this line
		// is the only place its "why" survives.
		reasons := make([]string, 0, len(excluded))
		for _, e := range excluded {
			reasons = append(reasons, e.zone+": "+e.reason)
		}
		r.logger.Info("zones excluded from ranking",
			slog.String(attrMachineType, q.machineType),
			slog.Any("excluded", reasons))
	}
	return candidates, nil
}

// structurallyInfeasible reports why a zone cannot host this shape at all, or
// "" when nothing rules it out.
//
// The catalog may remove a zone only when it positively knows the zone does
// not offer the shape. An unknown cell (no snapshot yet, or a scope Compute
// Engine did not report) keeps the zone: being wrong permissively costs one
// doomed create, a clear GCE error and a zone_health cell that stops it
// repeating, while being wrong restrictively leaves the fleet unable to
// provision with nothing to explain it.
func (r *ZoneRanker) structurallyInfeasible(z *gcecatalog.Zone, q feasibility) string {
	switch {
	case z.Zone == q.exclude:
		return "excluded by the caller"
	case q.pin != "" && z.Zone != q.pin:
		return "placement is pinned to " + q.pin
	case !z.IsAvailable():
		return "zone is down"
	}
	if a := r.shapeAvailability(z.Zone, q.machineType); a.Excludes() {
		return fmt.Sprintf("%s %s here", q.machineType, a)
	}
	if q.gpuType != "" {
		if a := r.acceleratorAvailability(z.Zone, q.gpuType); a.Excludes() {
			return fmt.Sprintf("%s %s here", q.gpuType, a)
		}
	}
	return ""
}

// resolveZones turns the operator's enabled names into the zones ranking
// reasons about, stamping each with what Compute Engine last said. A zone the
// snapshot is silent about carries no status, which reads as available. The
// enabled set is the operator's decision, and absence of knowledge never
// removes a candidate.
func (r *ZoneRanker) resolveZones(names []string) []*gcecatalog.Zone {
	universe := r.zoneUniverse()
	zones := make([]*gcecatalog.Zone, 0, len(names))
	for _, name := range names {
		zones = append(zones, &gcecatalog.Zone{
			Region: gcecatalog.RegionFromZone(name),
			Zone:   name,
			Status: universe[name],
		})
	}
	return zones
}

// zoneUniverse is every zone Compute Engine offers the project, or empty when
// no snapshot has landed.
func (r *ZoneRanker) zoneUniverse() map[string]gcecatalog.ZoneStatus {
	if r.shapes == nil {
		return nil
	}
	return r.shapes.Zones()
}

// shapeAvailability reads one machine-type cell of the Compute Engine
// snapshot. An unwired snapshot answers unknown, never "not offered".
func (r *ZoneRanker) shapeAvailability(zone, machineType string) gcecatalog.Availability {
	if r.shapes == nil {
		return gcecatalog.AvailabilityUnknown
	}
	return r.shapes.MachineTypeAvailability(zone, machineType)
}

// catalogReading reports how old the Compute Engine snapshot is, and whether
// there is one to age at all.
func (r *ZoneRanker) catalogReading() (time.Duration, bool) {
	if r.shapes == nil {
		return 0, false
	}
	builtAt := r.shapes.BuiltAt()
	if builtAt.IsZero() {
		return 0, false
	}
	return time.Since(builtAt), true
}

// acceleratorAvailability reads one accelerator cell, same rule.
func (r *ZoneRanker) acceleratorAvailability(zone, acceleratorType string) gcecatalog.Availability {
	if r.shapes == nil {
		return gcecatalog.AvailabilityUnknown
	}
	return r.shapes.AcceleratorAvailability(zone, acceleratorType)
}

// rankingShape resolves the machine type the ranking judges demand for: the
// requested type, or the catalog default when the request names none. A shape
// that cannot be resolved fails the ranking outright, because guessing a shape
// here would rank zones for capacity nobody asked about.
func (r *ZoneRanker) rankingShape(ctx context.Context, requested string) (*machinetype.MachineType, error) {
	if requested == "" {
		mt, err := r.machineTypes.GetDefault(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolving the default machine type for ranking: %w", err)
		}
		if mt == nil {
			return nil, errors.New("no default machine type configured; cannot rank zones for a shapeless request")
		}
		return mt, nil
	}
	mt, err := r.machineTypes.Get(ctx, requested)
	if err != nil {
		return nil, fmt.Errorf("resolving machine type %q for ranking: %w", requested, err)
	}
	if mt == nil {
		return nil, fmt.Errorf("machine type %q is not in the catalog", requested)
	}
	return mt, nil
}

// machineFamilyOf returns the family for zone_health matching.
func machineFamilyOf(mt *machinetype.MachineType) string {
	if mt.Family != "" {
		return mt.Family
	}
	return machinetype.FamilyOf(mt.ID)
}

// applyZoneHealth stamps each candidate's cooldown state and failure-decay
// term from the zone_health cells. A cell applies to a candidate when the
// machine family matches and either it is the candidate's own zone (zonal
// capacity/availability; preemption cells only for Spot specs) or a regional
// quota cell in the candidate's region whose metric the spec actually demands.
func (r *ZoneRanker) applyZoneHealth(ctx context.Context, candidates []*rankedZone, family string, demand []metricDemand, spot bool) {
	for _, c := range candidates {
		c.healthRaw = 1
	}
	if r.zoneHealth == nil {
		return
	}
	cells, err := r.zoneHealth.ListCells(ctx)
	if err != nil {
		// Advisory memory: ranking proceeds without it rather than blocking
		// placement on the health table.
		r.logger.Warn("zone health unavailable; ranking without cooldowns", slog.Any("error", err))
		for _, c := range candidates {
			c.notes = append(c.notes, "zone health unavailable; cooldowns not applied")
		}
		return
	}

	demanded := make(map[string]bool, len(demand)+2)
	for _, d := range demand {
		demanded[d.healthMetric] = true
	}
	// Instance quota binds every host, each of which is one instance slot, and
	// the per-host demand model does not price it, so its regional cooldown repels
	// all specs. Without this, an instance-quota exhaustion would cool only the
	// zone it surfaced in and failover would bounce through the region's
	// sibling zones, burning ladder attempts on a regional limit. The disk
	// axes need no such entry: the demand model prices whichever pool the
	// shape's own boot disk fills, and a cooldown on the other one says
	// nothing about it.
	demanded[gcecatalog.ZoneMetricQuotaInstance] = true
	// Preemption-storm cells repel only Spot specs.
	demanded[gcecatalog.ZoneMetricPreemption] = spot
	now := time.Now()
	for _, c := range candidates {
		for _, cell := range cells {
			if cell.MachineFamily != family || !cellApplies(cell, c.zone, demanded) {
				continue
			}
			c.failures = max(c.failures, cell.ConsecutiveFailures)
			if cell.InCooldown(now) {
				c.cooled = true
			}
		}
		c.healthRaw = 1 / float64(1+c.failures)
	}
}

// cellApplies reports whether a zone_health cell repels a candidate zone.
func cellApplies(cell *gcecatalog.ZoneHealth, z *gcecatalog.Zone, demanded map[string]bool) bool {
	if cell.BindingMetric == gcecatalog.ZoneMetricPreemption {
		// Zonal but Spot-scoped: a reclaim storm says nothing about the
		// zone's on-demand capacity.
		return cell.Zone == z.Zone && demanded[gcecatalog.ZoneMetricPreemption]
	}
	if cell.Zone == z.Zone {
		return true
	}
	return gcecatalog.IsRegionalMetric(cell.BindingMetric) &&
		gcecatalog.RegionFromZone(cell.Zone) == z.Region &&
		demanded[cell.BindingMetric]
}

// dropCooled removes candidates in active cooldown, unless every candidate
// is cooled, in which case cooldowns are ignored (they must never make the
// fleet unprovisionable) and the failure-decay term does the steering. It
// returns the survivors and the zones it dropped.
func dropCooled(candidates []*rankedZone) ([]*rankedZone, []zoneExclusion) {
	kept := make([]*rankedZone, 0, len(candidates))
	var dropped []zoneExclusion
	for _, c := range candidates {
		if c.cooled {
			dropped = append(dropped, zoneExclusion{
				zone:   c.zone.Zone,
				reason: fmt.Sprintf("in cooldown after %d recent failures", c.failures),
			})
			continue
		}
		kept = append(kept, c)
	}
	if len(kept) > 0 {
		return kept, dropped
	}
	for _, c := range candidates {
		c.notes = append(c.notes, "every candidate zone is in cooldown; cooldowns ignored")
	}
	return candidates, nil
}

// applyGeo stamps the proximity term when a preferred region is set. Regions
// missing from the coordinate table stay geoKnown=false and normalize to the
// neutral midpoint.
func (r *ZoneRanker) applyGeo(candidates []*rankedZone, preferredRegion string) {
	if preferredRegion == "" {
		return
	}
	for _, c := range candidates {
		if dist, ok := regionDistanceKm(c.zone.Region, preferredRegion); ok {
			c.geoRaw = -dist // closer is better
			c.geoKnown = true
		} else {
			c.notes = append(c.notes, "region location unknown; proximity scored neutrally")
		}
	}
}

// policyString reads one system_config key; absence (or no policy reader)
// reads as empty.
func (r *ZoneRanker) policyString(ctx context.Context, key string) (string, error) {
	if r.policy == nil {
		return "", nil
	}
	cfg, err := r.policy.Get(ctx, key)
	if err != nil {
		if errors.Is(err, cspanner.ErrKeyNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("reading policy %q: %w", key, err)
	}
	return cfg.AsString(), nil
}
