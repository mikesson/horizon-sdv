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
	sppb "cloud.google.com/go/spanner/apiv1/spannerpb"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

var lowPriorityRead = spanner.QueryOptions{Priority: sppb.RequestOptions_PRIORITY_LOW}

// Stats returns a point-in-time snapshot of queue depth for metrics. The reads
// are low-priority and bounded-staleness (observability tolerates seconds of
// lag; a stale read never blocks writers and can be served by any replica).
func (q *TaskQueue) Stats(ctx context.Context) (task.QueueStats, error) {
	stats := task.QueueStats{PendingByShard: make(map[int]int64)}

	byState := q.client.StaleSingle().QueryWithOptions(ctx, spanner.Statement{
		SQL: `SELECT state, COUNT(*) AS n FROM ` + tasksTable + ` GROUP BY state`,
	}, lowPriorityRead)
	if err := byState.Do(func(row *spanner.Row) error {
		var state string
		var n int64
		if err := row.Columns(&state, &n); err != nil {
			return err
		}
		switch task.State(state) {
		case task.StatePending:
			stats.Pending = n
		case task.StateLeased:
			stats.Leased = n
		case task.StateFailed:
			stats.Failed = n
		case task.StateDone:
			// Completed tasks are not queue depth; QueueStats does not carry them.
		}
		return nil
	}); err != nil {
		return task.QueueStats{}, fmt.Errorf("counting tasks by state: %w", err)
	}

	now := timeNow()
	var oldest time.Time
	byShard := q.client.StaleSingle().QueryWithOptions(ctx, spanner.Statement{
		SQL: `SELECT shard, COUNT(*) AS n, MIN(available_at) AS oldest FROM ` + tasksTable +
			` WHERE state = @pending GROUP BY shard`,
		Params: map[string]any{paramPending: string(task.StatePending)},
	}, lowPriorityRead)
	if err := byShard.Do(func(row *spanner.Row) error {
		var shard, n int64
		var minAvail spanner.NullTime
		if err := row.Columns(&shard, &n, &minAvail); err != nil {
			return err
		}
		stats.PendingByShard[int(shard)] = n
		if minAvail.Valid && (oldest.IsZero() || minAvail.Time.Before(oldest)) {
			oldest = minAvail.Time
		}
		return nil
	}); err != nil {
		return task.QueueStats{}, fmt.Errorf("counting pending tasks by shard: %w", err)
	}
	if !oldest.IsZero() && oldest.Before(now) {
		stats.OldestPendingAge = now.Sub(oldest)
	}
	return stats, nil
}

// adminTaskColumns is the column list scanAdminTask reads, in order.
const adminTaskColumns = `task_id, shard, task_type, state, resource_id,
	available_at, priority, lease_expiry, lease_holder, attempts, max_attempts,
	last_error, updated_at`

// ListFailed returns dead-lettered tasks, most recently failed first. A
// non-empty resourceID narrows to one resource's tasks; the tasks table is
// small (delete-on-complete) so this is a plain filtered scan with no index
// (idx_tasks_resource was dropped as unused in schema v30).
func (q *TaskQueue) ListFailed(ctx context.Context, limit int, resourceID string) ([]*task.Task, error) {
	if limit <= 0 {
		limit = 100
	}
	sql := `SELECT ` + adminTaskColumns + `
	      FROM ` + tasksTable + `
	      WHERE state = @failed`
	params := map[string]any{paramFailed: string(task.StateFailed), paramLimit: int64(limit)}
	if resourceID != "" {
		sql += ` AND resource_id = @resource_id`
		params[colResourceID] = resourceID
	}
	sql += ` ORDER BY updated_at DESC LIMIT @limit`
	stmt := spanner.Statement{SQL: sql, Params: params}
	var tasks []*task.Task
	// Bounded staleness: the DLQ console tolerates seconds of lag.
	iter := q.client.StaleSingle().QueryWithOptions(ctx, stmt, lowPriorityRead)
	if err := iter.Do(func(row *spanner.Row) error {
		t, err := scanAdminTask(row)
		if err != nil {
			return err
		}
		tasks = append(tasks, t)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("listing failed tasks: %w", err)
	}
	return tasks, nil
}

// ListLive returns pending and leased tasks, the queue's current working
// set, in-flight first (leased sorts before pending), then soonest-eligible
// first. Bounded staleness: the operations view tolerates seconds of lag.
func (q *TaskQueue) ListLive(ctx context.Context, limit int) ([]*task.Task, error) {
	if limit <= 0 {
		limit = 100
	}
	stmt := spanner.Statement{
		SQL: `SELECT ` + adminTaskColumns + `
		      FROM ` + tasksTable + `
		      WHERE state IN UNNEST(@states)
		      ORDER BY state, available_at LIMIT @limit`,
		Params: map[string]any{
			"states":   []string{string(task.StateLeased), string(task.StatePending)},
			paramLimit: int64(limit),
		},
	}
	var tasks []*task.Task
	iter := q.client.StaleSingle().QueryWithOptions(ctx, stmt, lowPriorityRead)
	if err := iter.Do(func(row *spanner.Row) error {
		t, err := scanAdminTask(row)
		if err != nil {
			return err
		}
		tasks = append(tasks, t)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("listing live tasks: %w", err)
	}
	return tasks, nil
}

// CountByTypeState returns queue depth broken down by task type and state.
func (q *TaskQueue) CountByTypeState(ctx context.Context) ([]task.TypeStateCount, error) {
	var counts []task.TypeStateCount
	iter := q.client.StaleSingle().QueryWithOptions(ctx, spanner.Statement{
		SQL: `SELECT task_type, state, COUNT(*) AS n FROM ` + tasksTable + `
		      GROUP BY task_type, state ORDER BY task_type, state`,
	}, lowPriorityRead)
	if err := iter.Do(func(row *spanner.Row) error {
		var taskType, state string
		var n int64
		if err := row.Columns(&taskType, &state, &n); err != nil {
			return err
		}
		counts = append(counts, task.TypeStateCount{Type: taskType, State: task.State(state), Count: n})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("counting tasks by type and state: %w", err)
	}
	return counts, nil
}

// RequeueFailed resets a dead-lettered task to pending for another attempt
// (attempts cleared, step journal dropped, because the fresh journey must not
// resume an operation the dead one started). It is an error if the task is
// missing, not dead-lettered, of a type whose requeue would silently no-op
// because the resource is already terminal (task.RequeueLeadsNowhere), or of a
// type that carries an operation handle and would therefore actuate untracked
// (task.RequeueUnsafe). A keyed
// reconcile dead-letter whose key already has a pending sibling cannot go
// back to pending (the reconcile_key unique index allows one); the retry the
// operator asked for is already scheduled, so the dead-letter is dropped and
// the sibling pulled forward instead.
func (q *TaskQueue) RequeueFailed(ctx context.Context, taskID string) error {
	_, err := q.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		row, err := txn.ReadRow(ctx, tasksTable, spanner.Key{taskID}, []string{colState, colTaskType, colResourceID})
		if err != nil {
			if spanner.ErrCode(err) == codes.NotFound {
				return fmt.Errorf("task %s not found", taskID)
			}
			return err
		}
		var state, taskType string
		var resourceID spanner.NullString
		if err := row.Columns(&state, &taskType, &resourceID); err != nil {
			return err
		}
		if task.State(state) != task.StateFailed {
			return fmt.Errorf("task %s is not dead-lettered (state %q)", taskID, state)
		}
		if task.RequeueLeadsNowhere(taskType) {
			return fmt.Errorf("task %s (%s): %w", taskID, taskType, task.ErrRequeueLeadsNowhere)
		}
		// An operation-backed type is refused for a different reason than the
		// one above, because it would actuate untracked rather than no-op, so it
		// gets its own sentinel and its own operator message.
		if task.RequeueUnsafe(taskType) {
			return fmt.Errorf("task %s (%s): %w", taskID, taskType, task.ErrRequeueUnsafe)
		}
		now := timeNow()
		if task.IsKeyedReconcile(taskType) && resourceID.StringVal != "" {
			sibling, found, err := q.pendingSiblingForKey(ctx, txn, taskType, resourceID.StringVal)
			if err != nil {
				return err
			}
			if found {
				return txn.BufferWrite([]*spanner.Mutation{
					spanner.Delete(tasksTable, spanner.Key{taskID}),
					spanner.UpdateMap(tasksTable, map[string]any{
						colTaskID:      sibling.id,
						colAvailableAt: minTime(sibling.availableAt, now),
						ColUpdatedAt:   now,
					}),
				})
			}
		}
		return txn.BufferWrite([]*spanner.Mutation{
			// Journaled steps from the dead journey must not survive the
			// requeue: a fresh claim would resume polling a long-dead remote
			// operation. (Today's only journaled type is also requeue-blocked,
			// and this keeps that safety structural rather than coincidental.)
			spanner.Delete(taskStepsTable, spanner.Key{taskID}.AsPrefix()),
			spanner.UpdateMap(tasksTable, map[string]any{
				colTaskID:      taskID,
				colState:       string(task.StatePending),
				colAttempts:    int64(0),
				colAvailableAt: now,
				colLastError:   spanner.NullString{},
				colLeaseExpiry: spanner.NullTime{},
				colLeaseHolder: spanner.NullString{},
				ColUpdatedAt:   now,
			}),
		})
	})
	if err != nil {
		return fmt.Errorf("requeueing task %s: %w", taskID, err)
	}
	return nil
}

// HasActive reports whether a live (pending or leased) task of the given type
// exists for a resource. Cross-loop waits use it to distinguish "the work I
// depend on is still retrying" from "nothing will ever make progress".
// Completed tasks are deleted and dead-letters are neither pending nor
// leased, so a false answer means no live attempt remains.
func (q *TaskQueue) HasActive(ctx context.Context, taskType, resourceID string) (bool, error) {
	stmt := spanner.Statement{
		SQL: `SELECT 1 FROM tasks
		      WHERE task_type = @type AND resource_id = @resource
		        AND state IN UNNEST(@states) LIMIT 1`,
		Params: map[string]any{
			"type":     taskType,
			"resource": resourceID,
			"states":   []string{string(task.StatePending), string(task.StateLeased)},
		},
	}
	iter := q.client.Single().QueryWithOptions(ctx, stmt, lowPriorityRead)
	defer iter.Stop()
	if _, err := iter.Next(); err != nil {
		if errors.Is(err, iterator.Done) {
			return false, nil
		}
		return false, fmt.Errorf("checking active %s task for %s: %w", taskType, resourceID, err)
	}
	return true, nil
}

// DeleteTask removes a task regardless of state (admin force-delete).
func (q *TaskQueue) DeleteTask(ctx context.Context, taskID string) error {
	if _, err := q.client.Apply(ctx, []*spanner.Mutation{spanner.Delete(tasksTable, spanner.Key{taskID})}); err != nil {
		return fmt.Errorf("deleting task %s: %w", taskID, err)
	}
	return nil
}

// scanAdminTask reads adminTaskColumns, in order.
func scanAdminTask(row *spanner.Row) (*task.Task, error) {
	var (
		id, taskType, state                    string
		shard, priority, attempts, maxAttempts int64
		resourceID, leaseHolder, lastError     spanner.NullString
		leaseExpiry                            spanner.NullTime
		availableAt, updatedAt                 time.Time
	)
	if err := row.Columns(&id, &shard, &taskType, &state, &resourceID,
		&availableAt, &priority, &leaseExpiry, &leaseHolder,
		&attempts, &maxAttempts, &lastError, &updatedAt); err != nil {
		return nil, fmt.Errorf("scanning task row: %w", err)
	}
	return &task.Task{
		ID:          id,
		Shard:       int(shard),
		Type:        taskType,
		State:       task.State(state),
		ResourceID:  resourceID.StringVal,
		AvailableAt: availableAt,
		Priority:    priority,
		LeaseExpiry: leaseExpiry.Time,
		LeaseHolder: leaseHolder.StringVal,
		Attempts:    int(attempts),
		MaxAttempts: int(maxAttempts),
		LastError:   lastError.StringVal,
		UpdatedAt:   updatedAt,
	}, nil
}
