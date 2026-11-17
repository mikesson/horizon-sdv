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
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// tagAdmin groups the privileged operations in the OpenAPI document.
const tagAdmin = "Admin"

// HostCounter defines the interface for counting hosts by status
type HostCounter interface {
	CountByStatus(ctx context.Context) (map[host.Status]int64, error)
}

// DeviceCounter provides device statistics for the stats endpoint.
type DeviceCounter interface {
	CountByStatus(ctx context.Context) (map[device.Status]int64, error)
}

// AdminHandler manages privileged system operations.
//
// It exposes endpoints for maintenance tasks and system statistics.
// (Tombstone purging is not among them: schema row deletion policies age
// deleted rows out automatically.)
type AdminHandler struct {
	hostCounter   HostCounter
	deviceCounter DeviceCounter
	quotaService  quota.Service
	taskAdmin     TaskAdmin
	leaseAdmin    LeaseAdmin
	auditReader   AuditReader
	auditLogger   audit.Sink
	deviceLens    DeviceLens
	hostLens      HostLens
	zoneCatalog   ZoneCatalog
	zoneUniverse  ZoneUniverse
	regionQuotas  RegionQuotas
	zoneHealth    ZoneHealthAdmin
	spotFleet     SpotFleetCounter
	spotAdvice    SpotAdviceReader
	sweepPolicy   SweepPolicyReader
	fleetPolicy   FleetPolicyReader
	standby       StandbyCounter
	demand        DemandReader
	// The capacity surface's own collaborators: the fleet counted by zone and
	// shape family, the curated shapes those families are judged as, and the
	// reserve margin ranking filters below.
	zoneFamily      ZoneFamilyCounter
	machineTypes    MachineCatalog
	placementPolicy PlacementPolicyReader
	// The autoscaler page's own collaborators: the fleet drawn host by host, the
	// keeper's actuation from the trail, and the compiled budgets internal/jobs
	// owns and this package may not import.
	fleetView      FleetViewer
	keeperActivity KeeperActivityReader
	keeperLimits   KeeperLimits
	// armCapability is the deployment's arm64 buildability fact, served so the
	// page can say which operator act the arm pool still waits on. Nil reads as
	// not ready.
	armCapability ARMCapability
	// spotAdviceEnabled mirrors CCO_SPOT_ADVICE for the snapshot's advice
	// health (an empty cell list is ambiguous without it).
	spotAdviceEnabled bool
	// spotAdviceStale is how old the newest advice cell may be before the
	// snapshot reports the advice as stale. It is ranking's own bound, so the
	// page says advice steers exactly while it does.
	spotAdviceStale time.Duration
	// connectivity holds the deploy-time media-path facts, assembled once
	// by the composition root from config.
	connectivity ConnectivityItem
	// workersPerInstance is the worker pool's ceiling, stated by the
	// control-plane snapshot as a deploy-time fact.
	workersPerInstance int
	logger             *slog.Logger
}

// NewAdminHandler creates a new admin handler.
func NewAdminHandler(
	hostCounter HostCounter,
	deviceCounter DeviceCounter,
	quotaService quota.Service,
	logger *slog.Logger,
) *AdminHandler {
	return &AdminHandler{
		hostCounter:   hostCounter,
		deviceCounter: deviceCounter,
		quotaService:  quotaService,
		logger:        logger,
	}
}

// Register registers admin operations with the Huma API.
func (h *AdminHandler) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "admin-stats",
		Method:      http.MethodGet,
		Path:        "/v2/admin/stats",
		Summary:     "Get platform statistics",
		Description: "Returns platform-wide statistics",
		Tags:        []string{tagAdmin},
		Security:    web.BearerAuth(),
	}, h.Stats)

	huma.Register(api, huma.Operation{
		OperationID: "admin-list-failed-tasks",
		Method:      http.MethodGet,
		Path:        "/v2/admin/tasks/failed",
		Summary:     "List dead-lettered tasks",
		Description: "Lists tasks that exhausted their retries and were dead-lettered",
		Tags:        []string{tagAdmin},
		Security:    web.BearerAuth(),
	}, h.ListFailedTasks)

	huma.Register(api, huma.Operation{
		OperationID: "admin-task-stats",
		Method:      http.MethodGet,
		Path:        "/v2/admin/tasks/stats",
		Summary:     "Get task queue statistics",
		Description: "Returns a point-in-time snapshot of task queue depth",
		Tags:        []string{tagAdmin},
		Security:    web.BearerAuth(),
	}, h.TaskStats)

	huma.Register(api, huma.Operation{
		OperationID: "admin-control-plane",
		Method:      http.MethodGet,
		Path:        "/v2/admin/control-plane",
		Summary:     "Get a control-plane snapshot",
		Description: "Returns queue depth, the live task working set, the task-type breakdown, " +
			"and the periodic-job leader leases in one snapshot for the operations view",
		Tags:     []string{tagAdmin},
		Security: web.BearerAuth(),
	}, h.ControlPlane)

	huma.Register(api, huma.Operation{
		OperationID: "admin-capacity",
		Method:      http.MethodGet,
		Path:        "/v2/admin/capacity",
		Summary:     "Get a capacity snapshot",
		Description: "Returns where the fleet can grow right now: the placement space zone by zone with a " +
			"server-derived verdict per machine family, the regional quota headroom behind those verdicts, " +
			"Spot capacity, and the fleet census",
		Tags:     []string{tagAdmin},
		Security: web.BearerAuth(),
	}, h.Capacity)

	huma.Register(api, huma.Operation{
		OperationID: "admin-autoscaler",
		Method:      http.MethodGet,
		Path:        "/v2/admin/autoscaler",
		Summary:     "Get an autoscaler snapshot",
		Description: "Returns the composed standby target with the arithmetic behind it, the fleet " +
			"drawn host by host with the standby predicate evaluated term by term, the shape census, " +
			"what the keeper actually did over the last day, and what is limiting the current cycle",
		Tags:     []string{tagAdmin},
		Security: web.BearerAuth(),
	}, h.Autoscaler)

	huma.Register(api, huma.Operation{
		OperationID: "admin-standby-recommendation",
		Method:      http.MethodGet,
		Path:        "/v2/admin/autoscaler/recommendation",
		Summary:     "Get the standing-capacity recommendation",
		Description: "Returns the peak of concurrent servable placement misses over the observation " +
			"window, with the interval that produced it, the counts behind it and the series a chart " +
			"draws. This is the statistic the standby target is composed from, read over days " +
			"instead of over the demand horizon",
		Tags:     []string{tagAdmin},
		Security: web.BearerAuth(),
	}, h.StandbyRecommendation)

	huma.Register(api, huma.Operation{
		OperationID: "admin-retry-task",
		Method:      http.MethodPost,
		Path:        "/v2/admin/tasks/{id}/retry",
		Summary:     "Retry a dead-lettered task",
		Description: "Resets a dead-lettered task to pending for another attempt. " +
			"Rejected (409) for task types whose resource is already terminal, because " +
			"a requeue would no-op. Use the resource's retry API instead.",
		Tags:     []string{tagAdmin},
		Security: web.BearerAuth(),
		Errors:   []int{http.StatusBadRequest, http.StatusConflict},
	}, h.RetryTask)

	huma.Register(api, huma.Operation{
		OperationID: "admin-delete-task",
		Method:      http.MethodDelete,
		Path:        "/v2/admin/tasks/{id}",
		Summary:     "Delete a task",
		Description: "Force-removes a task from the queue regardless of state",
		Tags:        []string{tagAdmin},
		Security:    web.BearerAuth(),
	}, h.DeleteTask)

	huma.Register(api, huma.Operation{
		OperationID: "admin-list-audit",
		Method:      http.MethodGet,
		Path:        "/v2/admin/audit",
		Summary:     "List audit log entries",
		Description: "Returns audit log entries matching the filter, newest first",
		Tags:        []string{tagAdmin},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusBadRequest},
	}, h.ListAudit)

	huma.Register(api, huma.Operation{
		OperationID: "admin-reclaimable",
		Method:      http.MethodGet,
		Path:        "/v2/admin/reclaimable",
		Summary:     "List reclaimable capacity",
		Description: "Returns pinned, stopped, and expiring resources an operator can reclaim",
		Tags:        []string{tagAdmin},
		Security:    web.BearerAuth(),
	}, h.Reclaimable)

	huma.Register(api, huma.Operation{
		OperationID: "admin-clear-zone-cooldown",
		Method:      http.MethodDelete,
		Path:        "/v2/admin/zone-health/{zone}/{machine_family}",
		Summary:     "Clear a zone-health cooldown",
		Description: "Removes the (zone, machine family) cell from the placement plane's reactive " +
			"memory: the cooldown ends and the consecutive-failure count that decays the zone's " +
			"ranking score is discarded with it. Use it to lift a cooldown once the cause is " +
			"fixed (a raised quota, say) instead of waiting out the cell's retention. Idempotent: " +
			"clearing a cell that is not there succeeds.",
		Tags:          []string{tagAdmin},
		Security:      web.BearerAuth(),
		DefaultStatus: http.StatusNoContent,
	}, h.ClearZoneCooldown)

	huma.Register(api, huma.Operation{
		OperationID: "admin-provisioning-stats",
		Method:      http.MethodGet,
		Path:        "/v2/admin/provisioning-stats",
		Summary:     "Get provisioning success statistics",
		Description: "Returns 7-day provisioning outcome counts for devices and hosts",
		Tags:        []string{tagAdmin},
		Security:    web.BearerAuth(),
	}, h.ProvisioningStats)
}

// Stats returns platform-wide statistics (Huma handler). It is a pure read:
// quota initialization happens at startup, never inside a GET.
func (h *AdminHandler) Stats(ctx context.Context, _ *StatsInput) (*StatsOutput, error) {
	output := &StatsOutput{}
	output.Body.DevicesByStatus = make(map[string]int64)
	output.Body.HostsByStatus = make(map[string]int64)
	output.Body.Timestamp = time.Now()

	if h.hostCounter != nil {
		counts, err := h.hostCounter.CountByStatus(ctx)
		if err != nil {
			h.logger.Warn("failed to count hosts by status", slog.Any("error", err))
		}
		total, active := tallyStatusCounts(counts, host.StatusDeleted, host.StatusRunning, output.Body.HostsByStatus)
		output.Body.Hosts = HostStats{Total: total, Active: active}
	}

	if h.deviceCounter != nil {
		counts, err := h.deviceCounter.CountByStatus(ctx)
		if err != nil {
			h.logger.Warn("failed to count devices by status", slog.Any("error", err))
		}
		output.Body.Devices.Total, output.Body.Devices.Active = tallyStatusCounts(counts, device.StatusDeleted, device.StatusRunning, output.Body.DevicesByStatus)
	}

	if h.quotaService != nil {
		q, err := h.quotaService.Get(ctx, quota.ScopePlatform, quota.GlobalScopeID)
		if err != nil {
			h.logger.Warn("failed to get platform quota", slog.Any("error", err))
		} else {
			output.Body.Hosts.MaxQuota = q.MaxHosts
		}
	}

	return output, nil
}

// tallyStatusCounts folds a by-status count map into out, skipping the
// deleted status; total excludes deleted, active is the running count.
func tallyStatusCounts[S ~string](counts map[S]int64, deleted, running S, out map[string]int64) (int64, int64) {
	var total, active int64
	for status, count := range counts {
		if status == deleted {
			continue
		}
		out[string(status)] = count
		total += count
		if status == running {
			active = count
		}
	}
	return total, active
}
