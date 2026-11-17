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

package host

import "time"

// GCE instance lifecycle states as the Compute API reports them. This is the
// single source of truth for raw vm_status strings, mirroring the device side's
// CVDState* block. Every consumer of a GCE instance status (the derivation
// below, the provision adopt guard, the drift writer) reads these instead of
// scattering wire literals. GCE has no DELETING state: a deleted instance
// simply stops existing.
const (
	GCEVMStateProvisioning   = "PROVISIONING"
	GCEVMStateStaging        = "STAGING"
	GCEVMStateRunning        = "RUNNING"
	GCEVMStateStopping       = "STOPPING"
	GCEVMStateSuspending     = "SUSPENDING"
	GCEVMStatePendingStop    = "PENDING_STOP"
	GCEVMStateSuspended      = "SUSPENDED"
	GCEVMStateRepairing      = "REPAIRING"
	GCEVMStateTerminated     = "TERMINATED"
	GCEVMStateDeprovisioning = "DEPROVISIONING"
)

// GCEVMStateStopMotion reports a VM observed mid-stop (or mid-teardown): not
// serving, not yet settled. The derivation reads it as 'stopping'; the
// provision adopt guard waits such an instance out rather than adopting or
// condemning it.
func GCEVMStateStopMotion(vmStatus string) bool {
	switch vmStatus {
	case GCEVMStateStopping, GCEVMStateSuspending, GCEVMStatePendingStop, GCEVMStateDeprovisioning:
		return true
	}
	return false
}

// GCEVMStateParked reports a VM settled in a not-running state it will not
// leave on its own (stopped, suspended).
func GCEVMStateParked(vmStatus string) bool {
	return vmStatus == GCEVMStateTerminated || vmStatus == GCEVMStateSuspended
}

// DerivePhase computes the lifecycle phase from the spec (desired_state) and
// observations (raw VM state, error and preemption conditions). It is the
// single kstatus-style derivation every GCE-observation writer goes through: the
// background drift writer (the host reconciler's planGCEUpdate), the provision read-back and
// the manual Sync (both via ApplyVMObservation). First matching rule wins;
// desired=absent always wins, which is what makes the F7 deleting-host
// resurrection inexpressible.
//
// ok=false ("underivable") is a decline on purpose and not a stub: the phases
// it declines are owned elsewhere, and DerivePhase never guesses them.
//   - pending vs provisioning (no VM observed yet) is an ACTUATION fact the
//     provision task owns. It is not observable from GCE, so an empty vmStatus
//     leaves the owner's status in place (the reconcile handler and the
//     stale-transitional sweep re-drive a stuck provision; a VM that was up and
//     then vanished is the missing-VM/MarkHostLost path, not this one).
//   - a mid-flight stop (RUNNING while desired=stopped) has no actuation
//     condition to distinguish "not yet acted" from "in progress".
//
// The caller keeps the current status on a decline; the raw observation is still
// recorded regardless.
//
// vmStatus is the raw GCE state observed THIS cycle ("" = no VM found under
// the host's current instance name).
func DerivePhase(h *Host, vmStatus string) (Status, bool) {
	switch {
	case h.DesiredState == DesiredAbsent:
		return StatusDeleting, true

	case h.ErrorMessage != "" && h.ErrorOccurredAt != nil:
		return StatusError, true

	case h.hasUnrecoveredPreemption():
		// Derived from the evidence (which incarnation was preempted), NOT from
		// vm_status, so it is immediate, without waiting for the VM observation
		// to catch up (the lag that once forced a direct status mark). Recovery
		// bumps the incarnation, flipping this false so the phase re-derives to
		// provisioning, with no clear and no re-preemption loop.
		return StatusPreempted, true

	case h.DesiredState == DesiredStopped:
		return derivePhaseStopped(vmStatus)

	default: // DesiredRunning (or unset legacy rows)
		return derivePhaseRunning(h, vmStatus)
	}
}

// derivePhaseStopped derives the phase of a host asked to stop, from the raw VM
// observation alone.
func derivePhaseStopped(vmStatus string) (Status, bool) {
	switch {
	case GCEVMStateStopMotion(vmStatus):
		return StatusStopping, true
	case GCEVMStateParked(vmStatus):
		return StatusStopped, true
	default:
		// RUNNING with desired=stopped is a stop not yet acted on or
		// mid-flight, indistinguishable without an actuation condition.
		// REPAIRING likewise declines rather than guesses.
		return "", false
	}
}

// derivePhaseRunning derives the phase of a host asked to run. It is also the
// arm legacy rows with an unset desired_state land in.
func derivePhaseRunning(h *Host, vmStatus string) (Status, bool) {
	switch {
	case vmStatus == GCEVMStateRunning:
		// RUNNING never implies guest readiness, so the VM being up is only
		// 'starting' until the Host Orchestrator answers. HOReadyAt is that
		// condition (the device controller sets it on first HO contact); until
		// then placement still packs onto the host but CVD-create postpones,
		// so devices wait for a serving HO instead of firing doomed creates.
		if h.HOReadyAt == nil {
			return StatusStarting, true
		}
		return StatusRunning, true
	case vmStatus == GCEVMStateStaging || vmStatus == GCEVMStateProvisioning:
		return StatusProvisioning, true
	case vmStatus == GCEVMStateRepairing:
		// GCE is self-healing the host (automaticRestart, typically a few
		// minutes) and returns it to its prior state: not serving, expected
		// back, which is the unreachable contract (running->unreachable->running).
		// Never a lost-mark or error; the repair is GCE's, not ours.
		return StatusUnreachable, true
	case GCEVMStateStopMotion(vmStatus):
		// A transient stop/teardown in progress. A Spot preemption is caught by
		// DerivePhase's hasUnrecoveredPreemption branch (the ops scan runs first
		// and pins the condition), so it never reaches here.
		return StatusStopping, true
	case GCEVMStateParked(vmStatus):
		return StatusStopped, true // externally stopped
	case vmStatus == "":
		// No VM under the current name: pending/provisioning (actuation-owned,
		// not yet inserted) or a vanished VM (the missing-VM/MarkHostLost
		// path). Neither is derivable here, so decline and let the owner keep
		// the status. (A preemption is DerivePhase's branch, not this one.)
		return "", false
	default:
		// Anything unmodeled (a status the API is not documented to return
		// for our VMs): decline rather than guess.
		return "", false
	}
}

// RecomputeStatus derives the host's lifecycle phase from its current
// conditions (DerivePhase over desired_state, the error condition, the raw VM
// observation and preemption) and publishes it to Status when the derivation
// is conclusive. It is the single choke point the condition writers go through:
// a writer sets its condition fields (an error, a stall, a fresh observation)
// then calls RecomputeStatus, and the phase falls out of the derivation instead
// of being hand-transitioned. An underivable phase (DerivePhase ok=false)
// leaves Status untouched: the transition task that owns pending/provisioning
// keeps it. Reports whether Status changed.
//
// Publication rides the same transition gate as ApplyVMObservation: the
// derivation can run over a stale stored vm_status (a condition writer sets
// its fields without refreshing the observation), and an illegal edge means
// exactly that. MarkHOReady on a just-started host whose vm_status still
// reads TERMINATED must not publish starting->stopped; the next observation
// writer completes the move from fresh data. Every intended publication
// (error latching, preemption, the ho_reachable promotion) has legal edges
// from its real source states.
//
// Preemption is the one status still MARKED rather than recomputed here: the
// recovery trigger reads a written 'preempted', and at mark time the VM
// observation lags the preemption evidence, so deriving it from vm_status would
// make recovery race the next sync. RecomputeStatus still derives 'preempted'
// once the VM observation catches up (freshlyPreempted and the VM gone).
func (h *Host) RecomputeStatus() bool {
	derived, ok := DerivePhase(h, h.VMStatus)
	if !ok || derived == h.Status || !h.Status.CanTransitionTo(derived) {
		return false
	}
	h.Status = derived
	h.UpdatedAt = time.Now()
	return true
}

// ApplyVMObservation records a fresh raw GCE state observation on the host and
// re-derives the lifecycle phase from it, behind the transition gate. It is the
// shared path for the on-demand observation writers (the provision read-back
// and the manual Sync), mirroring per host what the background drift writer
// (the host reconciler's planGCEUpdate) does: the raw vm_status is always recorded (it is an
// observation, kept even when the derived phase is blocked by an app-owned
// state), while the derived phase is published only when it is conclusive AND
// the current status may legally transition to it. The gate is what stops a
// lagging RUNNING observation from flipping a preempted host back to running
// before recovery runs (preempted->running is not a valid transition). Reports
// whether Status changed.
func (h *Host) ApplyVMObservation(vmStatus string) bool {
	now := time.Now()
	h.VMStatus = vmStatus
	h.VMObservedAt = &now
	derived, ok := DerivePhase(h, vmStatus)
	if !ok || derived == h.Status || !h.Status.CanTransitionTo(derived) {
		return false
	}
	h.Status = derived
	h.UpdatedAt = now
	return true
}

// MarkHOReady records that the Host Orchestrator answered for the first time
// this incarnation (the ho_reachable condition) and re-derives the phase, which
// promotes the first-boot host 'starting' -> 'running'. Idempotent: a no-op once
// HOReadyAt is set, so re-probing a running host (and backfilling a host that
// was already running before this condition existed) costs nothing. Reports
// whether Status changed. The promotion rides RecomputeStatus over the stored
// vm_status, so if that observation is momentarily stale the next reconciler cycle
// completes 'starting' -> 'running' from the same condition, with no imperative
// transition that could disagree with the derivation.
func (h *Host) MarkHOReady() bool {
	if h.HOReadyAt != nil {
		return false
	}
	now := time.Now()
	h.HOReadyAt = &now
	return h.RecomputeStatus()
}

// hasUnrecoveredPreemption reports whether the CURRENT incarnation was preempted
// and has not yet been re-provisioned. RetryDelayed bumps the incarnation on
// recovery, so a recovered host (preempted_incarnation < incarnation) is no
// longer preempted. The condition auto-clears without a write, and a
// re-provisioning host cannot re-derive preempted from its predecessor's
// preemption (the loop a PreemptedAt-window rule would allow).
func (h *Host) hasUnrecoveredPreemption() bool {
	return h.PreemptedIncarnation != nil && *h.PreemptedIncarnation == h.Incarnation
}
