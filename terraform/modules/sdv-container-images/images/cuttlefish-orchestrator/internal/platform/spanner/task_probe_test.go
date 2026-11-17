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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// The probe must name every shard that holds claimable work and only those:
// naming a shard that has nothing costs a wasted read-write claim, and missing
// one that has work strands it until something else looks.
func TestClaimableShardsNamesShardsHoldingWork(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	const shards = 16
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(shards), cspanner.WithHolder("worker-a"))
	ctx := t.Context()

	got, err := q.ClaimableShards(ctx)
	require.NoError(t, err)
	assert.Empty(t, got, "an empty queue has no claimable shard")

	// Enqueue until two distinct shards are occupied; ShardFor hashes the
	// generated ID, so which ones is not ours to choose.
	occupied := map[int]bool{}
	for len(occupied) < 2 {
		tk := task.New("demo", "res")
		require.NoError(t, q.Enqueue(ctx, tk))
		occupied[tk.Shard] = true
	}

	got, err = q.ClaimableShards(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, keys(occupied), got)
}

// A task that is not claimable yet must not show up: a delayed retry, and a
// task already leased by another worker, are both invisible to the probe for
// the same reason the claim skips them.
func TestClaimableShardsSkipsDelayedAndLeased(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))
	ctx := t.Context()

	delayed := task.New("demo", "later")
	delayed.AvailableAt = time.Now().Add(time.Hour)
	require.NoError(t, q.Enqueue(ctx, delayed))

	got, err := q.ClaimableShards(ctx)
	require.NoError(t, err)
	assert.Empty(t, got, "a task available in an hour is not claimable now")

	ready := task.New("demo", "now")
	require.NoError(t, q.Enqueue(ctx, ready))
	got, err = q.ClaimableShards(ctx)
	require.NoError(t, err)
	assert.Equal(t, []int{0}, got)

	claimed, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	got, err = q.ClaimableShards(ctx)
	require.NoError(t, err)
	assert.Empty(t, got, "a leased task is nobody else's to claim")
}

func keys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
