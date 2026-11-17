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

package device

import "testing"

// The CVD state constants are wire strings: they must match exactly what the
// Host Orchestrator emits (cvd's HumanFriendlyStateName of the InstanceState
// proto enum). A silent re-spelling (e.g. an American-spelling linter turning
// "Canceled" into "Canceled") blinds DerivePhase to that state, so pin them.
func TestCVDStateWireStrings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ got, want string }{
		{CVDStateRunning, "Running"},
		{CVDStateStarting, "Starting"},
		{CVDStatePreparing, "Preparing"},
		{CVDStateStopping, "Stopping"},
		{CVDStateStopped, "Stopped"},
		{CVDStateBootFailed, "Boot failed"},
		{CVDStatePrepareFailed, "Prepare failed"},
		{CVDStateUnreachable, "Unreachable"},
		{CVDStateCancelled, "Cancelled"}, //nolint:misspell // upstream literal
		{CVDStateUnspecified, "Unspecified"},
		{CVDStateUnknown, "Unknown"},
	} {
		if tc.got != tc.want {
			t.Errorf("CVD wire string = %q, want %q (must match upstream cvd)", tc.got, tc.want)
		}
	}
}

// dominanceOrder is a SECOND, independent statement of the precedence the fold
// must implement, worst first. It is written out by hand rather than read from
// instanceStatePrecedence on purpose: a test that consults the table it is
// testing agrees with any table, including a wrong one.
//
// Every entry's position is a claim. Reading down: a failed boot kills the
// group; an instance that is simply gone needs a recreate, so it outranks one
// that is merely unresponsive; a state carrying no information outranks any
// benign state so a group of them cannot read as healthy; every transient
// outranks Running; and Running is last, so a group is running only when all
// of it is.
var dominanceOrder = []string{
	CVDStateBootFailed,
	CVDStatePrepareFailed,
	CVDStateCancelled,
	"",
	CVDStateUnreachable,
	CVDStateUnspecified,
	CVDStateUnknown,
	CVDStatePreparing,
	CVDStateStarting,
	CVDStateStopping,
	CVDStateStopped,
	CVDStateRunning,
}

// TestFoldInstanceStatesDominance walks every ordered pair of states in the
// vocabulary and asserts the worse one decides the group, in both argument
// orders. Swapping any two rows of instanceStatePrecedence disagrees with
// dominanceOrder above and reddens the pairs that straddle the swap.
func TestFoldInstanceStatesDominance(t *testing.T) {
	t.Parallel()
	for i, worse := range dominanceOrder {
		for _, better := range dominanceOrder[i+1:] {
			for _, pair := range [][]Instance{
				{{Name: "a", State: worse}, {Name: "b", State: better}},
				{{Name: "a", State: better}, {Name: "b", State: worse}},
			} {
				if got := FoldInstanceStates(pair); got != worse {
					t.Errorf("FoldInstanceStates(%q, %q) = %q, want %q",
						pair[0].State, pair[1].State, got, worse)
				}
			}
		}
	}
}

// A state the table does not name must never be dropped: upstream has grown
// this enum before, and a dropped state lets a group of unknowns read as
// running. It ranks with Unspecified, below the failures the table does name,
// so real evidence still decides a mixed group.
func TestFoldInstanceStatesHandlesUnrecognizedState(t *testing.T) {
	t.Parallel()
	const invented = "Rebooting" // Not an upstream InstanceState today.
	tests := []struct {
		name      string
		instances []Instance
		want      string
	}{
		{
			"an unrecognized state beats running",
			[]Instance{{State: CVDStateRunning}, {State: invented}},
			invented,
		},
		{
			"an unrecognized state beats every benign state",
			[]Instance{{State: CVDStateStopped}, {State: CVDStateStarting}, {State: invented}},
			invented,
		},
		{
			"a named failure still beats an unrecognized state",
			[]Instance{{State: invented}, {State: CVDStateBootFailed}},
			CVDStateBootFailed,
		},
		{
			"an absent instance still beats an unrecognized state",
			[]Instance{{State: invented}, {State: ""}},
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FoldInstanceStates(tt.instances); got != tt.want {
				t.Errorf("FoldInstanceStates = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFoldInstanceStates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		instances []Instance
		want      string
	}{
		{"no instances observed reads as no CVD", nil, ""},
		{
			"a group is running only when all of it is",
			[]Instance{{State: CVDStateRunning}, {State: CVDStateRunning}, {State: CVDStateRunning}},
			CVDStateRunning,
		},
		{
			"one instance still coming up holds the whole group back",
			[]Instance{{State: CVDStateRunning}, {State: CVDStateStarting}},
			CVDStateStarting,
		},
		{
			"one failed boot decides the group",
			[]Instance{{State: CVDStateRunning}, {State: CVDStateRunning}, {State: CVDStateBootFailed}},
			CVDStateBootFailed,
		},
		{
			// The cheap restart rung fires on exactly Unreachable, and relaunching
			// cannot bring back an instance that is not there.
			"a missing instance outranks an unresponsive one",
			[]Instance{{State: CVDStateUnreachable}, {State: ""}},
			"",
		},
		{
			"equal ranks keep the host's own listing order",
			[]Instance{{Name: "first", State: "Alpha"}, {Name: "second", State: "Beta"}},
			"Alpha",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FoldInstanceStates(tt.instances); got != tt.want {
				t.Errorf("FoldInstanceStates = %q, want %q", got, tt.want)
			}
		})
	}
}

// The fold answers with a wire state so the existing derivation consumes it
// unchanged, including the decline arm, which is what leaves pending/creating
// to the create task. A fold straight to a lifecycle status could not express
// that, so pin the composition rather than trusting it.
func TestFoldInstanceStatesFeedsDerivePhase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		status    Status
		instances []Instance
		want      Status
		wantOK    bool
	}{
		{
			"every instance running settles running",
			StatusCreating,
			[]Instance{{State: CVDStateRunning}, {State: CVDStateRunning}},
			StatusRunning, true,
		},
		{
			"a partly-started group declines, leaving the create task its status",
			StatusCreating,
			[]Instance{{State: CVDStateRunning}, {State: CVDStateStarting}},
			"", false,
		},
		{
			"one failed instance settles the device to error",
			StatusRunning,
			[]Instance{{State: CVDStateRunning}, {State: CVDStateBootFailed}},
			StatusError, true,
		},
		{
			"a teardown is never resurrected by a healthy group",
			StatusDeleting,
			[]Instance{{State: CVDStateRunning}, {State: CVDStateRunning}},
			StatusDeleting, true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Device{Status: tt.status, Instances: tt.instances}
			got, ok := DerivePhase(d, FoldInstanceStates(d.Instances))
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("DerivePhase over folded instances = (%q, %v), want (%q, %v)",
					got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestDerivePhase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status Status // the device's current lifecycle status
		cvd    string
		want   Status
		wantOK bool
	}{
		// A device being torn down keeps that phase regardless of the CVD. A
		// failed CVD never resurrects a delete into "error".
		{"deleting wins over a failed cvd", StatusDeleting, CVDStateBootFailed, StatusDeleting, true},
		{"deleted wins", StatusDeleted, CVDStateRunning, StatusDeleted, true},

		{"running cvd derives running", StatusRunning, CVDStateRunning, StatusRunning, true},
		{"stopping cvd derives stopping", StatusRunning, CVDStateStopping, StatusStopping, true},
		{"stopped cvd derives stopped", StatusRunning, CVDStateStopped, StatusStopped, true},

		// Explicit CVD failures are terminal (derive error), not the zombie
		// recreate path.
		{"boot failed derives error", StatusRunning, CVDStateBootFailed, StatusError, true},
		{"prepare failed derives error", StatusCreating, CVDStatePrepareFailed, StatusError, true},
		{"canceled derives error", StatusCreating, CVDStateCancelled, StatusError, true},

		// Create-owned transients: decline, the create task keeps the status.
		{"starting is create-owned", StatusCreating, CVDStateStarting, "", false},
		{"preparing is create-owned", StatusCreating, CVDStatePreparing, "", false},

		// The zombie signal (unresponsive / gone) is health- and
		// reconcile-owned, not a lifecycle change.
		{"unreachable declines (zombie signal)", StatusRunning, CVDStateUnreachable, "", false},
		{"absent cvd declines", StatusRunning, "", "", false},
		{"unspecified declines", StatusRunning, CVDStateUnspecified, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Device{Status: tt.status}
			got, ok := DerivePhase(d, tt.cvd)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("DerivePhase(%s, %q) = (%q, %v), want (%q, %v)",
					tt.status, tt.cvd, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// TestDerivePhaseBootObservation pins the observation-driven rule: it must
// fire independently of the enum (a post-create loop latches the fleet enum
// at Running), must never resurrect a teardown, and non-terminal verdicts
// must change nothing.
func TestDerivePhaseBootObservation(t *testing.T) {
	t.Parallel()
	obs := func(v BootVerdict) *BootObservation { return &BootObservation{Verdict: v} }
	tests := []struct {
		name   string
		status Status
		cvd    string
		obs    *BootObservation
		want   Status
		wantOK bool
	}{
		// The incident shape: create window, enum stuck in a transient.
		{"looping in create window derives error", StatusCreating, CVDStateStarting, obs(BootVerdictLooping), StatusError, true},
		// The post-create shape: enum latched Running, loop visible only in logs.
		{"looping under a Running enum derives error", StatusRunning, CVDStateRunning, obs(BootVerdictLooping), StatusError, true},
		{"guest-declared failure derives error", StatusCreating, CVDStateStarting, obs(BootVerdictFailed), StatusError, true},
		{"teardown is never resurrected by a loop verdict", StatusDeleting, CVDStateStarting, obs(BootVerdictLooping), StatusDeleting, true},
		// Non-terminal verdicts leave the derivation to the enum.
		{"progress verdict declines in create window", StatusCreating, CVDStateStarting, obs(BootVerdictProgress), "", false},
		{"booted verdict defers to the enum", StatusCreating, CVDStateRunning, obs(BootVerdictBooted), StatusRunning, true},
		{"stalled verdict declines", StatusCreating, CVDStateStarting, obs(BootVerdictStalled), "", false},
		{"no observation behaves as today", StatusCreating, CVDStateStarting, nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Device{Status: tt.status, BootObservation: tt.obs}
			got, ok := DerivePhase(d, tt.cvd)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("DerivePhase(%s, %q, verdict=%v) = (%q, %v), want (%q, %v)",
					tt.status, tt.cvd, tt.obs, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestRecomputeStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		d          Device
		wantStatus Status
		wantChange bool
	}{
		{
			"running cvd publishes running",
			Device{Status: StatusCreating, CVDStatus: CVDStateRunning},
			StatusRunning, true,
		},
		{
			"boot failed publishes error",
			Device{Status: StatusRunning, CVDStatus: CVDStateBootFailed},
			StatusError, true,
		},
		{
			"unreachable leaves status (zombie is a condition)",
			Device{Status: StatusRunning, CVDStatus: CVDStateUnreachable},
			StatusRunning, false,
		},
		{
			"already running is unchanged",
			Device{Status: StatusRunning, CVDStatus: CVDStateRunning},
			StatusRunning, false,
		},
		{
			"deleting is never resurrected",
			Device{Status: StatusDeleting, CVDStatus: CVDStateRunning},
			StatusDeleting, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := tt.d.RecomputeStatus()
			if changed != tt.wantChange || tt.d.Status != tt.wantStatus {
				t.Errorf("RecomputeStatus: status=%q changed=%v, want status=%q changed=%v",
					tt.d.Status, changed, tt.wantStatus, tt.wantChange)
			}
		})
	}
}

func TestIsReconciling(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		d    Device
		want bool
	}{
		{"creating is reconciling", Device{Status: StatusCreating}, true},
		{"starting is reconciling", Device{Status: StatusStarting}, true},
		{"deleting is reconciling", Device{Status: StatusDeleting}, true},
		{"running healthy is settled", Device{Status: StatusRunning, HealthStatus: HealthStatusHealthy}, false},
		{"running zombie is reconciling (recovery in flight)", Device{Status: StatusRunning, HealthStatus: HealthStatusZombie}, true},
		{"stopped is settled", Device{Status: StatusStopped}, false},
		{"error is settled", Device{Status: StatusError}, false},
		{"deleted is settled", Device{Status: StatusDeleted}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.d.IsReconciling(); got != tc.want {
				t.Errorf("IsReconciling() = %v, want %v", got, tc.want)
			}
		})
	}
}
