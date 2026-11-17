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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

func TestTaskQueueAdminStatsAndDeadLetter(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	const numShards = 4
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(numShards), cspanner.WithHolder("w"))
	ctx := t.Context()

	enqueue(t, q, "p1", time.Now())
	enqueue(t, q, "p2", time.Now())
	// cleanup_cvd is a requeue-able type (no resource-status gate); the
	// terminal-resource types are covered below.
	dl := task.New(task.TypeDeviceCleanupCVD, "res-dl")
	dl.ID = "dl1"
	dl.MaxAttempts = 1
	require.NoError(t, q.Enqueue(ctx, dl))

	// Dead-letter dl1: claim (attempts -> 1, exhausted) then fail.
	claimed, err := q.Claim(ctx, task.ShardFor("dl1", numShards), 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, q.Fail(ctx, claimed[0], errors.New("boom"), time.Hour))

	stats, err := q.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), stats.Pending, "p1 + p2")
	assert.Equal(t, int64(1), stats.Failed, "dl1")
	assert.NotEmpty(t, stats.PendingByShard)

	failed, err := q.ListFailed(ctx, 10, "")
	require.NoError(t, err)
	require.Len(t, failed, 1)
	assert.Equal(t, "dl1", failed[0].ID)
	assert.Contains(t, failed[0].LastError, "boom")

	// The resource filter narrows to one resource's dead letters.
	failed, err = q.ListFailed(ctx, 10, "res-dl")
	require.NoError(t, err)
	require.Len(t, failed, 1)
	failed, err = q.ListFailed(ctx, 10, "other-resource")
	require.NoError(t, err)
	assert.Empty(t, failed)

	// Requeue clears the dead-letter and resets attempts.
	require.NoError(t, q.RequeueFailed(ctx, "dl1"))
	failed, err = q.ListFailed(ctx, 10, "")
	require.NoError(t, err)
	assert.Empty(t, failed)
	reclaimed, err := q.Claim(ctx, task.ShardFor("dl1", numShards), 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	assert.Equal(t, 1, reclaimed[0].Attempts, "attempts reset to 0, then +1 on the re-claim")

	// Requeue on a non-dead-lettered task is an error.
	require.Error(t, q.RequeueFailed(ctx, "dl1"), "dl1 is leased now, not failed")

	// DeleteTask force-removes a task.
	require.NoError(t, q.DeleteTask(ctx, "p1"))
	stats, err = q.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), stats.Pending, "p2 remains (p1 deleted, dl1 leased)")
}

// Requeueing a dead-lettered provision/create task is rejected, because its
// resource is already terminal, so the handler would claim, no-op, and
// complete, looking retried while doing nothing.
func TestTaskQueueRequeueRejectsTerminalResourceTypes(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	const numShards = 4
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(numShards), cspanner.WithHolder("w"))
	ctx := t.Context()

	dl := task.New(task.TypeHostProvision, "host-x")
	dl.ID = "dlp"
	dl.MaxAttempts = 1
	require.NoError(t, q.Enqueue(ctx, dl))
	claimed, err := q.Claim(ctx, task.ShardFor("dlp", numShards), 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, q.Fail(ctx, claimed[0], errors.New("boom"), time.Hour))

	err = q.RequeueFailed(ctx, "dlp")
	require.ErrorIs(t, err, task.ErrRequeueLeadsNowhere)

	failed, err := q.ListFailed(ctx, 10, "")
	require.NoError(t, err)
	require.Len(t, failed, 1, "rejected requeue leaves the dead-letter for the Retry API path")
}

// An operation-backed type is refused for a DIFFERENT reason than the terminal
// types above, which is why it has its own sentinel: requeue would not no-op, it
// would ACTUATE, with attempts reset to zero (so powerwash's MaxAttempts: 1
// protection is gone) and the operation already done, so its exclusive_key is
// NULL and admission cannot see the work. A concurrent user powerwash would be
// admitted alongside it.
//
// Covers every operation-backed type, the non-destructive ones included: a
// requeued bugreport-shaped verb would capture again and have its terminal
// write refused by the CAS, so the operator presses a button that reports
// success and changes nothing anyone can see. The list is derived from the
// operation registry, so it cannot drift from the verbs that actually produce
// handles.
func TestTaskQueueRequeueRejectsOperationBackedTypes(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	const numShards = 4
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(numShards), cspanner.WithHolder("w"))
	ctx := t.Context()

	types := operation.TaskTypes()
	require.NotEmpty(t, types, "the registry must name at least one operation-backed type")

	for i, taskType := range types {
		id := fmt.Sprintf("dlop%d", i)
		dl := task.New(taskType, "dev-x")
		dl.ID = id
		dl.MaxAttempts = 1
		require.NoError(t, q.Enqueue(ctx, dl))
		claimed, err := q.Claim(ctx, task.ShardFor(id, numShards), 10, time.Hour)
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		require.NoError(t, q.Fail(ctx, claimed[0], errors.New("host unreachable"), time.Hour))

		err = q.RequeueFailed(ctx, id)
		require.ErrorIs(t, err, task.ErrRequeueUnsafe,
			"%s must be refused with its own sentinel, not the no-op one", taskType)
		require.NotErrorIs(t, err, task.ErrRequeueLeadsNowhere,
			"%s would not no-op. It would actuate, so that message would be a lie to the operator", taskType)
	}
}

// A DLQ requeue restarts the journey, so journaled steps from the dead one
// must not survive. A fresh claim would resume polling a long-dead remote
// operation.
func TestTaskQueueRequeueClearsStepJournal(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	const numShards = 4
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(numShards), cspanner.WithHolder("w"))
	ctx := t.Context()

	dl := task.New(task.TypeDeviceCleanupCVD, "res-j")
	dl.ID = "dlj"
	dl.MaxAttempts = 1
	require.NoError(t, q.Enqueue(ctx, dl))
	claimed, err := q.Claim(ctx, task.ShardFor("dlj", numShards), 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, q.RecordOperation(ctx, claimed[0], "cvd_load", "op-dead"))
	require.NoError(t, q.Fail(ctx, claimed[0], errors.New("boom"), time.Hour))

	require.NoError(t, q.RequeueFailed(ctx, "dlj"))
	op, err := q.RecordedOperation(ctx, claimed[0], "cvd_load")
	require.NoError(t, err)
	assert.Empty(t, op, "requeue must clear the dead journey's journal")
}

// HasActive distinguishes a live (pending or leased) task from none: the
// cross-loop wait in device create keys off it.
func TestTaskQueueHasActive(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	const numShards = 4
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(numShards), cspanner.WithHolder("w"))
	ctx := t.Context()

	active, err := q.HasActive(ctx, task.TypeHostProvision, "host-a")
	require.NoError(t, err)
	assert.False(t, active, "nothing enqueued yet")

	tk := task.New(task.TypeHostProvision, "host-a")
	tk.ID = "prov-a"
	require.NoError(t, q.Enqueue(ctx, tk))

	active, err = q.HasActive(ctx, task.TypeHostProvision, "host-a")
	require.NoError(t, err)
	assert.True(t, active, "pending counts as active")
	active, err = q.HasActive(ctx, task.TypeDeviceCreateCVD, "host-a")
	require.NoError(t, err)
	assert.False(t, active, "different type does not match")

	claimed, err := q.Claim(ctx, task.ShardFor("prov-a", numShards), 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	active, err = q.HasActive(ctx, task.TypeHostProvision, "host-a")
	require.NoError(t, err)
	assert.True(t, active, "leased counts as active")

	require.NoError(t, q.Complete(ctx, claimed[0]))
	active, err = q.HasActive(ctx, task.TypeHostProvision, "host-a")
	require.NoError(t, err)
	assert.False(t, active, "completed tasks are deleted; no live attempt remains")
}

func TestTaskQueueListLiveAndCountByTypeState(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	const numShards = 4
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(numShards), cspanner.WithHolder("w"))
	ctx := t.Context()

	// One leased provision, one dead-lettered cleanup, two pending creates
	// (one delayed into the future): every state and two types. Claims happen
	// while their target is the only claimable task, because shards are
	// hash-assigned and a batched claim would otherwise sweep up same-shard
	// neighbors.
	prov := task.New(task.TypeHostProvision, "host-lv")
	prov.ID = "live-l1"
	require.NoError(t, q.Enqueue(ctx, prov))
	claimed, err := q.Claim(ctx, task.ShardFor("live-l1", numShards), 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	dl := task.New(task.TypeDeviceCleanupCVD, "res-lv")
	dl.ID = "live-dl"
	dl.MaxAttempts = 1
	require.NoError(t, q.Enqueue(ctx, dl))
	dlClaimed, err := q.Claim(ctx, task.ShardFor("live-dl", numShards), 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, dlClaimed, 1)
	require.NoError(t, q.Fail(ctx, dlClaimed[0], errors.New("boom"), time.Hour))
	enqueue(t, q, "live-p1", time.Now())
	enqueue(t, q, "live-p2", time.Now().Add(time.Hour))

	live, err := q.ListLive(ctx, 10)
	require.NoError(t, err)
	require.Len(t, live, 3, "pending + leased; the dead-letter is excluded")
	assert.Equal(t, "live-l1", live[0].ID, "leased sorts before pending")
	assert.Equal(t, task.StateLeased, live[0].State)
	assert.Equal(t, "w", live[0].LeaseHolder)
	assert.False(t, live[0].LeaseExpiry.IsZero(), "leased task carries its lease expiry")
	assert.Equal(t, 1, live[0].Attempts)
	assert.Equal(t, "live-p1", live[1].ID, "pending ordered by available_at")
	assert.Equal(t, "live-p2", live[2].ID)
	assert.True(t, live[2].LeaseExpiry.IsZero(), "pending task has no lease")

	counts, err := q.CountByTypeState(ctx)
	require.NoError(t, err)
	got := map[string]int64{}
	for _, c := range counts {
		got[c.Type+"/"+string(c.State)] = c.Count
	}
	assert.Equal(t, map[string]int64{
		"device.create_cvd/pending": 2,
		"host.provision/leased":     1,
		"device.cleanup_cvd/failed": 1,
	}, got)
}
