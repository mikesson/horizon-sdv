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

import (
	"testing"
	"time"
)

func TestDerivePhase(t *testing.T) {
	t.Parallel()
	now := time.Now()
	fresh := now.Add(-time.Hour)

	tests := []struct {
		name     string
		h        Host
		vmStatus string
		want     Status
		wantOK   bool
	}{
		// desired=absent ALWAYS wins, so the resurrection is inexpressible:
		// even a RUNNING VM with an unrecovered preemption derives deleting.
		{
			"absent wins over everything",
			Host{DesiredState: DesiredAbsent, LastPreemptionID: "op", PreemptedIncarnation: new(int64(0))},
			"RUNNING",
			StatusDeleting, true,
		},
		{
			"error condition derives error",
			Host{DesiredState: DesiredRunning, ErrorMessage: "boom", ErrorOccurredAt: &fresh},
			"",
			StatusError, true,
		},
		{
			// ho_reachable set: the VM is up AND HO has answered -> running.
			"running vm with HO ready derives running",
			Host{DesiredState: DesiredRunning, HOReadyAt: &fresh},
			"RUNNING", StatusRunning, true,
		},
		{
			// ho_reachable unset: VM up but HO not yet answered -> starting.
			"running vm without HO ready derives starting",
			Host{DesiredState: DesiredRunning},
			"RUNNING", StatusStarting, true,
		},
		{
			"staging derives provisioning",
			Host{DesiredState: DesiredRunning},
			"STAGING", StatusProvisioning, true,
		},
		{
			// The current incarnation is preempted: derived immediately,
			// independent of vm_status (no observation-lag wait) and of the op id.
			"unrecovered preemption derives preempted regardless of vm_status",
			Host{DesiredState: DesiredRunning, Incarnation: 0, PreemptedIncarnation: new(int64(0))},
			"",
			StatusPreempted, true,
		},
		{
			// Recovery bumped the incarnation past the preempted one: no longer
			// preempted, so the phase re-derives from the (absent) VM.
			"recovered preemption (incarnation bumped) is no longer preempted",
			Host{DesiredState: DesiredRunning, Incarnation: 1, PreemptedIncarnation: new(int64(0))},
			"",
			"", false,
		},
		{
			// Preemption precedence holds even while the VM still reports a live
			// state, which is the immediacy the incarnation key buys over a
			// vm_status rule.
			"unrecovered preemption wins over a terminated vm",
			Host{DesiredState: DesiredRunning, Incarnation: 2, PreemptedIncarnation: new(int64(2))},
			"TERMINATED",
			StatusPreempted, true,
		},
		{
			"terminated without preemption derives external stop",
			Host{DesiredState: DesiredRunning},
			"TERMINATED", StatusStopped, true,
		},
		{
			"stopping in progress derives stopping (not yet stopped)",
			Host{DesiredState: DesiredRunning},
			"STOPPING", StatusStopping, true,
		},
		{
			"suspending derives stopping",
			Host{DesiredState: DesiredRunning},
			"SUSPENDING", StatusStopping, true,
		},
		{
			// The instance is being deleted (external or in-flight teardown);
			// a transient stop toward gone, same as STOPPING.
			"deprovisioning derives stopping",
			Host{DesiredState: DesiredRunning},
			"DEPROVISIONING", StatusStopping, true,
		},
		{
			"deprovisioning with desired stopped derives stopping",
			Host{DesiredState: DesiredStopped},
			"DEPROVISIONING", StatusStopping, true,
		},
		{
			// GCE is self-healing the host. It is neither an error nor lost, so
			// the same contract as unreachable (running->unreachable->running).
			"repairing derives unreachable",
			Host{DesiredState: DesiredRunning},
			"REPAIRING", StatusUnreachable, true,
		},
		{
			// A dying Spot VM is observed in STOPPING before the reclaim
			// completes; the unrecovered preemption wins over the transient stop.
			"unrecovered preemption takes precedence over a stopping vm",
			Host{DesiredState: DesiredRunning, Incarnation: 0, PreemptedIncarnation: new(int64(0))},
			"STOPPING", StatusPreempted, true,
		},
		{
			"desired stopped with terminated vm derives stopped",
			Host{DesiredState: DesiredStopped},
			"TERMINATED", StatusStopped, true,
		},
		{
			"desired stopped with running vm is underivable",
			Host{DesiredState: DesiredStopped},
			"RUNNING", "", false,
		},
		{
			"no vm and no conditions is underivable",
			Host{DesiredState: DesiredRunning},
			"", "", false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DerivePhase(&tt.h, tt.vmStatus)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("DerivePhase = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestRecomputeStatus(t *testing.T) {
	t.Parallel()
	fresh := time.Now().Add(-time.Hour)

	tests := []struct {
		name       string
		h          Host
		wantStatus Status
		wantChange bool
	}{
		{
			"error condition publishes error",
			Host{Status: StatusProvisioning, DesiredState: DesiredRunning, ErrorMessage: "boom", ErrorOccurredAt: &fresh},
			StatusError, true,
		},
		{
			"desired absent publishes deleting regardless of the vm",
			Host{Status: StatusRunning, DesiredState: DesiredAbsent, VMStatus: "RUNNING"},
			StatusDeleting, true,
		},
		{
			// A stalled teardown is already deleting: stays deleting, no change.
			"already-deleting host is unchanged",
			Host{Status: StatusDeleting, DesiredState: DesiredAbsent},
			StatusDeleting, false,
		},
		{
			// Underivable (pending/provisioning, no VM yet): the owning
			// transition task keeps the status; RecomputeStatus must not touch it.
			"underivable leaves the status untouched",
			Host{Status: StatusProvisioning, DesiredState: DesiredRunning, VMStatus: ""},
			StatusProvisioning, false,
		},
		{
			// The error latch: while the error condition is set, a RUNNING VM
			// observation cannot flip the host back. DerivePhase keeps error
			// until Retry clears the error fields.
			"error latches over a running vm",
			Host{Status: StatusError, DesiredState: DesiredRunning, ErrorMessage: "boom", ErrorOccurredAt: &fresh, VMStatus: "RUNNING"},
			StatusError, false,
		},
		{
			// A condition writer can run over a STALE stored vm_status (Start
			// resets the latch but not the observation): the derivation then
			// yields an edge the FSM forbids, and publication must decline.
			// The next observation writer completes the move from fresh data.
			"stale parked observation cannot regress starting to stopped",
			Host{Status: StatusStarting, DesiredState: DesiredRunning, VMStatus: "TERMINATED"},
			StatusStarting, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := tt.h.RecomputeStatus()
			if changed != tt.wantChange {
				t.Errorf("changed = %v, want %v", changed, tt.wantChange)
			}
			if tt.h.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", tt.h.Status, tt.wantStatus)
			}
		})
	}
}

func TestApplyVMObservation(t *testing.T) {
	t.Parallel()
	fresh := time.Now().Add(-time.Hour)

	tests := []struct {
		name       string
		h          Host
		vmStatus   string
		wantStatus Status
		wantChange bool
	}{
		{
			// The provision read-back lands on starting: VM up, HO not yet
			// confirmed (ho_reachable unset). The device controller promotes later.
			"provisioning adopts a running vm as starting",
			Host{Status: StatusProvisioning, DesiredState: DesiredRunning},
			"RUNNING", StatusStarting, true,
		},
		{
			// Same read-back once HO has answered -> running.
			"provisioning with HO ready adopts a running vm as running",
			Host{Status: StatusProvisioning, DesiredState: DesiredRunning, HOReadyAt: &fresh},
			"RUNNING", StatusRunning, true,
		},
		{
			"pending adopts a staging vm as provisioning",
			Host{Status: StatusPending, DesiredState: DesiredRunning},
			"STAGING", StatusProvisioning, true,
		},
		{
			// The gate exists so a lagging RUNNING observation cannot
			// resurrect a preempted host before recovery runs.
			"preempted is not flipped back to running by a lagging observation",
			Host{Status: StatusPreempted, DesiredState: DesiredRunning},
			"RUNNING", StatusPreempted, false,
		},
		{
			// Error latches over any observation until Retry clears the fields.
			"error latches over a running vm",
			Host{Status: StatusError, DesiredState: DesiredRunning, ErrorMessage: "boom", ErrorOccurredAt: &fresh},
			"RUNNING", StatusError, false,
		},
		{
			// Underivable (no actuation condition): status left to the owner.
			"deleting host keeps its status on a still-running vm",
			Host{Status: StatusDeleting, DesiredState: DesiredAbsent},
			"RUNNING", StatusDeleting, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := tt.h.ApplyVMObservation(tt.vmStatus)
			if changed != tt.wantChange {
				t.Errorf("changed = %v, want %v", changed, tt.wantChange)
			}
			if tt.h.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", tt.h.Status, tt.wantStatus)
			}
			// The raw observation is recorded regardless of whether the phase changed.
			if tt.h.VMStatus != tt.vmStatus {
				t.Errorf("VMStatus = %q, want %q (observation always recorded)", tt.h.VMStatus, tt.vmStatus)
			}
			if tt.h.VMObservedAt == nil {
				t.Error("VMObservedAt not stamped")
			}
		})
	}
}

func TestMarkHOReady(t *testing.T) {
	t.Parallel()

	t.Run("promotes starting to running and records the latch", func(t *testing.T) {
		h := Host{Status: StatusStarting, DesiredState: DesiredRunning, VMStatus: "RUNNING"}
		if changed := h.MarkHOReady(); !changed {
			t.Error("MarkHOReady = false, want true (starting -> running)")
		}
		if h.Status != StatusRunning {
			t.Errorf("status = %q, want running", h.Status)
		}
		if h.HOReadyAt == nil {
			t.Error("HOReadyAt not set")
		}
	})

	t.Run("idempotent once recorded", func(t *testing.T) {
		earlier := time.Now().Add(-time.Hour)
		h := Host{Status: StatusRunning, DesiredState: DesiredRunning, VMStatus: "RUNNING", HOReadyAt: &earlier}
		if changed := h.MarkHOReady(); changed {
			t.Error("MarkHOReady = true, want false (already recorded)")
		}
		if !h.HOReadyAt.Equal(earlier) {
			t.Error("HOReadyAt overwritten; the latch must not move")
		}
	})

	t.Run("stale parked observation records the latch but does not regress", func(t *testing.T) {
		// Stop->Start resets the latch but never refreshes vm_status, which
		// still reads TERMINATED from the stopped window. If HO answers before
		// the next drift sweep stamps RUNNING, the promotion must decline
		// (starting->stopped is not a legal edge) while still recording the
		// latch. The next fresh observation then completes starting->running.
		h := Host{Status: StatusStarting, DesiredState: DesiredRunning, VMStatus: "TERMINATED"}
		if changed := h.MarkHOReady(); changed {
			t.Error("MarkHOReady = true, want false (stale observation must not publish)")
		}
		if h.Status != StatusStarting {
			t.Errorf("status = %q, want starting (never stopped from a stale read)", h.Status)
		}
		if h.HOReadyAt == nil {
			t.Error("HOReadyAt not recorded; the latch itself is the evidence")
		}
	})

	t.Run("backfills a legacy running host without changing status", func(t *testing.T) {
		// A host already running before the condition existed: record the latch,
		// no status change (running -> running).
		h := Host{Status: StatusRunning, DesiredState: DesiredRunning, VMStatus: "RUNNING"}
		if changed := h.MarkHOReady(); changed {
			t.Error("MarkHOReady = true, want false (status unchanged)")
		}
		if h.HOReadyAt == nil {
			t.Error("HOReadyAt not backfilled")
		}
	})
}

func TestIsReconciling(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		h    Host
		want bool
	}{
		{"provisioning is reconciling", Host{Status: StatusProvisioning}, true},
		{"deleting is reconciling", Host{Status: StatusDeleting}, true},
		{"preempted is reconciling (recovery in flight)", Host{Status: StatusPreempted}, true},
		{"running is settled", Host{Status: StatusRunning}, false},
		{"stopped is settled", Host{Status: StatusStopped}, false},
		{"error is settled", Host{Status: StatusError}, false},
		{"stalled teardown is reconciling", Host{Status: StatusDeleting, StalledReason: "boom"}, true},
		{"deleted is settled", Host{Status: StatusDeleted}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.h.IsReconciling(); got != tc.want {
				t.Errorf("IsReconciling() = %v, want %v", got, tc.want)
			}
		})
	}
}
