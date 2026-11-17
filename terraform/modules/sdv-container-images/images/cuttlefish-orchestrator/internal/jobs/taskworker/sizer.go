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
	"sync/atomic"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// growAfterPolls is how old a claimed task may be, counted in empty-queue poll
// caps, before the pool reads it as starvation rather than as its own poll
// delay.
//
// A healthy task is routinely claimed old. Nothing in this process enqueued it,
// every worker had just probed when it landed, and the next probe is a whole
// PollCap away, so an age just under one cap says nothing at all. A claim that
// loses its race and re-probes costs a second such wait. Three caps leaves
// margin for the jitter band on top of that, and still starts the pool growing
// well inside one handler's hold.
const growAfterPolls = 3

// idleShrinkPolls is how long the whole pool must go without a delivery before
// it gives one worker back, counted in the same poll caps.
//
// Growth is reactive and shrinking is damped, never the reverse. Part of that is
// the general result (every autoscaler measured against the alternative settled
// here) and part is specific to this signal: growth reads a CLAIM, so a pool at
// its floor with every worker inside a handler observes nothing at all, and a
// worker released early is paid for at the next backlog with a whole handler
// hold of blindness. One cap per worker means the pool sheds a worker only after
// every worker has had its own chance to find work and not, and a fleet that
// went quiet mid-shift still has most of its width when the next burst lands.
const idleShrinkPolls = 1

// sizer holds the pool's active worker count between its configured floor and
// ceiling, from the age of the work the pool claims.
//
// The signal is age, and queue depth would not work. Depth is a level that does not fall when workers are added
// inside one control period, so a depth-driven loop has no fixed point without
// being normalized per worker; the age of a claimed task falls as soon as the
// added workers drain the backlog, which is what closes the loop. Age is also
// free, since every claim already returns the row, where a depth signal would
// have to scan the queue exactly when the queue is deep.
//
// Age is measured from the task's ELIGIBLE moment and never from its creation.
// A task on its fifth backoff, or one postponed waiting on a precondition, has
// an old creation time by design; reading that as starvation would pin the pool
// at its ceiling for as long as anything in the queue was retrying.
//
// It owns no goroutine. Every decision runs on the worker that observed the
// thing it decided from, so there is no loop here to lose to a panic and nothing
// to shut down beyond releasing the workers it parked.
type sizer struct {
	min, max int
	// growAfter is the claimed-task age that reads as starvation; shrinkAfter is
	// the pool-wide quiet that releases one worker.
	growAfter, shrinkAfter time.Duration

	// active is written under mu and read without it: a worker asks "am I still
	// wanted" on every loop, and the gauge asks from a third goroutine.
	active atomic.Int64

	mu   sync.Mutex
	cond *sync.Cond
	// quietSince is when the pool last saw a delivery, or last gave a worker
	// back, whichever is later, so the pool shrinks one worker at a time.
	quietSince time.Time
	stopped    bool
}

// newSizer builds the controller for a resolved config. The pool starts at its
// floor: an instance that has just come up has claimed nothing, so it has no
// evidence for anything wider.
func newSizer(cfg Config) *sizer {
	s := &sizer{
		min:         cfg.MinWorkers,
		max:         cfg.MaxWorkers,
		growAfter:   growAfterPolls * cfg.PollCap,
		shrinkAfter: idleShrinkPolls * cfg.PollCap,
	}
	s.cond = sync.NewCond(&s.mu)
	s.active.Store(int64(cfg.MinWorkers))
	return s
}

// start readies the controller for a run: the floor active, nothing observed
// yet, and no worker held back from a previous run's stop.
func (s *sizer) start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = false
	s.quietSince = time.Now()
	s.active.Store(int64(s.min))
}

// hold parks the worker while the pool is narrower than its ordinal, and
// reports whether the pool is still running.
//
// A parked worker waits on the condition rather than on a timer, so it issues no
// queue read at all while parked and still resumes within one claim of the pool
// deciding it wants another. Workers call it at the top of their loop, so one
// released mid-task finishes that task first.
func (s *sizer) hold(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for !s.stopped && int64(id) >= s.active.Load() {
		s.cond.Wait()
	}
	return !s.stopped
}

// observeClaim records one delivery and widens the pool by a worker when the
// work had been eligible longer than a healthy poll explains.
//
// One worker per aged claim, and the worker it wakes claims immediately: a real
// backlog therefore reaches the ceiling in a chain of claims rather than in a
// chain of handler holds. What it cannot shorten is the FIRST observation: a
// pool whose every worker is inside a handler claims nothing, so growth begins
// when one of them returns.
func (s *sizer) observeClaim(age time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quietSince = time.Now()
	if age < s.growAfter || s.active.Load() >= int64(s.max) {
		return
	}
	s.active.Add(1)
	s.cond.Broadcast()
}

// observeIdle records a probe that found the queue empty, and gives one worker
// back once the whole pool has been quiet for shrinkAfter. The released worker
// needs no wake: it discovers it is outside the width at the top of its next
// loop.
func (s *sizer) observeIdle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.quietSince) < s.shrinkAfter || s.active.Load() <= int64(s.min) {
		return
	}
	s.active.Add(-1)
	s.quietSince = now
}

// stop releases every parked worker, so a shutdown drains the whole set and not
// just the part of it the pool happened to be using.
func (s *sizer) stop() {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	s.cond.Broadcast()
}

// claimAge is how long the oldest task in one delivery had been eligible.
//
// From AvailableAt, which is exactly the eligible moment: a retry's backoff and
// a postponed task's wait both move it forward, so neither reads as starvation
// the way the creation time would. A zero value means the row never round-tripped
// through the queue, and is read as new rather than as ancient.
func claimAge(tasks []*task.Task) time.Duration {
	now := time.Now()
	var oldest time.Duration
	for _, t := range tasks {
		if t.AvailableAt.IsZero() {
			continue
		}
		oldest = max(oldest, now.Sub(t.AvailableAt))
	}
	return oldest
}
