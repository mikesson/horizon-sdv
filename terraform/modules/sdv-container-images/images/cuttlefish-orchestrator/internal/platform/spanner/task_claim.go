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
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// claimOverscan is how many candidates beyond the requested batch the claim's
// SECOND look locks and inspects. With exactly `limit` candidates, one
// ineligible keyed head (a pending reconcile whose sibling is leased, which
// filterEligible drops) consumes the whole window, and every later poll of
// that shard re-selects the same head: nothing at equal or lower priority
// behind it can be claimed until the sibling's handler returns, up to
// HandleTimeout. The wider look runs ONLY when the first, limit-sized look
// was entirely filtered out. Widening the FOR UPDATE window on every claim
// collapsed claim throughput under load (the queueload harness caught it),
// so the hot path keeps the minimal lock footprint and only the starvation
// case pays for the extra locked rows.
const claimOverscan = 8

// Claim leases up to limit eligible tasks for one shard. The single read-write
// transaction reads candidates FOR UPDATE and stamps the leases with one DML
// statement; Spanner's serializable isolation guarantees no two callers lease
// the same row. The callback is side-effect free so it is safe to re-run on
// ABORTED.
//
// Why the query is shaped this way:
//   - FORCE_INDEX: the index is non-covering (trace_context, attempts, etc.
//     live only in the base table) and Spanner's optimizer picks a
//     non-covering index only when directed. Without it every claim
//     full-scans tasks under read locks, serializing all shards against each
//     other.
//   - FOR UPDATE: takes exclusive locks at read time, so two claimers of the
//     same shard queue briefly instead of both claiming and aborting on the
//     shared→exclusive upgrade at commit.
//   - Default priority: deprioritizing a lock-holding read only extends the
//     time the head of the shard stays locked.
func (q *TaskQueue) Claim(ctx context.Context, shard, limit int, lease time.Duration) ([]*task.Task, error) {
	var claimed []*task.Task
	_, err := q.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		claimed = nil // Reset: the transaction body may run more than once.
		candidates, err := q.selectCandidates(ctx, txn, shard, limit)
		if err != nil {
			return err
		}
		eligible, ids, err := q.filterEligible(ctx, txn, candidates)
		if err != nil {
			return err
		}
		if len(ids) == 0 && len(candidates) > 0 {
			// Every candidate was an ineligible keyed head. Without a wider
			// look the same heads come back every poll and the work behind
			// them starves for the sibling's whole handler runtime. Only this
			// rare case pays for the wider FOR UPDATE window.
			candidates, err = q.selectCandidates(ctx, txn, shard, limit+claimOverscan)
			if err != nil {
				return err
			}
			eligible, ids, err = q.filterEligible(ctx, txn, candidates)
			if err != nil {
				return err
			}
		}
		if len(ids) == 0 {
			return nil
		}
		// The wider look steps past ineligible heads, never widens the batch:
		// stamp at most limit, keeping candidate order (priority, age).
		if len(ids) > limit {
			ids = ids[:limit]
		}

		stamps, err := q.stampLeases(ctx, txn, ids, lease)
		if err != nil {
			return err
		}
		// Rebuild in candidate order (priority DESC, then age), because THEN
		// RETURN row order is unspecified.
		for _, id := range ids {
			s, ok := stamps[id]
			if !ok {
				continue // rows are locked FOR UPDATE, so unreachable
			}
			t := eligible[id]
			t.State = task.StateLeased
			t.LeaseHolder = q.holder
			t.Attempts = int(s.attempts)
			t.LeaseExpiry = s.leaseExpiry
			claimed = append(claimed, t)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("claiming tasks: %w", err)
	}
	return claimed, nil
}

// selectCandidates reads up to limit claimable rows of one shard FOR UPDATE,
// in claim order.
func (q *TaskQueue) selectCandidates(ctx context.Context, txn *spanner.ReadWriteTransaction, shard, limit int) ([]*task.Task, error) {
	stmt := spanner.Statement{
		SQL: `SELECT task_id, shard, task_type, state, resource_id,
		             trace_context, available_at, priority, attempts, max_attempts, created_at
		      FROM tasks@{FORCE_INDEX=idx_tasks_claimable_v2}
		      WHERE shard = @shard AND state = @pending AND available_at <= CURRENT_TIMESTAMP()
		      ORDER BY priority DESC, available_at
		      LIMIT @limit
		      FOR UPDATE`,
		Params: map[string]any{
			colShard:     int64(shard),
			paramPending: string(task.StatePending),
			paramLimit:   int64(limit),
		},
	}
	iter := txn.Query(ctx, stmt)
	defer iter.Stop()

	var candidates []*task.Task
	err := iter.Do(func(row *spanner.Row) error {
		t, err := scanClaimableTask(row)
		if err != nil {
			return err
		}
		candidates = append(candidates, t)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

// filterEligible applies per-key serialization to the claim's candidates, for
// KEYED RECONCILE tasks only (the workqueue locked-set rule). A key whose
// sibling is leased, or was already leased earlier in this batch, is left
// pending for a later poll, so one resource never reconciles concurrently.
// It is NOT a correlated subquery in the claim itself: that evaluates
// against every candidate under the claim's exclusive locks and serializes
// all claimers; targeted point lookups cost nothing for the non-reconcile
// types that dominate traffic. Returns the claimable tasks keyed by ID plus
// their ids in candidate order.
func (q *TaskQueue) filterEligible(ctx context.Context, txn *spanner.ReadWriteTransaction, candidates []*task.Task) (map[string]*task.Task, []string, error) {
	batchKeys := make(map[string]bool)
	eligible := make(map[string]*task.Task, len(candidates))
	var ids []string
	for _, t := range candidates {
		if task.IsKeyedReconcile(t.Type) {
			key := t.Type + "/" + t.ResourceID
			if batchKeys[key] {
				continue
			}
			busy, err := q.leasedSiblingExists(ctx, txn, t.Type, t.ResourceID)
			if err != nil {
				return nil, nil, err
			}
			if busy {
				continue
			}
			batchKeys[key] = true
		}
		eligible[t.ID] = t
		ids = append(ids, t.ID)
	}
	return eligible, ids, nil
}

// leaseStamp is the server-assigned delivery identity a claim's stamping DML
// hands back for one row.
type leaseStamp struct {
	attempts    int64
	leaseExpiry time.Time
}

// stampLeases marks the rows leased with one DML statement, giving every
// claimed row a SERVER-assigned lease: CURRENT_TIMESTAMP() is
// TrueTime-derived, and THEN RETURN hands the stored values back, so the
// delivery fence, and the GCE requestId derived from it, keys on a
// timestamp no instance clock produced. DML rather than mutations because
// mutations cannot call CURRENT_TIMESTAMP(). In-txn DML is read-visible
// (buffered mutations were not), but the sibling probes in filterEligible ran
// before this write, so its batch-key dedup still catches same-batch keyed
// duplicates.
func (q *TaskQueue) stampLeases(ctx context.Context, txn *spanner.ReadWriteTransaction, ids []string, lease time.Duration) (map[string]leaseStamp, error) {
	stamps := make(map[string]leaseStamp, len(ids))
	riter := txn.Query(ctx, spanner.Statement{
		SQL: `UPDATE tasks
		      SET state = @leased, lease_holder = @holder,
		          attempts = attempts + 1,
		          lease_expiry = TIMESTAMP_ADD(CURRENT_TIMESTAMP(), INTERVAL @lease_ms MILLISECOND),
		          updated_at = CURRENT_TIMESTAMP()
		      WHERE task_id IN UNNEST(@ids) AND state = @pending
		      THEN RETURN task_id, attempts, lease_expiry`,
		Params: map[string]any{
			paramLeased:  string(task.StateLeased),
			paramHolder:  q.holder,
			"lease_ms":   lease.Milliseconds(),
			paramIDs:     ids,
			paramPending: string(task.StatePending),
		},
	})
	if err := riter.Do(func(row *spanner.Row) error {
		var (
			id string
			s  leaseStamp
		)
		if err := row.Columns(&id, &s.attempts, &s.leaseExpiry); err != nil {
			return fmt.Errorf("scanning claimed lease: %w", err)
		}
		stamps[id] = s
		return nil
	}); err != nil {
		return nil, fmt.Errorf("stamping claimed leases: %w", err)
	}
	return stamps, nil
}

// Complete removes a finished task. The delete is fenced on the delivery
// (lease_holder, the attempts value written at claim, and the lease expiry
// stamped at claim) so a worker whose lease expired, and whose task was swept
// and re-claimed, cannot delete the new claimant's row. The holder alone is
// not enough: it identifies the instance, and after a skewed sweep the same
// instance can re-claim its own task, leaving the zombie's bookkeeping
// holder-matched. Attempts alone is not enough either: Postpone restores the
// pre-claim value, so postpone-separated deliveries repeat (holder, attempts)
// pairs. The lease expiry is unique per claim and is what finally names the
// delivery. A fenced-out ack is success (the redelivery finishes and acks
// instead; handlers are idempotent), and it is logged, because a fenced-out
// write is also the only trace left by an ownership handoff (and by a retry
// of an ambiguously-committed write).
func (q *TaskQueue) Complete(ctx context.Context, t *task.Task) error {
	var rows int64
	_, err := q.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		var err error
		rows, err = txn.Update(ctx, spanner.Statement{
			SQL: `DELETE FROM tasks
			      WHERE task_id = @id AND lease_holder = @holder AND attempts = @attempts
			        AND lease_expiry = @expiry`,
			Params: map[string]any{
				"id": t.ID, paramHolder: q.holder,
				"attempts": int64(t.Attempts), paramExpiry: t.LeaseExpiry,
			},
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("completing task %s: %w", t.ID, err)
	}
	if rows == 0 {
		q.logger.InfoContext(ctx, "task completion fenced out; row deleted or owned elsewhere",
			slog.String("task_id", t.ID), slog.String(paramHolder, q.holder))
	}
	return nil
}

// releaseToPending returns one leased delivery to pending, with attempts
// written back to returnAttempts, claimable again after the delay, and the note
// recorded in last_error. It is fenced on the delivery (holder + claimed
// attempts + lease expiry) like every bookkeeping write, and reports the rows
// written (0 = another claimant owns the outcome).
//
// For keyed reconcile types whose key already has a PENDING sibling, the
// returning row is DELETED and its retry history merged into the sibling
// instead: setting it pending would regenerate reconcile_key and collide with
// the sibling on the unique index, stranding the row leased forever and
// poisoning every sweep batch that touched it, while the sibling stayed
// unclaimable behind the stuck lease.
func (q *TaskQueue) releaseToPending(ctx context.Context, t *task.Task, note string, returnAttempts int, after time.Duration) (int64, error) {
	var rows int64
	_, err := q.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		rows = 0 // Reset: the transaction body may run more than once.
		if task.IsKeyedReconcile(t.Type) && t.ResourceID != "" {
			sibling, found, err := q.pendingSiblingForKey(ctx, txn, t.Type, t.ResourceID)
			if err != nil {
				return err
			}
			if found {
				rows, err = txn.Update(ctx, spanner.Statement{
					SQL: `DELETE FROM tasks
					      WHERE task_id = @id AND lease_holder = @holder AND state = @leased
					        AND attempts = @claimed AND lease_expiry = @expiry`,
					Params: map[string]any{
						"id": t.ID, paramHolder: q.holder,
						paramLeased:  string(task.StateLeased),
						paramClaimed: int64(t.Attempts),
						paramExpiry:  t.LeaseExpiry,
					},
				})
				if err != nil || rows == 0 {
					return err
				}
				// Merge as DML so the availability floor is server-side
				// (CURRENT_TIMESTAMP()) like every other requeue schedule, so
				// the last app-clock value in this path is gone. GREATEST/LEAST
				// reproduce mergeSiblingMutation's max-attempts/max-priority/
				// min-available_at (all sibling columns are NOT NULL).
				_, err = txn.Update(ctx, spanner.Statement{
					SQL: `UPDATE tasks
					      SET attempts = GREATEST(attempts, @attempts),
					          priority = GREATEST(priority, @priority),
					          available_at = LEAST(available_at,
					              TIMESTAMP_ADD(CURRENT_TIMESTAMP(), INTERVAL @after_ms MILLISECOND)),
					          updated_at = CURRENT_TIMESTAMP()
					      WHERE task_id = @sibling`,
					Params: map[string]any{
						"sibling":   sibling.id,
						"attempts":  int64(returnAttempts),
						colPriority: t.Priority,
						"after_ms":  after.Milliseconds(),
					},
				})
				return err
			}
		}
		var err error
		rows, err = txn.Update(ctx, spanner.Statement{
			SQL: `UPDATE tasks
			      SET state = @pending, attempts = @return, last_error = @note,
			          available_at = TIMESTAMP_ADD(CURRENT_TIMESTAMP(), INTERVAL @after_ms MILLISECOND),
			          lease_expiry = NULL, lease_holder = NULL, updated_at = CURRENT_TIMESTAMP()
			      WHERE task_id = @id AND lease_holder = @holder AND state = @leased
			        AND attempts = @claimed AND lease_expiry = @expiry`,
			Params: map[string]any{
				"id": t.ID, paramHolder: q.holder,
				paramLeased:  string(task.StateLeased),
				paramPending: string(task.StatePending),
				"return":     int64(returnAttempts),
				paramClaimed: int64(t.Attempts),
				paramExpiry:  t.LeaseExpiry,
				"note":       models.ToNullString(note),
				"after_ms":   after.Milliseconds(),
			},
		})
		return err
	})
	return rows, err
}

// Fail records a failed attempt: it reschedules the task after retryAfter
// (merging into a pending keyed sibling when one exists, see
// releaseToPending), or dead-letters it (StateFailed) once it is Exhausted.
// The lease is cleared either way so the sweeper ignores the row. Every write
// is fenced on the delivery (lease_holder + state + claimed attempts + lease
// expiry) so a stale worker cannot clobber a lease the sweeper has already
// reassigned, even when the re-claimant is the same instance. The dead-letter
// keeps the true attempts count: a forced dead-letter (an unregistered task
// type collapses MaxAttempts to the attempts already used) reads accurately in
// the DLQ, with last_error naming the cause.
func (q *TaskQueue) Fail(ctx context.Context, t *task.Task, cause error, retryAfter time.Duration) error {
	if t.Exhausted() {
		return q.deadLetter(ctx, t, cause)
	}
	rows, err := q.releaseToPending(ctx, t, truncateError(cause), t.Attempts, retryAfter)
	if err != nil {
		return fmt.Errorf("failing task %s: %w", t.ID, err)
	}
	if rows == 0 {
		// The new claimant owns the outcome, so this is not an error, but it
		// IS the only trace of a lease handoff (live-fire: a fenced-out Fail
		// left a task silently leased ~25 min until the sweeper reclaimed it).
		q.logger.WarnContext(ctx, "task failure record fenced out; outcome owned by another claimant",
			slog.String("task_id", t.ID),
			slog.String(paramHolder, q.holder),
			slog.Int("attempts", t.Attempts))
	}
	return nil
}

// deadLetter moves an exhausted delivery to StateFailed, fenced like every
// bookkeeping write. Keyed types never collide here: reconcile_key is NULL on
// failed rows, so a pending sibling may coexist with the dead-letter.
func (q *TaskQueue) deadLetter(ctx context.Context, t *task.Task, cause error) error {
	var rows int64
	_, err := q.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		var err error
		rows, err = txn.Update(ctx, spanner.Statement{
			SQL: `UPDATE tasks
			      SET state = @failed, last_error = @cause,
			          lease_expiry = NULL, lease_holder = NULL,
			          updated_at = CURRENT_TIMESTAMP()
			      WHERE task_id = @id AND lease_holder = @holder AND state = @leased
			        AND attempts = @claimed AND lease_expiry = @expiry`,
			Params: map[string]any{
				"id": t.ID, paramHolder: q.holder,
				paramLeased:  string(task.StateLeased),
				paramFailed:  string(task.StateFailed),
				paramClaimed: int64(t.Attempts),
				paramExpiry:  t.LeaseExpiry,
				"cause":      models.ToNullString(truncateError(cause)),
			},
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("dead-lettering task %s: %w", t.ID, err)
	}
	if rows == 0 {
		q.logger.WarnContext(ctx, "task dead-letter fenced out; outcome owned by another claimant",
			slog.String("task_id", t.ID),
			slog.String(paramHolder, q.holder),
			slog.Int("attempts", t.Attempts))
	}
	return nil
}

// Postpone returns a leased task to pending without counting the delivery:
// attempts is written back to its pre-claim value (Claim increments it), so a
// waiting task cannot dead-letter from waiting alone. Fenced on the delivery
// (lease_holder + state + claimed attempts + lease expiry) like Fail; the
// reason lands in last_error for operator visibility. A keyed task with a
// pending sibling merges into it instead (releaseToPending).
func (q *TaskQueue) Postpone(ctx context.Context, t *task.Task, reason string, after time.Duration) error {
	rows, err := q.releaseToPending(ctx, t, reason, max(t.Attempts-1, 0), after)
	if err != nil {
		return fmt.Errorf("postponing task %s: %w", t.ID, err)
	}
	if rows == 0 {
		q.logger.WarnContext(ctx, "task postpone fenced out; outcome owned by another claimant",
			slog.String("task_id", t.ID),
			slog.String(paramHolder, q.holder))
	}
	return nil
}

// scanClaimableTask reads the columns selected by Claim, in order.
func scanClaimableTask(row *spanner.Row) (*task.Task, error) {
	var (
		id, taskType, state                    string
		shard, priority, attempts, maxAttempts int64
		resourceID, traceContext               spanner.NullString
		availableAt, createdAt                 time.Time
	)
	if err := row.Columns(&id, &shard, &taskType, &state, &resourceID,
		&traceContext, &availableAt, &priority, &attempts, &maxAttempts, &createdAt); err != nil {
		return nil, fmt.Errorf("scanning task row: %w", err)
	}
	return &task.Task{
		ID:           id,
		Shard:        int(shard),
		Type:         taskType,
		State:        task.State(state),
		ResourceID:   resourceID.StringVal,
		TraceContext: traceContext.StringVal,
		AvailableAt:  availableAt,
		Priority:     priority,
		Attempts:     int(attempts),
		MaxAttempts:  int(maxAttempts),
		CreatedAt:    createdAt,
	}, nil
}
