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
	"fmt"
)

// NotFoundError is returned when a device cannot be found.
type NotFoundError struct {
	ID string
}

func (e *NotFoundError) Error() string {
	if e.ID != "" {
		return "device not found: " + e.ID
	}
	return "device not found"
}

// Is makes any *NotFoundError match the ErrNotFound sentinel via errors.Is.
func (e *NotFoundError) Is(target error) bool {
	_, ok := target.(*NotFoundError)
	return ok
}

// InvalidStateError is returned when a device state transition is invalid.
type InvalidStateError struct {
	Current  Status
	Expected Status
	Message  string
}

func (e *InvalidStateError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Current != "" && e.Expected != "" {
		return fmt.Sprintf("invalid state transition: cannot transition from %s to %s", e.Current, e.Expected)
	}
	return "invalid device state"
}

// Is makes any *InvalidStateError match the ErrInvalidState sentinel via errors.Is.
func (e *InvalidStateError) Is(target error) bool {
	_, ok := target.(*InvalidStateError)
	return ok
}

// StateConflictError is returned when a device's status forbids the action
// asked of it. It carries the status the row actually holds, so a caller can
// tell a conflict it may still make progress against from one a later
// lifecycle change has already settled. An async verb that loses the race to
// a stop or a delete has a definitive outcome to record and nothing to
// retry.
//
// It is part of the ErrInvalidState family rather than a status of its own:
// the synchronous answer for a wrong-state device is the same 409 it has
// always been.
type StateConflictError struct {
	// Action is the verb that was refused, in the vocabulary the user asked in
	// ("restart", "snapshot").
	Action string

	// Required is the status the action needs; Current is what the row says.
	Required Status
	Current  Status
}

func (e *StateConflictError) Error() string {
	return fmt.Sprintf("can only %s %s devices, current status %s", e.Action, e.Required, e.Current)
}

// Unwrap puts the type in the ErrInvalidState family, which is what keeps
// web.MapError's classification of it unchanged.
func (e *StateConflictError) Unwrap() error { return ErrInvalidState }

// AlreadyExistsError is returned when a device with the same identifier already exists.
type AlreadyExistsError struct {
	Name   string
	HostID string
}

func (e *AlreadyExistsError) Error() string {
	if e.Name != "" && e.HostID != "" {
		return fmt.Sprintf("device already exists: %s on host %s", e.Name, e.HostID)
	}
	return "device already exists"
}

// Is makes any *AlreadyExistsError match the ErrAlreadyExists sentinel via errors.Is.
func (e *AlreadyExistsError) Is(target error) bool {
	_, ok := target.(*AlreadyExistsError)
	return ok
}

// ValidationError is returned when a request is semantically invalid beyond
// transport-level schema validation (e.g. mutually exclusive fields both set).
// It has no sentinel: web.MapError matches it structurally.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

// SnapshotNotFoundError is returned when a device has no snapshot with the
// given id. Matched structurally by web.MapError; no sentinel.
type SnapshotNotFoundError struct {
	ID string
}

func (e *SnapshotNotFoundError) Error() string {
	if e.ID != "" {
		return "snapshot not found: " + e.ID
	}
	return "snapshot not found"
}

// SnapshotNotRestorableError is returned when a snapshot exists but cannot be
// restored, because it is still being taken, was lost with its host's disk, or
// was outlived by a host re-provision. Reason is user-facing prose. Matched
// structurally by web.MapError; no sentinel.
type SnapshotNotRestorableError struct {
	ID     string
	Reason string
}

func (e *SnapshotNotRestorableError) Error() string {
	return fmt.Sprintf("snapshot %s cannot be restored: %s", e.ID, e.Reason)
}

// SnapshotLimitError is returned when a device already holds its full
// snapshot budget. Each snapshot is guest RAM plus disk overlays on the
// host's boot disk, so an uncapped collection silently fills the disk under
// every device on the host. Matched structurally by web.MapError; no
// sentinel.
type SnapshotLimitError struct {
	Limit int
}

func (e *SnapshotLimitError) Error() string {
	return fmt.Sprintf("device already has %d snapshots; delete one before taking another", e.Limit)
}

// Sentinels for errors.Is matching; any instance of the corresponding type
// matches via its Is method. HTTP status codes live in web.MapError, the
// single mapper from these families to responses.
var (
	// ErrNotFound is returned when a device cannot be found.
	ErrNotFound = &NotFoundError{}

	// ErrInvalidState is returned when a state transition is invalid.
	ErrInvalidState = &InvalidStateError{}

	// ErrAlreadyExists is returned when a device already exists.
	ErrAlreadyExists = &AlreadyExistsError{}
)
