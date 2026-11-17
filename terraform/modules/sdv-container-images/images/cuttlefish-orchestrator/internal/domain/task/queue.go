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

package task

import (
	"context"
	"time"
)

// Queue is the durable task queue contract used by background workers. The
// Cloud Spanner implementation lives in internal/platform/spanner; it also
// exposes mutation builders (not part of this interface) so producers can fold
// an enqueue into the same transaction that creates the resource. Delivery is
// at-least-once, so Handlers must be idempotent.
type Queue interface {
	// Enqueue persists a new task in its own transaction. Producers that need
	// the enqueue to be atomic with another write use the implementation's
	// mutation builder instead.
	Enqueue(ctx context.Context, t *Task) error

	// ClaimableShards reports the shards holding at least one task eligible to
	// be claimed right now. It is a non-locking hint read, not a claim: a shard
	// it names may be emptied by a peer before Claim reaches it, and one it
	// omits may fill a moment later. Both are harmless, because Claim re-validates
	// everything and stays the authority on what may be taken. Its job is to
	// answer "is there anything at all" without opening a read-write
	// transaction, which is the whole cost of an idle queue.
	ClaimableShards(ctx context.Context) ([]int, error)

	// Claim atomically leases up to limit eligible tasks for the given shard,
	// marking each leased with a visibility timeout of lease. It is safe to call
	// concurrently across instances: every claimed task is held by exactly one
	// caller.
	Claim(ctx context.Context, shard, limit int, lease time.Duration) ([]*Task, error)

	// Complete marks a leased task finished and removes it from the queue.
	// It takes the whole claimed task so implementations can fence the removal
	// on the delivery (holder plus claimed attempts) instead of the instance
	// alone.
	Complete(ctx context.Context, t *Task) error

	// Fail records a failed attempt, rescheduling the task after retryAfter, or
	// dead-lettering it (StateFailed) once it is Exhausted.
	Fail(ctx context.Context, t *Task, cause error, retryAfter time.Duration) error

	// Postpone returns a leased task to pending, eligible again after the
	// delay, without counting the delivery against MaxAttempts. Used when a
	// handler reports a not-yet-met precondition (RescheduleError); the
	// handler bounds total waiting by wall clock, not attempts.
	Postpone(ctx context.Context, t *Task, reason string, after time.Duration) error

	// Sweep reclaims tasks whose lease has expired (crash recovery): back to
	// pending while attempts remain, dead-lettered once exhausted. It reports
	// how many rows it handled.
	Sweep(ctx context.Context) (int, error)
}

// QueueStats is a point-in-time snapshot of queue depth, for observability.
type QueueStats struct {
	Pending          int64         // claimable + delayed tasks awaiting a worker
	Leased           int64         // tasks currently held by a worker
	Failed           int64         // dead-lettered tasks
	OldestPendingAge time.Duration // age of the oldest pending task (0 if none)
	PendingByShard   map[int]int64 // pending depth per shard (hotspot canary)
}

// TypeStateCount is one cell of the queue's task-type × state breakdown, for
// observability.
type TypeStateCount struct {
	Type  string
	State State
	Count int64
}

// Handler processes a single task type. Handlers are registered with the worker
// pool and dispatched by TaskType.
type Handler interface {
	// TaskType is the Task.Type this handler processes, e.g. "device.create_cvd".
	TaskType() string

	// Handle runs one task. It must be idempotent: the queue delivers
	// at-least-once, so a task may be retried after a failure or redelivered
	// after a worker crash. A nil return means success (the worker completes the
	// task); a non-nil error triggers retry or dead-lettering.
	Handle(ctx context.Context, t *Task) error
}
