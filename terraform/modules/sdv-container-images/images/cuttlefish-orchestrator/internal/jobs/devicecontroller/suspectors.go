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
	"log/slog"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// Stale-transitional suspicion bounds (the device-side twin of the host
// reconciler's stale-transitional host scan): a device sitting in a task-owned state
// (deleting, or pending/creating) with no status write for this long has
// likely lost its transition task (admin task deletion, a bug). Suspicion is
// verified against the live queue and healed by device.reconcile, coalesced
// by the keyed queue while pending. Long-running work is protected by the
// live-task verify, not this threshold: a leased task is a live owner however
// long it runs.
const (
	// Shared with the create sampler's enrichment dwell and the HO client's
	// operation-wait ceiling via device.TransientProgressWindow. Three
	// mechanisms ask "how long may a transient sit unexplained", and
	// deriving them from one constant keeps them from drifting apart. This
	// is the CREATE-side threshold: pending/creating legitimately sit
	// task-owned for long stretches, so suspicion must dwell.
	staleTransitionalAfter = device.TransientProgressWindow

	// staleTeardownAfter is the teardown-side threshold, decoupled from the
	// create side and much shorter. The delete commits its mark and its
	// cleanup enqueue in ONE transaction, so a `deleting` row with no live
	// cleanup task is a lost task (admin deletion, a bug, a row from before
	// the single-commit delete) and not a race to wait out. Every extra
	// minute of dwell is a minute the row lies. The suspicion is verified
	// against the live queue before acting regardless; two minutes is padding
	// for health-cycle timing (interval 1m), not a guard. Short enough that
	// the robustness harness's quiesce window can watch the heal happen
	// instead of flagging the strand.
	staleTeardownAfter = 2 * time.Minute

	maxTeardownSuspectsPerRun = 20
	maxCreateSuspectsPerRun   = 20
	// Zombie recovery: a running device whose CVD died reaches
	// health_status=zombie after ZombieThreshold failed checks (~10 min); the
	// suspector then files device.reconcile to recreate it, capped per cycle.
	maxZombieSuspectsPerRun = 20
)

// maxRemediateEnqueuesPerRun caps how many degraded hosts a single sweep files
// remediation for; the rest are picked up next cycle.
const maxRemediateEnqueuesPerRun = 20

// maxCaptureSuspectsPerRun caps how many error devices a single sweep files a
// capture task for; the rest are picked up next cycle.
const maxCaptureSuspectsPerRun = 20

// ActiveTaskChecker verifies whether a live task of the given type already
// owns a resource. This is the dedup every suspector runs before filing work.
// The Spanner TaskQueue satisfies it.
type ActiveTaskChecker interface {
	HasActive(ctx context.Context, taskType, resourceID string) (bool, error)
}

// TaskEnqueuer enqueues a task directly. The Spanner TaskQueue satisfies it.
type TaskEnqueuer interface {
	Enqueue(ctx context.Context, t *task.Task) error
}

// ResyncEnqueuer files reconciler-priority work into the durable queue
// (keyed reconcile types coalesce while pending). The Spanner TaskQueue
// satisfies it.
type ResyncEnqueuer interface {
	EnqueueResync(ctx context.Context, taskType, resourceID string, delay time.Duration) error
}

// ReconcileSuspector wires the device.reconcile suspicion detectors for stale
// teardowns, stranded creates, and zombie recovery. Tasks verifies a
// suspicion against the live queue (a device with a live owning task needs no
// suspicion), and Enqueuer files keyed device.reconcile suspicion at resync
// priority. Both come from the task queue; the option keeps the controller
// free of a queue dependency when unwired.
type ReconcileSuspector struct {
	Tasks    ActiveTaskChecker
	Enqueuer ResyncEnqueuer
}

// HostRemediator wires the degraded-host sweep: Tasks verifies no live
// remediation task already owns the host, Enqueuer files host.remediate. It is
// the sole trigger for poison remediation, since the create path only fences,
// so a lost enqueue self-heals on the next sweep, and a host that was busy at
// degrade time is remediated once its creates settle. Both come from the task
// queue; the option keeps the controller free of a queue dependency when unwired.
type HostRemediator struct {
	Tasks    ActiveTaskChecker
	Enqueuer TaskEnqueuer
}

// CaptureSuspector wires the failed-capture detector (policy-gated
// capture-on-error): Enabled reads the artifacts.capture_on_error policy each
// cycle so the admin toggle takes effect without a redeploy (a pure wiring
// closure, because the policy store is not a queue capability), Tasks
// verifies a device has no live capture task already, and Enqueuer files
// device.capture_bugreport at resync priority. The option keeps the controller
// free of a queue and policy dependency when unwired.
type CaptureSuspector struct {
	Enabled  func(ctx context.Context) (bool, error)
	Tasks    ActiveTaskChecker
	Enqueuer ResyncEnqueuer
}

// WithReconcileSuspector enables the device.reconcile suspicion detectors.
func WithReconcileSuspector(rs ReconcileSuspector) Option {
	return func(s *Service) {
		s.suspector = &rs
	}
}

// WithHostRemediator enables the degraded-host poison-remediation sweep.
func WithHostRemediator(hr HostRemediator) Option {
	return func(s *Service) {
		s.remediator = &hr
	}
}

// WithCaptureSuspector enables the policy-gated capture-on-error detector.
func WithCaptureSuspector(cs CaptureSuspector) Option {
	return func(s *Service) {
		s.capture = &cs
	}
}

// fileSuspicion is the shared tail of every detector: verify no live
// verifyType task already owns the resource, then file the suspicion. It
// reports whether one was filed. A live owner skips silently; an indeterminate
// check and a failed enqueue are logged. All three are re-observed next cycle.
//
// The dedup check's error is logged rather than folded into "already owned",
// which is what it used to be. Re-observation only rescues a TRANSIENT failure.
// A persistent one, such as the checker's query being refused, would otherwise
// stop every detector filing anything, forever, and look exactly like a quiet
// fleet. It carries no guard-degraded marker on purpose: the failure declines
// to act instead of acting unprotected, and stretching that marker on its first
// application is how it becomes noise.
//
// Best-effort dedup by construction: the transition_key unique index covers
// only the four transition task types, so detector filings cannot use the
// race-free EnsureTransition insert, and this check-then-enqueue has a TOCTOU
// window. A racing duplicate is harmless everywhere it is used (keyed
// reconcile filings coalesce in the queue; remediation and capture handlers
// re-verify); extend the index if a detector's dedup ever needs to be
// race-free.
func (s *Service) fileSuspicion(ctx context.Context, tasks ActiveTaskChecker, verifyType, resourceID, what string, file func(context.Context) error) bool {
	active, err := tasks.HasActive(ctx, verifyType, resourceID)
	if err != nil {
		s.logger.Warn("could not check for an existing "+what+"; not filing this cycle",
			slog.String("resource_id", resourceID), slog.Any("error", err))
		return false
	}
	if active {
		return false
	}
	if err := file(ctx); err != nil {
		s.logger.Warn("failed to enqueue "+what,
			slog.String("resource_id", resourceID), slog.Any("error", err))
		return false
	}
	return true
}

// reconcileFiler files keyed device.reconcile suspicion for one device. Every
// reconcile-suspicion detector shares this filing.
func (s *Service) reconcileFiler(deviceID string) func(context.Context) error {
	return func(ctx context.Context) error {
		return s.suspector.Enqueuer.EnqueueResync(ctx, task.TypeDeviceReconcile, deviceID, 0)
	}
}

// suspectStaleTeardowns files device.reconcile suspicion for devices stuck
// 'deleting' past staleTeardownAfter with no live cleanup task. Without
// this, the only thing that ever revisits them is the host's eventual
// teardown. The single-commit delete writes no `deleting` state itself, so
// this scan is a safety net for rows that predate it or whose cleanup task
// was lost.
func (s *Service) suspectStaleTeardowns(ctx context.Context) {
	if s.suspector == nil {
		return
	}
	deleting := device.StatusDeleting
	rows, err := s.deviceRepo.List(ctx, device.ListFilter{Status: &deleting})
	if err != nil {
		s.logger.Warn("stale-teardown scan failed to list deleting devices", slog.Any("error", err))
		return
	}
	suspected := 0
	for _, d := range rows {
		if suspected >= maxTeardownSuspectsPerRun {
			break
		}
		if time.Since(d.UpdatedAt) < staleTeardownAfter {
			continue
		}
		if !s.fileSuspicion(ctx, s.suspector.Tasks, task.TypeDeviceCleanupCVD, d.ID,
			"device reconcile suspicion", s.reconcileFiler(d.ID)) {
			continue
		}
		s.logger.Warn("device stuck deleting with no live cleanup task; filed reconcile suspicion",
			slog.String("device_id", d.ID),
			slog.Duration("stale_for", time.Since(d.UpdatedAt).Round(time.Second)))
		suspected++
	}
}

// suspectStrandedCreates files device.reconcile suspicion for devices stuck
// pending or creating past staleTransitionalAfter with no live create task.
// It is the create-side twin of the stale-teardown detector, and the
// device-side symmetric of the host reconciler's stranded-transitional scan.
// Create tasks are enqueued atomically with their status writes, so a
// stranded create is rare (admin task deletion, a bug); before this detector,
// only queue redelivery ever revisited one. The reconcile handler heals it by
// re-ensuring the create task from the row's persisted desired_spec
// (race-free via the transition_key insert), so a false positive costs one
// no-op reconcile. A legitimately slow create is protected by the live-task
// verify: its leased task is a live owner however long the artifact fetch
// runs.
func (s *Service) suspectStrandedCreates(ctx context.Context) {
	if s.suspector == nil {
		return
	}
	suspected := 0
	for _, st := range []device.Status{device.StatusPending, device.StatusCreating} {
		rows, err := s.deviceRepo.List(ctx, device.ListFilter{Status: &st})
		if err != nil {
			s.logger.Warn("stranded-create scan failed to list devices",
				slog.String("status", string(st)), slog.Any("error", err))
			continue
		}
		for _, d := range rows {
			if suspected >= maxCreateSuspectsPerRun {
				return
			}
			if time.Since(d.UpdatedAt) < staleTransitionalAfter {
				continue
			}
			if !s.fileSuspicion(ctx, s.suspector.Tasks, task.TypeDeviceCreateCVD, d.ID,
				"stranded-create reconcile suspicion", s.reconcileFiler(d.ID)) {
				continue
			}
			s.logger.Warn("device stuck mid-create with no live create task; filed reconcile suspicion",
				slog.String("device_id", d.ID),
				slog.String("status", string(st)),
				slog.Duration("stale_for", time.Since(d.UpdatedAt).Round(time.Second)))
			suspected++
		}
	}
}

// suspectZombies files device.reconcile for running devices whose CVD died
// (health_status=zombie) so the reconcile handler recreates it from the
// persisted desired_spec (bounded budget) or fails it explicitly. Gated on
// ZombieRecreate: without it a zombie is detected but not acted on (today's
// behavior). A zombie on a dead host is left to the create path (postpone/fail
// on a not-ready/gone host) and the host reconciler's lost-host cascade, the
// same fallbacks the reconcile heal relies on, so no host pre-check is needed
// here.
func (s *Service) suspectZombies(ctx context.Context) {
	if s.suspector == nil || !s.mayAct(ctx, s.zombiePolicy, s.config.ZombieRecreate, "zombie_recreate") {
		return
	}
	running := device.StatusRunning
	zombie := device.HealthStatusZombie
	rows, err := s.deviceRepo.List(ctx, device.ListFilter{Status: &running, HealthStatus: &zombie})
	if err != nil {
		s.logger.Warn("zombie scan failed to list devices", slog.Any("error", err))
		return
	}
	suspected := 0
	for _, d := range rows {
		if suspected >= maxZombieSuspectsPerRun {
			break
		}
		// A live create task already owns the recovery (a recreate in flight).
		if !s.fileSuspicion(ctx, s.suspector.Tasks, task.TypeDeviceCreateCVD, d.ID,
			"zombie reconcile suspicion", s.reconcileFiler(d.ID)) {
			continue
		}
		s.logger.Warn("running device is a zombie (CVD died under it); filed reconcile to recreate",
			slog.String("device_id", d.ID))
		suspected++
	}
}

// suspectDegradedHosts files host.remediate for poison-degraded hosts that have
// settled to no live devices. This is the reliable remediation trigger. A
// degraded host still carrying live devices is left fenced for an operator
// (a host-wide `cvd reset` would destroy those tenants); a host with only
// error/torn-down rows is exactly the empty poisoned host the reset recovers.
// Skips hosts that already have a live remediate task so retries are not
// duplicated.
func (s *Service) suspectDegradedHosts(ctx context.Context) {
	if s.remediator == nil {
		return
	}
	degraded := true
	hosts, err := s.hostRepo.List(ctx, host.ListFilter{Degraded: &degraded})
	if err != nil {
		s.logger.Warn("degraded-host scan failed to list hosts", slog.Any("error", err))
		return
	}
	enqueued := 0
	for _, h := range hosts {
		if enqueued >= maxRemediateEnqueuesPerRun {
			break
		}
		live, err := s.deviceRepo.CountLiveByHost(ctx, h.ID)
		if err != nil {
			s.logger.Warn("degraded-host sweep failed to count live devices",
				slog.String("host_id", h.ID), slog.Any("error", err))
			continue
		}
		if live > 0 {
			continue // tenanted: leave fenced for an operator, do not auto-reset
		}
		if !s.fileSuspicion(ctx, s.remediator.Tasks, task.TypeHostRemediate, h.ID,
			"host remediation", func(ctx context.Context) error {
				return s.remediator.Enqueuer.Enqueue(ctx, task.New(task.TypeHostRemediate, h.ID))
			}) {
			continue
		}
		s.logger.Warn("degraded host has no live devices; filed poison remediation",
			slog.String("host_id", h.ID))
		enqueued++
	}
}

// suspectFailedCaptures files device.capture_bugreport for error devices that
// have not yet been captured, when the runtime capture-on-error policy is on.
// The policy is read each cycle (the admin Artifacts toggle takes effect
// without a redeploy); a device already carrying a live capture task is skipped
// so retries are not duplicated. The handler stamps bugreport_captured_at on a
// successful capture or when the device is un-capturable, so this scan (which
// filters on that column being NULL) converges instead of re-enqueuing forever.
func (s *Service) suspectFailedCaptures(ctx context.Context) {
	if s.capture == nil {
		return
	}
	enabled, err := s.capture.Enabled(ctx)
	if err != nil {
		s.logger.Warn("failed-capture scan could not read the capture-on-error policy", slog.Any("error", err))
		return
	}
	if !enabled {
		return
	}
	errored := device.StatusError
	rows, err := s.deviceRepo.List(ctx, device.ListFilter{Status: &errored, BugreportUncaptured: true})
	if err != nil {
		s.logger.Warn("failed-capture scan could not list error devices", slog.Any("error", err))
		return
	}
	enqueued := 0
	for _, d := range rows {
		if enqueued >= maxCaptureSuspectsPerRun {
			break
		}
		if !s.fileSuspicion(ctx, s.capture.Tasks, task.TypeDeviceCaptureBugreport, d.ID,
			"capture-on-error bugreport", func(ctx context.Context) error {
				return s.capture.Enqueuer.EnqueueResync(ctx, task.TypeDeviceCaptureBugreport, d.ID, 0)
			}) {
			continue
		}
		s.logger.Info("error device not yet captured; filed capture-on-error bugreport",
			slog.String("device_id", d.ID))
		enqueued++
	}
}
