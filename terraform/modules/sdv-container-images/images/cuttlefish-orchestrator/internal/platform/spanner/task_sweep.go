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
	"slices"

	"cloud.google.com/go/spanner"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// dlqReasonLeaseExpired records why the sweeper dead-lettered a task whose
// lease expired with no attempts left.
const dlqReasonLeaseExpired = "lease expired after exhausting max_attempts (worker crash?)"

// sweepBatchSize bounds one reclaim commit far below Spanner's 80,000-mutation
// limit (each swept row touches ~6 cells plus its index entries).
const sweepBatchSize = 500

// Sweep reclaims tasks whose lease has expired: back to pending while attempts
// remain, dead-lettered (StateFailed) once they are exhausted, so a task that
// keeps killing its worker cannot be redelivered forever. It reports the total
// rows handled. Leased work from a crashed instance becomes claimable again
// once its lease lapses; this is what makes the queue durable across instance
// death.
//
// Candidates come from a lock-free single-use read over the null-filtered
// lease-expiry index; reading inside the write transaction would hold shared
// locks on every row scanned, stalling all claims and enqueues for the scan.
// Each write batch re-verifies the expired lease server-side, so a row a
// worker re-claimed or finished after the read is left untouched.
//
// The sweeper is the queue's crash recovery, so a bad row it recovers must not
// stop it. Keyed reconcile candidates are handled per row (their requeue
// can require a merge into a pending sibling, see releaseToPending), and a
// failed batch degrades to per-row processing so one bad row can never block
// the reclamation of every other expired lease.
func (q *TaskQueue) Sweep(ctx context.Context) (int, error) {
	// The emulator hint is ignored by production Spanner; the IS NOT NULL
	// predicate is what makes the null-filtered index valid for this query.
	// The scan is advisory. Bounded staleness (served by the nearest
	// replica, never blocking writers) is safe because every write batch
	// re-verifies the expired lease server-side; a lease that expired within
	// the staleness bound is simply reclaimed one sweep later.
	var plain []string
	var keyed []sweepCandidate
	iter := q.client.StaleSingle().QueryWithOptions(ctx, spanner.Statement{
		SQL: `@{spanner_emulator.disable_query_null_filtered_index_check=true}
		      SELECT task_id, task_type, resource_id
		      FROM tasks@{FORCE_INDEX=idx_tasks_lease_expiry}
		      WHERE lease_expiry IS NOT NULL AND lease_expiry < CURRENT_TIMESTAMP()`,
	}, lowPriorityRead)
	if err := iter.Do(func(row *spanner.Row) error {
		var (
			id, taskType string
			resourceID   spanner.NullString
		)
		if err := row.Columns(&id, &taskType, &resourceID); err != nil {
			return fmt.Errorf("scanning swept task id: %w", err)
		}
		if task.IsKeyedReconcile(taskType) && resourceID.StringVal != "" {
			keyed = append(keyed, sweepCandidate{id: id, taskType: taskType, resourceID: resourceID.StringVal})
		} else {
			plain = append(plain, id)
		}
		return nil
	}); err != nil {
		return 0, fmt.Errorf("listing expired leases: %w", err)
	}

	reclaimed := 0
	for batch := range slices.Chunk(plain, sweepBatchSize) {
		n, err := q.sweepBatch(ctx, batch)
		reclaimed += n
		if err == nil {
			continue
		}
		q.logger.ErrorContext(ctx, "sweep batch failed; degrading to per-row recovery",
			slog.Int("batch", len(batch)), slog.Any("error", err))
		for _, id := range batch {
			n, rerr := q.sweepBatch(ctx, []string{id})
			reclaimed += n
			if rerr != nil {
				q.logger.ErrorContext(ctx, "sweeping expired lease failed; row left for the next sweep",
					slog.String("task_id", id), slog.Any("error", rerr))
			}
		}
	}
	for _, c := range keyed {
		n, err := q.sweepKeyedExpired(ctx, c)
		reclaimed += n
		if err != nil {
			q.logger.ErrorContext(ctx, "sweeping expired keyed lease failed; row left for the next sweep",
				slog.String("task_id", c.id), slog.Any("error", err))
		}
	}
	return reclaimed, nil
}

// sweepCandidate is one expired-lease row of a keyed reconcile type, swept
// per row because its requeue may need the sibling merge.
type sweepCandidate struct {
	id, taskType, resourceID string
}

// sweepKeyedExpired reclaims one keyed reconcile row: dead-letter when
// exhausted, merge-and-delete when a pending sibling exists, plain requeue
// otherwise. The row is re-read inside the transaction, so the expired-lease
// predicate holds at commit time (a re-claimed or finished row is skipped).
func (q *TaskQueue) sweepKeyedExpired(ctx context.Context, c sweepCandidate) (int, error) {
	var handled int64
	_, err := q.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		handled = 0 // Reset: the transaction body may run more than once.
		// The expiry verdict must be on server time like every other lease
		// comparison; the merge decision below needs the row's values in Go,
		// so this path fetches the clock rather than folding the predicate
		// into DML.
		now, err := serverNow(ctx, txn)
		if err != nil {
			return err
		}
		row, err := txn.ReadRow(ctx, tasksTable, spanner.Key{c.id},
			[]string{colState, colLeaseExpiry, colAttempts, "max_attempts", colPriority})
		if spanner.ErrCode(err) == codes.NotFound {
			return nil // completed (or merged away) since the scan
		}
		if err != nil {
			return fmt.Errorf("reading expired keyed task: %w", err)
		}
		var (
			state                 string
			leaseExpiry           spanner.NullTime
			attempts, maxAttempts int64
			priority              int64
		)
		if err := row.Columns(&state, &leaseExpiry, &attempts, &maxAttempts, &priority); err != nil {
			return fmt.Errorf("scanning expired keyed task: %w", err)
		}
		if task.State(state) != task.StateLeased || !leaseExpiry.Valid || !leaseExpiry.Time.Before(now) {
			return nil // re-claimed or already resolved; not ours
		}
		if attempts >= maxAttempts {
			handled = 1
			return txn.BufferWrite([]*spanner.Mutation{spanner.UpdateMap(tasksTable, map[string]any{
				colTaskID:      c.id,
				colState:       string(task.StateFailed),
				colLeaseExpiry: spanner.NullTime{},
				colLeaseHolder: spanner.NullString{},
				colLastError:   dlqReasonLeaseExpired,
				ColUpdatedAt:   now,
			})})
		}
		sibling, found, err := q.pendingSiblingForKey(ctx, txn, c.taskType, c.resourceID)
		if err != nil {
			return err
		}
		handled = 1
		if found {
			return txn.BufferWrite([]*spanner.Mutation{
				spanner.Delete(tasksTable, spanner.Key{c.id}),
				mergeSiblingMutation(sibling, attempts, priority, now),
			})
		}
		return txn.BufferWrite([]*spanner.Mutation{spanner.UpdateMap(tasksTable, map[string]any{
			colTaskID:      c.id,
			colState:       string(task.StatePending),
			colLeaseExpiry: spanner.NullTime{},
			colLeaseHolder: spanner.NullString{},
			ColUpdatedAt:   now,
		})})
	})
	return int(handled), err
}

// sweepBatch requeues or dead-letters one bounded batch of expired-lease
// candidates in a single transaction of two conditional DML statements.
func (q *TaskQueue) sweepBatch(ctx context.Context, ids []string) (int, error) {
	var handled int64
	_, err := q.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		handled = 0
		requeue := spanner.Statement{
			SQL: `UPDATE tasks
			      SET state = @pending, lease_expiry = NULL, lease_holder = NULL,
			          updated_at = CURRENT_TIMESTAMP()
			      WHERE task_id IN UNNEST(@ids)
			        AND state = @leased AND lease_expiry < CURRENT_TIMESTAMP()
			        AND attempts < max_attempts`,
			Params: map[string]any{
				paramIDs:     ids,
				paramLeased:  string(task.StateLeased),
				paramPending: string(task.StatePending),
			},
		}
		deadLetter := spanner.Statement{
			SQL: `UPDATE tasks
			      SET state = @failed, lease_expiry = NULL, lease_holder = NULL,
			          last_error = @cause, updated_at = CURRENT_TIMESTAMP()
			      WHERE task_id IN UNNEST(@ids)
			        AND state = @leased AND lease_expiry < CURRENT_TIMESTAMP()
			        AND attempts >= max_attempts`,
			Params: map[string]any{
				paramIDs:    ids,
				paramLeased: string(task.StateLeased),
				paramFailed: string(task.StateFailed),
				"cause":     dlqReasonLeaseExpired,
			},
		}
		counts, err := txn.BatchUpdate(ctx, []spanner.Statement{requeue, deadLetter})
		if err != nil {
			return err
		}
		for _, c := range counts {
			handled += c
		}
		return nil
	})
	return int(handled), err
}
