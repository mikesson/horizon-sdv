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

import "time"

// SnapshotState is a snapshot's lifecycle state. There are only three: a
// snapshot is being taken, is restorable, or died with its host's disk.
// Deleting one deletes the row. A "deleted" state would be a shim for a
// history nobody reads.
type SnapshotState string

const (
	// SnapshotStateCreating means the take is in flight; the directory on the
	// host may be absent or half-written.
	SnapshotStateCreating SnapshotState = "creating"

	// SnapshotStateReady means the take completed and the snapshot is
	// restorable while its host incarnation lives.
	SnapshotStateReady SnapshotState = "ready"

	// SnapshotStateLost means the host's local disk was destroyed after the
	// snapshot was taken, by a re-provision, preemption recovery, zone failover
	// or an out-of-band VM loss. The row survives so the loss is visible instead
	// of silent; the bytes are gone.
	SnapshotStateLost SnapshotState = "lost"
)

// SnapshotStates lists every state a snapshot can report. It must name every
// SnapshotState constant above: the entity's struct tag declares the same set
// a second time, and the agreement test in the device feature slice asserts
// the two agree.
func SnapshotStates() []SnapshotState {
	return []SnapshotState{SnapshotStateCreating, SnapshotStateReady, SnapshotStateLost}
}

// Snapshot is a point-in-time capture of a device's guest state (RAM image
// plus disk overlays) stored on the device's host VM, restorable only onto
// the same stopped device while that host incarnation lives.
//
// Host-pinned on purpose: the Host Orchestrator offers no way to move a
// snapshot off the VM, and every host-recovery path re-provisions from the
// database row alone, so durability beyond the incarnation would be a promise
// the platform cannot keep. The state field says so instead.
type Snapshot struct {
	// ID names the snapshot both here and as the directory name on the host
	// (the Host Orchestrator has no inventory of its own, so this row is it).
	ID string `json:"id"`

	// DeviceID is the device the snapshot was taken from and the only device
	// it can restore onto.
	DeviceID string `json:"device_id"`

	// HostID is the host holding the snapshot's bytes.
	HostID string `json:"host_id"`

	// CVDGroup and CVDName address the CVD on the host. Recorded at take time
	// rather than re-derived at restore so the addressing that took the
	// snapshot is the addressing that restores it.
	CVDGroup string `json:"-"`
	CVDName  string `json:"-"`

	// HostIncarnation is the host incarnation the bytes live on. Internal:
	// the API reports mortality through State, not through a counter the
	// caller would have to interpret.
	HostIncarnation int64 `json:"-"`

	// State is the snapshot's lifecycle state. The enum tag re-declares the
	// SnapshotState constants because a struct tag cannot reference them; an
	// agreement test in the device feature slice keeps the two in sync, and
	// the console's drift gate reads the published enum.
	State SnapshotState `json:"state" enum:"creating,ready,lost"`

	// CreatedBy is the email of the requester (informational only).
	CreatedBy string `json:"created_by,omitempty"`

	// CreatedAt is when the take was admitted.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when the row last changed state.
	UpdatedAt time.Time `json:"updated_at"`
}

// Restorable reports whether the snapshot can be restored right now, given
// its host's current incarnation. A nil return means restorable; otherwise a
// *SnapshotNotRestorableError says why in user-facing terms.
//
// The incarnation check is not redundant with State: lost-marking is a hook,
// and a hook can lose a race with a concurrent re-provision. The comparison
// against the live host row is the check that cannot be stale.
func (s *Snapshot) Restorable(hostIncarnation int64) error {
	switch {
	case s.State == SnapshotStateCreating:
		return &SnapshotNotRestorableError{ID: s.ID, Reason: "the snapshot is still being taken"}
	case s.State == SnapshotStateLost:
		return &SnapshotNotRestorableError{ID: s.ID, Reason: "the snapshot was lost when its host's disk was recreated"}
	case s.HostIncarnation != hostIncarnation:
		return &SnapshotNotRestorableError{ID: s.ID, Reason: "the host was re-provisioned since the snapshot was taken"}
	}
	return nil
}
