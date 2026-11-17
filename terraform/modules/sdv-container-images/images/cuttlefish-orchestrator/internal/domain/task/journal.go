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

import "context"

// OperationJournal durably records the remote operation a task attempt
// started (an HO cvd-load operation, a GCE instance operation) so that a
// redelivery (after an abandoned wait, a worker crash, or a lease takeover)
// RESUMES polling the same operation instead of re-actuating or adopting the
// half-finished result by observation.
//
// Semantics: successes only, and the record is written
// AFTER the remote accepts the operation (at-least-once; the small
// crash-before-record window is covered by the callers' existing idempotency:
// deterministic names, per-delivery request IDs, and the transient-state
// adopt guards). A recorded operation that later completes with a terminal
// failure is CLEARED by the consumer so the next attempt re-runs the step
// from the top instead of re-reading the same stored failure to exhaustion.
// Journal rows are interleaved in the task and die with it.
//
// Writes take the claimed task (rather than its ID alone) for the same reason
// Queue.Complete does: implementations fence them on the delivery (holder,
// claimed attempts, lease expiry) so a zombie attempt whose task was swept
// and re-claimed cannot append a newer record (or clear the journal) out from
// under the live delivery, whose next read would resume the wrong operation.
// The read is NOT fenced: resume works exactly by a new
// delivery reading the record an older delivery wrote.
type OperationJournal interface {
	// RecordedOperation returns the operation name recorded for a step, or
	// "" when none is recorded.
	RecordedOperation(ctx context.Context, t *Task, step string) (string, error)

	// RecordOperation records the operation name a step started. Fenced on
	// the delivery: an error means the record was refused (or the write
	// failed), never that a stale record landed.
	RecordOperation(ctx context.Context, t *Task, step, operation string) error

	// ClearOperations drops every recorded step for a task, used when a
	// resumed operation turns out terminally failed, so the journey restarts.
	// Fenced on the delivery like RecordOperation.
	ClearOperations(ctx context.Context, t *Task) error
}
