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

package batchsettle

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/batch"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// Config configures the settle loop.
type Config struct {
	// Enabled gates the loop.
	Enabled bool
	// Interval is the time between cycles. Defaults to 1 minute.
	Interval time.Duration
	// BatchSize bounds the batch-row GC and drained-host listings per cycle.
	BatchSize int
	// DryRun logs what would be settled or deleted without acting.
	DryRun bool
}

// Store is the batch-policy persistence the loop drives. Satisfied by the
// batch feature's Repository. MarkReady/MarkFailed report whether the call
// won the settle transition. Only the winner tears members down, so two
// instances sweeping the same batch cannot double-judge it.
type Store interface {
	ListUnsettledAtomic(ctx context.Context) ([]*batch.Batch, error)
	MarkReady(ctx context.Context, id string, at time.Time) (bool, error)
	MarkFailed(ctx context.Context, id string, at time.Time, reason string) (bool, error)
	// ListFailedWithLiveMembers returns tripped atomic batches whose member
	// devices are still outside teardown. The winner died between MarkFailed
	// and the member deletes, and nothing else revisits a settled batch.
	ListFailedWithLiveMembers(ctx context.Context, limit int) ([]string, error)
	ListEmptyBefore(ctx context.Context, cutoff time.Time, limit int) ([]string, error)
	Delete(ctx context.Context, id string) error
}

// DeviceTeardown is the slice of the device service the settle loop drives:
// members are listed through the same List users hit and torn down through
// the batched delete. The device feature's DeviceService satisfies it.
type DeviceTeardown interface {
	List(ctx context.Context, filter device.ListFilter) ([]*device.Device, error)
	BatchDelete(ctx context.Context, devices []*device.Device) (deleted int, err error)
}

// HostReclaimer deletes drained batch hosts through the same durable
// deprovision path users call. The host feature's HostService satisfies it.
type HostReclaimer interface {
	Delete(ctx context.Context, id string) error
}

// DrainedHostLister lists batch-provisioned hosts whose emptiness has aged
// past the grace window. The host repository satisfies it.
type DrainedHostLister interface {
	ListEmptyBatchHosts(ctx context.Context, cutoff time.Time, limit int) ([]*host.Host, error)
}

// Deps bundles the loop's collaborators; all are required.
type Deps struct {
	Store      Store
	Devices    DeviceTeardown
	Hosts      HostReclaimer
	HostLister DrainedHostLister
}

// Service runs the batch settlement loop.
type Service struct {
	config     Config
	store      Store
	devices    DeviceTeardown
	hosts      HostReclaimer
	hostLister DrainedHostLister
	logger     *slog.Logger
	runner     telemetry.Runner

	// Settlement shares the reaper's dry-run posture: they are one operator
	// decision about whether the platform deletes right now, and letting them
	// diverge would make "deletions suspended" untrue on half the loops.
	dryRunPolicy func(context.Context) (bool, error)

	totalReadied    atomic.Int64
	totalTripped    atomic.Int64
	totalHostsFreed atomic.Int64
	totalFailures   atomic.Int64
}

// WithDryRunPolicy supplies the runtime answer to whether deletions are
// suspended, reading the same policy the reaper reads. Unset, Config.DryRun
// stands.
func WithDryRunPolicy(fn func(context.Context) (bool, error)) Option {
	return func(s *Service) {
		s.dryRunPolicy = fn
	}
}

// resolveDryRun answers whether this cycle deletes anything; a failed read
// suspends deletion rather than resuming it, matching the reaper.
func (s *Service) resolveDryRun(ctx context.Context) bool {
	if s.dryRunPolicy == nil {
		return s.config.DryRun
	}
	dryRun, err := s.dryRunPolicy(ctx)
	if err != nil {
		s.logger.Warn("reading the reaper dry-run policy failed; suspending settlement deletions this cycle",
			slog.Any("error", err))
		return true
	}
	return dryRun
}

// Option configures a Service.
type Option func(*Service)

// New creates the settle loop. A non-positive Interval defaults to one minute.
func New(config Config, deps Deps, logger *slog.Logger, opts ...Option) *Service {
	if config.Interval <= 0 {
		config.Interval = time.Minute
	}
	s := &Service{
		config:     config,
		store:      deps.Store,
		devices:    deps.Devices,
		hosts:      deps.Hosts,
		hostLister: deps.HostLister,
		logger:     logger,
	}
	s.runner = telemetry.Runner{
		Interval:   config.Interval,
		Cycle:      s.settle,
		Metrics:    telemetry.NewLoopMetrics("batchsettle"),
		RunAtStart: true,
	}
	for _, opt := range opts {
		opt(s)
	}
	telemetry.RegisterCounters("batchsettle", []telemetry.ObservedCounter{
		{Name: "cco.batchsettle.batches_readied", Description: "Atomic batches the sweep latched as filled.", Value: &s.totalReadied},
		{Name: "cco.batchsettle.batches_failed", Description: "Atomic batches the sweep tripped and tore down.", Value: &s.totalTripped},
		{Name: "cco.batchsettle.batch_hosts_reaped", Description: "Drained exclusive batch hosts reclaimed ahead of TTL.", Value: &s.totalHostsFreed},
		{Name: "cco.batchsettle.failures", Description: "Settle, collect, or reclaim operations that failed.", Value: &s.totalFailures},
	})
	return s
}

// Start begins the settle loop. It returns immediately; a disabled loop never
// starts.
func (s *Service) Start(ctx context.Context) {
	if !s.config.Enabled {
		return
	}
	s.runner.Start(ctx)
}

// Stop halts the loop, draining an in-flight cycle before returning.
func (s *Service) Stop() {
	s.runner.Stop()
}

// settle runs one full cycle. Errors are logged within the individual phases;
// the returned error only marks the cycle unsuccessful for the loop's
// staleness metric.
func (s *Service) settle(ctx context.Context) error {
	start := time.Now()
	// One answer for the whole cycle, passed down instead of cached on the
	// service, so a sweep can never read a stale "delete".
	dryRun := s.resolveDryRun(ctx)
	settled, settleFailed := s.sweepAtomicBatches(ctx, dryRun)
	resumed, resumeFailed := s.resumeFailedTeardowns(ctx, dryRun)
	collected, collectFailed := s.collectEmptyBatches(ctx, dryRun)
	hostsFreed, hostsFailed := s.reapDrainedHosts(ctx, dryRun)

	failedOps := settleFailed + resumeFailed + collectFailed + hostsFailed
	s.totalFailures.Add(int64(failedOps))

	if settled > 0 || resumed > 0 || collected > 0 || hostsFreed > 0 || failedOps > 0 {
		s.logger.Info("batch settle cycle complete",
			slog.Int("batches_settled", settled),
			slog.Int("teardowns_resumed", resumed),
			slog.Int("batches_collected", collected),
			slog.Int("batch_hosts_reaped", hostsFreed),
			slog.Int("failed_ops", failedOps),
			slog.Duration("duration", time.Since(start)),
		)
	}
	if failedOps > 0 {
		return fmt.Errorf("%d batch settle operations failed", failedOps)
	}
	return nil
}
