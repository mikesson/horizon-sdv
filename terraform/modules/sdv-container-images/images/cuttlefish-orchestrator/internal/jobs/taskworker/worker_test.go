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
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/jobs/taskworker"
)

func TestPoolProcessesAndCompletes(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	for i := range 5 {
		enqueueFake(t, q, "demo", fmt.Sprintf("t%d", i))
	}
	h := &fakeHandler{typ: "demo"}
	p := newPool(t, fastConfig(), q, []task.Handler{h})

	p.Start(t.Context())
	require.Eventually(t, func() bool { return len(q.completedIDs()) == 5 },
		3*time.Second, 5*time.Millisecond)
	p.Stop()

	assert.Equal(t, 5, h.count())
	assert.Equal(t, int64(5), p.Stats().Processed)
	assert.Empty(t, q.failedRecords())
}

// A RescheduleError is waiting, not failure: the task is postponed with its
// reason and delay, and neither the failure path nor its counter fires.
func TestPoolPostponesOnReschedule(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	enqueueFake(t, q, "demo", "t1")
	h := &fakeHandler{typ: "demo", fn: func(context.Context, *task.Task) error {
		return task.Reschedule(7*time.Second, "waiting on host")
	}}
	p := newPool(t, fastConfig(), q, []task.Handler{h})

	p.Start(t.Context())
	require.Eventually(t, func() bool { return len(q.postponedRecords()) == 1 },
		3*time.Second, 5*time.Millisecond)
	p.Stop()

	rec := q.postponedRecords()[0]
	assert.Equal(t, "t1", rec.id)
	assert.Equal(t, "waiting on host", rec.reason)
	assert.Equal(t, 7*time.Second, rec.after)
	assert.Empty(t, q.failedRecords(), "a postpone is not a failure")
	assert.Equal(t, int64(1), p.Stats().Postponed)
	assert.Equal(t, int64(0), p.Stats().Failed)
}

func TestPoolFailsAndReschedules(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	enqueueFake(t, q, "demo", "t1")
	h := &fakeHandler{typ: "demo", fn: func(_ context.Context, _ *task.Task) error {
		return errors.New("boom")
	}}
	p := newPool(t, fastConfig(), q, []task.Handler{h})

	p.Start(t.Context())
	require.Eventually(t, func() bool { return len(q.failedRecords()) >= 1 },
		3*time.Second, 5*time.Millisecond)
	p.Stop()

	recs := q.failedRecords()
	require.Len(t, recs, 1)
	assert.Equal(t, "t1", recs[0].id)
	assert.False(t, recs[0].deadLettered, "not exhausted yet, so rescheduled")
	assert.Positive(t, recs[0].retryAfter)
	assert.Equal(t, int64(1), p.Stats().Failed)
}

// A DelayHintError (rate-limited call) floors the retry delay at the hint:
// the generic backoff curve must not hammer a throttling upstream.
func TestPoolHonorsRetryDelayHint(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	enqueueFake(t, q, "demo", "t1")
	h := &fakeHandler{typ: "demo", fn: func(_ context.Context, _ *task.Task) error {
		return &task.DelayHintError{After: 90 * time.Second, Err: errors.New("429 too many requests")}
	}}
	p := newPool(t, fastConfig(), q, []task.Handler{h})

	p.Start(t.Context())
	require.Eventually(t, func() bool { return len(q.failedRecords()) >= 1 },
		3*time.Second, 5*time.Millisecond)
	p.Stop()

	recs := q.failedRecords()
	require.Len(t, recs, 1)
	assert.GreaterOrEqual(t, recs[0].retryAfter, 90*time.Second,
		"hint floors the backoff (fastConfig's RetryMax is far below the hint)")
	assert.False(t, recs[0].deadLettered)
}

// A handler cut short by Pool.Stop is postponed with the attempt refunded,
// never failed: a deploy landing on a task's final attempt would otherwise
// dead-letter healthy work with "context canceled", and every deploy would
// push in-flight tasks one step down the backoff curve.
func TestPoolShutdownPostponesInFlightWork(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	enqueueFake(t, q, "demo", "t1")
	started := make(chan struct{})
	h := &fakeHandler{typ: "demo", fn: func(ctx context.Context, _ *task.Task) error {
		close(started)
		<-ctx.Done()
		return fmt.Errorf("handling: %w", ctx.Err())
	}}
	p := newPool(t, fastConfig(), q, []task.Handler{h})

	p.Start(t.Context())
	<-started
	p.Stop()

	require.Len(t, q.postponedRecords(), 1, "shutdown interruption is waiting, not failure")
	assert.Empty(t, q.failedRecords())
	assert.Equal(t, int64(0), p.Stats().Failed)
}

// typedNilTreeError is an error whose unwrap tree contains a typed-nil sentinel
// without Error() ever touching it, the lazy shape a hand-rolled multi-error
// can produce. errors.AsType matches the typed-nil with ok=true.
type typedNilTreeError struct{ nested error }

func (typedNilTreeError) Error() string     { return "wrapped failure" }
func (e typedNilTreeError) Unwrap() []error { return []error{e.nested} }

// A typed-nil *DelayHintError or *RescheduleError inside a wrapped error tree
// is a plain failure: it must never be dereferenced (the hint's After read
// crashed the whole process, which also serves the API) nor classified as a
// postpone.
func TestPoolTreatsTypedNilOutcomeErrorsAsFailures(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	enqueueFake(t, q, "demo", "t1")
	enqueueFake(t, q, "demo", "t2")
	h := &fakeHandler{typ: "demo", fn: func(_ context.Context, tk *task.Task) error {
		if tk.ID == "t1" {
			return typedNilTreeError{nested: (*task.DelayHintError)(nil)}
		}
		return typedNilTreeError{nested: (*task.RescheduleError)(nil)}
	}}
	p := newPool(t, fastConfig(), q, []task.Handler{h})

	p.Start(t.Context())
	require.Eventually(t, func() bool { return len(q.failedRecords()) == 2 },
		3*time.Second, 5*time.Millisecond)
	p.Stop()

	assert.Empty(t, q.postponedRecords(), "a typed-nil reschedule is a failure, not a postpone")
	assert.Equal(t, int64(2), p.Stats().Failed)
}

// An unregistered type is an AMBIGUOUS signal, not a terminal one: across a
// rolling deploy it means this instance is the old revision and another already
// knows the type. Claim has no type predicate, so the old instance wins those
// tasks routinely, and dead-lettering on sight made a new task type's first
// enqueue a coin flip, permanently, with the attempt budget collapsed so nothing
// retried. A young task is held for whoever can handle it.
func TestPoolPostponesUnknownTypeWhileYoung(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	enqueueFake(t, q, "unregistered", "t1")
	h := &fakeHandler{typ: "demo"} // only "demo" is registered
	p := newPool(t, fastConfig(), q, []task.Handler{h})

	p.Start(t.Context())
	require.Eventually(t, func() bool { return len(q.postponedRecords()) >= 1 },
		3*time.Second, 5*time.Millisecond)
	p.Stop()

	rec := q.postponedRecords()[0]
	assert.Equal(t, "t1", rec.id)
	assert.Contains(t, rec.reason, "unregistered",
		"the reason must name the type, since it is what an operator goes looking for")
	assert.Positive(t, rec.after)
	assert.Empty(t, q.failedRecords(), "a held task must not also be dead-lettered")
	assert.Zero(t, p.Stats().DeadLettered)
	assert.Zero(t, h.count(), "handler is never invoked")
}

// Past the grace window no revision is going to appear, so the task
// dead-letters, the original behavior, now bounded rather than immediate.
func TestPoolDeadLettersUnknownTypePastGrace(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	enqueueFakeAged(t, q, "unregistered", "t1", -24*time.Hour)
	h := &fakeHandler{typ: "demo"}
	p := newPool(t, fastConfig(), q, []task.Handler{h})

	p.Start(t.Context())
	require.Eventually(t, func() bool { return len(q.failedRecords()) >= 1 },
		3*time.Second, 5*time.Millisecond)
	p.Stop()

	recs := q.failedRecords()
	require.Len(t, recs, 1)
	assert.True(t, recs[0].deadLettered, "an aged unknown type is dead-lettered")
	assert.Equal(t, int64(1), p.Stats().DeadLettered)
	assert.Empty(t, q.postponedRecords(), "past the grace window there is nothing to wait for")
	assert.Zero(t, h.count())
}

// The fall-through is intended, and it is why this is best-effort and then
// terminal. Postpone goes through releaseToPending, whose keyed-reconcile
// sibling merge cannot engage for a type this binary does
// not know is keyed, so a regenerated reconcile_key can collide with a pending
// sibling. A task left leased on that path would stall every sweep batch that
// touched it, so a failed postpone must land on the dead-letter instead.
func TestPoolDeadLettersUnknownTypeWhenPostponeFails(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{postponeErrs: bookkeepingAttemptsForTest}
	enqueueFake(t, q, "unregistered", "t1")
	h := &fakeHandler{typ: "demo"}
	p := newPool(t, fastConfig(), q, []task.Handler{h})

	p.Start(t.Context())
	require.Eventually(t, func() bool { return len(q.failedRecords()) >= 1 },
		10*time.Second, 10*time.Millisecond)
	p.Stop()

	recs := q.failedRecords()
	require.Len(t, recs, 1)
	assert.True(t, recs[0].deadLettered,
		"a task whose postpone cannot commit must reach a terminal state, not stay leased")
	assert.Empty(t, q.postponedRecords(), "every postpone attempt failed")
}

func TestPoolStopDrainsInFlight(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	enqueueFake(t, q, "block", "t1")
	started := make(chan struct{})
	var once sync.Once
	h := &fakeHandler{typ: "block", fn: func(ctx context.Context, _ *task.Task) error {
		once.Do(func() { close(started) })
		<-ctx.Done() // block until the pool cancels in-flight work
		return ctx.Err()
	}}
	p := newPool(t, fastConfig(), q, []task.Handler{h})
	p.Start(t.Context())

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never started")
	}

	done := make(chan struct{})
	go func() { p.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not drain the in-flight handler")
	}
}

func TestPoolDisabledDoesNotStart(t *testing.T) {
	t.Parallel()
	cfg := fastConfig()
	cfg.Enabled = false
	q := &fakeQueue{}
	p := newPool(t, cfg, q, nil)

	p.Start(t.Context())
	assert.Zero(t, q.claimCount(), "disabled pool starts no workers")
	p.Stop()
}

// TestPoolRecoversPanickedHandler proves a poison task fails through the normal
// retry path instead of crashing the process.
func TestPoolRecoversPanickedHandler(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	enqueueFake(t, q, "demo", "t1")
	h := &fakeHandler{typ: "demo", fn: func(_ context.Context, _ *task.Task) error {
		panic("kaboom")
	}}
	p := newPool(t, fastConfig(), q, []task.Handler{h})

	p.Start(t.Context())
	require.Eventually(t, func() bool { return len(q.failedRecords()) >= 1 },
		3*time.Second, 5*time.Millisecond)
	p.Stop()

	recs := q.failedRecords()
	require.NotEmpty(t, recs)
	assert.Contains(t, recs[0].cause, "handler panicked: kaboom")
}

// Not parallel: swaps the global tracer provider to record spans.
func TestPoolLinksEnqueueTrace(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.TraceContext{})

	q := &fakeQueue{}
	tk := task.New("demo", "res-t1")
	tk.ID = "t1"
	tk.TraceContext = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	require.NoError(t, q.Enqueue(t.Context(), tk))

	h := &fakeHandler{typ: "demo"}
	p := newPool(t, fastConfig(), q, []task.Handler{h})
	p.Start(t.Context())
	require.Eventually(t, func() bool { return len(q.completedIDs()) == 1 },
		3*time.Second, 5*time.Millisecond)
	p.Stop()

	var handle sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == "demo" {
			handle = s
			break
		}
	}
	require.NotNil(t, handle, "handle span not recorded")
	assert.Equal(t, trace.SpanKindConsumer, handle.SpanKind())
	assert.False(t, handle.Parent().IsValid(), "attempt is its own root trace")
	assert.NotEqual(t, "4bf92f3577b34da6a3ce929d0e0e4736", handle.SpanContext().TraceID().String())
	require.Len(t, handle.Links(), 1, "attempt links to the enqueue-time span")
	link := handle.Links()[0].SpanContext
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", link.TraceID().String())
	assert.Equal(t, "00f067aa0ba902b7", link.SpanID().String())
}

func TestNewRejectsLeaseWithinHandleTimeout(t *testing.T) {
	t.Parallel()
	cfg := fastConfig()
	cfg.LeaseDuration = cfg.HandleTimeout // handler could outlive its lease
	_, err := taskworker.New(cfg, &fakeQueue{}, nil, testLogger())
	require.ErrorContains(t, err, "lease_duration")
}

// --- test doubles ---

func fastConfig() taskworker.Config {
	return taskworker.Config{
		Enabled:       true,
		MinWorkers:    3,
		MaxWorkers:    3,
		NumShards:     1,
		ClaimBatch:    2,
		LeaseDuration: 2 * time.Minute,
		HandleTimeout: time.Minute,
		PollMin:       time.Millisecond,
		PollMax:       5 * time.Millisecond,
		RetryBase:     time.Millisecond,
		RetryMax:      10 * time.Millisecond,
	}
}

func newPool(t *testing.T, cfg taskworker.Config, q task.Queue, hs []task.Handler, opts ...taskworker.Option) *taskworker.Pool {
	t.Helper()
	p, err := taskworker.New(cfg, q, hs, testLogger(), opts...)
	require.NoError(t, err)
	return p
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func enqueueFake(t *testing.T, q *fakeQueue, taskType, id string) {
	t.Helper()
	tk := task.New(taskType, "res-"+id)
	tk.ID = id
	_ = q.Enqueue(t.Context(), tk)
}

// enqueueFakeAged stamps CreatedAt so wall-clock budgets can be exercised. The
// real queue populates it at claim time (Claim selects created_at); task.New
// leaves it zero, which the worker reads as new rather than ancient.
func enqueueFakeAged(t *testing.T, q *fakeQueue, taskType, id string, age time.Duration) {
	t.Helper()
	tk := task.New(taskType, "res-"+id)
	tk.ID = id
	tk.CreatedAt = time.Now().Add(age)
	_ = q.Enqueue(t.Context(), tk)
}

// bookkeepingAttemptsForTest mirrors the worker's unexported bookkeepingAttempts
// so a test can fail every attempt of one queue write.
const bookkeepingAttemptsForTest = 3

type failRecord struct {
	id           string
	cause        string
	retryAfter   time.Duration
	deadLettered bool
}

// fakeQueue is an in-memory task.Queue. Claim ignores the shard and drains a
// single pending slice, which is sufficient to exercise worker dispatch.
type fakeQueue struct {
	mu        sync.Mutex
	pending   []*task.Task
	completed []string
	failed    []failRecord
	postponed []postponeRecord
	claims    int

	probes        int
	completeErrs  int // fail the first N Complete calls
	completeCalls int
	postponeErrs  int // fail the first N Postpone calls
	probeErr      error
	blindProbe    bool // probe always answers empty, whatever is pending
}

type postponeRecord struct {
	id     string
	reason string
	after  time.Duration
}

var _ task.Queue = (*fakeQueue)(nil)

func (f *fakeQueue) Enqueue(_ context.Context, t *task.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(f.pending, t)
	return nil
}

// ClaimableShards reports shard 0 while anything is pending: the fake ignores
// shards, so one is enough to let the pool through to the claim.
func (f *fakeQueue) ClaimableShards(_ context.Context) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes++
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	if f.blindProbe || len(f.pending) == 0 {
		return nil, nil
	}
	return []int{0}, nil
}

func (f *fakeQueue) Claim(_ context.Context, _, limit int, _ time.Duration) ([]*task.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims++
	if len(f.pending) == 0 {
		return nil, nil
	}
	n := min(limit, len(f.pending))
	out := make([]*task.Task, n)
	for i := range n {
		f.pending[i].Attempts++ // mimic the real queue incrementing on claim
		out[i] = f.pending[i]
	}
	f.pending = f.pending[n:]
	return out, nil
}

func (f *fakeQueue) Complete(_ context.Context, t *task.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completeCalls++
	if f.completeErrs > 0 {
		f.completeErrs--
		return errors.New("injected complete failure")
	}
	f.completed = append(f.completed, t.ID)
	return nil
}

func (f *fakeQueue) Fail(_ context.Context, t *task.Task, cause error, retryAfter time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, failRecord{
		id: t.ID, cause: cause.Error(), retryAfter: retryAfter, deadLettered: t.Exhausted(),
	})
	return nil
}

func (f *fakeQueue) Postpone(_ context.Context, t *task.Task, reason string, after time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.postponeErrs > 0 {
		f.postponeErrs--
		return errors.New("injected postpone failure")
	}
	f.postponed = append(f.postponed, postponeRecord{id: t.ID, reason: reason, after: after})
	return nil
}

func (f *fakeQueue) postponedRecords() []postponeRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.postponed)
}

func (f *fakeQueue) Sweep(_ context.Context) (int, error) { return 0, nil }

func (f *fakeQueue) completedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.completed...)
}

func (f *fakeQueue) failedRecords() []failRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]failRecord(nil), f.failed...)
}

func (f *fakeQueue) probeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probes
}

func (f *fakeQueue) claimCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claims
}

func (f *fakeQueue) completeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.completeCalls
}

type fakeHandler struct {
	typ string
	fn  func(ctx context.Context, t *task.Task) error

	mu      sync.Mutex
	handled []string
}

func (h *fakeHandler) TaskType() string { return h.typ }

func (h *fakeHandler) Handle(ctx context.Context, t *task.Task) error {
	h.mu.Lock()
	h.handled = append(h.handled, t.ID)
	h.mu.Unlock()
	if h.fn != nil {
		return h.fn(ctx, t)
	}
	return nil
}

func (h *fakeHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.handled)
}
