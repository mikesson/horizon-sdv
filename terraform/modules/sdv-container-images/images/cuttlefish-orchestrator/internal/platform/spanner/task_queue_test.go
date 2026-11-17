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

package spanner_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

func TestTaskQueueEnqueueCapturesTraceContext(t *testing.T) {
	t.Parallel()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))

	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)
	ctx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	}))

	tk := task.New("device.create_cvd", "dev-1")
	require.NoError(t, q.Enqueue(ctx, tk))
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", tk.TraceContext)

	got, err := q.Claim(t.Context(), 0, 1, time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, tk.TraceContext, got[0].TraceContext, "trace context survives the claim round-trip")
}

func TestTaskQueueEnqueueClaimComplete(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))
	ctx := t.Context()

	tk := task.New("device.create_cvd", "dev-1")
	tk.ID = "task-1"
	require.NoError(t, q.Enqueue(ctx, tk))

	got, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "task-1", got[0].ID)
	assert.Equal(t, "device.create_cvd", got[0].Type)
	assert.Equal(t, "dev-1", got[0].ResourceID)
	assert.Equal(t, task.StateLeased, got[0].State)
	assert.Equal(t, 1, got[0].Attempts)
	assert.Equal(t, "worker-a", got[0].LeaseHolder)

	require.NoError(t, q.Complete(ctx, got[0]))
	after, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	assert.Empty(t, after, "completed task is gone")
}

// The claim's lease is SERVER-assigned (CURRENT_TIMESTAMP() + lease, read
// back via THEN RETURN) so the delivery fence keys on a timestamp no
// instance clock produced. The returned value must be exactly the stored one.
func TestTaskQueueClaimStampsServerLease(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("w"))
	ctx := t.Context()

	enqueue(t, q, "t1", time.Now())
	before := time.Now()
	claimed, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	after := time.Now()

	// ~now+lease, with slack for test-host load (the emulator shares this
	// machine's clock).
	got := claimed[0].LeaseExpiry
	assert.WithinRange(t, got,
		before.Add(time.Hour-time.Minute), after.Add(time.Hour+time.Minute))

	// Exactly the stored value, because it is the fence key.
	stored := readTime(t, db, "t1", "lease_expiry")
	assert.True(t, got.Equal(stored), "THEN RETURN lease_expiry (%v) != stored (%v)", got, stored)

	// And the fence built from it matches: Complete deletes the row.
	require.NoError(t, q.Complete(ctx, claimed[0]))
	_, err = db.Client.Single().ReadRow(ctx, "tasks", spanner.Key{"t1"}, []string{"task_id"})
	assert.Equal(t, codes.NotFound, spanner.ErrCode(err),
		"fenced Complete must match the server-assigned lease")
}

func TestTaskQueueClaimLeasesNoDoubleClaim(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("w"))
	ctx := t.Context()

	enqueue(t, q, "t1", time.Now())
	enqueue(t, q, "t2", time.Now())

	first, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, first, 2)

	second, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	assert.Empty(t, second, "already-leased tasks are not re-claimed")
}

func TestTaskQueueClaimRespectsAvailableAt(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("w"))
	ctx := t.Context()

	enqueue(t, q, "future", time.Now().Add(time.Hour))

	got, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	assert.Empty(t, got, "task scheduled in the future is not claimable yet")
}

func TestTaskQueueFailRetryThenReclaim(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("w"))
	ctx := t.Context()

	enqueue(t, q, "t1", time.Now())
	claimed, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	// Fail with a future backoff: pending again, but not yet claimable.
	require.NoError(t, q.Fail(ctx, claimed[0], errors.New("boom"), time.Hour))
	assert.Equal(t, "pending", readString(t, db, "t1", "state"))
	none, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	assert.Empty(t, none, "backed-off task is not immediately claimable")

	// Once the backoff elapses (rewound directly), it is reclaimable and the
	// next delivery advances attempts to 2.
	_, err = db.Client.Apply(ctx, []*spanner.Mutation{
		spanner.UpdateMap("tasks", map[string]any{
			"task_id":      "t1",
			"available_at": time.Now().Add(-time.Second),
		}),
	})
	require.NoError(t, err)
	re, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, re, 1)
	assert.Equal(t, 2, re[0].Attempts)
}

// TestTaskQueueCompleteAndFailAreFenced proves a worker whose lease was swept
// and re-claimed by another instance can neither delete nor reschedule the new
// claimant's row.
func TestTaskQueueCompleteAndFailAreFenced(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	qa := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("instance-a"))
	ctx := t.Context()

	enqueue(t, qa, "t1", time.Now())
	claimed, err := qa.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	// Simulate sweep + re-claim by another instance.
	_, err = db.Client.Apply(ctx, []*spanner.Mutation{
		spanner.UpdateMap("tasks", map[string]any{
			"task_id":      "t1",
			"lease_holder": "instance-b",
		}),
	})
	require.NoError(t, err)

	require.NoError(t, qa.Complete(ctx, claimed[0]), "fenced-out ack is not an error")
	assert.Equal(t, "leased", readString(t, db, "t1", "state"), "row survives stale Complete")

	require.NoError(t, qa.Fail(ctx, claimed[0], errors.New("stale"), 0))
	assert.Equal(t, "leased", readString(t, db, "t1", "state"), "row survives stale Fail")
	assert.Equal(t, "instance-b", readString(t, db, "t1", "lease_holder"))
}

// TestTaskQueueFenceIsPerDelivery proves the fence distinguishes deliveries and
// not only instances: after an early sweep (clock skew) the SAME instance can
// re-claim its own still-running task, and the zombie delivery's bookkeeping,
// holder-matched, must still be fenced out by the claimed attempts value. A
// holder-only fence let a stale Complete delete the new claim and a stale Fail
// un-lease it (double execution).
func TestTaskQueueFenceIsPerDelivery(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("instance-a"))
	ctx := t.Context()

	enqueue(t, q, "t1", time.Now())
	zombie, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, zombie, 1)

	// The lease is swept early (skewed sweeper clock) while the zombie's
	// handler is still running…
	_, err = db.Client.Apply(ctx, []*spanner.Mutation{
		spanner.UpdateMap("tasks", map[string]any{
			"task_id":      "t1",
			"lease_expiry": time.Now().Add(-time.Minute),
		}),
	})
	require.NoError(t, err)
	swept, err := q.Sweep(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, swept)

	// …and the SAME instance re-claims it: same holder, next attempt.
	fresh, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, fresh, 1)
	require.Equal(t, zombie[0].Attempts+1, fresh[0].Attempts)

	// The zombie's bookkeeping matches on holder but not on delivery.
	require.NoError(t, q.Complete(ctx, zombie[0]), "fenced-out ack is not an error")
	assert.Equal(t, "leased", readString(t, db, "t1", "state"), "row survives same-instance stale Complete")

	require.NoError(t, q.Fail(ctx, zombie[0], errors.New("stale"), 0))
	assert.Equal(t, "leased", readString(t, db, "t1", "state"), "row survives same-instance stale Fail")

	require.NoError(t, q.Postpone(ctx, zombie[0], "stale wait", time.Hour))
	assert.Equal(t, "leased", readString(t, db, "t1", "state"), "row survives same-instance stale Postpone")

	// The live delivery's own bookkeeping still works.
	require.NoError(t, q.Complete(ctx, fresh[0]))
	none, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	assert.Empty(t, none, "completed task is gone")
}

// TestTaskQueuePostpone proves a postponed delivery does not count: attempts
// return to the pre-claim value, the task is pending but not claimable until
// the delay elapses, and the wait reason is visible on the row.
func TestTaskQueuePostpone(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("w"))
	ctx := t.Context()

	enqueue(t, q, "t1", time.Now())
	claimed, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, 1, claimed[0].Attempts)
	require.False(t, claimed[0].CreatedAt.IsZero(), "claimed tasks carry created_at for wait budgets")

	require.NoError(t, q.Postpone(ctx, claimed[0], "host provisioning", time.Hour))
	assert.Equal(t, "pending", readString(t, db, "t1", "state"))
	assert.Equal(t, int64(0), readInt64(t, db, "t1", "attempts"), "postpone burns no attempt")
	assert.Equal(t, "host provisioning", readString(t, db, "t1", "last_error"))

	none, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	assert.Empty(t, none, "postponed task is not claimable before its delay")

	// Once the delay elapses (rewound directly), the next delivery is attempt 1
	// again, because waiting consumed nothing.
	_, err = db.Client.Apply(ctx, []*spanner.Mutation{
		spanner.UpdateMap("tasks", map[string]any{
			"task_id":      "t1",
			"available_at": time.Now().Add(-time.Second),
		}),
	})
	require.NoError(t, err)
	re, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, re, 1)
	assert.Equal(t, 1, re[0].Attempts)
}

// A stale worker's Postpone must not touch a row re-claimed by another
// instance (same fencing as Complete/Fail).
func TestTaskQueuePostponeIsFenced(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	qa := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("instance-a"))
	ctx := t.Context()

	enqueue(t, qa, "t1", time.Now())
	claimed, err := qa.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	_, err = db.Client.Apply(ctx, []*spanner.Mutation{
		spanner.UpdateMap("tasks", map[string]any{
			"task_id":      "t1",
			"lease_holder": "instance-b",
		}),
	})
	require.NoError(t, err)

	require.NoError(t, qa.Postpone(ctx, claimed[0], "stale", 0), "fenced-out postpone is not an error")
	assert.Equal(t, "leased", readString(t, db, "t1", "state"), "row survives stale Postpone")
	assert.Equal(t, "instance-b", readString(t, db, "t1", "lease_holder"))
}

func TestTaskQueueFailDeadLetters(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("w"))
	ctx := t.Context()

	tk := task.New("t", "r")
	tk.ID = "dl"
	tk.MaxAttempts = 1
	require.NoError(t, q.Enqueue(ctx, tk))

	claimed, err := q.Claim(ctx, 0, 10, time.Hour) // attempts -> 1, now exhausted
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.True(t, claimed[0].Exhausted())

	require.NoError(t, q.Fail(ctx, claimed[0], errors.New("fatal"), time.Hour))
	assert.Equal(t, "failed", readString(t, db, "dl", "state"))

	none, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	assert.Empty(t, none, "dead-lettered task is not claimable")

	swept, err := q.Sweep(ctx)
	require.NoError(t, err)
	assert.Zero(t, swept, "dead-lettered task is not swept (lease cleared)")
}

// TestTaskQueueForcedDeadLetter pins the worker's unregistered-type contract:
// collapsing MaxAttempts to the attempts already used dead-letters through the
// normal fence. (Raising Attempts instead broke the fence, which reads WHERE
// attempts = the claimed value, and stranded the task leased for hours of sweep
// cycles.)
func TestTaskQueueForcedDeadLetter(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("w"))
	ctx := t.Context()

	enqueue(t, q, "orphan-type", time.Now())
	claimed, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.False(t, claimed[0].Exhausted())

	claimed[0].MaxAttempts = claimed[0].Attempts // the worker's force, fence-compatible
	require.NoError(t, q.Fail(ctx, claimed[0], errors.New("no handler for task type"), 0))
	assert.Equal(t, "failed", readString(t, db, "orphan-type", "state"),
		"forced dead-letter must land immediately, not strand the row leased")
	assert.Equal(t, int64(1), readInt64(t, db, "orphan-type", "attempts"),
		"the DLQ row keeps the true attempt count")
}

func TestTaskQueueSweepReclaimsExpiredLease(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("w"))
	ctx := t.Context()

	enqueue(t, q, "t1", time.Now())
	claimed, err := q.Claim(ctx, 0, 10, time.Hour) // lease_expiry ~ now+1h
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	swept, err := q.Sweep(ctx)
	require.NoError(t, err)
	assert.Zero(t, swept, "unexpired lease is not reclaimed")

	// Simulate a crashed worker by expiring the lease in the past.
	_, err = db.Client.Apply(ctx, []*spanner.Mutation{
		spanner.UpdateMap("tasks", map[string]any{
			"task_id":      "t1",
			"lease_expiry": time.Now().Add(-time.Hour),
		}),
	})
	require.NoError(t, err)

	swept, err = q.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, swept)

	re, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, re, 1, "reclaimed task is claimable again")
}

// TestTaskQueueSweepDeadLettersExhausted proves a task that keeps dying with
// its worker (lease expiry, never a clean Fail) cannot be redelivered forever:
// once its claim-time attempts hit max_attempts, the sweeper dead-letters it.
func TestTaskQueueSweepDeadLettersExhausted(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("w"))
	ctx := t.Context()

	tk := task.New("t", "r")
	tk.ID = "poison"
	tk.MaxAttempts = 1
	require.NoError(t, q.Enqueue(ctx, tk))

	claimed, err := q.Claim(ctx, 0, 10, time.Hour) // attempts -> 1 = max
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	// Simulate the worker dying without calling Fail: expire the lease.
	_, err = db.Client.Apply(ctx, []*spanner.Mutation{
		spanner.UpdateMap("tasks", map[string]any{
			"task_id":      "poison",
			"lease_expiry": time.Now().Add(-time.Minute),
		}),
	})
	require.NoError(t, err)

	swept, err := q.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, swept)
	assert.Equal(t, "failed", readString(t, db, "poison", "state"))

	none, err := q.Claim(ctx, 0, 10, time.Hour)
	require.NoError(t, err)
	assert.Empty(t, none, "dead-lettered task is not claimable")
}

func TestTaskQueueShardsDeterministically(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	const numShards = 4
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(numShards), cspanner.WithHolder("w"))
	ctx := t.Context()

	tk := task.New("t", "r")
	tk.ID = "abc-123"
	require.NoError(t, q.Enqueue(ctx, tk))

	wantShard := task.ShardFor("abc-123", numShards)
	assert.Equal(t, int64(wantShard), readInt64(t, db, "abc-123", "shard"))

	wrong, err := q.Claim(ctx, (wantShard+1)%numShards, 10, time.Hour)
	require.NoError(t, err)
	assert.Empty(t, wrong, "task only claimable from its own shard")

	right, err := q.Claim(ctx, wantShard, 10, time.Hour)
	require.NoError(t, err)
	require.Len(t, right, 1)
}

// TestTaskQueueConcurrentClaimNoDuplicates proves the core safety property:
// many workers claiming the same shard at once never lease the same task twice.
// Spanner's serializable isolation aborts the losing transaction, which the
// client retries and re-reads (finding the row already leased).
func TestTaskQueueConcurrentClaimNoDuplicates(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("w"))
	ctx := t.Context()

	const total = 20
	for i := range total {
		enqueue(t, q, fmt.Sprintf("t%02d", i), time.Now())
	}

	var (
		mu   sync.Mutex
		seen = make(map[string]int)
		wg   sync.WaitGroup
	)
	for range 8 {
		wg.Go(func() {
			for {
				claimed, err := q.Claim(ctx, 0, 5, time.Hour)
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				if len(claimed) == 0 {
					return
				}
				mu.Lock()
				for _, tk := range claimed {
					seen[tk.ID]++
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	assert.Len(t, seen, total, "every task claimed exactly once")
	for id, n := range seen {
		assert.Equalf(t, 1, n, "task %s claimed %d times", id, n)
	}
}

// --- helpers ---

func enqueue(t *testing.T, q *cspanner.TaskQueue, id string, availableAt time.Time) {
	t.Helper()
	tk := task.New("device.create_cvd", "res-"+id)
	tk.ID = id
	tk.AvailableAt = availableAt
	require.NoError(t, q.Enqueue(t.Context(), tk))
}

func readString(t *testing.T, db *spannertest.TestDB, id, col string) string {
	t.Helper()
	row, err := db.Client.Single().ReadRow(t.Context(), "tasks", spanner.Key{id}, []string{col})
	require.NoError(t, err)
	var s string
	require.NoError(t, row.Column(0, &s))
	return s
}

func readInt64(t *testing.T, db *spannertest.TestDB, id, col string) int64 {
	t.Helper()
	row, err := db.Client.Single().ReadRow(t.Context(), "tasks", spanner.Key{id}, []string{col})
	require.NoError(t, err)
	var v int64
	require.NoError(t, row.Column(0, &v))
	return v
}

func readTime(t *testing.T, db *spannertest.TestDB, id, col string) time.Time {
	t.Helper()
	row, err := db.Client.Single().ReadRow(t.Context(), "tasks", spanner.Key{id}, []string{col})
	require.NoError(t, err)
	var ts time.Time
	require.NoError(t, row.Column(0, &ts))
	return ts
}
