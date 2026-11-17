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

// Package task defines the durable work-queue domain: the Task entity, its
// lifecycle states, and the Queue and Handler contracts. Producers enqueue a
// task in the same transaction that creates the resource it acts on, and
// in-process workers claim, run, and complete tasks. The queue is backed by
// Cloud Spanner, but this package holds only pure domain types and depends on
// nothing outside the standard library. See
// docs/development/control-plane.md.
package task

import (
	"hash/fnv"
	"math/rand/v2"
	"time"
)

// State is the lifecycle state of a queued task.
//
// A task moves pending -> leased while a worker holds it, then to done on
// success or back to pending on a retryable failure. It reaches the terminal
// failed state (dead-letter) only after exhausting its attempts. The sweeper
// returns a leased task whose lease expired to pending, or dead-letters it
// once its attempts are exhausted.
type State string

// Task lifecycle states.
const (
	StatePending State = "pending" // Eligible to be claimed once AvailableAt passes.
	StateLeased  State = "leased"  // Held by a worker until LeaseExpiry.
	StateDone    State = "done"    // Completed successfully.
	StateFailed  State = "failed"  // Dead-lettered after exhausting MaxAttempts.
)

// IsTerminal reports whether the task has reached a final state.
func (s State) IsTerminal() bool {
	return s == StateDone || s == StateFailed
}

// Defaults applied to newly created tasks.
const (
	// DefaultMaxAttempts bounds retries before a task is dead-lettered.
	DefaultMaxAttempts = 10
	// DefaultShards is the claim-index fan-out; see ShardFor.
	DefaultShards = 16
)

// Priority tiers (controller-runtime's two-tier model): edge
// events claim ahead of resync so detector storms never delay user intent.
const (
	// PriorityEdge is the default for user-initiated work.
	PriorityEdge int64 = 0
	// PriorityResync marks reconciler-enqueued periodic work.
	PriorityResync int64 = -100
)

// Task is one unit of durable background work.
//
// The combination of ID (a UUID, no monotonic hotspot) and Shard spreads the
// claim index across Spanner splits. ResourceID names the device/host the task
// acts on; handlers load their state from it, so redelivery is idempotent.
type Task struct {
	// ID is the unique task identifier (UUID); assigned by the queue at enqueue.
	ID string `json:"id"`

	// Shard is the claim-index bucket, ShardFor(ID, numShards); set at enqueue.
	Shard int `json:"shard"`

	// Type is the dispatch key, e.g. "device.create_cvd".
	Type string `json:"type"`

	// State is the current lifecycle state.
	State State `json:"state"`

	// ResourceID is the device/host this task acts on (idempotency + tracing).
	// It is the task's ONLY argument: handlers re-derive everything else from
	// the resource row, which is what keeps redelivery idempotent and lets any
	// adjacent binary generation claim any task across a rolling deploy.
	ResourceID string `json:"resource_id,omitempty"`

	// TraceContext is the W3C traceparent active when the task was enqueued.
	// Each handling attempt links its own trace back to it. Kept as an
	// opaque string: this package stays stdlib-only.
	TraceContext string `json:"trace_context,omitempty"`

	// AvailableAt is the earliest time the task may be claimed (delay/backoff).
	AvailableAt time.Time `json:"available_at"`

	// Priority orders claiming within a shard (higher first, FIFO within a
	// tier via available_at). Two tiers only: PriorityEdge (default 0) for
	// user-initiated intent, PriorityResync for reconciler-enqueued periodic
	// work, so a detector's enqueue storm never delays a user's create.
	Priority int64 `json:"priority,omitempty"`

	// LeaseExpiry is the visibility timeout while leased; zero when not leased.
	LeaseExpiry time.Time `json:"lease_expiry,omitzero"`

	// LeaseHolder is the worker/instance id holding the lease (diagnostics only).
	LeaseHolder string `json:"lease_holder,omitempty"`

	// Attempts counts delivery attempts so far.
	Attempts int `json:"attempts"`

	// MaxAttempts bounds retries before dead-lettering.
	MaxAttempts int `json:"max_attempts"`

	// LastError records the most recent failure cause.
	LastError string `json:"last_error,omitempty"`

	// CreatedAt and UpdatedAt are persistence timestamps.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// New returns a pending task ready for immediate delivery. The queue assigns ID
// and Shard at enqueue time; CreatedAt/UpdatedAt are set on persist.
func New(taskType, resourceID string) *Task {
	return &Task{
		Type:        taskType,
		ResourceID:  resourceID,
		State:       StatePending,
		MaxAttempts: DefaultMaxAttempts,
		AvailableAt: time.Now(),
	}
}

// Exhausted reports whether the task has used all of its retry attempts.
func (t *Task) Exhausted() bool {
	return t.Attempts >= t.MaxAttempts
}

// ShardFor maps a task ID to one of numShards buckets. Prefixing the claim
// index with the shard spreads claims and state writes across Spanner splits,
// so a high enqueue/claim rate does not funnel into a single AvailableAt key
// range. Deterministic in id. Returns 0 when numShards <= 1.
func ShardFor(id string, numShards int) int {
	if numShards <= 1 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return int(h.Sum32()) % numShards
}

// Backoff returns the delay before the given 1-based retry attempt: exponential
// from base, capped at maxDelay, with equal jitter (a value in [d/2, d]) so that
// concurrent failures do not retry in lockstep.
func Backoff(attempt int, base, maxDelay time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := base
	for i := 1; i < attempt && d < maxDelay; i++ {
		d *= 2
	}
	if d > maxDelay {
		d = maxDelay
	}
	half := d / 2
	//nolint:gosec // retry jitter; not security-sensitive
	return half + time.Duration(rand.Int64N(int64(half)+1))
}
