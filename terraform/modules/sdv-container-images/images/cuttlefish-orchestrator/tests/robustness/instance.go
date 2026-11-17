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
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"google.golang.org/api/option"

	auditdomain "cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	hostdomain "cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype/machinetypetest"
	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	buildfeature "cloud-cuttlefish-orchestrator/internal/features/build"
	devicefeature "cloud-cuttlefish-orchestrator/internal/features/device"
	"cloud-cuttlefish-orchestrator/internal/features/device/cvd"
	"cloud-cuttlefish-orchestrator/internal/features/device/placement"
	gcecatalogfeature "cloud-cuttlefish-orchestrator/internal/features/gcecatalog"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	hostconfigfeature "cloud-cuttlefish-orchestrator/internal/features/hostconfig"
	machinetypefeature "cloud-cuttlefish-orchestrator/internal/features/machinetype"
	quotafeature "cloud-cuttlefish-orchestrator/internal/features/quota"
	"cloud-cuttlefish-orchestrator/internal/jobs/devicecontroller"
	"cloud-cuttlefish-orchestrator/internal/jobs/fleetkeeper"
	"cloud-cuttlefish-orchestrator/internal/jobs/hostreconciler"
	"cloud-cuttlefish-orchestrator/internal/jobs/leaderelection"
	"cloud-cuttlefish-orchestrator/internal/jobs/reaper"
	"cloud-cuttlefish-orchestrator/internal/jobs/taskreclaimer"
	"cloud-cuttlefish-orchestrator/internal/jobs/taskworker"
	"cloud-cuttlefish-orchestrator/internal/jobs/warmplane"
	"cloud-cuttlefish-orchestrator/internal/platform/audit"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

const (
	harnessZone = "test-zone-a"
	// harnessZoneB is the failover target: the stockout nemesis never stocks
	// out both zones at once, so ranked placement always has somewhere to go.
	harnessZoneB         = "test-zone-b"
	harnessRegion        = "test-zone"
	harnessUser          = "chaos@robustness-harness"
	harnessMachineFamily = "n1"
	harnessMachineType   = "n1-standard-8"
)

// harnessDiscovery stands in for the Compute Engine shape discovery the
// catalog composes its entries from.
//
// memory_gb decides real behavior here. An auto-provisioned host's slot
// ceiling is the packing density bounded by what this machine's RAM holds
// beside the host reserve, so 30 GB is what makes the keeper's top-ups
// three-slot hosts. Change it and every capacity expectation in the fleet
// scenarios moves with it.
func harnessDiscovery() *machinetypetest.FixedDiscovery {
	return machinetypetest.NewFixedDiscovery(machinetype.Shape{
		Name:         harnessMachineType,
		Family:       harnessMachineFamily,
		VCPUs:        8,
		MemoryGB:     30,
		Architecture: machinetype.ArchX86,
	})
}

// harnessImages stands in for the Compute Engine image lookup: every image
// family resolves to one x86 image in the project asked about, so provisioning
// runs the production resolution against the seeded host_image_configs rows.
type harnessImages struct{}

func (harnessImages) ResolveFamily(_ context.Context, project, family string) (*hostconfig.Image, error) {
	name := family + "-harness"
	return &hostconfig.Image{
		SelfLink:     "https://www.googleapis.com/compute/v1/projects/" + project + "/global/images/" + name,
		Name:         name,
		Family:       family,
		Project:      project,
		Architecture: machinetype.ArchX86,
	}, nil
}

// managedLabels must match between provisioning and the host reconciler or it
// never sweeps the harness's orphans. Same invariant as production.
func managedLabels() map[string]string {
	return map[string]string{"managed-by": "cloud-cuttlefish-orchestrator", "cco-deployment": "cco"}
}

// clients bundles the instance's Spanner platform client and the compute
// clients pointed at the fake GCE boundary.
type clients struct {
	spanner    *spanner.Client
	instances  *compute.InstancesClient
	operations *compute.ZoneOperationsClient
}

func newClients(ctx context.Context, cfg Cfg) (clients, error) {
	scfg := spanner.DefaultConfig()
	scfg.ProjectID = cfg.Project
	scfg.InstanceID = cfg.Instance
	scfg.DatabaseID = cfg.Database
	scfg.Emulator = cfg.Emulator
	// db-fault mode: this instance's Spanner traffic flows through its fault
	// proxy so the driver can degrade or sever the link.
	if cfg.InstanceIndex < len(cfg.ProxyAddrs) && cfg.ProxyAddrs[cfg.InstanceIndex] != "" {
		scfg.Emulator = cfg.ProxyAddrs[cfg.InstanceIndex]
	}
	client, err := spanner.NewClient(ctx, scfg)
	if err != nil {
		return clients{}, fmt.Errorf("creating spanner client: %w", err)
	}
	instancesClient, err := compute.NewInstancesRESTClient(ctx,
		option.WithEndpoint(cfg.GCEURL), option.WithoutAuthentication())
	if err != nil {
		client.Close()
		return clients{}, fmt.Errorf("creating instances client: %w", err)
	}
	operationsClient, err := compute.NewZoneOperationsRESTClient(ctx,
		option.WithEndpoint(cfg.GCEURL), option.WithoutAuthentication())
	if err != nil {
		client.Close()
		_ = instancesClient.Close()
		return clients{}, fmt.Errorf("creating operations client: %w", err)
	}
	return clients{spanner: client, instances: instancesClient, operations: operationsClient}, nil
}

// harnessFleetProvisioner mirrors internal/app's adapter: one standby host from
// the declarative template through the ordinary host create.
type harnessFleetProvisioner struct {
	devices *devicefeature.DeviceService
	hosts   *hostfeature.HostService
}

// fleetKeeperActor matches internal/app's created_by marker so the checker's
// keeper-host census reads the same fleet production would produce. The value
// is frozen on both sides, and checkPool matches it as a SQL literal.
const fleetKeeperActor = "poolkeeper"

func (p harnessFleetProvisioner) ProvisionFleetHost(ctx context.Context, pool string) (string, error) {
	hs, err := p.devices.ResolveFleetHostSpec(ctx, pool)
	if err != nil {
		return "", fmt.Errorf("resolving standby host spec: %w", err)
	}
	h, err := p.hosts.Create(ctx, *hs.Spec, fleetKeeperActor, hostdomain.OriginPool)
	if err != nil {
		return "", fmt.Errorf("creating standby host: %w", err)
	}
	return h.ID, nil
}

// runInstance composes one REAL orchestrator instance (repositories,
// device/host services, the production task handlers for CVD create/cleanup/
// reconcile, host provision/deprovision/reconcile and the async verbs, on a
// worker pool, plus reaper, host reconciler, device controller, task
// reclaimer and one background-plane elector) pointed at the fake GCE and fake
// HO boundaries. Everything between the API surface and those boundaries is
// production code.
func runInstance(cfg Cfg) error { //nolint:funlen // composition root, mirrors internal/app
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Clock-skew nemesis: shift THIS instance's lease arithmetic (task
	// leases, sweeps, leader leases, reaper scheduling) before any component
	// starts. Ledger and checker timestamps stay on the unshifted clock.
	if skew := cfg.skewFor(cfg.InstanceIndex); skew != 0 {
		spanner.SetClockOffsetForTesting(skew)
		fmt.Fprintf(os.Stderr, "clock skew applied: %v\n", skew)
	}

	cl, err := newClients(ctx, cfg)
	if err != nil {
		return err
	}
	client, instancesClient, operationsClient := cl.spanner, cl.instances, cl.operations
	defer client.Close()
	defer func() { _ = instancesClient.Close() }()
	defer func() { _ = operationsClient.Close() }()

	hostRepo := hostfeature.NewHostRepository(client)
	deviceRepo := devicefeature.NewDeviceRepository(client)
	buildRepo := buildfeature.NewBuildRepository(client)
	idemRepo := spanner.NewIdempotencyRepository(client)

	holder := leaderelection.InstanceID()
	// The in-process wake production wires, so the harness drives the same
	// discovery path: this instance's own API calls wake it, and work another
	// instance enqueued is found by the poll.
	waker := task.NewWaker(cfg.Workers)
	queue := spanner.NewTaskQueue(client, spanner.WithShards(cfg.Shards), spanner.WithHolder(holder),
		spanner.WithLogger(logger), spanner.WithWaker(waker))
	opsRepo := spanner.NewOperationsRepository(client, queue, logger)

	hoCfg := hoclient.DefaultClientConfig()
	hoCfg.Timeout = 10 * time.Second
	hoCfg.MaxWait = 30 * time.Second
	hoFactory := hoclient.NewFactory(hoCfg)

	// The failover checker reads host.zone_failover audit entries, so the
	// harness logs audits like production does.
	auditLogger := audit.NewLogger(spanner.NewAuditRepository(client), audit.DefaultLoggerConfig(), logger)

	zoneHealthRepo := spanner.NewZoneHealthRepository(client)
	machineTypeRepo := machinetypefeature.NewMachineTypeRepository(
		machinetypefeature.NewCurationRepository(client), harnessDiscovery())
	sysConfigRepo := spanner.NewSystemConfigRepository(client)
	validator := gcecatalogfeature.NewCatalogValidator(
		gcecatalogfeature.NewZonePolicy(sysConfigRepo, logger), machineTypeRepo,
		gcecatalogfeature.ValidatorConfig{}, logger,
		gcecatalogfeature.WithZoneHealthStore(zoneHealthRepo),
		gcecatalogfeature.WithPolicyReader(sysConfigRepo),
	)
	_, _, hostConfigSvc := hostconfigfeature.Wire(client,
		hostconfigfeature.Images{Source: harnessImages{}, Project: cfg.Project}, logger, "",
		hostconfigfeature.Deps{MachineTypes: machineTypeRepo})
	hostSvc := hostfeature.NewHostService(hostfeature.HostServiceConfig{
		ProjectID:            cfg.Project,
		Network:              "robustness-net",
		Subnetwork:           "robustness-subnet",
		Labels:               managedLabels(),
		UseExternalIP:        true,
		HostOrchestratorPort: int64(cfg.HOPort),
		MaxConcurrentGCEOps:  8,
	}, instancesClient, operationsClient, hostRepo, logger,
		hostfeature.WithTaskEnqueuer(queue),
		hostfeature.WithHostIdempotencyStore(idemRepo),
		hostfeature.WithHostOrchestratorFactory(hoFactory),
		hostfeature.WithCatalogValidator(validator),
		hostfeature.WithZoneHealth(zoneHealthRepo),
		hostfeature.WithMachineTypeCatalog(machineTypeRepo),
		hostfeature.WithImageResolver(hostConfigSvc),
		hostfeature.WithHostAuditLogger(auditLogger),
		// The transactional audit port, and not an alternative spelling of the
		// logger above. The entries that ride a host mutation (the create's
		// provenance stamp and both lease directions) are written through this
		// one alone, and unwired they are dropped without an error. Two features
		// stop working with it. The demand signal reads those create entries,
		// and a run counts what reclamation did from the lease trail.
		hostfeature.WithHostAuditMutator(spanner.NewAuditRepository(client)),
	)
	quotaSvc := quotafeature.NewService(quotafeature.NewRepository(client), hostRepo, logger)
	buildSvc, err := buildfeature.NewBuildService(buildRepo)
	if err != nil {
		return fmt.Errorf("creating build service: %w", err)
	}
	deviceSvc, err := devicefeature.NewDeviceService(deviceRepo, hostRepo, buildRepo, hoFactory, logger,
		devicefeature.WithTaskEnqueuer(queue),
		devicefeature.WithIdempotencyStore(idemRepo),
		// EXCLUSIVE verbs run for real. Admission writes operations rows, and the
		// open-operations checker reconciles them at quiesce.
		devicefeature.WithOperations(opsRepo),
		devicefeature.WithDeclarativeCreate(devicefeature.DeclarativeDeps{
			Provisioner:  hostSvc,
			Policy:       spanner.NewSystemConfigRepository(client),
			MachineTypes: machineTypeRepo,
			Ranker:       validator,
			Quota:        quotaSvc,
			Registrar:    buildSvc,
			Capacity:     placement.NewCapacityRepository(client),
			ZoneHealth:   zoneHealthRepo,
		}),
	)
	if err != nil {
		return fmt.Errorf("creating device service: %w", err)
	}
	cvdActuator := cvd.NewActuator(deviceRepo, hostRepo, buildRepo, hoFactory, logger,
		cvd.WithOperationJournal(queue),
		// Artifact affinity runs for real: completed creates ledger their
		// pinned fetch keys, and the placement checker replays the pick.
		cvd.WithArtifactLedger(spanner.NewArtifactCacheRepository(client)))

	// The async in-place verbs, mirroring internal/features/device's
	// wireVerbHandlers table.
	restartHandler, err := devicefeature.NewVerbOperationHandler(devicefeature.VerbDefinition{
		Verb: operation.VerbDeviceRestart, Label: "restart",
		Action: auditdomain.ActionDeviceRestart, Call: hoclient.Client.RestartCVD,
	}, deviceSvc, opsRepo, logger)
	if err != nil {
		return fmt.Errorf("building restart handler: %w", err)
	}
	powerwashHandler, err := devicefeature.NewVerbOperationHandler(devicefeature.VerbDefinition{
		Verb: operation.VerbDevicePowerwash, Label: "powerwash",
		Action: auditdomain.ActionDevicePowerwash, Call: hoclient.Client.PowerwashCVD,
	}, deviceSvc, opsRepo, logger)
	if err != nil {
		return fmt.Errorf("building powerwash handler: %w", err)
	}

	pool, err := taskworker.New(taskworker.Config{
		Enabled:       true,
		MinWorkers:    cfg.Workers,
		MaxWorkers:    cfg.Workers,
		NumShards:     cfg.Shards,
		ClaimBatch:    1,
		LeaseDuration: cfg.Lease,
		HandleTimeout: cfg.HandleTO,
		PollMin:       100 * time.Millisecond,
		PollMax:       time.Second,
		RetryBase:     300 * time.Millisecond,
		RetryMax:      5 * time.Second,
	}, queue, []task.Handler{
		cvd.NewCreateHandler(cvdActuator, queue),
		cvd.NewCleanupHandler(cvdActuator),
		cvd.NewReconcileHandler(cvdActuator, queue, deviceRepo, logger),
		hostfeature.NewProvisionHostHandler(hostSvc),
		hostfeature.NewDeprovisionHostHandler(hostSvc, queue),
		hostfeature.NewReconcileHostHandler(hostSvc, queue, logger),
		hostfeature.NewStopSettleHostHandler(hostSvc),
		restartHandler,
		powerwashHandler,
	}, logger, taskworker.WithWaker(waker))
	if err != nil {
		return fmt.Errorf("building worker pool: %w", err)
	}

	// ONE election for the gated part of the background plane, the shape
	// internal/app composes: every leader-gated job below takes this same
	// IsLeader, and a per-job elector reintroduced here would be a second
	// election the one-election checker fails the run on. The reaper sits
	// outside it, as in production: every instance reaps, and the conservation
	// checker is what says that stays safe.
	//
	// The TTL is scaled to the harness's clock like every other interval here
	// (production takes the elector's own default, which is far longer), and the
	// scaling is what keeps two proofs from going vacuous: a kill must hand the
	// plane over inside a run that lasts tens of seconds, and a SIGSTOP must
	// outlast the TTL for the paused leader and its successor to overlap at all.
	leaseRepo := spanner.NewLeaseRepository(client)
	elector := leaderelection.New(leaderelection.Config{
		Name: leaderelection.BackgroundPlaneLease, Holder: holder, TTL: cfg.LeaseTTL,
	}, leaseRepo, logger)
	elector.Start(ctx)
	isLeader := elector.IsLeader

	// The fleet keeper runs like production: policy from system_config through
	// the same settings service (seeded by the standing-capacity scenario,
	// absent otherwise = off), provisioning through the same declarative
	// template + host-create path internal/app adapts, the same demand read,
	// and driving the same warm plane. The interval stays wide enough for a
	// real cycle budget: the runner budgets each cycle at interval−1s, and an
	// emulator host create needs seconds.
	//
	// The settings service rather than a raw key read: the defaulting and
	// bounds-checking live there, and a harness reading keys directly would be
	// exercising a keeper the deployment does not run.
	//
	// The option list is production's whole set, and it has to stay that way.
	// Every one of these ports degrades silently to the behavior that predates
	// it when left unwired, so an omission here costs nothing visible and simply
	// drops that feature from the run. Rescue and reclamation were both absent
	// for exactly that reason, and every assertion about them passed.
	warmPlane := warmplane.New(deviceSvc, hostRepo, hoFactory,
		spanner.NewArtifactCacheRepository(client), hostdomain.ProvisionLead, logger)
	keeperJob := fleetkeeper.New(
		fleetkeeper.Config{Enabled: true, Interval: 5 * time.Second, ProvisionLead: hostdomain.ProvisionLead},
		hostConfigSvc, hostRepo,
		harnessFleetProvisioner{devices: deviceSvc, hosts: hostSvc}, logger,
		fleetkeeper.WithWarmPlane(warmPlane),
		fleetkeeper.WithRescuer(hostSvc),
		fleetkeeper.WithShortener(hostSvc),
		fleetkeeper.WithCollapser(hostSvc),
		fleetkeeper.WithBakeReader(warmPlane),
		fleetkeeper.WithQuotaReader(quotaSvc),
		fleetkeeper.WithDemandReader(spanner.NewAuditRepository(client)),
		fleetkeeper.WithLeaderCheck(isLeader))

	reconcilerJob := hostreconciler.New(hostreconciler.Config{
		ProjectID:      cfg.Project,
		Zones:          []string{harnessZone, harnessZoneB},
		SyncInterval:   cfg.SyncEvery,
		InstanceLabels: managedLabels(),
		// Deletion armed so every chaos run proves the orphan sweep never
		// eats a legitimate VM: a wrongful delete trips the conservation
		// and orphan checkers.
		DeleteOrphanVMs: true,
	}, instancesClient, hostRepo, logger,
		hostreconciler.WithOperationsClient(operationsClient),
		hostreconciler.WithPreemptionRecovery(queue),
		hostreconciler.WithReconcileEnqueuer(queue),
		hostreconciler.WithLeaderCheck(isLeader))
	healthCfg := device.DefaultHealthCheckConfig()
	healthCfg.Interval = cfg.HealthEvery
	controllerJob := devicecontroller.New(healthCfg, deviceRepo, hostRepo, hoFactory, logger,
		devicecontroller.WithLeaderCheck(isLeader),
		devicecontroller.WithReconcileSuspector(devicecontroller.ReconcileSuspector{
			Tasks:    queue,
			Enqueuer: queue,
		}))
	reaperJob := reaper.New(reaper.Config{
		Enabled:   true,
		Interval:  cfg.ReapEvery,
		BatchSize: 50,
	}, reaper.Deps{
		DeviceService: deviceSvc,
		HostService:   hostSvc,
		DeviceRepo:    deviceRepo,
		HostRepo:      hostRepo,
	}, logger)
	reclaimerJob := taskreclaimer.New(taskreclaimer.Config{Enabled: true, Interval: cfg.SweepEvery}, queue, logger,
		taskreclaimer.WithLeaderCheck(isLeader))

	reconcilerJob.Start(ctx)
	controllerJob.Start(ctx)
	reaperJob.Start(ctx)
	keeperJob.Start(ctx)
	pool.Start(ctx)
	reclaimerJob.Start(ctx)

	// The workload runs for cfg.Duration; jobs keep running until SIGTERM so
	// the fleet can drain and reconcile through the driver's quiesce window.
	wl, err := newWorkload(cfg, deviceSvc, hostSvc, buildSvc)
	if err != nil {
		return err
	}
	wl.run(ctx, cfg.Duration)

	<-ctx.Done()
	pool.Stop()
	reclaimerJob.Stop()
	reaperJob.Stop()
	reconcilerJob.Stop()
	controllerJob.Stop()
	// The keeper was the one job never joined here. The loop already exits on
	// the canceled context, so this was a missing join rather than a leak, but
	// an unjoined cycle can still be mid-provision while the checkers read.
	keeperJob.Stop()
	// After the jobs: Close drains the async audit buffer so the driver's
	// failover count sees every entry.
	auditLogger.Close()
	// Last, after every loop it gates: the release invites a survivor to start
	// cycling at once, which must not happen while this instance's own loops are
	// still finishing.
	elector.Stop()
	return wl.close()
}
