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
	"fmt"
	"log/slog"
	"time"

	"cloud-cuttlefish-orchestrator/internal/config"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	adminfeature "cloud-cuttlefish-orchestrator/internal/features/admin"
	batchfeature "cloud-cuttlefish-orchestrator/internal/features/batch"
	buildfeature "cloud-cuttlefish-orchestrator/internal/features/build"
	devicefeature "cloud-cuttlefish-orchestrator/internal/features/device"
	"cloud-cuttlefish-orchestrator/internal/features/device/cvd"
	"cloud-cuttlefish-orchestrator/internal/features/device/placement"
	gcecatalogfeature "cloud-cuttlefish-orchestrator/internal/features/gcecatalog"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	hostconfigfeature "cloud-cuttlefish-orchestrator/internal/features/hostconfig"
	machinetypefeature "cloud-cuttlefish-orchestrator/internal/features/machinetype"
	operationfeature "cloud-cuttlefish-orchestrator/internal/features/operation"
	quotafeature "cloud-cuttlefish-orchestrator/internal/features/quota"
	relayfeature "cloud-cuttlefish-orchestrator/internal/features/relay"
	storagefeature "cloud-cuttlefish-orchestrator/internal/features/storage"
	"cloud-cuttlefish-orchestrator/internal/jobs/fleetkeeper"
	"cloud-cuttlefish-orchestrator/internal/jobs/leaderelection"
	"cloud-cuttlefish-orchestrator/internal/jobs/taskworker"
	"cloud-cuttlefish-orchestrator/internal/platform/audit"
	"cloud-cuttlefish-orchestrator/internal/platform/cloudmonitoring"
	"cloud-cuttlefish-orchestrator/internal/platform/gce"
	"cloud-cuttlefish-orchestrator/internal/platform/gcs"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/platform/ice"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/turn"
)

// featureSet is the wired feature graph: the handlers the router mounts plus
// the cross-feature products the background jobs, the legacy /v1 surface, and the
// shutdown sequence consume. Everything consumed only inside wireFeatures
// stays a local there.
type featureSet struct {
	// Shared platform services and the task queue.
	auditLogger *audit.Logger
	// auditRepo is the audit table itself: the write side the logger drains
	// into, the create path's transactional insert, and the demand signal's
	// read. Held here because the background jobs read it directly.
	auditRepo       *spanner.AuditRepository
	hoClientFactory *hoclient.Factory
	instanceID      string
	workerCfg       taskworker.Config
	taskQueue       *spanner.TaskQueue
	taskWaker       *task.Waker
	operationsRepo  *spanner.OperationsRepository
	leaseRepo       *spanner.LeaseRepository
	spotAdviceRepo  *spanner.SpotAdviceRepository
	sysConfigRepo   *spanner.SystemConfigRepository
	zoneHealthRepo  *spanner.ZoneHealthRepository
	iceComposer     *ice.Composer

	// Feature slice products.
	deviceHandler          *devicefeature.DeviceHandler
	deviceSvc              *devicefeature.DeviceService
	deviceRepo             *devicefeature.DeviceRepository
	cvdActuator            *cvd.Actuator
	hostHandler            *hostfeature.HostHandler
	hostSvc                *hostfeature.HostService
	hostRepo               *hostfeature.HostRepository
	quotaHandler           *quotafeature.QuotaHandler
	quotaService           *quotafeature.Service
	buildHandler           *buildfeature.BuildHandler
	batchHandler           *batchfeature.Handler
	batchRepo              *batchfeature.Repository
	storageHandler         *storagefeature.StorageHandler
	machineTypeHandler     *machinetypefeature.MachineTypeHandler
	machineTypeRepo        *machinetypefeature.MachineTypeRepository
	gceCatalogHandler      *gcecatalogfeature.Handler
	zonePolicy             *gcecatalogfeature.ZonePolicy
	gceCatalog             *gce.Catalog
	hostImageConfigHandler *hostconfigfeature.Handler
	hostConfigSvc          *hostconfigfeature.Service
	armCapability          *hostfeature.ARMCapability
	relayHandler           *relayfeature.Handler
	adminHandler           *adminfeature.AdminHandler
	healthHandler          *adminfeature.HealthHandler
	operationHandler       *operationfeature.Handler

	// verbHandlers are the queue handlers for the async in-place device verbs.
	verbHandlers []task.Handler
}

// wireFeatures builds the shared platform services and every feature slice
// (one Wire call per slice), in dependency order. It is pure construction. The
// one startup seed (quota EnsureDefaultLimit) is an explicit Run step.
func wireFeatures(cfg *config.Config, logger *slog.Logger, c *clients) (*featureSet, error) {
	spannerClient := c.spanner
	gceClientSet := c.gce

	// Initialize Shared Platform Services
	auditRepo := spanner.NewAuditRepository(spannerClient)
	auditLogger := audit.NewLogger(auditRepo, audit.DefaultLoggerConfig(), logger)

	// TURN and ICE. The shared secret arrives via a mounted Secret Manager
	// volume (loaded by config.Load); the server lists are env config.
	//
	// The composer is what turns the topology into an answer per leg: a client
	// is pinned to relayed transport exactly where device media never meets the
	// internet at the host, and a device is offered STUN only where it has an
	// external address to reach one from (there is no Cloud NAT).
	var turnService *turn.CredentialService
	if cfg.TURN.SharedSecret != "" {
		turnService = turn.NewCredentialService(cfg.TURN.SharedSecret, time.Duration(cfg.TURN.TTL))
	}
	iceComposer := ice.NewComposer(turnService, ice.Options{
		STUNServers:          cfg.ICE.StunServers,
		ClientServers:        cfg.TURN.Servers,
		DeviceServers:        cfg.TURN.DeviceServers,
		PinClientsToRelay:    cfg.DeviceIngress == config.DeviceIngressRelay,
		DevicesReachInternet: cfg.DeviceIngress.UsesExternalIP(),
	})

	// HO Client
	hoClientConfig := hoclient.DefaultClientConfig()
	hoClientConfig.AllowInsecure = cfg.GCP.AllowSelfSignedHostSSLCert
	hoClientFactory := hoclient.NewFactory(hoClientConfig)

	// Initialize Features (using Wire factories)
	hostServiceConfig := hostfeature.HostServiceConfig{
		ProjectID:            cfg.GCP.ProjectID,
		Network:              cfg.GCP.Network,
		Subnetwork:           cfg.GCP.Subnetwork,
		ServiceAccount:       cfg.GCP.HostServiceAccount,
		NetworkTags:          []string{"cuttlefish-host", "cuttlefish-webrtc"},
		Labels:               managedByLabels(cfg),
		UseExternalIP:        cfg.DeviceIngress.UsesExternalIP(),
		HostOrchestratorPort: int64(cfg.GCP.HostOrchestratorPort),
		MaxConcurrentGCEOps:  20, // Cap concurrent GCE provisions per instance.
	}

	// Cross-cutting platform repositories shared across the slices below.
	spotAdviceRepo := spanner.NewSpotAdviceRepository(spannerClient)
	sysConfigRepo := spanner.NewSystemConfigRepository(spannerClient)
	zoneHealthRepo := spanner.NewZoneHealthRepository(spannerClient)

	// Machine curation is built first and on its own: the GCE snapshot below
	// narrows its shape discovery to the families it holds, and the catalog
	// the rest of the system reads is composed from that snapshot.
	machineCuration := machinetypefeature.WireCuration(spannerClient)

	// The zone policy is built ahead of its own slice's Wire: the GCE snapshot
	// below scopes its quota refresh to the zones an operator has enabled,
	// which this holds, and the snapshot is in turn what the slice's validator
	// ranks against.
	zonePolicy := gcecatalogfeature.NewZonePolicy(sysConfigRepo, logger)

	// What Compute Engine offers, held in memory and refreshed in the
	// background: the shapes and accelerators each zone stocks, and the live
	// regional quota. Facts about Google are cached here and never stored, so
	// restarting cannot lose them, and the API that holds them is more
	// available than any mirror of it would be.
	gceCatalog := gce.NewCatalog(cfg.GCP.ProjectID, gce.CatalogDeps{
		Compute: gce.NewComputeCatalog(gceClientSet.machineTypes,
			gceClientSet.acceleratorTypes, gceClientSet.zones, gceClientSet.regions),
		Families: machineCuration,
		Zones:    zonePolicy,
		// The region list reports a fixed flat array of metrics, which cannot
		// name a pool selected by a dimension and does not carry the hyperdisk
		// pools at all. Those come from the project's own quota telemetry.
		Dimensioned: cloudmonitoring.NewQuotaReader(cfg.GCP.ProjectID,
			cloudmonitoring.NewTimeSeries(c.metrics), logger),
	}, logger)

	// The catalog itself: the curated families, the shapes the snapshot
	// discovered in them, and the per-shape curation laid on top, composed on
	// every read. Curation writes rediscover immediately, so a family admitted
	// now is placeable now rather than at the next structural cycle.
	machineTypeHandler, machineTypeRepo := machinetypefeature.Wire(
		machineCuration, gceCatalog, logger,
		machinetypefeature.Deps{AuditLogger: auditLogger, Refresher: gceCatalog})

	// Advice a few sync ticks old is still steering-grade; older cells degrade
	// ranking to the quota/health signals. One value, because the capacity
	// surface reports advice as stale exactly when ranking stops consulting it.
	spotAdviceStale := 4 * time.Duration(cfg.CatalogSync.SpotAdviceSyncInterval)

	// Catalog Validator: admission validation plus zone ranking (quota
	// headroom, zone_health cooldowns, operator placement policy).
	validatorConfig := gcecatalogfeature.ValidatorConfig{
		UseExternalIP: cfg.DeviceIngress.UsesExternalIP(),
		// Twice the interval that produces the rows: one missed refresh is
		// jitter, two is a refresher that has stopped, and past that ranking
		// passes the region advisorily rather than blocking on stale numbers.
		// Plus the lag the quota telemetry withholds a sample for, since a row
		// carries the time of the sample it came from: without that allowance a
		// perfectly healthy per-family reading would never be current enough to
		// judge.
		QuotaStaleThreshold:      2*gce.QuotaRefreshInterval + cloudmonitoring.VisibilityLag,
		SpotAdviceStaleThreshold: spotAdviceStale,
		// The same rule for shapes. One missed structural cycle is jitter, two
		// is a refresher that has stopped, and past that a refusal stops
		// claiming to be about the zone when it is about our reading of it.
		ShapeStaleThreshold: 2 * gce.StructuralRefreshInterval,
	}
	gceCatalogHandler, catalogValidator := gcecatalogfeature.Wire(
		zonePolicy, machineTypeRepo, validatorConfig, logger,
		gcecatalogfeature.Deps{
			AuditLogger: auditLogger,
			ZoneHealth:  zoneHealthRepo,
			Policy:      sysConfigRepo,
			SpotAdvice:  spotAdviceRepo,
			Shapes:      gceCatalog,
		})

	// Host image configuration admin surface. Its service is also the one host
	// image resolution (host_image_configs rows resolved in their own
	// source_project, else CCO_HOST_IMAGE_PROJECT) that the host create
	// preflight, the provision task, the arm probe and the admin status read.
	hostImageConfigHandler, _, hostConfigSvc := hostconfigfeature.Wire(spannerClient,
		hostconfigfeature.Images{Source: gce.NewImageClient(gceClientSet.images), Project: cfg.GCP.HostImageProject},
		logger, cfg.GCP.ArtifactsBucket,
		hostconfigfeature.Deps{AuditLogger: auditLogger, MachineTypes: machineTypeRepo})

	// One arm64-buildability probe for its three consumers (the pool
	// derivation, the keeper's spending gate, and the autoscaler page) so they
	// cannot disagree about a fact that changes only by operator act.
	armCapability := hostfeature.NewARMCapability(hostConfigSvc, machineTypeRepo, logger)

	// Instance identity for queue lease ownership and leader election.
	instanceID := leaderelection.InstanceID()
	// Resolve the worker config once: its NumShards is the single source for
	// the queue's shard fan-out on both the enqueue and the claim side.
	workerCfg := taskworker.Config{
		Enabled:       cfg.TaskWorker.Enabled,
		MinWorkers:    cfg.TaskWorker.MinWorkers,
		MaxWorkers:    cfg.TaskWorker.MaxWorkers,
		NumShards:     cfg.TaskWorker.NumShards,
		ClaimBatch:    cfg.TaskWorker.ClaimBatch,
		LeaseDuration: time.Duration(cfg.TaskWorker.LeaseDuration),
		HandleTimeout: time.Duration(cfg.TaskWorker.HandleTimeout),
		PollMin:       time.Duration(cfg.TaskWorker.PollMin),
		PollMax:       time.Duration(cfg.TaskWorker.PollMax),
		RetryBase:     time.Duration(cfg.TaskWorker.RetryBase),
		RetryMax:      time.Duration(cfg.TaskWorker.RetryMax),
	}.WithDefaults()
	// The in-process wake shared by the producers and the pool that drains
	// them: an enqueue that commits on this instance is picked up at once
	// instead of at the next poll. Sized to the pool's CEILING, since that is
	// how many workers can be parked at once. It carries latency only. The
	// pool's poll is what guarantees delivery, here and for work another
	// instance enqueued.
	taskWaker := task.NewWaker(workerCfg.MaxWorkers)
	// Durable, Spanner-backed task queue for asynchronous device/host provisioning.
	taskQueue := spanner.NewTaskQueue(spannerClient, spanner.WithShards(workerCfg.NumShards), spanner.WithHolder(instanceID), spanner.WithLogger(logger), spanner.WithWaker(taskWaker))
	// The control-plane snapshot reports pool capacity; 0 when the pool is off.
	// The CEILING, because the page states deploy-time facts: how much of it an
	// instance is using right now is that instance's own level, and a snapshot
	// served by whichever instance answered would report a different number on
	// every poll.
	workersPerInstance := 0
	if workerCfg.Enabled {
		workersPerInstance = workerCfg.MaxWorkers
	}

	// Services
	idempotencyRepo := spanner.NewIdempotencyRepository(spannerClient)

	// Operation handles for the async device verbs. It shares the queue because
	// an operation and the task that executes it are inserted in ONE transaction:
	// a handle must never name work that was not enqueued.
	operationsRepo := spanner.NewOperationsRepository(spannerClient, taskQueue, logger)

	// The device repository is built ahead of the two service Wires: host
	// remediation gates on the device slice's live-device count while device
	// creation drives the host service, and the repository is the neutral
	// edge of that cycle.
	deviceRepo := devicefeature.NewDeviceRepository(spannerClient)

	// The stockout re-probe's two halves are switched on together: filing a task
	// this deployment has no handler for would hold it until it dead-letters.
	var stockoutReprobe hostfeature.StockoutReprober
	if cfg.CatalogSync.SpotAdvice {
		stockoutReprobe = taskQueue
	}

	hostHandler, hostSvc, hostRepo := hostfeature.Wire(hostServiceConfig, spannerClient,
		gceClientSet.instances, gceClientSet.operations, logger,
		hostfeature.Deps{
			AuditLogger: auditLogger,
			// The create's entry goes through the repository directly, not the
			// buffered logger: it commits with the host row.
			AuditMutator:        auditRepo,
			MachineTypes:        machineTypeRepo,
			TaskEnqueuer:        taskQueue,
			IdempotencyStore:    idempotencyRepo,
			CatalogValidator:    catalogValidator,
			ZoneHealth:          zoneHealthRepo,
			StockoutReprobe:     stockoutReprobe,
			Images:              hostConfigSvc,
			SystemConfig:        sysConfigRepo,
			HostOrchestrator:    hoClientFactory,
			LiveDeviceCounter:   deviceRepo.CountLiveByHost,
			LiveInstanceCounter: deviceRepo.CountLiveInstancesByHost,
			// Strong, so a device created within the staleness bound still
			// claims its CVD group in the host CVD view.
			DeviceLister: func(ctx context.Context, hostID string) ([]*device.Device, error) {
				return deviceRepo.List(ctx, device.ListFilter{HostID: &hostID, Strong: true})
			},
		})

	quotaHandler, quotaService := quotafeature.Wire(spannerClient, hostRepo, logger,
		quotafeature.Deps{AuditLogger: auditLogger})

	// Lease repository: leader-election leases (the background jobs start the
	// electors) and the admin lease panel.
	leaseRepo := spanner.NewLeaseRepository(spannerClient)

	gcsStore := gcs.NewClient(c.storage, cfg.GCP.ProjectID)
	storageHandler := storagefeature.Wire(gcsStore)

	buildSlice, err := buildfeature.Wire(spannerClient,
		buildfeature.Deps{
			AuditLogger:   auditLogger,
			Uploads:       gcsStore,
			StagingBucket: cfg.GCP.ArtifactsBucket,
		})
	if err != nil {
		return nil, fmt.Errorf("creating build service: %w", err)
	}

	deviceSlice, err := devicefeature.Wire(
		deviceRepo, hostRepo, buildSlice.Repository, hoClientFactory, logger,
		devicefeature.Deps{
			ICE:              iceComposer,
			AuditLogger:      auditLogger,
			TaskEnqueuer:     taskQueue,
			OperationJournal: taskQueue,
			IdempotencyStore: idempotencyRepo,
			ArtifactLedger:   spanner.NewArtifactCacheRepository(spannerClient),
			Operations:       operationsRepo,
			Artifacts:        gcsStore,
			ArtifactPolicy:   sysConfigRepo,
			ArtifactsBucket:  cfg.GCP.ArtifactsBucket,
			LifetimePolicy:   sysConfigRepo,
			Declarative: devicefeature.DeclarativeDeps{
				Provisioner:  hostSvc,
				Policy:       sysConfigRepo,
				MachineTypes: machineTypeRepo,
				Ranker:       catalogValidator,
				Quota:        quotaService,
				Registrar:    buildSlice.Service,
				Capacity:     placement.NewCapacityRepository(spannerClient),
				ZoneHealth:   zoneHealthRepo,
				ARM:          armCapability,
			},
			AllowInsecureHostTLS: cfg.GCP.AllowSelfSignedHostSSLCert,
			DevOrigin:            cfg.Auth.DevMode,
		})
	if err != nil {
		return nil, fmt.Errorf("creating device service: %w", err)
	}
	deviceSvc, cvdActuator := deviceSlice.Service, deviceSlice.Actuator

	// Batches drive the same device engine (resolve, provision, create). There
	// is no new backend, only coordinated admission over the interfaces the
	// device service and host service already expose. The repository holds only
	// the policy rows.
	batchHandler, batchRepo := batchfeature.Wire(spannerClient, deviceSvc, hostSvc, quotaService, logger)

	// Relay credential authority. Mounted only when a shared secret exists:
	// without one there is no key to derive, and every credential presented
	// would verify against the empty secret. It resolves both subject kinds a
	// credential can name: /v2 mints per device, legacy /v1 per host.
	var relayHandler *relayfeature.Handler
	if cfg.TURN.SharedSecret != "" {
		relayHandler = relayfeature.Wire(turnService, deviceSvc, hostSvc, cfg.TURN.Realm, logger)
	}

	adminHandler, healthHandler := adminfeature.Wire(
		hostRepo, deviceRepo, quotaService,
		adminfeature.Deps{
			TaskAdmin:          taskQueue,
			LeaseAdmin:         leaseRepo,
			AuditReader:        auditRepo,
			AuditLogger:        auditLogger,
			DeviceLens:         deviceRepo,
			HostLens:           hostRepo,
			ZoneCatalog:        zonePolicy,
			ZoneUniverse:       gceCatalog,
			RegionQuotas:       gceCatalog,
			ZoneHealth:         zoneHealthRepo,
			SpotFleet:          hostRepo,
			SpotAdvice:         spotAdviceRepo,
			ZoneFamily:         hostRepo,
			MachineTypes:       machineTypeRepo,
			PlacementPolicy:    hostConfigSvc,
			SpotAdviceEnabled:  cfg.CatalogSync.SpotAdvice,
			SpotAdviceStale:    spotAdviceStale,
			WorkersPerInstance: workersPerInstance,
			SweepPolicy:        hostConfigSvc,
			FleetPolicy:        hostConfigSvc,
			Standby:            hostRepo,
			Demand:             auditRepo,
			ARMCapability:      armCapability,
			FleetView:          hostRepo,
			KeeperActivity:     auditRepo,
			// The keeper's own budgets, handed over as data: a feature may not
			// import internal/jobs, and the autoscaler page states these as
			// compiled facts exactly so nobody makes them settings. The
			// provisioning budget is not among them. It is a share of the fleet
			// standing, so the page evaluates the domain's own rule against the
			// same status it publishes instead of being handed a number.
			KeeperLimits: adminfeature.KeeperLimits{
				CycleSeconds:         int64(fleetkeeper.DefaultInterval.Seconds()),
				MaxShortensPerCycle:  fleetkeeper.MaxShortensPerCycle,
				MaxCollapsesPerCycle: fleetkeeper.MaxCollapsesPerCycle,
				QuotaReserveHosts:    fleetkeeper.QuotaReserveHosts,
				LeaseName:            leaderelection.BackgroundPlaneLease,
			},
			Connectivity: connectivityFacts(cfg),
		},
		"1.0.0", logger,
	)

	operationHandler, err := operationfeature.Wire(operationsRepo)
	if err != nil {
		return nil, fmt.Errorf("creating operations slice: %w", err)
	}

	return &featureSet{
		auditLogger:     auditLogger,
		auditRepo:       auditRepo,
		hoClientFactory: hoClientFactory,
		instanceID:      instanceID,
		workerCfg:       workerCfg,
		taskQueue:       taskQueue,
		taskWaker:       taskWaker,
		operationsRepo:  operationsRepo,
		leaseRepo:       leaseRepo,
		spotAdviceRepo:  spotAdviceRepo,
		sysConfigRepo:   sysConfigRepo,
		zoneHealthRepo:  zoneHealthRepo,
		iceComposer:     iceComposer,

		deviceHandler:          deviceSlice.Handler,
		deviceSvc:              deviceSvc,
		deviceRepo:             deviceRepo,
		cvdActuator:            cvdActuator,
		hostHandler:            hostHandler,
		hostSvc:                hostSvc,
		hostRepo:               hostRepo,
		quotaHandler:           quotaHandler,
		quotaService:           quotaService,
		buildHandler:           buildSlice.Handler,
		batchHandler:           batchHandler,
		batchRepo:              batchRepo,
		storageHandler:         storageHandler,
		machineTypeHandler:     machineTypeHandler,
		machineTypeRepo:        machineTypeRepo,
		gceCatalogHandler:      gceCatalogHandler,
		zonePolicy:             zonePolicy,
		gceCatalog:             gceCatalog,
		hostImageConfigHandler: hostImageConfigHandler,
		hostConfigSvc:          hostConfigSvc,
		armCapability:          armCapability,
		relayHandler:           relayHandler,
		adminHandler:           adminHandler,
		healthHandler:          healthHandler,
		operationHandler:       operationHandler,
		verbHandlers:           deviceSlice.VerbHandlers,
	}, nil
}

// managedByLabels returns the GCE labels identifying VMs owned by this
// install; host creation stamps them and the orphan sweep claims nothing
// that lacks any of them. The deployment label keeps a second install in the
// same project from reading this one's hosts as its own orphans.
func managedByLabels(cfg *config.Config) map[string]string {
	return map[string]string{
		"managed-by":     "cloud-cuttlefish-orchestrator",
		"cco-deployment": cfg.Deployment,
	}
}

// connectivityFacts assembles the read-only media-plane block the
// control-plane snapshot serves: the deployment's ingress topology and ICE
// server sets, with the combinations that cannot stream stated as problems
// instead of left to be inferred from blank rows. It never carries the TURN
// shared secret or anything derived from it, only whether one is deployed.
func connectivityFacts(cfg *config.Config) adminfeature.ConnectivityItem {
	item := adminfeature.ConnectivityItem{
		IngressMode:          string(cfg.DeviceIngress),
		TransportPolicy:      "all",
		StunServers:          cfg.ICE.StunServers,
		TurnClientServers:    cfg.TURN.Servers,
		TurnDeviceServers:    cfg.TURN.DeviceServers,
		TurnRealm:            cfg.TURN.Realm,
		CredentialTTLSeconds: int64(time.Duration(cfg.TURN.TTL).Seconds()),
		CredentialsMintable:  cfg.TURN.SharedSecret != "",
	}
	if cfg.DeviceIngress == config.DeviceIngressRelay {
		// Mirrors the ICE composer's PinClientsToRelay: where device media
		// never meets the internet at the host, a direct pair cannot form.
		item.TransportPolicy = "relay"
		if !item.CredentialsMintable {
			item.Problems = append(item.Problems,
				"relay ingress without a TURN shared secret: no credential can be minted, streaming cannot work")
		}
		if len(item.TurnDeviceServers) == 0 {
			item.Problems = append(item.Problems,
				"relay ingress with no device-leg TURN servers: internal-only hosts cannot reach a relay, streaming cannot work")
		}
	}
	return item
}
