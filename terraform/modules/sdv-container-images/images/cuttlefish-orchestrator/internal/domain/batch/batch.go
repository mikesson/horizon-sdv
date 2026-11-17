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

// Package batch provides domain types for a Batch: a set of devices created
// and torn down as one unit (the CI/fleet primitive). A batch is a thin
// resource over the device engine. It owns a spec and a membership label
// (devices.batch_id); its status is DERIVED from its member devices, and
// there is no batch reconcile loop (fixed, Job-style membership). The
// persisted Batch row carries only policy and the sweep's settle markers,
// state with no natural home on any single member.
package batch

import (
	"errors"
	"fmt"
	"time"
)

// MaxCount bounds a batch's device count: it caps blast radius, admission
// transaction size, and the fleet a single request can claim.
const MaxCount = 100

// Status is the DERIVED lifecycle of a batch, computed from its member devices
// (DerivePhase). It is never persisted.
type Status string

const (
	// StatusProvisioning means members are still coming up.
	StatusProvisioning Status = "provisioning"
	// StatusReady means at least min_ready members are running with none errored.
	StatusReady Status = "ready"
	// StatusDegraded means the target is met with some errors, or the batch has
	// settled below target.
	StatusDegraded Status = "degraded"
	// StatusFailed means the atomic policy tripped (FailedAt set): a member
	// permanently failed, or the batch missed its ready timeout. Terminal.
	StatusFailed Status = "failed"
	// StatusDeleting means the batch is being torn down.
	StatusDeleting Status = "deleting"
)

// FillPolicy governs the batch's post-admission outcome when members fail.
type FillPolicy string

const (
	// FillBestEffort reports whatever reaches ready; failed members are counted,
	// not healed or escalated. No active machinery beyond the members' own TTL.
	FillBestEffort FillPolicy = "best_effort"
	// FillAtomic fails (and tears down) the whole batch if a member permanently
	// fails or the batch does not fully fill within ready_timeout. The policy
	// applies to the FILL phase only: once every requested member has been
	// running at once (ReadyAt latched), later member loss degrades the batch
	// but never retroactively fails it. Enforced by the reaper's batch sweep.
	FillAtomic FillPolicy = "atomic"
)

// Valid reports whether the fill policy is a known value.
func (f FillPolicy) Valid() bool { return f == FillBestEffort || f == FillAtomic }

// Placement governs how member devices map onto hosts.
type Placement string

const (
	// PlacementIsolated provisions dedicated hosts for the batch (no packing
	// onto shared capacity): zero placement contention and clean teardown. The
	// v1 default.
	PlacementIsolated Placement = "isolated"
	// PlacementPack fills existing free host capacity first (shareable hosts
	// only), then provisions the remainder. This is the most economical
	// option, at the cost of handling placement contention with concurrent
	// creates.
	PlacementPack Placement = "pack"
)

// Valid reports whether the placement is a known value.
func (p Placement) Valid() bool { return p == PlacementIsolated || p == PlacementPack }

// Spec is a batch's create-time intent. The members carry the durable state
// (batch_id, build, ttl, tags); Spec validates the request, drives admission,
// and its policy half is persisted as the Batch row.
type Spec struct {
	NamePrefix string
	Count      int
	MinReady   int
	FillPolicy FillPolicy
	Placement  Placement
	TTLHours   int
	// ReadyTimeout is the atomic fill deadline (0 = none): the batch fails if
	// it has not fully filled within this duration of creation.
	ReadyTimeout time.Duration
}

// Batch is the persisted batch-level state: the admission policy plus the
// settle markers the reaper's batch sweep writes. Everything else about a
// batch (membership, counts, status) stays derived from the member device
// rows. This row is not a status store.
type Batch struct {
	ID             string
	NamePrefix     string
	RequestedCount int
	FillPolicy     FillPolicy
	Placement      Placement
	ReadyTimeout   time.Duration // 0 = no fill deadline
	// SpecFingerprint hashes the admission request (spec + requirements). A
	// keyed retry must present the same fingerprint to resume: reusing a key
	// with a different request is a conflict, never a silent fleet reshape.
	// Empty only on rows written before fingerprinting existed.
	SpecFingerprint string
	CreatedBy       string
	CreatedAt       time.Time
	// ReadyAt latches when every requested member was observed running at
	// once: the atomic fill contract is met and later member loss can only
	// degrade, never fail, the batch.
	ReadyAt *time.Time
	// FailedAt marks a tripped atomic policy; the batch is terminal and its
	// members are torn down.
	FailedAt      *time.Time
	FailureReason string
}

// Status derives the batch's status: a tripped atomic policy is terminal and
// takes precedence over whatever the members roll up to (after teardown there
// are no members left to say anything). minReady is the live non-deleting
// member count. A batch is ready when all its live members are running.
func (b *Batch) Status(r Rollup) Status {
	if b.FailedAt != nil {
		return StatusFailed
	}
	return DerivePhase(r.Total-r.Deleting, r)
}

// Settled reports whether the sweep is done with this batch: it either met
// the fill contract or tripped the policy.
func (b *Batch) Settled() bool { return b.ReadyAt != nil || b.FailedAt != nil }

// Rollup is the observed state of a batch's member devices, computed by the
// feature layer (which reads the live device rows) and fed to DerivePhase.
// Keeping it as plain counts keeps this package free of a device-domain import.
type Rollup struct {
	Total       int // live (non-deleted) member devices
	Running     int // status == running (the readiness signal)
	Errored     int // status == error (terminal per-device failure)
	Deleting    int // members tearing down
	Reconciling int // members still converging toward running (excludes teardown)
}

// DerivePhase computes a batch's status from its members. Any member still
// converging (excluding teardown) means the batch is provisioning. It is never
// reported ready while a member is being created, restarted, or recreated, so
// a running-but-zombie member correctly un-settles the batch. Once settled,
// ready means at least minReady members are running with none errored.
// Anything else is degraded, including a target met with failures and a
// permanent best_effort shortfall. Deleting means every member is tearing down.
// (StatusFailed comes only from Batch.Status, off the persisted FailedAt.)
func DerivePhase(minReady int, r Rollup) Status {
	switch {
	case r.Total > 0 && r.Deleting == r.Total:
		return StatusDeleting
	case r.Reconciling > 0:
		return StatusProvisioning
	case r.Running >= minReady && r.Errored == 0:
		return StatusReady
	default:
		return StatusDegraded
	}
}

// ErrInvalidSpec is returned by Validate for a malformed batch spec.
var ErrInvalidSpec = errors.New("invalid batch spec")

// ErrNotFound reports that no batch has the given id: no policy row and no
// member devices (a failed batch keeps its row, so it stays readable).
var ErrNotFound = errors.New("batch not found")

// ErrFailed reports a keyed create replaying a batch whose atomic policy
// already tripped: its fleet is gone and the per-resource idempotency state is
// dead, so the caller must delete the batch or use a fresh key.
var ErrFailed = errors.New("batch failed")

// Validate checks the primitive spec constraints (the requirements themselves
// are validated by the resolver at the feature layer).
func (s *Spec) Validate() error {
	switch {
	case s.NamePrefix == "":
		return fmt.Errorf("%w: name_prefix is required", ErrInvalidSpec)
	case s.Count < 1 || s.Count > MaxCount:
		return fmt.Errorf("%w: count must be between 1 and %d", ErrInvalidSpec, MaxCount)
	case s.MinReady < 1 || s.MinReady > s.Count:
		return fmt.Errorf("%w: min_ready must be between 1 and count", ErrInvalidSpec)
	case !s.FillPolicy.Valid():
		return fmt.Errorf("%w: fill_policy must be best_effort or atomic", ErrInvalidSpec)
	case !s.Placement.Valid():
		return fmt.Errorf("%w: placement must be isolated or pack", ErrInvalidSpec)
	case s.TTLHours < 0:
		return fmt.Errorf("%w: ttl_hours must not be negative", ErrInvalidSpec)
	case s.ReadyTimeout < 0:
		return fmt.Errorf("%w: ready_timeout_seconds must not be negative", ErrInvalidSpec)
	case s.ReadyTimeout > 0 && s.FillPolicy != FillAtomic:
		return fmt.Errorf("%w: ready_timeout_seconds requires fill_policy atomic", ErrInvalidSpec)
	case s.FillPolicy == FillAtomic && s.MinReady != s.Count:
		// Atomic is all-or-nothing by definition; a lower floor is a
		// contradiction better rejected than silently reinterpreted.
		return fmt.Errorf("%w: atomic fill_policy requires min_ready equal to count", ErrInvalidSpec)
	}
	return nil
}
