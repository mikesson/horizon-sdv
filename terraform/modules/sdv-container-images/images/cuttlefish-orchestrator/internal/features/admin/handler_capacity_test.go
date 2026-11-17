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

package adminfeature

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

type fakeZoneFamilyCounter struct {
	counts []host.ZoneFamilyCount
}

func (f *fakeZoneFamilyCounter) CountByZoneFamily(context.Context) ([]host.ZoneFamilyCount, error) {
	return f.counts, nil
}

type fakeMachineCatalog struct {
	rows []*machinetype.MachineType
	// unoffered is what the catalog says about the shapes it withholds, keyed by
	// machine type. It is the answer a lookup gets, and the reason it is a
	// lookup and not a row in the listing.
	unoffered map[string]*machinetype.UnofferedError
	reads     int
	lookups   int
}

func (f *fakeMachineCatalog) List(_ context.Context, _ machinetype.ListFilter) ([]*machinetype.MachineType, error) {
	f.reads++
	return f.rows, nil
}

func (f *fakeMachineCatalog) Get(_ context.Context, id string) (*machinetype.MachineType, error) {
	f.lookups++
	if unoffered, withheld := f.unoffered[id]; withheld {
		return nil, unoffered
	}
	for _, row := range f.rows {
		if row.ID == id {
			return row, nil
		}
	}
	return nil, machinetype.ErrNotFound
}

type fakePlacementPolicy struct{ margin int64 }

func (f *fakePlacementPolicy) GetQuotaMargin(context.Context) (int64, bool, error) {
	return f.margin, true, nil
}

// capacityFixture is one composed snapshot request: the collaborators a test
// varies, everything else defaulted to a single-zone fleet on n1.
type capacityFixture struct {
	enabled  []string
	statuses map[string]gcecatalog.ZoneStatus
	quotas   map[string][]*gcecatalog.RegionalQuota
	census   []host.ZoneFamilyCount
	cells    []*gcecatalog.ZoneHealth
	advice   []*gcecatalog.SpotAdvice
	shapes   []*machinetype.MachineType
	margin   int64
}

func defaultCapacityFixture() capacityFixture {
	return capacityFixture{
		enabled:  []string{"europe-west4-a"},
		statuses: map[string]gcecatalog.ZoneStatus{"europe-west4-a": gcecatalog.ZoneStatusUp},
		quotas: map[string][]*gcecatalog.RegionalQuota{
			"europe-west4": {{Region: "europe-west4", QuotaType: "CPUS", LimitValue: 400, UsageValue: 24}},
		},
		census: []host.ZoneFamilyCount{
			{Zone: "europe-west4-a", MachineFamily: "n1", Machines: 2, Arriving: 1, Devices: 6},
		},
		shapes: []*machinetype.MachineType{
			{ID: "n1-standard-8", Family: "n1", VCPUs: 8, BootDiskType: "pd-balanced", IsEnabled: true},
		},
		margin: 10,
	}
}

func (f capacityFixture) snapshot(t *testing.T) struct {
	Now     time.Time            `json:"now"`
	Fleet   CapacityFleetItem    `json:"fleet"`
	Regions []CapacityRegionItem `json:"regions"`
	Zones   []CapacityZoneItem   `json:"zones"`
	Pools   []CapacityPoolItem   `json:"pools"`
	Spot    SpotPanelItem        `json:"spot"`
	Margin  int64                `json:"reserve_margin_percent"`
} {
	t.Helper()
	catalog := &fakeZoneCatalog{enabled: f.enabled, statuses: f.statuses, quotas: f.quotas}
	h := &AdminHandler{
		taskAdmin:       &fakeTaskAdmin{},
		zoneCatalog:     catalog,
		zoneUniverse:    catalog,
		regionQuotas:    catalog,
		zoneHealth:      &fakeZoneHealth{cells: f.cells},
		zoneFamily:      &fakeZoneFamilyCounter{counts: f.census},
		machineTypes:    &fakeMachineCatalog{rows: f.shapes},
		spotAdvice:      &fakeSpotAdvice{cells: f.advice},
		placementPolicy: &fakePlacementPolicy{margin: f.margin},
	}
	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("Test API", "1.0.0"))
	h.Register(api)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v2/admin/capacity", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Now     time.Time            `json:"now"`
		Fleet   CapacityFleetItem    `json:"fleet"`
		Regions []CapacityRegionItem `json:"regions"`
		Zones   []CapacityZoneItem   `json:"zones"`
		Pools   []CapacityPoolItem   `json:"pools"`
		Spot    SpotPanelItem        `json:"spot"`
		Margin  int64                `json:"reserve_margin_percent"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return body
}

// findZone is the assertion helper; a missing zone fails loudly rather than
// letting an index shift silently assert about a neighbor.
func findZone(t *testing.T, zones []CapacityZoneItem, name string) CapacityZoneItem {
	t.Helper()
	for _, z := range zones {
		if z.Zone == name {
			return z
		}
	}
	t.Fatalf("zone %s not on the board: %+v", name, zones)
	return CapacityZoneItem{}
}

// The board's rows are the placement space plus whatever still has state in it,
// and the tile totals its families rather than restating one of them.
func TestCapacityBoardUniverse(t *testing.T) {
	t.Parallel()
	f := defaultCapacityFixture()
	f.enabled = []string{"europe-west4-a", "europe-west4-b"}
	f.statuses["europe-west4-b"] = gcecatalog.ZoneStatusUp
	f.statuses["us-central1-a"] = gcecatalog.ZoneStatusUp
	f.statuses["asia-east1-a"] = gcecatalog.ZoneStatusUp
	// Disabled but still holding a machine: placement state, so it stays.
	f.census = append(f.census, host.ZoneFamilyCount{
		Zone: "us-central1-a", MachineFamily: "n2", Machines: 1, Devices: 3,
	})
	f.quotas["us-central1"] = []*gcecatalog.RegionalQuota{
		{Region: "us-central1", QuotaType: "N2_CPUS", LimitValue: 200, UsageValue: 8},
	}

	body := f.snapshot(t)
	if len(body.Zones) != 3 {
		t.Fatalf("zones = %+v, want 3, since asia-east1-a is disabled and empty and so is not placement state", body.Zones)
	}
	a := findZone(t, body.Zones, "europe-west4-a")
	if a.Machines != 2 || a.Arriving != 1 || a.Devices != 6 {
		t.Errorf("europe-west4-a census = %d machines / %d arriving / %d devices, want 2/1/6",
			a.Machines, a.Arriving, a.Devices)
	}
	usc := findZone(t, body.Zones, "us-central1-a")
	if usc.Enabled || usc.Verdict != verdictDisabled {
		t.Errorf("us-central1-a = %+v, want a disabled tile", usc)
	}
	if body.Fleet.Machines != 3 || body.Fleet.Devices != 9 || body.Fleet.Regions != 2 {
		t.Errorf("fleet = %+v, want 3 machines / 9 devices / 2 regions", body.Fleet)
	}
	if body.Fleet.ZonesEnabled != 2 {
		t.Errorf("zones_enabled = %d, want 2, since the disabled zone still holding a host does not count",
			body.Fleet.ZonesEnabled)
	}
}

// The KPI strip states what the zones that are not clear are doing, so the
// breakdown is counted here from the verdicts the tiles carry, one counter per
// verdict, and a disabled zone in none of them.
func TestCapacityFleetCountsWhatHoldsPlacementBack(t *testing.T) {
	t.Parallel()
	live := time.Now().Add(20 * time.Minute)
	lapsed := time.Now().Add(-time.Minute)
	f := defaultCapacityFixture()
	f.enabled = []string{"europe-west4-a", "europe-west4-b", "europe-west4-c", "us-east5-a"}
	for _, zone := range []string{"europe-west4-b", "europe-west4-c", "us-east5-a", "us-central1-a"} {
		f.statuses[zone] = gcecatalog.ZoneStatusUp
	}
	// One zone per verdict: a live cooldown, a pool spent to its reserve, a low
	// Spot band, and a disabled zone that still holds a machine.
	f.cells = []*gcecatalog.ZoneHealth{
		{
			Zone: "europe-west4-b", MachineFamily: "n1",
			BindingMetric: gcecatalog.ZoneMetricCapacity, ConsecutiveFailures: 2, CooldownUntil: &live,
		},
		{
			Zone: "us-east5-a", MachineFamily: "n1",
			BindingMetric: gcecatalog.ZoneMetricQuotaCPU, ConsecutiveFailures: 1, CooldownUntil: &lapsed,
		},
	}
	f.quotas["us-east5"] = []*gcecatalog.RegionalQuota{
		{Region: "us-east5", QuotaType: "CPUS", LimitValue: 3000, UsageValue: 2912},
	}
	f.advice = []*gcecatalog.SpotAdvice{
		{Zone: "europe-west4-c", MachineType: "n1-standard-8", Obtainability: 0.2},
	}
	f.census = append(f.census,
		host.ZoneFamilyCount{Zone: "europe-west4-c", MachineFamily: "n1", Machines: 1},
		host.ZoneFamilyCount{Zone: "us-central1-a", MachineFamily: "n1", Machines: 1},
	)

	fleet := f.snapshot(t).Fleet
	if fleet.ZonesEnabled != 4 || fleet.ZonesClear != 1 {
		t.Fatalf("fleet = %+v, want 4 enabled zones with 1 clear", fleet)
	}
	if fleet.ZonesCooling != 1 || fleet.ZonesBlocked != 1 || fleet.ZonesConstrained != 1 {
		t.Errorf("breakdown = %d cooling / %d blocked / %d constrained, want 1/1/1",
			fleet.ZonesCooling, fleet.ZonesBlocked, fleet.ZonesConstrained)
	}
}

// A live zone_health cell flips the verdict to cooling and carries the instant
// the countdown is computed against. Once it lapses the cell still exists,
// because the failure count decays the ranking score, and the verdict goes back
// to clear.
func TestCapacityCooldownFlipsTheVerdictAndExpiryFlipsItBack(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(30 * time.Minute)
	f := defaultCapacityFixture()
	f.cells = []*gcecatalog.ZoneHealth{{
		Zone: "europe-west4-a", MachineFamily: "n1",
		BindingMetric: gcecatalog.ZoneMetricCapacity, ConsecutiveFailures: 2,
		CooldownUntil: &future, LastReason: "zone stockout",
	}}

	cooling := findZone(t, f.snapshot(t).Zones, "europe-west4-a")
	if cooling.Verdict != verdictCooling {
		t.Fatalf("verdict = %q, want %q while the cell is in cooldown", cooling.Verdict, verdictCooling)
	}
	if cooling.CoolingUntil.IsZero() || !cooling.CoolingUntil.Equal(future.UTC().Truncate(time.Nanosecond)) {
		t.Errorf("cooling_until = %v, want the cell's own expiry %v", cooling.CoolingUntil, future)
	}
	if cooling.VerdictDetail == "" {
		t.Error("a cooling verdict must carry the sentence the console renders")
	}
	if len(cooling.Cells) != 1 || cooling.Cells[0].Cooldown == nil ||
		cooling.Cells[0].Cooldown.MetricLabel != "capacity" {
		t.Errorf("cell = %+v, want the cooldown with its English metric label", cooling.Cells)
	}

	// Same cell, expired: the cooldown no longer repels placement.
	past := time.Now().Add(-time.Minute)
	f.cells[0].CooldownUntil = &past
	lapsed := findZone(t, f.snapshot(t).Zones, "europe-west4-a")
	if lapsed.Verdict != verdictClear {
		t.Fatalf("verdict = %q after expiry, want %q", lapsed.Verdict, verdictClear)
	}
	if !lapsed.CoolingUntil.IsZero() {
		t.Errorf("cooling_until = %v after expiry, want zero", lapsed.CoolingUntil)
	}
	if lapsed.Cells[0].Cooldown == nil {
		t.Error("the lapsed cell must still ride the wire, since its failure count still decays the score")
	}
}

// A family whose quota metric has no series is unknown, never blocked: Google
// publishes nothing for a per-family pool until the family is first used, and
// placement treats what it cannot read as open.
func TestCapacityFamilyWithoutQuotaSeriesIsUnknownNotBlocked(t *testing.T) {
	t.Parallel()
	f := defaultCapacityFixture()
	// C4 draws the dimensioned pool, which the region list cannot name and this
	// project has never populated.
	f.shapes = append(f.shapes, &machinetype.MachineType{
		ID: "c4-standard-8", Family: "c4", VCPUs: 8, BootDiskType: "hyperdisk-balanced", IsEnabled: true,
	})
	f.census = append(f.census, host.ZoneFamilyCount{
		Zone: "europe-west4-a", MachineFamily: "c4", Machines: 1, Devices: 2,
	})

	body := f.snapshot(t)
	zone := findZone(t, body.Zones, "europe-west4-a")
	var c4 CapacityCellItem
	for _, cell := range zone.Cells {
		if cell.MachineFamily == "c4" {
			c4 = cell
		}
	}
	if c4.MachineFamily == "" {
		t.Fatalf("no c4 cell on the tile: %+v", zone.Cells)
	}
	if c4.QuotaState != poolUnknown {
		t.Errorf("c4 quota_state = %q, want %q", c4.QuotaState, poolUnknown)
	}
	if c4.Verdict == verdictBlocked {
		t.Error("an unpublished quota series must never read as blocked")
	}
	// And the pool itself is carried, dashed rather than dropped.
	var dimensioned CapacityPoolItem
	for _, pool := range body.Pools {
		if pool.Pool == gcecatalog.DimensionedQuotaType(gcecatalog.QuotaTypeCPUsPerVMFamily, "C4") {
			dimensioned = pool
		}
	}
	if dimensioned.Pool == "" {
		t.Fatalf("the dimensioned pool is missing from the headroom rows: %+v", body.Pools)
	}
	if dimensioned.State != poolUnknown || dimensioned.Limit != 0 {
		t.Errorf("dimensioned pool = %+v, want an unknown state with no numbers", dimensioned)
	}
	if len(dimensioned.Families) != 1 || dimensioned.Families[0] != "c4" {
		t.Errorf("dimensioned pool families = %v, want [c4]", dimensioned.Families)
	}
}

// A pool spent to its reserve blocks the families that draw it, names itself in
// the verdict, and puts the region's rail on the same footing.
func TestCapacityQuotaWallBlocksTheFamilyAndTheRail(t *testing.T) {
	t.Parallel()
	f := defaultCapacityFixture()
	f.quotas["europe-west4"] = []*gcecatalog.RegionalQuota{
		{Region: "europe-west4", QuotaType: "CPUS", LimitValue: 3000, UsageValue: 2912},
	}

	body := f.snapshot(t)
	zone := findZone(t, body.Zones, "europe-west4-a")
	if zone.Verdict != verdictBlocked {
		t.Fatalf("verdict = %q, want %q, with 88 vCPUs free against a 300 reserve", zone.Verdict, verdictBlocked)
	}
	if zone.Cells[0].QuotaPool != "CPUS" {
		t.Errorf("quota_pool = %q, want the pool that ran out", zone.Cells[0].QuotaPool)
	}
	if len(body.Regions) != 1 || body.Regions[0].QuotaState != regionBlocked {
		t.Fatalf("regions = %+v, want the rail blocked on the same pool", body.Regions)
	}
	if body.Regions[0].QuotaUsage != 2912 || body.Regions[0].QuotaLimit != 3000 {
		t.Errorf("rail numbers = %d of %d, want 2912 of 3000",
			body.Regions[0].QuotaUsage, body.Regions[0].QuotaLimit)
	}
	// The sentence is rendered verbatim beside grouped numbers, so it carries
	// its own separators rather than reading 2912 next to 2,912.
	if want := "CPUS in europe-west4 is spent to its reserve, 2,912 of 3,000 used"; zone.VerdictDetail != want {
		t.Errorf("verdict_detail = %q, want %q", zone.VerdictDetail, want)
	}
	if body.Fleet.ZonesClear != 0 || body.Fleet.ZonesBlocked != 1 {
		t.Errorf("fleet = %+v, want the zone counted as blocked rather than clear", body.Fleet)
	}
}

// A zone an operator switched off answers "disabled", but the advice cells
// measured there still ride its tile: the operator's choice is not a reason to
// claim the zone was never probed.
func TestCapacityDisabledZoneKeepsItsSpotAdvice(t *testing.T) {
	t.Parallel()
	f := defaultCapacityFixture()
	f.enabled = nil
	f.advice = []*gcecatalog.SpotAdvice{
		{Zone: "europe-west4-a", MachineType: "n1-standard-8", Obtainability: 0.9},
	}

	zone := findZone(t, f.snapshot(t).Zones, "europe-west4-a")
	if zone.Verdict != verdictDisabled {
		t.Fatalf("verdict = %q, want %q", zone.Verdict, verdictDisabled)
	}
	if zone.Spot == nil || zone.Spot.Band != spotBandHigh {
		t.Errorf("tile spot line = %+v, want the zone's live advice cell", zone.Spot)
	}
}

// A low Spot obtainability band annotates the Spot line and constrains the
// cell; it never reads as blocked, because it says nothing about on-demand.
func TestCapacityLowSpotBandConstrainsWithoutCondemningOnDemand(t *testing.T) {
	t.Parallel()
	synced := time.Now().Add(-4 * time.Minute).UTC().Truncate(time.Second)
	f := defaultCapacityFixture()
	f.advice = []*gcecatalog.SpotAdvice{
		{
			Zone: "europe-west4-a", MachineType: "n1-standard-8", Obtainability: 0.2,
			UptimeTierSeconds: gcecatalog.SpotUptimeTierMinute, SyncedAt: synced,
		},
	}

	zone := findZone(t, f.snapshot(t).Zones, "europe-west4-a")
	if zone.Verdict != verdictConstrained {
		t.Fatalf("verdict = %q, want %q", zone.Verdict, verdictConstrained)
	}
	if zone.Spot == nil || zone.Spot.Band != spotBandLow || !zone.Spot.SyncedAt.Equal(synced) {
		t.Fatalf("tile spot line = %+v, want the low band with its sync time", zone.Spot)
	}

	// The same cell in the high band leaves the verdict clear.
	f.advice[0].Obtainability = 0.9
	high := findZone(t, f.snapshot(t).Zones, "europe-west4-a")
	if high.Verdict != verdictClear || high.Spot.Band != spotBandHigh {
		t.Errorf("verdict/band = %q/%q, want clear/high", high.Verdict, high.Spot.Band)
	}
}

// The tile's single Spot line is the weakest of the zone's families, because
// that is the one that refuses a create first.
func TestCapacityTileReportsTheWeakestSpotCell(t *testing.T) {
	t.Parallel()
	f := defaultCapacityFixture()
	f.shapes = append(f.shapes, &machinetype.MachineType{
		ID: "n2-standard-8", Family: "n2", VCPUs: 8, BootDiskType: "pd-balanced", IsEnabled: true,
	})
	f.census = append(f.census, host.ZoneFamilyCount{
		Zone: "europe-west4-a", MachineFamily: "n2", Machines: 1,
	})
	f.advice = []*gcecatalog.SpotAdvice{
		{Zone: "europe-west4-a", MachineType: "n1-standard-8", Obtainability: 0.9},
		{Zone: "europe-west4-a", MachineType: "n2-standard-8", Obtainability: 0.5},
	}

	zone := findZone(t, f.snapshot(t).Zones, "europe-west4-a")
	if zone.Spot == nil || zone.Spot.MachineType != "n2-standard-8" {
		t.Errorf("tile spot line = %+v, want the weaker n2 cell", zone.Spot)
	}
}

// An enabled zone the fleet has not reached yet still carries every curated
// family, so the advice and quota data have a cell to land in. Enabling a zone
// must put a live tile on the board, and that tile must not claim nothing was
// observed while the advice sync is probing it.
func TestCapacityEnabledEmptyZoneCarriesTheCuratedFamilies(t *testing.T) {
	t.Parallel()
	synced := time.Now().Add(-3 * time.Minute).UTC().Truncate(time.Second)
	f := defaultCapacityFixture()
	f.enabled = []string{"europe-west4-a", "europe-west4-c"}
	f.statuses["europe-west4-c"] = gcecatalog.ZoneStatusUp
	// No hosts and no cooldowns in europe-west4-c. Only the advice probe has been
	// there, exactly the state a zone is in moments after an operator enables it.
	f.advice = []*gcecatalog.SpotAdvice{{
		Zone: "europe-west4-c", MachineType: "n1-standard-8", Obtainability: 0.9,
		UptimeTierSeconds: gcecatalog.SpotUptimeTierHour, SyncedAt: synced,
	}}

	zone := findZone(t, f.snapshot(t).Zones, "europe-west4-c")
	if len(zone.Cells) != 1 || zone.Cells[0].MachineFamily != "n1" {
		t.Fatalf("cells = %+v, want the curated n1 family with nothing running in it", zone.Cells)
	}
	if zone.Cells[0].Machines != 0 || zone.Cells[0].QuotaState != poolOpen {
		t.Errorf("n1 cell = %+v, want zero machines against the region's open pool", zone.Cells[0])
	}
	if zone.Spot == nil || zone.Spot.MachineType != "n1-standard-8" {
		t.Errorf("tile spot line = %+v, want the advice cell the probe already measured", zone.Spot)
	}
	if zone.Verdict != verdictClear {
		t.Errorf("verdict = %q, want %q, since open quota and high advice is a measurement and not an absence",
			zone.Verdict, verdictClear)
	}
}

// The rail chip judges only pools the fleet's shapes draw: every region also
// carries rows for families the fleet never provisions, and a wall in one of
// those is not this fleet's wall.
func TestCapacityRegionChipIgnoresPoolsTheFleetDoesNotDraw(t *testing.T) {
	t.Parallel()
	f := defaultCapacityFixture()
	f.quotas["europe-west4"] = append(f.quotas["europe-west4"],
		&gcecatalog.RegionalQuota{Region: "europe-west4", QuotaType: "A2_CPUS", LimitValue: 192, UsageValue: 192})

	regions := f.snapshot(t).Regions
	if len(regions) != 1 || regions[0].QuotaState != regionComfort || regions[0].QuotaPool != "CPUS" {
		t.Errorf("rail = %+v, want comfortable on CPUS, since the spent A2 pool feeds no shape the fleet places",
			regions)
	}
}

// With nothing observed at all the cell says so. "No signal" is not "clear":
// the first is an absence of measurement, the second is a measurement.
func TestCapacityAbsentSignalsReadAsNoSignal(t *testing.T) {
	t.Parallel()
	f := defaultCapacityFixture()
	f.quotas = map[string][]*gcecatalog.RegionalQuota{}

	zone := findZone(t, f.snapshot(t).Zones, "europe-west4-a")
	if zone.Verdict != verdictNoSignal {
		t.Errorf("verdict = %q, want %q with no cooldown, no advice and no quota series",
			zone.Verdict, verdictNoSignal)
	}
}

// The margin the bars mark is the one ranking actually filters below, read from
// the operator's setting rather than compiled in beside it.
func TestCapacityPoolsMarkTheOperatorsReserveMargin(t *testing.T) {
	t.Parallel()
	f := defaultCapacityFixture()
	f.margin = 25

	body := f.snapshot(t)
	if body.Margin != 25 {
		t.Errorf("reserve_margin_percent = %d, want the stored 25", body.Margin)
	}
	for _, pool := range body.Pools {
		if pool.Pool != "CPUS" {
			continue
		}
		if pool.Reserve != 100 {
			t.Errorf("CPUS reserve = %d, want 100 (25%% of 400)", pool.Reserve)
		}
		if pool.Usage != 24 || pool.Limit != 400 || pool.State != poolOpen {
			t.Errorf("CPUS pool = %+v, want 24 of 400, open", pool)
		}
	}
}

// spotBlock serves one composed Spot capacity block from the collaborators a test
// varies, so each of its three subjects is asserted on its own.
func spotBlock(t *testing.T, h *AdminHandler) SpotPanelItem {
	t.Helper()
	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("Test API", "1.0.0"))
	h.taskAdmin = &fakeTaskAdmin{}
	h.Register(api)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v2/admin/capacity", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Spot SpotPanelItem `json:"spot"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return body.Spot
}

// The census rides the wire cell by cell and as the two totals the card states,
// so the surface never reduces it a second way.
func TestCapacitySpotFleetSplit(t *testing.T) {
	t.Parallel()
	spot := spotBlock(t, &AdminHandler{
		spotFleet: &fakeSpotFleet{counts: []host.ModelStatusCount{
			{Model: host.ProvisioningModelSpot, Status: host.StatusRunning, Count: 3},
			{Model: host.ProvisioningModelStandard, Status: host.StatusRunning, Count: 1},
		}},
	})

	if len(spot.Fleet) != 2 || spot.Fleet[0].Model != "SPOT" || spot.Fleet[0].Count != 3 {
		t.Errorf("fleet = %+v", spot.Fleet)
	}
	if spot.SpotMachines != 3 || spot.StandardMachines != 1 {
		t.Errorf("model split = %d Spot / %d on-demand, want 3/1", spot.SpotMachines, spot.StandardMachines)
	}
}

// The churn counts audit entries, and the preemptions with nothing recorded
// against them are the number the card alarms on.
func TestCapacitySpotChurn(t *testing.T) {
	t.Parallel()
	spot := spotBlock(t, &AdminHandler{
		auditReader: &fakeSpotAuditReader{
			counts: map[audit.Action]int64{
				audit.ActionHostPreempted:           4,
				audit.ActionHostPreemptionRecovered: 3,
			},
			// One entry without a duration: skipped, not averaged as zero.
			entries: []audit.Entry{
				{Metadata: map[string]any{"duration_seconds": float64(30)}},
				{Metadata: map[string]any{"duration_seconds": float64(50)}},
				{Metadata: map[string]any{}},
			},
		},
	})

	if spot.Preempted7d != 4 || spot.Recovered7d != 3 || spot.Unrecovered7d != 1 {
		t.Errorf("churn = %d preempted / %d recovered / %d unrecovered, want 4/3/1",
			spot.Preempted7d, spot.Recovered7d, spot.Unrecovered7d)
	}
	if spot.TypicalRecoverySeconds != 40 {
		t.Errorf("typical recovery = %d, want 40 (mean of 30 and 50; the missing duration skipped)",
			spot.TypicalRecoverySeconds)
	}
}

// The advice block reports the freshest cell it holds, and says it is still the
// one ranking consults.
func TestCapacitySpotAdviceHealth(t *testing.T) {
	t.Parallel()
	synced := time.Now().Add(-4 * time.Minute).UTC().Truncate(time.Second)
	spot := spotBlock(t, &AdminHandler{
		spotAdvice: &fakeSpotAdvice{cells: []*gcecatalog.SpotAdvice{
			{Zone: "europe-west4-a", MachineType: "n1-standard-8", SyncedAt: synced.Add(-10 * time.Minute)},
			{Zone: "europe-west4-b", MachineType: "n1-standard-8", SyncedAt: synced},
		}},
		spotAdviceEnabled: true,
	})

	if !spot.Advice.Enabled || spot.Advice.Cells != 2 || !spot.Advice.NewestSyncedAt.Equal(synced) {
		t.Errorf("advice = %+v", spot.Advice)
	}
	if spot.Advice.Stale {
		t.Error("advice synced four minutes ago is what ranking still consults, so it is not stale")
	}
}

// Staleness is judged against the bound the composition root hands over, the
// one ranking stops consulting a cell at, so the page cannot call advice fresh
// while placement has already stopped steering on it.
func TestCapacitySpotAdviceStalenessFollowsRankingsBound(t *testing.T) {
	t.Parallel()
	synced := time.Now().Add(-45 * time.Minute).UTC().Truncate(time.Second)
	staleness := func(bound time.Duration) bool {
		return spotBlock(t, &AdminHandler{
			spotAdvice:        &fakeSpotAdvice{cells: []*gcecatalog.SpotAdvice{{Zone: "europe-west4-a", SyncedAt: synced}}},
			spotAdviceEnabled: true,
			spotAdviceStale:   bound,
		}).Advice.Stale
	}

	if !staleness(30 * time.Minute) {
		t.Error("a cell older than the deployment's bound must read as stale")
	}
	if staleness(2 * time.Hour) {
		t.Error("the same cell inside a longer bound is still steering placement")
	}
}

// Advice that never synced is stale rather than fresh with nothing in it: the
// page's whole claim is whether Spot steering can be trusted.
func TestCapacityUnsyncedSpotAdviceIsStale(t *testing.T) {
	t.Parallel()
	advice := spotBlock(t, &AdminHandler{spotAdviceEnabled: true}).Advice
	if !advice.Stale || advice.Cells != 0 {
		t.Errorf("advice = %+v, want no cells and stale", advice)
	}
}

// recordingAuditSink captures the entries an operation files.
type recordingAuditSink struct{ entries []audit.Entry }

func (r *recordingAuditSink) LogAsync(entry audit.Entry) { r.entries = append(r.entries, entry) }

func TestClearZoneCooldownDropsTheCellAndAudits(t *testing.T) {
	t.Parallel()

	health := &fakeZoneHealth{}
	sink := &recordingAuditSink{}
	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("Test API", "1.0.0"))
	h := &AdminHandler{zoneHealth: health, auditLogger: sink}
	h.Register(api)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete,
		"/v2/admin/zone-health/europe-west4-b/n1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body %s", rec.Code, rec.Body.String())
	}
	if got := health.cleared; len(got) != 1 || got[0] != "europe-west4-b/n1" {
		t.Errorf("cleared = %v, want [europe-west4-b/n1]", got)
	}
	if len(sink.entries) != 1 {
		t.Fatalf("audit entries = %+v, want 1", sink.entries)
	}
	entry := sink.entries[0]
	if entry.Action != audit.ActionZoneCooldownClear || entry.ResourceType != audit.ResourceSystem {
		t.Errorf("entry = %+v, want a %s on %s", entry, audit.ActionZoneCooldownClear, audit.ResourceSystem)
	}
	if entry.ResourceID == nil || *entry.ResourceID != "europe-west4-b/n1" {
		t.Errorf("entry.ResourceID = %v, want the cell key", entry.ResourceID)
	}
}

func TestClearZoneCooldownStoreFailureIs500AndSkipsTheAudit(t *testing.T) {
	t.Parallel()

	sink := &recordingAuditSink{}
	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("Test API", "1.0.0"))
	h := &AdminHandler{zoneHealth: &fakeZoneHealth{clearErr: errBoom}, auditLogger: sink}
	h.Register(api)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete,
		"/v2/admin/zone-health/europe-west4-b/n1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if len(sink.entries) != 0 {
		t.Errorf("audit entries = %+v, want none for a failed clear", sink.entries)
	}
}
