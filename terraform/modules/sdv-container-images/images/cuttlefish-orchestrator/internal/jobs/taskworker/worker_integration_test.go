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

package taskworker_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/jobs/taskworker"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// TestPoolDrainsRealSpannerQueue exercises the full claim -> handle -> complete
// path against the Spanner-backed queue on the emulator.
func TestPoolDrainsRealSpannerQueue(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-int"))
	ctx := t.Context()

	const total = 3
	for i := range total {
		tk := task.New("demo", fmt.Sprintf("res-%d", i))
		tk.ID = fmt.Sprintf("it-%d", i)
		require.NoError(t, q.Enqueue(ctx, tk))
	}

	h := &fakeHandler{typ: "demo"}
	cfg := taskworker.Config{
		Enabled:       true,
		MinWorkers:    2,
		MaxWorkers:    2,
		NumShards:     1,
		ClaimBatch:    2,
		LeaseDuration: 2 * time.Minute,
		HandleTimeout: time.Minute,
		PollMin:       5 * time.Millisecond,
		PollMax:       20 * time.Millisecond,
		RetryBase:     5 * time.Millisecond,
		RetryMax:      time.Second,
	}
	p := newPool(t, cfg, q, []task.Handler{h})

	p.Start(ctx)
	require.Eventually(t, func() bool { return p.Stats().Processed == total },
		5*time.Second, 10*time.Millisecond)
	p.Stop()

	assert.Equal(t, total, h.count())

	// Every task was completed (deleted): the queue is now empty.
	remaining, err := q.Claim(ctx, 0, 10, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, remaining)
}
