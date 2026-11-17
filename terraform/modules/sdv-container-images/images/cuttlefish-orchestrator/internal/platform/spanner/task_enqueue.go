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
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// EnqueueMutation builds the insert for a new task, assigning its ID and shard
// and capturing the caller's trace context so the worker's handling joins the
// originating request's trace. Fold the returned mutation into a caller's
// transaction to enqueue atomically with another write. This is the producer
// boundary that becomes spanner.Send when the native Spanner queue is GA.
func (q *TaskQueue) EnqueueMutation(ctx context.Context, t *task.Task) *spanner.Mutation {
	if t.ID == "" {
		t.ID = uuid.New().String()
	}
	t.Shard = task.ShardFor(t.ID, q.numShards)
	if t.State == "" {
		t.State = task.StatePending
	}
	if t.MaxAttempts <= 0 {
		t.MaxAttempts = task.DefaultMaxAttempts
	}
	if t.AvailableAt.IsZero() {
		t.AvailableAt = timeNow()
	}
	if t.TraceContext == "" {
		carrier := propagation.MapCarrier{}
		otel.GetTextMapPropagator().Inject(ctx, carrier)
		t.TraceContext = carrier.Get("traceparent")
	}
	// Mark the producer span with the task identity: the worker's per-attempt
	// trace links back here, and this event (searchable task.id) points
	// forward. An event rather than attributes, because one request may enqueue
	// many tasks (e.g. batch delete).
	trace.SpanFromContext(ctx).AddEvent("task.enqueued", trace.WithAttributes(
		attribute.String("task.id", t.ID),
		attribute.String("task.type", t.Type),
	))
	now := timeNow()
	return spanner.InsertMap(tasksTable, map[string]any{
		colTaskID:       t.ID,
		colShard:        int64(t.Shard),
		colTaskType:     t.Type,
		colState:        string(t.State),
		colResourceID:   models.ToNullString(t.ResourceID),
		"trace_context": models.ToNullString(t.TraceContext),
		colAvailableAt:  t.AvailableAt,
		colPriority:     t.Priority,
		colAttempts:     int64(t.Attempts),
		"max_attempts":  int64(t.MaxAttempts),
		ColCreatedAt:    now,
		ColUpdatedAt:    now,
	})
}

// Enqueue persists a task in its own transaction.
func (q *TaskQueue) Enqueue(ctx context.Context, t *task.Task) error {
	m := q.EnqueueMutation(ctx, t)
	if _, err := q.client.Apply(ctx, []*spanner.Mutation{m}); err != nil {
		return fmt.Errorf("enqueuing task: %w", err)
	}
	q.Wake()
	return nil
}

// EnqueueResync enqueues reconciler-filed periodic work: a taskType task for
// resourceID at PriorityResync, optionally delayed. Keyed reconcile types go
// through EnqueueReconcile (pending dedup + coalesce); every other type is a
// plain enqueue. This is the single construction point for detector filings, so
// jobs pass (type, id, delay) instead of assembling tasks.
func (q *TaskQueue) EnqueueResync(ctx context.Context, taskType, resourceID string, delay time.Duration) error {
	t := task.New(taskType, resourceID)
	t.Priority = task.PriorityResync
	if delay > 0 {
		t.AvailableAt = timeNow().Add(delay)
	}
	if task.IsKeyedReconcile(taskType) {
		return q.EnqueueReconcile(ctx, t)
	}
	return q.Enqueue(ctx, t)
}

// EnqueueReconcile enqueues a keyed reconcile task with workqueue coalesce
// semantics: at most one PENDING task per (type, resource). An enqueue that
// finds one MERGES into it, taking the max priority (an edge event must raise a
// waiting resync row, never be dropped behind it) and the min available_at (a
// delayed re-add never pushes a ready item back). A
// LEASED sibling does not coalesce: the new pending row is the workqueue
// "dirty" bit, dispatched only after the sibling finishes (the claim's
// sibling skip). Two racing inserts are settled by the unique
// reconcile_key index; the loser re-runs once and merges.
func (q *TaskQueue) EnqueueReconcile(ctx context.Context, t *task.Task) error {
	key := t.Type + "/" + t.ResourceID
	var err error
	for range 2 {
		if err = q.enqueueReconcileOnce(ctx, t, key); err == nil ||
			spanner.ErrCode(err) != codes.AlreadyExists {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("enqueuing reconcile %s: %w", key, err)
	}
	q.Wake()
	return nil
}

// EnsureTransition idempotently enqueues a transition task for (taskType,
// resourceID): a plain insert whose duplicate is rejected by the
// transition_key unique index, with AlreadyExists caught as the idempotent
// no-op. It replaces the non-atomic HasActive->Enqueue check the reconcile
// healers used: there is no TOCTOU window, and a producer racing to enqueue
// the same transition has its duplicate dropped by the index rather than
// committed. It reports whether a new task was inserted (false = a live
// sibling already owns the transition).
//
// A plain insert + AlreadyExists is used over INSERT OR IGNORE because
// INSERT OR IGNORE only suppresses a PRIMARY KEY conflict, not a secondary
// unique index (verified on the emulator), so this is the portable form.
// available_at / created_at / updated_at / state / attempts / max_attempts are
// left to their server-side column DEFAULTs (CURRENT_TIMESTAMP() and
// constants), so no timestamp rides an instance clock and the generated
// transition_key is computed from the defaulted state='pending'. The task
// carries no payload; the handler re-derives from the resource row. Only the
// four transition types are indexed by transition_key, so this must not be
// called for other types (a non-transition insert would never dedup).
func (q *TaskQueue) EnsureTransition(ctx context.Context, taskType, resourceID string, priority int64) (bool, error) {
	id := uuid.New().String()
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	m := spanner.InsertMap(tasksTable, map[string]any{
		colTaskID:       id,
		colShard:        int64(task.ShardFor(id, q.numShards)),
		colTaskType:     taskType,
		colResourceID:   resourceID,
		"trace_context": models.ToNullString(carrier.Get("traceparent")),
		colPriority:     priority,
	})
	if _, err := q.client.Apply(ctx, []*spanner.Mutation{m}); err != nil {
		if spanner.ErrCode(err) == codes.AlreadyExists {
			return false, nil // a live sibling already owns the transition
		}
		return false, fmt.Errorf("ensuring transition task %s/%s: %w", taskType, resourceID, err)
	}
	q.Wake()
	return true, nil
}

func (q *TaskQueue) enqueueReconcileOnce(ctx context.Context, t *task.Task, key string) error {
	_, err := q.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		iter := txn.Query(ctx, spanner.Statement{
			// Emulator-only hint; production ignores it. The IS NOT NULL is what
			// makes the null-filtered index legitimately readable. A bound
			// non-null key is not the justification, only optimizer inference,
			// which has measured gaps and moves with optimizer versions.
			SQL: `@{spanner_emulator.disable_query_null_filtered_index_check=true}
			      SELECT task_id, priority, available_at
			      FROM tasks@{FORCE_INDEX=idx_tasks_reconcile_key}
			      WHERE reconcile_key IS NOT NULL AND reconcile_key = @key`,
			Params: map[string]any{"key": key},
		})
		defer iter.Stop()
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return txn.BufferWrite([]*spanner.Mutation{q.EnqueueMutation(ctx, t)})
		}
		if err != nil {
			return fmt.Errorf("reading pending reconcile row: %w", err)
		}
		var (
			id          string
			priority    int64
			availableAt time.Time
		)
		if err := row.Columns(&id, &priority, &availableAt); err != nil {
			return fmt.Errorf("scanning pending reconcile row: %w", err)
		}
		return txn.BufferWrite([]*spanner.Mutation{spanner.UpdateMap(tasksTable, map[string]any{
			colTaskID:      id,
			colPriority:    max(priority, t.Priority),
			colAvailableAt: minTime(availableAt, t.AvailableAt),
			ColUpdatedAt:   timeNow(),
		})})
	})
	return err
}
