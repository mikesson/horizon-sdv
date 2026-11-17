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
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype/machinetypetest"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

type fakeZoneHealth struct {
	cells []*gcecatalog.ZoneHealth
	err   error
}

func (f *fakeZoneHealth) RecordFailure(_ context.Context, zone, family, metric, reason string) (*gcecatalog.ZoneHealth, error) {
	cell := &gcecatalog.ZoneHealth{Zone: zone, MachineFamily: family, BindingMetric: metric, LastReason: reason}
	f.cells = append(f.cells, cell)
	return cell, nil
}

func (f *fakeZoneHealth) ClearCell(context.Context, string, string) error { return nil }

func (f *fakeZoneHealth) ListCells(context.Context) ([]*gcecatalog.ZoneHealth, error) {
	return f.cells, f.err
}

func (f *fakeZoneHealth) ListCapacityCells(context.Context, []string) ([]*gcecatalog.ZoneHealth, error) {
	return f.cells, f.err
}

type fakePolicy struct{ values map[string]string }

func (f *fakePolicy) Get(_ context.Context, key string) (*models.SystemConfig, error) {
	v, ok := f.values[key]
	if !ok {
		return nil, cspanner.ErrKeyNotFound
	}
	return &models.SystemConfig{Key: key, Value: v}, nil
}

type fakeSpotAdvice struct {
	cells []*gcecatalog.SpotAdvice
	err   error
}

func (f *fakeSpotAdvice) UpsertCells(_ context.Context, cells []*gcecatalog.SpotAdvice) error {
	f.cells = append(f.cells, cells...)
	return nil
}

func (f *fakeSpotAdvice) ListCells(context.Context) ([]*gcecatalog.SpotAdvice, error) {
	return f.cells, f.err
}

// rankingFixture builds a catalog with n1-standard-8 available in three
// fresh-quota zones across two regions.
type rankingFixture struct {
	catalog    *mockCatalogRepo
	types      *machinetypetest.FakeRepo
	zoneHealth *fakeZoneHealth
	spotAdvice *fakeSpotAdvice
	policy     *fakePolicy
}

func newRankingFixture() *rankingFixture {
	f := &rankingFixture{
		catalog:    newMockCatalogRepo(),
		types:      machinetypetest.NewFakeRepo(),
		zoneHealth: &fakeZoneHealth{},
		spotAdvice: &fakeSpotAdvice{},
		policy:     &fakePolicy{values: map[string]string{}},
	}
	f.types.Types["n1-standard-8"] = &machinetype.MachineType{
		ID: "n1-standard-8", Family: "n1", VCPUs: 8,
		IsEnabled: true, IsDefault: true, GPUMode: GPUModeAttachable, MaxGPUs: 4,
	}
	for _, zone := range []string{"europe-west4-a", "europe-west4-b", "us-central1-a"} {
		f.addZone(zone)
	}
	f.setCPUQuota("europe-west4", 100)
	f.setCPUQuota("us-central1", 100)
	return f
}

func (f *rankingFixture) addZone(zone string) {
	f.catalog.enableZone(zone, gcecatalog.ZoneStatusUp)
	f.catalog.machineTypes[zone] = map[string]bool{"n1-standard-8": true}
	f.catalog.accelerators[zone] = map[string]int64{"nvidia-tesla-t4": 4}
}

// setCPUQuota records a fresh 500-limit CPU quota row with the given usage.
func (f *rankingFixture) setCPUQuota(region string, usage int64) {
	now := time.Now()
	f.catalog.quotas[region+":CPUS"] = &gcecatalog.RegionalQuota{
		Region: region, QuotaType: gcecatalog.QuotaTypeCPUs,
		LimitValue: 500, UsageValue: usage, LastSyncAt: &now,
	}
}

func (f *rankingFixture) validator(t *testing.T) *CatalogValidator {
	t.Helper()
	return NewCatalogValidator(f.catalog, f.types, ValidatorConfig{}, slog.Default(),
		WithZoneHealthStore(f.zoneHealth), WithPolicyReader(f.policy),
		WithSpotAdviceStore(f.spotAdvice), WithShapeCatalog(f.catalog))
}

// addSpotAdvice records a fresh advice cell for n1-standard-8 in the zone.
func (f *rankingFixture) addSpotAdvice(zone string, obtainability float64, uptimeTier int64) {
	f.spotAdvice.cells = append(f.spotAdvice.cells, &gcecatalog.SpotAdvice{
		Zone: zone, MachineType: "n1-standard-8",
		Obtainability: obtainability, UptimeTierSeconds: uptimeTier,
		SyncedAt: time.Now(),
	})
}

func n1Request() gcecatalog.ValidationRequest {
	return gcecatalog.ValidationRequest{MachineType: "n1-standard-8"}
}

func n1SpotRequest() gcecatalog.ValidationRequest {
	return gcecatalog.ValidationRequest{MachineType: "n1-standard-8", ProvisioningModel: "SPOT"}
}

func zoneNames(candidates []gcecatalog.ZoneCandidate) []string {
	names := make([]string, 0, len(candidates))
	for _, c := range candidates {
		names = append(names, c.Zone)
	}
	return names
}

func TestRankZonesDeterministicAndComplete(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	v := f.validator(t)
	ctx := t.Context()

	first, err := v.RankZones(ctx, n1Request(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	require.Len(t, first, 3)

	// Equal headroom and health everywhere: the zone-name tie-break orders.
	assert.Equal(t, []string{"europe-west4-a", "europe-west4-b", "us-central1-a"}, zoneNames(first))

	second, err := v.RankZones(ctx, n1Request(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	assert.Equal(t, first, second, "identical inputs must rank identically")

	for _, c := range first {
		assert.NotEmpty(t, c.Reasons, "every candidate carries its ranking trace")
	}
}

func TestRankZonesHeadroomPrefersIdleRegion(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	f.setCPUQuota("europe-west4", 450) // 10% free
	f.setCPUQuota("us-central1", 50)   // 90% free
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(), n1Request(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, candidates)
	assert.Equal(t, "us-central1-a", candidates[0].Zone,
		"the idle region must outrank the nearly-full one")
}

func TestRankZonesQuotaHeadroomFiltersRegion(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	// europe-west4 has 8 CPUs free of 500: an 8-vCPU host cannot fit above
	// the 10% reserve; every europe-west4 zone must drop.
	f.setCPUQuota("europe-west4", 492)
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(), n1Request(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"us-central1-a"}, zoneNames(candidates))
}

func TestRankZonesStaleQuotaPassesAdvisorily(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	// Exhausted quota, but stale beyond the trust threshold, so the region
	// passes with a note instead of being filtered (a dead sync ticker must
	// not block provisioning).
	old := time.Now().Add(-2 * time.Hour)
	f.catalog.quotas["europe-west4:CPUS"].UsageValue = 500
	f.catalog.quotas["europe-west4:CPUS"].LastSyncAt = &old
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(), n1Request(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	require.Len(t, candidates, 3)
	var noted bool
	for _, c := range candidates {
		if c.Zone == "europe-west4-a" {
			noted = strings.Contains(strings.Join(c.Reasons, "; "), "stale")
		}
	}
	assert.True(t, noted, "the stale-quota degradation must appear in the trace")
}

func TestRankZonesCooldownFiltersAndAllCooledFallsBack(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	cooled := time.Now().Add(10 * time.Minute)
	f.zoneHealth.cells = []*gcecatalog.ZoneHealth{{
		Zone: "europe-west4-a", MachineFamily: "n1",
		BindingMetric:       gcecatalog.ZoneMetricCapacity,
		ConsecutiveFailures: 2, CooldownUntil: &cooled,
	}}
	v := f.validator(t)
	ctx := t.Context()

	candidates, err := v.RankZones(ctx, n1Request(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"europe-west4-b", "us-central1-a"}, zoneNames(candidates),
		"a cooled zone is filtered while alternatives exist")

	// Cool every candidate: cooldowns must be ignored rather than making the
	// fleet unprovisionable, and the least-failed zones rank first.
	for _, zone := range []string{"europe-west4-b", "us-central1-a"} {
		f.zoneHealth.cells = append(f.zoneHealth.cells, &gcecatalog.ZoneHealth{
			Zone: zone, MachineFamily: "n1",
			BindingMetric:       gcecatalog.ZoneMetricCapacity,
			ConsecutiveFailures: 5, CooldownUntil: &cooled,
		})
	}
	candidates, err = v.RankZones(ctx, n1Request(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	require.Len(t, candidates, 3)
	assert.Equal(t, "europe-west4-a", candidates[0].Zone,
		"with everything cooled, the least-failed zone ranks first")
	assert.Contains(t, strings.Join(candidates[0].Reasons, "; "), "cooldowns ignored")
}

func TestRankZonesRegionalQuotaCooldownFansOut(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	cooled := time.Now().Add(10 * time.Minute)
	// A CPU-quota cooldown recorded in europe-west4-a cools the whole region
	// for the family (quota is regional), so europe-west4-b drops too.
	f.zoneHealth.cells = []*gcecatalog.ZoneHealth{{
		Zone: "europe-west4-a", MachineFamily: "n1",
		BindingMetric:       gcecatalog.ZoneMetricQuotaCPU,
		ConsecutiveFailures: 1, CooldownUntil: &cooled,
	}}
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(), n1Request(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"us-central1-a"}, zoneNames(candidates))
}

func TestRankZonesInstanceQuotaCooldownFansOut(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	cooled := time.Now().Add(10 * time.Minute)
	// Instance quota binds every host even though the per-host demand model
	// does not price it: the regional cooldown must repel the sibling zone,
	// or failover would bounce through the region on a regional limit.
	f.zoneHealth.cells = []*gcecatalog.ZoneHealth{{
		Zone: "europe-west4-a", MachineFamily: "n1",
		BindingMetric:       gcecatalog.ZoneMetricQuotaInstance,
		ConsecutiveFailures: 1, CooldownUntil: &cooled,
	}}
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(), n1Request(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"us-central1-a"}, zoneNames(candidates))
}

func TestRankZonesZonalCapacityCooldownStaysZonal(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	cooled := time.Now().Add(10 * time.Minute)
	f.zoneHealth.cells = []*gcecatalog.ZoneHealth{{
		Zone: "europe-west4-a", MachineFamily: "n1",
		BindingMetric:       gcecatalog.ZoneMetricCapacity,
		ConsecutiveFailures: 1, CooldownUntil: &cooled,
	}}
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(), n1Request(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"europe-west4-b", "us-central1-a"}, zoneNames(candidates),
		"a stockout cools only its own zone, not the sibling in the region")
}

func TestRankZonesCooldownIgnoresOtherFamilies(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	cooled := time.Now().Add(10 * time.Minute)
	f.zoneHealth.cells = []*gcecatalog.ZoneHealth{{
		Zone: "europe-west4-a", MachineFamily: "g2",
		BindingMetric:       gcecatalog.ZoneMetricCapacity,
		ConsecutiveFailures: 3, CooldownUntil: &cooled,
	}}
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(), n1Request(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	assert.Len(t, candidates, 3, "a g2 cooldown says nothing about an n1 spec")
}

// --- spot capacity advice ---

// Fresh advice steers a Spot ranking: obtainability outranks the flat quota
// picture, the trace names the term, and a standard ranking of the same shape
// ignores the advice entirely.
func TestRankZonesSpotAdviceRanksByObtainability(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	// Quota is identical across regions; advice is the only differentiator.
	f.addSpotAdvice("europe-west4-a", 0.2, gcecatalog.SpotUptimeTierHour)
	f.addSpotAdvice("europe-west4-b", 0.9, gcecatalog.SpotUptimeTierHour)
	f.addSpotAdvice("us-central1-a", 0.5, gcecatalog.SpotUptimeTier10Min)
	v := f.validator(t)
	ctx := t.Context()

	candidates, err := v.RankZones(ctx, n1SpotRequest(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	require.Len(t, candidates, 3)
	assert.Equal(t, "europe-west4-b", candidates[0].Zone, "highest obtainability ranks first")
	assert.Contains(t, strings.Join(candidates[0].Reasons, "; "), "spot obtainability",
		"the trace carries the advice term")
	assert.Contains(t, strings.Join(candidates[0].Reasons, "; "), "uptime tier 60m")

	standard, err := v.RankZones(ctx, n1Request(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	require.Len(t, standard, 3)
	assert.NotContains(t, strings.Join(standard[0].Reasons, "; "), "spot obtainability",
		"a standard ranking never consults spot advice")
}

// The min-uptime filter drops only zones whose FRESH advice undershoots it:
// zones without advice stay (degrade to the quota/health signals, with a
// note), and stale advice neither filters nor scores.
func TestRankZonesSpotAdviceUptimeFilter(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	f.addSpotAdvice("europe-west4-a", 0.9, gcecatalog.SpotUptimeTierMinute)
	f.spotAdvice.cells = append(f.spotAdvice.cells, &gcecatalog.SpotAdvice{
		Zone: "europe-west4-b", MachineType: "n1-standard-8",
		Obtainability: 0.1, UptimeTierSeconds: gcecatalog.SpotUptimeTierMinute,
		SyncedAt: time.Now().Add(-2 * time.Hour), // stale: must not filter
	})
	// us-central1-a has no advice at all: must not filter.
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(), n1SpotRequest(),
		gcecatalog.RankOptions{SpotMinUptimeSeconds: 600})
	require.NoError(t, err)
	assert.Equal(t, []string{"europe-west4-b", "us-central1-a"}, zoneNames(candidates),
		"only the fresh below-tier zone is filtered")
	allReasons := make([]string, 0, len(candidates))
	for _, c := range candidates {
		allReasons = append(allReasons, c.Reasons...)
	}
	assert.Contains(t, strings.Join(allReasons, "; "), "no fresh spot advice",
		"unadvised candidates carry the degradation note")
}

// Every zone below the uptime tier: the Spot ranking comes back empty so the
// caller can fall back to standard capacity (auto) or fail outright (spot).
func TestRankZonesSpotAdviceAllFilteredIsEmpty(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	for _, zone := range []string{"europe-west4-a", "europe-west4-b", "us-central1-a"} {
		f.addSpotAdvice(zone, 0.9, gcecatalog.SpotUptimeTierMinute)
	}
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(), n1SpotRequest(),
		gcecatalog.RankOptions{SpotMinUptimeSeconds: 3600})
	require.NoError(t, err)
	assert.Empty(t, candidates, "an all-filtered Spot ranking is empty, not a guess")
}

// An erroring advice store degrades a Spot ranking to the quota/health
// signals with a note. It must never block placement.
func TestRankZonesSpotAdviceUnavailableDegrades(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	f.spotAdvice.err = assert.AnError
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(), n1SpotRequest(),
		gcecatalog.RankOptions{SpotMinUptimeSeconds: 3600})
	require.NoError(t, err)
	require.Len(t, candidates, 3)
	assert.Contains(t, strings.Join(candidates[0].Reasons, "; "), "spot advice unavailable")
}

// A preemption-storm cell repels only Spot specs: standard capacity in the
// zone is unaffected, and the cell never fans out regionally.
func TestRankZonesPreemptionCooldownIsSpotScoped(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	cooled := time.Now().Add(10 * time.Minute)
	f.zoneHealth.cells = []*gcecatalog.ZoneHealth{{
		Zone: "europe-west4-a", MachineFamily: "n1",
		BindingMetric:       gcecatalog.ZoneMetricPreemption,
		ConsecutiveFailures: 1, CooldownUntil: &cooled,
	}}
	v := f.validator(t)
	ctx := t.Context()

	spotReq := n1Request()
	spotReq.ProvisioningModel = "SPOT"
	candidates, err := v.RankZones(ctx, spotReq, gcecatalog.RankOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"europe-west4-b", "us-central1-a"}, zoneNames(candidates),
		"a reclaim storm repels Spot placement from its zone only (no regional fan-out)")

	candidates, err = v.RankZones(ctx, n1Request(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	assert.Len(t, candidates, 3,
		"a reclaim storm says nothing about standard capacity in the zone")
}

func TestRankZonesZonePinPolicy(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	f.policy.values[models.ConfigKeyZonePin] = "europe-west4-b"
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(), n1Request(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"europe-west4-b"}, zoneNames(candidates))
}

func TestRankZonesGeoPreference(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	v := f.validator(t)
	ctx := t.Context()

	// Geo-dominant (interactive): the hinted region's zones must win even
	// with equal capacity everywhere.
	candidates, err := v.RankZones(ctx, n1Request(), gcecatalog.RankOptions{
		PreferredRegion: "us-central1", GeoDominant: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, candidates)
	assert.Equal(t, "us-central1-a", candidates[0].Zone)

	// As a tie-break (headless): still first among capacity-equal zones.
	candidates, err = v.RankZones(ctx, n1Request(), gcecatalog.RankOptions{
		PreferredRegion: "us-central1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, candidates)
	assert.Equal(t, "us-central1-a", candidates[0].Zone)
}

func TestRankZonesExcludeZone(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(), n1Request(),
		gcecatalog.RankOptions{ExcludeZone: "europe-west4-a"})
	require.NoError(t, err)
	assert.Equal(t, []string{"europe-west4-b", "us-central1-a"}, zoneNames(candidates))
}

// An integrated shape's GPU is welded to the machine, so the machine-type
// availability check is the whole question and zone_accelerators must not be
// asked a second time.
//
// This is not theoretical tidiness. The host row now STATES an integrated host's
// accelerator, and the failover and alternative-zone paths build their ranking
// request from that row, so read naively that stamp turns an absent
// zone_accelerators row into a hard filter no create ever applied before it, and
// a g2 host whose L4 rows have not synced fails over nowhere at all. The fixture
// below stocks T4 and NOT L4, which is exactly that state.
func TestRankZonesIntegratedGPUNeedsNoSeparateAcceleratorRow(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	f.types.Types["g2-standard-8"] = &machinetype.MachineType{
		ID: "g2-standard-8", Family: "g2", VCPUs: 8, IsEnabled: true,
		GPUMode: GPUModeIntegrated, IntegratedGPUType: "nvidia-l4", IntegratedGPUCount: 1,
	}
	for zone := range f.catalog.machineTypes {
		f.catalog.machineTypes[zone]["g2-standard-8"] = true
	}
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(), gcecatalog.ValidationRequest{
		MachineType: "g2-standard-8", GPUType: "nvidia-l4", GPUCount: 1,
	}, gcecatalog.RankOptions{})
	require.NoError(t, err)
	assert.NotEmpty(t, candidates,
		"a stamped g2 host ranked no zones: the integrated accelerator was filtered on separately")

	// The converse, so this cannot pass by ignoring the accelerator entirely: an
	// ATTACHABLE shape still has to find its GPU stocked in the zone.
	missing, err := v.RankZones(t.Context(), gcecatalog.ValidationRequest{
		MachineType: "n1-standard-8", GPUType: "nvidia-l4", GPUCount: 1,
	}, gcecatalog.RankOptions{})
	require.NoError(t, err)
	assert.Empty(t, missing, "an attached GPU the zones do not stock must still filter them out")
}

// Ranking actually consults the GCE snapshot: a zone Compute Engine reported
// without the shape is not a placement candidate, and the exclusion says so.
func TestRankZonesDropsAZoneThatDoesNotOfferTheShape(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	// europe-west4-b was reported, and n1-standard-8 was not in it.
	f.catalog.machineTypes["europe-west4-b"] = map[string]bool{"c4a-standard-8-metal": true}
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(), n1Request(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"europe-west4-a", "us-central1-a"}, zoneNames(candidates),
		"a zone that positively does not offer the shape must not be a candidate")

	// And the same when the zone offers nothing at all. Reported and empty is
	// still a positive answer.
	f.catalog.machineTypes["europe-west4-b"] = map[string]bool{}
	candidates, err = v.RankZones(t.Context(), n1Request(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"europe-west4-a", "us-central1-a"}, zoneNames(candidates))
}

// Absence of knowledge never removes a zone. An empty snapshot (nothing
// fetched yet, or a scope Compute Engine did not report) must rank every
// enabled zone, because a wrongly-removed zone leaves the fleet unable to
// provision with nothing to explain it, while a wrongly-kept one costs one
// create attempt that zone_health then remembers.
func TestRankZonesAbsenceNeverExcludes(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	all := zoneNames(mustRank(t, f.validator(t), n1Request()))
	require.Len(t, all, 3)

	// Cold start: no snapshot at all.
	cold := newRankingFixture()
	clear(cold.catalog.machineTypes)
	clear(cold.catalog.accelerators)
	assert.Equal(t, all, zoneNames(mustRank(t, cold.validator(t), n1Request())),
		"a cold start must still place: an unbuilt snapshot excludes nothing")

	// One scope Compute Engine failed to report, the rest answered.
	partial := newRankingFixture()
	delete(partial.catalog.machineTypes, "europe-west4-b")
	assert.Equal(t, all, zoneNames(mustRank(t, partial.validator(t), n1Request())),
		"a zone the snapshot is silent about must stay a candidate")

	// The same rule for a separately attached accelerator.
	gpu := newRankingFixture()
	clear(gpu.catalog.accelerators)
	candidates := mustRank(t, gpu.validator(t), gcecatalog.ValidationRequest{
		MachineType: "n1-standard-8", GPUType: "nvidia-tesla-t4", GPUCount: 1,
	})
	assert.Equal(t, all, zoneNames(candidates),
		"an unknown accelerator cell must not filter a zone out")
}

func mustRank(t *testing.T, v *CatalogValidator, req gcecatalog.ValidationRequest) []gcecatalog.ZoneCandidate {
	t.Helper()
	candidates, err := v.RankZones(t.Context(), req, gcecatalog.RankOptions{})
	require.NoError(t, err)
	return candidates
}

// A zone-listing outage mid-ranking must surface as an error, never as an
// empty candidate list. "No zone has capacity" and "the database is down"
// demand opposite reactions.
func TestRankZonesZoneListingErrorFails(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	f.catalog.enabledZonesErr = context.DeadlineExceeded
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(), n1Request(), gcecatalog.RankOptions{})
	require.Error(t, err)
	assert.Nil(t, candidates)
}

func TestRankZonesZoneHealthUnavailableDegrades(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	f.zoneHealth.err = context.DeadlineExceeded
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(), n1Request(), gcecatalog.RankOptions{})
	require.NoError(t, err, "an unavailable health table must not block ranking")
	require.Len(t, candidates, 3)
	assert.Contains(t, strings.Join(candidates[0].Reasons, "; "), "zone health unavailable")
}

func TestRankZonesSpotDemandsPreemptibleQuota(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	now := time.Now()
	// Preemptible CPU quota exhausted in europe-west4; standard quota is
	// plentiful. A Spot spec must judge the preemptible pool.
	f.catalog.quotas["europe-west4:PREEMPTIBLE_CPUS"] = &gcecatalog.RegionalQuota{
		Region: "europe-west4", QuotaType: gcecatalog.QuotaTypePreemptibleCPUs,
		LimitValue: 16, UsageValue: 16, LastSyncAt: &now,
	}
	f.catalog.quotas["us-central1:PREEMPTIBLE_CPUS"] = &gcecatalog.RegionalQuota{
		Region: "us-central1", QuotaType: gcecatalog.QuotaTypePreemptibleCPUs,
		LimitValue: 64, UsageValue: 0, LastSyncAt: &now,
	}
	v := f.validator(t)

	req := n1Request()
	req.ProvisioningModel = "SPOT"
	candidates, err := v.RankZones(t.Context(), req, gcecatalog.RankOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"us-central1-a"}, zoneNames(candidates))
}

func TestRankZonesSpotFallsBackToStandardQuota(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	// No PREEMPTIBLE_* rows synced: this project has no preemptible grant, so
	// its Spot VMs draw from the standard pools. An exhausted standard pool
	// must still filter the region, not pass advisorily as "unknown".
	f.setCPUQuota("europe-west4", 492) // 8 free of 500: below the reserve
	v := f.validator(t)

	req := n1Request()
	req.ProvisioningModel = "SPOT"
	candidates, err := v.RankZones(t.Context(), req, gcecatalog.RankOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"us-central1-a"}, zoneNames(candidates))
	assert.Contains(t, strings.Join(candidates[0].Reasons, "; "),
		"PREEMPTIBLE_CPUS not tracked in us-central1; judging CPUS",
		"the substitution must appear in the trace")
}

func TestNormalizeIsScaleInvariant(t *testing.T) {
	t.Parallel()
	base := []*rankedZone{
		{geoRaw: -100, geoKnown: true},
		{geoRaw: -400, geoKnown: true},
		{geoRaw: -250, geoKnown: true},
	}
	scaled := []*rankedZone{
		{geoRaw: -100000, geoKnown: true},
		{geoRaw: -400000, geoKnown: true},
		{geoRaw: -250000, geoKnown: true},
	}
	term := func(c *rankedZone) (float64, bool) { return c.geoRaw, c.geoKnown }
	assert.Equal(t, normalize(base, term), normalize(scaled, term),
		"a raw-unit change must not move any normalized value")
}

// End-to-end guard on normalize-before-weight: the weights sum to at most 1
// over 0-100 terms, so every candidate's score must stay within [0,100]
// whatever raw units the terms use. A future term scored outside normalize()
// (raw kilometers, raw fractions) blows past the bound and fails here.
func TestRankZonesScoresStayWithinNormalizedBounds(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	f.setCPUQuota("europe-west4", 450)
	f.setCPUQuota("us-central1", 50)
	v := f.validator(t)

	for _, opts := range []gcecatalog.RankOptions{
		{},
		{PreferredRegion: "us-central1"},
		{PreferredRegion: "us-central1", GeoDominant: true},
	} {
		candidates, err := v.RankZones(t.Context(), n1Request(), opts)
		require.NoError(t, err)
		require.NotEmpty(t, candidates)
		for _, c := range candidates {
			assert.GreaterOrEqual(t, c.Score, 0.0, "zone %s, opts %+v", c.Zone, opts)
			assert.LessOrEqual(t, c.Score, 100.0, "zone %s, opts %+v", c.Zone, opts)
		}
	}
}

func TestRankZonesValidateAlternativesAreRanked(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	// Make europe-west4 nearly full so the ranked alternatives prefer
	// us-central1. Catalog order would have said europe-west4-b first.
	f.setCPUQuota("europe-west4", 400)
	v := f.validator(t)

	result, err := v.Validate(t.Context(), gcecatalog.ValidationRequest{
		Zone: "europe-west4-z", MachineType: "n1-standard-8",
	})
	require.NoError(t, err)
	require.False(t, result.Valid)
	require.NotEmpty(t, result.AlternativeZones)
	assert.Equal(t, "us-central1-a", result.AlternativeZones[0])
}

// A limit-0 preemptible grant resolves to the standard pool in ranking too:
// the exhausted standard pool filters the region (a zero-limit row passing
// advisorily would hide the pool Spot actually draws from), and the roomy
// one places with the substitution in the trace.
func TestRankZonesSpotZeroLimitPreemptibleJudgesStandardPool(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	now := time.Now()
	zeroGrant := func(region string) *gcecatalog.RegionalQuota {
		return &gcecatalog.RegionalQuota{
			Region: region, QuotaType: gcecatalog.QuotaTypePreemptibleCPUs,
			LimitValue: 0, UsageValue: 0, LastSyncAt: &now,
		}
	}
	f.catalog.quotas["europe-west4:PREEMPTIBLE_CPUS"] = zeroGrant("europe-west4")
	f.catalog.quotas["us-central1:PREEMPTIBLE_CPUS"] = zeroGrant("us-central1")
	f.setCPUQuota("europe-west4", 492) // 8 free of 500: below the reserve
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(), n1SpotRequest(), gcecatalog.RankOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"us-central1-a"}, zoneNames(candidates))
	assert.Contains(t, strings.Join(candidates[0].Reasons, "; "),
		"PREEMPTIBLE_CPUS granted 0 in us-central1; judging CPUS",
		"the zero-limit substitution must appear in the trace")
}
