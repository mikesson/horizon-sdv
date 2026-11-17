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
	"math/rand/v2"
	"sync"
	"time"
)

// Runner drives one periodic background loop with the control-plane lifecycle
// every job shares: a leadership gate before each cycle, a CycleBudget timeout
// around it, LoopMetrics recording after it, and a cancel-and-join Stop.
//
// Set the exported fields before Start; they must not change afterward. The
// zero value of LeaderCheck means "always run" and of Metrics "unmetered".
// Cycle errors are not logged here. Jobs log their own failures with domain
// context; the error only marks the cycle unsuccessful for the staleness age.
type Runner struct {
	// Interval is the tick period. Must be positive by Start time (callers
	// default their configs); it also sizes the per-cycle CycleBudget.
	Interval time.Duration
	// Cycle runs one iteration; nil error resets the staleness age, and so
	// does an error from Partial.
	Cycle func(ctx context.Context) error
	// LeaderCheck gates cycles: while it returns false the tick is skipped
	// (and no cycle is recorded). Nil runs every cycle.
	LeaderCheck func() bool
	// Metrics, when set, records every executed cycle.
	Metrics *LoopMetrics
	// RunAtStart runs one cycle immediately on Start, before the first tick.
	// The initial cycle runs inside the loop goroutine, so a slow dependency
	// cannot block startup.
	RunAtStart bool
	// Budget bounds each cycle: zero applies the default CycleBudget(Interval),
	// negative runs the cycle unbounded (only the loop context cancels it),
	// positive is an explicit per-cycle timeout. Unbounded is for loops whose
	// cycles legitimately outlast their interval (a full catalog crawl): the
	// serial loop means such a cycle delays, never overlaps, the next tick,
	// whereas a budget would cancel it mid-pass every tick and livelock.
	Budget time.Duration
	// RetryAfterFailure, when positive, runs the next cycle this soon after a
	// failed or partial one, the RunAtStart cycle included, instead of a whole
	// Interval later. Each further failure doubles the delay up to Interval,
	// and a successful cycle restores Interval. Zero keeps the fixed Interval.
	// It suits a slow loop whose first read can fail on a fresh install. A loop
	// that ticks every minute gains little from it, and a retry only adds load
	// to a dependency that is already failing.
	RetryAfterFailure time.Duration

	// jitter spreads a retry delay so instances that failed together do not
	// retry together. Nil applies plus or minus 20%; tests set it.
	jitter func(time.Duration) time.Duration

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// Start launches the loop goroutine. It returns immediately and is a no-op
// while already running.
func (r *Runner) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.running = true
	r.wg.Go(func() { r.loop(runCtx) })
}

// Stop cancels the loop, aborting an in-flight cycle, and waits for the
// goroutine to exit. A no-op when not running.
func (r *Runner) Stop() {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	r.running = false
	cancel := r.cancel
	r.cancel = nil
	r.mu.Unlock()

	cancel()
	r.wg.Wait()
}

func (r *Runner) loop(ctx context.Context) {
	// failures counts consecutive failed cycles while a retry is pending. The
	// ticker is reset only when that count changes, so a loop that keeps
	// succeeding keeps its phase.
	failures := 0
	if r.RunAtStart {
		failures = r.failuresAfter(r.runCycle(ctx), failures)
	}
	ticker := time.NewTicker(r.period(failures))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			before := failures
			failures = r.failuresAfter(r.runCycle(ctx), failures)
			// Shutdown is the loop's own context ending. A cycle that hit its
			// Budget deadline also returns a context error, and that one is a
			// real failure to retry.
			if ctx.Err() != nil {
				return
			}
			if failures > 0 || before > 0 {
				ticker.Reset(r.period(failures))
			}
		}
	}
}

// failuresAfter returns the consecutive-failure count after a cycle with the
// given outcome. Without RetryAfterFailure it stays zero, so the ticker is
// never reset.
func (r *Runner) failuresAfter(outcome cycleOutcome, failures int) int {
	if r.RetryAfterFailure <= 0 || outcome != cycleFailed {
		return 0
	}
	return failures + 1
}

// period returns the delay to the next cycle after the given number of
// consecutive failures: Interval with none, otherwise RetryAfterFailure
// doubled per further failure, capped at Interval and jittered.
func (r *Runner) period(failures int) time.Duration {
	if failures == 0 {
		return r.Interval
	}
	delay := r.RetryAfterFailure
	for range failures - 1 {
		if delay >= r.Interval {
			break
		}
		delay *= 2
	}
	delay = min(delay, r.Interval)
	if r.jitter != nil {
		return r.jitter(delay)
	}
	return time.Duration(float64(delay) * (0.8 + 0.4*rand.Float64())) //nolint:gosec // Retry spreading, not security.
}

// cycleOutcome is what one runCycle call did, as far as scheduling cares.
type cycleOutcome int

const (
	// cycleSkipped is a cycle the leadership gate did not run. Another
	// instance holds the lease, which is not a failure to retry.
	cycleSkipped cycleOutcome = iota
	cycleSucceeded
	// cycleFailed covers both an error and a Partial result.
	cycleFailed
)

// runCycle executes one gated, budgeted, recorded cycle.
func (r *Runner) runCycle(ctx context.Context) cycleOutcome {
	if r.LeaderCheck != nil && !r.LeaderCheck() {
		return cycleSkipped // another instance holds this job's lease
	}
	budget := r.Budget
	if budget == 0 {
		budget = CycleBudget(r.Interval)
	}
	if budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}
	start := time.Now()
	err := r.Cycle(ctx)
	if r.Metrics != nil {
		r.Metrics.RecordCycle(ctx, start, err == nil || IsPartial(err))
	}
	if err != nil {
		return cycleFailed
	}
	return cycleSucceeded
}

// partialError marks a cycle that published what it read but missed part of
// it.
type partialError struct{ err error }

func (e *partialError) Error() string { return e.err.Error() }
func (e *partialError) Unwrap() error { return e.err }

// Partial marks err as a partial cycle: the cycle published a result, so the
// staleness age resets, but part of the read failed, so a Runner with
// RetryAfterFailure retries it early. Partial(nil) is nil.
func Partial(err error) error {
	if err == nil {
		return nil
	}
	return &partialError{err: err}
}

// IsPartial reports whether err came from Partial.
func IsPartial(err error) bool {
	_, ok := errors.AsType[*partialError](err)
	return ok
}
