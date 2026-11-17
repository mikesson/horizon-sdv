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
	"errors"
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// Edge-priority tasks claim ahead of older resync tasks; FIFO within a tier.
func TestClaimOrdersByPriorityThenAge(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))
	ctx := t.Context()

	resync := task.New("host.reconcile", "h-1")
	resync.Priority = task.PriorityResync
	require.NoError(t, q.Enqueue(ctx, resync))

	edge := task.New("device.create_cvd", "d-1") // enqueued later, claims first
	require.NoError(t, q.Enqueue(ctx, edge))

	got, err := q.Claim(ctx, 0, 2, time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, edge.ID, got[0].ID, "edge priority claims ahead of older resync work")
	assert.Equal(t, resync.ID, got[1].ID)
	assert.Equal(t, task.PriorityResync, got[1].Priority, "priority survives the claim round-trip")
}

// A key with a LEASED sibling is skipped, which is per-key serialization: an
// edge event landing mid-attempt waits for that attempt instead of running the
// same resource concurrently (the workqueue locked-set).
func TestClaimSkipsKeysWithLeasedSibling(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))
	ctx := t.Context()

	first := task.New("host.reconcile", "h-1")
	require.NoError(t, q.Enqueue(ctx, first))
	claimed, err := q.Claim(ctx, 0, 1, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	// The dirty bit: a second pending task for the same key while the first
	// is leased. Also an unrelated key that must remain claimable.
	dirty := task.New("host.reconcile", "h-1")
	require.NoError(t, q.Enqueue(ctx, dirty))
	other := task.New("host.reconcile", "h-2")
	require.NoError(t, q.Enqueue(ctx, other))

	got, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 1, "the leased key's sibling must be skipped")
	assert.Equal(t, other.ID, got[0].ID)

	// Once the first delivery completes, the dirty task becomes claimable.
	require.NoError(t, q.Complete(ctx, claimed[0]))
	got, err = q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, dirty.ID, got[0].ID)
}

// An ineligible keyed head (its sibling leased) must not starve the shard: a
// single-slot claim (the worker pool's ClaimBatch) steps past it to eligible
// work behind it, the over-scan never widens the batch beyond the requested
// limit, and the dirty bit still runs once the sibling completes.
func TestClaimStepsPastIneligibleKeyedHead(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))
	ctx := t.Context()

	first := task.New("host.reconcile", "h-1")
	require.NoError(t, q.Enqueue(ctx, first))
	claimed, err := q.Claim(ctx, 0, 1, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	// The dirty-bit sibling becomes the shard head (same priority, older than
	// everything enqueued after it) but is ineligible while h-1 is leased.
	dirty := task.New("host.reconcile", "h-1")
	require.NoError(t, q.Enqueue(ctx, dirty))
	behind := task.New("host.provision", "h-2")
	require.NoError(t, q.Enqueue(ctx, behind))
	alsoBehind := task.New("host.provision", "h-3")
	require.NoError(t, q.Enqueue(ctx, alsoBehind))

	got, err := q.Claim(ctx, 0, 1, time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 1, "an ineligible head must not starve the shard")
	assert.Equal(t, behind.ID, got[0].ID)

	// The over-scan never widens the batch: the next claim takes the next task.
	got, err = q.Claim(ctx, 0, 1, time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, alsoBehind.ID, got[0].ID)

	// The dirty bit was recorded, not lost: it runs once the sibling completes.
	require.NoError(t, q.Complete(ctx, claimed[0]))
	got, err = q.Claim(ctx, 0, 1, time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, dirty.ID, got[0].ID)
}

// EnqueueReconcile coalesces into an existing PENDING row (priority = max,
// available_at = min) instead of inserting a duplicate.
func TestEnqueueReconcileCoalesces(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))
	ctx := t.Context()

	resync := task.New("host.reconcile", "h-1")
	resync.Priority = task.PriorityResync
	resync.AvailableAt = time.Now().Add(time.Hour) // delayed resync
	require.NoError(t, q.EnqueueReconcile(ctx, resync))

	// An edge event for the same key: must merge, raising priority and
	// pulling the ready time in, never a second pending row.
	edge := task.New("host.reconcile", "h-1")
	require.NoError(t, q.EnqueueReconcile(ctx, edge))

	got, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 1, "coalesce must leave exactly one pending row")
	assert.Equal(t, resync.ID, got[0].ID, "the existing row was merged into, not replaced")
	assert.Equal(t, task.PriorityEdge, got[0].Priority, "priority raised to the max of both")

	// While that one is leased, a new EnqueueReconcile inserts the dirty row
	// (no pending sibling to merge into).
	dirty := task.New("host.reconcile", "h-1")
	require.NoError(t, q.EnqueueReconcile(ctx, dirty))
	stats := mustPending(t, q)
	assert.Equal(t, int64(1), stats, "leased sibling does not coalesce; one new pending row")
}

func mustPending(t *testing.T, q *cspanner.TaskQueue) int64 {
	t.Helper()
	stats, err := q.Stats(t.Context())
	require.NoError(t, err)
	return stats.Pending
}

// The audit-F-A1 stall: a keyed task failing back to pending while a dirty-bit
// sibling exists would regenerate reconcile_key and violate the unique index,
// stranding the row leased and poisoning every sweep. Fail must instead merge
// the delivery into the sibling: row deleted, attempts carried forward.
func TestKeyedFailMergesIntoPendingSibling(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))
	ctx := t.Context()

	first := task.New("host.reconcile", "h-1")
	require.NoError(t, q.EnqueueReconcile(ctx, first))
	claimed, err := q.Claim(ctx, 0, 1, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	dirty := task.New("host.reconcile", "h-1")
	require.NoError(t, q.EnqueueReconcile(ctx, dirty))

	require.NoError(t, q.Fail(ctx, claimed[0], errors.New("transient"), time.Hour))
	assertRowGone(t, db, first.ID)
	assert.Equal(t, int64(1), readInt64(t, db, dirty.ID, "attempts"),
		"the failed delivery's attempt survives the merge (dead-letter pressure)")

	got, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 1, "the surviving sibling is claimable")
	assert.Equal(t, dirty.ID, got[0].ID)
	assert.Equal(t, 2, got[0].Attempts, "claim increments from the merged count")
}

// Postpone is the designed requeue path for keyed device.reconcile (launch
// in flight): it must merge like Fail, restoring the pre-claim attempts.
func TestKeyedPostponeMergesIntoPendingSibling(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))
	ctx := t.Context()

	first := task.New("device.reconcile", "d-1")
	require.NoError(t, q.EnqueueReconcile(ctx, first))
	claimed, err := q.Claim(ctx, 0, 1, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	dirty := task.New("device.reconcile", "d-1")
	require.NoError(t, q.EnqueueReconcile(ctx, dirty))

	require.NoError(t, q.Postpone(ctx, claimed[0], "launch in flight", time.Minute))
	assertRowGone(t, db, first.ID)
	assert.Equal(t, int64(0), readInt64(t, db, dirty.ID, "attempts"),
		"postpone burns no attempt through the merge either")
}

// A crashed worker's expired keyed lease must sweep cleanly even when a
// pending sibling exists, taking the merge path instead of a colliding requeue
// that aborts the whole sweep batch.
func TestKeyedSweepMergesIntoPendingSibling(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))
	ctx := t.Context()

	first := task.New("host.reconcile", "h-1")
	require.NoError(t, q.EnqueueReconcile(ctx, first))
	claimed, err := q.Claim(ctx, 0, 1, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	dirty := task.New("host.reconcile", "h-1")
	require.NoError(t, q.EnqueueReconcile(ctx, dirty))

	// Crash: the lease expires without any bookkeeping.
	_, err = db.Client.Apply(ctx, []*spanner.Mutation{
		spanner.UpdateMap("tasks", map[string]any{
			"task_id":      first.ID,
			"lease_expiry": time.Now().Add(-time.Minute),
		}),
	})
	require.NoError(t, err)

	swept, err := q.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, swept)
	assertRowGone(t, db, first.ID)
	assert.Equal(t, int64(1), readInt64(t, db, dirty.ID, "attempts"),
		"the crashed delivery's attempt survives the sweep merge")
}

// An expired keyed lease with NO sibling requeues in place (the plain path).
func TestKeyedSweepRequeuesWithoutSibling(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))
	ctx := t.Context()

	first := task.New("host.reconcile", "h-1")
	require.NoError(t, q.EnqueueReconcile(ctx, first))
	claimed, err := q.Claim(ctx, 0, 1, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	_, err = db.Client.Apply(ctx, []*spanner.Mutation{
		spanner.UpdateMap("tasks", map[string]any{
			"task_id":      first.ID,
			"lease_expiry": time.Now().Add(-time.Minute),
		}),
	})
	require.NoError(t, err)

	swept, err := q.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, swept)
	assert.Equal(t, "pending", readString(t, db, first.ID, "state"))
}

// A leased sibling whose lease already lapsed must not veto the pending row:
// its handler is provably dead (LeaseDuration > HandleTimeout), and blocking
// on it would let a stuck row deadlock its own key.
func TestClaimSkipsOnlyLiveLeasedSiblings(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))
	ctx := t.Context()

	first := task.New("host.reconcile", "h-1")
	require.NoError(t, q.EnqueueReconcile(ctx, first))
	claimed, err := q.Claim(ctx, 0, 1, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	dirty := task.New("host.reconcile", "h-1")
	require.NoError(t, q.EnqueueReconcile(ctx, dirty))

	got, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	assert.Empty(t, got, "live leased sibling blocks the key")

	_, err = db.Client.Apply(ctx, []*spanner.Mutation{
		spanner.UpdateMap("tasks", map[string]any{
			"task_id":      first.ID,
			"lease_expiry": time.Now().Add(-time.Minute),
		}),
	})
	require.NoError(t, err)

	got, err = q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 1, "expired leased sibling no longer blocks the key")
	assert.Equal(t, dirty.ID, got[0].ID)
}

// Admin requeue of a keyed dead-letter whose key already has a pending
// sibling drops the dead-letter and pulls the sibling forward, because going
// back to pending would collide on the unique index.
func TestRequeueFailedKeyedMergesIntoSibling(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))
	ctx := t.Context()

	first := task.New("host.reconcile", "h-1")
	first.MaxAttempts = 1
	require.NoError(t, q.EnqueueReconcile(ctx, first))
	claimed, err := q.Claim(ctx, 0, 1, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, q.Fail(ctx, claimed[0], errors.New("fatal"), 0))
	require.Equal(t, "failed", readString(t, db, first.ID, "state"))

	dirty := task.New("host.reconcile", "h-1")
	dirty.AvailableAt = time.Now().Add(time.Hour)
	require.NoError(t, q.EnqueueReconcile(ctx, dirty))

	require.NoError(t, q.RequeueFailed(ctx, first.ID))
	assertRowGone(t, db, first.ID)

	got, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 1, "the sibling was pulled forward to now")
	assert.Equal(t, dirty.ID, got[0].ID)
}

func assertRowGone(t *testing.T, db *spannertest.TestDB, id string) {
	t.Helper()
	_, err := db.Client.Single().ReadRow(t.Context(), "tasks", spanner.Key{id}, []string{"task_id"})
	require.Equal(t, codes.NotFound, spanner.ErrCode(err), "row %s must be gone", id)
}

// EnqueueResync is the detectors' single filing point: resync priority, an
// optional delay, and routing. Keyed reconcile types get the coalescing
// keyed queue (a second filing merges, no duplicate row), everything else a
// plain enqueue (two filings are two rows; HasActive dedup is the callers'
// guard there).
func TestEnqueueResyncRoutesAndDelays(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))
	ctx := t.Context()

	// Keyed type: two filings coalesce into one pending row.
	require.NoError(t, q.EnqueueResync(ctx, task.TypeHostReconcile, "h-1", 0))
	require.NoError(t, q.EnqueueResync(ctx, task.TypeHostReconcile, "h-1", 0))
	got, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 1, "keyed filings must coalesce")
	assert.Equal(t, task.TypeHostReconcile, got[0].Type)
	assert.Equal(t, "h-1", got[0].ResourceID)
	assert.Equal(t, task.PriorityResync, got[0].Priority)

	// Non-keyed type with a delay: a plain enqueue, not claimable before the
	// delay elapses.
	require.NoError(t, q.EnqueueResync(ctx, task.TypeDeviceCaptureBugreport, "d-1", time.Hour))
	got, err = q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	assert.Empty(t, got, "the delayed filing must not be claimable yet")
	active, err := q.HasActive(ctx, task.TypeDeviceCaptureBugreport, "d-1")
	require.NoError(t, err)
	assert.True(t, active, "the delayed filing is live for HasActive dedup")
}
