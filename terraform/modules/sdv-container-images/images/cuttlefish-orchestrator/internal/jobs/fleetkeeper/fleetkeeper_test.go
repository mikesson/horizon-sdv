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

package fleetkeeper

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
)

type fakePolicy struct {
	policy hostconfig.FleetPolicy
	err    error
}

func (f fakePolicy) GetFleetPolicy(context.Context) (hostconfig.FleetPolicy, error) {
	if f.err != nil {
		return hostconfig.FleetPolicy{}, f.err
	}
	return f.policy, nil
}

// policyFor builds a policy with one populated pool, the shape the settings
// service always produces on a fleet nobody has partitioned.
func policyFor(mode hostconfig.AutoscaleMode, standby int64) hostconfig.FleetPolicy {
	p := hostconfig.DefaultFleetPolicy()
	p.Mode = mode
	p.Tiers = map[string]hostconfig.FleetTier{hostconfig.DefaultTier: {Standby: standby}}
	return p
}

// poolPolicy builds a policy with a pool per entry, for the rows about how a
// cycle divides itself between them.
func poolPolicy(mode hostconfig.AutoscaleMode, tiers map[string]hostconfig.FleetTier) hostconfig.FleetPolicy {
	p := hostconfig.DefaultFleetPolicy()
	p.Mode = mode
	p.Tiers = tiers
	return p
}

type fakeStandby struct {
	n        int64
	draining int64
	// occupied is the rest of the standing fleet: hosts placement can still land
	// on that devices have filled. The total is derived from all three rather
	// than set, because a fake free to report a total below its own idle count
	// would exercise arithmetic no fleet can produce, and every floor assertion
	// would then be about an impossible world.
	occupied int64
	// pools replaces the three counts above when it is set, so a row can hand
	// the cycle a fleet partitioned several ways.
	pools host.FleetInventory
	err   error
	// calls counts the reads. The cycle opens exactly one however many pools it
	// then decides about, and a listing moved inside the pool loop turns this
	// into the pool count.
	calls *int
}

func (f fakeStandby) StandbyInventory(context.Context, time.Duration) (host.FleetInventory, error) {
	if f.calls != nil {
		*f.calls++
	}
	if f.pools != nil {
		return f.pools, f.err
	}
	return host.FleetInventory{hostconfig.DefaultTier: {
		Standby:  f.n,
		Draining: f.draining,
		Total:    f.n + f.draining + f.occupied,
	}}, f.err
}

// fakeDemand replays a fixed set of misses, relative to now. Every miss is
// stamped SERVABLE unless unservable says otherwise. The keeper's arithmetic is
// about the subset a standby host could have absorbed, so a fake that left the
// facts unstamped would only ever exercise the unjudgeable path.
type fakeDemand struct {
	offsets []time.Duration
	// unservable stamps every replayed miss as requiring a GPU, which no
	// standing capacity answers.
	unservable bool
	partial    bool
	err        error
}

func (f fakeDemand) Demand(context.Context, time.Time) (hostconfig.DemandWindow, error) {
	if f.err != nil {
		return hostconfig.DemandWindow{}, f.err
	}
	now := time.Now()
	fits := true
	window := hostconfig.DemandWindow{
		Misses:  make([]hostconfig.Miss, 0, len(f.offsets)),
		Partial: f.partial,
	}
	for _, d := range f.offsets {
		window.Misses = append(window.Misses, hostconfig.Miss{
			At:              now.Add(d),
			GPURequired:     &f.unservable,
			FitsDefaultHost: &fits,
		})
	}
	return window, nil
}

type countingProvisioner struct {
	calls int
	// pools records which pool each buy was made for, because the machine has
	// to join the pool whose gap paid for it: a buy for one pool that landed in
	// another leaves the gap open next cycle and reads as surplus where it went.
	pools []string
	err   error
	// poolErrs fails the buy for one pool alone: a pool out of GPU quota beside
	// pools that are fine, which is the state a cycle has to survive.
	poolErrs map[string]error
}

func (p *countingProvisioner) ProvisionFleetHost(_ context.Context, pool string) (string, error) {
	p.calls++
	p.pools = append(p.pools, pool)
	if err, failing := p.poolErrs[pool]; failing {
		return "", err
	}
	if p.err != nil {
		return "", p.err
	}
	return fmt.Sprintf("standby-%d", p.calls), nil
}

// countingWarmPlane records how often the keeper drove the prefetch plane.
type countingWarmPlane struct{ advances int }

func (w *countingWarmPlane) Advance(context.Context) { w.advances++ }

// fakeRescuer answers with a fixed count and records what it was asked for.
// The deficits matter as much as the answer, because they are the whole gap the
// renewal must be offered, uncapped, and per pool.
type fakeRescuer struct {
	rescued int64
	// perPool replaces rescued when set, so a row can renew in one pool and not
	// another.
	perPool map[string]int64
	err     error
	// offers is every deficit map the cycle handed over: one per cycle, whatever
	// the fleet is partitioned into.
	offers []map[string]int64
}

func (f *fakeRescuer) RescueFleetHosts(_ context.Context, _ time.Duration, deficits map[string]int64) (map[string]int64, error) {
	f.offers = append(f.offers, deficits)
	if f.perPool != nil {
		return f.perPool, f.err
	}
	return map[string]int64{hostconfig.DefaultTier: f.rescued}, f.err
}

// fakeShortener records every reclamation the keeper asked for. What it was
// asked matters as much as what it answered: the surplus, the grace and the
// exemption set are all decisions the keeper makes, and each has its own way of
// being silently wrong.
type fakeShortener struct {
	shortened int64
	err       error
	surpluses []map[string]int64
	graces    []time.Duration
	baking    []map[string]int64
}

func (f *fakeShortener) ShortenSurplusHosts(_ context.Context, _ time.Duration, surplus map[string]int64, grace time.Duration, baking map[string]int64) (int64, error) {
	f.surpluses = append(f.surpluses, surplus)
	f.graces = append(f.graces, grace)
	f.baking = append(f.baking, baking)
	return f.shortened, f.err
}

// fakeCollapser records every dedicated-empty collapse the keeper asked for.
// The budget and the grace are both keeper decisions with their own ways of
// being silently wrong: one shared with the surplus, one hardcoded.
type fakeCollapser struct {
	collapsed int64
	err       error
	budgets   []int64
	graces    []time.Duration
}

func (f *fakeCollapser) CollapseEmptyDedicated(_ context.Context, _ time.Duration, budget int64, grace time.Duration) (int64, error) {
	f.budgets = append(f.budgets, budget)
	f.graces = append(f.graces, grace)
	return f.collapsed, f.err
}

// fakeBakes reports a fixed set of mid-bake disks.
type fakeBakes struct{ inFlight map[string]int64 }

func (f fakeBakes) BakeInFlight() map[string]int64 { return f.inFlight }

// fakeQuota serves a platform quota row, or fails the read.
type fakeQuota struct {
	maxHosts  int64
	usedHosts int64
	err       error
}

func (f fakeQuota) Get(context.Context, quota.Scope, string) (*quota.Quota, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &quota.Quota{
		Scope: quota.ScopePlatform, ScopeID: quota.GlobalScopeID,
		MaxHosts: f.maxHosts, UsedHosts: f.usedHosts,
	}, nil
}

func keeperFor(policy PolicyReader, standby StandbyCounter, prov Provisioner, opts ...Option) *Service {
	return New(Config{Enabled: true}, policy, standby, prov, nil, opts...)
}

// One cycle per row: what the keeper provisions is exactly the capped deficit,
// and every failure or off state provisions NOTHING. The fail-safe direction
// is spend-nothing, and inverting any arm goes red here.
func TestKeepCycle(t *testing.T) {
	t.Parallel()
	readErr := errors.New("spanner unavailable")
	cases := []struct {
		name           string
		policy         fakePolicy
		standby        fakeStandby
		wantProvisions int
		wantErr        bool
	}{
		{"no standing intent provisions nothing", fakePolicy{policy: policyFor(hostconfig.ModeOn, 0)}, fakeStandby{}, 0, false},
		{"failed policy read provisions nothing", fakePolicy{err: readErr}, fakeStandby{}, 0, true},
		{"failed inventory count provisions nothing", fakePolicy{policy: policyFor(hostconfig.ModeOn, 2)}, fakeStandby{err: readErr}, 0, true},
		{"target met provisions nothing", fakePolicy{policy: policyFor(hostconfig.ModeOn, 2)}, fakeStandby{n: 2}, 0, false},
		{"target exceeded provisions nothing", fakePolicy{policy: policyFor(hostconfig.ModeOn, 1)}, fakeStandby{n: 3}, 0, false},
		{"deficit of one tops up one", fakePolicy{policy: policyFor(hostconfig.ModeOn, 1)}, fakeStandby{n: 0}, 1, false},
		{"deficit is capped per cycle", fakePolicy{policy: policyFor(hostconfig.ModeOn, 10)}, fakeStandby{n: 0}, hostconfig.MinTopUpsPerCycle, false},
		// The two acting modes are equivalent on every scale-OUT path and
		// diverge only where a lease is lowered. Pinned so the equivalence is a
		// stated fact rather than an untested assumption.
		{"provision_only tops up like on", fakePolicy{policy: policyFor(hostconfig.ModeProvisionOnly, 2)}, fakeStandby{n: 0}, hostconfig.MinTopUpsPerCycle, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			prov := &countingProvisioner{}
			err := keeperFor(tc.policy, tc.standby, prov).keep(t.Context())
			if (err != nil) != tc.wantErr {
				t.Errorf("keep() error = %v, wantErr %v", err, tc.wantErr)
			}
			if prov.calls != tc.wantProvisions {
				t.Errorf("provisions = %d, want %d", prov.calls, tc.wantProvisions)
			}
		})
	}
}

// This is the one thing `off` uniquely gates, and no other assertion reaches
// it: every other row above provisions nothing because the arithmetic reached
// zero. Here the arithmetic asks for two hosts and the MODE refuses, which is
// what makes an observation week measure unbuffered demand rather than the
// buffer answering it. Deleting the mode check leaves every other test green.
func TestOffProvisionsNothingUnderADeficit(t *testing.T) {
	t.Parallel()
	prov := &countingProvisioner{}
	err := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOff, 4)},
		fakeStandby{n: 0},
		prov,
	).keep(t.Context())
	if err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if prov.calls != 0 {
		t.Errorf("provisions = %d, want 0; off must not create a host", prov.calls)
	}
}

// Where the warm advance sits inside the cycle decides whether warming
// happens at all. It must run when the target is already met, because a full
// complement is exactly when there is inventory worth warming, and it must not
// run when there is no target at all or the world could not be read. Moving
// the call below the deficit's early return compiles, satisfies every other
// row here, and silently stops warming in the steady state.
func TestKeepWarmAdvance(t *testing.T) {
	t.Parallel()
	readErr := errors.New("spanner unavailable")
	cases := []struct {
		name        string
		policy      fakePolicy
		standby     fakeStandby
		wantAdvance int
	}{
		{"a met target still warms", fakePolicy{policy: policyFor(hostconfig.ModeOn, 2)}, fakeStandby{n: 2}, 1},
		{"an exceeded target still warms", fakePolicy{policy: policyFor(hostconfig.ModeOn, 1)}, fakeStandby{n: 3}, 1},
		{"a deficit warms as well as tops up", fakePolicy{policy: policyFor(hostconfig.ModeOn, 2)}, fakeStandby{n: 0}, 1},
		// Warming spends nothing and creates nothing, so the mode is not what
		// it answers to: a fleet standing under `off` is still worth warming.
		{"off still warms a fleet it is holding", fakePolicy{policy: policyFor(hostconfig.ModeOff, 2)}, fakeStandby{n: 2}, 1},
		{"no target warms nothing", fakePolicy{policy: policyFor(hostconfig.ModeOn, 0)}, fakeStandby{}, 0},
		{"a failed policy read warms nothing", fakePolicy{err: readErr}, fakeStandby{}, 0},
		{"a failed inventory count warms nothing", fakePolicy{policy: policyFor(hostconfig.ModeOn, 2)}, fakeStandby{err: readErr}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			warm := &countingWarmPlane{}
			s := keeperFor(tc.policy, tc.standby, &countingProvisioner{}, WithWarmPlane(warm))
			_ = s.keep(t.Context())
			if warm.advances != tc.wantAdvance {
				t.Errorf("warm advances = %d, want %d", warm.advances, tc.wantAdvance)
			}
		})
	}
}

// Demand raises the target above the operator's standing intent, and the
// keeper acts on the composed number rather than on the intent alone. With
// standing intent 0, only demand can produce a top-up here, so a keeper
// reading the tier directly provisions nothing and goes red.
func TestDemandRaisesTheTarget(t *testing.T) {
	t.Parallel()
	leg := host.ProvisioningLeg
	prov := &countingProvisioner{}
	// Three misses inside one leg: three hosts were in flight at once.
	demand := fakeDemand{offsets: []time.Duration{-leg / 4, -leg / 3, -leg / 2}}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 0)},
		fakeStandby{n: 0},
		prov,
		WithDemandReader(demand),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if prov.calls != hostconfig.MinTopUpsPerCycle {
		t.Errorf("provisions = %d, want %d (a target of 3, capped per cycle)", prov.calls, hostconfig.MinTopUpsPerCycle)
	}
}

// The per-cycle provisioning budget is a share of the fleet already standing,
// so a large fleet closes a large deficit in one cycle where a small one still
// converges two hosts at a time. The bound is proportional because four hosts
// a minute cannot build a fleet of thousands.
//
// It fails if the budget is ever wired back to a constant, because then only
// two of the twenty would be provisioned.
func TestTopUpBudgetTracksTheStandingFleet(t *testing.T) {
	t.Parallel()
	// 640 hosts already occupied, so the cycle may provision a thirty-second of
	// them, and a standing intent of 20 is well under the target's own ceiling.
	prov := &countingProvisioner{}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 20)},
		fakeStandby{n: 0, occupied: 640},
		prov,
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if prov.calls != 20 {
		t.Errorf("provisions = %d, want 20 (a deficit of 20 under a budget of %d)",
			prov.calls, hostconfig.TopUpBudget(640))
	}
}

// Demand the fleet cannot serve must never raise the target. These misses are
// identical to the ones above in count and spacing, and differ only in being
// beyond what a standby host can answer. Buying hosts for them would spend
// money and change nothing, because the very next such create would miss
// placement on the hosts just bought.
//
// It is the claim the whole servability split exists to make, and the one most
// likely to be undone by someone reasoning that a miss is a miss.
func TestUnservableDemandNeverRaisesTheTarget(t *testing.T) {
	t.Parallel()
	leg := host.ProvisioningLeg
	prov := &countingProvisioner{}
	demand := fakeDemand{
		offsets:    []time.Duration{-leg / 4, -leg / 3, -leg / 2},
		unservable: true,
	}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 0)},
		fakeStandby{n: 0},
		prov,
		WithDemandReader(demand),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if prov.calls != 0 {
		t.Errorf("provisions = %d, want 0; no standby host could have served any of these", prov.calls)
	}
}

// A disabled horizon is an operator declining to let observed demand raise the
// target, and it must be honored even with a reader wired and misses to read.
// Reading the window regardless and only ignoring the answer would pass a
// target assertion while still paying for the query every cycle.
func TestDisabledHorizonIgnoresDemand(t *testing.T) {
	t.Parallel()
	policy := policyFor(hostconfig.ModeOn, 0)
	policy.DemandHorizonSec = 0
	prov := &countingProvisioner{}
	s := keeperFor(
		fakePolicy{policy: policy},
		fakeStandby{n: 0},
		prov,
		WithDemandReader(fakeDemand{offsets: []time.Duration{-time.Second, -2 * time.Second}}),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if prov.calls != 0 {
		t.Errorf("provisions = %d, want 0; a zero horizon disables the demand signal", prov.calls)
	}
}

// A failed demand read provisions nothing, like every other failed read: the
// alternative is composing a target from a signal that silently read zero,
// which under-provisions exactly when the audit table is struggling.
func TestFailedDemandReadProvisionsNothing(t *testing.T) {
	t.Parallel()
	prov := &countingProvisioner{}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 2)},
		fakeStandby{n: 0},
		prov,
		WithDemandReader(fakeDemand{err: errors.New("audit unavailable")}),
	)
	if err := s.keep(t.Context()); err == nil {
		t.Error("keep() must report the failed demand read")
	}
	if prov.calls != 0 {
		t.Errorf("provisions = %d, want 0", prov.calls)
	}
}

// The published status is what the operations card renders, and it must report
// the inputs beside the answer. A target with no standby, draining or miss
// count beside it cannot be checked by the operator reading it.
func TestStatusPublishesTheInputsBesideTheTarget(t *testing.T) {
	t.Parallel()
	leg := host.ProvisioningLeg
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeProvisionOnly, 1)},
		fakeStandby{n: 3, draining: 2},
		&countingProvisioner{},
		WithDemandReader(fakeDemand{offsets: []time.Duration{-leg / 4, -leg / 3}}),
	)
	view, _, err := s.observe(t.Context())
	if err != nil {
		t.Fatalf("observe() error = %v", err)
	}
	status := view.Fleet
	if status.Mode != hostconfig.ModeProvisionOnly {
		t.Errorf("mode = %q, want provision_only", status.Mode)
	}
	if status.Standby != 3 || status.Draining != 2 {
		t.Errorf("standby/draining = %d/%d, want 3/2", status.Standby, status.Draining)
	}
	if status.Misses != 2 {
		t.Errorf("misses = %d, want 2", status.Misses)
	}
	// Demand (2) beats standing intent (1), and says so.
	if status.Target != 2 || status.Signal != hostconfig.SignalDemand {
		t.Errorf("target/signal = %d/%q, want 2/demand", status.Target, status.Signal)
	}
}

// alwaysOpen is a daily window covering the whole day, so it is open at
// whatever instant the keeper reads. The keeper has no clock boundary and
// needs none. Which side of a window edge an instant falls on is decided in
// the domain and tested there; what is tested HERE is that the keeper hands
// the schedules to the composition at all.
func alwaysOpen(hosts int64) hostconfig.FleetSchedule {
	return hostconfig.FleetSchedule{
		Name:        "all day",
		Hosts:       hosts,
		Start:       "00:00",
		DurationSec: 24 * 3600,
		Location:    "UTC",
	}
}

// A schedule raises the published target above the operator's standing intent,
// and the keeper acts on the composed number. With standing intent 0 and no
// demand reader, only the schedule can produce a top-up, so a keeper that
// reads the tier's standby and drops its windows provisions nothing and goes
// red.
func TestScheduleRaisesTheTarget(t *testing.T) {
	t.Parallel()
	policy := policyFor(hostconfig.ModeOn, 0)
	policy.Tiers = map[string]hostconfig.FleetTier{
		hostconfig.DefaultTier: {Standby: 0, Schedules: []hostconfig.FleetSchedule{alwaysOpen(3)}},
	}
	prov := &countingProvisioner{}
	s := keeperFor(fakePolicy{policy: policy}, fakeStandby{n: 0}, prov)

	view, _, err := s.observe(t.Context())
	if err != nil {
		t.Fatalf("observe() error = %v", err)
	}
	status := view.Fleet
	if status.Target != 3 || status.Signal != hostconfig.SignalSchedule {
		t.Errorf("target/signal = %d/%q, want 3/schedule", status.Target, status.Signal)
	}
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if prov.calls != hostconfig.MinTopUpsPerCycle {
		t.Errorf("provisions = %d, want %d (a target of 3, capped per cycle)", prov.calls, hostconfig.MinTopUpsPerCycle)
	}
}

// Step 3 ships in off, which means the card publishes a target a schedule
// raised and nothing acts on it. Both halves are the claim: a mode gate that
// also suppressed the computation would leave the observation week reading
// zero through every window an operator configured.
func TestOffPublishesAScheduledTargetWithoutActingOnIt(t *testing.T) {
	t.Parallel()
	policy := policyFor(hostconfig.ModeOff, 0)
	policy.Tiers = map[string]hostconfig.FleetTier{
		hostconfig.DefaultTier: {Standby: 0, Schedules: []hostconfig.FleetSchedule{alwaysOpen(4)}},
	}
	prov := &countingProvisioner{}
	s := keeperFor(fakePolicy{policy: policy}, fakeStandby{n: 0}, prov)

	view, _, err := s.observe(t.Context())
	if err != nil {
		t.Fatalf("observe() error = %v", err)
	}
	status := view.Fleet
	if status.Target != 4 {
		t.Errorf("target = %d, want 4; off still publishes", status.Target)
	}
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if prov.calls != 0 {
		t.Errorf("provisions = %d, want 0; off must not act on a scheduled target", prov.calls)
	}
}

// No warm plane means target-holding only, and the keeper must not care. This
// is the wiring a deployment without prefetch uses.
func TestKeepWithoutWarmPlaneStillTopsUp(t *testing.T) {
	t.Parallel()
	prov := &countingProvisioner{}
	err := keeperFor(fakePolicy{policy: policyFor(hostconfig.ModeOn, 1)}, fakeStandby{n: 0}, prov).keep(t.Context())
	if err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if prov.calls != 1 {
		t.Errorf("provisions = %d, want 1", prov.calls)
	}
}

// A deficit is met from the fleet's own draining hosts before any VM is bought:
// a renewed host is warm capacity inside this cycle where a top-up is a cold VM
// and a provisioning leg. Both halves are the claim: the renewal is offered the
// WHOLE deficit, and what it returns comes off what is provisioned. A keeper
// that renewed and then provisioned the original deficit anyway overshoots the
// target every time demand rises.
func TestRescueMeetsTheDeficitBeforeProvisioning(t *testing.T) {
	t.Parallel()
	prov := &countingProvisioner{}
	resc := &fakeRescuer{rescued: 2}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeProvisionOnly, 3)},
		fakeStandby{n: 0},
		prov,
		WithRescuer(resc),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if len(resc.offers) != 1 || resc.offers[0][hostconfig.DefaultTier] != 3 {
		t.Errorf("rescue offers = %v, want one call offered the whole deficit of 3", resc.offers)
	}
	if prov.calls != 1 {
		t.Errorf("provisions = %d, want 1 (a deficit of 3 less the 2 renewed)", prov.calls)
	}
}

// Renewal covering the whole deficit buys nothing at all. The fleet is back at
// target without a VM being created.
func TestRescueCoveringTheDeficitProvisionsNothing(t *testing.T) {
	t.Parallel()
	prov := &countingProvisioner{}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeProvisionOnly, 2)},
		fakeStandby{n: 0},
		prov,
		WithRescuer(&fakeRescuer{rescued: 2}),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if prov.calls != 0 {
		t.Errorf("provisions = %d, want 0; the deficit was met by renewal", prov.calls)
	}
}

// Renewal is not subject to the per-cycle provisioning budget, and showing
// that needs a deficit above the cap: the renewal is offered six where a top-up
// would be capped at two. It creates no VM and lowers no lease, and the
// deficit, itself clamped by MaxStandbyHosts, is the only bound it needs.
// Capping it would leave the fleet buying cold hosts while warm ones drained
// away beside them.
func TestRescueIsNotCappedPerCycle(t *testing.T) {
	t.Parallel()
	prov := &countingProvisioner{}
	resc := &fakeRescuer{rescued: 6}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 6)},
		fakeStandby{n: 0},
		prov,
		WithRescuer(resc),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if len(resc.offers) != 1 || resc.offers[0][hostconfig.DefaultTier] != 6 {
		t.Errorf("rescue offers = %v, want the full deficit of 6, uncapped", resc.offers)
	}
	if prov.calls != 0 {
		t.Errorf("provisions = %d, want 0", prov.calls)
	}
}

// A failed renewal does NOT abandon the cycle, which is the one place this job
// departs from "any failure spends nothing", and the difference is what failed.
// The reads that produced the deficit succeeded, so the deficit is still true;
// only one way of meeting it broke. The count returned with the error is what
// actually committed, so a partial renewal is subtracted rather than provisioned
// over.
func TestARescueFailureStillProvisionsTheRemainder(t *testing.T) {
	t.Parallel()
	prov := &countingProvisioner{}
	resc := &fakeRescuer{rescued: 1, err: errors.New("spanner unavailable")}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 4)},
		fakeStandby{n: 0},
		prov,
		WithRescuer(resc),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v; a failed renewal must not abandon the cycle", err)
	}
	// Deficit 4, one renewed before the failure, three left, capped at two.
	if prov.calls != hostconfig.MinTopUpsPerCycle {
		t.Errorf("provisions = %d, want %d", prov.calls, hostconfig.MinTopUpsPerCycle)
	}
}

// A surplus is given back by shortening leases, capped per cycle. This is the
// only thing in the keeper that can shorten a host's life, so it is also the
// only assertion that separates `on` from `provision_only` in behavior rather
// than in vocabulary.
func TestSurplusShortensUnderOn(t *testing.T) {
	t.Parallel()
	short := &fakeShortener{shortened: 2}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 1)},
		fakeStandby{n: 3},
		&countingProvisioner{},
		WithShortener(short),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if len(short.surpluses) != 1 || short.surpluses[0][hostconfig.DefaultTier] != 2 {
		t.Errorf("shorten surpluses = %v, want one call for the surplus of 2", short.surpluses)
	}
}

// The surplus is capped per cycle, so a target dropped from full to nothing
// converges over cycles instead of reclaiming the whole fleet at once, and a
// target set wrong and corrected a minute later costs two hosts, not sixteen.
func TestShortenIsCappedPerCycle(t *testing.T) {
	t.Parallel()
	short := &fakeShortener{}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 0)},
		fakeStandby{n: 9},
		&countingProvisioner{},
		WithShortener(short),
	)
	// A target of zero would return before the surplus arm if the deficit branch
	// still owned the early return, so this row also pins the cycle's shape.
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if len(short.surpluses) != 1 || short.surpluses[0][hostconfig.DefaultTier] != MaxShortensPerCycle {
		t.Errorf("shorten surpluses = %v, want one call capped at %d", short.surpluses, MaxShortensPerCycle)
	}
}

// Surplus acts on nothing under provision_only, and this is now a real gate
// rather than arithmetic. The assertion is the mode's contract, and it is the
// shape the dedicated collapse has to satisfy too when it lands: a mode that may
// only add capacity must not reach any path that gives capacity back.
func TestProvisionOnlyActsOnNothingUnderSurplus(t *testing.T) {
	t.Parallel()
	prov := &countingProvisioner{}
	resc := &fakeRescuer{rescued: 2}
	short := &fakeShortener{shortened: 2}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeProvisionOnly, 1)},
		fakeStandby{n: 3},
		prov,
		WithRescuer(resc),
		WithShortener(short),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if prov.calls != 0 {
		t.Errorf("provisions = %d, want 0 under surplus", prov.calls)
	}
	if len(resc.offers) != 0 {
		t.Errorf("rescue was called %v times under surplus; there is no deficit to meet", len(resc.offers))
	}
	if len(short.surpluses) != 0 {
		t.Errorf("shorten was called %v times under provision_only; that mode may never lower a lease", short.surpluses)
	}
}

// `off` reaches no actuating path in either direction. The surplus arm is the
// second half of what off means, and nothing else asserts it: every other off
// assertion is about a deficit.
func TestOffShortensNothingUnderSurplus(t *testing.T) {
	t.Parallel()
	short := &fakeShortener{shortened: 2}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOff, 1)},
		fakeStandby{n: 4},
		&countingProvisioner{},
		WithShortener(short),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if len(short.surpluses) != 0 {
		t.Errorf("shorten was called %v times under off", short.surpluses)
	}
}

// The mid-bake exemption reaches the shortener. A host mid-bake holds no
// artifact-ledger rows until the bake finishes, so it reads as the coldest host
// in the fleet and is chosen first for destruction, which is exactly backwards
// since it is the host closest to becoming warm. Wiring the plane as a
// WarmPlane and forgetting to wire it as a BakeReader compiles, warms
// correctly, and shortens the wrong hosts forever.
func TestShortenCarriesTheMidBakeExemption(t *testing.T) {
	t.Parallel()
	short := &fakeShortener{}
	baking := map[string]int64{"host-a": 3}
	s := keeperFor(
		// A target above zero, so this cycle walks the plane: the exemption is
		// only read from a walk that ran. A surplus against a standing target is
		// the ordinary case: the operator lowered the number, and did not
		// switch the fleet off.
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 1)},
		fakeStandby{n: 3},
		&countingProvisioner{},
		WithWarmPlane(&countingWarmPlane{}),
		WithShortener(short),
		WithBakeReader(fakeBakes{inFlight: baking}),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if len(short.baking) != 1 || short.baking[0]["host-a"] != 3 {
		t.Errorf("exemption = %v, want host-a at incarnation 3", short.baking)
	}
}

// A cycle that did not walk the plane must not read its in-flight set.
//
// The warm walk is gated on a target above zero, so dropping the target to
// zero, the loudest reclamation an operator can ask for, stops the walk and
// freezes whatever it was baking. Consulted anyway, that frozen set exempts
// the very hosts the operator asked for, and the bookkeeping being in-memory
// it goes on exempting them until the process restarts: the fleet never fully
// drains and nothing anywhere reports why.
//
// Found by the robustness harness, which reclaimed every host but the one or two
// that happened to be mid-bake when the target dropped.
func TestShortenIgnoresAStaleExemption(t *testing.T) {
	t.Parallel()
	short := &fakeShortener{}
	warm := &countingWarmPlane{}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 0)},
		fakeStandby{n: 2},
		&countingProvisioner{},
		WithWarmPlane(warm),
		WithShortener(short),
		WithBakeReader(fakeBakes{inFlight: map[string]int64{"host-a": 3}}),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if warm.advances != 0 {
		t.Fatalf("warm advances = %d, want 0: the premise of this test is that no walk ran", warm.advances)
	}
	if len(short.surpluses) != 1 {
		t.Fatalf("shorten calls = %v, want exactly one", short.surpluses)
	}
	if len(short.baking[0]) != 0 {
		t.Errorf("exemption = %v, want none: no walk ran this cycle", short.baking[0])
	}
}

// The grace a shortened host keeps is the operator's, read from the same policy
// the target was composed from. Reading it a second time would let one cycle act
// on two different policies; hardcoding the default would make the setting
// decorative, which no assertion on the target would notice.
func TestShortenUsesTheConfiguredDrainGrace(t *testing.T) {
	t.Parallel()
	policy := policyFor(hostconfig.ModeOn, 0)
	policy.DrainGraceSec = 300
	short := &fakeShortener{}
	s := keeperFor(fakePolicy{policy: policy}, fakeStandby{n: 1}, &countingProvisioner{}, WithShortener(short))
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if len(short.graces) != 1 || short.graces[0] != 5*time.Minute {
		t.Errorf("graces = %v, want one call at 5m", short.graces)
	}
}

// A zero grace would write an expiry of now and hand the whole surplus to the
// reaper at once. The policy reader is contracted never to produce one, so this
// asserts the arm that catches a contract violation rather than a reachable
// state. It is cheap, against a failure that destroys capacity instantly.
func TestAZeroDrainGraceFallsBackToTheCompiledDefault(t *testing.T) {
	t.Parallel()
	policy := policyFor(hostconfig.ModeOn, 0)
	policy.DrainGraceSec = 0
	short := &fakeShortener{}
	s := keeperFor(fakePolicy{policy: policy}, fakeStandby{n: 1}, &countingProvisioner{}, WithShortener(short))
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	want := time.Duration(hostconfig.DefaultDrainGraceSec) * time.Second
	if len(short.graces) != 1 || short.graces[0] != want {
		t.Errorf("graces = %v, want one call at %v", short.graces, want)
	}
}

// An unwired shortener leaves a surplus to run out its own leases, which is what
// the fleet did before reclamation existed and is the safe reading of a missing
// dependency.
func TestSurplusWithoutAShortenerIsHarmless(t *testing.T) {
	t.Parallel()
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 1)},
		fakeStandby{n: 3},
		&countingProvisioner{},
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
}

// A failed shorten is reported. A failed renewal leaves a true deficit the
// top-up can still meet, but nothing else in this cycle can act on a surplus,
// so there is no remainder to carry on with.
func TestAShortenFailureIsReported(t *testing.T) {
	t.Parallel()
	short := &fakeShortener{shortened: 1, err: errors.New("spanner unavailable")}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 1)},
		fakeStandby{n: 4},
		&countingProvisioner{},
		WithShortener(short),
	)
	if err := s.keep(t.Context()); err == nil {
		t.Error("keep() must report the failed shorten")
	}
}

// A dedicated host is not inventory, so it does not belong to either arm of the
// deficit branch and the collapse runs whatever that branch is about to do.
//
// The below-target row is the one that matters and the one an implementation
// gets wrong for free: a collapse written inside the deficit branch never
// reclaims an idle exclusive VM while the fleet is short, which is the moment
// the cycle is about to buy capacity while holding capacity nobody can reach.
//
// The no-fleet row is not a corner case either. A deployment running with
// standby 0 has no fleet arithmetic at all, and the collapse is the only thing
// in this job that still has work to do there.
func TestCollapseRunsWhateverTheDeficitBranchDoes(t *testing.T) {
	t.Parallel()
	readErr := errors.New("spanner unavailable")
	cases := []struct {
		name         string
		policy       fakePolicy
		standby      fakeStandby
		wantCollapse int
	}{
		{"below target", fakePolicy{policy: policyFor(hostconfig.ModeOn, 2)}, fakeStandby{n: 0}, 1},
		{"at target", fakePolicy{policy: policyFor(hostconfig.ModeOn, 2)}, fakeStandby{n: 2}, 1},
		{"above target", fakePolicy{policy: policyFor(hostconfig.ModeOn, 1)}, fakeStandby{n: 3}, 1},
		{"no fleet at all", fakePolicy{policy: policyFor(hostconfig.ModeOn, 0)}, fakeStandby{}, 1},
		// The world could not be read, so nothing is known about it. This is
		// the same fail-safe every other actuation here answers to.
		{"a failed policy read collapses nothing", fakePolicy{err: readErr}, fakeStandby{}, 0},
		{"a failed inventory count collapses nothing", fakePolicy{policy: policyFor(hostconfig.ModeOn, 2)}, fakeStandby{err: readErr}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			coll := &fakeCollapser{collapsed: 1}
			s := keeperFor(tc.policy, tc.standby, &countingProvisioner{}, WithCollapser(coll))
			_ = s.keep(t.Context())
			if len(coll.budgets) != tc.wantCollapse {
				t.Errorf("collapse calls = %d, want %d", len(coll.budgets), tc.wantCollapse)
			}
		})
	}
}

// The two reclamations hold separate budgets, and the surplus here is deep
// enough that a shared one could not serve both: nine hosts over target, so a
// pooled cap is spent entirely on the surplus and the collapse gets nothing.
//
// They answer to unrelated conditions, one to a target the operator lowered
// and the other to groups that ended, and starving either behind the other
// means idle exclusive VMs surviving for exactly as long as a fleet drain
// lasts.
func TestTheTwoReclamationBudgetsAreSeparate(t *testing.T) {
	t.Parallel()
	short := &fakeShortener{shortened: MaxShortensPerCycle}
	coll := &fakeCollapser{collapsed: MaxCollapsesPerCycle}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 0)},
		fakeStandby{n: 9},
		&countingProvisioner{},
		WithShortener(short),
		WithCollapser(coll),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if len(short.surpluses) != 1 || short.surpluses[0][hostconfig.DefaultTier] != MaxShortensPerCycle {
		t.Errorf("shorten surpluses = %v, want one call capped at %d", short.surpluses, MaxShortensPerCycle)
	}
	if len(coll.budgets) != 1 || coll.budgets[0] != MaxCollapsesPerCycle {
		t.Errorf("collapse budgets = %v, want one call at %d in the same cycle", coll.budgets, MaxCollapsesPerCycle)
	}
}

// The collapse lowers a lease, so it answers to the same gate the surplus
// does, and nothing else in this file can catch that gate going missing.
//
// Every existing mode assertion counts calls to the SHORTENER. The collapse
// reaches the world through a different port, so an unwired gate here gives
// hosts back under a mode whose whole meaning is that it never does. Verified by
// removing the gate: TestProvisionOnlyActsOnNothingUnderSurplus,
// TestOffShortensNothingUnderSurplus, TestOffProvisionsNothingUnderADeficit and
// TestKeepCycle all stayed green, and only the two rows below went red.
//
// `on` is a row rather than an assumption, so the table proves it can fail.
func TestOnlyOnCollapses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode         hostconfig.AutoscaleMode
		wantCollapse int
	}{
		{hostconfig.ModeOff, 0},
		{hostconfig.ModeProvisionOnly, 0},
		{hostconfig.ModeOn, 1},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			t.Parallel()
			coll := &fakeCollapser{collapsed: 1}
			s := keeperFor(
				fakePolicy{policy: policyFor(tc.mode, 2)},
				fakeStandby{n: 0},
				&countingProvisioner{},
				WithCollapser(coll),
			)
			if err := s.keep(t.Context()); err != nil {
				t.Fatalf("keep() error = %v", err)
			}
			if len(coll.budgets) != tc.wantCollapse {
				t.Errorf("collapse calls under %s = %d, want %d", tc.mode, len(coll.budgets), tc.wantCollapse)
			}
		})
	}
}

// The grace a collapsed host keeps is the operator's, read from the same policy
// the rest of the cycle used. Hardcoding the default would make the setting
// decorative on this path alone, which every assertion about the surplus still
// passes.
func TestCollapseUsesTheConfiguredDrainGrace(t *testing.T) {
	t.Parallel()
	policy := policyFor(hostconfig.ModeOn, 0)
	policy.DrainGraceSec = 300
	coll := &fakeCollapser{}
	s := keeperFor(fakePolicy{policy: policy}, fakeStandby{}, &countingProvisioner{}, WithCollapser(coll))
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if len(coll.graces) != 1 || coll.graces[0] != 5*time.Minute {
		t.Errorf("graces = %v, want one call at 5m", coll.graces)
	}
}

// A failed collapse does not abandon the cycle, matching the renewal rather than
// the surplus. The difference is what the failure tells you: the reads that
// produced the target still succeeded, so a fleet below target is still below
// target, and refusing to buy capacity because an unrelated reclamation could
// not run leaves the deficit standing for as long as the failure lasts.
func TestACollapseFailureStillTopsUp(t *testing.T) {
	t.Parallel()
	prov := &countingProvisioner{}
	coll := &fakeCollapser{err: errors.New("spanner unavailable")}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 2)},
		fakeStandby{n: 0},
		prov,
		WithCollapser(coll),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v; a failed collapse must not abandon the cycle", err)
	}
	if prov.calls != hostconfig.MinTopUpsPerCycle {
		t.Errorf("provisions = %d, want %d", prov.calls, hostconfig.MinTopUpsPerCycle)
	}
}

// The buffer never spends the host quota's last slot. One free slot against a
// real deficit: nothing is bought, and the slot stays there for the user create
// that would otherwise have taken a 429, the failure that cannot be un-failed.
func TestQuotaReserveHoldsBackTheLastSlot(t *testing.T) {
	t.Parallel()
	prov := &countingProvisioner{}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 4)},
		fakeStandby{n: 0},
		prov,
		WithQuotaReader(fakeQuota{maxHosts: 10, usedHosts: 9}),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if prov.calls != 0 {
		t.Errorf("provisions = %d, want 0; the last quota slot is reserved", prov.calls)
	}
}

// Headroom above the reserve is spendable down TO the reserve and not through
// it. The numbers are chosen so the reserve binds BELOW the per-cycle cap: two
// free slots, one spendable, and a deficit of four. With the cap doing the work
// instead this reads 2 and the reserve arithmetic is untested.
func TestQuotaReserveCapsThisCyclesTopUps(t *testing.T) {
	t.Parallel()
	prov := &countingProvisioner{}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 4)},
		fakeStandby{n: 0},
		prov,
		WithQuotaReader(fakeQuota{maxHosts: 10, usedHosts: 8}),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if prov.calls != 1 {
		t.Errorf("provisions = %d, want 1 (two free slots, one of them reserved)", prov.calls)
	}
}

// The reserve does not gate renewal, and the ordering that makes that true is
// the point. A draining host already holds its quota slot, so refusing to renew
// it while quota is tight gives the slot up for nothing: the host is reaped, the
// fleet is one host further from target, and nobody gains a slot.
func TestTheQuotaReserveDoesNotBlockRenewal(t *testing.T) {
	t.Parallel()
	prov := &countingProvisioner{}
	resc := &fakeRescuer{rescued: 2}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 4)},
		fakeStandby{n: 0},
		prov,
		WithRescuer(resc),
		WithQuotaReader(fakeQuota{maxHosts: 10, usedHosts: 10}),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if len(resc.offers) != 1 {
		t.Errorf("rescue calls = %d, want 1; an exhausted quota must not stop a renewal", len(resc.offers))
	}
	if prov.calls != 0 {
		t.Errorf("provisions = %d, want 0; the quota is exhausted", prov.calls)
	}
}

// A failed quota read provisions nothing, like every other failed read of the
// world: the alternative is spending against a ceiling nobody could confirm.
func TestFailedQuotaReadProvisionsNothing(t *testing.T) {
	t.Parallel()
	prov := &countingProvisioner{}
	s := keeperFor(
		fakePolicy{policy: policyFor(hostconfig.ModeOn, 4)},
		fakeStandby{n: 0},
		prov,
		WithQuotaReader(fakeQuota{err: errors.New("spanner unavailable")}),
	)
	if err := s.keep(t.Context()); err == nil {
		t.Error("keep() must report the failed quota read")
	}
	if prov.calls != 0 {
		t.Errorf("provisions = %d, want 0", prov.calls)
	}
}

// A provision failure stops that pool's buying for the cycle and is reported.
// The next cycle re-reads the world rather than hammering a failing provision
// path within one.
func TestKeepStopsBuyingForAPoolWhoseProvisionFailed(t *testing.T) {
	t.Parallel()
	prov := &countingProvisioner{err: errors.New("quota exhausted")}
	err := keeperFor(fakePolicy{policy: policyFor(hostconfig.ModeOn, 5)}, fakeStandby{n: 0}, prov).keep(t.Context())
	if err == nil {
		t.Error("keep() must report the failed provision")
	}
	if prov.calls != 1 {
		t.Errorf("provision attempts = %d, want 1 (stop on first failure)", prov.calls)
	}
}

// A pool that cannot be bought for does not take the rest of the cycle with it.
// The gpu pool is short by three and every buy for it fails; the default pool is
// short by two and still gets its host, and the spare pool still gives its
// surplus back in the same cycle.
//
// The gpu pool is allocated FIRST here, for being the deeper deficit, which is
// what made this the whole cycle's problem rather than its own: a keeper that
// returns on the first failed buy leaves every other pool short and reclaims
// nothing anywhere, every thirty seconds, for as long as one pool has no machine
// to buy.
func TestAFailedBuyInOnePoolStillServesTheOthers(t *testing.T) {
	t.Parallel()
	prov := &countingProvisioner{poolErrs: map[string]error{host.PoolGPU: errors.New("no GPU quota")}}
	short := &fakeShortener{shortened: 2}
	s := keeperFor(
		fakePolicy{policy: poolPolicy(hostconfig.ModeOn, map[string]hostconfig.FleetTier{
			hostconfig.DefaultTier: {Standby: 2},
			host.PoolGPU:           {Standby: 3},
			host.PoolARM:           {Standby: 0},
		})},
		fakeStandby{pools: host.FleetInventory{
			hostconfig.DefaultTier: {Standby: 0, Total: 0},
			host.PoolGPU:           {Standby: 0, Total: 0},
			host.PoolARM:           {Standby: 2, Total: 2},
		}},
		prov,
		WithShortener(short),
	)
	if err := s.keep(t.Context()); err == nil {
		t.Error("keep() must report the failed buy")
	}
	bought := map[string]int{}
	for _, pool := range prov.pools {
		bought[pool]++
	}
	if bought[host.PoolGPU] != 1 {
		t.Errorf("gpu buys = %d, want 1: the pool that failed is not retried inside the cycle", bought[host.PoolGPU])
	}
	if bought[hostconfig.DefaultTier] != 1 {
		t.Errorf("bought %v, want the default pool's host bought after the gpu pool's buy failed", bought)
	}
	if len(short.surpluses) != 1 || short.surpluses[0][host.PoolARM] != MaxShortensPerCycle {
		t.Errorf("reclamation = %v, want the spare pool's surplus given back in the same cycle", short.surpluses)
	}
}

// floorPolicy is a policy holding the floor and nothing else: no standing
// intent, so every host bought below is bought by the floor alone and no
// buffer arithmetic can account for it.
func floorPolicy(mode hostconfig.AutoscaleMode, floor int64) hostconfig.FleetPolicy {
	return poolPolicy(mode, map[string]hostconfig.FleetTier{hostconfig.DefaultTier: {Floor: floor}})
}

// The floor's whole promise, on the fleet it was written for: two hosts stand,
// devices fill them, and nothing is bought. A deficit reading the buffer alone
// buys a replacement for every create, which is the behavior an operator read
// as placement ignoring the fleet, and the reason this contract exists.
func TestTheFloorBuysToItselfAndThenStops(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		standby        fakeStandby
		wantProvisions int
	}{
		{"an empty fleet buys the whole floor", fakeStandby{}, 2},
		{"a create taking one buys nothing while the fleet holds", fakeStandby{n: 1, occupied: 1}, 0},
		{"a full fleet at the floor buys nothing", fakeStandby{occupied: 2}, 0},
		{"a host lost below the floor is replaced", fakeStandby{occupied: 1}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			prov := &countingProvisioner{}
			err := keeperFor(
				fakePolicy{policy: floorPolicy(hostconfig.ModeOn, 2)}, tc.standby, prov,
			).keep(t.Context())
			if err != nil {
				t.Fatalf("keep() error = %v", err)
			}
			if prov.calls != tc.wantProvisions {
				t.Errorf("provisions = %d, want %d", prov.calls, tc.wantProvisions)
			}
		})
	}
}

// The other direction, and the one the floor is named for: deletes empty the
// fleet and reclamation walks it down to the floor and stops there. A surplus
// computed from the buffer alone gives the last two hosts back as well, and
// every assertion written against the buffer still passes while it does.
func TestReclamationStopsAtTheFloor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		standby     fakeStandby
		wantSurplus []int64
	}{
		{"a fleet above the floor gives back what is spare, capped", fakeStandby{n: 5}, []int64{MaxShortensPerCycle}},
		{"a fleet at the floor gives nothing back", fakeStandby{n: 2}, nil},
		{"a fleet at the floor with a device on one gives nothing back", fakeStandby{n: 1, occupied: 1}, nil},
		{"one host of slack gives back exactly one", fakeStandby{n: 3}, []int64{1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			short := &fakeShortener{}
			err := keeperFor(
				fakePolicy{policy: floorPolicy(hostconfig.ModeOn, 2)}, tc.standby,
				&countingProvisioner{}, WithShortener(short),
			).keep(t.Context())
			if err != nil {
				t.Fatalf("keep() error = %v", err)
			}
			if len(short.surpluses) != len(tc.wantSurplus) {
				t.Fatalf("shortener called %d time(s) with %v, want %v",
					len(short.surpluses), short.surpluses, tc.wantSurplus)
			}
			for i, want := range tc.wantSurplus {
				if got := short.surpluses[i][hostconfig.DefaultTier]; got != want {
					t.Errorf("surplus[%d] = %d, want %d", i, got, want)
				}
			}
		})
	}
}

// Renewal is the free way to meet either bound, so it must be offered the
// COMPOSED deficit rather than the buffer's half of it. Offered the buffer's
// alone, a fleet short only of its floor renews nothing and buys instead,
// paying for a host it already has draining.
func TestRescueServesTheFloorsDeficit(t *testing.T) {
	t.Parallel()
	rescuer := &fakeRescuer{rescued: 2}
	prov := &countingProvisioner{}
	err := keeperFor(
		fakePolicy{policy: floorPolicy(hostconfig.ModeOn, 2)},
		fakeStandby{draining: 2},
		prov,
		WithRescuer(rescuer),
	).keep(t.Context())
	if err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	// Two draining hosts count toward the standing fleet, so the floor of two is
	// met and only the renewal the buffer would want is asked for... which is
	// none. The deficit that reaches the rescuer is what this pins.
	if len(rescuer.offers) != 0 {
		t.Fatalf("rescuer offered %v with both bounds met", rescuer.offers)
	}
	if prov.calls != 0 {
		t.Errorf("provisions = %d, want 0", prov.calls)
	}

	rescuer = &fakeRescuer{rescued: 2}
	prov = &countingProvisioner{}
	err = keeperFor(
		fakePolicy{policy: floorPolicy(hostconfig.ModeOn, 4)},
		fakeStandby{draining: 2},
		prov,
		WithRescuer(rescuer),
	).keep(t.Context())
	if err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if len(rescuer.offers) != 1 || rescuer.offers[0][hostconfig.DefaultTier] != 2 {
		t.Fatalf("rescuer offered %v, want one offer of 2, the floor's own deficit", rescuer.offers)
	}
	if prov.calls != 0 {
		t.Errorf("provisions = %d, want 0: renewal met the whole deficit", prov.calls)
	}
}

// The cycle's statement count is a constant of the code path: one inventory
// read, one renewal listing and one reclamation listing, whatever the fleet is
// partitioned into.
//
// This is the assertion the whole per-pool design answers to, and it is written
// against a fleet with TWO pools short and TWO spare so the mutation is real in
// both directions. Move either call inside the pool loop, the obvious way to
// give each pool its own budget, and the count on that side becomes two. A
// single spare pool would leave the reclamation half passing under exactly the
// mutation it names.
func TestOneCallPerListingWhateverThePoolCount(t *testing.T) {
	t.Parallel()
	reads := 0
	resc := &fakeRescuer{perPool: map[string]int64{}}
	short := &fakeShortener{}
	prov := &countingProvisioner{}
	s := keeperFor(
		fakePolicy{policy: poolPolicy(hostconfig.ModeOn, map[string]hostconfig.FleetTier{
			hostconfig.DefaultTier: {Standby: 2},
			"gpu":                  {Standby: 2},
			"arm":                  {Standby: 0},
			"spot":                 {Standby: 0},
		})},
		fakeStandby{calls: &reads, pools: host.FleetInventory{
			// Two pools short of their buffer and two holding a surplus: every
			// actuating path this cycle has is reached, from more than one pool.
			hostconfig.DefaultTier: {Standby: 0, Total: 0},
			"gpu":                  {Standby: 1, Draining: 1, Total: 2},
			"arm":                  {Standby: 3, Total: 3},
			"spot":                 {Standby: 1, Total: 1},
		}},
		prov,
		WithRescuer(resc),
		WithShortener(short),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if reads != 1 {
		t.Errorf("inventory reads = %d, want 1; a read per pool puts the pool count inside the cycle", reads)
	}
	if len(resc.offers) != 1 {
		t.Errorf("renewal calls = %d, want 1 for the whole cycle: %v", len(resc.offers), resc.offers)
	}
	if len(short.surpluses) != 1 {
		t.Errorf("reclamation calls = %d, want 1 for the whole cycle: %v", len(short.surpluses), short.surpluses)
	}
	// And the one call of each carries every pool that needed it, so the single
	// call is not a single POOL being served.
	if got := resc.offers[0]; got[hostconfig.DefaultTier] != 2 || got["gpu"] != 1 {
		t.Errorf("renewal was offered %v, want both short pools' own deficits", got)
	}
	if got := short.surpluses[0]; got["arm"] != MaxShortensPerCycle || got["spot"] != 1 || len(got) != 2 {
		t.Errorf("reclamation was offered %v, want each spare pool's own surplus under its own cap", got)
	}
}

// A pool no machine can be built for is never bought for, and every buy names
// the pool its machine has to join.
//
// A host bought against a pool it cannot be built into joins another one: the
// gap that bought it stands, the machine reads as surplus where it landed, and
// the cycle creates and reclaims real VMs every thirty seconds while the deficit
// never moves. Drop the filter and the arm pool takes budget it can never spend;
// drop the pool from the buy and both hosts land in the default pool while the
// gpu pool's gap stays open.
func TestBuysNameTheirPoolAndSkipTheOnesNothingCanBuildFor(t *testing.T) {
	t.Parallel()
	prov := &countingProvisioner{}
	s := keeperFor(
		fakePolicy{policy: poolPolicy(hostconfig.ModeOn, map[string]hostconfig.FleetTier{
			hostconfig.DefaultTier: {Standby: 1},
			host.PoolGPU:           {Standby: 1},
			host.PoolARM:           {Standby: 3},
		})},
		fakeStandby{pools: host.FleetInventory{
			hostconfig.DefaultTier: {Standby: 0, Total: 0},
			host.PoolGPU:           {Standby: 0, Total: 0},
			host.PoolARM:           {Standby: 0, Total: 0},
		}},
		prov,
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if prov.calls != 2 {
		t.Fatalf("provisions = %d, want 2, one each for the two pools a machine exists for", prov.calls)
	}
	bought := map[string]int{}
	for _, pool := range prov.pools {
		bought[pool]++
	}
	if bought[hostconfig.DefaultTier] != 1 || bought[host.PoolGPU] != 1 || bought[host.PoolARM] != 0 {
		t.Errorf("bought %v, want one for the default pool, one for gpu, and none for arm", bought)
	}
}

// A pool short beside a pool spare is answered in ONE cycle, in both
// directions. They are no longer alternatives: a cycle that did only one of them
// would leave whichever pool lost the coin toss waiting, every cycle, for as
// long as the other one had work.
func TestACycleBuysForOnePoolAndReclaimsFromAnother(t *testing.T) {
	t.Parallel()
	short := &fakeShortener{}
	prov := &countingProvisioner{}
	s := keeperFor(
		fakePolicy{policy: poolPolicy(hostconfig.ModeOn, map[string]hostconfig.FleetTier{
			hostconfig.DefaultTier: {Standby: 2},
			"gpu":                  {Standby: 0},
		})},
		fakeStandby{pools: host.FleetInventory{
			hostconfig.DefaultTier: {Standby: 0, Total: 0},
			"gpu":                  {Standby: 2, Total: 2},
		}},
		prov,
		WithShortener(short),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if prov.calls != 2 {
		t.Errorf("provisions = %d, want 2 for the pool that is short", prov.calls)
	}
	if len(short.surpluses) != 1 || short.surpluses[0]["gpu"] != 2 {
		t.Errorf("reclamation = %v, want the gpu pool's surplus of 2 in the same cycle", short.surpluses)
	}
}

// The per-cycle provisioning budget stays GLOBAL, because it rations a host
// quota the whole deployment shares, and is handed out deepest-deficit-first
// so one starved pool cannot be crowded out by a pool that is barely short.
//
// A budget of two against deficits of five and one: both hosts go to the deep
// pool. An even split buys one each and leaves the pool short by five converging
// at half the rate, which is the mutation this row names.
func TestTheCycleBudgetGoesToTheDeepestDeficitFirst(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		deficits map[string]int64
		budget   int64
		want     []string
	}{
		{"one pool takes the whole budget", map[string]int64{"a": 5, "b": 1}, 2, []string{"a", "a"}},
		{"the deep pool is served until they are level", map[string]int64{"a": 3, "b": 1}, 3, []string{"a", "a", "b"}},
		{"a budget past every deficit buys each pool exactly its gap", map[string]int64{"a": 1, "b": 1}, 9, []string{"a", "b"}},
		{"ties are broken by name, so a cycle spends the same way twice", map[string]int64{"b": 1, "a": 1}, 1, []string{"a"}},
		{"nothing to buy", map[string]int64{}, 4, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := allocate(tc.deficits, tc.budget)
			if len(got) != len(tc.want) {
				t.Fatalf("allocation = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("allocation = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// A renewal that answered one pool's deficit must not be subtracted from
// another's. The gpu pool is short by two and renews both; the default pool is
// short by two and renews nothing, so it buys two.
//
// A keeper that added the renewals up and subtracted the total would buy
// nothing for the default pool and leave it short forever, while every count in
// the cycle looked right.
func TestRenewalsAreSubtractedFromThePoolTheyLandedIn(t *testing.T) {
	t.Parallel()
	prov := &countingProvisioner{}
	resc := &fakeRescuer{perPool: map[string]int64{"gpu": 2}}
	s := keeperFor(
		fakePolicy{policy: poolPolicy(hostconfig.ModeOn, map[string]hostconfig.FleetTier{
			hostconfig.DefaultTier: {Standby: 2},
			"gpu":                  {Standby: 2},
		})},
		fakeStandby{pools: host.FleetInventory{
			hostconfig.DefaultTier: {Standby: 0, Total: 0},
			"gpu":                  {Standby: 0, Draining: 2, Total: 2},
		}},
		prov,
		WithRescuer(resc),
	)
	if err := s.keep(t.Context()); err != nil {
		t.Fatalf("keep() error = %v", err)
	}
	if prov.calls != 2 {
		t.Errorf("provisions = %d, want 2; the pool that renewed nothing still has to buy", prov.calls)
	}
}
