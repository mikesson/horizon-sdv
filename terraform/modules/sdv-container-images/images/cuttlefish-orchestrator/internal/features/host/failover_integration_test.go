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
	"log/slog"
	"strings"
	"testing"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
	"cloud-cuttlefish-orchestrator/tests/harness/fakegce"
)

// TestProvisionLadderFailsOverOnStockout drives the real provisioning ladder
// end to end against a stocked-out fake GCE zone: the first delivery's insert
// is rejected with zoneResourcePoolExhausted, which must cool the zone in the
// real zone_health table and move the host to the next ranked zone with a
// fresh incarnation, and the next delivery must then provision successfully
// in that zone.
func TestProvisionLadderFailsOverOnStockout(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	require.NoError(t, db.SeedQuota(ctx, 100, 0))

	gce := fakegce.New()
	t.Cleanup(gce.Close)
	gce.StockoutZone("europe-west4-a")

	instancesClient, err := compute.NewInstancesRESTClient(ctx,
		option.WithEndpoint(gce.URL()), option.WithoutAuthentication())
	require.NoError(t, err)
	t.Cleanup(func() { _ = instancesClient.Close() })
	operationsClient, err := compute.NewZoneOperationsRESTClient(ctx,
		option.WithEndpoint(gce.URL()), option.WithoutAuthentication())
	require.NoError(t, err)
	t.Cleanup(func() { _ = operationsClient.Close() })

	hostRepo := NewHostRepository(db.Client)
	zoneHealth := cspanner.NewZoneHealthRepository(db.Client)
	validator := &fakePlacementValidator{candidates: []gcecatalog.ZoneCandidate{
		{Zone: "us-central1-a", Region: "us-central1", Score: 90},
	}}
	enq := &fakeEnqueuer{}
	svc := NewHostService(
		HostServiceConfig{ProjectID: "test-project", HostOrchestratorPort: 2080},
		instancesClient, operationsClient, hostRepo, slog.New(slog.DiscardHandler),
		WithTaskEnqueuer(enq),
		WithCatalogValidator(validator),
		WithZoneHealth(zoneHealth),
		WithImageResolver(staticImages{}),
	)

	// A ranked (unpinned) host bound to the zone about to stock out.
	h, err := svc.Create(ctx, host.CreateRequest{
		Name: "fo", Zone: "europe-west4-a", MachineType: "n1-standard-8", MaxDevices: 4,
	}, "u@example.com", host.OriginUser)
	require.NoError(t, err)

	handler := NewProvisionHostHandler(svc)
	delivery := func(attempt int) *task.Task {
		tk := task.New(TaskTypeProvisionHost, h.ID)
		tk.Attempts = attempt
		tk.CreatedAt = time.Now()
		tk.LeaseExpiry = time.Now().Add(30 * time.Second)
		return tk
	}

	// First delivery: the insert is rejected by the stockout; the attempt
	// burns, the zone cools, and the host moves before the redelivery.
	err = handler.Handle(ctx, delivery(1))
	require.Error(t, err, "the stockout must surface as the delivery's error")

	moved, err := hostRepo.Get(ctx, h.ID)
	require.NoError(t, err)
	assert.Equal(t, "us-central1-a", moved.Zone)
	assert.Equal(t, int64(1), moved.Incarnation)
	assert.True(t, strings.HasSuffix(moved.GCEInstanceName, "-i1"),
		"instance name %q must embed the new incarnation", moved.GCEInstanceName)
	assert.Equal(t, host.StatusProvisioning, moved.Status)

	cells, err := zoneHealth.ListCells(ctx)
	require.NoError(t, err)
	require.Len(t, cells, 1)
	assert.Equal(t, "europe-west4-a", cells[0].Zone)
	assert.Equal(t, "n1", cells[0].MachineFamily)
	assert.Equal(t, gcecatalog.ZoneMetricCapacity, cells[0].BindingMetric)
	assert.True(t, cells[0].InCooldown(time.Now()))

	// Second delivery: inserts cleanly into the failed-over zone.
	require.NoError(t, handler.Handle(ctx, delivery(2)))

	done, err := hostRepo.Get(ctx, h.ID)
	require.NoError(t, err)
	// The VM is RUNNING but no Host Orchestrator has answered yet, so the
	// derived phase is starting.
	assert.Equal(t, host.StatusStarting, done.Status)
	assert.Equal(t, "us-central1-a", gce.InstanceZone(done.GCEInstanceName),
		"the new incarnation's VM must live in the failed-over zone")

	// The failed zone's cooldown survives (nothing succeeded there); the new
	// zone never cooled.
	cells, err = zoneHealth.ListCells(ctx)
	require.NoError(t, err)
	require.Len(t, cells, 1)
	assert.Equal(t, "europe-west4-a", cells[0].Zone)
}

// TestProvisionLadderPinnedHostRetriesInPlace is the pinned twin: the stockout
// cools the zone for every OTHER placement, but the pinned host itself never
// moves. It retries in place and, once the zone recovers, provisions there.
func TestProvisionLadderPinnedHostRetriesInPlace(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	require.NoError(t, db.SeedQuota(ctx, 100, 0))

	gce := fakegce.New()
	t.Cleanup(gce.Close)
	gce.StockoutZone("europe-west4-a")

	instancesClient, err := compute.NewInstancesRESTClient(ctx,
		option.WithEndpoint(gce.URL()), option.WithoutAuthentication())
	require.NoError(t, err)
	t.Cleanup(func() { _ = instancesClient.Close() })
	operationsClient, err := compute.NewZoneOperationsRESTClient(ctx,
		option.WithEndpoint(gce.URL()), option.WithoutAuthentication())
	require.NoError(t, err)
	t.Cleanup(func() { _ = operationsClient.Close() })

	hostRepo := NewHostRepository(db.Client)
	zoneHealth := cspanner.NewZoneHealthRepository(db.Client)
	validator := &fakePlacementValidator{candidates: []gcecatalog.ZoneCandidate{
		{Zone: "us-central1-a", Region: "us-central1", Score: 90},
	}}
	svc := NewHostService(
		HostServiceConfig{ProjectID: "test-project", HostOrchestratorPort: 2080},
		instancesClient, operationsClient, hostRepo, slog.New(slog.DiscardHandler),
		WithTaskEnqueuer(&fakeEnqueuer{}),
		WithCatalogValidator(validator),
		WithZoneHealth(zoneHealth),
		WithImageResolver(staticImages{}),
	)

	h, err := svc.Create(ctx, host.CreateRequest{
		Name: "pinned", Zone: "europe-west4-a", ZonePinned: true,
		MachineType: "n1-standard-8", MaxDevices: 4,
	}, "u@example.com", host.OriginUser)
	require.NoError(t, err)

	handler := NewProvisionHostHandler(svc)
	tk := task.New(TaskTypeProvisionHost, h.ID)
	tk.Attempts, tk.CreatedAt, tk.LeaseExpiry = 1, time.Now(), time.Now().Add(30*time.Second)
	require.Error(t, handler.Handle(ctx, tk))

	stayed, err := hostRepo.Get(ctx, h.ID)
	require.NoError(t, err)
	assert.Equal(t, "europe-west4-a", stayed.Zone, "a pinned host never moves")
	assert.Zero(t, stayed.Incarnation)

	cells, err := zoneHealth.ListCells(ctx)
	require.NoError(t, err)
	assert.Len(t, cells, 1, "the cooldown still teaches every other placement")

	// The zone recovers; the next delivery provisions in place and clears
	// the cell.
	gce.RestoreZone("europe-west4-a")
	tk2 := task.New(TaskTypeProvisionHost, h.ID)
	tk2.Attempts, tk2.CreatedAt, tk2.LeaseExpiry = 2, time.Now(), time.Now().Add(30*time.Second)
	require.NoError(t, handler.Handle(ctx, tk2))

	recovered, err := hostRepo.Get(ctx, h.ID)
	require.NoError(t, err)
	assert.Equal(t, host.StatusStarting, recovered.Status)
	assert.Equal(t, "europe-west4-a", gce.InstanceZone(recovered.GCEInstanceName))

	cells, err = zoneHealth.ListCells(ctx)
	require.NoError(t, err)
	assert.Empty(t, cells, "a successful provision clears its zone's cell")
}
