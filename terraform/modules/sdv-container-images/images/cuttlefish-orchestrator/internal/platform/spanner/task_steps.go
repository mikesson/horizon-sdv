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

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// The TaskQueue is also the task.OperationJournal: step rows live interleaved
// in tasks (schema v36), so they share the queue's client and die with their
// task row on Complete.
var _ task.OperationJournal = (*TaskQueue)(nil)

// errStaleJournalDelivery aborts a journal write whose delivery no longer
// holds the task, because the lease lapsed and the task was swept (and possibly
// re-claimed). Surfaced wrapped; callers treat any journal-write error as
// "the record did not land", which for a zombie is exactly right.
var errStaleJournalDelivery = errors.New(
	"delivery no longer holds the task; journal write fenced")

// RecordedOperation returns the operation name recorded for a step, or ""
// when none is recorded. The newest record for the step name wins. The read
// is not fenced on the delivery: a redelivery resumes by reading the record
// a PRIOR delivery wrote.
func (q *TaskQueue) RecordedOperation(ctx context.Context, t *task.Task, step string) (string, error) {
	stmt := spanner.Statement{
		SQL: `SELECT output FROM task_steps
		      WHERE task_id = @task_id AND step_name = @step
		      ORDER BY step_id DESC LIMIT 1`,
		Params: map[string]any{colTaskID: t.ID, "step": step},
	}
	iter := q.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading step %q of task %s: %w", step, t.ID, err)
	}
	var output spanner.NullJSON
	if err := row.Columns(&output); err != nil {
		return "", fmt.Errorf("scanning step %q of task %s: %w", step, t.ID, err)
	}
	op, _ := output.Value.(string)
	return op, nil
}

// RecordOperation records the operation name a step started. Written AFTER
// the remote accepted the operation (at-least-once). The write is fenced on
// the delivery (see deliveryHolds): a zombie attempt whose task was swept and
// re-claimed is refused, so its late record cannot newest-win over the live
// delivery's journal and send the live delivery's next read to the wrong
// operation.
//
// The journal is NAME-KEYED, not positional: a re-record for the same
// step_name appends a higher step_id and RecordedOperation returns the newest.
// It therefore assumes exactly one resumable operation per step_name and has
// no positional-determinism guard (unlike DBOS's UnexpectedStep). That is fine
// for today's single journaled step (cvd_load); adding a second journaled step
// to any task type requires revisiting this assumption.
func (q *TaskQueue) RecordOperation(ctx context.Context, t *task.Task, step, operation string) error {
	_, err := q.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		if err := q.fenceDelivery(ctx, txn, t); err != nil {
			return err
		}
		iter := txn.Query(ctx, spanner.Statement{
			SQL:    `SELECT COALESCE(MAX(step_id), -1) FROM task_steps WHERE task_id = @task_id`,
			Params: map[string]any{colTaskID: t.ID},
		})
		defer iter.Stop()
		row, err := iter.Next()
		if err != nil {
			return fmt.Errorf("reading step cursor: %w", err)
		}
		var maxID int64
		if err := row.Columns(&maxID); err != nil {
			return fmt.Errorf("scanning step cursor: %w", err)
		}
		return txn.BufferWrite([]*spanner.Mutation{
			spanner.InsertOrUpdate(taskStepsTable,
				[]string{colTaskID, "step_id", "step_name", "output"},
				[]any{
					t.ID, maxID + 1, step,
					spanner.NullJSON{Value: operation, Valid: true},
				}),
		})
	})
	if err != nil {
		return fmt.Errorf("recording step %q of task %s: %w", step, t.ID, err)
	}
	return nil
}

// ClearOperations drops every recorded step for a task, restarting its
// journey, used when a resumed operation turns out terminally failed. Fenced
// on the delivery: a zombie's clear must not wipe records the live delivery
// wrote.
func (q *TaskQueue) ClearOperations(ctx context.Context, t *task.Task) error {
	_, err := q.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		if err := q.fenceDelivery(ctx, txn, t); err != nil {
			return err
		}
		return txn.BufferWrite([]*spanner.Mutation{
			spanner.Delete(taskStepsTable, spanner.Key{t.ID}.AsPrefix()),
		})
	})
	if err != nil {
		return fmt.Errorf("clearing steps of task %s: %w", t.ID, err)
	}
	return nil
}

// fenceDelivery returns errStaleJournalDelivery unless the caller's delivery
// (this queue's holder, the attempts value written at claim, and the lease
// expiry stamped at claim) still holds the leased task row. Same triple as
// the bookkeeping fence (see Complete for why holder alone is not enough),
// read inside the caller's transaction so the fence and the buffered write
// commit together, or fail together.
func (q *TaskQueue) fenceDelivery(ctx context.Context, txn *spanner.ReadWriteTransaction, t *task.Task) error {
	iter := txn.Query(ctx, spanner.Statement{
		SQL: `SELECT 1 FROM tasks
		      WHERE task_id = @id AND lease_holder = @holder AND state = @leased
		        AND attempts = @attempts AND lease_expiry = @expiry`,
		Params: map[string]any{
			"id": t.ID, paramHolder: q.holder, paramLeased: string(task.StateLeased),
			colAttempts: int64(t.Attempts), paramExpiry: t.LeaseExpiry,
		},
	})
	defer iter.Stop()
	_, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return errStaleJournalDelivery
	}
	if err != nil {
		return fmt.Errorf("probing delivery of task %s: %w", t.ID, err)
	}
	return nil
}
