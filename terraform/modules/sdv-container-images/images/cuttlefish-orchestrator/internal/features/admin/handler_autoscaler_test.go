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
	"strings"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

type fakeQuotaService struct{ maxHosts, used int64 }

func (f *fakeQuotaService) Get(context.Context, quota.Scope, string) (*quota.Quota, error) {
	return &quota.Quota{MaxHosts: f.maxHosts, UsedHosts: f.used}, nil
}

func (f *fakeQuotaService) SetLimit(context.Context, quota.Scope, string, int64) error { return nil }
func (f *fakeQuotaService) ReconcileAll(context.Context) (int, error)                  { return 0, nil }

// testKeeperLimits mirror the keeper's real budgets without importing
// internal/jobs, which is the same reason the handler takes them as data.
var testKeeperLimits = KeeperLimits{
	CycleSeconds:         30,
	MaxShortensPerCycle:  2,
	MaxCollapsesPerCycle: 2,
	QuotaReserveHosts:    1,
	LeaseName:            "background-plane",
}

// singlePoolView is the whole picture in one pool, which is what a deployment
// nobody has partitioned composes to, and the case every gate row below is
// about.
func singlePoolView(status hostconfig.FleetStatus) hostconfig.FleetView {
	return hostconfig.FleetView{
		Pools: []hostconfig.PoolStatus{{Pool: hostconfig.DefaultTier, Status: status}},
		Fleet: status,
	}
}

// gateHandler builds a handler with the keeper's lease held, so a test that
// wants a different reason has to say so.
func gateHandler(leases []spanner.LeaderLease, q *fakeQuotaService) *AdminHandler {
	h := &AdminHandler{keeperLimits: testKeeperLimits, leaseAdmin: &fakeLeaseAdmin{leases: leases}}
	if q != nil {
		h.quotaService = q
	}
	return h
}

func heldLease(now time.Time) []spanner.LeaderLease {
	return []spanner.LeaderLease{{
		Name: "background-plane", Holder: "instance-a",
		Expiry: now.Add(20 * time.Second), RenewedAt: now,
	}}
}

// A still diagram has at least six causes and they are indistinguishable
// without this. What the gate must never do is report a lower-order reason while
// a higher-order one holds: "the target is met" on a fleet whose keeper is not
// running anywhere would be a page reassuring an operator about a stopped loop.
func TestGateStatusReportsTheMostFundamentalCause(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name   string
		leases []spanner.LeaderLease
		quota  *fakeQuotaService
		status hostconfig.FleetStatus
		want   string
	}{
		{
			// No lease row at all: the loop has never led, so nothing cycles.
			name:   "nobody is running the keeper",
			leases: nil,
			status: hostconfig.FleetStatus{Mode: hostconfig.ModeOn, Target: 3},
			want:   GateNoLeader,
		},
		{
			// An expired lease means the loop stopped, since every loop
			// renews every few seconds while alive.
			name: "the lease has lapsed",
			leases: []spanner.LeaderLease{{
				Name: "background-plane", Holder: "instance-a", Expiry: now.Add(-time.Minute),
			}},
			status: hostconfig.FleetStatus{Mode: hostconfig.ModeOn, Target: 3},
			want:   GateNoLeader,
		},
		{
			// Off is a configuration with no fault in it, and it outranks the
			// arithmetic beneath it: a deficit under off is unmet BY DESIGN.
			name:   "off composes a target and acts on none of it",
			leases: heldLease(now),
			status: hostconfig.FleetStatus{Mode: hostconfig.ModeOff, Target: 3},
			want:   GateModeOff,
		},
		{
			// Near the ceiling top-ups stop forever with no error anywhere,
			// which is why this reason is named instead of left to be inferred
			// from a deficit that never closes.
			name:   "the quota reserve holds the last slot back",
			leases: heldLease(now),
			quota:  &fakeQuotaService{maxHosts: 10, used: 9},
			status: hostconfig.FleetStatus{Mode: hostconfig.ModeOn, Target: 3},
			want:   GateQuotaReserved,
		},
		{
			// Rescue spends nothing, so it is not held back by the reserve. A
			// gate that reported quota here would explain a cycle that is in fact
			// acting.
			name:   "a draining host to renew outranks the reserve",
			leases: heldLease(now),
			quota:  &fakeQuotaService{maxHosts: 10, used: 10},
			status: hostconfig.FleetStatus{Mode: hostconfig.ModeOn, Target: 3, Draining: 1, Total: 1},
			want:   GateActing,
		},
		{
			// provision_only can add and never give back, so a surplus simply
			// runs its leases out. Saying "acting" would claim a reclamation the
			// mode forbids.
			name:   "a surplus under provision_only is left to expire",
			leases: heldLease(now),
			quota:  &fakeQuotaService{maxHosts: 10, used: 2},
			status: hostconfig.FleetStatus{Mode: hostconfig.ModeProvisionOnly, Target: 1, Standby: 4, Total: 4},
			want:   GateProvisionOnly,
		},
		{
			// The cap is reported in BOTH directions. Reported on one side only,
			// an operator watching a large deficit close two hosts at a time
			// would be told the cycle is simply "acting" while the mirror case
			// named the cap, and would have to guess which page was lying.
			name:   "a large deficit converges over cycles",
			leases: heldLease(now),
			quota:  &fakeQuotaService{maxHosts: 20, used: 2},
			status: hostconfig.FleetStatus{Mode: hostconfig.ModeOn, Target: 8, Standby: 0},
			want:   GateCapped,
		},
		{
			// Converging over several cycles is a real limit and a benign one,
			// so it is distinguished from both "acting" and "stuck".
			name:   "a large surplus converges over cycles",
			leases: heldLease(now),
			quota:  &fakeQuotaService{maxHosts: 20, used: 8},
			status: hostconfig.FleetStatus{Mode: hostconfig.ModeOn, Target: 1, Standby: 8, Total: 8},
			want:   GateCapped,
		},
		{
			name:   "the fleet holds what was asked for",
			leases: heldLease(now),
			quota:  &fakeQuotaService{maxHosts: 10, used: 3},
			status: hostconfig.FleetStatus{Mode: hostconfig.ModeOn, Target: 3, Standby: 3, Total: 3},
			want:   GateTargetMet,
		},
		{
			// A fleet sitting on its floor with every host free: spare by the
			// buffer, and spare by nothing the keeper may act on. A gate reading
			// the buffer alone reports a reclamation that will never happen, on
			// exactly the fleet an operator set the floor to protect.
			name:   "a fleet on its floor is not surplus",
			leases: heldLease(now),
			quota:  &fakeQuotaService{maxHosts: 10, used: 2},
			status: hostconfig.FleetStatus{Mode: hostconfig.ModeOn, Target: 0, Standby: 2, Floor: 2, Total: 2},
			want:   GateTargetMet,
		},
		{
			// And the other direction on the same fleet: the buffer is met and
			// the floor is not, so the cycle is buying. Reported through the
			// deficit arm, which a gate keyed on the target alone never reaches.
			name:   "a fleet under its floor is acting",
			leases: heldLease(now),
			quota:  &fakeQuotaService{maxHosts: 10, used: 2},
			status: hostconfig.FleetStatus{Mode: hostconfig.ModeOn, Target: 0, Standby: 1, Floor: 3, Total: 1},
			want:   GateActing,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gate, err := gateHandler(tc.leases, tc.quota).gateStatus(t.Context(), singlePoolView(tc.status), now)
			if err != nil {
				t.Fatalf("gateStatus: %v", err)
			}
			if gate.Reason != tc.want {
				t.Errorf("reason = %q, want %q", gate.Reason, tc.want)
			}
		})
	}
}

// Neither number may go negative, because a page rendering "-2 deficit" would
// be arithmetic leaking through a label, and neither may be positive at the
// same time as the other. They stopped being one subtraction when the fleet
// gained a second bound, so every status here states the fleet its idle counts
// came out of: a status whose total is below its own standby count describes no
// fleet.
func TestGateStatusDeficitAndSurplusAreComplements(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	h := gateHandler(heldLease(now), &fakeQuotaService{maxHosts: 10, used: 2})

	short, err := h.gateStatus(t.Context(), singlePoolView(hostconfig.FleetStatus{
		Mode: hostconfig.ModeOn, Target: 4, Standby: 1, Total: 1,
	}), now)
	if err != nil {
		t.Fatalf("gateStatus: %v", err)
	}
	if short.Deficit != 3 || short.Surplus != 0 {
		t.Errorf("deficit/surplus = %d/%d, want 3/0", short.Deficit, short.Surplus)
	}

	over, err := h.gateStatus(t.Context(), singlePoolView(hostconfig.FleetStatus{
		Mode: hostconfig.ModeOn, Target: 1, Standby: 2, Total: 2,
	}), now)
	if err != nil {
		t.Fatalf("gateStatus: %v", err)
	}
	if over.Deficit != 0 || over.Surplus != 1 {
		t.Errorf("deficit/surplus = %d/%d, want 0/1", over.Deficit, over.Surplus)
	}
}

// An unwired lease reader is a deployment fact and says nothing about the
// loop. Reporting no leader would put a fault on the page that nobody could
// act on, and it would mask every other reason behind it.
func TestGateStatusWithNoLeaseReaderDoesNotClaimAStoppedLoop(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	h := &AdminHandler{keeperLimits: testKeeperLimits}

	gate, err := h.gateStatus(t.Context(), singlePoolView(hostconfig.FleetStatus{
		Mode: hostconfig.ModeOn, Target: 2, Standby: 2,
	}), now)
	if err != nil {
		t.Fatalf("gateStatus: %v", err)
	}
	if gate.Reason != GateTargetMet {
		t.Errorf("reason = %q, want %q", gate.Reason, GateTargetMet)
	}
}

type fakeFleetViewer struct {
	hosts       []host.FleetHost
	shortenable []*host.Host
	askedFor    int
}

func (f *fakeFleetViewer) ListFleetView(context.Context, time.Duration) ([]host.FleetHost, error) {
	return f.hosts, nil
}

func (f *fakeFleetViewer) ListShortenable(_ context.Context, _ time.Duration, limit int) ([]*host.Host, error) {
	f.askedFor = limit
	// The bound is PER POOL, exactly as the repository applies it: a flat
	// truncation here would model a contract the real listing does not have.
	kept := make([]*host.Host, 0, len(f.shortenable))
	taken := map[string]int{}
	for _, h := range f.shortenable {
		pool := host.NormalizePool(h.Pool)
		if taken[pool] >= limit {
			continue
		}
		taken[pool]++
		kept = append(kept, h)
	}
	return kept, nil
}

func (f *fakeFleetViewer) CountByShape(context.Context) ([]host.ShapeCount, error) { return nil, nil }

// Which hosts a reduction takes is the page's one opaque consequence,
// and the order comes from the reclaimer's own listing rather than a sort here:
// a second ordering would be free to disagree with the one that runs.
func TestShortenOrderComesFromTheReclaimersOwnListing(t *testing.T) {
	t.Parallel()
	viewer := &fakeFleetViewer{shortenable: []*host.Host{{ID: "cold"}, {ID: "cool"}, {ID: "warm"}}}
	h := &AdminHandler{keeperLimits: testKeeperLimits, fleetView: viewer}
	hosts := []FleetHostItem{{ID: "warm"}, {ID: "cold"}, {ID: "cool"}}

	if err := h.stampShortenOrder(t.Context(), hosts, map[string]int64{host.PoolDefault: 5}); err != nil {
		t.Fatalf("stampShortenOrder: %v", err)
	}
	if viewer.askedFor != int(testKeeperLimits.MaxShortensPerCycle) {
		t.Errorf("asked for %d candidates, want the per-cycle cap %d, so the chips say next and not eventually",
			viewer.askedFor, testKeeperLimits.MaxShortensPerCycle)
	}
	byID := map[string]int64{}
	for _, item := range hosts {
		byID[item.ID] = item.ShortenOrder
	}
	if byID["cold"] != 1 || byID["cool"] != 2 {
		t.Errorf("order = %+v, want cold first and cool second", byID)
	}
	if byID["warm"] != 0 {
		t.Errorf("warm carries order %d, want none, since this cycle cannot reach it", byID["warm"])
	}
}

// The host's pool reaches the wire from the fleet view that read it, carried
// rather than re-derived: a mapping that dropped it would publish every host as
// the default pool's whatever its row said, and the reader has nothing else to
// tell them apart by.
func TestFleetHostItemsCarryThePool(t *testing.T) {
	t.Parallel()
	items := fleetHostItems([]host.FleetHost{
		{ID: "plain", Pool: host.PoolDefault},
		{ID: "accelerated", Pool: "gpu"},
	})
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if items[0].Pool != host.PoolDefault {
		t.Errorf("pool = %q, want %q", items[0].Pool, host.PoolDefault)
	}
	if items[1].Pool != "gpu" {
		t.Errorf("pool = %q, want gpu", items[1].Pool)
	}
}

// ladderRows are three shapes over two families, so a ladder drawn over them
// exercises the fact the page cannot pretend away: stockouts are recorded per
// family, and the two n4 rungs therefore share one observation.
func ladderRows() []*machinetype.MachineType {
	return []*machinetype.MachineType{
		{ID: "c4-standard-16", Family: "c4", Architecture: machinetype.ArchX86, VCPUs: 16, MemoryGB: 60, Fit: 7, IsEnabled: true},
		{ID: "n4-standard-16", Family: "n4", Architecture: machinetype.ArchX86, VCPUs: 16, MemoryGB: 64, Fit: 7, IsEnabled: true},
		{ID: "n4-standard-8", Family: "n4", Architecture: machinetype.ArchX86, VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true},
	}
}

// ladderRowsWithGPU adds the shape the pools disagree about: the gpu pool exists
// to buy it, and every CPU pool refuses it by a rule of its own.
func ladderRowsWithGPU() []*machinetype.MachineType {
	return append(ladderRows(), &machinetype.MachineType{
		ID: "g2-standard-8", Family: "g2", Architecture: machinetype.ArchX86,
		VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true, GPUMode: machinetype.GPUModeIntegrated,
	})
}

// withheldShape is the catalog's own account of a shape it does not offer. It
// is the answer a lookup on a stored rung gets once the catalog stops listing
// it.
func withheldShape(id, family string, vcpus, memoryGB int64) *machinetype.UnofferedError {
	return &machinetype.UnofferedError{
		MachineType: id, Reason: machinetype.ReasonBelowFloor,
		Family: family, VCPUs: vcpus, MemoryGB: memoryGB,
	}
}

// stockoutCell is one stockout observation as the failure path records it.
func stockoutCell(zone, family string, at, until time.Time) *gcecatalog.ZoneHealth {
	return &gcecatalog.ZoneHealth{
		Zone: zone, MachineFamily: family, BindingMetric: gcecatalog.ZoneMetricCapacity,
		ConsecutiveFailures: 1, LastFailureAt: &at, CooldownUntil: &until,
	}
}

func ladderHandler(catalog *fakeMachineCatalog, zones []string, health *fakeZoneHealth) *AdminHandler {
	return &AdminHandler{
		machineTypes: catalog,
		zoneCatalog:  &fakeZoneCatalog{enabled: zones},
		zoneHealth:   health,
	}
}

// ladderPolicy is a policy holding exactly the pools and ladders a case is
// about, in the shape the view reads them out of.
func ladderPolicy(ladders map[string][]string) hostconfig.FleetPolicy {
	policy := hostconfig.DefaultFleetPolicy()
	for pool, rungs := range ladders {
		policy.Tiers[pool] = hostconfig.FleetTier{Ladder: rungs}
	}
	return policy
}

// The page's rule is that every number is server-derived, and the ladder is the
// hardest case of it: what a rung holds, what is left to probe on it, and which
// rung the next host is built from are all judgements the walk makes and a
// console cannot. Each rung here is a different answer to those questions.
func TestLadderViewJudgesEachRungTheWayTheWalkDoes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	cooling := now.Add(10 * time.Minute)
	zones := []string{"europe-west4-a", "europe-west4-b"}
	health := &fakeZoneHealth{cells: []*gcecatalog.ZoneHealth{
		stockoutCell("europe-west4-a", "c4", now.Add(-9*time.Minute), cooling),
		stockoutCell("europe-west4-b", "c4", now.Add(-4*time.Minute), cooling),
		stockoutCell("europe-west4-a", "n4", now.Add(-6*time.Minute), cooling),
		// Lapsed: the shape it belonged to is preferred again.
		stockoutCell("europe-west4-b", "n4", now.Add(-2*time.Hour), now.Add(-time.Hour)),
		// A quota refusal says nothing about capacity.
		{Zone: "europe-west4-b", MachineFamily: "c4", BindingMetric: gcecatalog.ZoneMetricQuotaCPU, CooldownUntil: &cooling},
	}}
	h := ladderHandler(&fakeMachineCatalog{rows: ladderRows()}, zones, health)

	views, err := h.ladderViews(t.Context(), ladderPolicy(map[string][]string{
		host.PoolDefault: {"c4-standard-16", "n4-standard-16", "n4-standard-8"},
	}), now)
	if err != nil {
		t.Fatalf("ladderViews: %v", err)
	}
	rungs := views[host.PoolDefault]

	want := []LadderRungItem{
		{
			MachineType: "c4-standard-16", MachineFamily: "c4", Slots: 7,
			Zones: 2, StockedOutZones: 2, NewestStockoutAt: now.Add(-4 * time.Minute),
		},
		{
			MachineType: "n4-standard-16", MachineFamily: "n4", Slots: 7,
			Zones: 2, StockedOutZones: 1, NewestStockoutAt: now.Add(-6 * time.Minute),
			Landing: true,
		},
		{
			// Same family as the rung above, so the same observation stands
			// against it; it packs three slots, which is below the ceiling the
			// preferred rung promised, so the walk steps over it either way.
			MachineType: "n4-standard-8", MachineFamily: "n4", Slots: 3,
			Zones: 2, StockedOutZones: 1, NewestStockoutAt: now.Add(-6 * time.Minute),
		},
	}
	if len(rungs) != len(want) {
		t.Fatalf("rungs = %+v, want %d of them in the operator's order", rungs, len(want))
	}
	for i, w := range want {
		if rungs[i] != w {
			t.Errorf("rung %d = %+v, want %+v", i, rungs[i], w)
		}
	}
	if len(health.capacityReads) != 1 {
		t.Fatalf("stockout reads = %d, want exactly one covering the whole ladder", len(health.capacityReads))
	}
	if got := health.capacityReads[0]; len(got) != 2 || got[0] != "c4" || got[1] != "n4" {
		t.Errorf("read covered %v, want [c4 n4], since one family per read is one statement per rung", got)
	}
}

// A ladder with nothing left to probe is where provisioning fails outright
// without building anything. Marking a rung would draw a step that is not
// available, on the page an operator reads to find out why nothing is arriving.
func TestLadderViewMarksNoRungWhenTheWholeLadderIsStockedOut(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	cooling := now.Add(10 * time.Minute)
	health := &fakeZoneHealth{cells: []*gcecatalog.ZoneHealth{
		stockoutCell("europe-west4-a", "c4", now, cooling),
		stockoutCell("europe-west4-a", "n4", now, cooling),
	}}
	h := ladderHandler(&fakeMachineCatalog{rows: ladderRows()}, []string{"europe-west4-a"}, health)

	views, err := h.ladderViews(t.Context(), ladderPolicy(map[string][]string{
		host.PoolDefault: {"c4-standard-16", "n4-standard-16"},
	}), now)
	if err != nil {
		t.Fatalf("ladderViews: %v", err)
	}
	for i, rung := range views[host.PoolDefault] {
		if rung.Landing {
			t.Errorf("rung %d (%s) is marked as the one provisioning would build, with every rung stocked out",
				i, rung.MachineType)
		}
	}
}

// No ladder is the catalog default, and the section is absent rather than
// empty. This pins the cost: a deployment that stored no ladder must
// not pay a catalog listing and a stockout read on every poll of a page that
// refreshes every few seconds.
func TestLadderViewReadsNothingWithoutAStoredLadder(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	catalog := &fakeMachineCatalog{rows: ladderRows()}
	health := &fakeZoneHealth{}
	h := ladderHandler(catalog, []string{"europe-west4-a"}, health)

	views, err := h.ladderViews(t.Context(), hostconfig.DefaultFleetPolicy(), now)
	if err != nil {
		t.Fatalf("ladderViews: %v", err)
	}
	if len(views) != 0 {
		t.Errorf("views = %+v, want none", views)
	}
	if catalog.reads != 0 || len(health.capacityReads) != 0 {
		t.Errorf("catalog reads = %d, stockout reads = %d, want none of either",
			catalog.reads, len(health.capacityReads))
	}
}

// Every pool's ladder is judged by the rules of the pool that owns it, and the
// whole set costs the reads ONE ladder costs. A read moved inside the pool loop
// is the failure this pins: the page polls every few seconds, so a statement per
// pool would be paid forever.
func TestLadderViewsJudgeEachPoolAndReadOnce(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	catalog := &fakeMachineCatalog{rows: ladderRowsWithGPU()}
	health := &fakeZoneHealth{}
	h := ladderHandler(catalog, []string{"europe-west4-a"}, health)

	views, err := h.ladderViews(t.Context(), ladderPolicy(map[string][]string{
		// The GPU shape is refused in the default pool and admitted in the gpu
		// one; the CPU shape is the other way round.
		host.PoolDefault: {"n4-standard-16", "g2-standard-8"},
		host.PoolGPU:     {"g2-standard-8", "n4-standard-16"},
	}), now)
	if err != nil {
		t.Fatalf("ladderViews: %v", err)
	}
	// Both stored rungs are drawn in each pool, and the one the pool refuses
	// says which rule took it out of the walk.
	if got := rungMachineTypes(views[host.PoolDefault]); len(got) != 2 || got[0] != "n4-standard-16" {
		t.Errorf("default pool = %v, want both stored shapes in the operator's order", got)
	}
	if refused := views[host.PoolDefault][1]; !strings.Contains(refused.Refused, "integrated GPU") {
		t.Errorf("the GPU shape in the CPU pool = %+v, want the integrated-GPU rule named", refused)
	}
	if landing := views[host.PoolDefault][0]; !landing.Landing {
		t.Error("the admitted rung is not marked as the one provisioning would build")
	}
	if got := rungMachineTypes(views[host.PoolGPU]); len(got) != 2 || got[0] != "g2-standard-8" {
		t.Errorf("gpu pool = %v, want both stored shapes in the operator's order", got)
	}
	if refused := views[host.PoolGPU][1]; !strings.Contains(refused.Refused, "carries no GPU") {
		t.Errorf("the CPU shape in the GPU pool = %+v, want the no-accelerator rule named", refused)
	}
	if catalog.reads != 1 || len(health.capacityReads) != 1 || catalog.lookups != 0 {
		t.Fatalf("catalog reads = %d, lookups = %d, stockout reads = %d, want one listing and one stockout read "+
			"for every pool together, and no lookup: every stored rung is in the listing",
			catalog.reads, catalog.lookups, len(health.capacityReads))
	}
	if got := health.capacityReads[0]; len(got) != 2 || got[0] != "n4" || got[1] != "g2" {
		t.Errorf("read covered %v, want [n4 g2], every pool's families in the one read", got)
	}
}

// A stored rung the catalog no longer offers is a preference the operator set
// and no longer gets. Dropped from the view, the ladder simply reads shorter
// than the one they wrote, and the page they open to find out why nothing is
// arriving is the page that hides it.
//
// It is refused in the catalog's own words, because only the catalog knows which
// of its three refusals applies and they have three different remedies. This
// shape wants a capacity override; the generic sentence would send an operator
// looking for a family to enable that is enabled already. It is also the
// sentence the editor refuses the same rung with, so the two cannot disagree.
func TestLadderViewNamesAStoredRungTheCatalogNoLongerOffers(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	// n1-highmem-2 is below the capacity floor and carries no override, so the
	// catalog withholds it: no row in the listing, and a lookup that says why.
	catalog := &fakeMachineCatalog{
		rows:      ladderRows(),
		unoffered: map[string]*machinetype.UnofferedError{"n1-highmem-2": withheldShape("n1-highmem-2", "n1", 2, 13)},
	}
	h := ladderHandler(catalog, []string{"europe-west4-a"}, &fakeZoneHealth{})

	views, err := h.ladderViews(t.Context(), ladderPolicy(map[string][]string{
		host.PoolDefault: {"n4-standard-16", "n1-highmem-2"},
	}), now)
	if err != nil {
		t.Fatalf("ladderViews: %v", err)
	}
	rungs := views[host.PoolDefault]
	if len(rungs) != 2 {
		t.Fatalf("view holds %d rungs, want both the operator stored", len(rungs))
	}
	refused := rungs[1]
	if refused.MachineType != "n1-highmem-2" {
		t.Fatalf("second rung = %q, want the stored order kept", refused.MachineType)
	}
	if !strings.Contains(refused.Refused, "2 vCPUs and 13 GB hold no Android VM") ||
		!strings.Contains(refused.Refused, "per-shape capacity override") {
		t.Errorf("refusal = %q, want the catalog's own reason and the remedy it names", refused.Refused)
	}
	if refused.Slots != 0 || refused.Zones != 0 || refused.Landing {
		t.Errorf("refused rung = %+v, want no numbers beside it: there is no machine to describe", refused)
	}
	if !rungs[0].Landing {
		t.Error("the admitted rung is not the one provisioning would build; the refusal displaced the walk")
	}
	if catalog.lookups != 1 {
		t.Errorf("catalog lookups = %d, want one: the rung the listing does not hold, and nothing else", catalog.lookups)
	}
}

// What the reason costs. The catalog is asked only about a rung its listing does
// not hold, and once per shape however many pools stored it. A lookup per pool,
// or one for a rung the pool refuses by a rule of its own, is a statement paid
// forever on a page that polls every few seconds.
func TestLadderViewAsksTheCatalogOncePerWithheldShape(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	catalog := &fakeMachineCatalog{
		rows:      ladderRowsWithGPU(),
		unoffered: map[string]*machinetype.UnofferedError{"n1-highmem-2": withheldShape("n1-highmem-2", "n1", 2, 13)},
	}
	h := ladderHandler(catalog, []string{"europe-west4-a"}, &fakeZoneHealth{})

	views, err := h.ladderViews(t.Context(), ladderPolicy(map[string][]string{
		// The withheld shape is stored in both pools; the accelerated one is
		// refused in the default pool by the pool's own rule, which needs nobody.
		host.PoolDefault: {"n4-standard-16", "n1-highmem-2", "g2-standard-8"},
		host.PoolGPU:     {"g2-standard-8", "n1-highmem-2"},
	}), now)
	if err != nil {
		t.Fatalf("ladderViews: %v", err)
	}
	for _, pool := range []string{host.PoolDefault, host.PoolGPU} {
		rungs := views[pool]
		if len(rungs) < 2 {
			t.Fatalf("%s pool = %+v, want every stored rung drawn", pool, rungs)
		}
		if refused := rungs[1]; refused.MachineType != "n1-highmem-2" ||
			!strings.Contains(refused.Refused, "hold no Android VM") {
			t.Errorf("%s pool's second rung = %+v, want the withheld shape refused in the catalog's words",
				pool, refused)
		}
	}
	if catalog.lookups != 1 {
		t.Errorf("catalog lookups = %d, want one for the shape both pools stored", catalog.lookups)
	}
}

// rungMachineTypes names the shapes a rendered ladder kept.
func rungMachineTypes(rungs []LadderRungItem) []string {
	types := make([]string, len(rungs))
	for i, rung := range rungs {
		types[i] = rung.MachineType
	}
	return types
}

// With the fleet at or below target nothing is spare, so an ordinal would be a
// threat the system is not making.
func TestShortenOrderIsWithheldWithoutASurplus(t *testing.T) {
	t.Parallel()
	viewer := &fakeFleetViewer{shortenable: []*host.Host{{ID: "cold"}}}
	h := &AdminHandler{keeperLimits: testKeeperLimits, fleetView: viewer}
	hosts := []FleetHostItem{{ID: "cold"}}

	if err := h.stampShortenOrder(t.Context(), hosts, nil); err != nil {
		t.Fatalf("stampShortenOrder: %v", err)
	}
	if hosts[0].ShortenOrder != 0 {
		t.Errorf("order = %d, want none", hosts[0].ShortenOrder)
	}
	if viewer.askedFor != 0 {
		t.Error("the reclamation order was queried with no surplus to reclaim")
	}
}

// The page draws a region per pool, so each pool's two contracts, its gap and
// what is limiting it are all served rather than derived in a console: the
// page's rule is that every number is the server's.
//
// The rows are chosen so the two pools disagree about everything a region
// shows. One is short and converging over cycles, one is exactly met, which a
// snapshot that published the fleet's answer under each pool's name could not
// produce.
func TestPoolsAreServedWithTheirOwnGapAndReason(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	h := gateHandler(heldLease(now), &fakeQuotaService{maxHosts: 40, used: 4})
	view := hostconfig.FleetView{
		Pools: []hostconfig.PoolStatus{
			{Pool: host.PoolDefault, Status: hostconfig.FleetStatus{
				Mode: hostconfig.ModeOn, Target: 8, Standby: 0, Total: 0,
			}},
			{Pool: "gpu", Status: hostconfig.FleetStatus{
				Mode: hostconfig.ModeOn, Target: 2, Standby: 2, Total: 2,
			}},
		},
		Fleet: hostconfig.FleetStatus{Mode: hostconfig.ModeOn, Target: 10, Standby: 2, Total: 2},
	}

	gate, err := h.gateStatus(t.Context(), view, now)
	if err != nil {
		t.Fatalf("gateStatus: %v", err)
	}
	pools := h.fleetPoolItems(view, gate, nil)
	if len(pools) != 2 {
		t.Fatalf("pools = %d, want 2", len(pools))
	}
	if pools[0].Pool != host.PoolDefault || pools[0].Deficit != 8 || pools[0].Reason != GateCapped {
		t.Errorf("the default pool = %+v, want a deficit of 8 converging over cycles", pools[0])
	}
	if pools[1].Pool != "gpu" || pools[1].Deficit != 0 || pools[1].Surplus != 0 || pools[1].Reason != GateTargetMet {
		t.Errorf("the gpu pool = %+v, want its own met target", pools[1])
	}
	// The pool's own picture rides along, so the region can state the two
	// contracts without a second read.
	if pools[1].Status.Target != 2 || pools[1].Status.Standby != 2 {
		t.Errorf("the gpu pool's status = %+v, want the target and standby it was composed with", pools[1].Status)
	}
}

// A cause that belongs to the deployment outranks anything a pool can say about
// itself: with nothing holding the keeper's lease, no pool is acting, however
// exactly its own bounds are met.
func TestAStoppedLoopIsEveryPoolsReason(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	h := gateHandler(nil, &fakeQuotaService{maxHosts: 10, used: 1})
	view := hostconfig.FleetView{
		Pools: []hostconfig.PoolStatus{
			{Pool: host.PoolDefault, Status: hostconfig.FleetStatus{Mode: hostconfig.ModeOn, Target: 2, Standby: 2, Total: 2}},
			{Pool: "gpu", Status: hostconfig.FleetStatus{Mode: hostconfig.ModeOn, Target: 2, Standby: 0}},
		},
		Fleet: hostconfig.FleetStatus{Mode: hostconfig.ModeOn, Target: 4, Standby: 2, Total: 2},
	}

	gate, err := h.gateStatus(t.Context(), view, now)
	if err != nil {
		t.Fatalf("gateStatus: %v", err)
	}
	for _, pool := range h.fleetPoolItems(view, gate, nil) {
		if pool.Reason != GateNoLeader {
			t.Errorf("the %s pool reports %q, want %q, since nothing is cycling anywhere",
				pool.Pool, pool.Reason, GateNoLeader)
		}
	}
}

// The reclamation chips are per pool, and the ordinal is the host's position
// among the candidates of its OWN pool: a single numbering across pools would
// tell the second pool's first victim it was third, and one pool's surplus
// would put chips on another pool's machines.
func TestShortenOrderIsNumberedInsideEachPool(t *testing.T) {
	t.Parallel()
	viewer := &fakeFleetViewer{shortenable: []*host.Host{
		{ID: "default-cold", Pool: host.PoolDefault},
		{ID: "gpu-cold", Pool: "gpu"},
		{ID: "default-cool", Pool: host.PoolDefault},
	}}
	h := &AdminHandler{keeperLimits: testKeeperLimits, fleetView: viewer}
	hosts := []FleetHostItem{{ID: "default-cold"}, {ID: "gpu-cold"}, {ID: "default-cool"}}

	// Only the default pool is spare, and by two.
	err := h.stampShortenOrder(t.Context(), hosts, map[string]int64{host.PoolDefault: 2})
	if err != nil {
		t.Fatalf("stampShortenOrder: %v", err)
	}
	byID := map[string]int64{}
	for _, item := range hosts {
		byID[item.ID] = item.ShortenOrder
	}
	if byID["default-cold"] != 1 || byID["default-cool"] != 2 {
		t.Errorf("order = %+v, want the default pool numbered 1 and 2 from its own candidates", byID)
	}
	if byID["gpu-cold"] != 0 {
		t.Errorf("the gpu pool's machine carries order %d, want none, since that pool holds no surplus", byID["gpu-cold"])
	}
}

// The reclamation cap binds each pool on its own, so what one cycle can move is
// the pools' capped gaps added. Three pools spare by one each are all reclaimed
// in the cycle that reports them, and a gate measuring their total against one
// pool's cap would tell an operator the fleet is converging over cycles after it
// had already finished.
func TestTheSurplusGateCapsEachPoolSeparately(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	h := gateHandler(heldLease(now), &fakeQuotaService{maxHosts: 40, used: 6})
	spare := func(pool string, standby int64) hostconfig.PoolStatus {
		return hostconfig.PoolStatus{Pool: pool, Status: hostconfig.FleetStatus{
			Mode: hostconfig.ModeOn, Standby: standby, Total: standby,
		}}
	}

	whole, err := h.gateStatus(t.Context(), hostconfig.FleetView{
		Pools: []hostconfig.PoolStatus{spare(host.PoolDefault, 1), spare("gpu", 1), spare("arm", 1)},
		Fleet: hostconfig.FleetStatus{Mode: hostconfig.ModeOn, Standby: 3, Total: 3},
	}, now)
	if err != nil {
		t.Fatalf("gateStatus: %v", err)
	}
	if whole.Surplus != 3 {
		t.Fatalf("surplus = %d, want the three pools' gaps added", whole.Surplus)
	}
	if whole.Reason != GateActing {
		t.Errorf("reason = %q, want %q: every pool's one spare host is reclaimed this cycle",
			whole.Reason, GateActing)
	}

	// And a pool whose own surplus outruns the cap still reports it, however
	// little the pool beside it holds.
	capped, err := h.gateStatus(t.Context(), hostconfig.FleetView{
		Pools: []hostconfig.PoolStatus{spare(host.PoolDefault, 5), spare("gpu", 1)},
		Fleet: hostconfig.FleetStatus{Mode: hostconfig.ModeOn, Standby: 6, Total: 6},
	}, now)
	if err != nil {
		t.Fatalf("gateStatus: %v", err)
	}
	if capped.Reason != GateCapped {
		t.Errorf("reason = %q, want %q: one pool is spare by more than a cycle may lower",
			capped.Reason, GateCapped)
	}
}

// The target's ceiling is applied inside each pool against that pool's own
// machines, so the deployment's ceiling is its pools' added. Taken over the
// summed fleet it states a number no pool is ever held to, such as a
// two-machine pool clamped at sixteen under a published ceiling of two hundred
// and fifty, with nothing on the page an operator could reconcile the clamp
// against.
func TestTheTargetCeilingIsEachPoolsOwnAdded(t *testing.T) {
	t.Parallel()
	h := &AdminHandler{keeperLimits: testKeeperLimits}

	limits := h.autoscalerLimits(hostconfig.FleetView{
		Pools: []hostconfig.PoolStatus{
			{Pool: host.PoolDefault, Status: hostconfig.FleetStatus{Total: 2000}},
			{Pool: "gpu", Status: hostconfig.FleetStatus{Total: 2}},
		},
		Fleet: hostconfig.FleetStatus{Total: 2002},
	})
	if want := hostconfig.StandbyCap(2000) + hostconfig.StandbyCap(2); limits.MaxStandbyHosts != want {
		t.Errorf("target ceiling = %d, want %d, each pool's own ceiling added", limits.MaxStandbyHosts, want)
	}
	// The per-cycle budget is the other way round on purpose: it rations a host
	// quota the whole deployment shares, so it stays a share of the whole fleet.
	if want := hostconfig.TopUpBudget(2002); limits.MaxTopUpsPerCycle != want {
		t.Errorf("per-cycle budget = %d, want the whole fleet's share, %d", limits.MaxTopUpsPerCycle, want)
	}

	// With one pool the fold IS that pool, which is what keeps the number the
	// page publishes today exactly what it published before there were pools.
	one := h.autoscalerLimits(singlePoolView(hostconfig.FleetStatus{Total: 40}))
	if want := hostconfig.StandbyCap(40); one.MaxStandbyHosts != want {
		t.Errorf("single-pool ceiling = %d, want %d", one.MaxStandbyHosts, want)
	}
}
