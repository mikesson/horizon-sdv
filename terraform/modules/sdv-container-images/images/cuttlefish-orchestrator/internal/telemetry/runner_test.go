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

package telemetry

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestRunnerRunsAtStartAndOnTicks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cycles atomic.Int32
		var sawDeadline atomic.Bool
		r := &Runner{
			Interval:   time.Minute,
			RunAtStart: true,
			Cycle: func(ctx context.Context) error {
				if _, ok := ctx.Deadline(); ok {
					sawDeadline.Store(true)
				}
				cycles.Add(1)
				return nil
			},
		}
		r.Start(t.Context())
		r.Start(t.Context()) // idempotent: must not double the loop
		synctest.Wait()
		if got := cycles.Load(); got != 1 {
			t.Fatalf("cycles after start = %d, want 1 (RunAtStart)", got)
		}
		if !sawDeadline.Load() {
			t.Error("cycle context carried no deadline; CycleBudget not applied")
		}

		time.Sleep(2*time.Minute + time.Second)
		synctest.Wait()
		if got := cycles.Load(); got != 3 {
			t.Fatalf("cycles after two ticks = %d, want 3", got)
		}

		r.Stop()
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if got := cycles.Load(); got != 3 {
			t.Errorf("cycles after Stop = %d, want 3 (loop must have exited)", got)
		}
	})
}

func TestRunnerLeaderGateSkipsCycles(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var leader atomic.Bool
		var cycles atomic.Int32
		r := &Runner{
			Interval:    time.Minute,
			RunAtStart:  true,
			LeaderCheck: leader.Load,
			Cycle: func(context.Context) error {
				cycles.Add(1)
				return nil
			},
		}
		r.Start(t.Context())
		defer r.Stop()

		time.Sleep(3 * time.Minute)
		synctest.Wait()
		if got := cycles.Load(); got != 0 {
			t.Fatalf("cycles while not leader = %d, want 0", got)
		}

		leader.Store(true)
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := cycles.Load(); got == 0 {
			t.Error("no cycle ran after acquiring leadership")
		}
	})
}

func TestRunnerStopAbortsInFlightCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		r := &Runner{
			Interval:   time.Hour,
			RunAtStart: true,
			Cycle: func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			},
		}
		r.Start(t.Context())
		<-started
		// Must cancel the in-flight cycle and join; a leaked goroutine or a
		// hang would fail the synctest bubble.
		r.Stop()
		r.Stop() // idempotent when already stopped
	})
}

func TestRunnerDefaultBudgetCancelsOverrunningCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		var canceled atomic.Bool
		r := &Runner{
			Interval:   time.Hour,
			RunAtStart: true,
			Cycle: func(ctx context.Context) error {
				if calls.Add(1) > 1 {
					return nil
				}
				select {
				case <-ctx.Done():
					canceled.Store(true)
					return ctx.Err()
				case <-time.After(90 * time.Minute):
					return nil
				}
			},
		}
		r.Start(t.Context())
		defer r.Stop()

		time.Sleep(time.Hour) // past CycleBudget(time.Hour) = 59m
		synctest.Wait()
		if !canceled.Load() {
			t.Fatal("cycle overran the default budget without being canceled")
		}
	})
}

func TestRunnerNegativeBudgetRunsUnbounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		var finished, canceled atomic.Bool
		r := &Runner{
			Interval:   time.Hour,
			RunAtStart: true,
			Budget:     -1,
			Cycle: func(ctx context.Context) error {
				if calls.Add(1) > 1 {
					return nil
				}
				if _, ok := ctx.Deadline(); ok {
					t.Error("cycle context carries a deadline; Budget < 0 must run unbounded")
				}
				select {
				case <-ctx.Done():
					canceled.Store(true)
					return ctx.Err()
				case <-time.After(90 * time.Minute): // past CycleBudget(time.Hour)
					finished.Store(true)
					return nil
				}
			},
		}
		r.Start(t.Context())
		defer r.Stop()

		time.Sleep(91 * time.Minute)
		synctest.Wait()
		if canceled.Load() {
			t.Fatal("unbounded cycle was canceled before completing")
		}
		if !finished.Load() {
			t.Fatal("unbounded cycle did not run to completion")
		}
	})
}

func TestRunnerExplicitBudgetSetsDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var deadlineIn atomic.Int64
		r := &Runner{
			Interval:   time.Hour,
			RunAtStart: true,
			Budget:     10 * time.Minute,
			Cycle: func(ctx context.Context) error {
				if d, ok := ctx.Deadline(); ok {
					deadlineIn.Store(int64(time.Until(d)))
				}
				return nil
			},
		}
		r.Start(t.Context())
		defer r.Stop()

		synctest.Wait()
		if got := time.Duration(deadlineIn.Load()); got != 10*time.Minute {
			t.Fatalf("cycle deadline = %v from now, want the explicit 10m budget", got)
		}
	})
}

func TestRunnerStopWithoutStartIsNoOp(t *testing.T) {
	t.Parallel()
	r := &Runner{Interval: time.Minute, Cycle: func(context.Context) error { return nil }}
	r.Stop()
}

// noJitter makes retry delays exact, so a test can name the times cycles run
// at.
func noJitter(d time.Duration) time.Duration { return d }

// callTimes runs r for run and returns how long after Start each cycle began.
// fail reports whether the nth call, counting from 1, fails.
func callTimes(t *testing.T, r *Runner, fail func(n int) bool, run time.Duration) []time.Duration {
	t.Helper()
	start := time.Now()
	var mu sync.Mutex
	var calls []time.Duration
	r.Cycle = func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, time.Since(start))
		if fail(len(calls)) {
			return errors.New("compute engine refused the read")
		}
		return nil
	}
	r.Start(t.Context())
	time.Sleep(run)
	synctest.Wait()
	r.Stop()
	mu.Lock()
	defer mu.Unlock()
	return slices.Clone(calls)
}

// A failed cycle is retried RetryAfterFailure later, and the first success
// restores the full Interval. Without the ticker reset the retry is a whole
// Interval away.
func TestRunnerRetriesAFailedCycleEarly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &Runner{
			Interval:          time.Hour,
			RetryAfterFailure: 30 * time.Second,
			RunAtStart:        true,
			jitter:            noJitter,
		}
		got := callTimes(t, r, func(n int) bool { return n == 2 }, 2*time.Hour+time.Minute)
		want := []time.Duration{0, time.Hour, time.Hour + 30*time.Second, 2*time.Hour + 30*time.Second}
		if !slices.Equal(got, want) {
			t.Errorf("cycles ran at %v, want %v", got, want)
		}
	})
}

// The start cycle is the one a fresh install fails, so the retry must cover
// it too, although the ticker is created after it.
func TestRunnerRetriesAFailedStartCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &Runner{
			Interval:          time.Hour,
			RetryAfterFailure: 30 * time.Second,
			RunAtStart:        true,
			jitter:            noJitter,
		}
		got := callTimes(t, r, func(n int) bool { return n == 1 }, time.Hour+time.Minute)
		want := []time.Duration{0, 30 * time.Second, time.Hour + 30*time.Second}
		if !slices.Equal(got, want) {
			t.Errorf("cycles ran at %v, want %v", got, want)
		}
	})
}

// Each further failure doubles the delay, and the delay never exceeds
// Interval.
func TestRunnerRetryDelayDoublesUpToInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &Runner{
			Interval:          2 * time.Minute,
			RetryAfterFailure: 30 * time.Second,
			RunAtStart:        true,
			jitter:            noJitter,
		}
		got := callTimes(t, r, func(int) bool { return true }, 6*time.Minute)
		// Delays of 30s, 60s and 120s, then 120s again at the cap.
		want := []time.Duration{0, 30 * time.Second, 90 * time.Second, 210 * time.Second, 330 * time.Second}
		if !slices.Equal(got, want) {
			t.Errorf("cycles ran at %v, want %v", got, want)
		}
	})
}

// A Partial cycle published, so it counts as success for staleness, but it
// still takes the short retry.
func TestRunnerRetriesAPartialCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &Runner{
			Interval:          time.Hour,
			RetryAfterFailure: 30 * time.Second,
			RunAtStart:        true,
			jitter:            noJitter,
		}
		var calls atomic.Int32
		r.Cycle = func(context.Context) error {
			if calls.Add(1) == 1 {
				return Partial(errors.New("one zone did not answer"))
			}
			return nil
		}
		r.Start(t.Context())
		defer r.Stop()
		time.Sleep(31 * time.Second)
		synctest.Wait()
		if got := calls.Load(); got != 2 {
			t.Errorf("cycles 31s after a partial start cycle = %d, want 2", got)
		}
	})
}

// Without RetryAfterFailure a failed cycle waits the whole Interval, so the
// loops that did not opt in keep their schedule.
func TestRunnerWithoutRetryKeepsInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &Runner{Interval: time.Hour, RunAtStart: true}
		got := callTimes(t, r, func(int) bool { return true }, time.Hour+time.Minute)
		want := []time.Duration{0, time.Hour}
		if !slices.Equal(got, want) {
			t.Errorf("cycles ran at %v, want %v", got, want)
		}
	})
}

// A cycle that runs past its Budget fails with a context error, and that is
// a failure to retry. Only the loop's own context ending is shutdown.
func TestRunnerRetriesACycleThatHitItsBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &Runner{
			Interval:          time.Hour,
			RetryAfterFailure: 30 * time.Second,
			Budget:            time.Second,
			RunAtStart:        true,
			jitter:            noJitter,
		}
		var calls atomic.Int32
		r.Cycle = func(ctx context.Context) error {
			if calls.Add(1) == 1 {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}
		r.Start(t.Context())
		defer r.Stop()
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := calls.Load(); got != 2 {
			t.Errorf("cycles a minute after a start cycle hit its budget = %d, want 2", got)
		}
	})
}
