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
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/jobs/taskworker"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// probeCounter watches the real queue's discovery so the test below can assert
// its own premise, that the workers were parked when the enqueue landed,
// instead of sleeping and hoping.
type probeCounter struct {
	*cspanner.TaskQueue
	probes atomic.Int64
}

func (p *probeCounter) ClaimableShards(ctx context.Context) ([]int, error) {
	p.probes.Add(1)
	return p.TaskQueue.ClaimableShards(ctx)
}

// The wake is the latency path, measured against the real queue: an enqueue
// that commits in this process is picked up long before the poll comes round.
// The floor is 5 seconds here, so a task handled inside two can only have been
// found by the wake. Unwire the waker and this is the poll-floor case, which
// times out.
func TestWakeBeatsThePollFloor(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	ctx := t.Context()

	const workers = 2
	waker := task.NewWaker(workers)
	q := &probeCounter{TaskQueue: cspanner.NewTaskQueue(db.Client,
		cspanner.WithShards(4), cspanner.WithHolder("worker-wake"), cspanner.WithWaker(waker))}

	h := &fakeHandler{typ: "demo"}
	cfg := taskworker.Config{
		Enabled:       true,
		MinWorkers:    workers,
		MaxWorkers:    workers,
		NumShards:     4,
		ClaimBatch:    1,
		LeaseDuration: 2 * time.Minute,
		HandleTimeout: time.Minute,
		// A floor set far above the wake path.
		PollMin:   5 * time.Second,
		PollMax:   10 * time.Second,
		PollCap:   30 * time.Second,
		RetryBase: time.Second,
		RetryMax:  time.Minute,
	}
	p := newPool(t, cfg, q, []task.Handler{h}, taskworker.WithWaker(waker))
	p.Start(ctx)
	defer p.Stop()

	require.Eventually(t, func() bool { return q.probes.Load() >= workers },
		5*time.Second, 5*time.Millisecond, "both workers looked once and parked")

	start := time.Now()
	require.NoError(t, q.Enqueue(ctx, task.New("demo", "res-wake")))
	require.Eventually(t, func() bool { return p.Stats().Processed == 1 },
		2*time.Second, 5*time.Millisecond, "the wake must not wait for the poll")
	assert.Less(t, time.Since(start), cfg.PollMin,
		"claimed inside the poll floor, so the wake is what found it")
}
