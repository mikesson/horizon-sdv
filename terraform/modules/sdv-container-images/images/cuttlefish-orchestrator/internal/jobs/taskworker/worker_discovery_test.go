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
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/jobs/taskworker"
)

// idleConfig is the shape these tests reason about: a floor band far shorter
// than the cap, so a stretched delay is unmistakable on the fake clock.
func idleConfig() taskworker.Config {
	return taskworker.Config{
		Enabled:       true,
		MinWorkers:    2,
		MaxWorkers:    2,
		NumShards:     4,
		ClaimBatch:    1,
		LeaseDuration: 2 * time.Minute,
		HandleTimeout: time.Minute,
		PollMin:       200 * time.Millisecond,
		PollMax:       time.Second,
		PollCap:       30 * time.Second,
		RetryBase:     time.Second,
		RetryMax:      time.Minute,
	}
}

// The poll floor is the delivery guarantee, and it must hold with the wake
// path absent entirely: no waker wired, nothing to signal. A task enqueued
// while every worker is parked at the stretched cap is still handled, inside
// one cap. This is the promise that lets the wake be lossy: the producer of
// most work is another instance, which can never wake this one.
func TestPoolDeliversWithNoWakerAtAll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &fakeQueue{}
		cfg := idleConfig()
		h := &fakeHandler{typ: "demo"}
		p := newPool(t, cfg, q, []task.Handler{h}) // no WithWaker, on purpose
		p.Start(t.Context())

		// Long enough for both workers to reach the cap and sit on it.
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		require.Empty(t, q.completedIDs(), "nothing was enqueued yet")

		enqueueFake(t, q, "demo", "late")
		deadline := time.Now().Add(cfg.PollCap)
		for time.Now().Before(deadline) && len(q.completedIDs()) == 0 {
			time.Sleep(100 * time.Millisecond)
			synctest.Wait()
		}
		assert.Equal(t, []string{"late"}, q.completedIDs(),
			"polling alone must find the task within one cap")
		p.Stop()
	})
}

// The idle delay stretches: an empty queue is asked about on the order of the
// cap, not the floor band. Without the stretch the same window would cost
// hundreds of looks per worker. The count is the assertion, because "it still
// works" is true either way.
func TestPoolStretchesTheIdlePollTowardTheCap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &fakeQueue{}
		cfg := idleConfig()
		p := newPool(t, cfg, q, nil)
		p.Start(t.Context())

		const idle = 10 * time.Minute
		time.Sleep(idle)
		synctest.Wait()
		p.Stop()

		// At the floor band alone (mean 600ms) two workers would look ~2000
		// times; capped at 30s (mean ~15s) they look a few dozen.
		floorLooks := int(idle/((cfg.PollMin+cfg.PollMax)/2)) * cfg.MaxWorkers
		assert.Less(t, q.probeCount(), floorLooks/10,
			"the empty-queue delay must stretch, not stay at the floor")
		assert.Positive(t, q.probeCount(), "an idle pool still looks")
		assert.Zero(t, q.claimCount(), "an empty queue opens no claim transaction")
	})
}

// The probe gates the claim. While discovery answers "nothing anywhere", no
// read-write claim may be opened however much is actually pending. That
// gating is the entire idle-commit saving, and a pool that claimed anyway
// would keep the commits while adding the probe on top.
func TestPoolOpensNoClaimWhileTheProbeIsEmpty(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &fakeQueue{blindProbe: true}
		for i := range 5 {
			enqueueFake(t, q, "demo", string(rune('a'+i)))
		}
		h := &fakeHandler{typ: "demo"}
		p := newPool(t, idleConfig(), q, []task.Handler{h})
		p.Start(t.Context())

		time.Sleep(2 * time.Minute)
		synctest.Wait()
		p.Stop()

		assert.Positive(t, q.probeCount(), "the pool did look")
		assert.Zero(t, q.claimCount(), "a blind probe must hold the claim shut")
		assert.Zero(t, h.count(), "and nothing is handled while it does")
	})
}

// A wake collapses the stretched delay back to the floor. The pool is parked at
// the cap; one signal puts it back to work without waiting the cap out.
func TestPoolWakeCollapsesTheStretchedDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &fakeQueue{}
		cfg := idleConfig()
		waker := task.NewWaker(cfg.MaxWorkers)
		h := &fakeHandler{typ: "demo"}
		p := newPool(t, cfg, q, []task.Handler{h}, taskworker.WithWaker(waker))
		p.Start(t.Context())

		time.Sleep(5 * time.Minute) // park both workers at the cap
		synctest.Wait()

		enqueueFake(t, q, "demo", "urgent")
		waker.Wake()
		// One floor band is the budget: anything longer means the worker waited
		// out its stretched timer rather than the wake.
		time.Sleep(cfg.PollMax)
		synctest.Wait()
		assert.Equal(t, []string{"urgent"}, q.completedIDs())
		p.Stop()
	})
}

// A wake nobody is listening for is discarded, never buffered without bound and
// never blocking the producer: the enqueue path calls this on every commit.
func TestWakerIsLossyAndNeverBlocks(t *testing.T) {
	t.Parallel()
	w := task.NewWaker(2)
	for range 100 {
		w.Wake()
	}
	assert.Len(t, w.C(), 2, "the buffer bounds what a burst can queue")

	var nilWaker *task.Waker
	nilWaker.Wake() // must not panic
	assert.Nil(t, nilWaker.C(), "a nil waker never fires, so the pool polls")
}
