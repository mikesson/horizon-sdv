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

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"cloud-cuttlefish-orchestrator/internal/config"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	devicefeature "cloud-cuttlefish-orchestrator/internal/features/device"
	"cloud-cuttlefish-orchestrator/internal/features/device/cvd"
	gcecatalogfeature "cloud-cuttlefish-orchestrator/internal/features/gcecatalog"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	"cloud-cuttlefish-orchestrator/internal/jobs/batchsettle"
	"cloud-cuttlefish-orchestrator/internal/jobs/catalogsync"
	"cloud-cuttlefish-orchestrator/internal/jobs/devicecontroller"
	"cloud-cuttlefish-orchestrator/internal/jobs/fleetkeeper"
	"cloud-cuttlefish-orchestrator/internal/jobs/hostreconciler"
	"cloud-cuttlefish-orchestrator/internal/jobs/leaderelection"
	"cloud-cuttlefish-orchestrator/internal/jobs/reaper"
	"cloud-cuttlefish-orchestrator/internal/jobs/taskreclaimer"
	"cloud-cuttlefish-orchestrator/internal/jobs/taskworker"
	"cloud-cuttlefish-orchestrator/internal/jobs/warmplane"
	"cloud-cuttlefish-orchestrator/internal/platform/gce"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// jobs bundles the background services for ordered shutdown.
type jobs struct {
	workerPool     *taskworker.Pool
	reclaimer      *taskreclaimer.Service
	controller     *devicecontroller.Service
	reaper         *reaper.Service
	batchSettle    *batchsettle.Service
	hostReconciler *hostreconciler.Service
	catalogSync    *catalogsync.Service
	gceCatalog     *gce.Catalog
	fleetKeeper    *fleetkeeper.Service
	elector        *leaderelection.Elector
}

// fleetKeeperActor marks keeper-created hosts in created_by (informational,
// like every created_by). The VALUE is frozen: the robustness harness carries a
// second copy of it and censuses keeper hosts with a SQL literal, so changing
// the string breaks that census with no error to read.
const fleetKeeperActor = "poolkeeper"

// fleetKeeperOrigin is the provenance stamped on a keeper top-up. Unlike
// fleetKeeperActor above, behavior depends on it: it is what puts the host in
// the managed fleet class, so stamping it as a user's host would leave the
// keeper unable to count or reclaim the inventory it just provisioned.
const fleetKeeperOrigin = host.OriginPool

// fleetProvisioner adapts the device service's declarative host-template port
// and the host service's create into the keeper's one-call port. The keeper
// stays out of internal/features, and a standby host is exactly the host a
// no-capacity declarative create would have provisioned.
type fleetProvisioner struct {
	devices *devicefeature.DeviceService
	hosts   *hostfeature.HostService
}

// ProvisionFleetHost resolves the policy-default headless host template for the
// pool that is short and creates the host as the keeper actor.
//
// The origin is what actually marks this host as the keeper's. created_by is
// informational, and the template is the same one a no-capacity declarative
// create resolves, so nothing else on the row distinguishes the two. The pool
// rides the resolved template, so the machine joins the pool the buy answered.
func (p fleetProvisioner) ProvisionFleetHost(ctx context.Context, pool string) (string, error) {
	hs, err := p.devices.ResolveFleetHostSpec(ctx, pool)
	if err != nil {
		return "", fmt.Errorf("resolving standby host spec: %w", err)
	}
	h, err := p.hosts.Create(ctx, *hs.Spec, fleetKeeperActor, fleetKeeperOrigin)
	if err != nil {
		return "", fmt.Errorf("creating standby host: %w", err)
	}
	return h.ID, nil
}

// buildJobs constructs the background jobs over the wired feature graph and
// starts the background plane's elector before them (the jobs themselves start
// later, via startJobs).
func buildJobs(ctx context.Context, cfg *config.Config, logger *slog.Logger, c *clients, f *featureSet) (jobs, error) {
	// Leader election: the loops that must not run twice at once share ONE
	// lease, so autoscaling does not multiply GCE calls, table scans, or
	// deletes. Each of them takes this same isLeader through its WithLeaderCheck
	// option, and shutdown releases the one lease.
	//
	// The reaper and the batch-settle loop are NOT among them. Every action they
	// take is idempotent under a concurrent double-run, so the gate bought
	// nothing and cost the fleet a serialization point on the two loops whose
	// work grows with it. Their listings de-correlate instead.
	//
	// It starts here instead of beside the jobs because acquiring leadership is
	// the slow part (a Spanner round trip), and the loops skip their cycles
	// until it lands, so starting it first costs a job nothing and saves the
	// first cycle.
	elector := leaderelection.New(
		leaderelection.Config{Name: leaderelection.BackgroundPlaneLease, Holder: f.instanceID},
		f.leaseRepo, logger)
	elector.Start(ctx)
	isLeader := elector.IsLeader

	// What each loop may do on its own is a runtime setting, read once per
	// cycle: enabling a destructive sweep must not need a redeploy, and the
	// operator who enables one must be able to take it back just as fast.
	sweepPolicy := func(key string) func(context.Context) (bool, error) {
		return func(ctx context.Context) (bool, error) {
			cfg, err := f.sysConfigRepo.Get(ctx, key)
			if err != nil {
				if errors.Is(err, spanner.ErrKeyNotFound) {
					return false, nil
				}
				return false, fmt.Errorf("reading %s policy: %w", key, err)
			}
			return cfg.AsBool(), nil
		}
	}

	// Host reconciler. Zones default to wherever hosts actually live (queried
	// per cycle); the managed labels decide which unclaimed VMs are this
	// install's orphans.
	reconcilerConfig := hostreconciler.Config{
		ProjectID:      cfg.GCP.ProjectID,
		Zones:          cfg.HostReconciler.Zones,
		SyncInterval:   time.Duration(cfg.HostReconciler.Interval),
		InstanceLabels: managedByLabels(cfg),
	}
	reconcilerJob := hostreconciler.New(reconcilerConfig, c.gce.instances, f.hostRepo, logger,
		hostreconciler.WithQuotaService(f.quotaService),
		hostreconciler.WithOperationsClient(c.gce.operations),
		// A tripped zone recovery breaker also cools the zone for new Spot
		// placement (a preemption-storm zone_health cell).
		hostreconciler.WithZoneHealth(f.zoneHealthRepo),
		// Preemptions are audited: rows die with the fleet, so the audit trail
		// is what the operations view's preemption KPIs count.
		hostreconciler.WithAuditLogger(f.auditLogger),
		// Auto-recover preempted Spot VMs by durably re-provisioning them (same
		// model) through jittered keyed reconcile tasks; the host queue retries
		// and dead-letters to host=error if needed.
		hostreconciler.WithPreemptionRecovery(f.taskQueue),
		hostreconciler.WithReconcileEnqueuer(f.taskQueue),
		hostreconciler.WithLeaderCheck(isLeader),
		hostreconciler.WithDeletePolicy(sweepPolicy(models.ConfigKeyDeleteOrphanVMs)),
	)

	// Device controller
	healthConfig := device.DefaultHealthCheckConfig()
	if cfg.DeviceController.Interval > 0 {
		healthConfig.Interval = time.Duration(cfg.DeviceController.Interval)
	}
	deviceController := devicecontroller.New(healthConfig, f.deviceRepo, f.hostRepo, f.hoClientFactory, logger,
		devicecontroller.WithLeaderCheck(isLeader),
		devicecontroller.WithSweepPolicy(sweepPolicy(models.ConfigKeyOrphanCVDs)),
		devicecontroller.WithZombiePolicy(sweepPolicy(models.ConfigKeyZombieRecreate)),
		// A device under a live restart/powerwash is owned by that operation:
		// without this the controller settles a lifecycle status from a CVD
		// state the verb is passing through on purpose (Stopped mid-powerwash,
		// BootFailed during first boot), writing a row that contradicts
		// working work.
		devicecontroller.WithLiveOperations(f.operationsRepo),
		// Device-side reconcile suspicion, mirroring the host side: stale
		// teardowns and zombie recovery file device.reconcile, coalesced by
		// the keyed queue.
		devicecontroller.WithReconcileSuspector(devicecontroller.ReconcileSuspector{
			Tasks:    f.taskQueue,
			Enqueuer: f.taskQueue,
		}),
		// Sole trigger for host-poison remediation: sweep degraded hosts that
		// have settled to no live devices and queue an in-place `cvd reset`.
		devicecontroller.WithHostRemediator(devicecontroller.HostRemediator{
			Tasks:    f.taskQueue,
			Enqueuer: f.taskQueue,
		}),
		// Policy-gated capture-on-error: file a bugreport capture for each
		// error device once, gated by the runtime artifacts.capture_on_error
		// toggle (default off, since an absent key reads false).
		devicecontroller.WithCaptureSuspector(devicecontroller.CaptureSuspector{
			Enabled: func(ctx context.Context) (bool, error) {
				cfg, err := f.sysConfigRepo.Get(ctx, models.ConfigKeyCaptureOnError)
				if err != nil {
					if errors.Is(err, spanner.ErrKeyNotFound) {
						return false, nil
					}
					return false, fmt.Errorf("reading capture-on-error policy: %w", err)
				}
				return cfg.AsBool(), nil
			},
			Tasks:    f.taskQueue,
			Enqueuer: f.taskQueue,
		}))

	// Spot capacity-advice probe (nil when disabled or on a construction
	// failure). It uses the background plane's lease like every other loop,
	// so a probe that fails to build strands no lease of its own.
	var catalogSyncService *catalogsync.Service
	if cfg.CatalogSync.SpotAdvice {
		catalogSyncService = newCatalogSync(cfg, c.gceHTTP, f, logger, catalogsync.WithLeaderCheck(isLeader))
	}

	// Reaper
	reaperConfig := reaper.Config{
		Enabled:   cfg.Reaper.Enabled,
		Interval:  time.Duration(cfg.Reaper.Interval),
		BatchSize: cfg.Reaper.BatchSize,
		DryRun:    cfg.Reaper.DryRun,
	}
	reaperJob := reaper.New(reaperConfig, reaper.Deps{
		DeviceService: f.deviceSvc,
		HostService:   f.hostSvc,
		DeviceRepo:    f.deviceRepo,
		HostRepo:      f.hostRepo,
	}, logger,
		reaper.WithDryRunPolicy(sweepPolicy(models.ConfigKeyReaperDryRun)))

	// Batch settlement: atomic fill enforcement, emptied batch-row GC, and
	// drained-batch-host reclamation. Its own loop, ungated like the reaper and
	// on the reaper's cadence and dry-run posture.
	settleJob := batchsettle.New(batchsettle.Config{
		Enabled:   reaperConfig.Enabled,
		Interval:  reaperConfig.Interval,
		BatchSize: reaperConfig.BatchSize,
		DryRun:    reaperConfig.DryRun,
	}, batchsettle.Deps{
		Store:      f.batchRepo,
		Devices:    f.deviceSvc,
		Hosts:      f.hostSvc,
		HostLister: f.hostRepo,
	}, logger,
		// Same key as the reaper: one operator decision, both loops. It is read
		// per cycle per INSTANCE now, which is a read the idle-commit floor never
		// sees and a suspension every instance honors within one cycle.
		batchsettle.WithDryRunPolicy(sweepPolicy(models.ConfigKeyReaperDryRun)))

	// Task worker pool: drains the durable queue and runs CVD creation on this
	// instance. Safe to run on every instance: the queue claim is serializable.
	handlers := make([]task.Handler, 0, 8+len(f.verbHandlers))
	handlers = append(handlers,
		cvd.NewCreateHandler(f.cvdActuator, f.taskQueue),
		cvd.NewCleanupHandler(f.cvdActuator),
		cvd.NewReconcileHandler(f.cvdActuator, f.taskQueue, f.deviceRepo, logger),
		devicefeature.NewCaptureBugreportHandler(f.deviceSvc),
		hostfeature.NewProvisionHostHandler(f.hostSvc),
		hostfeature.NewDeprovisionHostHandler(f.hostSvc, f.taskQueue),
		hostfeature.NewReconcileHostHandler(f.hostSvc, f.taskQueue, logger),
		hostfeature.NewRemediateHostHandler(f.hostSvc),
		hostfeature.NewStopSettleHostHandler(f.hostSvc),
	)
	// The async in-place device verbs (restart, powerwash). Built by the device
	// slice because the verb-to-client-method mapping is its knowledge; empty when
	// operations are not wired.
	handlers = append(handlers, f.verbHandlers...)
	// The stockout re-probe, registered on the same switch that lets the host
	// service file one. It runs on every instance like the rest of the pool: it
	// is one probe of one cell, not a periodic loop, so it needs no leadership.
	if cfg.CatalogSync.SpotAdvice {
		handlers = append(handlers, gcecatalogfeature.NewReprobeAdviceHandler(
			gce.NewSpotAdviceClient(c.gceHTTP, cfg.GCP.ProjectID), f.spotAdviceRepo, logger))
	}

	workerPool, err := taskworker.New(
		f.workerCfg,
		f.taskQueue,
		handlers,
		logger,
		taskworker.WithWaker(f.taskWaker),
	)
	if err != nil {
		return jobs{}, fmt.Errorf("task worker config: %w", err)
	}
	if err := taskworker.RegisterQueueMetrics(f.taskQueue, logger); err != nil {
		logger.Warn("Failed to register queue metrics", slog.Any("error", err))
	}
	// The in-flight operation working set: queue depth's direct analog, and the
	// signal that says whether the verb handles are healthy at all.
	if err := spanner.RegisterOperationMetrics(f.operationsRepo, logger); err != nil {
		logger.Warn("Failed to register operation metrics", slog.Any("error", err))
	}

	// Task reclaimer: reclaims expired task leases for crash recovery, leader-gated.
	reclaimerJob := taskreclaimer.New(taskreclaimer.Config{
		Enabled:  cfg.TaskReclaimer.Enabled,
		Interval: time.Duration(cfg.TaskReclaimer.Interval),
	}, f.taskQueue, logger, taskreclaimer.WithLeaderCheck(isLeader))

	// Fleet keeper: composes and publishes the standby target, and drives the
	// warm plane that prefetches hot builds onto standing inventory. Always
	// built, and fleet.autoscale_mode is the runtime switch (absent reads as
	// off), so enabling the autoscaler never needs a redeploy, mirroring the
	// sweep policies.
	//
	// The lead goes to both explicitly instead of being defaulted twice: the
	// keeper's deficit count and the warm plane's target listing have to mean
	// the same thing by construction, without relying on two constructors
	// agreeing on a fallback.
	warmPlane := warmplane.New(f.deviceSvc, f.hostRepo, f.hoClientFactory,
		spanner.NewArtifactCacheRepository(c.spanner), host.ProvisionLead, logger)
	fleetKeeperJob := fleetkeeper.New(
		// The interval is named rather than left to the constructor's fallback:
		// the autoscaler page states it as a compiled fact, and a default applied
		// in one place and published from another is two numbers that agree until
		// somebody changes one.
		fleetkeeper.Config{
			Enabled:       true,
			Interval:      fleetkeeper.DefaultInterval,
			ProvisionLead: host.ProvisionLead,
		},
		f.hostConfigSvc, f.hostRepo,
		fleetProvisioner{devices: f.deviceSvc, hosts: f.hostSvc}, logger,
		fleetkeeper.WithWarmPlane(warmPlane),
		// The arm pool's spending gate: without an arm64 host image the keeper
		// must not buy for that pool, however an operator bounded it.
		fleetkeeper.WithARMCapability(f.armCapability.Ready),
		// Meet a deficit from the fleet's own draining hosts before buying a
		// cold one. The host service and not the repository, because the
		// renewal is audited and the audited mutation lives on the service.
		fleetkeeper.WithRescuer(f.hostSvc),
		// Give a surplus back by shortening leases. Same service as the renewal
		// for the same reason, and gated on `on` rather than provision_only:
		// this is the one thing the keeper does that can shorten a host's life.
		fleetkeeper.WithShortener(f.hostSvc),
		// Reclaim hosts fenced for a group that has ended. Nothing else in the
		// system does: an exclusive host is outside the fleet's inventory and its
		// outflow alike, so without this it bills its whole lease serving nobody.
		fleetkeeper.WithCollapser(f.hostSvc),
		// The same warm plane the keeper drives, under a second and narrower
		// port: a host mid-bake carries no ledger rows yet, so without this it
		// reads as the coldest host in the fleet and is shortened first.
		fleetkeeper.WithBakeReader(warmPlane),
		// The buffer never spends the host quota's last slot: that slot is the
		// difference between a user's next create succeeding and returning 429.
		fleetkeeper.WithQuotaReader(f.quotaService),
		// The demand signal reads host-create audit entries directly, not
		// through the buffered logger: the logger is the write side, and these
		// entries are read back within a cycle of being written.
		fleetkeeper.WithDemandReader(f.auditRepo),
		fleetkeeper.WithLeaderCheck(isLeader))

	return jobs{
		workerPool:     workerPool,
		reclaimer:      reclaimerJob,
		controller:     deviceController,
		reaper:         reaperJob,
		batchSettle:    settleJob,
		hostReconciler: reconcilerJob,
		catalogSync:    catalogSyncService,
		gceCatalog:     f.gceCatalog,
		fleetKeeper:    fleetKeeperJob,
		elector:        elector,
	}, nil
}

// startJobs starts each background job.
func startJobs(ctx context.Context, j jobs) {
	// First, so the first snapshot is already in flight while the jobs that
	// rank against it come up. It lands asynchronously either way, and every
	// availability cell reads as unknown until it does, which removes no zone.
	j.gceCatalog.Start(ctx)
	j.hostReconciler.Start(ctx)
	j.controller.Start(ctx)
	if j.catalogSync != nil {
		j.catalogSync.Start(ctx)
	}
	j.reaper.Start(ctx)
	j.batchSettle.Start(ctx)
	j.fleetKeeper.Start(ctx)
	j.workerPool.Start(ctx)
	j.reclaimer.Start(ctx)
}

// stop joins the background jobs. Their loops already observed the canceled
// lifecycle ctx; Stop() waits for in-flight cycles. The worker pool goes
// first so claimed tasks finish their bookkeeping; anything cut short is
// redelivered from the queue after its lease lapses.
func (j jobs) stop() {
	j.workerPool.Stop()
	j.reclaimer.Stop()
	j.controller.Stop()
	j.reaper.Stop()
	j.batchSettle.Stop()
	j.fleetKeeper.Stop()
	j.hostReconciler.Stop()
	if j.catalogSync != nil {
		j.catalogSync.Stop()
	}
	j.gceCatalog.Stop()
	// Last, and after every loop it gates: releasing the lease invites a
	// surviving instance to start cycling immediately, so releasing it while
	// this instance's own loops were still finishing would arrange exactly the
	// overlap the release exists to avoid. An instance that dies without
	// reaching this line costs a successor the lease TTL instead.
	j.elector.Stop()
}

// newCatalogSync builds the Spot capacity-advice probe; nil on a config
// failure, which includes the dependencies not being wired.
func newCatalogSync(cfg *config.Config, gceHTTP *http.Client, f *featureSet, logger *slog.Logger, extra ...catalogsync.Option) *catalogsync.Service {
	syncConfig := catalogsync.Config{
		ProjectID:              cfg.GCP.ProjectID,
		SpotAdviceSyncInterval: time.Duration(cfg.CatalogSync.SpotAdviceSyncInterval),
	}

	opts := []catalogsync.Option{catalogsync.WithSpotAdvice(
		gce.NewSpotAdviceClient(gceHTTP, cfg.GCP.ProjectID),
		f.spotAdviceRepo, f.machineTypeRepo, f.zonePolicy)}
	for _, opt := range extra {
		if opt != nil {
			opts = append(opts, opt)
		}
	}
	svc, err := catalogsync.New(syncConfig, logger, opts...)
	if err != nil {
		logger.Warn("Failed to create the Spot capacity-advice probe", slog.Any("error", err))
		return nil
	}
	return svc
}
