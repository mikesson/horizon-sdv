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

package main

import (
	"context"
	"math"
	"sync/atomic"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// histBuckets spans ~100µs to ~14min in powers of two; anything larger lands in
// the final bucket.
const (
	histBuckets = 24
	histBase    = 100 * time.Microsecond
)

// hist is a concurrent log2-bucketed latency histogram. Snapshots are plain
// data, mergeable across instances by elementwise addition.
type hist struct {
	buckets [histBuckets]atomic.Int64
	count   atomic.Int64
	sumNs   atomic.Int64
}

func (h *hist) observe(d time.Duration) {
	if d < 0 {
		d = 0
	}
	idx := 0
	for v := d / histBase; v > 0 && idx < histBuckets-1; v >>= 1 {
		idx++
	}
	h.buckets[idx].Add(1)
	h.count.Add(1)
	h.sumNs.Add(int64(d))
}

func (h *hist) snapshot() histSnap {
	var s histSnap
	s.Count = h.count.Load()
	s.SumNs = h.sumNs.Load()
	for i := range h.buckets {
		s.Buckets[i] = h.buckets[i].Load()
	}
	return s
}

// histSnap is the serializable form of a hist.
type histSnap struct {
	Buckets [histBuckets]int64 `json:"buckets"`
	Count   int64              `json:"count"`
	SumNs   int64              `json:"sum_ns"`
}

func (s *histSnap) merge(o histSnap) {
	s.Count += o.Count
	s.SumNs += o.SumNs
	for i := range s.Buckets {
		s.Buckets[i] += o.Buckets[i]
	}
}

// percentile returns the upper bound of the bucket containing the p-quantile
// (0 < p <= 1), an over-estimate by at most 2x.
func (s *histSnap) percentile(p float64) time.Duration {
	if s.Count == 0 {
		return 0
	}
	target := int64(math.Ceil(p * float64(s.Count)))
	var cum int64
	for i, n := range s.Buckets {
		cum += n
		if cum >= target {
			return histBase << i
		}
	}
	return histBase << (histBuckets - 1)
}

func (s *histSnap) mean() time.Duration {
	if s.Count == 0 {
		return 0
	}
	return time.Duration(s.SumNs / s.Count)
}

// instrumentedQueue decorates the production task.Queue with harness-side
// counters, latency histograms, and ledger events (claims and outcomes for the
// post-run history checkers). The pool and sweeper run against it, so every
// measured operation is the real Spanner code path.
type instrumentedQueue struct {
	inner  task.Queue
	led    *ledger
	holder string

	probes     atomic.Int64 // read-only claimable-shard probes issued
	probeEmpty atomic.Int64 // probes that found no shard with claimable work
	claims     atomic.Int64 // claim transactions issued
	claimEmpty atomic.Int64 // claims that found the shard empty
	claimTasks atomic.Int64 // tasks leased across all claims
	claimErrs  atomic.Int64
	completes  atomic.Int64
	fails      atomic.Int64
	sweeps     atomic.Int64
	swept      atomic.Int64

	claimLat    hist
	completeLat hist
}

var _ task.Queue = (*instrumentedQueue)(nil)

func (q *instrumentedQueue) Enqueue(ctx context.Context, t *task.Task) error {
	return q.inner.Enqueue(ctx, t)
}

// ClaimableShards is counted separately from Claim because the whole point of
// the probe is that it replaces claim transactions on an empty queue: a run
// whose probes climb while its claims stay flat is the win, and one where both
// climb together means discovery is not gating anything.
func (q *instrumentedQueue) ClaimableShards(ctx context.Context) ([]int, error) {
	shards, err := q.inner.ClaimableShards(ctx)
	q.probes.Add(1)
	if err == nil && len(shards) == 0 {
		q.probeEmpty.Add(1)
	}
	return shards, err
}

func (q *instrumentedQueue) Claim(ctx context.Context, shard, limit int, lease time.Duration) ([]*task.Task, error) {
	start := time.Now()
	tasks, err := q.inner.Claim(ctx, shard, limit, lease)
	q.claimLat.observe(time.Since(start))
	q.claims.Add(1)
	switch {
	case err != nil:
		q.claimErrs.Add(1)
	case len(tasks) == 0:
		q.claimEmpty.Add(1)
	default:
		q.claimTasks.Add(int64(len(tasks)))
		for _, t := range tasks {
			q.led.claim(t.ID, q.holder, t.Attempts, t.LeaseExpiry)
		}
	}
	return tasks, err
}

func (q *instrumentedQueue) Complete(ctx context.Context, t *task.Task) error {
	start := time.Now()
	err := q.inner.Complete(ctx, t)
	q.completeLat.observe(time.Since(start))
	q.completes.Add(1)
	if err == nil {
		q.led.complete(t.ID, q.holder)
	}
	return err
}

func (q *instrumentedQueue) Fail(ctx context.Context, t *task.Task, cause error, retryAfter time.Duration) error {
	q.fails.Add(1)
	err := q.inner.Fail(ctx, t, cause, retryAfter)
	if err == nil {
		q.led.fail(t.ID, q.holder, t.Attempts, t.Exhausted())
	}
	return err
}

// Postpone passes through un-ledgered: the synthetic handler never
// reschedules, and a postpone resets attempts to the pre-claim value, which
// the attempt-number-reuse checker would misread as a lost update.
func (q *instrumentedQueue) Postpone(ctx context.Context, t *task.Task, reason string, after time.Duration) error {
	return q.inner.Postpone(ctx, t, reason, after)
}

func (q *instrumentedQueue) Sweep(ctx context.Context) (int, error) {
	n, err := q.inner.Sweep(ctx)
	q.sweeps.Add(1)
	q.swept.Add(int64(n))
	return n, err
}
