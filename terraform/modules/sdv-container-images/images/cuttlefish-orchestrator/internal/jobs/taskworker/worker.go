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
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// Config tunes the worker pool. Start from DefaultConfig and override; zero
// values (except Enabled) are replaced with defaults.
type Config struct {
	// Enabled gates the pool. DefaultConfig sets it true.
	Enabled bool
	// MinWorkers and MaxWorkers bound the pool's own active worker count. It
	// sizes itself between them from the age of the work it claims (see sizer),
	// so the floor is what an idle instance costs and the ceiling is what a
	// backlogged one may spend.
	//
	// Both bound PARKED goroutines rather than CPU. A handler holds its worker
	// for the whole remote wait (a CVD create blocks on the host's `cvd load`
	// for 94-156 s), so the concurrent creates one instance can carry is the
	// active count, and a waiting worker costs a stack and a socket.
	//
	// A ceiling below the floor is resolved in the ceiling's favor: it is the
	// resource bound, where the floor is only a latency preference.
	MinWorkers int
	MaxWorkers int
	// NumShards must match the queue's shard fan-out. Workers claim the shards
	// the queue's claimable-shard probe names, so this is the enqueue side's
	// fan-out rather than a claim-side guess.
	NumShards int
	// ClaimBatch is the max tasks a worker leases per claim. Keep it near 1 for
	// long-running handlers so leased tasks are not held idle past their lease.
	ClaimBatch int
	// LeaseDuration is the visibility timeout for a claimed task; it must exceed
	// HandleTimeout so a running handler's task is not swept out from under it.
	// The margin (LeaseDuration − HandleTimeout) is the ONLY guard against a
	// live-but-stuck handler racing the re-claimant of its swept task (the
	// bookkeeping fence catches the writes, not the work), so handlers must
	// never run context-ignoring work longer than it. See
	// docs/development/architecture-decisions.md.
	LeaseDuration time.Duration
	// HandleTimeout bounds a single handler invocation.
	HandleTimeout time.Duration
	// PollMin and PollMax bound the jittered delay after the first poll that
	// finds nothing; PollCap bounds it after the delay has stretched. An idle
	// queue is asked about every PollCap or so instead of every PollMax, and a
	// producer's wake collapses the delay back to the floor, so the stretch
	// costs latency only where nothing in this process enqueued the work.
	//
	// PollCap is also the unit the pool sizes itself in: it is the longest a
	// healthy task can wait for this pool's first probe, so it is what separates
	// a task that is merely waiting from one that is starving.
	PollMin time.Duration
	PollMax time.Duration
	PollCap time.Duration
	// RetryBase and RetryMax bound the exponential backoff for failed tasks.
	RetryBase time.Duration
	RetryMax  time.Duration
}

// DefaultConfig returns production-ready defaults.
func DefaultConfig() Config {
	return Config{
		Enabled: true,
		// The floor is what an instance costs when nothing is queued, and it is
		// the width this pool has always had. The ceiling is sized for I/O-bound
		// handlers on this deployment's single vCPU: 64 parked goroutines are a
		// few megabytes of stacks against a gibibyte, and the Spanner side
		// carries them with an order of magnitude to spare: four gRPC channels
		// at a hundred concurrent requests each, against a worker that holds at
		// most one request at a time. It is NOT sized against the
		// platform's 8-vCPU envelope; this deployment runs one.
		MinWorkers: 8,
		MaxWorkers: 64,
		NumShards:  task.DefaultShards,
		ClaimBatch: 1,
		// A cold CVD create fetches multi-GB build artifacts on the host
		// before the operation completes, proven >10m in practice. The
		// lease must outlive the handler so a live attempt is never swept.
		LeaseDuration: 35 * time.Minute,
		HandleTimeout: 30 * time.Minute,
		PollMin:       200 * time.Millisecond,
		PollMax:       2 * time.Second,
		PollCap:       30 * time.Second,
		RetryBase:     time.Second,
		RetryMax:      5 * time.Minute,
	}
}

// WithDefaults replaces non-positive fields with their defaults. Enabled is left
// untouched (its zero value is a meaningful "off"). Callers that need a resolved
// value before constructing the pool (e.g. NumShards for the queue) use this to
// resolve the config exactly once.
func (c Config) WithDefaults() Config {
	d := DefaultConfig()
	if c.MinWorkers <= 0 {
		c.MinWorkers = d.MinWorkers
	}
	if c.MaxWorkers <= 0 {
		c.MaxWorkers = d.MaxWorkers
	}
	// The ceiling wins a disagreement: it bounds a resource, where the floor
	// only expresses how quickly the pool wants to answer.
	c.MinWorkers = min(c.MinWorkers, c.MaxWorkers)
	if c.NumShards <= 0 {
		c.NumShards = d.NumShards
	}
	if c.ClaimBatch <= 0 {
		c.ClaimBatch = d.ClaimBatch
	}
	if c.LeaseDuration <= 0 {
		c.LeaseDuration = d.LeaseDuration
	}
	if c.HandleTimeout <= 0 {
		c.HandleTimeout = d.HandleTimeout
	}
	if c.PollMin <= 0 {
		c.PollMin = d.PollMin
	}
	if c.PollMax <= 0 {
		c.PollMax = d.PollMax
	}
	if c.PollCap <= 0 {
		c.PollCap = d.PollCap
	}
	// A cap below the floor would shorten the delay as the queue stayed empty.
	c.PollCap = max(c.PollCap, c.PollMax)
	if c.RetryBase <= 0 {
		c.RetryBase = d.RetryBase
	}
	if c.RetryMax <= 0 {
		c.RetryMax = d.RetryMax
	}
	return c
}

// Stats is a point-in-time snapshot of pool counters.
type Stats struct {
	Processed    int64
	Failed       int64
	DeadLettered int64
	Postponed    int64
	Overran      int64 // handlers that ran past the lease (double-delivery risk)
	Active       int64 // workers currently permitted to claim (a level, not a count)
	Deferred     int64 // tasks postponed for their class's share of the pool
}

// Pool is the in-process worker pool draining the task queue.
type Pool struct {
	cfg      Config
	queue    task.Queue
	handlers map[string]task.Handler
	logger   *slog.Logger
	waker    *task.Waker
	sizer    *sizer
	bulkhead *bulkhead

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	processed    atomic.Int64
	failed       atomic.Int64
	deadLettered atomic.Int64
	postponed    atomic.Int64
	// unregisteredPostponed is the rolling-deploy subset of postponed: tasks
	// this revision has no handler for, held for one that does. Counted
	// separately because a non-zero value outside a deploy window means a task
	// type was enqueued that nothing will ever handle.
	unregisteredPostponed atomic.Int64
	// bulkheadDeferred is the other subset of postponed: long-occupancy tasks
	// claimed while their class already held its share of the pool. A climbing
	// value is the bulkhead binding, which is a saturated pool rather than a
	// fault.
	bulkheadDeferred atomic.Int64
	inFlight         atomic.Int64
	handlerOverran   atomic.Int64 // handlers that ran past the lease (double-delivery risk)

	tracer         trace.Tracer
	handleDuration metric.Float64Histogram
}

// Option configures a pool at construction.
type Option func(*Pool)

// WithWaker gives the pool the process-local signal producers fire once a
// commit carrying a task insert succeeds, so a parked worker stops waiting out
// its poll delay. Delivery does not depend on it: without a waker the pool
// discovers every task by polling, which is what bounds the wait when the
// producer was another instance.
func WithWaker(w *task.Waker) Option {
	return func(p *Pool) { p.waker = w }
}

// New builds a pool. handlers are indexed by their TaskType; a duplicate type is
// a programming error and the last registration wins. A nil logger falls back to
// slog.Default. It rejects a config whose LeaseDuration does not exceed
// HandleTimeout: a handler outliving its lease would be swept and re-claimed
// while still running, defeating the lease fencing on Complete/Fail.
func New(cfg Config, queue task.Queue, handlers []task.Handler, logger *slog.Logger, opts ...Option) (*Pool, error) {
	if logger == nil {
		logger = slog.Default()
	}
	cfg = cfg.WithDefaults()
	if cfg.LeaseDuration <= cfg.HandleTimeout {
		return nil, fmt.Errorf("lease_duration (%v) must exceed handle_timeout (%v)",
			cfg.LeaseDuration, cfg.HandleTimeout)
	}
	reg := make(map[string]task.Handler, len(handlers))
	for _, h := range handlers {
		reg[h.TaskType()] = h
	}
	s := newSizer(cfg)
	p := &Pool{
		cfg:      cfg,
		queue:    queue,
		handlers: reg,
		logger:   logger,
		sizer:    s,
		bulkhead: newBulkhead(s),
	}
	for _, opt := range opts {
		opt(p)
	}
	p.initMetrics()
	return p, nil
}

// initMetrics sets up OTel instruments. Instruments are no-ops when no meter
// provider is configured (e.g. in tests), so this is always safe.
func (p *Pool) initMetrics() {
	meter := otel.Meter(telemetry.ScopeName("taskworker"))
	p.tracer = otel.Tracer(telemetry.ScopeName("taskworker"))
	p.handleDuration, _ = meter.Float64Histogram("cco.tasks.handle_duration",
		metric.WithUnit("s"), metric.WithDescription("Task handler execution time."))
	telemetry.RegisterCounters("taskworker", []telemetry.ObservedCounter{
		{Name: "cco.tasks.processed", Description: "Tasks completed successfully.", Value: &p.processed},
		{Name: "cco.tasks.failed", Description: "Task handler failures (retried or dead-lettered).", Value: &p.failed},
		{Name: "cco.tasks.dead_lettered", Description: "Tasks dead-lettered for an unregistered type, after their grace window elapsed.", Value: &p.deadLettered},
		{Name: "cco.tasks.unregistered_postponed", Description: "Tasks of an unregistered type held for another revision (expected only during a rolling deploy).", Value: &p.unregisteredPostponed},
		{Name: "cco.tasks.bulkhead_deferred", Description: "Long-occupancy tasks postponed because their class already held its share of the pool.", Value: &p.bulkheadDeferred},
		{Name: "cco.tasks.in_flight", Description: "Tasks currently being handled.", Value: &p.inFlight, Gauge: true},
		{Name: "cco.tasks.active_workers", Description: "Workers this instance currently permits to claim, between the pool's floor and ceiling.", Value: &p.sizer.active, Gauge: true},
		{Name: "cco.tasks.handler_overran", Description: "Handlers that ran past their lease (double-delivery risk).", Value: &p.handlerOverran},
	})
}

// Start launches the worker goroutines. It returns immediately; the pool runs
// until the context is canceled or Stop is called.
func (p *Pool) Start(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		return
	}
	if !p.cfg.Enabled {
		p.logger.Info("task worker pool disabled")
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.running = true
	p.sizer.start()
	// The whole set is launched; the sizer decides how much of it may claim, and
	// parks the rest at no queue cost. Starting only the floor and spawning the
	// rest on demand would buy nothing, since a parked goroutine is a stack, and
	// would put worker lifetimes on the growth path, where a shutdown racing a
	// spawn is a leak nobody sees until it is one.
	for i := range p.cfg.MaxWorkers {
		p.wg.Go(func() { p.workerLoop(runCtx, i) })
	}
	// A parked worker waits on the sizer's condition rather than on the context,
	// so cancellation has to be turned into a wake for it. Stop cancels through
	// this; so does a lifecycle context canceled without a Stop.
	p.wg.Go(func() {
		<-runCtx.Done()
		p.sizer.stop()
	})
	p.logger.Info("task worker pool started",
		slog.Int("min_workers", p.cfg.MinWorkers),
		slog.Int("max_workers", p.cfg.MaxWorkers),
		slog.Int("shards", p.cfg.NumShards),
		slog.Int("claim_batch", p.cfg.ClaimBatch))
}

// Stop signals the workers to stop claiming, cancels any in-flight handlers, and
// waits for them to return: every worker the pool started, including the ones
// the sizer had parked. Tasks cut short are postponed back to pending with
// the attempt refunded (bookkeeping runs detached from the canceled context);
// only a crash leaves a task leased until its lease lapses. Idempotent handlers
// make both paths safe.
func (p *Pool) Stop() {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	p.running = false
	cancel := p.cancel
	p.cancel = nil
	p.mu.Unlock()

	cancel()
	p.wg.Wait()
	p.logger.Info("task worker pool stopped",
		slog.Int64("processed", p.processed.Load()),
		slog.Int64("failed", p.failed.Load()),
		slog.Int64("dead_lettered", p.deadLettered.Load()))
}

// Stats returns a snapshot of the pool counters.
func (p *Pool) Stats() Stats {
	return Stats{
		Processed:    p.processed.Load(),
		Failed:       p.failed.Load(),
		DeadLettered: p.deadLettered.Load(),
		Postponed:    p.postponed.Load(),
		Overran:      p.handlerOverran.Load(),
		Active:       p.sizer.active.Load(),
		Deferred:     p.bulkheadDeferred.Load(),
	}
}

// noShard is the "nothing to claim" answer from discover, and the empty value
// of the shard hint a delivering claim leaves behind.
const noShard = -1

// workerLoop is one worker's discover-claim-handle cycle. Discovery is the
// read-only probe; the claim is opened only against a shard the probe named,
// so a queue with nothing in it costs reads and never a read-write transaction
// that commits nothing. The claim remains the authority: every hint it is
// given is re-read and re-validated under its own locks.
func (p *Pool) workerLoop(ctx context.Context, id int) {
	p.logger.Debug("task worker started", slog.Int("worker", id))
	idle := newIdleDelay(p.cfg)
	hinted := noShard
	for ctx.Err() == nil {
		// A worker outside the pool's current width parks here, so it finishes
		// the task it was running before it stops claiming, and costs no queue
		// read at all while parked.
		if !p.sizer.hold(id) {
			return
		}
		next, wait := p.pollOnce(ctx, id, hinted, &idle)
		hinted = next
		if wait && !p.idleWait(ctx, &idle) {
			return
		}
	}
}

// pollOnce runs one cycle for a worker holding hinted (noShard to discover
// first). It reports the shard to claim again without probing, and whether the
// worker should wait out an idle interval before looking again.
func (p *Pool) pollOnce(ctx context.Context, id, hinted int, idle *idleDelay) (int, bool) {
	shard := hinted
	if shard == noShard {
		shard = p.discover(ctx, id)
		if shard == noShard {
			// The one unambiguous "nothing to do anywhere" the pool observes:
			// a claim that lost a race or errored says nothing about the queue.
			p.sizer.observeIdle()
			return noShard, true
		}
		idle.reset()
	}
	tasks, err := p.queue.Claim(ctx, shard, p.cfg.ClaimBatch, p.cfg.LeaseDuration)
	switch {
	case err != nil:
		if ctx.Err() == nil {
			p.logger.Error("task claim failed",
				slog.Int("worker", id), slog.Int("shard", shard), slog.Any("error", err))
		}
		return noShard, true
	case len(tasks) == 0:
		// A shard that delivered a moment ago says nothing about the rest of
		// the queue once it drains, so re-probe before paying a delay.
		// Otherwise the claim lost a race or every candidate was an ineligible
		// keyed head, and waiting is the right answer.
		return noShard, hinted == noShard
	}
	// This shard just delivered, so try it again before probing, and wake one
	// peer: a burst arrives as a single wake, and this is what spreads it
	// across the pool instead of draining it single-file.
	//
	// Not for a delivery the pool is about to decline, though: the woken peer
	// would claim into the same full budget and pay a claim and a postpone to
	// learn it.
	if p.bulkhead.admits(tasks) {
		p.waker.Wake()
	}
	// Before the handlers run, not after: this worker is about to disappear into
	// one for minutes, and how long the work it just took had been waiting is the
	// evidence the pool sizes itself on.
	p.sizer.observeClaim(claimAge(tasks))
	for _, t := range tasks {
		if ctx.Err() != nil {
			return noShard, false
		}
		p.process(ctx, t)
	}
	return shard, false
}

// discover asks the queue which shards hold claimable work and picks one at
// random, so concurrent workers spread over the shards that have work instead
// of contending on the lowest. It returns noShard when the queue has nothing,
// the case that must not open a read-write transaction, and treats a failed
// probe the same, leaving the claim to the next poll.
func (p *Pool) discover(ctx context.Context, id int) int {
	shards, err := p.queue.ClaimableShards(ctx)
	if err != nil {
		if ctx.Err() == nil {
			p.logger.Error("claimable-shard probe failed",
				slog.Int("worker", id), slog.Any("error", err))
		}
		return noShard
	}
	if len(shards) == 0 {
		return noShard
	}
	return shards[rand.IntN(len(shards))] //nolint:gosec // shard spread; not security-sensitive
}

// Bookkeeping (the Complete/Fail write after a handler returns) is bounded per
// attempt and retried: the writes are fenced and idempotent, so a blind retry
// after an ambiguous outcome (e.g. commit DeadlineExceeded) is safe, and
// without one a single Spanner blip strands the task leased until the sweeper
// reclaims it at lease expiry (~25 minutes of dead air observed live).
const (
	bookkeepingTimeout  = 10 * time.Second
	bookkeepingAttempts = 3
	bookkeepingBackoff  = time.Second
)

// bookkeep runs one queue write detached from pool cancellation, so a shutdown
// mid-task still records the outcome, retrying transient failures with a
// linear backoff. It returns the last error when every attempt failed.
func (p *Pool) bookkeep(ctx context.Context, fn func(ctx context.Context) error) error {
	ctx = context.WithoutCancel(ctx)
	var err error
	for attempt := range bookkeepingAttempts {
		if attempt > 0 {
			sleep(ctx, time.Duration(attempt)*bookkeepingBackoff)
		}
		actx, cancel := context.WithTimeout(ctx, bookkeepingTimeout)
		err = fn(actx)
		cancel()
		if err == nil {
			return nil
		}
	}
	return err
}

// process dispatches one task to its handler and records the outcome. The
// handler runs under its own timeout; completion and failure bookkeeping go
// through bookkeep (detached from cancellation, retried).
func (p *Pool) process(ctx context.Context, t *task.Task) {
	log := p.logger.With(
		slog.String("task_id", t.ID),
		slog.String("task_type", t.Type),
		slog.String("resource_id", t.ResourceID))

	h, ok := p.handlers[t.Type]
	if !ok {
		p.disposeUnregistered(ctx, log, t)
		return
	}

	// After the handler lookup, so an unregistered type is held for the
	// revision that knows it without ever taking a slot, and before the
	// handler runs, which is the occupancy being budgeted.
	if isLongOccupancy(t.Type) {
		if !p.bulkhead.admit() {
			p.deferOverBudget(ctx, log, t)
			return
		}
		defer p.bulkhead.release()
	}

	hctx, cancel := context.WithTimeout(ctx, p.cfg.HandleTimeout)
	defer cancel()
	hctx, span := p.startAttemptSpan(hctx, t)

	p.inFlight.Add(1)
	start := time.Now()
	err := p.runHandler(hctx, h, t)
	p.inFlight.Add(-1)
	if err != nil && ctx.Err() != nil && errors.Is(err, context.Canceled) {
		// The pool is shutting down (deploy, scale-down) and cut this handler
		// short: that is waiting for the replacement instance, not failure.
		// Requeue without burning the attempt. A shutdown landing on a
		// task's final attempt otherwise dead-letters healthy work with
		// "context canceled", and every deploy pushes in-flight tasks one
		// step down the backoff curve. HandleTimeout stays a real failure:
		// it surfaces as DeadlineExceeded, not Canceled.
		err = task.Reschedule(shutdownRequeueDelay, "pool shutdown interrupted the attempt")
	}
	resched := p.recordOutcome(hctx, span, log, t, start, err)
	isPostpone := resched != nil

	if isPostpone {
		log.Info("task waiting on precondition; postponed",
			slog.String("reason", resched.Reason),
			slog.Duration("retry_after", resched.After))
		if perr := p.bookkeep(ctx, func(bctx context.Context) error {
			return p.queue.Postpone(bctx, t, resched.Reason, resched.After)
		}); perr != nil {
			log.Error("failed to postpone task; task stays leased until swept",
				slog.Any("error", perr))
		}
		p.postponed.Add(1)
		return
	}

	if err != nil {
		retryAfter := task.Backoff(t.Attempts, p.cfg.RetryBase, p.cfg.RetryMax)
		// A server-guided delay (rate limit) overrides a shorter backoff. The
		// nil check matters: AsType matches a typed-nil inside a wrapped error
		// tree with ok=true, and dereferencing it here would crash the process
		// that also serves the API. There is no recover above this frame.
		if hint, ok := errors.AsType[*task.DelayHintError](err); ok && hint != nil {
			retryAfter = max(retryAfter, hint.After)
		}
		log.Warn("task handler failed; rescheduling",
			slog.Int("attempts", t.Attempts),
			slog.Duration("retry_after", retryAfter),
			slog.Duration("elapsed", time.Since(start)),
			slog.Any("error", err))
		if ferr := p.bookkeep(ctx, func(bctx context.Context) error {
			return p.queue.Fail(bctx, t, err, retryAfter)
		}); ferr != nil {
			log.Error("failed to record task failure; task stays leased until swept",
				slog.Any("error", ferr))
		}
		p.failed.Add(1)
		return
	}

	if err := p.bookkeep(ctx, func(bctx context.Context) error {
		return p.queue.Complete(bctx, t)
	}); err != nil {
		// The lease will lapse and the (idempotent) task will be redelivered.
		log.Error("handler succeeded but completing the task failed; will redeliver",
			slog.Any("error", err))
		return
	}
	p.processed.Add(1)
	log.Debug("task completed", slog.Duration("elapsed", time.Since(start)))
}

// shutdownRequeueDelay is how soon a task postponed by pool shutdown becomes
// claimable again. It is short, because the claimant is the replacement
// instance of a rolling deploy, not this one.
const shutdownRequeueDelay = 5 * time.Second

// Disposal of a task whose type this binary cannot dispatch.
const (
	// unregisteredTypeGrace bounds how long such a task is kept alive before it
	// is dead-lettered. It exists because "unregistered" means two different
	// things: within one binary the type will never become handleable, but
	// ACROSS A ROLLING DEPLOY it means this instance is the old one and another
	// already knows the type. Claim filters on shard, state and available_at
	// with no type predicate, so the old instance wins those tasks routinely.
	//
	// The task domain already promises this works. ResourceID is documented as
	// the task's only argument exactly so "any adjacent binary generation
	// [can] claim any task across a rolling deploy", and destroying a task on
	// sight broke that promise for the one generation that matters.
	//
	// 15 minutes matches hostWaitBudget, the codebase's other "wait for
	// infrastructure to catch up" budget, and is far longer than a revision
	// takes to become the one serving.
	unregisteredTypeGrace = 15 * time.Minute

	// unregisteredTypeRetry paces the wait: short enough that a user-visible
	// verb is picked up promptly once its handler exists, long enough not to
	// spin. Postpone does not burn an attempt, so only the grace window above
	// bounds the total wait.
	unregisteredTypeRetry = 30 * time.Second
)

// disposeUnregistered handles a task with no registered handler: postpone while
// it is young enough that another revision may still claim it, dead-letter once
// nothing is going to appear.
//
// Dead-lettering is permanent and unretryable, because it collapses the
// attempt budget, so it is the wrong default for an ambiguous signal.
// Postponing costs one redelivery.
//
// A failed postpone falls THROUGH to the dead-letter instead of leaving the
// task leased. Postpone goes through releaseToPending, whose keyed-reconcile
// sibling merge cannot engage for a type this binary does not know is keyed,
// so a regenerated reconcile_key can collide with a pending sibling. That is the one case where dead-lettering is the safer
// outcome, and it is why this is best-effort and then terminal.
func (p *Pool) disposeUnregistered(ctx context.Context, log *slog.Logger, t *task.Task) {
	// A zero CreatedAt means the row never round-tripped through the queue.
	// Read it as new, never as ancient: the fallback below is destruction.
	var age time.Duration
	if !t.CreatedAt.IsZero() {
		age = time.Since(t.CreatedAt)
	}

	if age < unregisteredTypeGrace {
		log.Warn("no handler registered for task type; postponing for another revision",
			slog.Duration("age", age),
			slog.Duration("retry_after", unregisteredTypeRetry))
		err := p.bookkeep(ctx, func(bctx context.Context) error {
			return p.queue.Postpone(bctx, t,
				fmt.Sprintf("no handler for task type %q on this revision", t.Type),
				unregisteredTypeRetry)
		})
		if err == nil {
			p.postponed.Add(1)
			p.unregisteredPostponed.Add(1)
			return
		}
		log.Error("failed to postpone unregistered task; dead-lettering instead",
			slog.Any("error", err))
	}

	log.Error("no handler registered for task type; dead-lettering",
		slog.Duration("age", age),
		slog.Duration("grace", unregisteredTypeGrace))
	// Force the dead-letter by collapsing the budget to the attempts already
	// used. Mutating Attempts upward instead would break the Fail fence (WHERE
	// attempts = the claimed value) and strand the task leased until swept, for
	// hours of sweep cycles before it really exhausted.
	t.MaxAttempts = t.Attempts
	if err := p.bookkeep(ctx, func(bctx context.Context) error {
		return p.queue.Fail(bctx, t, fmt.Errorf("no handler for task type %q", t.Type), 0)
	}); err != nil {
		log.Error("failed to dead-letter task", slog.Any("error", err))
	}
	p.deadLettered.Add(1)
}

// startAttemptSpan opens one attempt's span. Each attempt is its own root
// trace, linked to the enqueue-time span: consumer spans are always sampled
// (see telemetry.samplerFor), so task handling stays traced even when the HTTP
// tier is sampled down, and a trace per attempt stays timeline-readable where
// one trace smeared across retry backoff would not. Navigation: the link
// points back to the request; the producer span's task.enqueued event and the
// task.id attribute here point forward.
//
//nolint:spancheck // Ownership transfers to the caller; recordOutcome ends the span.
func (p *Pool) startAttemptSpan(ctx context.Context, t *task.Task) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("task.type", t.Type),
			attribute.String("task.id", t.ID),
			attribute.String("resource.id", t.ResourceID),
			attribute.Int("task.attempt", t.Attempts),
		),
	}
	if t.TraceContext != "" {
		origin := trace.SpanContextFromContext(otel.GetTextMapPropagator().Extract(
			ctx, propagation.MapCarrier{"traceparent": t.TraceContext}))
		if origin.IsValid() {
			opts = append(opts, trace.WithLinks(trace.Link{SpanContext: origin}))
		}
	}
	return p.tracer.Start(ctx, t.Type, opts...)
}

// recordOutcome closes out one attempt's telemetry (the lease-overrun guard,
// the span disposition, and the handle-duration sample) and classifies the
// handler error, returning the reschedule when the attempt was a postpone
// (waiting, not failure: no error status on the span, no failure counter).
// ctx carries the handle span, linking the duration sample to its trace.
func (p *Pool) recordOutcome(ctx context.Context, span trace.Span, log *slog.Logger, t *task.Task, start time.Time, err error) *task.RescheduleError {
	// A handler that outlived its lease may have already been swept and its task
	// re-claimed elsewhere; its Complete/Fail will then miss the per-delivery
	// lease fence and its side effects race the redelivery. New() enforces
	// LeaseDuration > HandleTimeout, so a handler that honors its context's
	// cancellation cannot reach here, but one that ignores it can. Count and log
	// it loudly.
	if elapsed := time.Since(start); elapsed >= p.cfg.LeaseDuration {
		p.handlerOverran.Add(1)
		log.Error("handler ran past its lease; possible double-delivery",
			slog.Duration("elapsed", elapsed),
			slog.Duration("lease", p.cfg.LeaseDuration))
	}
	resched, isPostpone := errors.AsType[*task.RescheduleError](err)
	// AsType matches a typed-nil inside a wrapped error tree with ok=true;
	// classify that as a plain failure so the span and the Fail bookkeeping
	// agree instead of marking a postponed span on a failed task.
	isPostpone = isPostpone && resched != nil
	switch {
	case isPostpone:
		span.AddEvent("task.postponed")
	case err != nil:
		// SetStatus rather than RecordError alone, so a failed attempt shows as errored
		// on Cloud Trace's error views, which key off span status.
		telemetry.RecordError(span, err)
	}
	span.End()
	p.handleDuration.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(attribute.String("task.type", t.Type), attribute.Bool("success", err == nil)))
	if !isPostpone {
		return nil
	}
	return resched
}

// runHandler invokes the handler, converting a panic into an error so a poison
// task fails (and eventually dead-letters) through the normal retry path
// instead of killing the process, which also serves the API.
//
//nolint:nonamedreturns // The deferred recover can only report through a named return.
func (p *Pool) runHandler(ctx context.Context, h task.Handler, t *task.Task) (err error) {
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("task handler panicked",
				slog.String("task_id", t.ID),
				slog.String("task_type", t.Type),
				slog.Any("panic", r),
				slog.String("stack", string(debug.Stack())))
			err = fmt.Errorf("handler panicked: %v", r)
		}
	}()
	if err := h.Handle(ctx, t); err != nil {
		return fmt.Errorf("handling %s: %w", t.Type, err)
	}
	return nil
}

// idleDelay is one worker's empty-queue delay. It starts in the [PollMin,
// PollMax) band the pool has always used and stretches its upper edge toward
// PollCap for as long as the queue answers empty, so an instance with nothing
// to do asks tens of times an hour rather than thousands.
type idleDelay struct {
	cfg   Config
	upper time.Duration
}

func newIdleDelay(cfg Config) idleDelay {
	return idleDelay{cfg: cfg, upper: cfg.PollMax}
}

// reset returns the delay to the floor band. Anything that proves the queue is
// live calls it: a wake, or a probe that named a shard.
func (d *idleDelay) reset() { d.upper = d.cfg.PollMax }

// next returns the jittered delay to wait now and stretches the band for the
// wait after it.
func (d *idleDelay) next() time.Duration {
	wait := d.cfg.PollMin
	if span := d.upper - d.cfg.PollMin; span > 0 {
		wait += time.Duration(rand.Int64N(int64(span))) //nolint:gosec // poll jitter; not security-sensitive
	}
	d.upper = min(2*d.upper, d.cfg.PollCap)
	return wait
}

// idleWait parks the worker for one empty-queue delay, or until a producer's
// wake says work landed in this process. It reports whether the pool is still
// running. The delay is what makes delivery independent of the wake: with no
// waker wired at all, this is the whole discovery path and the cap bounds it.
func (p *Pool) idleWait(ctx context.Context, d *idleDelay) bool {
	timer := time.NewTimer(d.next())
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-p.waker.C():
		d.reset()
		return true
	case <-timer.C:
		return true
	}
}

// sleep waits for d or until ctx is canceled. It reports whether the full delay
// elapsed (false means the context was canceled).
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
