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

package spanner

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

const (
	tasksTable     = "tasks"
	taskStepsTable = "task_steps"

	colTaskID      = "task_id"
	colShard       = "shard"
	colTaskType    = "task_type"
	colState       = "state"
	colPriority    = "priority"
	colLeaseHolder = "lease_holder"
	colLeaseExpiry = "lease_expiry"
	colAttempts    = "attempts"
	colAvailableAt = "available_at"
	colLastError   = "last_error"

	// Parameter names shared by the queue's fenced DML (goconst).
	paramLeased  = "leased"
	paramPending = "pending"
	paramFailed  = "failed"
	paramHolder  = "holder"
	paramClaimed = "claimed"
	paramExpiry  = "expiry"
	paramIDs     = "ids"
	paramLimit   = "limit"
)

// TaskQueue is the Cloud Spanner-backed implementation of task.Queue.
//
// Beyond the domain interface it exposes EnqueueMutation, which returns a
// *spanner.Mutation so producers can fold an enqueue into their own
// transaction, committing the task atomically with the resource it creates.
// The builder carries Spanner types and so cannot live on the pure domain
// interface.
type TaskQueue struct {
	client    *Client
	numShards int
	holder    string // Recorded as lease_holder on claim; identifies this instance.
	logger    *slog.Logger
	waker     *task.Waker
}

var _ task.Queue = (*TaskQueue)(nil)

// TaskQueueOption configures NewTaskQueue.
type TaskQueueOption func(*TaskQueue)

// WithShards sets the claim-index fan-out (see task.ShardFor); values below 1
// fall back to task.DefaultShards.
func WithShards(n int) TaskQueueOption {
	return func(q *TaskQueue) { q.numShards = n }
}

// WithHolder sets the instance identity recorded as lease_holder on claim. It
// is half of the delivery fence, so production instances must each pass a
// unique value.
func WithHolder(holder string) TaskQueueOption {
	return func(q *TaskQueue) { q.holder = holder }
}

// WithLogger sets the queue's logger; nil falls back to slog.Default.
func WithLogger(logger *slog.Logger) TaskQueueOption {
	return func(q *TaskQueue) { q.logger = logger }
}

// WithWaker gives the queue the in-process signal an idle worker pool parks
// on, so an enqueue that commits here is picked up in microseconds instead of
// at the next poll. Without it every enqueue is discovered by polling alone,
// which is the guarantee either way.
func WithWaker(w *task.Waker) TaskQueueOption {
	return func(q *TaskQueue) { q.waker = w }
}

// NewTaskQueue creates a Spanner task queue with task.DefaultShards, an empty
// holder, and slog.Default, overridable via options.
func NewTaskQueue(client *Client, opts ...TaskQueueOption) *TaskQueue {
	q := &TaskQueue{client: client, numShards: task.DefaultShards, logger: slog.Default()}
	for _, opt := range opts {
		opt(q)
	}
	if q.numShards < 1 {
		q.numShards = task.DefaultShards
	}
	if q.logger == nil {
		q.logger = slog.Default()
	}
	return q
}

// Wake tells this process's worker pool that a commit carrying a task insert
// landed. The enqueue paths that own their own transaction call it themselves;
// producers that fold EnqueueMutation into their transaction call it once
// their commit reports success. It is a no-op when no waker was wired, and
// costs one wasted probe when the commit it followed did not actually enqueue.
func (q *TaskQueue) Wake() {
	q.waker.Wake()
}

// minTime returns the earlier of two timestamps.
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// serverNow reads CURRENT_TIMESTAMP() inside txn, the TrueTime-derived clock
// every lease comparison uses. For code that must compare expiries in Go
// (rather than in a DML predicate), this is the authoritative "now"; the
// process clock is not.
func serverNow(ctx context.Context, txn *spanner.ReadWriteTransaction) (time.Time, error) {
	iter := txn.Query(ctx, spanner.Statement{SQL: `SELECT CURRENT_TIMESTAMP()`})
	defer iter.Stop()
	row, err := iter.Next()
	if err != nil {
		return time.Time{}, fmt.Errorf("reading server time: %w", err)
	}
	var now time.Time
	if err := row.Columns(&now); err != nil {
		return time.Time{}, fmt.Errorf("scanning server time: %w", err)
	}
	return now, nil
}

// truncateError renders a failure cause to fit last_error STRING(2000); an
// oversized value would fail the write and strand the task leased.
func truncateError(err error) string {
	if err == nil {
		return ""
	}
	return TruncateRunes(err.Error(), 2000)
}
