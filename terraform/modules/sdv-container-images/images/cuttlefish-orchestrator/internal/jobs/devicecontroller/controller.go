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

package devicecontroller

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/errgroup"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// maxConcurrentHostChecks caps the fan-out of per-host health check
// goroutines so a large fleet doesn't spawn unbounded concurrent HTTP calls.
const maxConcurrentHostChecks = 8

// defaultFailureThreshold marks a device unhealthy after this many
// consecutive failures when the config leaves FailureThreshold unset.
const defaultFailureThreshold = 3

// Service executes periodic health checks on active devices.
//
// It runs as a background daemon, polling the Host Orchestrator for CVD status
// and updating the device repository. It detects "zombie" devices that drift
// from the expected state.
type Service struct {
	config          device.HealthCheckConfig
	deviceRepo      device.Repository
	hostRepo        host.Repository
	hoClientFactory *hoclient.Factory
	logger          *slog.Logger
	runner          telemetry.Runner

	// Runtime answers to "may this loop act on what it detects", read once per
	// cycle. Unset, the compiled HealthCheckConfig values stand.
	sweepPolicy  func(context.Context) (bool, error)
	zombiePolicy func(context.Context) (bool, error)

	checksPerformed atomic.Int64
	// statusMu guards the two maps: cycleCounts accumulates during a cycle
	// (per-host goroutines), lastCounts is the completed-cycle snapshot the
	// gauge reports.
	statusMu    sync.Mutex
	cycleCounts map[device.HealthStatus]int64
	lastCounts  map[device.HealthStatus]int64

	// CVD census state. firstSeen records when an unclaimed group was first
	// observed (key "hostID/group"); censusMu guards it across the per-host
	// goroutines. cycleOrphans accumulates during a cycle; orphanCVDs is the
	// completed-cycle gauge value.
	censusMu        sync.Mutex
	censusFirstSeen map[string]time.Time
	cycleOrphans    atomic.Int64
	orphanCVDs      atomic.Int64
	orphanCVDsSwept atomic.Int64

	// suspector, when wired, files device.reconcile suspicion for stale
	// teardowns and zombie recovery (nil = detectors off).
	suspector *ReconcileSuspector

	// remediator, when wired, files host.remediate for degraded hosts that
	// have settled to no live devices (nil = sweep off).
	remediator *HostRemediator

	// capture, when wired, files device.capture_bugreport for error devices
	// that have not yet been captured, gated by the runtime capture-on-error
	// policy (nil = detector off).
	capture *CaptureSuspector

	// operations, when wired, reports which devices have a live MUTATING
	// operation, such as a restart or powerwash in flight. Those devices are
	// owned by their operation for its duration, so this controller observes them
	// and does not act (nil = no guard, the pre-operations behavior).
	operations LiveOperationLens
}

// LiveOperationLens reports which of the given resources have a live mutating
// operation, and which instance of each it actuates. The Spanner
// OperationsRepository satisfies it.
type LiveOperationLens interface {
	LiveByResources(ctx context.Context, resourceType string, ids []string) (operation.LiveSet, error)
}

// WithLiveOperations installs the in-flight-verb guard.
//
// Without it the device controller settles a lifecycle status from whatever `cvd
// fleet` reports mid-verb, and a powerwash legitimately passes through Stopped
// and, during first boot, BootFailed. Those derive `stopped` and `error`, so the
// row contradicts a verb that is working correctly, and a user reading it can
// act on the lie (starting a device mid-powerwash). It self-heals once the CVD is
// up, but the window is the whole verb.
func WithLiveOperations(lens LiveOperationLens) Option {
	return func(s *Service) { s.operations = lens }
}

// Option configures a Service.
type Option func(*Service)

// WithSweepPolicy supplies the runtime answer to whether the census may
// delete the orphan CVD groups it finds.
func WithSweepPolicy(fn func(context.Context) (bool, error)) Option {
	return func(s *Service) {
		s.sweepPolicy = fn
	}
}

// WithZombiePolicy supplies the runtime answer to whether a detected zombie is
// recreated or only recorded.
func WithZombiePolicy(fn func(context.Context) (bool, error)) Option {
	return func(s *Service) {
		s.zombiePolicy = fn
	}
}

// mayAct resolves one runtime policy, falling back to the compiled default
// when none is wired. A failed read resolves to detect-only: a Spanner blip
// must never be the reason a loop starts deleting or recreating.
func (s *Service) mayAct(ctx context.Context, policy func(context.Context) (bool, error), compiled bool, what string) bool {
	if policy == nil {
		return compiled
	}
	allowed, err := policy(ctx)
	if err != nil {
		s.logger.Warn("reading a sweep policy failed; staying detect-only",
			slog.String("policy", what), slog.Any("error", err))
		return false
	}
	return allowed
}

// WithLeaderCheck installs a leadership predicate; health checks run only
// while it returns true (or when unset), so just the leader polls hosts.
func WithLeaderCheck(fn func() bool) Option {
	return func(s *Service) {
		s.runner.LeaderCheck = fn
	}
}

// New creates the device controller with the given configuration.
// A non-positive FailureThreshold defaults to 3.
// The service does not start automatically; call Start() to begin monitoring.
func New(
	config device.HealthCheckConfig,
	deviceRepo device.Repository,
	hostRepo host.Repository,
	hoClientFactory *hoclient.Factory,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	if config.FailureThreshold <= 0 {
		config.FailureThreshold = defaultFailureThreshold
	}
	s := &Service{
		config:          config,
		deviceRepo:      deviceRepo,
		hostRepo:        hostRepo,
		hoClientFactory: hoClientFactory,
		logger:          logger,
		cycleCounts:     make(map[device.HealthStatus]int64),
		lastCounts:      make(map[device.HealthStatus]int64),
		censusFirstSeen: make(map[string]time.Time),
	}
	s.runner = telemetry.Runner{
		Interval:   config.Interval,
		Cycle:      s.runHealthChecks,
		Metrics:    telemetry.NewLoopMetrics("devicecontroller"),
		RunAtStart: true,
	}
	for _, opt := range opts {
		opt(s)
	}
	s.initMetrics()
	return s
}

// initMetrics registers the health gauge and check counters. Instruments are
// no-ops without a meter provider, so this is always safe. The per-status
// device gauge keeps its own callback: its series set is dynamic (one per
// observed health status), which the shared counter registration cannot
// express.
func (s *Service) initMetrics() {
	telemetry.RegisterCounters("devicecontroller", []telemetry.ObservedCounter{
		{Name: "cco.devicecontroller.checks", Description: "Device health checks performed.", Value: &s.checksPerformed},
		{Name: "cco.devicecontroller.orphan_cvds", Description: "CVD groups no device row claims, as of the last census. Leaked host RAM; any nonzero value is an anomaly.", Value: &s.orphanCVDs, Gauge: true},
		{Name: "cco.devicecontroller.orphan_cvds_swept", Description: "Orphan CVD groups deleted by the census (only with sweeping enabled).", Value: &s.orphanCVDsSwept},
	})
	meter := otel.Meter(telemetry.ScopeName("devicecontroller"))
	devices, _ := meter.Int64ObservableGauge("cco.devicecontroller.devices",
		metric.WithDescription("Devices by health status, as of the last completed check cycle."))
	_, _ = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s.statusMu.Lock()
		defer s.statusMu.Unlock()
		for status, n := range s.lastCounts {
			o.ObserveInt64(devices, n, metric.WithAttributes(attribute.String("status", string(status))))
		}
		return nil
	}, devices)
}

// recordStatuses accumulates one batch of results into the current cycle's
// status tally.
func (s *Service) recordStatuses(updates []device.HealthUpdate) {
	s.checksPerformed.Add(int64(len(updates)))
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	for _, u := range updates {
		s.cycleCounts[u.Status]++
	}
}

// publishCycleCounts snapshots the finished cycle's tally for the gauge and
// resets the accumulator.
func (s *Service) publishCycleCounts() {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.lastCounts = s.cycleCounts
	s.cycleCounts = make(map[device.HealthStatus]int64)
}

// Start begins the periodic health checks. It returns immediately.
func (s *Service) Start(ctx context.Context) {
	s.runner.Start(ctx)
	s.logger.Info("device controller started",
		slog.Duration("interval", s.config.Interval),
	)
}

// Stop gracefully stops the device controller, canceling ongoing checks and
// waiting for the monitor loop to exit.
func (s *Service) Stop() {
	s.runner.Stop()
	s.logger.Info("device controller stopped")
}

// runHealthChecks runs one full cycle: per-host device checks and census,
// then the suspicion detectors. A non-nil error marks the cycle unsuccessful
// for the loop's staleness metric.
func (s *Service) runHealthChecks(ctx context.Context) error {
	devices, err := s.deviceRepo.ListNeedingHealthCheck(ctx, s.config.Interval)
	if err != nil {
		s.logger.Error("failed to list devices for health check", slog.Any("error", err))
		return fmt.Errorf("listing devices for health check: %w", err)
	}

	devicesByHost := make(map[string][]*device.Device)
	for _, d := range devices {
		// Stopped rides along for drift only: a CVD running under a stopped row
		// heals to running, and no host-side failure can degrade it (see
		// atRest). Every other status is owned by its transition task.
		if d.Status == device.StatusRunning || d.Status == device.StatusStarting ||
			d.Status == device.StatusStopped {
			devicesByHost[d.HostID] = append(devicesByHost[d.HostID], d)
		}
	}

	// Visit every RUNNING host, including device-less ones. The census is a
	// per-HOST drift check: leaked CVD groups retire on hosts whose devices
	// are gone, which is exactly why nothing else visits them.
	// Hosts that are not running but still have checkable devices get the
	// accurate failure records via the same per-host visit. STARTING hosts are
	// visited too: reaching their Host Orchestrator is what promotes them to
	// running (the ho_reachable condition), and nothing else probes them.
	visited := make(map[string]bool, len(devicesByHost))
	var g errgroup.Group
	g.SetLimit(maxConcurrentHostChecks)
	for _, h := range s.listHostsToProbe(ctx) {
		visited[h.ID] = true
		g.Go(func() error {
			s.visitHost(ctx, h, h.ID, devicesByHost[h.ID])
			return nil
		})
	}
	for hostID, hostDevices := range devicesByHost {
		if visited[hostID] {
			continue
		}
		visited[hostID] = true
		g.Go(func() error {
			s.visitHost(ctx, nil, hostID, hostDevices)
			return nil
		})
	}
	_ = g.Wait() // visitHost never returns an error; Wait only joins
	s.pruneCensus(visited)
	s.suspectStaleTeardowns(ctx)
	s.suspectStrandedCreates(ctx)
	s.suspectZombies(ctx)
	s.suspectDegradedHosts(ctx)
	s.suspectFailedCaptures(ctx)
	s.publishCycleCounts()
	s.orphanCVDs.Store(s.cycleOrphans.Swap(0))
	return nil
}
