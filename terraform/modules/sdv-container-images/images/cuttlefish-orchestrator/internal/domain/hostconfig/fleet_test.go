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

package hostconfig

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

func policy(standby int64) FleetPolicy {
	p := DefaultFleetPolicy()
	p.Tiers = map[string]FleetTier{DefaultTier: {Standby: standby}}
	return p
}

// tier is the default pool's intent alone, for the composition steps that take
// one pool rather than the whole policy.
func tier(standby int64) FleetTier { return FleetTier{Standby: standby} }

// One row per way the target can be reached: each signal winning on its own,
// the clamp, and the tie rule. A sum instead of max-wins goes red on the rows
// where two signals are both non-zero.
func TestTarget(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		standby    int64
		signals    TargetSignals
		want       int64
		wantSignal string
		wantLimit  string
	}{
		{"nothing asked for", 0, TargetSignals{}, 0, SignalStandby, ""},
		{"standing intent alone", 3, TargetSignals{}, 3, SignalStandby, ""},
		{"demand alone", 0, TargetSignals{Demand: 4}, 4, SignalDemand, ""},
		{"schedule alone", 0, TargetSignals{Schedule: 5}, 5, SignalSchedule, ""},
		{"the largest wins, not the sum", 2, TargetSignals{Schedule: 3, Demand: 4}, 4, SignalDemand, ""},
		{"standing intent can win", 6, TargetSignals{Schedule: 3, Demand: 4}, 6, SignalStandby, ""},
		// A tie reports the cheaper explanation: a target equal to the floor is
		// the floor being held, not demand that happens to match it.
		{"a tie goes to the standing intent", 4, TargetSignals{Demand: 4}, 4, SignalStandby, ""},
		{"a tie between schedule and demand goes to the schedule", 0, TargetSignals{Schedule: 3, Demand: 3}, 3, SignalSchedule, ""},
		{
			"the clamp binds and says so", 0,
			TargetSignals{Demand: MaxStandbyHosts + 20},
			MaxStandbyHosts, SignalDemand, LimitStandbyCap,
		},
		{"a negative signal cannot lower the target below zero", 0, TargetSignals{Demand: -5}, 0, SignalStandby, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// A fleet of zero, so every row here is judged against the cap's
			// compiled floor; the proportional half has its own table below.
			got := Target(tier(tc.standby), tc.signals, 0)
			if got.Target != tc.want {
				t.Errorf("target = %d, want %d", got.Target, tc.want)
			}
			if got.Signal != tc.wantSignal {
				t.Errorf("signal = %q, want %q", got.Signal, tc.wantSignal)
			}
			if got.LimitedBy != tc.wantLimit {
				t.Errorf("limited_by = %q, want %q", got.LimitedBy, tc.wantLimit)
			}
		})
	}
}

// The two fleet-proportional bounds hold their compiled floors on a small fleet
// and track the fleet past it. The floors are what make a deployment of ten
// behave exactly as it did before they became proportional at all.
//
// Both fail if either rule is ever reduced to its floor or to its share alone.
func TestTheKeeperBoundsTrackTheStandingFleet(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		fleet                  int64
		wantStandby, wantTopUp int64
	}{
		{0, MaxStandbyHosts, MinTopUpsPerCycle},
		{10, MaxStandbyHosts, MinTopUpsPerCycle},
		// The last fleet at which BOTH shares still sit on their floors: 64 hosts
		// is an eighth of 8 and a thirty-second of 2.
		{MinTopUpsPerCycle * TopUpFleetShare, MaxStandbyHosts, MinTopUpsPerCycle},
		// The rate's share overtakes its floor first, which is what keeps it
		// tighter than the level it converges toward.
		{MaxStandbyHosts * StandbyCapFleetShare, MaxStandbyHosts, 4},
		{2000, 250, 62},
		{30000, 3750, 937},
	} {
		t.Run(fmt.Sprintf("fleet-%d", tc.fleet), func(t *testing.T) {
			t.Parallel()
			if got := StandbyCap(tc.fleet); got != tc.wantStandby {
				t.Errorf("StandbyCap(%d) = %d, want %d", tc.fleet, got, tc.wantStandby)
			}
			if got := TopUpBudget(tc.fleet); got != tc.wantTopUp {
				t.Errorf("TopUpBudget(%d) = %d, want %d", tc.fleet, got, tc.wantTopUp)
			}
		})
	}
}

// The rate bound must stay strictly tighter than the level bound, or a per-cycle
// budget as wide as the ceiling it converges toward could never bind, which is
// exactly the case it exists for.
func TestTheRateBoundStaysTighterThanTheLevel(t *testing.T) {
	t.Parallel()
	for _, fleet := range []int64{2000, 30000} {
		if TopUpBudget(fleet) >= StandbyCap(fleet) {
			t.Errorf("at a fleet of %d the per-cycle budget (%d) is not tighter than the target ceiling (%d)",
				fleet, TopUpBudget(fleet), StandbyCap(fleet))
		}
	}
}

// Demand is the one signal that can reach past the compiled ceiling, and on a
// large fleet the clamp moves with the fleet, while still reporting that it
// bound, because a target pinned below what demand asked for is a fact an
// operator cannot otherwise see.
func TestDemandReachesThroughTheCompiledCeilingOnALargeFleet(t *testing.T) {
	t.Parallel()
	small := Target(FleetTier{}, TargetSignals{Demand: 500}, 10)
	if small.Target != MaxStandbyHosts || small.LimitedBy != LimitStandbyCap {
		t.Errorf("on a fleet of 10: target = %d limited_by = %q, want %d and the cap",
			small.Target, small.LimitedBy, MaxStandbyHosts)
	}
	large := Target(FleetTier{}, TargetSignals{Demand: 500}, 2000)
	if large.Target != 250 || large.LimitedBy != LimitStandbyCap {
		t.Errorf("on a fleet of 2000: target = %d limited_by = %q, want 250 and the cap",
			large.Target, large.LimitedBy)
	}
	unclamped := Target(FleetTier{}, TargetSignals{Demand: 100}, 2000)
	if unclamped.Target != 100 || unclamped.LimitedBy != "" {
		t.Errorf("a demand of 100 under a ceiling of 250: target = %d limited_by = %q, want 100 and no limit",
			unclamped.Target, unclamped.LimitedBy)
	}
}

// The floor's bound stays compiled while the buffer's became proportional, and
// the asymmetry is arithmetic: the buffer asks for a subset of a fleet demand
// drives, while the floor asks for the fleet itself, so a floor clamped to a
// share of what is already standing is a floor that is always already met.
//
// It fails if boundedFloor is ever switched to StandbyCap: a stored floor of 100
// against a standing fleet of 2000 would then be honored rather than clipped.
func TestTheFloorBoundStaysCompiled(t *testing.T) {
	t.Parallel()
	policy := DefaultFleetPolicy()
	policy.Tiers = map[string]FleetTier{DefaultTier: {Floor: 100}}
	status := ComposeStatus(policy, DefaultTier, time.Now(),
		host.StandbyInventory{Standby: 0, Total: 2000}, DemandWindow{})
	if status.Floor != MaxFleetFloor {
		t.Errorf("floor = %d on a fleet of 2000, want the compiled bound %d", status.Floor, MaxFleetFloor)
	}
}

// The compiled default is off with nothing standing: it is what a malformed or
// absent policy degrades to, and it must never be a policy that spends.
func TestDefaultFleetPolicySpendsNothing(t *testing.T) {
	t.Parallel()
	p := DefaultFleetPolicy()
	if p.Mode != ModeOff {
		t.Errorf("default mode = %q, want off", p.Mode)
	}
	if p.Mode.Provisions() || p.Mode.Reclaims() {
		t.Error("the default mode must neither provision nor reclaim")
	}
	if standby := p.Pool(DefaultTier).Standby; standby != 0 {
		t.Errorf("default standby = %d, want 0", standby)
	}
}

// What each mode permits, stated once. provision_only and on agree on
// provisioning and disagree on reclaiming, which is the whole distinction
// until shorten lands.
func TestAutoscaleModeAuthority(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode                 AutoscaleMode
		valid                bool
		provisions, reclaims bool
	}{
		{ModeOff, true, false, false},
		{ModeProvisionOnly, true, true, false},
		{ModeOn, true, true, true},
		{AutoscaleMode(""), false, false, false},
		{AutoscaleMode("ON"), false, false, false},
		{AutoscaleMode("enabled"), false, false, false},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			t.Parallel()
			if got := tc.mode.Valid(); got != tc.valid {
				t.Errorf("Valid() = %v, want %v", got, tc.valid)
			}
			if got := tc.mode.Provisions(); got != tc.provisions {
				t.Errorf("Provisions() = %v, want %v", got, tc.provisions)
			}
			if got := tc.mode.Reclaims(); got != tc.reclaims {
				t.Errorf("Reclaims() = %v, want %v", got, tc.reclaims)
			}
		})
	}
}

// AutoscaleModes must list exactly the modes Valid accepts: it is what the
// enum tag and the console picker are generated against, so a mode missing
// here is a mode nobody can select.
func TestAutoscaleModesCoversEveryValidMode(t *testing.T) {
	t.Parallel()
	for _, m := range AutoscaleModes {
		if !m.Valid() {
			t.Errorf("AutoscaleModes lists %q, which Valid() rejects", m)
		}
	}
	if len(AutoscaleModes) != 3 {
		t.Errorf("AutoscaleModes has %d entries, want 3", len(AutoscaleModes))
	}
}

// The peak is the largest number of misses inside ONE provisioning leg, and
// the dimension is what makes it a host count rather than a rate. Each row
// names the implementation it kills.
func TestPeakConcurrent(t *testing.T) {
	t.Parallel()
	leg := host.ProvisioningLeg
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	at := func(offsets ...time.Duration) []time.Time {
		out := make([]time.Time, 0, len(offsets))
		for _, d := range offsets {
			out = append(out, base.Add(d))
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		times []time.Time
		want  int64
	}{
		{"no misses", nil, 0},
		{"one miss", at(0), 1},
		{"three in one leg", at(0, leg/4, leg/2), 3},
		// Six misses one leg apart is a steady stream, not six at once: each
		// one's host existed before the next arrived. A raw count answers 6; a
		// bucket-pair estimator answers 2.
		{"six one leg apart is a target of one", at(0, leg, 2*leg, 3*leg, 4*leg, 5*leg), 1},
		// Half-open: exactly one leg later is the first moment NOT concurrent.
		// A closed window answers 2 here.
		{"exactly one leg apart is not concurrent", at(0, leg), 1},
		{"a nanosecond inside the leg is concurrent", at(0, leg-time.Nanosecond), 2},
		// The peak is the maximum over all windows, not the last one.
		{"the peak is not the tail", at(0, time.Second, 2*time.Second, 10*leg), 3},
		{"the peak is not the head", at(0, 10*leg, 10*leg+time.Second, 10*leg+2*time.Second), 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := PeakConcurrent(tc.times, leg); got != tc.want {
				t.Errorf("PeakConcurrent = %d, want %d", got, tc.want)
			}
		})
	}
}

// A non-positive window has no meaning as a concurrency measure, and returning
// the full count would report every miss in the horizon as simultaneous.
func TestPeakConcurrentRejectsANonPositiveWindow(t *testing.T) {
	t.Parallel()
	times := []time.Time{time.Now(), time.Now()}
	if got := PeakConcurrent(times, 0); got != 0 {
		t.Errorf("PeakConcurrent with a zero window = %d, want 0", got)
	}
}

// servableWindow builds a window of misses a standby host could have absorbed:
// stamped, and stamped with facts that clear every axis. Not the zero Miss:
// that one is unjudgeable, which is also servable, so a composition
// that had stopped reading the facts entirely would still pass.
func servableWindow(times ...time.Time) DemandWindow {
	servable, fits := false, true
	w := DemandWindow{Misses: make([]Miss, 0, len(times))}
	for _, at := range times {
		w.Misses = append(w.Misses, Miss{At: at, GPURequired: &servable, FitsDefaultHost: &fits})
	}
	return w
}

// The composition the card and the keeper share. A horizon of zero must
// suppress the demand signal even with miss times in hand, or an operator
// disabling the signal still gets a target raised by it.
func TestComposeStatus(t *testing.T) {
	t.Parallel()
	leg := host.ProvisioningLeg
	now := time.Now()
	misses := servableWindow(now.Add(-leg/4), now.Add(-leg/3), now.Add(-leg/2))

	live := ComposeStatus(policy(1), DefaultTier, now, host.StandbyInventory{Standby: 2, Draining: 1, Total: 3}, misses)
	if live.Misses != 3 || live.Target != 3 || live.Signal != SignalDemand {
		t.Errorf("with a horizon: misses/target/signal = %d/%d/%q, want 3/3/demand",
			live.Misses, live.Target, live.Signal)
	}
	if live.Standby != 2 || live.Draining != 1 {
		t.Errorf("standby/draining = %d/%d, want 2/1", live.Standby, live.Draining)
	}

	off := policy(1)
	off.DemandHorizonSec = 0
	silent := ComposeStatus(off, DefaultTier, now, host.StandbyInventory{Standby: 2, Draining: 1, Total: 3}, misses)
	if silent.Misses != 0 || silent.Target != 1 || silent.Signal != SignalStandby {
		t.Errorf("with the horizon disabled: misses/target/signal = %d/%d/%q, want 0/1/standby",
			silent.Misses, silent.Target, silent.Signal)
	}
}

// Demand this fleet cannot serve must move nothing the keeper acts on, and must
// still be visible on the card. It is the step's whole claim, and it is written
// as one composition against another rather than against an expected number so
// that it outlives every later change to how the target is reached: a rule that
// starts reading the unservable half goes red here whatever the arithmetic
// around it has become.
func TestTheTargetReadsOnlyTheServableHalf(t *testing.T) {
	t.Parallel()
	leg := host.ProvisioningLeg
	now := time.Now()
	// No standing intent, so the target is the demand signal and nothing else.
	// A floor of its own would mask a raised target as a held floor.
	p := policy(0)

	servable := servableWindow(now.Add(-leg/4), now.Add(-leg/3))
	crowded := servableWindow(now.Add(-leg/4), now.Add(-leg/3))
	required, fits := true, false
	for range 6 {
		crowded.Misses = append(crowded.Misses, Miss{
			At: now.Add(-leg / 2), GPURequired: &required, FitsDefaultHost: &fits,
		})
	}

	quiet := ComposeStatus(p, DefaultTier, now, host.StandbyInventory{}, servable)
	loud := ComposeStatus(p, DefaultTier, now, host.StandbyInventory{}, crowded)

	if loud.Target != quiet.Target || loud.Misses != quiet.Misses || loud.Signal != quiet.Signal {
		t.Errorf("six unservable misses moved the target: %d/%d/%q, want %d/%d/%q",
			loud.Target, loud.Misses, loud.Signal, quiet.Target, quiet.Misses, quiet.Signal)
	}
	if quiet.Target == 0 {
		t.Fatal("the servable window raised no target, so the comparison above proves nothing")
	}
	// And they were counted all the same. Without this, a composition that had
	// stopped reading the window entirely would satisfy the equality above.
	if loud.Demand.Unservable != 6 || loud.Demand.GPURequired != 6 || loud.Demand.NeedsLargerHost != 6 {
		t.Errorf("the unservable misses are missing from the split: %+v", loud.Demand)
	}
	if quiet.Demand != (DemandSplit{Servable: 2}) {
		t.Errorf("the servable window split = %+v, want two servable misses and nothing else", quiet.Demand)
	}
}

// DemandSince is what the query's WHERE clause is built from; a zero time is
// the caller's signal not to query at all.
func TestDemandSince(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	p := DefaultFleetPolicy()
	p.DemandHorizonSec = 900
	if got := p.DemandSince(now); !got.Equal(now.Add(-15 * time.Minute)) {
		t.Errorf("DemandSince = %v, want %v", got, now.Add(-15*time.Minute))
	}
	p.DemandHorizonSec = 0
	if got := p.DemandSince(now); !got.IsZero() {
		t.Errorf("DemandSince with a zero horizon = %v, want the zero time", got)
	}
}

// A pool reads its own intent, and a pool the policy never configured (which a
// malformed read produces for every pool at once) must answer the zero intent
// rather than panic. The zero intent asks for nothing, which is the direction
// that spends nothing.
func TestPoolReadsItsOwnIntent(t *testing.T) {
	t.Parallel()
	if got := policy(4).Pool(DefaultTier).Standby; got != 4 {
		t.Errorf("the default pool's standby = %d, want 4", got)
	}
	if got := policy(4).Pool("gpu"); !reflect.DeepEqual(got, FleetTier{}) {
		t.Errorf("an unconfigured pool = %+v, want the zero intent", got)
	}
	if got := (FleetPolicy{}).Pool(DefaultTier); !reflect.DeepEqual(got, FleetTier{}) {
		t.Errorf("a policy with no pools = %+v, want the zero intent", got)
	}
}

// The two bounds against every relation they can stand in. Each row is a fleet
// and the two answers the cycle takes from it, so an arithmetic that dropped
// either bound goes red on the rows where they disagree, and only on those,
// which is why the rows where they agree are here too.
func TestDeficitAndSurplusHoldBothBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                    string
		floor, total            int64
		target, free            int64
		wantDeficit, wantSurpls int64
	}{
		{"nothing asked for, nothing standing", 0, 0, 0, 0, 0, 0},
		{"an empty fleet under a floor buys the floor", 2, 0, 0, 0, 2, 0},
		{"devices filling the fleet buy nothing while the floor holds", 2, 2, 0, 1, 0, 0},
		{"the buffer buys on its own with the floor met", 2, 3, 2, 0, 2, 0},
		{"the larger shortfall wins, and they are never summed", 4, 1, 2, 0, 3, 0},
		{"a floor above the fleet holds it there", 2, 5, 0, 5, 0, 3},
		{"at the floor nothing is spare, however free the fleet is", 2, 2, 0, 2, 0, 0},
		{"the buffer holds capacity the floor would release", 0, 5, 3, 4, 0, 1},
		{"spare only where BOTH bounds have slack", 1, 5, 2, 4, 0, 2},
		{"a fleet short by one bound is spare by neither", 3, 2, 0, 2, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			status := FleetStatus{Floor: tc.floor, Total: tc.total, Target: tc.target, Standby: tc.free}
			if got := status.Deficit(); got != tc.wantDeficit {
				t.Errorf("Deficit = %d, want %d", got, tc.wantDeficit)
			}
			if got := status.Surplus(); got != tc.wantSurpls {
				t.Errorf("Surplus = %d, want %d", got, tc.wantSurpls)
			}
		})
	}
}

// The attribution, which is the only reason the floor is a signal at all: with
// the buffer satisfied and the floor short, the target explains nothing about
// the host that is about to be bought. Attributing that buy to the standing
// intent would put a number on the card that the fleet is not pursuing.
func TestTheFloorTakesTheSignalOnlyWhenItAloneBuys(t *testing.T) {
	t.Parallel()
	now := time.Now()
	floored := func(floor, standby int64) FleetPolicy {
		p := policy(standby)
		p.Tiers = map[string]FleetTier{DefaultTier: {Standby: standby, Floor: floor}}
		return p
	}
	for _, tc := range []struct {
		name         string
		policy       FleetPolicy
		inventory    host.StandbyInventory
		wantSignal   string
		wantDeficit  int64
		wantTheFloor int64
	}{
		{
			"the floor alone buys", floored(2, 0), host.StandbyInventory{}, SignalFloor, 2, 2,
		},
		{
			"the buffer buys more than the floor, so it keeps the attribution",
			floored(1, 3),
			host.StandbyInventory{},
			SignalStandby, 3, 1,
		},
		{
			"a tie leaves the attribution with the target",
			floored(2, 2),
			host.StandbyInventory{},
			SignalStandby, 2, 2,
		},
		{
			"a met floor beside a met buffer attributes nothing to the floor",
			floored(2, 0),
			host.StandbyInventory{Standby: 1, Total: 2},
			SignalStandby, 0, 2,
		},
		{
			"a floor past the compiled bound is clipped before it can spend",
			floored(MaxFleetFloor+5, 0),
			host.StandbyInventory{},
			SignalFloor,
			MaxFleetFloor, MaxFleetFloor,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			status := ComposeStatus(tc.policy, DefaultTier, now, tc.inventory, DemandWindow{})
			if status.Signal != tc.wantSignal {
				t.Errorf("signal = %q, want %q", status.Signal, tc.wantSignal)
			}
			if got := status.Deficit(); got != tc.wantDeficit {
				t.Errorf("deficit = %d, want %d", got, tc.wantDeficit)
			}
			if status.Floor != tc.wantTheFloor {
				t.Errorf("published floor = %d, want %d", status.Floor, tc.wantTheFloor)
			}
		})
	}
}

// The published picture carries the floor's own pair, and it is a pair: the
// bound alone cannot be judged, and a surface holding the idle counts alone
// cannot tell a fleet that stopped shrinking from one nobody asked to shrink.
func TestComposeStatusPublishesTheStandingFleet(t *testing.T) {
	t.Parallel()
	p := policy(1)
	p.Tiers = map[string]FleetTier{DefaultTier: {Standby: 1, Floor: 3}}
	status := ComposeStatus(p, DefaultTier, time.Now(),
		host.StandbyInventory{Standby: 2, Draining: 1, Total: 6}, DemandWindow{})
	if status.Floor != 3 || status.Total != 6 {
		t.Errorf("floor/total = %d/%d, want 3/6", status.Floor, status.Total)
	}
	if status.Standby != 2 || status.Draining != 1 {
		t.Errorf("standby/draining = %d/%d, want 2/1", status.Standby, status.Draining)
	}
}

// The enum tag on FleetStatus.Signal is a second copy of the vocabulary above,
// and it is the copy the API actually enforces: on drift, a signal the domain
// publishes is rejected by the schema with nothing else complaining. Read off
// the compiled tag rather than the source text so a rename cannot pass.
func TestSignalEnumTagMatchesTheVocabulary(t *testing.T) {
	t.Parallel()
	field, ok := reflect.TypeFor[FleetStatus]().FieldByName("Signal")
	if !ok {
		t.Fatal("FleetStatus has no Signal field")
	}
	want := strings.Join(Signals, ",")
	if tag := field.Tag.Get("enum"); tag != want {
		t.Errorf("enum tag = %q, want %q", tag, want)
	}
}

// The fold is the identity on a fleet with one pool: every fleet-wide surface
// reads exactly what it read before pools existed.
//
// It is the whole inertness claim, and it is written field by field rather than
// as a spot check: a fold that dropped the signal, the limit or the split would
// leave the counts agreeing while the card lost its explanation.
func TestOnePoolFoldsToItsOwnPicture(t *testing.T) {
	t.Parallel()
	now := time.Now()
	p := policy(2)
	p.Tiers = map[string]FleetTier{DefaultTier: {Standby: 2, Floor: 1}}
	inventory := host.FleetInventory{DefaultTier: {Standby: 1, Draining: 2, Total: 4}}
	window := servableWindow(now.Add(-host.ProvisioningLeg / 4))

	view := ComposeFleet(p, now, inventory, window)
	if len(view.Pools) != 1 || view.Pools[0].Pool != DefaultTier {
		t.Fatalf("pools = %+v, want the default pool alone", view.Pools)
	}
	if !reflect.DeepEqual(view.Fleet, view.Pools[0].Status) {
		t.Errorf("the fleet picture = %+v, want the default pool's own %+v", view.Fleet, view.Pools[0].Status)
	}
	if !reflect.DeepEqual(view.Fleet, ComposeStatus(p, DefaultTier, now, inventory[DefaultTier], window)) {
		t.Error("the fold is not the composition it folds: a fleet-wide surface would read a different number")
	}
}

// A pool the policy configures but the fleet has no machines in is still
// decided about, because it is the pool that is about to be bought for, and so
// is a pool holding machines the policy has stopped configuring, which is the pool
// whose surplus is about to be given back. The default is always there, because
// it is where a machine naming no pool belongs.
func TestComposeFleetDecidesAboutEveryPoolEitherSideNames(t *testing.T) {
	t.Parallel()
	now := time.Now()
	p := policy(0)
	p.Tiers = map[string]FleetTier{DefaultTier: {}, "gpu": {Standby: 2}}

	view := ComposeFleet(p, now, host.FleetInventory{"arm": {Standby: 1, Total: 1}}, DemandWindow{})

	got := make([]string, 0, len(view.Pools))
	for _, pool := range view.Pools {
		got = append(got, pool.Pool)
	}
	want := []string{DefaultTier, "arm", "gpu"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pools = %v, want %v (the default first, the rest in name order)", got, want)
	}
	if view.Deficits()["gpu"] != 2 {
		t.Errorf("deficits = %v, want the configured-but-empty pool short by 2", view.Deficits())
	}
	if view.Surpluses()["arm"] != 1 {
		t.Errorf("surpluses = %v, want the unconfigured-but-standing pool spare by 1", view.Surpluses())
	}
}

// The deployment's gap is its pools' gaps ADDED, never the gap of the summed
// picture. One pool short by two beside one spare by two nets to nothing in the
// sums while the keeper is about to buy two and release two, so a page reading
// the folded picture would report a still fleet that is about to move twice.
func TestTheFleetsGapIsTheSumOfThePoolsGaps(t *testing.T) {
	t.Parallel()
	now := time.Now()
	p := policy(0)
	p.Tiers = map[string]FleetTier{DefaultTier: {Standby: 2}, "gpu": {Standby: 0}}

	view := ComposeFleet(p, now, host.FleetInventory{
		DefaultTier: {Standby: 0, Total: 0},
		"gpu":       {Standby: 2, Total: 2},
	}, DemandWindow{})

	if view.Deficit() != 2 || view.Surplus() != 2 {
		t.Errorf("deficit/surplus = %d/%d, want 2/2", view.Deficit(), view.Surplus())
	}
	if view.Fleet.Deficit() != 0 || view.Fleet.Surplus() != 0 {
		t.Errorf("the folded picture reports %d/%d, which is the reading this must not be",
			view.Fleet.Deficit(), view.Fleet.Surplus())
	}
}

// Each pool's ceiling is a share of ITS OWN standing machines, not the fleet's.
// Read against the fleet, a pool holding two machines beside a pool holding two
// thousand would be allowed a buffer of two hundred and fifty it can never fill.
func TestEachPoolsCeilingIsAShareOfItsOwnMachines(t *testing.T) {
	t.Parallel()
	now := time.Now()
	p := policy(0)
	p.Tiers = map[string]FleetTier{DefaultTier: {}, "gpu": {}}
	// The same crowd of servable misses in each pool, so the only thing that can
	// separate the two targets is the fleet each ceiling is a share of.
	leg := host.ProvisioningLeg
	servable, fits := false, true
	window := DemandWindow{Misses: make([]Miss, 0, 1000)}
	for _, pool := range []string{DefaultTier, "gpu"} {
		for i := range 500 {
			window.Misses = append(window.Misses, Miss{
				At: now.Add(-leg/2 + time.Duration(i)), Pool: pool,
				GPURequired: &servable, FitsDefaultHost: &fits,
			})
		}
	}

	view := ComposeFleet(p, now, host.FleetInventory{
		DefaultTier: {Standby: 0, Total: 2000},
		"gpu":       {Standby: 0, Total: 2},
	}, window)

	byPool := map[string]FleetStatus{}
	for _, pool := range view.Pools {
		byPool[pool.Pool] = pool.Status
	}
	if got := byPool[DefaultTier].Target; got != StandbyCap(2000) {
		t.Errorf("the large pool's target = %d, want its own ceiling %d", got, StandbyCap(2000))
	}
	if got := byPool["gpu"].Target; got != MaxStandbyHosts {
		t.Errorf("the small pool's target = %d, want the compiled ceiling %d", got, MaxStandbyHosts)
	}
}
