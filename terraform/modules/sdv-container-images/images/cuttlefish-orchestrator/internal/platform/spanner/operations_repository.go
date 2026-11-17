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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/google/uuid"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// paramKey is the bound resource key shared by the generated-column queries.
const paramKey = "key"

// OperationsRepository persists operation handles.
//
// It is homed here rather than in a feature slice because three packages consume
// it across import firewalls: features/device writes terminal state from its
// task handlers, features/operation serves the reads, and jobs/taskreclaimer
// sweeps orphans. Features may not import features and jobs may not import
// features, so no feature-owned repository could serve all three. Each consumer
// takes a narrow interface declared where it is used.
type OperationsRepository struct {
	client *Client
	queue  *TaskQueue
	logger *slog.Logger
}

// NewOperationsRepository builds the repository. The queue is required: an
// operation and the task that executes it are inserted in ONE transaction, so
// admission needs the queue's mutation builder rather than its own insert.
func NewOperationsRepository(client *Client, queue *TaskQueue, logger *slog.Logger) *OperationsRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &OperationsRepository{client: client, queue: queue, logger: logger}
}

// AdmitRequest is one verb admission: the operation to record and the task that
// will execute it.
type AdmitRequest struct {
	Verb       operation.Verb
	ResourceID string
	CreatedBy  string

	// InstanceName narrows the verb to one instance of the resource; "" means
	// the whole resource. It changes nothing about admission, since exclusivity
	// is per resource, but the device controller reads it to exempt only the
	// instance being actuated from settling.
	InstanceName string

	// Request is the user's parameters, if the verb takes any. Resolution (host
	// URL, CVD group) is NOT stored. The handler re-derives it at execution
	// time, because minutes can pass before the claim and a host can move.
	Request []byte

	// Mutations are folded into the admission transaction, for callers that must
	// commit a row change atomically with the admission.
	Mutations []*spanner.Mutation
}

// Admit records an operation and enqueues its task in one transaction, refusing
// the verb when an exclusive operation is already live on the resource.
//
// The refusal is guaranteed. Two mechanisms back it, and both are needed.
// Spanner's serializability settles two concurrent admissions (one commits,
// the other re-runs, re-reads, and sees the live row), and the
// UNIQUE index on exclusive_key rejects a loser structurally even if isolation
// were relaxed. The unique index is why the AlreadyExists retry below exists:
// without it a racing caller gets a 500 instead of a 409.
//
// Admission also SELF-HEALS: an operation whose task is gone without a terminal
// write is terminated here, so a dangling row can never permanently block a
// resource and the orphan sweep is hygiene rather than a liveness gate. That
// only holds for exclusive verbs. Nothing reads this index for a verb that is
// neither exclusive nor mutating, so those depend on the sweep.
func (r *OperationsRepository) Admit(ctx context.Context, req AdmitRequest) (*operation.Record, error) {
	policy, err := operation.PolicyFor(req.Verb)
	if err != nil {
		return nil, fmt.Errorf("admitting %s: %w", req.Verb, err)
	}

	// Mint the id OUTSIDE the retryable closure. A ReadWriteTransaction callback
	// may run more than once, so an id generated inside can differ between
	// attempts and the handle returned to the caller may not name the row that
	// committed.
	rec := &operation.Record{
		ID:           uuid.New().String(),
		Verb:         req.Verb,
		ResourceType: operation.ResourceTypeDevice,
		ResourceID:   req.ResourceID,
		InstanceName: req.InstanceName,
		Request:      req.Request,
		Exclusive:    policy.Exclusive,
		Mutating:     policy.Mutating,
		CreatedBy:    req.CreatedBy,
	}

	key := rec.ResourceType + "/" + rec.ResourceID
	var inFlight *operation.Record
	for range 2 {
		inFlight, err = r.admitOnce(ctx, rec, policy, key, req.Mutations)
		if err == nil || spanner.ErrCode(err) != codes.AlreadyExists {
			break
		}
		// The unique index rejected this insert, so a racing admission won.
		// Re-run to re-read the winner and report it as the in-flight operation.
	}
	if inFlight != nil {
		operationMetrics().refused(ctx, req.Verb)
		return nil, fmt.Errorf("%w: %s (%s, started %s)", operation.ErrInFlight,
			operation.ResourceName(inFlight.ID), inFlight.Verb,
			inFlight.CreatedAt.UTC().Format(time.RFC3339))
	}
	if err != nil {
		return nil, fmt.Errorf("admitting %s on %s: %w", req.Verb, req.ResourceID, err)
	}
	// The verb's task committed inside admitOnce; nothing else tells the pool.
	r.queue.Wake()
	return rec, nil
}

// admitOnce runs one attempt of the admission transaction. It returns the live
// operation when the verb must be refused, leaving rec untouched.
func (r *OperationsRepository) admitOnce(ctx context.Context, rec *operation.Record,
	policy operation.Policy, key string, extra []*spanner.Mutation,
) (*operation.Record, error) {
	var refused *operation.Record
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		refused = nil // Reset: the transaction body may run more than once.

		heal, blocked, err := r.resolveExclusivity(ctx, txn, policy, key)
		if err != nil {
			return err
		}
		if blocked != nil {
			refused = blocked
			return nil
		}

		t := task.New(policy.TaskType, rec.ResourceID)
		t.MaxAttempts = policy.MaxAttempts
		enqueue := r.queue.EnqueueMutation(ctx, t)
		rec.TaskID = t.ID

		now := timeNow()
		rec.CreatedAt = now
		rec.UpdatedAt = now
		insert, err := models.OperationInsert(rec, now)
		if err != nil {
			return fmt.Errorf("building operation insert: %w", err)
		}

		writes := make([]*spanner.Mutation, 0, len(heal)+len(extra)+2)
		writes = append(writes, heal...)
		writes = append(writes, insert, enqueue)
		writes = append(writes, extra...)
		return txn.BufferWrite(writes)
	})
	if err != nil {
		return nil, err
	}
	return refused, nil
}

// resolveExclusivity decides what an exclusive verb may do about a predecessor.
// It returns the heal writes to fold into this transaction, or the live operation
// the caller must be refused for.
//
// The heal is a BLIND write on purpose: the read that justified it is in this same
// serializable transaction, so the transaction is the fence and a `done = false`
// predicate would be redundant rather than safer. That is the exception to the
// terminal-write CAS, and it is the only one.
func (r *OperationsRepository) resolveExclusivity(ctx context.Context, txn *spanner.ReadWriteTransaction,
	policy operation.Policy, key string,
) (heal []*spanner.Mutation, blocked *operation.Record, err error) {
	if !policy.Exclusive {
		return nil, nil, nil
	}
	live, found, err := r.liveExclusive(ctx, txn, key)
	if err != nil || !found {
		return nil, nil, err
	}
	alive, err := r.taskIsLive(ctx, txn, live.TaskID)
	if err != nil {
		return nil, nil, err
	}
	if alive {
		return nil, live, nil
	}
	return []*spanner.Mutation{terminalUpdate(live.ID, operation.OrphanedProblem(), nil, timeNow())}, nil, nil
}

// liveExclusive reads the in-flight exclusive operation for a resource key,
// reporting whether one exists.
func (r *OperationsRepository) liveExclusive(ctx context.Context, txn *spanner.ReadWriteTransaction, key string) (*operation.Record, bool, error) {
	iter := txn.Query(ctx, spanner.Statement{
		// The IS NOT NULL is the documented requirement for reading a
		// NULL_FILTERED index, not redundancy next to the equality: the index
		// omits every row whose key is NULL, so the query has to exclude them
		// too. Spanner does happen to infer that from `= @param` today, but that
		// inference is the optimizer's courtesy rather than the contract, it has
		// measured gaps (an IN UNNEST of a bound array is refused outright), and
		// optimizer versions become the default on their own schedule, so a
		// query that leans on inference can stop working without anyone touching
		// it. Stating the predicate costs nothing: the plan and latency are
		// identical either way, because the index holds only non-null rows.
		//
		// The emulator cannot adjudicate any of this. It answers "not able to
		// determine" for every form, correct or broken, which is why the hint
		// below means "verified against the real database", and why setting it
		// without doing that is how the reconciling query shipped refused.
		SQL: `@{spanner_emulator.disable_query_null_filtered_index_check=true}
		      SELECT ` + models.OperationSelectList + `
		      FROM operations@{FORCE_INDEX=idx_operations_exclusive}
		      WHERE exclusive_key IS NOT NULL AND exclusive_key = @key`,
		Params: map[string]any{paramKey: key},
	})
	defer iter.Stop()

	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading live exclusive operation: %w", err)
	}
	rec, err := models.ScanOperation(row)
	if err != nil {
		return nil, false, fmt.Errorf("scanning live exclusive operation: %w", err)
	}
	return rec, true, nil
}

// taskIsLive reports whether a task row is pending or leased.
//
// The predicate is "no LIVE task", not "no task row": Complete deletes the row
// but deadLetter UPDATEs it to failed, where it then persists for the tasks
// table's retention. Treating a dead-lettered row as live would leave an
// operation whose handler never wrote an outcome looking healthy forever.
func (r *OperationsRepository) taskIsLive(ctx context.Context, txn *spanner.ReadWriteTransaction, taskID string) (bool, error) {
	if taskID == "" {
		return false, nil
	}
	row, err := txn.ReadRow(ctx, tasksTable, spanner.Key{taskID}, []string{colState})
	if err != nil {
		if spanner.ErrCode(err) == codes.NotFound {
			return false, nil
		}
		return false, fmt.Errorf("reading operation's task row: %w", err)
	}
	var state string
	if err := row.Columns(&state); err != nil {
		return false, fmt.Errorf("scanning task state: %w", err)
	}
	s := task.State(state)
	return s == task.StatePending || s == task.StateLeased, nil
}

// Terminate writes an operation's terminal state, refusing to overwrite one
// that has already settled.
//
// It is DML with a CAS rather than a mutation because a
// Spanner mutation is a blind write with nowhere to put `WHERE done = false`, so
// a repository "shaped like the idempotency one" would silently drop the
// condition. This is the one place the design steps outside the queue's delivery
// fencing and has to replace it. Without the CAS, an at-least-once redelivery
// that fails after a recorded success overwrites the success with a failure, and
// the user is told a powerwash failed that in fact succeeded.
//
// A zero row count is not an error: it means another delivery already settled the
// operation. It is logged AND counted, because it is the only trace of the race
// it just prevented and a log line is the one form of that trace nothing
// aggregates.
func (r *OperationsRepository) Terminate(ctx context.Context, id string, problem *operation.Problem, response []byte) error {
	var rows int64
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		rows = 0 // Reset: the transaction body may run more than once.
		var err error
		rows, err = txn.Update(ctx, terminalStatement(id, problem, response, timeNow()))
		return err
	})
	if err != nil {
		return fmt.Errorf("terminating operation %s: %w", id, err)
	}
	if rows == 0 {
		r.logger.InfoContext(ctx, "operation terminal write fenced out; already settled by another delivery",
			slog.String("operation_id", id))
		operationMetrics().terminalWriteRejected(ctx)
	}
	return nil
}

// Get reads one operation by id.
func (r *OperationsRepository) Get(ctx context.Context, id string) (*operation.Record, error) {
	stmt := spanner.Statement{
		SQL:    `SELECT ` + models.OperationSelectList + ` FROM operations WHERE operation_id = @id`,
		Params: map[string]any{"id": id},
	}
	rec, found, err := r.queryOne(ctx, stmt)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w: %s", operation.ErrNotFound, id)
	}
	return rec, nil
}

// GetByTask reads the operation a claimed task executes, which is the handler's
// entry point. Stable across retries, postpones and sweeper requeues, because a
// task row is never re-keyed.
func (r *OperationsRepository) GetByTask(ctx context.Context, taskID string) (*operation.Record, error) {
	stmt := spanner.Statement{
		// task_id is nullable, so the null-filtered index omits rows without one
		// and the query must say so explicitly. See liveExclusive for why this
		// is stated rather than left to the optimizer to infer.
		SQL: `@{spanner_emulator.disable_query_null_filtered_index_check=true}
		      SELECT ` + models.OperationSelectList + `
		      FROM operations@{FORCE_INDEX=idx_operations_task}
		      WHERE task_id IS NOT NULL AND task_id = @task`,
		Params: map[string]any{"task": taskID},
	}
	rec, found, err := r.queryOne(ctx, stmt)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w: for task %s", operation.ErrNotFound, taskID)
	}
	return rec, nil
}

// ListFilter narrows a listing. Done is tri-state: nil means "either".
type ListFilter struct {
	ResourceID string
	Done       *bool
	Limit      int
	Offset     int
}

// List returns operations newest-first.
//
// Every caller is paginated, and here that is required rather than
// conventional: a resource_id listing rides idx_operations_by_resource, which is
// the only operations index that is NOT null-filtered and therefore spans the
// full retention.
func (r *OperationsRepository) List(ctx context.Context, f ListFilter) ([]*operation.Record, error) {
	if f.ResourceID == "" && f.Done != nil && !*f.Done {
		return r.listOpenPage(ctx, f)
	}

	where := ""
	params := map[string]any{paramLimit: int64(f.Limit), "offset": int64(f.Offset)}
	switch {
	case f.ResourceID != "" && f.Done != nil:
		where = "WHERE resource_id = @resource AND done = @done"
		params["resource"] = f.ResourceID
		params["done"] = *f.Done
	case f.ResourceID != "":
		where = "WHERE resource_id = @resource"
		params["resource"] = f.ResourceID
	case f.Done != nil:
		where = "WHERE done = @done"
		params["done"] = *f.Done
	}

	stmt := spanner.Statement{
		SQL: `SELECT ` + models.OperationSelectList + ` FROM operations ` + where + `
		      ORDER BY created_at DESC LIMIT @limit OFFSET @offset`,
		Params: params,
	}
	return r.queryAll(ctx, stmt)
}

// listOpenPage is the unfiltered ?done=false listing, newest-first.
//
// It exists because `WHERE done = false` cannot use an index: the only one
// covering live rows is null-filtered on open_at, and the optimizer will not
// infer the connection from a predicate on `done`. A plan check against real
// Spanner showed that form as `scan_type: TableScan, Full scan: true` over the
// whole 90-day retention, sorted, which a fleet-wide poll would repeat every
// few seconds for a result set that is only ever the in-flight operations.
//
// open_at is `IF(done = false, created_at, NULL) STORED`, so for a live row it
// IS created_at and ordering by it is the same newest-first order. The
// null-filtered index therefore holds exactly this listing's rows and no more.
func (r *OperationsRepository) listOpenPage(ctx context.Context, f ListFilter) ([]*operation.Record, error) {
	stmt := spanner.Statement{
		// The IS NOT NULL predicate is what makes the index legal to force;
		// leaving it to inference is the trap liveExclusive documents.
		SQL: `@{spanner_emulator.disable_query_null_filtered_index_check=true}
		      SELECT ` + models.OperationSelectList + `
		      FROM operations@{FORCE_INDEX=idx_operations_open}
		      WHERE open_at IS NOT NULL
		      ORDER BY open_at DESC LIMIT @limit OFFSET @offset`,
		Params: map[string]any{paramLimit: int64(f.Limit), "offset": int64(f.Offset)},
	}
	return r.queryAll(ctx, stmt)
}

// ListOpen returns in-flight operations oldest-first, skipping any admitted
// within minAge.
//
// It reads idx_operations_open, which is the only index covering EVERY in-flight
// row: exclusive_key and mutating_key are both NULL for a verb that is neither,
// so neither can serve this. The age floor lets the orphan sweep ignore
// operations admitted moments ago rather than reasoning about read visibility.
func (r *OperationsRepository) ListOpen(ctx context.Context, minAge time.Duration, limit int) ([]*operation.Record, error) {
	stmt := spanner.Statement{
		// open_at is NULL once an operation settles, which is exactly what the
		// null-filtered index drops; the range predicate alone leaves that to
		// optimizer inference. See liveExclusive.
		SQL: `@{spanner_emulator.disable_query_null_filtered_index_check=true}
		      SELECT ` + models.OperationSelectList + `
		      FROM operations@{FORCE_INDEX=idx_operations_open}
		      WHERE open_at IS NOT NULL AND open_at < @before
		      ORDER BY open_at LIMIT @limit`,
		Params: map[string]any{"before": timeNow().Add(-minAge), "limit": int64(limit)},
	}
	return r.queryAll(ctx, stmt)
}

// ProgressByTasks reads the live task rows behind a set of operations and returns
// each one's progress, keyed by task id.
//
// This is where an operation's AIP-151 metadata comes from. Nothing about progress
// is stored on the operation: while it is live its task row exists by definition
// and already carries attempts, last error and next availability, so storing them
// would mean a worker write per retry and per postpone for data that is stale by
// the next poll.
//
// One query for the whole page, not a point read per row: a point read is correct
// for a single GET and becomes twenty round trips on a default-page list.
//
// A missing task id yields no entry, because the task completed and was deleted,
// so there is no progress left to report. A DEAD-LETTERED task is not missing
// (deadLetter updates the row rather than deleting it), and its last_error is
// the best diagnostic available for an operation whose handler never wrote one,
// so it is read here and kept in the result.
func (r *OperationsRepository) ProgressByTasks(ctx context.Context, taskIDs []string) (map[string]operation.Progress, error) {
	out := make(map[string]operation.Progress, len(taskIDs))
	if len(taskIDs) == 0 {
		return out, nil
	}
	stmt := spanner.Statement{
		SQL: `SELECT task_id, state, attempts, last_error, available_at
		      FROM tasks WHERE task_id IN UNNEST(@ids)`,
		Params: map[string]any{paramIDs: taskIDs},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading operation progress: %w", err)
		}
		var (
			id, state   string
			attempts    int64
			lastError   spanner.NullString
			availableAt time.Time
		)
		if err := row.Columns(&id, &state, &attempts, &lastError, &availableAt); err != nil {
			return nil, fmt.Errorf("scanning operation progress: %w", err)
		}
		p := operation.Progress{
			Attempt:   int(attempts),
			LastError: models.FromNullString(lastError),
		}
		// A retry hint is only meaningful while the task is still waiting to run.
		if task.State(state) == task.StatePending {
			next := availableAt
			p.NextAttemptAt = &next
		}
		out[id] = p
	}
}

// LiveByResources returns which of the given resources have a live MUTATING
// operation, and which instance of each that verb is actuating. It is the
// reconciling signal, and the device controller's per-instance hold.
//
// instance_name rides the result rather than the key: it is a plain column, not
// part of mutating_key, so the query still selects on the resource alone and
// the caller pairs the two. A NULL reads back as the empty name, which
// operation.LiveSet resolves as "every instance".
//
// One query per read or list, mapped over the result set, never a lookup per
// row: on a list path that is one extra query, which avoids N+1.
func (r *OperationsRepository) LiveByResources(ctx context.Context, resourceType string, ids []string) (operation.LiveSet, error) {
	out := make(operation.LiveSet, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = resourceType + "/" + id
	}
	stmt := spanner.Statement{
		// The IS NOT NULL is REQUIRED, not a redundant precaution. Against an
		// IN UNNEST of a bound array, production Spanner cannot prove the
		// predicate excludes nulls and refuses the null-filtered index outright
		// ("cannot be used because it filters nulls that are required to answer
		// the query"). The equality and range forms below and above need no such
		// help; only this one does. The emulator cannot adjudicate any of them,
		// because it answers "not able to determine" for every form, hint or no
		// hint, so each was run against the real database before its hint was
		// written.
		SQL: `@{spanner_emulator.disable_query_null_filtered_index_check=true}
		      SELECT resource_id, instance_name
		      FROM operations@{FORCE_INDEX=idx_operations_mutating}
		      WHERE mutating_key IS NOT NULL AND mutating_key IN UNNEST(@keys)`,
		Params: map[string]any{"keys": keys},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading live mutating operations: %w", err)
		}
		var (
			id       string
			instance spanner.NullString
		)
		if err := row.Columns(&id, &instance); err != nil {
			return nil, fmt.Errorf("scanning resource id: %w", err)
		}
		out[operation.LiveResource{ResourceID: id, InstanceName: models.FromNullString(instance)}] = true
	}
}

func (r *OperationsRepository) queryOne(ctx context.Context, stmt spanner.Statement) (*operation.Record, bool, error) {
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()
	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading operation: %w", err)
	}
	rec, err := models.ScanOperation(row)
	if err != nil {
		return nil, false, fmt.Errorf("scanning operation: %w", err)
	}
	return rec, true, nil
}

func (r *OperationsRepository) queryAll(ctx context.Context, stmt spanner.Statement) ([]*operation.Record, error) {
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()
	var out []*operation.Record
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("listing operations: %w", err)
		}
		rec, err := models.ScanOperation(row)
		if err != nil {
			return nil, fmt.Errorf("scanning listed operation: %w", err)
		}
		out = append(out, rec)
	}
}

// terminalStatement builds the CAS that settles an operation.
func terminalStatement(id string, problem *operation.Problem, response []byte, now time.Time) spanner.Statement {
	params := map[string]any{"id": id, "now": now}
	params[models.ColError] = nullJSONOrNil(problem)
	params[models.ColResponse] = nullJSONFromRaw(response)
	return spanner.Statement{
		SQL: `UPDATE operations
		      SET done = true, error = @error, response = @response, updated_at = @now
		      WHERE operation_id = @id AND done = false`,
		Params: params,
	}
}

// terminalUpdate is the mutation form, for the one writer whose justifying read
// is in the same transaction (admission's orphan heal) and which therefore needs
// no CAS.
func terminalUpdate(id string, problem *operation.Problem, response []byte, now time.Time) *spanner.Mutation {
	return spanner.UpdateMap(models.OperationsTable, map[string]any{
		models.ColOperationID:   id,
		models.ColOperationDone: true,
		models.ColError:         nullJSONOrNil(problem),
		models.ColResponse:      nullJSONFromRaw(response),
		ColUpdatedAt:            now,
	})
}

func nullJSONOrNil(problem *operation.Problem) spanner.NullJSON {
	if problem == nil {
		return spanner.NullJSON{}
	}
	return spanner.NullJSON{Value: problem, Valid: true}
}

func nullJSONFromRaw(raw []byte) spanner.NullJSON {
	if len(raw) == 0 {
		return spanner.NullJSON{}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return spanner.NullJSON{}
	}
	return spanner.NullJSON{Value: v, Valid: true}
}
