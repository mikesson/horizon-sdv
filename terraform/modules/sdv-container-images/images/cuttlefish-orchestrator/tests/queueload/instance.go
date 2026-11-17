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
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/jobs/leaderelection"
	"cloud-cuttlefish-orchestrator/internal/jobs/taskreclaimer"
	"cloud-cuttlefish-orchestrator/internal/jobs/taskworker"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

// snapshot is one stats emission from an instance, written as a JSON line on
// stdout. Counters and histograms are cumulative, so the driver can use the
// last line seen from a SIGKILLed instance as a (slightly stale) final value.
type snapshot struct {
	Kind string    `json:"kind"` // "snap" (periodic) or "final" (graceful exit)
	Idx  int       `json:"idx"`
	PID  int       `json:"pid"`
	T    time.Time `json:"t"`

	Processed    int64 `json:"processed"`
	Failed       int64 `json:"failed"`
	DeadLettered int64 `json:"dead_lettered"`

	Probes     int64 `json:"probes"`
	ProbeEmpty int64 `json:"probe_empty"`
	Claims     int64 `json:"claims"`
	ClaimEmpty int64 `json:"claim_empty"`
	ClaimTasks int64 `json:"claim_tasks"`
	ClaimErrs  int64 `json:"claim_errs"`
	Completes  int64 `json:"completes"`
	Fails      int64 `json:"fails"`
	Sweeps     int64 `json:"sweeps"`
	Swept      int64 `json:"swept"`

	KeyedProcessed int64 `json:"keyed_processed,omitempty"`

	ClaimLat    histSnap `json:"claim_lat"`
	CompleteLat histSnap `json:"complete_lat"`
	StartLat    histSnap `json:"start_lat"`
}

// newSweeperElector elects this instance on the same background-plane lease
// production runs all its periodic jobs on; here it gates the one job this
// harness composes.
//
// The TTL is shortened from the elector's own default to this harness's clock:
// a run lasts tens of seconds, so a SIGKILLed sweeper has to hand over inside
// one. Otherwise the lease reclaim this harness measures happens after the
// measuring has stopped.
func newSweeperElector(client *spanner.Client, holder string, logger *slog.Logger) *leaderelection.Elector {
	return leaderelection.New(leaderelection.Config{
		Name: leaderelection.BackgroundPlaneLease, Holder: holder, TTL: 10 * time.Second,
	}, spanner.NewLeaseRepository(client), logger)
}

// runInstance simulates one orchestrator instance: its own Spanner client and
// session pool, the production TaskQueue, worker pool, sweeper, and leader
// elector. That is everything the internal/app composition root runs around
// the queue, minus the API.
// It stops on SIGTERM (graceful drain); the driver SIGKILLs it to model a
// crash.
func runInstance(cfg Cfg) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Clock-skew nemesis: shift THIS instance's lease arithmetic (claim expiry,
	// sweep comparisons, leader leases) before any component starts. Ledger
	// timestamps stay on the unshifted clock so histories remain comparable.
	if skew := cfg.skewFor(cfg.InstanceIndex); skew != 0 {
		spanner.SetClockOffsetForTesting(skew)
		fmt.Fprintf(os.Stderr, "clock skew applied: %v\n", skew)
	}

	client, err := newPlatformClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	holder := leaderelection.InstanceID()
	led, err := openLedger(cfg.RunDir, cfg.InstanceIndex, os.Getpid())
	if err != nil {
		return err
	}
	// The same in-process wake the orchestrator wires: the driver enqueues from
	// its OWN process, so an instance only ever wakes itself. That leaves the
	// cross-instance case for the poll floor to cover on its own.
	waker := task.NewWaker(cfg.Workers)
	queue := &instrumentedQueue{
		inner: spanner.NewTaskQueue(client, spanner.WithShards(cfg.Shards), spanner.WithHolder(holder),
			spanner.WithLogger(logger), spanner.WithWaker(waker)),
		led:    led,
		holder: holder,
	}
	handler := &syntheticHandler{cfg: cfg, ledger: led, holder: holder, startLat: &hist{}}
	handlers, keyed := registeredHandlers(cfg, handler)

	pool, err := taskworker.New(taskworker.Config{
		Enabled:       true,
		MinWorkers:    cfg.MinWorkers,
		MaxWorkers:    cfg.Workers,
		NumShards:     cfg.Shards,
		ClaimBatch:    cfg.ClaimBatch,
		LeaseDuration: cfg.Lease,
		HandleTimeout: cfg.HandleTO,
		PollMin:       cfg.PollMin,
		PollMax:       cfg.PollMax,
		RetryBase:     cfg.RetryBase,
		RetryMax:      cfg.RetryMax,
	}, queue, handlers, logger, taskworker.WithWaker(waker))
	if err != nil {
		return fmt.Errorf("building worker pool: %w", err)
	}

	elector := newSweeperElector(client, holder, logger)
	elector.Start(ctx)
	sweeper := taskreclaimer.New(taskreclaimer.Config{Enabled: true, Interval: cfg.SweepEvery}, queue, logger,
		taskreclaimer.WithLeaderCheck(elector.IsLeader))
	sweeper.Start(ctx)
	pool.Start(ctx)

	emit := func(kind string) {
		s := snapshot{Kind: kind, Idx: cfg.InstanceIndex, PID: os.Getpid(), T: time.Now()}
		ps := pool.Stats()
		s.Processed, s.Failed, s.DeadLettered = ps.Processed, ps.Failed, ps.DeadLettered
		s.Probes = queue.probes.Load()
		s.ProbeEmpty = queue.probeEmpty.Load()
		s.Claims = queue.claims.Load()
		s.ClaimEmpty = queue.claimEmpty.Load()
		s.ClaimTasks = queue.claimTasks.Load()
		s.ClaimErrs = queue.claimErrs.Load()
		s.Completes = queue.completes.Load()
		s.Fails = queue.fails.Load()
		s.Sweeps = queue.sweeps.Load()
		s.Swept = queue.swept.Load()
		s.ClaimLat = queue.claimLat.snapshot()
		s.CompleteLat = queue.completeLat.snapshot()
		s.StartLat = handler.startLat.snapshot()
		if keyed != nil {
			s.KeyedProcessed = keyed.processed.Load()
		}
		line, err := json.Marshal(s)
		if err != nil {
			logger.Warn("snapshot encode", slog.Any("error", err))
			return
		}
		fmt.Fprintf(os.Stdout, "%s\n", line)
	}

	ticker := time.NewTicker(cfg.SnapshotEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			emit("snap")
		case <-ctx.Done():
			// Graceful drain: in-flight handlers finish (bookkeeping is detached
			// from cancellation in the pool), then report final numbers.
			pool.Stop()
			sweeper.Stop()
			elector.Stop()
			emit("final")
			if err := led.close(); err != nil {
				logger.Warn("ledger close", slog.Any("error", err))
			}
			return nil
		}
	}
}

// registeredHandlers assembles the handlers an instance registers: the
// synthetic one always, plus the keyed one when the scenario churns keyed
// reconciles (returned separately so the snapshot can read its counter).
func registeredHandlers(cfg Cfg, syn *syntheticHandler) ([]task.Handler, *keyedHandler) {
	if cfg.Scenario != scenarioKeyed {
		return []task.Handler{syn}, nil
	}
	keyed := &keyedHandler{cfg: cfg}
	return []task.Handler{syn, keyed}, keyed
}

// newPlatformClient builds the production Spanner platform client (session
// pool, channels, instrumentation) against the configured database. An
// instance whose index has a fault proxy dials the emulator through it, so the
// driver can inject latency, blackholes, and connection cuts on this
// instance's DB link alone.
func newPlatformClient(ctx context.Context, cfg Cfg) (*spanner.Client, error) {
	scfg := spanner.DefaultConfig()
	scfg.ProjectID = cfg.Project
	scfg.InstanceID = cfg.Instance
	scfg.DatabaseID = cfg.Database
	scfg.Emulator = cfg.Emulator
	if cfg.InstanceIndex < len(cfg.ProxyAddrs) && cfg.ProxyAddrs[cfg.InstanceIndex] != "" {
		scfg.Emulator = cfg.ProxyAddrs[cfg.InstanceIndex]
	}
	client, err := spanner.NewClient(ctx, scfg)
	if err != nil {
		return nil, fmt.Errorf("creating spanner client: %w", err)
	}
	return client, nil
}
