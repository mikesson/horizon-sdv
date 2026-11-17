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
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

func TestTaskStepsRecordResumeClear(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))
	ctx := t.Context()

	tk := task.New("device.create_cvd", "dev-1")
	require.NoError(t, q.Enqueue(ctx, tk))
	claimed, err := q.Claim(ctx, 0, 1, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	delivery := claimed[0]

	// Nothing recorded yet.
	op, err := q.RecordedOperation(ctx, delivery, "cvd_load")
	require.NoError(t, err)
	assert.Empty(t, op)

	// Record, then resume reads it back.
	require.NoError(t, q.RecordOperation(ctx, delivery, "cvd_load", "op-abc"))
	op, err = q.RecordedOperation(ctx, delivery, "cvd_load")
	require.NoError(t, err)
	assert.Equal(t, "op-abc", op)

	// A different step name is independent.
	op, err = q.RecordedOperation(ctx, delivery, "vm_insert")
	require.NoError(t, err)
	assert.Empty(t, op)

	// Re-recording the step (post-clear journey restart): newest wins.
	require.NoError(t, q.RecordOperation(ctx, delivery, "cvd_load", "op-def"))
	op, err = q.RecordedOperation(ctx, delivery, "cvd_load")
	require.NoError(t, err)
	assert.Equal(t, "op-def", op)

	// Clear restarts the journey.
	require.NoError(t, q.ClearOperations(ctx, delivery))
	op, err = q.RecordedOperation(ctx, delivery, "cvd_load")
	require.NoError(t, err)
	assert.Empty(t, op)
}

func TestTaskStepsDieWithTheirTask(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))
	ctx := t.Context()

	tk := task.New("device.create_cvd", "dev-1")
	require.NoError(t, q.Enqueue(ctx, tk))
	claimed, err := q.Claim(ctx, 0, 1, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, q.RecordOperation(ctx, claimed[0], "cvd_load", "op-abc"))

	// Complete deletes the task row; the interleaved journal cascades away.
	require.NoError(t, q.Complete(ctx, claimed[0]))
	op, err := q.RecordedOperation(ctx, claimed[0], "cvd_load")
	require.NoError(t, err)
	assert.Empty(t, op, "journal rows must die with their task")
}

// TestTaskStepsWritesFencedOnDelivery proves the journal cannot be corrupted
// by a zombie: once a stuck delivery's task is swept and re-claimed, its late
// RecordOperation is refused (not appended newest, which the live delivery's
// next read would resume) and its ClearOperations cannot wipe the live
// delivery's records. The read is unfenced on purpose, so the live delivery
// resumes by reading the zombie's pre-sweep record.
func TestTaskStepsWritesFencedOnDelivery(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))
	ctx := t.Context()

	tk := task.New("device.create_cvd", "dev-1")
	require.NoError(t, q.Enqueue(ctx, tk))
	first, err := q.Claim(ctx, 0, 1, time.Hour)
	require.NoError(t, err)
	require.Len(t, first, 1)
	zombie := first[0]

	// The zombie records its operation while it legitimately holds the lease.
	require.NoError(t, q.RecordOperation(ctx, zombie, "cvd_load", "op-zombie"))

	// The worker hangs; its lease lapses and the sweeper reclaims the task.
	_, err = db.Client.Apply(ctx, []*spanner.Mutation{
		spanner.UpdateMap("tasks", map[string]any{
			"task_id":      tk.ID,
			"lease_expiry": time.Now().Add(-time.Hour),
		}),
	})
	require.NoError(t, err)
	swept, err := q.Sweep(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, swept)

	second, err := q.Claim(ctx, 0, 1, time.Hour)
	require.NoError(t, err)
	require.Len(t, second, 1)
	live := second[0]

	// The live delivery resumes the zombie's pre-sweep record (unfenced read),
	// then records the fresh operation it started after a journey restart.
	op, err := q.RecordedOperation(ctx, live, "cvd_load")
	require.NoError(t, err)
	assert.Equal(t, "op-zombie", op, "resume must read the prior delivery's record")
	require.NoError(t, q.RecordOperation(ctx, live, "cvd_load", "op-live"))

	// The zombie wakes and tries to record. It is refused, and the live record
	// still wins the next read.
	err = q.RecordOperation(ctx, zombie, "cvd_load", "op-zombie-late")
	require.Error(t, err, "a swept delivery's record must be fenced out")
	op, err = q.RecordedOperation(ctx, live, "cvd_load")
	require.NoError(t, err)
	assert.Equal(t, "op-live", op, "the zombie's late record must not newest-win")

	// The zombie's clear is fenced too: the live journal survives.
	require.Error(t, q.ClearOperations(ctx, zombie),
		"a swept delivery's clear must be fenced out")
	op, err = q.RecordedOperation(ctx, live, "cvd_load")
	require.NoError(t, err)
	assert.Equal(t, "op-live", op, "the zombie's clear must not wipe the live journal")

	// The live delivery's own writes stay unimpeded.
	require.NoError(t, q.ClearOperations(ctx, live))
	op, err = q.RecordedOperation(ctx, live, "cvd_load")
	require.NoError(t, err)
	assert.Empty(t, op)
}
