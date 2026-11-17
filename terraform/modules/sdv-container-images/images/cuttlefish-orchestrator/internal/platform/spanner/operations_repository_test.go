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

package spanner_test

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// opRepo builds a repository over a fresh emulator database, plus the queue it
// shares a transaction with.
func opRepo(t *testing.T) (*cspanner.OperationsRepository, *cspanner.TaskQueue, *spannertest.TestDB) {
	t.Helper()
	db := spannertest.MustNewTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(4), cspanner.WithHolder("w"))
	return cspanner.NewOperationsRepository(db.Client, q, nil), q, db
}

func TestOperationAdmitInsertsOperationAndTask(t *testing.T) {
	t.Parallel()
	repo, q, _ := opRepo(t)
	ctx := t.Context()

	rec, err := repo.Admit(ctx, cspanner.AdmitRequest{
		Verb:       operation.VerbDeviceRestart,
		ResourceID: "dev-1",
		CreatedBy:  "someone@example.com",
	})
	require.NoError(t, err)
	require.NotEmpty(t, rec.ID)
	require.NotEmpty(t, rec.TaskID, "the handle must name the task that will execute it")

	got, err := repo.Get(ctx, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, operation.VerbDeviceRestart, got.Verb)
	assert.Equal(t, "dev-1", got.ResourceID)
	assert.Equal(t, "someone@example.com", got.CreatedBy,
		"actuation runs in a worker with no principal, so the row is the only carrier")
	assert.False(t, got.Done)
	assert.True(t, got.Exclusive)
	assert.True(t, got.Mutating)

	// The task is real and claimable: an operation that named no queued work
	// would be a handle onto nothing.
	claimed, err := q.Claim(ctx, task.ShardFor(rec.TaskID, 4), 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	assert.Equal(t, rec.TaskID, claimed[0].ID)
	assert.Equal(t, 3, claimed[0].MaxAttempts, "restart's registry budget rides the task")

	// GetByTask is the handler's entry point.
	byTask, err := repo.GetByTask(ctx, rec.TaskID)
	require.NoError(t, err)
	assert.Equal(t, rec.ID, byTask.ID)
}

// The powerwash budget must reach the task, because it is what makes the sweep
// dead-letter instead of redelivering a destructive actuation.
func TestOperationAdmitCarriesPowerwashAttemptBudget(t *testing.T) {
	t.Parallel()
	repo, q, _ := opRepo(t)

	rec, err := repo.Admit(t.Context(), cspanner.AdmitRequest{
		Verb:       operation.VerbDevicePowerwash,
		ResourceID: "dev-1",
	})
	require.NoError(t, err)

	claimed, err := q.Claim(t.Context(), task.ShardFor(rec.TaskID, 4), 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	assert.Equal(t, 1, claimed[0].MaxAttempts,
		"powerwash must never auto-retry: a redelivery re-wipes a device")
	assert.True(t, claimed[0].Exhausted(), "one attempt means the first is the last")
}

// The AIP-151 refusal. Serializability settles the race and the UNIQUE index
// backs it, but the observable requirement is that the loser gets a 409-shaped
// sentinel naming what it must wait for, and not an AlreadyExists 500.
func TestOperationAdmitRefusesASecondExclusiveVerb(t *testing.T) {
	t.Parallel()
	repo, _, _ := opRepo(t)
	ctx := t.Context()

	first, err := repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDeviceRestart, ResourceID: "dev-1",
	})
	require.NoError(t, err)

	_, err = repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDevicePowerwash, ResourceID: "dev-1",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, operation.ErrInFlight)
	assert.Contains(t, err.Error(), operation.ResourceName(first.ID),
		"the refusal must name the operation to poll, or the client cannot wait for it")
	assert.Contains(t, err.Error(), string(operation.VerbDeviceRestart))

	// A different device is unaffected: exclusivity is per resource.
	_, err = repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDeviceRestart, ResourceID: "dev-2",
	})
	require.NoError(t, err)
}

// Two concurrent admissions must yield exactly one operation and clean refusals
// for the rest, which is the user-visible property however it is reached.
//
// What this does NOT prove: the AlreadyExists retry loop. Serializability
// resolves the race first here (the losing transaction aborts, re-runs, re-reads
// and finds the live row) so the unique index never rejects an insert and the
// loop is never entered. Verified by mutation: collapsing the loop to one
// iteration leaves this test green. The loop stays as a fallback (it is the house
// convention, and the index violation IS reachable, since an admission that skips
// the orphan heal surfaces exactly it), but nothing here exercises it.
func TestOperationAdmitConcurrentYieldsOneOperationAndOne409(t *testing.T) {
	t.Parallel()
	repo, _, _ := opRepo(t)
	ctx := t.Context()

	const racers = 4
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		okCount int
		errs    []error
	)
	for range racers {
		wg.Go(func() {
			_, err := repo.Admit(ctx, cspanner.AdmitRequest{
				Verb: operation.VerbDeviceRestart, ResourceID: "dev-1",
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				okCount++
				return
			}
			errs = append(errs, err)
		})
	}
	wg.Wait()

	assert.Equal(t, 1, okCount, "exactly one admission may win")
	require.Len(t, errs, racers-1)
	for _, err := range errs {
		assert.ErrorIs(t, err, operation.ErrInFlight,
			"a losing racer must be refused cleanly, not with a raw AlreadyExists")
	}
}

// The self-healing property. What it turns on is that the heal UPDATE (which
// regenerates exclusive_key to NULL) and the successor INSERT (which takes that
// same value) commit against the UNIQUE index in ONE transaction.
func TestOperationAdmitHealsAnOrphanAndAdmitsTheSuccessor(t *testing.T) {
	t.Parallel()
	repo, q, _ := opRepo(t)
	ctx := t.Context()

	orphan, err := repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDeviceRestart, ResourceID: "dev-1",
	})
	require.NoError(t, err)

	// The task vanishes without the handler writing an outcome: an instance
	// killed mid-actuation, or a Complete that landed while the terminal write
	// did not.
	require.NoError(t, q.DeleteTask(ctx, orphan.TaskID))

	successor, err := repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDevicePowerwash, ResourceID: "dev-1",
	})
	require.NoError(t, err, "a dangling row must never permanently block a device")

	healed, err := repo.Get(ctx, orphan.ID)
	require.NoError(t, err)
	assert.True(t, healed.Done, "the orphan must be settled, not left open forever")
	require.NotNil(t, healed.Error)
	assert.Contains(t, healed.Error.Detail, "may or may not",
		"the message must admit the actuation may have taken effect")

	live, err := repo.Get(ctx, successor.ID)
	require.NoError(t, err)
	assert.False(t, live.Done)
}

// A dead-lettered task is NOT gone, because deadLetter UPDATEs the row to failed
// rather than deleting it, so an operation whose handler never wrote an outcome
// would look healthy forever if the predicate were "no task row" instead of "no
// LIVE task".
func TestOperationAdmitHealsPastADeadLetteredTask(t *testing.T) {
	t.Parallel()
	repo, q, _ := opRepo(t)
	ctx := t.Context()

	orphan, err := repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDevicePowerwash, ResourceID: "dev-1",
	})
	require.NoError(t, err)

	// Powerwash has MaxAttempts 1, so one claim exhausts it and Fail dead-letters.
	claimed, err := q.Claim(ctx, task.ShardFor(orphan.TaskID, 4), 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, q.Fail(ctx, claimed[0], errors.New("host unreachable"), 0))

	_, err = repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDeviceRestart, ResourceID: "dev-1",
	})
	require.NoError(t, err, "a dead-lettered task is not a live one")

	healed, err := repo.Get(ctx, orphan.ID)
	require.NoError(t, err)
	assert.True(t, healed.Done)
}

// A live task blocks: this is the assertion that would go green if the liveness
// predicate were dropped entirely, so it is what pins the refusal to task state
// rather than to row existence.
func TestOperationAdmitRefusesWhileTheTaskIsLeased(t *testing.T) {
	t.Parallel()
	repo, q, _ := opRepo(t)
	ctx := t.Context()

	first, err := repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDeviceRestart, ResourceID: "dev-1",
	})
	require.NoError(t, err)

	claimed, err := q.Claim(ctx, task.ShardFor(first.TaskID, 4), 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "the task must be leased for this to test what it claims")

	_, err = repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDeviceRestart, ResourceID: "dev-1",
	})
	assert.ErrorIs(t, err, operation.ErrInFlight)
}

// The CAS. Without it an at-least-once redelivery that fails after a recorded
// success overwrites the success with a failure, and a user is told a powerwash
// failed that in fact succeeded.
func TestOperationTerminateRefusesToOverwriteASettledOperation(t *testing.T) {
	t.Parallel()
	repo, _, _ := opRepo(t)
	ctx := t.Context()

	rec, err := repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDevicePowerwash, ResourceID: "dev-1",
	})
	require.NoError(t, err)

	success, err := json.Marshal(map[string]any{"powerwashed_at": time.Now().UTC()})
	require.NoError(t, err)
	require.NoError(t, repo.Terminate(ctx, rec.ID, nil, success))

	// The redelivered attempt fails and tries to record that.
	require.NoError(t, repo.Terminate(ctx, rec.ID,
		&operation.Problem{Title: "Bad Gateway", Status: 502, Detail: "host unreachable"}, nil),
		"a fenced-out terminal write is not an error; another delivery owns the outcome")

	got, err := repo.Get(ctx, rec.ID)
	require.NoError(t, err)
	assert.True(t, got.Done)
	assert.Nil(t, got.Error, "the recorded SUCCESS must survive the later failure")
	require.NotNil(t, got.Response)
	assert.Contains(t, string(got.Response), "powerwashed_at")
}

// Terminating clears the admission key, so the next verb is admitted.
func TestOperationTerminateReleasesExclusivity(t *testing.T) {
	t.Parallel()
	repo, _, _ := opRepo(t)
	ctx := t.Context()

	first, err := repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDeviceRestart, ResourceID: "dev-1",
	})
	require.NoError(t, err)
	require.NoError(t, repo.Terminate(ctx, first.ID, nil, []byte(`{"restarted_at":"2026-07-26T00:00:00Z"}`)))

	_, err = repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDeviceRestart, ResourceID: "dev-1",
	})
	assert.NoError(t, err)
}

// All three generated predicates, against every verb in the registry. This is the
// column class with no second chance, because a STORED generated column's
// expression cannot be altered in place, and it has been specified wrongly twice
// on paper.
func TestOperationGeneratedKeysCoverEveryRegisteredVerb(t *testing.T) {
	t.Parallel()
	repo, _, _ := opRepo(t)
	ctx := t.Context()

	for _, verb := range operation.Verbs() {
		policy, err := operation.PolicyFor(verb)
		require.NoError(t, err)

		resource := "dev-keys-" + string(verb)
		rec, err := repo.Admit(ctx, cspanner.AdmitRequest{Verb: verb, ResourceID: resource})
		require.NoError(t, err)

		// open_at covers EVERY in-flight row regardless of the other two keys.
		open, err := repo.ListOpen(ctx, 0, 100)
		require.NoError(t, err)
		assert.True(t, containsOperation(open, rec.ID),
			"%s is invisible to idx_operations_open, so the orphan sweep would never find it", verb)

		// mutating_key drives the reconciling signal.
		live, err := repo.LiveByResources(ctx, operation.ResourceTypeDevice, []string{resource})
		require.NoError(t, err)
		assert.Equal(t, policy.Mutating, live.Covers(resource, ""),
			"%s: mutating_key disagrees with its registry entry", verb)

		// exclusive_key drives admission: a second exclusive verb is refused
		// only when this one is exclusive.
		_, err = repo.Admit(ctx, cspanner.AdmitRequest{
			Verb: operation.VerbDeviceRestart, ResourceID: resource,
		})
		if policy.Exclusive {
			assert.ErrorIs(t, err, operation.ErrInFlight,
				"%s claims exclusivity but did not block a second verb", verb)
		} else {
			assert.NoError(t, err,
				"%s is not exclusive and must not block anything", verb)
		}

		// Settling must remove it from every live index.
		require.NoError(t, repo.Terminate(ctx, rec.ID, nil, []byte(`{}`)))
		open, err = repo.ListOpen(ctx, 0, 100)
		require.NoError(t, err)
		assert.False(t, containsOperation(open, rec.ID),
			"%s is still in the open index after settling; the sweep would re-terminate it forever", verb)
		live, err = repo.LiveByResources(ctx, operation.ResourceTypeDevice, []string{resource})
		require.NoError(t, err)
		assert.False(t, live.Covers(resource, ""), "%s still reports reconciling after settling", verb)
	}
}

// The sweep's age floor: an operation admitted moments ago must be skipped, so the
// sweep never races a handler that has not started.
func TestOperationListOpenHonorsTheAgeFloor(t *testing.T) {
	t.Parallel()
	repo, _, _ := opRepo(t)
	ctx := t.Context()

	rec, err := repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDeviceRestart, ResourceID: "dev-1",
	})
	require.NoError(t, err)

	fresh, err := repo.ListOpen(ctx, time.Hour, 100)
	require.NoError(t, err)
	assert.False(t, containsOperation(fresh, rec.ID), "a just-admitted operation is not an orphan")

	all, err := repo.ListOpen(ctx, 0, 100)
	require.NoError(t, err)
	assert.True(t, containsOperation(all, rec.ID))
}

func TestOperationListFiltersAndPaginates(t *testing.T) {
	t.Parallel()
	repo, _, _ := opRepo(t)
	ctx := t.Context()

	first, err := repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDeviceRestart, ResourceID: "dev-1",
	})
	require.NoError(t, err)
	require.NoError(t, repo.Terminate(ctx, first.ID, nil, []byte(`{}`)))
	second, err := repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDevicePowerwash, ResourceID: "dev-1",
	})
	require.NoError(t, err)
	_, err = repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDeviceRestart, ResourceID: "dev-other",
	})
	require.NoError(t, err)

	byResource, err := repo.List(ctx, cspanner.ListFilter{ResourceID: "dev-1", Limit: 10})
	require.NoError(t, err)
	assert.Len(t, byResource, 2)

	notDone := false
	open, err := repo.List(ctx, cspanner.ListFilter{ResourceID: "dev-1", Done: &notDone, Limit: 10})
	require.NoError(t, err)
	require.Len(t, open, 1)
	assert.Equal(t, second.ID, open[0].ID)

	page, err := repo.List(ctx, cspanner.ListFilter{ResourceID: "dev-1", Limit: 1})
	require.NoError(t, err)
	assert.Len(t, page, 1, "limit bounds the page; the index behind resource_id spans the full retention")
}

// The fleet-wide ?done=false listing reads idx_operations_open rather than
// scanning on `done`, so it has to agree with the plain filter it replaced:
// live rows only, newest first, across every resource.
func TestOperationListOpenAcrossResourcesIsNewestFirst(t *testing.T) {
	t.Parallel()
	repo, _, _ := opRepo(t)
	ctx := t.Context()

	settled, err := repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDeviceRestart, ResourceID: "dev-1",
	})
	require.NoError(t, err)
	require.NoError(t, repo.Terminate(ctx, settled.ID, nil, []byte(`{}`)))

	older, err := repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDevicePowerwash, ResourceID: "dev-1",
	})
	require.NoError(t, err)
	newer, err := repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDeviceRestart, ResourceID: "dev-2",
	})
	require.NoError(t, err)

	notDone := false
	open, err := repo.List(ctx, cspanner.ListFilter{Done: &notDone, Limit: 10})
	require.NoError(t, err)

	ids := make([]string, 0, len(open))
	for _, op := range open {
		ids = append(ids, op.ID)
	}
	assert.Equal(t, []string{newer.ID, older.ID}, ids,
		"open operations from every resource, newest first, with the settled one dropped")

	page, err := repo.List(ctx, cspanner.ListFilter{Done: &notDone, Limit: 1, Offset: 1})
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, older.ID, page[0].ID, "offset walks the same order")
}

// The device controller holds a device's instances from settling while a verb
// passes them through, and it can only key that hold on the pair the row
// carries. A result set keyed by resource alone makes a restart of one
// instance exempt the whole device, an instance that is actually dead
// included.
//
// Nothing issues a per-instance verb yet: the transport that will is separate
// work, so the row is fabricated through Admit here. That is also why this test
// matters. Until that transport lands, it is the only thing exercising the
// column at all.
func TestOperationLiveByResourcesCarriesTheActuatedInstance(t *testing.T) {
	t.Parallel()
	repo, _, _ := opRepo(t)
	ctx := t.Context()

	_, err := repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDeviceRestart, ResourceID: "dev-1", InstanceName: "i2",
	})
	require.NoError(t, err)
	// No instance name: the device-wide verbs (start, stop, delete, and every
	// group verb) pass every instance through.
	_, err = repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDevicePowerwash, ResourceID: "dev-2",
	})
	require.NoError(t, err)

	live, err := repo.LiveByResources(ctx, operation.ResourceTypeDevice, []string{"dev-1", "dev-2", "dev-3"})
	require.NoError(t, err)

	assert.True(t, live.Covers("dev-1", "i2"), "the restarted instance is under its verb")
	assert.False(t, live.Covers("dev-1", "i5"),
		"a per-instance verb must not exempt an instance it is not touching")
	assert.True(t, live.Covers("dev-2", "i5"), "a NULL instance_name exempts every instance")
	assert.False(t, live.Covers("dev-3", "i2"), "no live verb, no hold")

	// The column round-trips on a full read too, so a handler can tell which
	// instance it was admitted for.
	byResource, err := repo.List(ctx, cspanner.ListFilter{ResourceID: "dev-1", Limit: 10})
	require.NoError(t, err)
	require.Len(t, byResource, 1)
	assert.Equal(t, "i2", byResource[0].InstanceName)
}

func TestOperationGetUnknownIsNotFound(t *testing.T) {
	t.Parallel()
	repo, _, _ := opRepo(t)

	_, err := repo.Get(t.Context(), "no-such-operation")
	assert.ErrorIs(t, err, operation.ErrNotFound)

	_, err = repo.GetByTask(t.Context(), "no-such-task")
	assert.ErrorIs(t, err, operation.ErrNotFound)
}

// The request payload is the only place a verb's arguments can live, since a task
// carries just a resource id.
func TestOperationAdmitRoundTripsTheRequestPayload(t *testing.T) {
	t.Parallel()
	repo, _, _ := opRepo(t)

	rec, err := repo.Admit(t.Context(), cspanner.AdmitRequest{
		Verb:       operation.VerbDeviceRestart,
		ResourceID: "dev-1",
		Request:    []byte(`{"reason":"wedged launcher"}`),
	})
	require.NoError(t, err)

	got, err := repo.Get(t.Context(), rec.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Request)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(got.Request, &decoded))
	assert.Equal(t, "wedged launcher", decoded["reason"])
}

// An unregistered verb must be refused before anything is written: every default
// the registry would otherwise supply is wrong.
func TestOperationAdmitRejectsAnUnregisteredVerb(t *testing.T) {
	t.Parallel()
	repo, _, _ := opRepo(t)

	_, err := repo.Admit(t.Context(), cspanner.AdmitRequest{
		Verb: operation.Verb("device.nonexistent"), ResourceID: "dev-1",
	})
	assert.ErrorIs(t, err, operation.ErrUnknownVerb)
}

func TestOperationOpenStatsTracksTheWorkingSet(t *testing.T) {
	t.Parallel()
	repo, _, _ := opRepo(t)
	ctx := t.Context()

	rec, err := repo.Admit(ctx, cspanner.AdmitRequest{
		Verb: operation.VerbDeviceRestart, ResourceID: "dev-1",
	})
	require.NoError(t, err)

	depth, age, err := repo.OpenStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), depth)
	assert.GreaterOrEqual(t, age, time.Duration(0))

	require.NoError(t, repo.Terminate(ctx, rec.ID, nil, []byte(`{}`)))
	depth, _, err = repo.OpenStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), depth, "a settled operation leaves the working set")
}

func containsOperation(recs []*operation.Record, id string) bool {
	for _, r := range recs {
		if r.ID == id {
			return true
		}
	}
	return false
}
