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

package hostfeature

import (
	"context"
	"log/slog"

	compute "cloud.google.com/go/compute/apiv1"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

// Deps carries the host slice's injected collaborators beyond the GCE clients
// and configuration; nil fields disable the corresponding capability exactly
// as the underlying service options do.
type Deps struct {
	AuditLogger audit.Sink
	// AuditMutator commits the create's entry with the host row. Separate from
	// AuditLogger because it is the one audit write here that must not be
	// droppable: the row hard-deletes, and the fleet autoscaler counts these
	// entries under exactly the load that makes the buffered sink drop.
	AuditMutator     AuditMutator
	MachineTypes     machinetype.Catalog
	TaskEnqueuer     TaskEnqueuer
	IdempotencyStore IdempotencyStore
	CatalogValidator gcecatalog.Validator
	ZoneHealth       gcecatalog.ZoneHealthStore
	// StockoutReprobe files the capacity-advice re-probe a stockout earns; nil
	// where the advice sync is switched off and nothing would handle the task.
	StockoutReprobe StockoutReprober

	// Images is the deployment's host image resolution (the hostconfig
	// slice's service): the create preflight and the provision task read it.
	Images ImageResolver
	// SystemConfig resolves operator policy: the default host lease and the
	// shape ladder.
	SystemConfig ConfigReader

	// HostOrchestrator builds HO clients for host-poison remediation
	// (`cvd reset`).
	HostOrchestrator *hoclient.Factory

	// LiveDeviceCounter gates the destructive remediation reset on the host
	// carrying no live-device tenants (the device repository's
	// CountLiveByHost).
	LiveDeviceCounter func(ctx context.Context, hostID string) (int64, error)

	// LiveInstanceCounter gates lowering max_devices. It is not
	// LiveDeviceCounter because max_devices bounds instances, and one live
	// device can want eight of them, so a row count would let a host be shrunk
	// below what it already carries.
	LiveInstanceCounter func(ctx context.Context, hostID string) (int64, error)

	// DeviceLister joins the host CVD view to the device rows on the host
	// (the device repository's ListByHost).
	DeviceLister DeviceLister
}

// Wire creates the host feature: repository, service, and handler. The service and repository are returned for their cross-slice
// consumers (device provisioning and placement, batch admission, quota
// reconciliation, admin lenses, the reconciler/reaper/settlement jobs, the
// legacy /v1 surface).
func Wire(
	config HostServiceConfig,
	client *spannerplatform.Client,
	instances *compute.InstancesClient,
	operations *compute.ZoneOperationsClient,
	logger *slog.Logger,
	deps Deps,
) (*HostHandler, *HostService, *HostRepository) {
	repo := NewHostRepository(client)
	svc := NewHostService(config, instances, operations, repo, logger,
		WithHostAuditLogger(deps.AuditLogger),
		WithHostAuditMutator(deps.AuditMutator),
		WithMachineTypeCatalog(deps.MachineTypes),
		WithTaskEnqueuer(deps.TaskEnqueuer),
		WithHostIdempotencyStore(deps.IdempotencyStore),
		WithCatalogValidator(deps.CatalogValidator),
		WithZoneHealth(deps.ZoneHealth),
		WithStockoutReprober(deps.StockoutReprobe),
		WithImageResolver(deps.Images),
		WithHostOrchestratorFactory(deps.HostOrchestrator),
		WithLiveDeviceCounter(deps.LiveDeviceCounter),
		WithLiveInstanceCounter(deps.LiveInstanceCounter),
		WithSystemConfig(deps.SystemConfig),
	)
	handler := NewHostHandler(svc)
	handler.SetCVDViewer(NewCVDViewer(svc, deps.DeviceLister, deps.HostOrchestrator))
	return handler, svc, repo
}
