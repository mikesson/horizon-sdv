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

package reaper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// Config configures the reaper service behavior.
type Config struct {
	// Enabled determines whether the reaper runs. Defaults to true.
	Enabled bool

	// Interval is the time between reaper cycles. Defaults to 1 minute.
	Interval time.Duration

	// BatchSize is the FLOOR on how many resources one cycle may reap. The
	// effective bound is a share of the population the sweep draws from (see
	// reapFleetShare) or this, whichever is larger, so a fleet of ten behaves
	// exactly as it always has and a fleet of a hundred thousand does not fall
	// behind its own expiry rate.
	BatchSize int

	// DryRun logs what would be reaped without actually deleting resources.
	DryRun bool
}

// Service enforces resource retention policies in the background.
//
// It automatically identifies and terminates expired ephemeral resources (Devices/Hosts)
// that are not "pinned" or "protected", freeing up capacity and preventing cost leaks.
type Service struct {
	config             Config
	deviceService      DeviceReaper
	batchDeviceService BatchDeviceService // Optional: for batch operations
	hostService        HostReaper
	deviceRepo         DeviceExpiryLister
	hostRepo           HostExpiryLister
	logger             *slog.Logger
	runner             telemetry.Runner

	// dryRunPolicy is the runtime answer to "are deletions suspended", read
	// once at the top of each cycle and passed down. Unset, Config.DryRun
	// stands.
	dryRunPolicy func(context.Context) (bool, error)

	totalDevicesReaped atomic.Int64
	totalHostsReaped   atomic.Int64
	totalPinsLapsed    atomic.Int64
	totalFailures      atomic.Int64
}

// DeviceReaper is the slice of the device service the reaper drives: lapsed
// pins are removed through UnpinLapsed and expired devices deleted through the
// same Delete users call. The device feature's DeviceService satisfies it.
type DeviceReaper interface {
	// Delete stops and removes a device.
	Delete(ctx context.Context, id string) error
	// UnpinLapsed unpins a device whose pinned_until has passed, re-checking
	// that inside its transaction. A device re-pinned since the listing is
	// refused with device.ErrInvalidState.
	UnpinLapsed(ctx context.Context, id, userID string) (*device.Device, error)
}

// BatchDeviceService is the optional batched-teardown capability, detected by
// type assertion on the device reaper.
type BatchDeviceService interface {
	// BatchDelete deletes multiple devices efficiently with batched database operations.
	BatchDelete(ctx context.Context, devices []*device.Device) (deleted int, err error)
}

// HostReaper is the slice of the host service the reaper drives: expired
// hosts are deleted through the same durable-deprovision path users call.
// The host feature's HostService satisfies it.
type HostReaper interface {
	// DeleteExpired marks a host for deletion and enqueues deprovisioning,
	// re-checking inside its transaction that the host is ephemeral, holds no
	// protected devices and is past expires_at. A host extended or protected
	// since the listing is refused with host.ErrInvalidState.
	DeleteExpired(ctx context.Context, id string) error
}

// DeviceExpiryLister defines the repository queries behind the device sweeps.
// It is a subset of the device repository focused on expiration.
type DeviceExpiryLister interface {
	// ListExpired returns devices whose TTL has passed and are eligible for deletion.
	ListExpired(ctx context.Context, limit int) ([]*device.Device, error)
	// ListLapsedPins returns pinned devices whose pinned_until has passed.
	ListLapsedPins(ctx context.Context, limit int) ([]*device.Device, error)
	// CountUndeleted sizes the two listings above against the population they
	// are drawn from. Read ONCE per cycle, never per item.
	CountUndeleted(ctx context.Context) (int64, error)
}

// HostExpiryLister defines the repository query behind the host expiry sweep.
type HostExpiryLister interface {
	// ListExpired returns hosts whose TTL has passed and have no pinned devices.
	ListExpired(ctx context.Context, limit int) ([]*host.Host, error)
	// CountUndeleted sizes that listing, read once per cycle.
	//
	// Hosts are counted separately from devices rather than one number sizing
	// both sweeps: the two populations differ by the packing density, so at 3-50
	// devices per host a shared figure would either starve the device sweeps or
	// hand the host sweep a budget two orders of magnitude past its own
	// population.
	CountUndeleted(ctx context.Context) (int64, error)
}

// Deps bundles the reaper's collaborators; all are required.
type Deps struct {
	DeviceService DeviceReaper
	HostService   HostReaper
	DeviceRepo    DeviceExpiryLister
	HostRepo      HostExpiryLister
}

// Option configures a Service.
type Option func(*Service)

// WithDryRunPolicy supplies the runtime answer to whether deletions are
// suspended, read once per cycle so an operator can pause reaping without a
// redeploy. Unset, the compiled Config value stands.
func WithDryRunPolicy(fn func(context.Context) (bool, error)) Option {
	return func(s *Service) {
		s.dryRunPolicy = fn
	}
}

// New creates a new reaper service. A non-positive Interval defaults to one
// minute (time.NewTicker panics on zero).
// If Deps.DeviceService implements BatchDeviceService, batch delete operations will be used
// for better performance.
func New(config Config, deps Deps, logger *slog.Logger, opts ...Option) *Service {
	if config.Interval <= 0 {
		config.Interval = time.Minute
	}
	s := &Service{
		config:        config,
		deviceService: deps.DeviceService,
		hostService:   deps.HostService,
		deviceRepo:    deps.DeviceRepo,
		hostRepo:      deps.HostRepo,
		logger:        logger,
	}

	// Check if device service supports batch operations
	if batchSvc, ok := deps.DeviceService.(BatchDeviceService); ok {
		s.batchDeviceService = batchSvc
	}

	s.runner = telemetry.Runner{
		Interval:   config.Interval,
		Cycle:      s.reap,
		Metrics:    telemetry.NewLoopMetrics("reaper"),
		RunAtStart: true,
	}
	for _, opt := range opts {
		opt(s)
	}
	s.initMetrics()
	return s
}

// initMetrics registers observable counters for the reaper's actions.
// Instruments are no-ops without a meter provider, so this is always safe.
func (s *Service) initMetrics() {
	telemetry.RegisterCounters("reaper", []telemetry.ObservedCounter{
		{Name: "cco.reaper.devices_reaped", Description: "Expired devices deleted.", Value: &s.totalDevicesReaped},
		{Name: "cco.reaper.hosts_reaped", Description: "Expired hosts deleted.", Value: &s.totalHostsReaped},
		{Name: "cco.reaper.pins_lapsed", Description: "Devices auto-unpinned after pinned_until passed.", Value: &s.totalPinsLapsed},
		{Name: "cco.reaper.failures", Description: "Reap or unpin operations that failed.", Value: &s.totalFailures},
	})
}

// Start begins the reaper background loop. It returns immediately; a disabled
// reaper never starts.
func (s *Service) Start(ctx context.Context) {
	if !s.config.Enabled {
		return
	}
	s.runner.Start(ctx)
}

// Stop gracefully stops the reaper background loop, draining an in-flight
// reap cycle before returning.
func (s *Service) Stop() {
	s.runner.Stop()
}

// reap runs one full cycle across every sweep. Errors are logged within the
// individual phases; the returned error only marks the cycle unsuccessful for
// the loop's staleness metric.
func (s *Service) reap(ctx context.Context) error {
	startTime := time.Now()
	// One answer for the whole cycle, passed down rather than cached on the
	// service: a sweep must never be able to read a stale "delete".
	dryRun := s.resolveDryRun(ctx)
	// One sizing read per population per cycle, and never per item: the bound is
	// a share of what each sweep can reach, so it has to know how much that is.
	deviceLimit := s.sweepLimit(ctx, "device", s.deviceRepo.CountUndeleted)
	hostLimit := s.sweepLimit(ctx, "host", s.hostRepo.CountUndeleted)
	// Lapsed pins are handled first: unpinning restores a fresh ephemeral TTL,
	// so a just-lapsed pin gets a normal wind-down rather than being caught by
	// the expiry sweep in the same cycle.
	pinsLapsed, pinsFailed := s.unpinLapsedPins(ctx, dryRun, deviceLimit)
	deviceCount, deviceFailed := s.reapExpiredDevices(ctx, dryRun, deviceLimit)
	hostCount, hostFailed := s.reapExpiredHosts(ctx, dryRun, hostLimit)

	failedOps := pinsFailed + deviceFailed + hostFailed
	s.totalFailures.Add(int64(failedOps))

	if pinsLapsed > 0 || deviceCount > 0 || hostCount > 0 || failedOps > 0 {
		s.logger.Info("reaper cycle complete",
			slog.Int("pins_lapsed", pinsLapsed),
			slog.Int("pins_failed", pinsFailed),
			slog.Int("devices_reaped", deviceCount),
			slog.Int("devices_failed", deviceFailed),
			slog.Int("hosts_reaped", hostCount),
			slog.Int("hosts_failed", hostFailed),
			slog.Duration("duration", time.Since(startTime)),
		)
	}
	if failedOps > 0 {
		return fmt.Errorf("%d reap operations failed", failedOps)
	}
	return nil
}

// resolveDryRun answers whether this cycle deletes anything. A failed read
// suspends deletion rather than resuming it: an operator who paused reaping
// during an incident must not have it resumed by a transient Spanner error,
// and a cycle that deletes nothing is recoverable where one that deletes is
// not.
func (s *Service) resolveDryRun(ctx context.Context) bool {
	if s.dryRunPolicy == nil {
		return s.config.DryRun
	}
	dryRun, err := s.dryRunPolicy(ctx)
	if err != nil {
		s.logger.Warn("reading the reaper dry-run policy failed; suspending deletions this cycle",
			slog.Any("error", err))
		return true
	}
	return dryRun
}

// reaperActor is the audit identity for lifecycle changes the reaper makes.
const reaperActor = "system:reaper"

// reapFleetShare is the share of a sweep's own population one cycle may list.
//
// A blast radius first. Acting on at most a thirty-second of the population per
// cycle means a fleet something wrongly decided was entirely expired takes half
// an hour to walk rather than a minute. That is the same converge-over-cycles
// property the fleet keeper's budgets have, and long enough for the dry-run
// policy, which is re-read at the top of every cycle, to stop it.
//
// It is also the throughput the fleet needs, because the two questions have the
// same answer. In steady state a population divided by its mean lifetime IS its
// expiry rate, so 50k-100k devices living ~1.85 h retire ~900 a minute, and a
// serial batch of 50 a minute falls eighteenfold behind. A thirty-second of that
// population is 1,500-3,100 a minute, which drains a backlog instead of merely
// keeping pace with one.
//
// Under a few thousand rows the share is smaller than Config.BatchSize and the
// floor stands, so a fleet of ten reaps exactly as it always has.
const reapFleetShare = 32

// reapConcurrency bounds the per-item actions one sweep runs at once.
//
// Each item is one Spanner transaction plus an enqueue, and they are
// independent, so the serial loop this replaces made a cycle as long as its
// batch. It stays small because the sweep shares a database with the request
// handlers, and items that share a host contend on that host's counter row,
// where a wide fan-out would spend the gain on aborts.
const reapConcurrency = 8

// sweepLimit is how many rows one sweep may list this cycle: the configured
// batch floor, or a share of the population it draws from, whichever is larger.
//
// A failed count leaves the floor standing rather than failing the cycle.
// Sizing only widens a bound, so a cycle that could not read the population
// still reaps, at exactly the rate it reaped before the sizing existed.
func (s *Service) sweepLimit(ctx context.Context, what string, count func(context.Context) (int64, error)) int {
	live, err := count(ctx)
	if err != nil {
		s.logger.Warn("sizing the "+what+" sweep failed; the batch floor stands",
			slog.Any("error", err))
		return s.config.BatchSize
	}
	return max(s.config.BatchSize, int(live/reapFleetShare))
}

// sweep describes one list-then-act reap path for reapEach.
type sweep[T any] struct {
	// what names the resource in logs ("lapsed pin", "expired device").
	what string
	// list returns the candidates (already bounded by the caller's limit).
	list func(context.Context) ([]T, error)
	// act performs the reap action on one candidate.
	act func(context.Context, T) error
	// attrs identifies one candidate in logs.
	attrs func(T) []any
	// tolerate, when set, classifies an act error as a concurrent win to skip
	// silently rather than a failure.
	tolerate func(error) bool
	// counter, when set, is incremented per successful action.
	counter *atomic.Int64
	// actedMsg, when non-empty, logs each successful action.
	actedMsg string
}

// reapEach runs one sweep: list the candidates, then act on each under a
// bounded group. Dry-run logs and skips the action, a tolerated error is a
// concurrent win, any other error is logged and counted. It returns (acted,
// failed); a failed listing counts as one failure.
//
// No goroutine ever returns an error, by design: the group would cancel the
// rest of the batch on the first one, where the serial loop this replaces logged
// it and carried on. The group is here for its concurrency bound alone.
//
// It recovers no panic, and neither did the serial loop. A panic in an item
// takes down the process that also serves the API either way. That is the
// standard to hold items to, not something the fan-out changed.
func reapEach[T any](ctx context.Context, s *Service, dryRun bool, sw sweep[T]) (int, int) {
	items, err := sw.list(ctx)
	if err != nil {
		s.logger.Error("failed to list "+sw.what+"s", slog.Any("error", err))
		return 0, 1
	}
	if dryRun {
		// Serial, because nothing is acted on: there is no work to spread, and
		// the log reads in the order the listing returned.
		for _, item := range items {
			s.logger.Info("dry run: would act on "+sw.what, sw.attrs(item)...)
		}
		return 0, 0
	}
	var acted, failed atomic.Int64
	var group errgroup.Group
	group.SetLimit(reapConcurrency)
	for _, item := range items {
		group.Go(func() error {
			if err := sw.act(ctx, item); err != nil {
				if sw.tolerate == nil || !sw.tolerate(err) {
					s.logger.Error("failed to act on "+sw.what,
						append(sw.attrs(item), slog.Any("error", err))...)
					failed.Add(1)
				}
				return nil
			}
			acted.Add(1)
			if sw.counter != nil {
				sw.counter.Add(1)
			}
			if sw.actedMsg != "" {
				s.logger.Info(sw.actedMsg, sw.attrs(item)...)
			}
			return nil
		})
	}
	_ = group.Wait()
	return int(acted.Load()), int(failed.Load())
}

// unpinLapsedPins enforces pinned_until: a pin past its expiry is removed via
// UnpinLapsed, which restores a fresh ephemeral TTL and releases the host's
// protected count in one commit. A concurrent user unpin or re-pin that wins
// the race is not a failure.
func (s *Service) unpinLapsedPins(ctx context.Context, dryRun bool, limit int) (int, int) {
	return reapEach(ctx, s, dryRun, sweep[*device.Device]{
		what: "lapsed pin",
		list: func(ctx context.Context) ([]*device.Device, error) {
			return s.deviceRepo.ListLapsedPins(ctx, limit)
		},
		act: func(ctx context.Context, d *device.Device) error {
			if _, err := s.deviceService.UnpinLapsed(ctx, d.ID, reaperActor); err != nil {
				return fmt.Errorf("unpinning device %s: %w", d.ID, err)
			}
			return nil
		},
		attrs: func(d *device.Device) []any {
			out := []any{slog.String("device_id", d.ID)}
			if d.Lifecycle.PinnedUntil != nil {
				out = append(out, slog.Time("pinned_until", *d.Lifecycle.PinnedUntil))
			}
			return out
		},
		tolerate: func(err error) bool { return errors.Is(err, device.ErrInvalidState) },
		counter:  &s.totalPinsLapsed,
		actedMsg: "unpinned device with lapsed pin",
	})
}

// reapExpiredDevices deletes expired devices, preferring the batched teardown
// when the device service supports it.
func (s *Service) reapExpiredDevices(ctx context.Context, dryRun bool, limit int) (int, int) {
	if s.batchDeviceService == nil {
		// Individual deletes (N+1 pattern) for device services without the
		// batched capability.
		return reapEach(ctx, s, dryRun, sweep[*device.Device]{
			what: "expired device",
			list: func(ctx context.Context) ([]*device.Device, error) {
				return s.deviceRepo.ListExpired(ctx, limit)
			},
			act:     func(ctx context.Context, d *device.Device) error { return s.deviceService.Delete(ctx, d.ID) },
			attrs:   func(d *device.Device) []any { return []any{slog.String("device_id", d.ID)} },
			counter: &s.totalDevicesReaped,
		})
	}

	devices, err := s.deviceRepo.ListExpired(ctx, limit)
	if err != nil {
		s.logger.Error("failed to list expired devices", slog.Any("error", err))
		return 0, 1
	}
	if len(devices) == 0 {
		return 0, 0
	}
	if dryRun {
		s.logger.Info("dry run: would reap expired devices", slog.Int("count", len(devices)))
		return 0, 0
	}
	deleted, err := s.batchDeviceService.BatchDelete(ctx, devices)
	failed := 0
	if err != nil {
		s.logger.Error("batch delete failed",
			slog.Int("deleted", deleted),
			slog.Any("error", err),
		)
		failed = 1
	}
	s.totalDevicesReaped.Add(int64(deleted))
	return deleted, failed
}

// reapExpiredHosts deletes hosts whose TTL has passed and that hold no pinned
// devices. The listing is a snapshot; DeleteExpired re-checks both under its
// transaction, and a host extended or protected since is a concurrent win.
func (s *Service) reapExpiredHosts(ctx context.Context, dryRun bool, limit int) (int, int) {
	return reapEach(ctx, s, dryRun, sweep[*host.Host]{
		what: "expired host",
		list: func(ctx context.Context) ([]*host.Host, error) {
			return s.hostRepo.ListExpired(ctx, limit)
		},
		act:      func(ctx context.Context, h *host.Host) error { return s.hostService.DeleteExpired(ctx, h.ID) },
		attrs:    func(h *host.Host) []any { return []any{slog.String("host_id", h.ID)} },
		tolerate: func(err error) bool { return errors.Is(err, host.ErrInvalidState) },
		counter:  &s.totalHostsReaped,
	})
}
