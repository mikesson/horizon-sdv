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
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/jobs/taskworker"
)

// longType is a member of the compiled long-occupancy class; shortType stands
// for everything else. Named from the domain rather than invented, because the
// class is a set of real task types and a test that invented one would pass
// while the shipped set was empty.
const (
	longType  = task.TypeDeviceCreateCVD
	shortType = task.TypeHostDeprovision
)

// bulkheadConfig is a pool of exactly width workers, with floor and ceiling
// equal, so the sizer holds still and the budget under test is the one the
// width implies.
func bulkheadConfig(width int) taskworker.Config {
	return taskworker.Config{
		Enabled:       true,
		MinWorkers:    width,
		MaxWorkers:    width,
		NumShards:     1,
		ClaimBatch:    1,
		LeaseDuration: 20 * time.Minute,
		HandleTimeout: 10 * time.Minute,
		PollMin:       200 * time.Millisecond,
		PollMax:       time.Second,
		PollCap:       time.Second,
		RetryBase:     time.Second,
		RetryMax:      time.Minute,
	}
}

// blockingHandler holds every task it is given until the test releases it, which
// is the shape of a CVD create: minutes inside one remote wait.
func blockingHandler(typ string) *fakeHandler {
	return &fakeHandler{typ: typ, fn: func(ctx context.Context, _ *task.Task) error {
		<-ctx.Done()
		return ctx.Err()
	}}
}

// A long-occupancy class at its budget does not get the next worker: the task is
// postponed back to pending, and its handler never runs.
//
// It fails if the admission check is removed: the fourth worker would enter the
// handler, taking the pool to four concurrent creates with nothing left to claim
// the short work behind them.
func TestPoolDefersLongWorkOverItsShare(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &fakeQueue{}
		for i := range 5 {
			enqueueFake(t, q, longType, fmt.Sprintf("long%d", i))
		}
		h := blockingHandler(longType)
		p := newPool(t, bulkheadConfig(4), q, []task.Handler{h})
		p.Start(t.Context())

		synctest.Wait()
		assert.Equal(t, 3, h.count(), "a pool of four admits three long handlers, keeping one worker claiming")
		require.NotEmpty(t, q.postponedRecords(), "the declined tasks go back to pending")
		assert.Contains(t, q.postponedRecords()[0].reason, "share of the pool",
			"the reason must say what declined the task; an operator reads it off the row")
		assert.Positive(t, q.postponedRecords()[0].after, "a deferred task must not come straight back")
		assert.Equal(t, int64(2), p.Stats().Deferred, "both of the tasks over budget are counted")
		assert.Empty(t, q.failedRecords(), "being declined is not a failed attempt")

		p.Stop()
	})
}

// The starvation proof: with every worker the long class may have inside a
// handler, short control work behind it in the queue is still picked up, with
// no fake time advanced at all, so it happens inside one poll interval.
//
// It fails without the bulkhead: all four workers would be inside blocked
// creates and the deprovision behind them would wait out the whole handler hold.
func TestShortWorkRunsWhileLongWorkSaturatesThePool(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &fakeQueue{}
		for i := range 4 {
			enqueueFake(t, q, longType, fmt.Sprintf("long%d", i))
		}
		enqueueFake(t, q, shortType, "teardown") // behind the flood, as a real queue orders it
		long := blockingHandler(longType)
		short := &fakeHandler{typ: shortType}
		p := newPool(t, bulkheadConfig(4), q, []task.Handler{long, short})
		p.Start(t.Context())

		synctest.Wait()
		assert.Equal(t, []string{"teardown"}, q.completedIDs(),
			"the short task must not wait out a long handler's hold")
		assert.Equal(t, 3, long.count(), "and the long class stays at its budget")

		p.Stop()
	})
}

// The slot is given back however the handler leaves: panic, failure, postpone
// or success. A pool one worker wide has a budget of exactly one, so a slot
// leaked on any of those paths defers every task after it and the chain stops.
//
// It fails if the release is moved off the deferred path, or attached to the
// success path alone: the count stops at the leaking task.
func TestBulkheadSlotIsReleasedOnEveryHandlerExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &fakeQueue{}
		for _, id := range []string{"panic", "fail", "wait", "ok"} {
			enqueueFake(t, q, longType, id)
		}
		h := &fakeHandler{typ: longType, fn: func(_ context.Context, tk *task.Task) error {
			switch tk.ID {
			case "panic":
				panic("kaboom")
			case "fail":
				return errors.New("boom")
			case "wait":
				return task.Reschedule(time.Minute, "waiting on host")
			default:
				return nil
			}
		}}
		p := newPool(t, bulkheadConfig(1), q, []task.Handler{h})
		p.Start(t.Context())

		synctest.Wait()
		assert.Equal(t, 4, h.count(), "every task after a leaked slot would be deferred instead of handled")
		assert.Equal(t, []string{"ok"}, q.completedIDs())
		assert.Zero(t, p.Stats().Deferred, "one worker at a time cannot exceed a budget of one")

		p.Stop()
	})
}

// A pool at the deployment's floor, carrying long work up to its budget and
// short work beside it, behaves exactly as it did before the bulkhead existed:
// everything runs, nothing is postponed.
//
// It fails if the reserve is ever widened past a quarter (at a half the sixth
// create is deferred and the pool of eight runs four) or if the budget stops
// tracking the width and starts binding on small deployments.
func TestFloorWidthPoolIsUnchangedWithoutALongFlood(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &fakeQueue{}
		for i := range 6 { // the floor's budget exactly, so nothing is declined
			enqueueFake(t, q, longType, fmt.Sprintf("long%d", i))
		}
		for i := range 2 {
			enqueueFake(t, q, shortType, fmt.Sprintf("short%d", i))
		}
		long := blockingHandler(longType)
		short := &fakeHandler{typ: shortType}
		p := newPool(t, bulkheadConfig(8), q, []task.Handler{long, short})
		p.Start(t.Context())

		synctest.Wait()
		assert.Equal(t, 6, long.count(), "the floor still runs six creates at once")
		assert.Equal(t, 2, short.count())
		assert.Empty(t, q.postponedRecords(), "nothing is deferred while the class is inside its share")
		assert.Zero(t, p.Stats().Deferred)

		p.Stop()
	})
}
