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

package adminfeature

import (
	"log/slog"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
)

// Deps carries the admin feature's injected collaborators beyond the core
// repositories; nil fields disable the corresponding endpoints or auditing.
type Deps struct {
	TaskAdmin    TaskAdmin
	LeaseAdmin   LeaseAdmin
	AuditReader  AuditReader
	AuditLogger  audit.Sink
	DeviceLens   DeviceLens
	HostLens     HostLens
	ZoneCatalog  ZoneCatalog
	ZoneUniverse ZoneUniverse
	RegionQuotas RegionQuotas
	ZoneHealth   ZoneHealthAdmin
	SpotFleet    SpotFleetCounter
	SpotAdvice   SpotAdviceReader
	// ZoneFamily, MachineTypes and PlacementPolicy feed the capacity snapshot:
	// where the fleet's machines are by shape family, what the catalog says a
	// host of each family costs, and the reserve margin ranking filters below.
	ZoneFamily      ZoneFamilyCounter
	MachineTypes    MachineCatalog
	PlacementPolicy PlacementPolicyReader
	// SpotAdviceEnabled mirrors CCO_SPOT_ADVICE for the snapshot's
	// advice-health block, and SpotAdviceStale is the age past which ranking
	// stops consulting a cell. It is the same bound, so the page and placement
	// agree on when advice stopped steering.
	SpotAdviceEnabled bool
	SpotAdviceStale   time.Duration
	// WorkersPerInstance is the task worker pool's CEILING per serving
	// instance, surfaced in the control-plane snapshot (0 when the pool is
	// disabled). The live width is a different number. An instance sizes itself
	// between a floor and this ceiling from the age of the work it claims, so
	// the live width is that instance's own level and a snapshot served by
	// whichever instance answered would report a different one on every poll.
	WorkersPerInstance int
	// SweepPolicy reports what the background loops may do on their own, for
	// the snapshot's reconciler cards.
	SweepPolicy SweepPolicyReader
	// FleetPolicy, Standby and Demand feed the snapshot's autoscaler card: the
	// operator's intent, what the fleet holds, and the demand the horizon saw.
	// All three, because the card publishes the composed target and composing
	// it from two of the three inputs would publish a different number from the
	// one the keeper acts on.
	FleetPolicy FleetPolicyReader
	Standby     StandbyCounter
	Demand      DemandReader

	// FleetView, KeeperActivity and KeeperLimits feed the autoscaler page: the
	// fleet host by host, what the keeper actually did, and the compiled budgets
	// the page states as facts. The limits arrive as data because they belong to
	// internal/jobs, which no feature may import.
	FleetView      FleetViewer
	KeeperActivity KeeperActivityReader
	KeeperLimits   KeeperLimits
	ARMCapability  ARMCapability

	// Connectivity holds the deploy-time media-path facts the snapshot
	// serves read-only; the composition root assembles it from config.
	Connectivity ConnectivityItem
}

// Wire creates all admin feature components.
func Wire(
	hostCounter HostCounter,
	deviceCounter DeviceCounter,
	quotaService quota.Service,
	deps Deps,
	version string,
	logger *slog.Logger,
) (*AdminHandler, *HealthHandler) {
	admin := NewAdminHandler(
		hostCounter,
		deviceCounter,
		quotaService,
		logger,
	)
	admin.taskAdmin = deps.TaskAdmin
	admin.leaseAdmin = deps.LeaseAdmin
	admin.workersPerInstance = deps.WorkersPerInstance
	admin.auditReader = deps.AuditReader
	admin.auditLogger = deps.AuditLogger
	admin.deviceLens = deps.DeviceLens
	admin.hostLens = deps.HostLens
	admin.zoneCatalog = deps.ZoneCatalog
	admin.zoneUniverse = deps.ZoneUniverse
	admin.regionQuotas = deps.RegionQuotas
	admin.zoneHealth = deps.ZoneHealth
	admin.spotFleet = deps.SpotFleet
	admin.spotAdvice = deps.SpotAdvice
	admin.zoneFamily = deps.ZoneFamily
	admin.machineTypes = deps.MachineTypes
	admin.placementPolicy = deps.PlacementPolicy
	admin.spotAdviceEnabled = deps.SpotAdviceEnabled
	admin.spotAdviceStale = deps.SpotAdviceStale
	admin.sweepPolicy = deps.SweepPolicy
	admin.fleetPolicy = deps.FleetPolicy
	admin.standby = deps.Standby
	admin.demand = deps.Demand
	admin.fleetView = deps.FleetView
	admin.keeperActivity = deps.KeeperActivity
	admin.armCapability = deps.ARMCapability
	admin.keeperLimits = deps.KeeperLimits
	admin.connectivity = deps.Connectivity
	health := NewHealthHandler(version)

	return admin, health
}
