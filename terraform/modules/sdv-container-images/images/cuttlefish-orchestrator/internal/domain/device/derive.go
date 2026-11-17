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

import (
	"slices"
	"time"
)

// CVD fleet-status strings as the Host Orchestrator reports them (the cvd
// tool's HumanFriendlyStateName of its InstanceState enum). This is the
// device's raw observation, the CVD-plane analog of a host's vm_status.
// Because `cvd fleet` is itself the live run_cvd probe, CVDStateRunning already
// means the launcher answered (healthy) and CVDStateUnreachable means it did
// not (the zombie signal); the status string carries liveness on its own.
//
// This is the SINGLE source of truth for CVD wire-state strings. The Host
// Orchestrator client (platform/hoclient) imports these rather than redefining
// them. Domain cannot import platform, so the canonical set lives here and the
// platform layer references it, keeping the two from drifting.
const (
	CVDStateRunning       = "Running"
	CVDStateStarting      = "Starting"
	CVDStatePreparing     = "Preparing"
	CVDStateStopping      = "Stopping"
	CVDStateStopped       = "Stopped"
	CVDStateBootFailed    = "Boot failed"
	CVDStatePrepareFailed = "Prepare failed"
	CVDStateUnreachable   = "Unreachable"
	CVDStateCancelled     = "Cancelled" //nolint:misspell // cvd builds the fleet string by title-casing INSTANCE_STATE_CANCELLED, so the American spelling would match no status on the wire.
	CVDStateUnspecified   = "Unspecified"
	// CVDStateUnknown is not an upstream InstanceState: the HO client synthesizes
	// it when the fleet status is unavailable (the operator /devices fallback
	// carries no status). DerivePhase treats it like Unspecified and declines.
	CVDStateUnknown = "Unknown"
)

// instanceStatePrecedence orders the CVD wire states from the one that decides
// a whole group down to the one that yields to every other. It is a written
// table rather than min() over the enum because upstream's InstanceState is not
// severity-ordered (RUNNING=3, STOPPING=4, STOPPED=5, BOOT_FAILED=7) and CCO's
// own Status is a third enum again.
//
// Four rows encode a decision beyond plain ordering:
//
//   - Terminal failures lead. Upstream resets the entire group when any
//     instance fails to boot, so one boot-failed instance is a dead group.
//     DerivePhase settles that to error outside the zombie recreate budget,
//     which is right, because re-running the same build fails again.
//   - An absent instance outranks an unreachable one. Both are the zombie
//     signal, but the cheap restart rung fires on exactly Unreachable, and
//     relaunching a group cannot bring back an instance that is not there.
//     Reporting the absence spends no restart budget on a group that needs a
//     recreate.
//   - Every transient outranks Running, which answers the case that decides
//     this table: during a per-instance restart, starting IS worse than
//     running. A group with one instance still coming up declines the
//     derivation and holds its status, instead of reporting a device that is
//     only partly up as running.
//   - Running is last, so it wins only unanimously, which is the definition
//     of a running group.
var instanceStatePrecedence = []string{
	CVDStateBootFailed,
	CVDStatePrepareFailed,
	CVDStateCancelled,
	"", // Absent from a fleet listing that did answer.
	CVDStateUnreachable,
	CVDStateUnspecified,
	CVDStateUnknown,
	CVDStatePreparing,
	CVDStateStarting,
	CVDStateStopping,
	CVDStateStopped,
	CVDStateRunning,
}

// instanceStateRank is a state's position in instanceStatePrecedence; lower
// dominates. A state the table does not name ranks with CVDStateUnspecified.
// That is below the failures the table does name, so hard evidence still
// decides a mixed group, and above every benign state, so a group of
// unrecognized states can never read as running. Upstream has grown this enum
// before, and dropping what it does not recognize is what would make that
// growth silent.
func instanceStateRank(state string) int {
	if i := slices.Index(instanceStatePrecedence, state); i >= 0 {
		return i
	}
	return slices.Index(instanceStatePrecedence, CVDStateUnspecified)
}

// FoldInstanceStates reports the one CVD wire state that stands for a whole
// group: the state of its worst instance, by instanceStatePrecedence. Ties keep
// the earliest instance, so the answer follows the host's own listing order.
//
// It returns a wire state rather than a Status so that the existing derivation
// consumes it unchanged. DerivePhase still owns the teardown latch, the boot
// verdict and the DECLINE arm that leaves pending/creating to the create
// task, and the health condition still reads the raw state. A fold
// straight to Status would have to restate all of that, and could not express
// the decline at all.
//
// No instances is the empty state: a device whose group has not been observed
// is in exactly the position of one whose CVD was missing from the listing.
func FoldInstanceStates(instances []Instance) string {
	worst, worstRank := "", len(instanceStatePrecedence)
	for _, inst := range instances {
		if rank := instanceStateRank(inst.State); rank < worstRank {
			worst, worstRank = inst.State, rank
		}
	}
	return worst
}

// DerivePhase computes a device's lifecycle status from its raw CVD
// observation. It mirrors host.DerivePhase: first-match rules, a device being
// torn down always keeps that phase (a failed CVD never resurrects a delete
// into "error"), and observations the CVD plane cannot settle decline so the
// status stays with its owner (the create task or the device controller):
//
//   - Starting/Preparing are create-owned transients (the create task owns
//     pending/creating until the CVD is up),
//   - Unreachable / an absent CVD ("") are the zombie signal, a condition the
//     device controller counts and device.reconcile acts on, not a lifecycle change.
//
// cvdStatus is the last observed fleet status ("" = the device's CVD was not in
// the fleet listing). An explicit CVD failure (boot/prepare failed, canceled)
// is terminal and derives error. Re-running the same build would fail again,
// so it does not ride the zombie recreate budget.
//
// The boot-observation rule is checked before the enum and independently of
// it: a post-create boot loop latches the fleet enum at Running (run_cvd
// answers the status probe with an unconditional success however the guest
// behaves), so an enum-preconditioned rule could never fire there. A looping
// or guest-declared-failed verdict is the log-derived equivalent of
// CVDStateBootFailed. It is terminal for the same reason, and outside the
// zombie recreate budget for the same reason. Verdict and enum are written as
// one sample, so the pair this rule reads is coherent.
func DerivePhase(d *Device, cvdStatus string) (Status, bool) {
	switch d.Status {
	case StatusDeleting, StatusDeleted:
		return d.Status, true
	case StatusPending, StatusCreating, StatusStarting, StatusRunning, StatusStopping,
		StatusStopped, StatusError:
		// The CVD observation is allowed to settle these.
	}
	if o := d.BootObservation; o != nil && o.Verdict.Terminal() {
		return StatusError, true
	}
	switch cvdStatus {
	case CVDStateRunning:
		return StatusRunning, true
	case CVDStateStopping:
		return StatusStopping, true
	case CVDStateStopped:
		return StatusStopped, true // stopped out of band
	case CVDStateBootFailed, CVDStatePrepareFailed, CVDStateCancelled:
		return StatusError, true
	default:
		// Starting/Preparing (create-owned), Unreachable / absent (health- and
		// reconcile-owned zombie signal), Unspecified/unmodeled: decline.
		return "", false
	}
}

// RecomputeStatus publishes the lifecycle status derived from the device's
// current CVD observation, when the observation settles it. A declined
// derivation leaves the status to its owner (the create task, the health
// monitor or device.reconcile). Reports whether Status changed. The health
// monitor writes this transition through the CAS-safe repository UpdateStatus rather than
// calling this directly; this method is the in-memory choke point for callers
// that mutate a device under a transaction.
func (d *Device) RecomputeStatus() bool {
	derived, ok := DerivePhase(d, d.CVDStatus)
	if !ok || derived == d.Status {
		return false
	}
	d.Status = derived
	d.UpdatedAt = time.Now()
	return true
}

// CVDStateIsTransient reports a CVD still coming up (create/start in flight).
// These are expected mid-bring-up and must not count as health failures.
func CVDStateIsTransient(cvdStatus string) bool {
	return cvdStatus == CVDStateStarting || cvdStatus == CVDStatePreparing
}

// CVDStateIsUnobserved reports a state that carries no information about the
// CVD, as opposed to evidence about it. The client synthesizes Unknown when
// the fleet listing is unavailable and it falls back to the operator's device
// list, which has no status field: that means "we could not look", which is
// not the same as the zombie signal (absent from a listing that DID answer).
// Counting it as a health failure fails every device on the host toward zombie
// while all of them are fine.
func CVDStateIsUnobserved(cvdStatus string) bool {
	return cvdStatus == CVDStateUnknown
}
