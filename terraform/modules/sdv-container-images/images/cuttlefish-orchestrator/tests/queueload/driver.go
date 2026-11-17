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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	gspanner "cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/tests/harness/faultproxy"
)

// driver owns a run: it bootstraps the database, spawns instance subprocesses,
// generates load through the production enqueue path, samples queue depth, and
// aggregates everything into the final report.
type driver struct {
	cfg      Cfg
	baseCtx  context.Context  // process lifetime; subprocesses outlive the load phase
	raw      *gspanner.Client // harness plumbing: reset, preload, invariant checks
	platform *spanner.Client  // production enqueue path + Stats
	queue    *spanner.TaskQueue

	enqueued     atomic.Int64
	counterEnq   atomic.Int64 // counter-shaped (paced) enqueues; preload bypasses the counter txn
	enqErrs      atomic.Int64
	seq          atomic.Int64
	expectPoison atomic.Int64
	enqLat       hist
	keyedCalls   atomic.Int64 // keyed reconcile filings (keyed scenario); merges make calls != rows
	keyedErrs    atomic.Int64

	// enqIDs records every committed task ID with its scripted outcome. It is
	// the ground-truth set the conservation checker reconciles effects and DLQ
	// rows against.
	enqMu  sync.Mutex
	enqIDs map[string]outcome

	proxies []*faultproxy.Proxy // per-instance DB fault proxies (db-fault mode)
	nemesis sync.WaitGroup      // chaos/pause/db-fault goroutines; joined before drain

	// terminating marks the driver-commanded shutdown: instance exits after
	// this are expected, exits before it are findings.
	terminating atomic.Bool

	mu         sync.Mutex
	procs      []*instProc
	kills      int
	pauses     int
	dbFaults   int
	instDeaths []string // uncommanded instance exits (panic, fatal, OOM kill)
}

// instProc tracks one instance subprocess across its life; a killed process
// keeps its last snapshot so cumulative counters survive as lower bounds.
type instProc struct {
	idx  int
	cmd  *exec.Cmd
	done chan struct{}

	mu    sync.Mutex
	last  *snapshot
	final *snapshot
	dead  bool
}

func (p *instProc) latest() *snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.final != nil {
		return p.final
	}
	return p.last
}

func runDriver(cfg Cfg) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := ensureRunDir(&cfg); err != nil {
		return err
	}
	fmt.Printf("queueload: scenario=%s db=%s artifacts=%s\n", cfg.Scenario, databasePath(cfg), cfg.RunDir)

	if err := bootstrap(ctx, cfg); err != nil {
		return err
	}
	raw, err := gspanner.NewClient(ctx, databasePath(cfg))
	if err != nil {
		return fmt.Errorf("creating raw spanner client: %w", err)
	}
	defer raw.Close()
	platform, err := newPlatformClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer platform.Close()

	if err := reset(ctx, raw, cfg); err != nil {
		return err
	}
	d := &driver{
		cfg:      cfg,
		baseCtx:  ctx,
		raw:      raw,
		platform: platform,
		queue:    spanner.NewTaskQueue(platform, spanner.WithShards(cfg.Shards), spanner.WithHolder("driver")),
		enqIDs:   make(map[string]outcome),
	}
	// DB fault proxies interpose on the instances' emulator link only; the
	// driver's own clients (created above, before ProxyAddrs is populated)
	// stay direct so verification reads are never fault-injected.
	if cfg.DBFault != "" {
		defer func() {
			for _, p := range d.proxies {
				_ = p.Close()
			}
		}()
		for i := range cfg.Instances {
			p, err := faultproxy.Listen(cfg.Emulator)
			if err != nil {
				return fmt.Errorf("starting fault proxy for instance %d: %w", i, err)
			}
			d.proxies = append(d.proxies, p)
			d.cfg.ProxyAddrs = append(d.cfg.ProxyAddrs, p.Addr())
		}
	}
	return d.run(ctx)
}

func ensureRunDir(cfg *Cfg) error {
	if cfg.RunDir == "" {
		dir, err := os.MkdirTemp("", "queueload-")
		if err != nil {
			return fmt.Errorf("creating run dir: %w", err)
		}
		cfg.RunDir = dir
		return nil
	}
	if err := os.MkdirAll(cfg.RunDir, 0o750); err != nil {
		return fmt.Errorf("creating run dir: %w", err)
	}
	return nil
}

// run executes the load phase against freshly spawned instances, waits for
// convergence, and reports.
func (d *driver) run(ctx context.Context) error {
	started := time.Now()
	if d.cfg.Scenario == scenarioDrain {
		fmt.Printf("preloading %d tasks...\n", d.cfg.Preload)
		if err := d.preload(ctx, d.cfg.Preload); err != nil {
			return err
		}
	}
	if err := d.keyedPreflight(ctx); err != nil {
		return err
	}

	for i := range d.cfg.Instances {
		if err := d.spawn(i); err != nil {
			d.terminate()
			return err
		}
	}

	samplerCtx, stopSampler := context.WithCancel(ctx)
	defer stopSampler()
	samplerDone := make(chan struct{})
	go d.sample(samplerCtx, started, samplerDone)

	// Nemeses and load stop together: injecting faults while the drain phase
	// verifies convergence would just measure the sweeper again. The join
	// after stopLoad guarantees every paused instance is resumed and every
	// proxy fault is cleared before drain verification starts.
	loadCtx, stopLoad := context.WithCancel(ctx)
	if d.cfg.KillEvery > 0 {
		d.nemesis.Go(func() { d.chaos(loadCtx) })
	}
	if d.cfg.PauseEvery > 0 {
		d.nemesis.Go(func() { d.pauseLoop(loadCtx) })
	}
	if d.cfg.DBFault != "" {
		d.nemesis.Go(func() { d.dbFaultLoop(loadCtx) })
	}
	d.runLoadPhase(loadCtx)
	stopLoad()
	d.nemesis.Wait()

	drained := true
	if ctx.Err() == nil && d.cfg.Instances > 0 {
		drained = d.awaitDrain(ctx)
	}

	d.terminate()
	stopSampler()
	<-samplerDone

	// Final collection must still run after a ctrl-C canceled ctx.
	collectCtx := context.WithoutCancel(ctx)
	stats, err := d.queue.Stats(collectCtx)
	if err != nil {
		fmt.Printf("final queue stats unavailable: %v\n", err)
	}
	var counterSum int64
	if d.counterEnq.Load() > 0 {
		counterSum, err = d.sumCounters(collectCtx)
		if err != nil {
			fmt.Printf("counter sum unavailable: %v\n", err)
			counterSum = -1
		}
	}
	quiesce, err := d.queueRows(collectCtx)
	if err != nil {
		fmt.Printf("quiescent queue rows unavailable: %v\n", err)
	}

	return d.report(runResult{
		elapsed:    time.Since(started),
		offered:    d.offered(),
		enqueued:   d.enqueued.Load(),
		counterEnq: d.counterEnq.Load(),
		enqErrs:    d.enqErrs.Load(),
		keyedCalls: d.keyedCalls.Load(),
		keyedErrs:  d.keyedErrs.Load(),
		enqLat:     d.enqLat.snapshot(),
		poison:     d.expectPoison.Load(),
		sums:       d.sumSnaps(),
		queueStats: stats,
		counterSum: counterSum,
		quiesce:    quiesce,
		drained:    drained,
		kills:      d.killCount(),
		pauses:     d.pauseCount(),
		dbFaults:   d.dbFaultCount(),
		interrupt:  ctx.Err() != nil,
	})
}

// quiescentRows is the tasks table read back after drain: dead-letters feed
// the conservation checker; any pending or leased leftover is a failure.
type quiescentRows struct {
	dlq     []dlqRow
	pending int64
	leased  int64
}

// queueRows reads every surviving task row after drain through the driver's
// direct (never fault-injected) client.
func (d *driver) queueRows(ctx context.Context) (quiescentRows, error) {
	var q quiescentRows
	iter := d.raw.Single().Query(ctx, gspanner.Statement{
		SQL: "SELECT task_id, state, attempts, max_attempts FROM tasks",
	})
	defer iter.Stop()
	err := iter.Do(func(row *gspanner.Row) error {
		var (
			id, state             string
			attempts, maxAttempts int64
		)
		if err := row.Columns(&id, &state, &attempts, &maxAttempts); err != nil {
			return fmt.Errorf("scanning task row: %w", err)
		}
		switch task.State(state) {
		case task.StateFailed:
			q.dlq = append(q.dlq, dlqRow{id: id, attempts: attempts, maxAttempts: maxAttempts})
		case task.StatePending:
			q.pending++
		case task.StateLeased:
			q.leased++
		case task.StateDone:
		}
		return nil
	})
	if err != nil {
		return q, fmt.Errorf("reading quiescent rows: %w", err)
	}
	return q, nil
}

// pauseLoop SIGSTOPs a random live instance for cfg.PauseFor, then resumes it.
// This is the classic stalled-process nemesis. A pause longer than the lease forces
// the sweeper to reclaim the victim's tasks while it still believes it owns
// them; its post-resume Complete/Fail writes must fence out. One victim at a
// time, and the victim is always resumed before the loop returns.
func (d *driver) pauseLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.PauseEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		victim := d.pickAlive()
		if victim == nil {
			continue
		}
		if err := victim.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
			continue // lost a race with a kill; try again next tick
		}
		d.mu.Lock()
		d.pauses++
		d.mu.Unlock()
		fmt.Printf("chaos: SIGSTOP instance %d (pid %d) for %v\n",
			victim.idx, victim.cmd.Process.Pid, d.cfg.PauseFor)
		select {
		case <-ctx.Done():
		case <-time.After(d.cfg.PauseFor):
		}
		_ = victim.cmd.Process.Signal(syscall.SIGCONT)
		fmt.Printf("chaos: SIGCONT instance %d\n", victim.idx)
	}
}

// dbFaultLoop injects the configured fault on a random instance's DB link for
// cfg.DBFaultFor, then clears it. Faults are always cleared before the loop
// returns.
func (d *driver) dbFaultLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.DBFaultEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		idx := rand.IntN(len(d.proxies)) //nolint:gosec // chaos selection
		p := d.proxies[idx]
		d.mu.Lock()
		d.dbFaults++
		d.mu.Unlock()
		switch d.cfg.DBFault {
		case dbFaultLatency:
			fmt.Printf("chaos: +%v DB latency on instance %d for %v\n", d.cfg.DBFaultLatency, idx, d.cfg.DBFaultFor)
			p.SetLatency(d.cfg.DBFaultLatency)
			waitOrDone(ctx, d.cfg.DBFaultFor)
			p.SetLatency(0)
		case dbFaultBlackhole:
			fmt.Printf("chaos: DB blackhole on instance %d for %v\n", idx, d.cfg.DBFaultFor)
			p.SetBlackhole(true)
			waitOrDone(ctx, d.cfg.DBFaultFor)
			p.SetBlackhole(false)
		case dbFaultCut:
			// The ambiguous-commit fault: connections die with commits possibly
			// in flight; the client must reconnect and the fenced, idempotent
			// bookkeeping must absorb retries of writes that already landed.
			n := p.CutConnections()
			fmt.Printf("chaos: cut %d DB connections on instance %d\n", n, idx)
		}
	}
}

func waitOrDone(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// pickAlive returns a random live instance, or nil if none.
func (d *driver) pickAlive() *instProc {
	d.mu.Lock()
	defer d.mu.Unlock()
	var alive []*instProc
	for _, p := range d.procs {
		p.mu.Lock()
		if !p.dead {
			alive = append(alive, p)
		}
		p.mu.Unlock()
	}
	if len(alive) == 0 {
		return nil
	}
	return alive[rand.IntN(len(alive))] //nolint:gosec // chaos selection
}

// runLoadPhase executes the configured load shape and returns when enqueue
// traffic is finished (or the context is canceled).
func (d *driver) runLoadPhase(ctx context.Context) {
	switch d.cfg.Scenario {
	case scenarioIdle, scenarioDrain:
		// No enqueue traffic; hold the phase open for the configured duration
		// (idle) or until told to stop by drain completion (drain enqueues
		// nothing here, because preload happened before the instances started).
		if d.cfg.Scenario == scenarioIdle {
			select {
			case <-ctx.Done():
			case <-time.After(d.cfg.Duration):
			}
		}
	case scenarioSteady:
		d.paceEnqueues(ctx, nil)
	case scenarioBurst:
		burstAt := time.After(d.cfg.Duration / 2)
		d.paceEnqueues(ctx, burstAt)
	case scenarioKeyed:
		var wg sync.WaitGroup
		wg.Go(func() { d.paceKeyed(ctx) })
		d.paceEnqueues(ctx, nil)
		wg.Wait()
	}
}

// paceKeyed files keyed reconciles through the production EnqueueResync path
// at cfg.KeyedRate for cfg.Duration, cycling a fixed resource set. While a
// resource's sibling is leased in a keyed handler, the filing lands as a new
// pending dirty-bit, which is the ineligible head the scenario exists to park
// in the claim order.
func (d *driver) paceKeyed(ctx context.Context) {
	interval := time.Duration(float64(time.Second) / d.cfg.KeyedRate)
	deadline := time.Now().Add(d.cfg.Duration)
	next := time.Now()
	for time.Now().Before(deadline) && ctx.Err() == nil {
		res := fmt.Sprintf("%s%d", keyedResourcePrefix, rand.IntN(d.cfg.KeyedResources)) //nolint:gosec // load spread
		if err := d.queue.EnqueueResync(ctx, task.TypeDeviceReconcile, res, 0); err != nil {
			if ctx.Err() == nil {
				d.keyedErrs.Add(1)
			}
		} else {
			d.keyedCalls.Add(1)
		}
		next = next.Add(interval)
		sleepUntil(ctx, next)
	}
}

// paceEnqueues generates open-loop load at cfg.Rate for cfg.Duration through
// cfg.Enqueuers concurrent workers. A non-nil burstAt channel triggers a
// one-time injection of cfg.Burst extra tasks at full speed.
func (d *driver) paceEnqueues(ctx context.Context, burstAt <-chan time.Time) {
	tokens := make(chan struct{}, d.cfg.Enqueuers)
	var wg sync.WaitGroup
	for range d.cfg.Enqueuers {
		wg.Go(func() {
			for range tokens {
				d.enqueueOne(ctx)
			}
		})
	}

	interval := time.Duration(float64(time.Second) / d.cfg.Rate)
	deadline := time.Now().Add(d.cfg.Duration)
	next := time.Now()
	for time.Now().Before(deadline) && ctx.Err() == nil {
		sendToken(ctx, tokens)
		if burstAt != nil && d.tryBurst(ctx, tokens, burstAt) {
			burstAt = nil
		}
		next = next.Add(interval)
		sleepUntil(ctx, next)
	}
	close(tokens)
	wg.Wait()
}

func sendToken(ctx context.Context, tokens chan<- struct{}) {
	select {
	case <-ctx.Done():
	case tokens <- struct{}{}:
	}
}

// tryBurst injects cfg.Burst extra tokens once the burst timer fires,
// reporting whether it did.
func (d *driver) tryBurst(ctx context.Context, tokens chan<- struct{}, burstAt <-chan time.Time) bool {
	select {
	case <-burstAt:
	default:
		return false
	}
	for range d.cfg.Burst {
		sendToken(ctx, tokens)
	}
	return true
}

func sleepUntil(ctx context.Context, t time.Time) {
	wait := time.Until(t)
	if wait <= 0 {
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(wait):
	}
}

// offered is the load the pacer intended to generate; compare with enqueued to
// see whether the enqueue path kept up.
func (d *driver) offered() int64 {
	switch d.cfg.Scenario {
	case scenarioIdle:
		return 0
	case scenarioDrain:
		return int64(d.cfg.Preload)
	case scenarioBurst:
		return int64(d.cfg.Rate*d.cfg.Duration.Seconds()) + int64(d.cfg.Burst)
	default:
		return int64(d.cfg.Rate * d.cfg.Duration.Seconds())
	}
}

// enqueueOne pushes one task through the production enqueue path. The counter
// shape reads and increments one of cfg.HotRows rows inside the enqueue
// transaction, which is the same read-modify-write + task-insert commit device
// creation performs against its host row.
func (d *driver) enqueueOne(ctx context.Context) {
	t := task.New(taskTypeSynthetic, fmt.Sprintf("load-%d", d.seq.Add(1)))
	t.MaxAttempts = d.cfg.MaxAttempts
	if d.cfg.Scenario == scenarioKeyed {
		// Same priority tier as the keyed reconciles: the claim orders by
		// priority DESC, so only equal-or-lower work sits behind an ineligible
		// keyed head, which is the starvation shape this scenario measures.
		t.Priority = task.PriorityResync
	}

	start := time.Now()
	var err error
	if d.cfg.EnqueueShape == shapeCounter {
		row := int64(rand.IntN(d.cfg.HotRows)) //nolint:gosec // load spread
		_, err = d.platform.ReadWriteTransaction(ctx, func(ctx context.Context, txn *gspanner.ReadWriteTransaction) error {
			r, err := txn.ReadRow(ctx, countersTable, gspanner.Key{row}, []string{"n"})
			if err != nil {
				return fmt.Errorf("reading counter %d: %w", row, err)
			}
			var n int64
			if err := r.Column(0, &n); err != nil {
				return fmt.Errorf("scanning counter: %w", err)
			}
			return txn.BufferWrite([]*gspanner.Mutation{
				gspanner.Update(countersTable, []string{"counter_id", "n"}, []any{row, n + 1}),
				d.queue.EnqueueMutation(ctx, t),
			})
		})
	} else {
		err = d.queue.Enqueue(ctx, t)
	}
	d.enqLat.observe(time.Since(start))

	if err != nil {
		if ctx.Err() == nil {
			d.enqErrs.Add(1)
		}
		return
	}
	d.enqueued.Add(1)
	if d.cfg.EnqueueShape == shapeCounter {
		d.counterEnq.Add(1)
	}
	d.recordEnqueued(t.ID)
}

// recordEnqueued adds a committed task to the conservation ground-truth set
// and tallies the ones scripted to dead-letter, which the report reconciles
// against the DLQ.
func (d *driver) recordEnqueued(id string) {
	o := outcomeFor(id, d.cfg.PoisonRate, d.cfg.TransientRate)
	if o == outcomePoison {
		d.expectPoison.Add(1)
	}
	d.enqMu.Lock()
	d.enqIDs[id] = o
	d.enqMu.Unlock()
}

// enqSnapshot copies the enqueued-ID set for the checker.
func (d *driver) enqSnapshot() map[string]outcome {
	d.enqMu.Lock()
	defer d.enqMu.Unlock()
	out := make(map[string]outcome, len(d.enqIDs))
	maps.Copy(out, d.enqIDs)
	return out
}

// preload bulk-inserts pending tasks in batches, bypassing rate pacing and the
// counter shape (a backlog's commit shape does not matter for drain
// measurement). Used by the drain scenario before any worker starts.
func (d *driver) preload(ctx context.Context, n int) error {
	const batch = 500
	muts := make([]*gspanner.Mutation, 0, batch)
	flush := func() error {
		if len(muts) == 0 {
			return nil
		}
		if _, err := d.platform.Apply(ctx, muts); err != nil {
			return fmt.Errorf("preload batch: %w", err)
		}
		muts = muts[:0]
		return nil
	}
	for range n {
		t := task.New(taskTypeSynthetic, fmt.Sprintf("load-%d", d.seq.Add(1)))
		t.MaxAttempts = d.cfg.MaxAttempts
		muts = append(muts, d.queue.EnqueueMutation(ctx, t))
		d.enqueued.Add(1)
		d.recordEnqueued(t.ID)
		if len(muts) == batch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

// spawn starts one instance subprocess and wires up snapshot/log collection.
func (d *driver) spawn(idx int) error {
	cfgJSON, err := json.Marshal(d.cfg)
	if err != nil {
		return fmt.Errorf("serializing config: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating executable: %w", err)
	}
	// Children hang off the process-lifetime context: if the driver dies, they
	// get SIGTERM (graceful final snapshot), then SIGKILL after a bound.
	// Planned chaos kills and the load-phase context are managed separately.
	cmd := exec.CommandContext(d.baseCtx, exe, "-role=instance", "-config="+string(cfgJSON),
		"-instance-index="+strconv.Itoa(idx))
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 30 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting instance %d: %w", idx, err)
	}

	p := &instProc{idx: idx, cmd: cmd, done: make(chan struct{})}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			var s snapshot
			if json.Unmarshal(sc.Bytes(), &s) != nil {
				continue
			}
			p.mu.Lock()
			if s.Kind == "final" {
				p.final = &s
			} else {
				p.last = &s
			}
			p.mu.Unlock()
		}
	}()
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			fmt.Fprintf(os.Stderr, "[inst%d] %s\n", idx, sc.Text())
		}
	}()
	go d.watchExit(p)

	d.mu.Lock()
	d.procs = append(d.procs, p)
	d.mu.Unlock()
	return nil
}

// watchExit reaps one instance subprocess. An exit nobody commanded (no
// SIGKILL nemesis (p.dead), no configured self-kill by instance 0 under
// -kill-after-effects, no driver shutdown, no interrupt) is a panic or a
// fatal in production code, and stays a finding even when the surviving
// instances drain the queue.
func (d *driver) watchExit(p *instProc) {
	err := p.cmd.Wait()
	selfKill := d.cfg.KillAfterEffects > 0 && p.idx == 0
	p.mu.Lock()
	commanded := p.dead
	p.mu.Unlock()
	if !commanded && !selfKill && !d.terminating.Load() && d.baseCtx.Err() == nil {
		status := "exit status 0"
		if err != nil {
			status = err.Error()
		}
		d.mu.Lock()
		d.instDeaths = append(d.instDeaths, fmt.Sprintf(
			"instance %d (pid %d) exited uncommanded mid-run: %s", p.idx, p.cmd.Process.Pid, status))
		d.mu.Unlock()
	}
	close(p.done)
}

// chaos SIGKILLs a random live instance every cfg.KillEvery and spawns a
// replacement after cfg.RestartAfter, modeling an instance crash plus Cloud
// Run replacing it. Killed leases must be reclaimed by the sweeper.
func (d *driver) chaos(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.KillEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		victim := d.pickAlive()
		if victim == nil {
			continue
		}
		d.mu.Lock()
		d.kills++
		d.mu.Unlock()

		victim.mu.Lock()
		victim.dead = true
		victim.mu.Unlock()
		fmt.Printf("chaos: SIGKILL instance %d (pid %d)\n", victim.idx, victim.cmd.Process.Pid)
		_ = victim.cmd.Process.Kill()

		// The respawn joins the nemesis group so terminate() cannot race a
		// late spawn: run() waits for the group before draining.
		idx := victim.idx
		d.nemesis.Go(func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(d.cfg.RestartAfter):
			}
			if ctx.Err() != nil {
				return
			}
			if err := d.spawn(idx); err != nil {
				fmt.Printf("chaos: respawn of instance %d failed: %v\n", idx, err)
			} else {
				fmt.Printf("chaos: instance %d respawned\n", idx)
			}
		})
	}
}

// awaitDrain polls queue depth until pending and leased both reach zero,
// reporting whether the queue emptied within cfg.DrainTimeout. Dead-lettered
// (failed) tasks are terminal and do not block draining.
func (d *driver) awaitDrain(ctx context.Context) bool {
	fmt.Println("load phase done; waiting for the queue to drain...")
	deadline := time.Now().Add(d.cfg.DrainTimeout)
	for time.Now().Before(deadline) {
		stats, err := d.queue.Stats(ctx)
		if err == nil && stats.Pending == 0 && stats.Leased == 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
	}
	fmt.Printf("drain timeout (%v) exceeded\n", d.cfg.DrainTimeout)
	return false
}

// terminate gracefully stops all live instances (SIGTERM, bounded wait) so
// they emit final snapshots.
func (d *driver) terminate() {
	d.terminating.Store(true)
	d.mu.Lock()
	procs := make([]*instProc, len(d.procs))
	copy(procs, d.procs)
	d.mu.Unlock()

	for _, p := range procs {
		p.mu.Lock()
		dead := p.dead
		p.mu.Unlock()
		if !dead {
			// SIGCONT first: a stopped process cannot handle SIGTERM, and a
			// pause window may have been cut short by shutdown. Resuming a
			// running process is a no-op.
			_ = p.cmd.Process.Signal(syscall.SIGCONT)
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
		}
	}
	for _, p := range procs {
		select {
		case <-p.done:
		case <-time.After(30 * time.Second):
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	}
}

// sample periodically records queue depth plus instance-counter deltas to the
// console and snapshots.csv until its context is canceled.
func (d *driver) sample(ctx context.Context, started time.Time, done chan<- struct{}) {
	defer close(done)
	csv, err := os.Create(filepath.Join(d.cfg.RunDir, "snapshots.csv"))
	if err != nil {
		fmt.Printf("snapshots.csv unavailable: %v\n", err)
		return
	}
	defer csv.Close()
	fmt.Fprintln(csv, "elapsed_s,pending,leased,failed,oldest_pending_s,enqueued,processed,claims,claim_empty,claim_errs,swept")

	var prev sums
	prevT := time.Now()
	ticker := time.NewTicker(d.cfg.SnapshotEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		stats, err := d.queue.Stats(ctx)
		if err != nil {
			continue
		}
		cur := d.sumSnaps()
		dt := time.Since(prevT).Seconds()
		elapsed := time.Since(started)

		fmt.Fprintf(csv, "%.0f,%d,%d,%d,%.1f,%d,%d,%d,%d,%d,%d\n",
			elapsed.Seconds(), stats.Pending, stats.Leased, stats.Failed,
			stats.OldestPendingAge.Seconds(), d.enqueued.Load(), cur.processed,
			cur.claims, cur.claimEmpty, cur.claimErrs, cur.swept)

		emptyPct := 0.0
		if cur.claims > 0 {
			emptyPct = 100 * float64(cur.claimEmpty) / float64(cur.claims)
		}
		fmt.Printf("[%4.0fs] pending=%d leased=%d dlq=%d oldest=%.1fs | enq %d | done %.1f/s | claims %.1f/s empty %.0f%% p99 %v\n",
			elapsed.Seconds(), stats.Pending, stats.Leased, stats.Failed,
			stats.OldestPendingAge.Seconds(), d.enqueued.Load(),
			rate(cur.processed-prev.processed, dt), rate(cur.claims-prev.claims, dt),
			emptyPct, cur.claimLat.percentile(0.99))

		prev = cur
		prevT = time.Now()
	}
}

func rate(delta int64, seconds float64) float64 {
	if seconds <= 0 {
		return 0
	}
	return float64(delta) / seconds
}

// sums aggregates cumulative instance counters across all processes, using
// each process's final snapshot when it exited gracefully or its last
// periodic one when it was killed.
type sums struct {
	processed                                                                  int64
	probes, probeEmpty                                                         int64
	claims, claimEmpty, claimTasks, claimErrs, completes, fails, sweeps, swept int64
	keyedProcessed                                                             int64
	claimLat, completeLat, startLat                                            histSnap
	staleCounters                                                              bool // some instance died without a final snapshot
}

func (d *driver) sumSnaps() sums {
	d.mu.Lock()
	defer d.mu.Unlock()
	var s sums
	for _, p := range d.procs {
		snap := p.latest()
		if snap == nil {
			continue
		}
		if snap.Kind != "final" {
			p.mu.Lock()
			if p.dead {
				s.staleCounters = true
			}
			p.mu.Unlock()
		}
		s.processed += snap.Processed
		s.probes += snap.Probes
		s.probeEmpty += snap.ProbeEmpty
		s.claims += snap.Claims
		s.claimEmpty += snap.ClaimEmpty
		s.claimTasks += snap.ClaimTasks
		s.claimErrs += snap.ClaimErrs
		s.completes += snap.Completes
		s.fails += snap.Fails
		s.sweeps += snap.Sweeps
		s.swept += snap.Swept
		s.keyedProcessed += snap.KeyedProcessed
		s.claimLat.merge(snap.ClaimLat)
		s.completeLat.merge(snap.CompleteLat)
		s.startLat.merge(snap.StartLat)
	}
	return s
}

func (d *driver) killCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.kills
}

func (d *driver) pauseCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pauses
}

func (d *driver) dbFaultCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dbFaults
}

// sumCounters verifies the counter-shape invariant: the sum over
// loadtest_counters must equal the number of successfully enqueued tasks,
// because each enqueue transaction incremented exactly one row.
func (d *driver) sumCounters(ctx context.Context) (int64, error) {
	iter := d.raw.Single().Query(ctx, gspanner.Statement{SQL: "SELECT COALESCE(SUM(n), 0) FROM loadtest_counters"})
	defer iter.Stop()
	row, err := iter.Next()
	if err != nil {
		return 0, fmt.Errorf("summing counters: %w", err)
	}
	var n int64
	if err := row.Column(0, &n); err != nil {
		return 0, fmt.Errorf("scanning counter sum: %w", err)
	}
	return n, nil
}
