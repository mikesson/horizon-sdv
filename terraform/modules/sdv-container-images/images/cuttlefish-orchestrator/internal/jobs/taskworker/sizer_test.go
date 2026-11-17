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

package taskworker

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// sizerConfig is a resolved config whose poll cap makes the two derived
// thresholds easy to read: grow at 3s of claimed-task age, shrink after 1s of
// pool-wide quiet.
func sizerConfig(minW, maxW int) Config {
	return Config{
		Enabled: true, MinWorkers: minW, MaxWorkers: maxW,
		NumShards: 1, ClaimBatch: 1,
		LeaseDuration: 2 * time.Minute, HandleTimeout: time.Minute,
		PollMin: 200 * time.Millisecond, PollMax: time.Second, PollCap: time.Second,
		RetryBase: time.Second, RetryMax: time.Minute,
	}.WithDefaults()
}

// TestSizerGrowsOnlyOnAgedClaims is the signal itself: work claimed inside the
// window a healthy poll explains moves nothing, and work claimed past it adds a
// worker per claim.
//
// It fails if growth ever keys on the mere fact of a claim rather than on its
// age: plant `age >= 0` in observeClaim and the first subtest goes red.
func TestSizerGrowsOnlyOnAgedClaims(t *testing.T) {
	s := newSizer(sizerConfig(8, 64))
	s.start()

	s.observeClaim(s.growAfter - time.Millisecond)
	if got := s.active.Load(); got != 8 {
		t.Errorf("a claim inside the poll window grew the pool to %d, want 8", got)
	}
	for range 3 {
		s.observeClaim(s.growAfter)
	}
	if got := s.active.Load(); got != 11 {
		t.Errorf("three aged claims took the pool to %d, want 11", got)
	}
}

// TestSizerIgnoresRetryAge pins the timestamp the age is measured from. A task
// on its fifth backoff has an old CreatedAt and a fresh AvailableAt, and only
// the second is starvation.
//
// It fails if claimAge is ever switched to CreatedAt.
func TestSizerIgnoresRetryAge(t *testing.T) {
	s := newSizer(sizerConfig(8, 64))
	s.start()

	retried := &task.Task{
		ID:          "retried",
		CreatedAt:   time.Now().Add(-time.Hour),
		AvailableAt: time.Now(),
	}
	s.observeClaim(claimAge([]*task.Task{retried}))
	if got := s.active.Load(); got != 8 {
		t.Errorf("a retried task grew the pool to %d; its backoff is not starvation", got)
	}

	starved := &task.Task{
		ID:          "starved",
		CreatedAt:   time.Now().Add(-time.Hour),
		AvailableAt: time.Now().Add(-s.growAfter),
	}
	s.observeClaim(claimAge([]*task.Task{starved}))
	if got := s.active.Load(); got != 9 {
		t.Errorf("a task eligible for %v grew the pool to %d, want 9", s.growAfter, got)
	}
}

// TestSizerClampsAtCeilingAndFloor holds both ends. Growth stops at the ceiling
// however much aged work arrives, and shrinking stops at the floor however long
// the queue stays empty.
func TestSizerClampsAtCeilingAndFloor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSizer(sizerConfig(2, 4))
		s.start()

		for range 20 {
			s.observeClaim(s.growAfter)
		}
		if got := s.active.Load(); got != 4 {
			t.Errorf("twenty aged claims took the pool to %d, want the ceiling 4", got)
		}

		for range 20 {
			time.Sleep(s.shrinkAfter)
			s.observeIdle()
		}
		if got := s.active.Load(); got != 2 {
			t.Errorf("twenty idle intervals took the pool to %d, want the floor 2", got)
		}
	})
}

// TestSizerShrinkIsDamped is the asymmetry: growth answers one claim, while
// giving a worker back costs a whole interval of pool-wide quiet, and any
// delivery restarts that clock.
//
// It fails if observeIdle ever sheds a worker per probe: drop the quietSince
// comparison and the first two assertions go red.
func TestSizerShrinkIsDamped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSizer(sizerConfig(2, 8))
		s.start()
		for range 4 {
			s.observeClaim(s.growAfter)
		}
		if got := s.active.Load(); got != 6 {
			t.Fatalf("setup: pool at %d, want 6", got)
		}

		// A burst of probes inside one interval sheds nothing.
		for range 10 {
			s.observeIdle()
		}
		if got := s.active.Load(); got != 6 {
			t.Errorf("ten probes inside one interval shed down to %d, want 6", got)
		}

		// A delivery restarts the clock, so the interval that follows it is the
		// one that counts.
		time.Sleep(s.shrinkAfter - time.Millisecond)
		s.observeClaim(0)
		time.Sleep(time.Millisecond)
		s.observeIdle()
		if got := s.active.Load(); got != 6 {
			t.Errorf("a delivery failed to restart the quiet clock: pool at %d, want 6", got)
		}

		// And one full interval of quiet gives exactly one worker back.
		time.Sleep(s.shrinkAfter)
		s.observeIdle()
		s.observeIdle()
		if got := s.active.Load(); got != 5 {
			t.Errorf("one quiet interval shed to %d, want exactly one worker given back (5)", got)
		}
	})
}

// TestSizerParksAndResumes covers the mechanism the width is expressed through:
// a worker outside it blocks in hold, a grow releases it within one claim, and
// stop releases every parked worker so a shutdown can drain the whole set.
//
// It fails if grow ever forgets to broadcast (the resumed count stays 0) or if
// stop does not (the bubble deadlocks and synctest reports it).
func TestSizerParksAndResumes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSizer(sizerConfig(1, 4))
		s.start()

		var mu sync.Mutex
		running := map[int]bool{}
		var wg sync.WaitGroup
		for id := range 4 {
			wg.Go(func() {
				for s.hold(id) {
					mu.Lock()
					running[id] = true
					mu.Unlock()
					// One pass per release; park again until the next grow.
					s.mu.Lock()
					for int64(id) < s.active.Load() && !s.stopped {
						s.cond.Wait()
					}
					s.mu.Unlock()
				}
			})
		}

		synctest.Wait()
		mu.Lock()
		got := len(running)
		mu.Unlock()
		if got != 1 {
			t.Errorf("%d workers ran at a floor of 1", got)
		}

		s.observeClaim(s.growAfter)
		synctest.Wait()
		mu.Lock()
		got = len(running)
		mu.Unlock()
		if got != 2 {
			t.Errorf("%d workers ran after one aged claim, want 2", got)
		}

		// Every worker returns, parked or not; the bubble hangs if one does not.
		s.stop()
		wg.Wait()
	})
}
