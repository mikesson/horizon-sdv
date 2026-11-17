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

// Model-based tests for the device state machine and lifecycle.
//
// The example-based tests beside this file check transitions someone thought
// of. These generate sequences instead, and check properties that must hold
// after every sequence. The model is written by hand and kept independent of
// the implementation: a property that reads its answer out of the code under
// test cannot disagree with it.

package device

import (
	"errors"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// allStatuses is the full vocabulary, including states the machine can never
// leave. Generating targets from it (rather than from ValidTransitions) is the
// point: most generated transitions are rejections, and the rejections are
// what the terminal-state properties rest on.
var allStatuses = []Status{
	StatusPending, StatusCreating, StatusStarting, StatusRunning,
	StatusStopping, StatusStopped, StatusDeleting, StatusDeleted, StatusError,
}

// TestStatusMachineNeverLeavesTheTransitionTable drives random transition
// sequences and holds the status to what the table permits. The model is a
// single shadow variable: whatever the table said was reachable.
func TestStatusMachineNeverLeavesTheTransitionTable(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		d := &Device{Status: rapid.SampledFrom(allStatuses).Draw(t, "initial")}
		model := d.Status

		t.Repeat(map[string]func(*rapid.T){
			"transition": func(t *rapid.T) {
				target := rapid.SampledFrom(allStatuses).Draw(t, "target")
				allowed := model.CanTransitionTo(target)
				err := d.TransitionTo(target)

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
				// other half. web.MapError answers 409 off this match, and the
				// reaper tolerates a lapsed-pin race by it. A refusal outside
				// the family is a silent 500 and an untolerated sweep failure.
				if !errors.Is(err, ErrInvalidState) {
					t.Fatalf("refusal of %s -> %s is outside the ErrInvalidState family: %v", model, target, err)
				}
				if d.Status != model {
					t.Fatalf("refused transition still mutated status: %s -> %s", model, d.Status)
				}
			},
			"setError": func(t *rapid.T) {
				// Documented to bypass the machine, so the model follows it
				// unconditionally rather than consulting the table.
				d.SetError("generated")
				model = StatusError
				if d.Status != StatusError {
					t.Fatalf("SetError left status at %s", d.Status)
				}
			},
			"": func(t *rapid.T) {
				if d.Status != model {
					t.Fatalf("status %s diverged from model %s", d.Status, model)
				}
			},
		})
	})
}

// TestDeletedIsAbsorbing pins the one property the whole teardown path rests
// on: nothing revives a deleted device. ValidTransitions has no entry for it,
// which is easy to break by adding one.
func TestDeletedIsAbsorbing(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		target := rapid.SampledFrom(allStatuses).Draw(t, "target")
		d := &Device{Status: StatusDeleted}

		if err := d.TransitionTo(target); err == nil {
			t.Fatalf("deleted -> %s was allowed; deleted must be absorbing", target)
		}
		if d.Status != StatusDeleted {
			t.Fatalf("status left deleted for %s", d.Status)
		}
		if !d.Status.IsTerminal() {
			t.Fatal("deleted must report IsTerminal")
		}
	})
}

// TestErrorIsRecoverable is the mirror of the above, and guards a property the
// system depends on in the other direction: error must NOT be absorbing, since
// a retried creation task recovers a device whose CVD failed to start.
func TestErrorIsRecoverable(t *testing.T) {
	t.Parallel()
	recoverable := []Status{StatusCreating, StatusRunning, StatusDeleting, StatusDeleted}
	for _, target := range recoverable {
		d := &Device{Status: StatusError}
		if err := d.TransitionTo(target); err != nil {
			t.Errorf("error -> %s must stay available for recovery: %v", target, err)
		}
	}
}

// drawLifecycle generates a lifecycle in either mode, with the field
// combinations the constructors actually produce.
func drawLifecycle(t *rapid.T) *DeviceLifecycle {
	ttl := rapid.Int64Range(1, 7*24*3600).Draw(t, "ttl")
	l := NewEphemeralLifecycle(ttl)
	if rapid.Bool().Draw(t, "startPinned") {
		var until *time.Time
		if rapid.Bool().Draw(t, "hasPinnedUntil") {
			u := time.Now().Add(time.Duration(rapid.Int64Range(1, 7*24*3600).Draw(t, "pinFor")) * time.Second)
			until = &u
		}
		l.Pin("generated-user", PinReasonMemoryLeakDebug, "", until)
	}
	return &l
}

// TestPinnedDevicesNeverExpire holds the lifecycle invariant that a pinned
// device is not reaped until its pin expires. The reaper half of that needs
// the repository; this half holds the type to its contract so the reaper is
// never handed an expired pinned device in the first place.
func TestPinnedDevicesNeverExpire(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		l := drawLifecycle(t)

		t.Repeat(map[string]func(*rapid.T){
			"pin": func(t *rapid.T) {
				l.Pin("generated-user", PinReasonMemoryLeakDebug, "", nil)
			},
			"unpin": func(t *rapid.T) {
				l.Unpin()
			},
			"extend": func(t *rapid.T) {
				_ = l.Extend(rapid.Int64Range(-10, 7*24*3600).Draw(t, "seconds"), DefaultExtendCount)
			},
			"": func(t *rapid.T) {
				if l.IsPinned() && l.IsExpired() {
					t.Fatal("a pinned device reported expired; the reaper would take it")
				}
				if l.IsPinned() && l.ExpiresAt != nil {
					t.Fatal("pinned lifecycle still carries ExpiresAt")
				}
				if l.IsEphemeral() && l.ExpiresAt == nil {
					t.Fatal("ephemeral lifecycle has no ExpiresAt; it can never be reaped")
				}
				if l.IsPinned() == l.IsEphemeral() {
					t.Fatalf("mode %q is neither exclusively pinned nor ephemeral", l.Mode)
				}
			},
		})
	})
}

// TestExtendIsBoundedByAllowance holds the quota half of extension, for
// every allowance the policy surface can grant, including zero, which must
// refuse the first extension.
func TestExtendIsBoundedByAllowance(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		l := NewEphemeralLifecycle(rapid.Int64Range(1, 7*24*3600).Draw(t, "ttl"))
		allowance := rapid.IntRange(0, MaxExtendCount).Draw(t, "allowance")
		granted := 0

		t.Repeat(map[string]func(*rapid.T){
			"extend": func(t *rapid.T) {
				if err := l.Extend(rapid.Int64Range(-10, 7*24*3600).Draw(t, "seconds"), allowance); err == nil {
					granted++
				}
			},
			"": func(t *rapid.T) {
				if granted > allowance {
					t.Fatalf("granted %d extensions, allowance is %d", granted, allowance)
				}
				if l.ExtendCount != granted {
					t.Fatalf("ExtendCount %d disagrees with granted extensions %d", l.ExtendCount, granted)
				}
			},
		})
	})
}

// TestExtendNeverShortensTheLease is the property the name promises: an
// operation called Extend must not move expiry closer.
func TestExtendNeverShortensTheLease(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		l := NewEphemeralLifecycle(rapid.Int64Range(1, 7*24*3600).Draw(t, "ttl"))

		t.Repeat(map[string]func(*rapid.T){
			"extend": func(t *rapid.T) {
				before := *l.ExpiresAt
				if err := l.Extend(rapid.Int64Range(-10, 7*24*3600).Draw(t, "seconds"), DefaultExtendCount); err != nil {
					return // Refused extensions change nothing; the invariant is trivially kept.
				}
				if l.ExpiresAt.Before(before) {
					t.Fatalf("Extend moved expiry backwards: %s -> %s (lease cut by %s)",
						before, *l.ExpiresAt, before.Sub(*l.ExpiresAt))
				}
			},
		})
	})
}
