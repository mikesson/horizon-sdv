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

// Model-based tests for the host state machine and lease.
//
// The host's transition table is the more involved of the two: it admits
// externally observed drift (a VM stopped outside the orchestrator) and
// reclassification (a dying Spot VM observed as stopping before it is
// understood as preempted). Generated sequences are worth more here than
// anywhere else in the domain, because those edges exist so that whichever
// observation lands first does not foreclose the others.

package host

import (
	"errors"
	"testing"

	"pgregory.net/rapid"
)

var allStatuses = []Status{
	StatusPending, StatusProvisioning, StatusStarting, StatusRunning,
	StatusStopping, StatusStopped, StatusDeleting, StatusDeleted,
	StatusError, StatusUnreachable, StatusPreempted,
}

func TestHostStatusMachineNeverLeavesTheTransitionTable(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		h := &Host{Status: rapid.SampledFrom(allStatuses).Draw(t, "initial")}
		model := h.Status

		t.Repeat(map[string]func(*rapid.T){
			"transition": func(t *rapid.T) {
				target := rapid.SampledFrom(allStatuses).Draw(t, "target")
				allowed := model.CanTransitionTo(target)
				err := h.TransitionTo(target)

				if allowed {
					if err != nil {
						t.Fatalf("table permits %s -> %s but TransitionTo refused: %v", model, target, err)
					}
					model = target
					return
				}
				if err == nil {
					t.Fatalf("table forbids %s -> %s but TransitionTo allowed it", model, target)
				}
				// Refusing is half the contract; refusing in the family is the
				// other half. The provision task and the retry path both return
				// this error unwrapped, so a refusal outside the family reaches
				// the transport as a 500 instead of the 409 web.MapError owes it.
				if !errors.Is(err, ErrInvalidState) {
					t.Fatalf("refusal of %s -> %s is outside the ErrInvalidState family: %v", model, target, err)
				}
				if h.Status != model {
					t.Fatalf("refused transition still mutated status: %s -> %s", model, h.Status)
				}
			},
			"": func(t *rapid.T) {
				if h.Status != model {
					t.Fatalf("status %s diverged from model %s", h.Status, model)
				}
			},
		})
	})
}

// A deleted host must stay deleted: the cascade that marks its devices lost
// runs once, and a revived host would strand them.
func TestHostDeletedIsAbsorbing(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		target := rapid.SampledFrom(allStatuses).Draw(t, "target")
		h := &Host{Status: StatusDeleted}

		if err := h.TransitionTo(target); err == nil {
			t.Fatalf("deleted -> %s was allowed; deleted must be absorbing", target)
		}
		if h.Status != StatusDeleted {
			t.Fatalf("status left deleted for %s", h.Status)
		}
	})
}

// Every non-terminal status must be able to reach deleting or deleted.
// Without this a host can become unreclaimable and its VM bills forever,
// which strands a real VM.
func TestEveryLiveStatusCanReachTeardown(t *testing.T) {
	t.Parallel()
	for _, s := range allStatuses {
		if s == StatusDeleted {
			continue
		}
		if !s.CanTransitionTo(StatusDeleting) && !s.CanTransitionTo(StatusDeleted) {
			t.Errorf("%s can reach neither deleting nor deleted; a host there is unreclaimable", s)
		}
	}
}

// A protected host (one holding pinned devices) must never report expired.
// That is the host half of the pin invariant. A device cannot outlive its
// host, so the host must not be reaped under a pinned device.
func TestProtectedHostsNeverExpire(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		l := NewEphemeralLifecycle(rapid.Int64Range(1, 7*24*3600).Draw(t, "ttl"))
		model := int64(0) // Shadow count, clamped at zero the same way.

		t.Repeat(map[string]func(*rapid.T){
			"adjust": func(t *rapid.T) {
				delta := rapid.Int64Range(-3, 3).Draw(t, "delta")
				l.IncrementProtectedCount(delta)
				model = max(model+delta, 0)
			},
			"": func(t *rapid.T) {
				if l.ProtectedDeviceCount != model {
					t.Fatalf("protected count %d diverged from model %d", l.ProtectedDeviceCount, model)
				}
				if l.ProtectedDeviceCount < 0 {
					t.Fatalf("protected count went negative: %d", l.ProtectedDeviceCount)
				}
				if l.IsProtected() && l.IsExpired() {
					t.Fatal("a protected host reported expired; the reaper would take it under its pinned devices")
				}
				if l.IsProtected() == l.IsEphemeral() {
					t.Fatalf("protected and ephemeral must be exclusive (count=%d, mode=%q)",
						l.ProtectedDeviceCount, l.Mode)
				}
			},
		})
	})
}
