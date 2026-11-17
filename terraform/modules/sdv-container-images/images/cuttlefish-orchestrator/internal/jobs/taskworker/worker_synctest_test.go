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
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/jobs/taskworker"
)

// TestPoolEmptyPollBackoff uses a synctest bubble (fake clock) to verify the
// empty-queue poll loop without real sleeps, and that Stop terminates every
// worker. The bubble would fail if any worker goroutine leaked.
func TestPoolEmptyPollBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &fakeQueue{} // always empty -> the probe names no shard
		cfg := taskworker.Config{
			Enabled:       true,
			MinWorkers:    2,
			MaxWorkers:    2,
			NumShards:     1,
			ClaimBatch:    1,
			LeaseDuration: 2 * time.Minute,
			HandleTimeout: time.Minute,
			PollMin:       200 * time.Millisecond,
			PollMax:       time.Second,
			PollCap:       5 * time.Second,
			RetryBase:     time.Second,
			RetryMax:      time.Minute,
		}
		p := newPool(t, cfg, q, nil)
		p.Start(t.Context())

		// Each worker polls once, then parks on its backoff timer.
		synctest.Wait()
		first := q.probeCount()
		assert.GreaterOrEqual(t, first, cfg.MaxWorkers, "each worker polled at least once")

		// Advancing fake time past several backoff windows drives re-polls.
		time.Sleep(30 * time.Second)
		synctest.Wait()
		assert.Greater(t, q.probeCount(), first, "workers re-poll after backoff")
		assert.Zero(t, q.claimCount(), "and never open a claim over an empty queue")

		// Stop cancels the workers; the bubble ends only if all of them exit.
		p.Stop()
	})
}

// TestPoolWidensOnAgedBacklog drives the elasticity end to end: a backlog whose
// work has been eligible far longer than a poll explains takes the pool from its
// floor to its ceiling, and it does so within claims rather than within handler
// holds: the handler here runs a minute and the whole widening happens first.
//
// It fails if the pool ever stops honoring the claimed age (nothing widens, and
// the tasks drain one handler at a time), or if a parked worker is not released
// by a grow.
func TestPoolWidensOnAgedBacklog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &fakeQueue{}
		for i := range 6 {
			tk := task.New("slow", fmt.Sprintf("res-%d", i))
			tk.ID = fmt.Sprintf("t%d", i)
			// Eligible well before growAfter (3 x PollCap = 3s here).
			tk.AvailableAt = time.Now().Add(-time.Minute)
			require.NoError(t, q.Enqueue(t.Context(), tk))
		}
		h := &fakeHandler{typ: "slow", fn: func(ctx context.Context, _ *task.Task) error {
			select {
			case <-ctx.Done():
			case <-time.After(time.Minute):
			}
			return nil
		}}
		cfg := taskworker.Config{
			Enabled:       true,
			MinWorkers:    1,
			MaxWorkers:    4,
			NumShards:     1,
			ClaimBatch:    1,
			LeaseDuration: 10 * time.Minute,
			HandleTimeout: 5 * time.Minute,
			PollMin:       200 * time.Millisecond,
			PollMax:       time.Second,
			PollCap:       time.Second,
			RetryBase:     time.Second,
			RetryMax:      time.Minute,
		}
		p := newPool(t, cfg, q, []task.Handler{h})
		p.Start(t.Context())

		// No fake time advanced at all: the widening rides the claims themselves,
		// so it is complete before the first handler returns.
		synctest.Wait()
		assert.Equal(t, int64(4), p.Stats().Active, "an aged backlog takes the pool to its ceiling")

		time.Sleep(3 * time.Minute)
		synctest.Wait()
		assert.Len(t, q.completedIDs(), 6, "and the whole backlog drains")
		p.Stop()
	})
}

// TestPoolRestsAtItsFloor is the other half: an instance with nothing to do
// costs exactly what the floor costs, so only the floor's workers ever probe.
//
// It fails if a parked worker polls: the probe count would run with the ceiling
// rather than with the floor.
func TestPoolRestsAtItsFloor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &fakeQueue{} // always empty
		cfg := taskworker.Config{
			Enabled:       true,
			MinWorkers:    1,
			MaxWorkers:    16,
			NumShards:     1,
			ClaimBatch:    1,
			LeaseDuration: 2 * time.Minute,
			HandleTimeout: time.Minute,
			PollMin:       200 * time.Millisecond,
			PollMax:       time.Second,
			PollCap:       time.Second,
			RetryBase:     time.Second,
			RetryMax:      time.Minute,
		}
		p := newPool(t, cfg, q, nil)
		p.Start(t.Context())

		time.Sleep(30 * time.Second)
		synctest.Wait()

		assert.Equal(t, int64(1), p.Stats().Active, "an idle pool sits at its floor")
		// One worker waiting a mean 0.6s reaches ~50 probes in 30s; the ceiling's
		// sixteen would reach ~800, so anything under a hundred can only be the
		// floor doing the polling.
		assert.Less(t, q.probeCount(), 100, "only the floor's workers probe")
		p.Stop()
	})
}

// TestPoolFlagsHandlerOverrunningLease drives a handler that ignores its context
// deadline and runs past the lease. New() guarantees a handler that honors ctx
// cancellation cannot overrun (HandleTimeout < LeaseDuration), so this exercises
// the runtime guard for one that does, which counts the double-delivery risk.
// The task still completes, because the guard observes and does not abort.
func TestPoolFlagsHandlerOverrunningLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &fakeQueue{}
		tk := &task.Task{ID: "slow", Type: "slow", MaxAttempts: 3}
		require.NoError(t, q.Enqueue(t.Context(), tk))
		// Ignores ctx.Done(): sleeps past LeaseDuration (2m) on the fake clock.
		h := &fakeHandler{typ: "slow", fn: func(context.Context, *task.Task) error {
			time.Sleep(3 * time.Minute)
			return nil
		}}
		cfg := taskworker.Config{
			Enabled:       true,
			MinWorkers:    1,
			MaxWorkers:    1,
			NumShards:     1,
			ClaimBatch:    1,
			LeaseDuration: 2 * time.Minute,
			HandleTimeout: time.Minute,
			PollMin:       200 * time.Millisecond,
			PollMax:       time.Second,
			RetryBase:     time.Second,
			RetryMax:      time.Minute,
		}
		p := newPool(t, cfg, q, []task.Handler{h})
		p.Start(t.Context())

		// Fake time carries the worker through the handler's 3m overrun.
		time.Sleep(5 * time.Minute)
		synctest.Wait()

		assert.Equal(t, []string{"slow"}, q.completedIDs(), "the slow task still completes")
		assert.Equal(t, int64(1), p.Stats().Overran, "the lease overrun is flagged")
		p.Stop()
	})
}

// TestPoolRetriesBookkeeping drives a Complete that fails twice before
// succeeding: the fenced bookkeeping writes are idempotent, so the pool
// retries them instead of letting a single Spanner blip strand a finished
// task leased until the sweeper reclaims it at lease expiry.
func TestPoolRetriesBookkeeping(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &fakeQueue{completeErrs: 2}
		tk := &task.Task{ID: "t1", Type: "noop", MaxAttempts: 3}
		require.NoError(t, q.Enqueue(t.Context(), tk))
		h := &fakeHandler{typ: "noop"}
		cfg := taskworker.Config{
			Enabled:       true,
			MinWorkers:    1,
			MaxWorkers:    1,
			NumShards:     1,
			ClaimBatch:    1,
			LeaseDuration: 2 * time.Minute,
			HandleTimeout: time.Minute,
			PollMin:       200 * time.Millisecond,
			PollMax:       time.Second,
			RetryBase:     time.Second,
			RetryMax:      time.Minute,
		}
		p := newPool(t, cfg, q, []task.Handler{h})
		p.Start(t.Context())

		// Fake time carries the worker through the retry backoffs (1s + 2s).
		time.Sleep(5 * time.Second)
		synctest.Wait()

		assert.Equal(t, []string{"t1"}, q.completedIDs(), "task completes despite bookkeeping blips")
		assert.Equal(t, 3, q.completeCallCount(), "two failed attempts plus the success")
		p.Stop()
	})
}
