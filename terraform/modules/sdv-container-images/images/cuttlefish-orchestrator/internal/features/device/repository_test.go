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

package devicefeature_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	devicefeature "cloud-cuttlefish-orchestrator/internal/features/device"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// hostDeviceCount reads a host's current_device_count directly.
func hostDeviceCount(t *testing.T, db *spannertest.TestDB, hostID string) int64 {
	t.Helper()
	row, err := db.Client.Single().ReadRow(t.Context(), "hosts", spanner.Key{hostID}, []string{"current_device_count"})
	if err != nil {
		t.Fatalf("read host count: %v", err)
	}
	var n int64
	if err := row.Columns(&n); err != nil {
		t.Fatalf("scan host count: %v", err)
	}
	return n
}

// TestDeviceRepository_CRUD tests basic CRUD operations.
func TestDeviceRepository_CRUD(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	// Seed required parent data.
	hostID := "host-test-001"
	if err := db.SeedHost(ctx, hostID, "test-host", "us-central1-a"); err != nil {
		t.Fatalf("Failed to seed host: %v", err)
	}

	repo := devicefeature.NewDeviceRepository(db.Client)

	t.Run("Create", func(t *testing.T) {
		d := &device.Device{
			ID:        "device-test-001",
			HostID:    hostID,
			Name:      "test-device",
			Status:    device.StatusPending,
			GPUMode:   device.GPUModeGfxstream,
			BuildID:   "P12345678",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}

		err := repo.Create(ctx, d)
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
	})

	t.Run("Get", func(t *testing.T) {
		d, err := repo.Get(ctx, "device-test-001")
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}

		if d.Name != "test-device" {
			t.Errorf("Expected name 'test-device', got %q", d.Name)
		}
		if d.Status != device.StatusPending {
			t.Errorf("Expected status 'pending', got %q", d.Status)
		}
		if d.GPUMode != device.GPUModeGfxstream {
			t.Errorf("Expected GPU mode 'gfxstream', got %q", d.GPUMode)
		}
	})
}

// TestDeviceRepository_List tests listing devices with filters.
func TestDeviceRepository_List(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	// Seed hosts.
	host1 := "host-list-001"
	host2 := "host-list-002"
	if err := db.SeedHost(ctx, host1, "host-1", "us-central1-a"); err != nil {
		t.Fatalf("Failed to seed host 1: %v", err)
	}
	if err := db.SeedHost(ctx, host2, "host-2", "us-central1-b"); err != nil {
		t.Fatalf("Failed to seed host 2: %v", err)
	}

	repo := devicefeature.NewDeviceRepository(db.Client)

	// Create test devices.
	devices := []*device.Device{
		{ID: "dev-1", HostID: host1, Name: "device-1", Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream},
		{ID: "dev-2", HostID: host1, Name: "device-2", Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream},
		{ID: "dev-3", HostID: host2, Name: "device-3", Status: device.StatusStopped, GPUMode: device.GPUModeGfxstream},
		{ID: "dev-4", HostID: host2, Name: "device-4", Status: device.StatusDeleted, GPUMode: device.GPUModeGfxstream},
	}

	for _, d := range devices {
		if err := repo.Create(ctx, d); err != nil {
			t.Fatalf("Failed to create device %s: %v", d.ID, err)
		}
	}

	t.Run("ListAll", func(t *testing.T) {
		result, err := repo.List(ctx, device.ListFilter{})
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}
		// Should exclude deleted devices by default.
		if len(result) != 3 {
			t.Errorf("Expected 3 devices, got %d", len(result))
		}
	})

	t.Run("ListByHost", func(t *testing.T) {
		result, err := repo.ListByHost(ctx, host1)
		if err != nil {
			t.Fatalf("ListByHost failed: %v", err)
		}
		if len(result) != 2 {
			t.Errorf("Expected 2 devices on host1, got %d", len(result))
		}
	})

	t.Run("ListByStatus", func(t *testing.T) {
		status := device.StatusRunning
		result, err := repo.List(ctx, device.ListFilter{Status: &status})
		if err != nil {
			t.Fatalf("List with status filter failed: %v", err)
		}
		if len(result) != 2 {
			t.Errorf("Expected 2 running devices, got %d", len(result))
		}
	})

	t.Run("ListIncludeDeleted", func(t *testing.T) {
		result, err := repo.List(ctx, device.ListFilter{IncludeDeleted: true})
		if err != nil {
			t.Fatalf("List with include deleted failed: %v", err)
		}
		if len(result) != 4 {
			t.Errorf("Expected 4 devices (including deleted), got %d", len(result))
		}
	})

	t.Run("ListWithLimit", func(t *testing.T) {
		result, err := repo.List(ctx, device.ListFilter{Limit: 2})
		if err != nil {
			t.Fatalf("List with limit failed: %v", err)
		}
		if len(result) != 2 {
			t.Errorf("Expected 2 devices with limit, got %d", len(result))
		}
	})
}

// TestDeviceRepository_ListByCreator tests the created_by filter behind the
// mine/created_by list params. List and Count scope to one creator's rows.
func TestDeviceRepository_ListByCreator(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedHost(ctx, "host-creator", "host-creator", "us-central1-a"); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)

	seed := []*device.Device{
		{ID: "own-1", HostID: "host-creator", Name: "own-1", Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream, CreatedBy: new("alice@example.com")},
		{ID: "own-2", HostID: "host-creator", Name: "own-2", Status: device.StatusStopped, GPUMode: device.GPUModeGfxstream, CreatedBy: new("alice@example.com")},
		{ID: "foreign", HostID: "host-creator", Name: "foreign", Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream, CreatedBy: new("bob@example.com")},
		{ID: "anon", HostID: "host-creator", Name: "anon", Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream}, // NULL created_by: never matches a creator filter
	}
	for _, d := range seed {
		if err := repo.Create(ctx, d); err != nil {
			t.Fatalf("create %s: %v", d.ID, err)
		}
	}

	result, err := repo.List(ctx, device.ListFilter{CreatedBy: new("alice@example.com")})
	if err != nil {
		t.Fatalf("List(CreatedBy): %v", err)
	}
	if len(result) != 2 {
		t.Errorf("List(CreatedBy=alice) returned %d devices, want 2", len(result))
	}
	for _, d := range result {
		if d.CreatedBy == nil || *d.CreatedBy != "alice@example.com" {
			t.Errorf("device %s created_by = %v, want alice@example.com", d.ID, d.CreatedBy)
		}
	}

	count, err := repo.Count(ctx, device.ListFilter{CreatedBy: new("alice@example.com")})
	if err != nil {
		t.Fatalf("Count(CreatedBy): %v", err)
	}
	if count != 2 {
		t.Errorf("Count(CreatedBy=alice) = %d, want 2", count)
	}
}

// TestDeviceRepository_ListByBatchID tests the member listing the batch
// feature is built on: BatchID filter with a strong read (batch Get/Delete
// must see rows committed a moment ago).
func TestDeviceRepository_ListByBatchID(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedHost(ctx, "host-batch", "host-batch", "us-central1-a"); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)

	seed := []*device.Device{
		{ID: "b1-a", HostID: "host-batch", Name: "b1-a", Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream, BatchID: "batch-1"},
		{ID: "b1-b", HostID: "host-batch", Name: "b1-b", Status: device.StatusCreating, GPUMode: device.GPUModeGfxstream, BatchID: "batch-1"},
		{ID: "b2-a", HostID: "host-batch", Name: "b2-a", Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream, BatchID: "batch-2"},
		{ID: "solo", HostID: "host-batch", Name: "solo", Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream},                     // no batch: excluded
		{ID: "b3-a", HostID: "host-batch", Name: "b3-a", Status: device.StatusDeleted, GPUMode: device.GPUModeGfxstream, BatchID: "batch-3"}, // fully deleted: excluded
	}
	for _, d := range seed {
		if err := repo.Create(ctx, d); err != nil {
			t.Fatalf("create %s: %v", d.ID, err)
		}
	}

	batchID := "batch-1"
	members, err := repo.List(ctx, device.ListFilter{BatchID: &batchID, Strong: true})
	if err != nil {
		t.Fatalf("List(BatchID): %v", err)
	}
	if len(members) != 2 {
		t.Errorf("List(BatchID=batch-1) returned %d devices, want 2", len(members))
	}
	for _, d := range members {
		if d.BatchID != batchID {
			t.Errorf("member %s has batch_id %q, want %q", d.ID, d.BatchID, batchID)
		}
	}
}

// TestDeviceRepository_UpdateStatus tests status-specific updates.
func TestDeviceRepository_UpdateStatus(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	hostID := "host-status-001"
	if err := db.SeedHost(ctx, hostID, "status-host", "us-central1-a"); err != nil {
		t.Fatalf("Failed to seed host: %v", err)
	}

	repo := devicefeature.NewDeviceRepository(db.Client)

	// Create test device.
	d := &device.Device{
		ID:      "dev-status-001",
		HostID:  hostID,
		Name:    "status-test",
		Status:  device.StatusPending,
		GPUMode: device.GPUModeGfxstream,
	}
	if err := repo.Create(ctx, d); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	t.Run("InvalidTransitionRejected", func(t *testing.T) {
		// pending -> running skips creating; UpdateStatus validates against
		// the state machine.
		err := repo.UpdateStatus(ctx, d.ID, device.StatusRunning, "")
		if !errors.Is(err, device.ErrInvalidState) {
			t.Fatalf("UpdateStatus = %v, want ErrInvalidState", err)
		}
	})

	t.Run("UpdateToRunning", func(t *testing.T) {
		if err := repo.UpdateStatus(ctx, d.ID, device.StatusCreating, ""); err != nil {
			t.Fatalf("UpdateStatus to creating failed: %v", err)
		}
		err := repo.UpdateStatus(ctx, d.ID, device.StatusRunning, "")
		if err != nil {
			t.Fatalf("UpdateStatus failed: %v", err)
		}

		updated, err := repo.Get(ctx, d.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if updated.Status != device.StatusRunning {
			t.Errorf("Expected status 'running', got %q", updated.Status)
		}
		if updated.StartedAt == nil {
			t.Error("Expected StartedAt to be set")
		}
	})

	t.Run("UpdateToError", func(t *testing.T) {
		err := repo.UpdateStatus(ctx, d.ID, device.StatusError, "connection timeout")
		if err != nil {
			t.Fatalf("UpdateStatus failed: %v", err)
		}

		updated, err := repo.Get(ctx, d.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if updated.Status != device.StatusError {
			t.Errorf("Expected status 'error', got %q", updated.Status)
		}
		if updated.ErrorMessage != "connection timeout" {
			t.Errorf("Expected error message 'connection timeout', got %q", updated.ErrorMessage)
		}
	})
}

// A device healed out of error re-enters the population every lease-lowering
// write counts, so the heal must raise its host's lease exactly as a device
// commit would. The keeper shortens a host whose only device row is error (a
// non-tenant to the guard, correctly), the device controller then heals that
// row to running from the CVD observation, and without the raise the device is
// promised more life than the host has.
func TestDeviceRepository_UpdateStatusHealRaisesHostLease(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	hostID := "host-heal-001"
	if err := db.SeedHost(ctx, hostID, "heal-host", "us-central1-a"); err != nil {
		t.Fatalf("Failed to seed host: %v", err)
	}

	repo := devicefeature.NewDeviceRepository(db.Client)

	deviceExpiry := time.Now().Add(90 * time.Minute).UTC()
	d := &device.Device{
		ID:        "dev-heal-001",
		HostID:    hostID,
		Name:      "heal-test",
		Status:    device.StatusPending,
		GPUMode:   device.GPUModeSwift,
		Lifecycle: device.DeviceLifecycle{Mode: device.LifecycleModeEphemeral, ExpiresAt: &deviceExpiry},
	}
	if err := repo.Create(ctx, d); err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	for _, st := range []device.Status{device.StatusCreating, device.StatusError} {
		if err := repo.UpdateStatus(ctx, d.ID, st, ""); err != nil {
			t.Fatalf("UpdateStatus to %s failed: %v", st, err)
		}
	}

	// The shortened lease: an ephemeral host run down while its only device
	// row was error, the state ShortenLease legitimately writes.
	shortened := time.Now().Add(5 * time.Minute).UTC()
	if _, err := db.Client.Apply(ctx, []*spanner.Mutation{spanner.UpdateMap("hosts", map[string]any{
		"host_id": hostID, "lifecycle_mode": "ephemeral", "expires_at": shortened,
	})}); err != nil {
		t.Fatalf("shortening host lease: %v", err)
	}

	if err := repo.UpdateStatus(ctx, d.ID, device.StatusRunning, ""); err != nil {
		t.Fatalf("UpdateStatus heal to running failed: %v", err)
	}

	row, err := db.Client.Single().ReadRow(ctx, "hosts", spanner.Key{hostID},
		[]string{"expires_at", "current_device_count"})
	if err != nil {
		t.Fatalf("reading host lease: %v", err)
	}
	var hostExpiry spanner.NullTime
	var count int64
	if err := row.Columns(&hostExpiry, &count); err != nil {
		t.Fatalf("scanning host lease: %v", err)
	}
	promised := deviceExpiry.Add(time.Duration(host.GracePeriodSeconds) * time.Second)
	if !hostExpiry.Valid || hostExpiry.Time.Before(promised) {
		t.Errorf("host lease after heal = %v, want at least the device's %v plus the teardown grace (%v)",
			hostExpiry, deviceExpiry, promised)
	}
	// The raise rides zero counter deltas: healing a row is not a second
	// admission.
	if count != 0 {
		t.Errorf("current_device_count after heal = %d, want 0 (heal must not re-charge the slot)", count)
	}
}

// TestDeviceRepository_Health tests health-related operations.
//
//nolint:gocyclo // Each branch is one assertion, and the subtests read the state the previous one wrote.
func TestDeviceRepository_Health(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	hostID := "host-health-001"
	if err := db.SeedHost(ctx, hostID, "health-host", "us-central1-a"); err != nil {
		t.Fatalf("Failed to seed host: %v", err)
	}

	repo := devicefeature.NewDeviceRepository(db.Client)

	// Create test devices with different health states.
	devices := []*device.Device{
		{ID: "dev-h1", HostID: hostID, Name: "healthy-1", Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream},
		{ID: "dev-h2", HostID: hostID, Name: "unhealthy-1", Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream},
	}
	for _, d := range devices {
		if err := repo.Create(ctx, d); err != nil {
			t.Fatalf("Create failed: %v", err)
		}
	}

	t.Run("BatchUpdateHealth", func(t *testing.T) {
		err := repo.BatchUpdateHealth(ctx, []device.HealthUpdate{
			{DeviceID: "dev-h1", HostID: hostID, Status: device.HealthStatusHealthy},
			{
				DeviceID: "dev-h2", HostID: hostID, Status: device.HealthStatusUnhealthy,
				ConsecutiveFails: 3, FailReason: "ADB connection failed",
			},
		})
		if err != nil {
			t.Fatalf("BatchUpdateHealth failed: %v", err)
		}

		d, err := repo.Get(ctx, "dev-h1")
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if d.HealthStatus != device.HealthStatusHealthy {
			t.Errorf("Expected health status 'healthy', got %q", d.HealthStatus)
		}
		if d.LastHealthCheck == nil {
			t.Error("Expected LastHealthCheck to be set")
		}
		if d.LastHealthyAt == nil {
			t.Error("Expected LastHealthyAt to be set")
		}

		d, err = repo.Get(ctx, "dev-h2")
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if d.HealthStatus != device.HealthStatusUnhealthy {
			t.Errorf("Expected health status 'unhealthy', got %q", d.HealthStatus)
		}
		if d.ConsecutiveFails != 3 {
			t.Errorf("Expected 3 consecutive fails, got %d", d.ConsecutiveFails)
		}
		if d.LastFailureReason != "ADB connection failed" {
			t.Errorf("Expected failure reason 'ADB connection failed', got %q", d.LastFailureReason)
		}
	})

	t.Run("BatchUpdateHealthTimestampOnly", func(t *testing.T) {
		// A timestamp-only update advances the check timestamps but must not
		// touch the health columns (steady-state healthy hot path).
		before, err := repo.Get(ctx, "dev-h2")
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		err = repo.BatchUpdateHealth(ctx, []device.HealthUpdate{
			{DeviceID: "dev-h2", HostID: hostID, Status: device.HealthStatusHealthy, TimestampOnly: true},
		})
		if err != nil {
			t.Fatalf("BatchUpdateHealth failed: %v", err)
		}
		after, err := repo.Get(ctx, "dev-h2")
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if after.HealthStatus != before.HealthStatus || after.ConsecutiveFails != before.ConsecutiveFails {
			t.Errorf("timestamp-only update changed health columns: %q/%d -> %q/%d",
				before.HealthStatus, before.ConsecutiveFails, after.HealthStatus, after.ConsecutiveFails)
		}
		if after.LastHealthCheck == nil || !after.LastHealthCheck.After(*before.LastHealthCheck) {
			t.Error("Expected LastHealthCheck to advance")
		}
	})
}

// TestDeviceRepository_Lifecycle tests lifecycle-related operations.
func TestDeviceRepository_Lifecycle(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	hostID := "host-lifecycle-001"
	if err := db.SeedHost(ctx, hostID, "lifecycle-host", "us-central1-a"); err != nil {
		t.Fatalf("Failed to seed host: %v", err)
	}

	repo := devicefeature.NewDeviceRepository(db.Client)

	// Create test device.
	d := &device.Device{
		ID:      "dev-lifecycle-001",
		HostID:  hostID,
		Name:    "lifecycle-test",
		Status:  device.StatusRunning,
		GPUMode: device.GPUModeGfxstream,
		Lifecycle: device.DeviceLifecycle{
			Mode:       device.LifecycleModeEphemeral,
			TTLSeconds: 14400,
		},
	}
	if err := repo.Create(ctx, d); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	t.Run("ListExpired", func(t *testing.T) {
		// Create an expired device.
		pastTime := time.Now().Add(-1 * time.Hour)
		expiredDevice := &device.Device{
			ID:      "dev-expired-001",
			HostID:  hostID,
			Name:    "expired-test",
			Status:  device.StatusRunning,
			GPUMode: device.GPUModeGfxstream,
			Lifecycle: device.DeviceLifecycle{
				Mode:      device.LifecycleModeEphemeral,
				ExpiresAt: &pastTime,
			},
		}
		if err := repo.Create(ctx, expiredDevice); err != nil {
			t.Fatalf("Create expired device failed: %v", err)
		}

		expired, err := repo.ListExpired(ctx, 10)
		if err != nil {
			t.Fatalf("ListExpired failed: %v", err)
		}
		if len(expired) < 1 {
			t.Errorf("Expected at least 1 expired device, got %d", len(expired))
		}

		found := false
		for _, e := range expired {
			if e.ID == "dev-expired-001" {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected to find dev-expired-001 in expired list")
		}
	})
}

// TestDeviceRepository_CountOperations tests counting operations.
func TestDeviceRepository_CountOperations(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	// Seed hosts.
	host1 := "host-count-001"
	host2 := "host-count-002"
	if err := db.SeedHost(ctx, host1, "count-host-1", "us-central1-a"); err != nil {
		t.Fatalf("Failed to seed host 1: %v", err)
	}
	if err := db.SeedHost(ctx, host2, "count-host-2", "us-central1-b"); err != nil {
		t.Fatalf("Failed to seed host 2: %v", err)
	}

	repo := devicefeature.NewDeviceRepository(db.Client)

	// Create test devices.
	devices := []*device.Device{
		{ID: "cnt-1", HostID: host1, Name: "count-1", Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream},
		{ID: "cnt-2", HostID: host1, Name: "count-2", Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream},
		{ID: "cnt-3", HostID: host2, Name: "count-3", Status: device.StatusStopped, GPUMode: device.GPUModeGfxstream},
	}
	for _, d := range devices {
		if err := repo.Create(ctx, d); err != nil {
			t.Fatalf("Create failed: %v", err)
		}
	}

	t.Run("CountByStatus", func(t *testing.T) {
		counts, err := repo.CountByStatus(ctx)
		if err != nil {
			t.Fatalf("CountByStatus failed: %v", err)
		}
		if counts[device.StatusRunning] != 2 {
			t.Errorf("Expected 2 running devices, got %d", counts[device.StatusRunning])
		}
		if counts[device.StatusStopped] != 1 {
			t.Errorf("Expected 1 stopped device, got %d", counts[device.StatusStopped])
		}
	})
}

// TestDeviceRepository_CreateChargesInstances pins what a host's capacity is
// denominated in. max_devices bounds CVD INSTANCES, so a device is charged one
// slot per instance it desires and a group that does not fit is refused whole.
// A four-slot host that took a two-instance device has room for two more
// instances, not for two more devices.
func TestDeviceRepository_CreateChargesInstances(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	// Four slots, and exclusive, because a multi-instance device takes no
	// shared host.
	hostID := "host-slots-001"
	seedExclusiveHost(t, db, hostID)
	repo := devicefeature.NewDeviceRepository(db.Client)

	group := func(id string, instances int) *device.Device {
		return &device.Device{
			ID: id, HostID: hostID, Name: id, Status: device.StatusPending,
			GPUMode: device.GPUModeAuto, Mode: "advanced", InstanceCount: instances,
		}
	}

	// Eight instances against four slots: refused, and nothing is charged.
	if err := repo.CreateWithHostIncrement(ctx, group("dev-slots-eight", 8)); !errors.Is(err, host.ErrCapacityExceeded) {
		t.Fatalf("create of an 8-instance device on a 4-slot host = %v, want ErrCapacityExceeded", err)
	}
	if got := hostDeviceCount(t, db, hostID); got != 0 {
		t.Fatalf("refused create charged the host: current_device_count = %d, want 0", got)
	}
	if _, err := repo.Get(ctx, "dev-slots-eight"); !errors.Is(err, device.ErrNotFound) {
		t.Errorf("refused create left a device row: Get = %v, want ErrNotFound", err)
	}

	// Two instances fit, and cost two.
	if err := repo.CreateWithHostIncrement(ctx, group("dev-slots-pair", 2)); err != nil {
		t.Fatalf("create of a 2-instance device: %v", err)
	}
	if got := hostDeviceCount(t, db, hostID); got != 2 {
		t.Fatalf("after a 2-instance device: current_device_count = %d, want 2", got)
	}

	// Three more do not fit. Two free slots is the whole remaining capacity,
	// even though the host carries a single device row.
	if err := repo.CreateWithHostIncrement(ctx, group("dev-slots-three", 3)); !errors.Is(err, host.ErrCapacityExceeded) {
		t.Fatalf("create of a 3-instance device into 2 free slots = %v, want ErrCapacityExceeded", err)
	}

	// With the pair gone, four exactly fill it, and the host then takes
	// nothing at all. The capacity check runs before the exclusivity one, so
	// the refusal below is the capacity error.
	if _, err := repo.MarkDeletedWithHostDecrement(ctx, hostID, "dev-slots-pair"); err != nil {
		t.Fatalf("deleting the pair: %v", err)
	}
	if err := repo.CreateWithHostIncrement(ctx, group("dev-slots-fill", 4)); err != nil {
		t.Fatalf("create filling the host: %v", err)
	}
	if got := hostDeviceCount(t, db, hostID); got != 4 {
		t.Fatalf("after filling: current_device_count = %d, want 4", got)
	}
	if err := repo.CreateWithHostIncrement(ctx, group("dev-slots-one", 1)); !errors.Is(err, host.ErrCapacityExceeded) {
		t.Errorf("create onto a full host = %v, want ErrCapacityExceeded", err)
	}
}

// TestDeviceRepository_DeleteRefundsDesiredInstances pins the refund side of
// that accounting, on the device that makes a mistake permanent. A device
// deleted while still pending never reached the host, so its OBSERVED instance
// array is empty. The refund must still be the slots the create was charged,
// because the counter floors at zero, so a short refund is never recovered and
// the host reaches max_devices carrying nothing.
func TestDeviceRepository_DeleteRefundsDesiredInstances(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	// Exclusive, because a multi-instance device takes no shared host, and
	// two of them, because a multi-instance device needs an empty host.
	hostID, otherHostID := "host-refund-001", "host-refund-002"
	seedExclusiveHost(t, db, hostID)
	seedExclusiveHost(t, db, otherHostID)
	repo := devicefeature.NewDeviceRepository(db.Client)

	solo := &device.Device{
		ID: "dev-refund-solo", HostID: hostID, Name: "solo", Status: device.StatusPending,
		GPUMode: device.GPUModeAuto, Mode: "advanced", InstanceCount: 3,
	}
	if err := repo.CreateWithHostIncrement(ctx, solo); err != nil {
		t.Fatalf("CreateWithHostIncrement: %v", err)
	}
	if got := hostDeviceCount(t, db, hostID); got != 3 {
		t.Fatalf("after create: current_device_count = %d, want 3", got)
	}
	stored, err := repo.Get(ctx, solo.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(stored.Instances) != 0 {
		t.Fatalf("fixture is not the pre-host shape: %d instances observed, want none", len(stored.Instances))
	}

	transitioned, err := repo.MarkDeletedWithHostDecrement(ctx, hostID, solo.ID)
	if err != nil {
		t.Fatalf("MarkDeletedWithHostDecrement: %v", err)
	}
	if !transitioned {
		t.Fatal("delete should report transitioned=true")
	}
	if got := hostDeviceCount(t, db, hostID); got != 0 {
		t.Fatalf("after deleting a 3-instance device: current_device_count = %d, want 0", got)
	}

	// The batch path refunds the same way, summing slots rather than rows.
	batch := []*device.Device{
		{
			ID: "dev-refund-b1", HostID: hostID, Name: "b1", Status: device.StatusPending,
			GPUMode: device.GPUModeAuto, Mode: "advanced", InstanceCount: 2,
		},
		{
			ID: "dev-refund-b2", HostID: otherHostID, Name: "b2", Status: device.StatusPending,
			GPUMode: device.GPUModeAuto, Mode: "advanced", InstanceCount: 2,
		},
	}
	for _, d := range batch {
		if err := repo.CreateWithHostIncrement(ctx, d); err != nil {
			t.Fatalf("CreateWithHostIncrement(%s): %v", d.ID, err)
		}
	}
	wantHostCount(t, db, hostID, 2, "after a 2-instance create")
	wantHostCount(t, db, otherHostID, 2, "after a 2-instance create")
	if _, err := repo.BatchMarkDeletedWithHostDecrement(ctx, batch, nil); err != nil {
		t.Fatalf("BatchMarkDeletedWithHostDecrement: %v", err)
	}
	wantHostCount(t, db, hostID, 0, "after batch-deleting 2-instance devices")
	wantHostCount(t, db, otherHostID, 0, "after batch-deleting 2-instance devices")
}

// TestDeviceRepository_UncountedInstancesCostOneSlot covers the device whose
// stored spec records no instance count, which is a row written before the
// spec carried one, restored as zero by the lenient decode. Charging what the
// row literally says would cost its host nothing, so the host would accept
// devices without ever filling up. An under-count is the same defect as the
// oversubscription it mirrors. It costs its one instance, and gets it back.
func TestDeviceRepository_UncountedInstancesCostOneSlot(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	hostID := "host-uncounted-001"
	if err := db.SeedHost(ctx, hostID, "uncounted-host", "europe-west4-a"); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)

	// No Mode and no InstanceCount, so desired_spec is never written, which is
	// exactly the shape of a row that predates the count.
	d := &device.Device{
		ID: "dev-uncounted-001", HostID: hostID, Name: "uncounted",
		Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream,
	}
	if err := repo.CreateWithHostIncrement(ctx, d); err != nil {
		t.Fatalf("CreateWithHostIncrement: %v", err)
	}
	stored, err := repo.Get(ctx, d.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.InstanceCount != 0 {
		t.Fatalf("fixture is not the uncounted shape: InstanceCount = %d, want 0", stored.InstanceCount)
	}
	if got := hostDeviceCount(t, db, hostID); got != 1 {
		t.Fatalf("uncounted device charged %d slots, want 1", got)
	}

	if _, err := repo.MarkDeletedWithHostDecrement(ctx, hostID, d.ID); err != nil {
		t.Fatalf("MarkDeletedWithHostDecrement: %v", err)
	}
	if got := hostDeviceCount(t, db, hostID); got != 0 {
		t.Errorf("after delete: current_device_count = %d, want 0", got)
	}
}

// TestDeviceRepository_MarkDeletedWithHostDecrement verifies the atomic, idempotent
// delete. It decrements the host counter exactly once, and a repeat delete does not
// double-decrement.
func TestDeviceRepository_MarkDeletedWithHostDecrement(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	hostID := "host-del-001"
	if err := db.SeedHost(ctx, hostID, "del-host", "us-central1-a"); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)

	for _, id := range []string{"dev-d1", "dev-d2"} {
		d := &device.Device{ID: id, HostID: hostID, Name: id, Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream}
		if err := repo.CreateWithHostIncrement(ctx, d); err != nil {
			t.Fatalf("CreateWithHostIncrement: %v", err)
		}
	}
	if got := hostDeviceCount(t, db, hostID); got != 2 {
		t.Fatalf("setup: current_device_count = %d, want 2", got)
	}

	transitioned, err := repo.MarkDeletedWithHostDecrement(ctx, hostID, "dev-d1")
	if err != nil {
		t.Fatalf("MarkDeletedWithHostDecrement: %v", err)
	}
	if !transitioned {
		t.Fatal("first delete should report transitioned=true")
	}
	if got := hostDeviceCount(t, db, hostID); got != 1 {
		t.Fatalf("after delete: current_device_count = %d, want 1", got)
	}

	transitioned, err = repo.MarkDeletedWithHostDecrement(ctx, hostID, "dev-d1")
	if err != nil {
		t.Fatalf("repeat MarkDeletedWithHostDecrement: %v", err)
	}
	if transitioned {
		t.Error("repeat delete should report transitioned=false")
	}
	if got := hostDeviceCount(t, db, hostID); got != 1 {
		t.Errorf("repeat delete double-decremented: current_device_count = %d, want 1", got)
	}
}

// TestDeviceRepository_BatchMarkDeletedWithHostDecrement verifies the batch path
// aggregates per-host deltas (buffered writes are not read-visible within a txn,
// so multiple devices on one host must decrement the counter by their full count)
// and skips already-deleted devices.
func TestDeviceRepository_BatchMarkDeletedWithHostDecrement(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	hostID := "host-batch-001"
	if err := db.SeedHost(ctx, hostID, "batch-host", "us-central1-a"); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)

	devs := []*device.Device{
		{ID: "b1", HostID: hostID, Name: "b1", Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream},
		{ID: "b2", HostID: hostID, Name: "b2", Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream},
		{ID: "b3", HostID: hostID, Name: "b3", Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream},
	}
	for _, d := range devs {
		if err := repo.CreateWithHostIncrement(ctx, d); err != nil {
			t.Fatalf("CreateWithHostIncrement: %v", err)
		}
	}

	// Pre-delete one so the batch must skip it (idempotency) and decrement by the
	// number actually transitioned.
	if _, err := repo.MarkDeletedWithHostDecrement(ctx, hostID, "b1"); err != nil {
		t.Fatalf("pre-delete: %v", err)
	}
	if got := hostDeviceCount(t, db, hostID); got != 2 {
		t.Fatalf("after pre-delete: current_device_count = %d, want 2", got)
	}

	// Per-device extras must commit only with their device's transition. b1 is
	// already deleted, so its extra (a queued cleanup task) must be dropped.
	extraFor := map[string]*spanner.Mutation{
		"b1": taskInsertMutation("task-b1"),
		"b2": taskInsertMutation("task-b2"),
	}
	transitioned, err := repo.BatchMarkDeletedWithHostDecrement(ctx, devs, extraFor)
	if err != nil {
		t.Fatalf("BatchMarkDeletedWithHostDecrement: %v", err)
	}
	if len(transitioned) != 2 {
		t.Errorf("transitioned = %d, want 2 (b1 already deleted)", len(transitioned))
	}
	if got := hostDeviceCount(t, db, hostID); got != 0 {
		t.Errorf("after batch: current_device_count = %d, want 0 (two same-host devices decremented together)", got)
	}
	if taskRowExists(t, db, "task-b1") {
		t.Error("extra mutation for already-deleted b1 was committed; want dropped")
	}
	if !taskRowExists(t, db, "task-b2") {
		t.Error("extra mutation for transitioned b2 was not committed")
	}
}

func taskInsertMutation(id string) *spanner.Mutation {
	return spanner.InsertMap("tasks", map[string]any{
		"task_id":   id,
		"shard":     int64(0),
		"task_type": "device.cleanup_cvd",
	})
}

func taskRowExists(t *testing.T, db *spannertest.TestDB, id string) bool {
	t.Helper()
	_, err := db.Client.Single().ReadRow(t.Context(), "tasks", spanner.Key{id}, []string{"task_id"})
	if spanner.ErrCode(err) == codes.NotFound {
		return false
	}
	if err != nil {
		t.Fatalf("reading task %s: %v", id, err)
	}
	return true
}

// TestDeviceRepository_TagsRoundTrip verifies tags persist through create/get.
func TestDeviceRepository_TagsRoundTrip(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	hostID := "host-tags-001"
	if err := db.SeedHost(ctx, hostID, "tags-host", "us-central1-a"); err != nil {
		t.Fatalf("Failed to seed host: %v", err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)

	d := &device.Device{
		ID:      "dev-tags-001",
		HostID:  hostID,
		Name:    "tags-test",
		Status:  device.StatusRunning,
		GPUMode: device.GPUModeGfxstream,
		Tags:    []string{"ci", "team-a"},
		Lifecycle: device.DeviceLifecycle{
			Mode:       device.LifecycleModeEphemeral,
			TTLSeconds: 14400,
		},
	}
	if err := repo.Create(ctx, d); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	got, err := repo.Get(ctx, d.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "ci" || got.Tags[1] != "team-a" {
		t.Errorf("got tags %v, want [ci team-a]", got.Tags)
	}
}

// hostProtection reads the host lifecycle columns that device pin flips adjust.
func hostProtection(t *testing.T, db *spannertest.TestDB, hostID string) (protected int64, mode string, expiresAt spanner.NullTime) {
	t.Helper()
	row, err := db.Client.Single().ReadRow(t.Context(), "hosts", spanner.Key{hostID},
		[]string{"protected_device_count", "lifecycle_mode", "expires_at"})
	if err != nil {
		t.Fatalf("read host protection: %v", err)
	}
	var protectedCol spanner.NullInt64
	var modeCol spanner.NullString
	if err := row.Columns(&protectedCol, &modeCol, &expiresAt); err != nil {
		t.Fatalf("scan host protection: %v", err)
	}
	return protectedCol.Int64, modeCol.StringVal, expiresAt
}

// assertHostProtection asserts the host lifecycle columns that device pin
// flips adjust: the protected count, the lifecycle mode, and whether an
// expiry is set (protected hosts have none; ephemeral hosts have one).
func assertHostProtection(t *testing.T, db *spannertest.TestDB, hostID string, wantProtected int64, wantMode string, wantExpiry bool) {
	t.Helper()
	protected, mode, expiresAt := hostProtection(t, db, hostID)
	if protected != wantProtected {
		t.Errorf("protected_device_count = %d, want %d", protected, wantProtected)
	}
	if mode != wantMode {
		t.Errorf("host lifecycle_mode = %q, want %q", mode, wantMode)
	}
	if expiresAt.Valid != wantExpiry {
		t.Errorf("host expires_at = %v, want set=%t", expiresAt, wantExpiry)
	}
}

// TestDeviceRepository_MutatePinFlip verifies the transactional pin contract.
// A pin flip through Mutate adjusts hosts.protected_device_count AND the
// host's lifecycle_mode/expires_at in the same commit, and an unpin restores
// the ephemeral state.
func TestDeviceRepository_MutatePinFlip(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	hostID := "host-pin-001"
	if err := db.SeedHost(ctx, hostID, "pin-host", "us-central1-a"); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)

	d := &device.Device{
		ID: "dev-pin-001", HostID: hostID, Name: "pin-me",
		Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream,
		Lifecycle: device.NewEphemeralLifecycle(0),
	}
	if err := repo.CreateWithHostIncrement(ctx, d); err != nil {
		t.Fatalf("CreateWithHostIncrement: %v", err)
	}

	// Pin: device lifecycle and host protection flip in one commit.
	pinned, err := repo.Mutate(ctx, d.ID, func(cur *device.Device) error {
		cur.Lifecycle.Pin("pinner@example.com", device.PinReasonOvernightTest, "", nil)
		return nil
	})
	if err != nil {
		t.Fatalf("Mutate(pin): %v", err)
	}
	if !pinned.Lifecycle.IsPinned() {
		t.Fatal("device not pinned after Mutate")
	}
	assertHostProtection(t, db, hostID, 1, "protected", false)
	stored, err := repo.Get(ctx, d.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !stored.Lifecycle.IsPinned() || stored.Lifecycle.ExpiresAt != nil {
		t.Errorf("stored device lifecycle = %+v, want pinned without expiry", stored.Lifecycle)
	}

	// Unpin restores the ephemeral state on both the device and the host.
	unpinned, err := repo.Mutate(ctx, d.ID, func(cur *device.Device) error {
		cur.Lifecycle.Unpin()
		return nil
	})
	if err != nil {
		t.Fatalf("Mutate(unpin): %v", err)
	}
	if !unpinned.Lifecycle.IsEphemeral() || unpinned.Lifecycle.ExpiresAt == nil {
		t.Errorf("device lifecycle after unpin = %+v, want ephemeral with expiry", unpinned.Lifecycle)
	}
	assertHostProtection(t, db, hostID, 0, "ephemeral", true)

	// A no-flip mutation must not touch the host counters.
	if _, err := repo.Mutate(ctx, d.ID, func(cur *device.Device) error {
		cur.Name = "renamed"
		return nil
	}); err != nil {
		t.Fatalf("Mutate(rename): %v", err)
	}
	assertHostProtection(t, db, hostID, 0, "ephemeral", true)
}

// TestDeviceRepository_MutateRejectsTombstone pins the pin-races-delete leak
// the robustness harness caught. A Mutate landing after the delete transition
// must not touch the tombstone. Its delete already settled the host
// counters, so a pin here would raise protected_device_count with no
// compensating transition ever coming.
func TestDeviceRepository_MutateRejectsTombstone(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	hostID := "host-tomb-001"
	if err := db.SeedHost(ctx, hostID, "tomb-host", "us-central1-a"); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)

	d := &device.Device{
		ID: "dev-tomb-001", HostID: hostID, Name: "tomb-me",
		Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream,
		Lifecycle: device.NewEphemeralLifecycle(0),
	}
	if err := repo.CreateWithHostIncrement(ctx, d); err != nil {
		t.Fatalf("CreateWithHostIncrement: %v", err)
	}
	if _, err := repo.MarkDeletedWithHostDecrement(ctx, hostID, d.ID); err != nil {
		t.Fatalf("MarkDeletedWithHostDecrement: %v", err)
	}

	_, err := repo.Mutate(ctx, d.ID, func(cur *device.Device) error {
		cur.Lifecycle.Pin("pinner@example.com", device.PinReasonOvernightTest, "", nil)
		return nil
	})
	if !errors.Is(err, device.ErrNotFound) {
		t.Fatalf("Mutate on tombstone: err = %v, want ErrNotFound", err)
	}
	assertHostProtection(t, db, hostID, 0, "ephemeral", true)

	stored, err := repo.Get(ctx, d.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Lifecycle.IsPinned() {
		t.Error("tombstone acquired a pin")
	}
}

// TestDeviceRepository_DeletePinnedReleasesProtection verifies that deleting a
// PINNED device decrements both current_device_count and protected_device_count
// in one commit, idempotently (a second delete no-ops on both).
func TestDeviceRepository_DeletePinnedReleasesProtection(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	hostID := "host-pindel-001"
	if err := db.SeedHost(ctx, hostID, "pindel-host", "us-central1-a"); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)

	d := &device.Device{
		ID: "dev-pindel-001", HostID: hostID, Name: "pinned-victim",
		Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream,
		Lifecycle: device.NewEphemeralLifecycle(0),
	}
	if err := repo.CreateWithHostIncrement(ctx, d); err != nil {
		t.Fatalf("CreateWithHostIncrement: %v", err)
	}
	if _, err := repo.Mutate(ctx, d.ID, func(cur *device.Device) error {
		cur.Lifecycle.Pin("pinner@example.com", device.PinReasonDemoPreparation, "", nil)
		return nil
	}); err != nil {
		t.Fatalf("Mutate(pin): %v", err)
	}
	if got := hostDeviceCount(t, db, hostID); got != 1 {
		t.Fatalf("setup: current_device_count = %d, want 1", got)
	}
	if protected, _, _ := hostProtection(t, db, hostID); protected != 1 {
		t.Fatalf("setup: protected_device_count = %d, want 1", protected)
	}

	transitioned, err := repo.MarkDeletedWithHostDecrement(ctx, hostID, d.ID)
	if err != nil {
		t.Fatalf("MarkDeletedWithHostDecrement: %v", err)
	}
	if !transitioned {
		t.Fatal("first delete should report transitioned=true")
	}
	if got := hostDeviceCount(t, db, hostID); got != 0 {
		t.Errorf("current_device_count = %d, want 0", got)
	}
	assertHostProtection(t, db, hostID, 0, "ephemeral", true)

	// Second delete: idempotent no-op on the row and both counters.
	transitioned, err = repo.MarkDeletedWithHostDecrement(ctx, hostID, d.ID)
	if err != nil {
		t.Fatalf("repeat MarkDeletedWithHostDecrement: %v", err)
	}
	if transitioned {
		t.Error("repeat delete should report transitioned=false")
	}
	if got := hostDeviceCount(t, db, hostID); got != 0 {
		t.Errorf("repeat delete changed current_device_count = %d, want 0", got)
	}
	if protected, _, _ := hostProtection(t, db, hostID); protected != 0 {
		t.Errorf("repeat delete changed protected_device_count = %d, want 0", protected)
	}
}

// hostExpiry reads a host's lifecycle expiry directly.
func hostExpiry(t *testing.T, db *spannertest.TestDB, hostID string) spanner.NullTime {
	t.Helper()
	row, err := db.Client.Single().ReadRow(t.Context(), "hosts", spanner.Key{hostID}, []string{"expires_at"})
	if err != nil {
		t.Fatalf("reading host expiry: %v", err)
	}
	var exp spanner.NullTime
	if err := row.Columns(&exp); err != nil {
		t.Fatalf("scanning host expiry: %v", err)
	}
	return exp
}

// setHostExpiry pins a host's expires_at for lease-raise assertions.
func setHostExpiry(t *testing.T, db *spannertest.TestDB, hostID string, at time.Time) {
	t.Helper()
	if _, err := db.Client.Apply(t.Context(), []*spanner.Mutation{
		spanner.UpdateMap("hosts", map[string]any{
			"host_id": hostID, "expires_at": at, "updated_at": time.Now(),
		}),
	}); err != nil {
		t.Fatalf("setting host expiry: %v", err)
	}
}

func TestDeviceRepository_ListLapsedPins(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := devicefeature.NewDeviceRepository(db.Client)
	if err := db.SeedHost(ctx, "pin-host", "pin-host", "us-central1-a"); err != nil {
		t.Fatalf("SeedHost: %v", err)
	}

	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	lapsed := &device.Device{ID: "pin-lapsed", HostID: "pin-host", Name: "lapsed", Status: device.StatusRunning}
	lapsed.Lifecycle.Pin("u@example.com", device.PinReasonOvernightTest, "", &past)
	active := &device.Device{ID: "pin-active", HostID: "pin-host", Name: "active", Status: device.StatusRunning}
	active.Lifecycle.Pin("u@example.com", device.PinReasonOvernightTest, "", &future)
	plain := &device.Device{ID: "pin-none", HostID: "pin-host", Name: "plain", Status: device.StatusRunning}
	plain.Lifecycle = device.NewEphemeralLifecycle(0)
	for _, d := range []*device.Device{lapsed, active, plain} {
		if err := repo.Create(ctx, d); err != nil {
			t.Fatalf("Create %s: %v", d.ID, err)
		}
	}

	got, err := repo.ListLapsedPins(ctx, 10)
	if err != nil {
		t.Fatalf("ListLapsedPins: %v", err)
	}
	if len(got) != 1 || got[0].ID != "pin-lapsed" {
		ids := make([]string, len(got))
		for i, d := range got {
			ids[i] = d.ID
		}
		t.Errorf("ListLapsedPins = %v, want [pin-lapsed]", ids)
	}
}

// The host lease. Any device write that raises the device's promised expiry
// raises the host's expires_at to promise+grace in the same commit, and a
// promise the host already outlives changes nothing.
func TestDeviceRepository_HostLeaseRaise(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := devicefeature.NewDeviceRepository(db.Client)
	if err := db.SeedHost(ctx, "lease-host", "lease-host", "us-central1-a"); err != nil {
		t.Fatalf("SeedHost: %v", err)
	}
	grace := time.Duration(host.GracePeriodSeconds) * time.Second

	// Host expiring in 1h; a 4h device must raise it to deviceExpiry+grace.
	setHostExpiry(t, db, "lease-host", time.Now().Add(time.Hour))
	d := &device.Device{ID: "lease-dev", HostID: "lease-host", Name: "lease-dev", Status: device.StatusPending}
	d.Lifecycle = device.NewEphemeralLifecycle(4 * 3600)
	if err := repo.CreateWithHostIncrement(ctx, d); err != nil {
		t.Fatalf("CreateWithHostIncrement: %v", err)
	}
	exp := hostExpiry(t, db, "lease-host")
	want := d.Lifecycle.ExpiresAt.Add(grace)
	if !exp.Valid || !exp.Time.Equal(want) {
		t.Errorf("host expiry after create = %v, want %v", exp, want)
	}

	// Extending the device raises the host again.
	extended, err := repo.Mutate(ctx, "lease-dev", func(cur *device.Device) error {
		return cur.Lifecycle.Extend(6*3600, device.DefaultExtendCount)
	})
	if err != nil {
		t.Fatalf("Mutate(extend): %v", err)
	}
	exp = hostExpiry(t, db, "lease-host")
	want = extended.Lifecycle.ExpiresAt.Add(grace)
	if !exp.Valid || !exp.Time.Equal(want) {
		t.Errorf("host expiry after extend = %v, want %v", exp, want)
	}

	// A promise the host already outlives is not a raise (monotone).
	far := time.Now().Add(48 * time.Hour)
	setHostExpiry(t, db, "lease-host", far)
	d2 := &device.Device{ID: "lease-dev-2", HostID: "lease-host", Name: "lease-dev-2", Status: device.StatusPending}
	d2.Lifecycle = device.NewEphemeralLifecycle(0)
	if err := repo.CreateWithHostIncrement(ctx, d2); err != nil {
		t.Fatalf("CreateWithHostIncrement: %v", err)
	}
	exp = hostExpiry(t, db, "lease-host")
	if !exp.Valid || !exp.Time.Equal(far) {
		t.Errorf("host expiry shrank: %v, want %v", exp, far)
	}
}

// TestDeviceRepository_ListOrderIsTotal pins the order a paging client depends
// on. The fixture gives every row the same created_at, which is what a batch
// create produces, and lays the ids out so that device_id DESC disagrees with
// the table's own (host_id, device_id) order, which is what a scan returns when
// the sort key ties. Walking the list one row at a time must then still visit
// each device exactly once.
func TestDeviceRepository_ListOrderIsTotal(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	for _, h := range []string{"page-host-a", "page-host-b"} {
		if err := db.SeedHost(ctx, h, h, "us-central1-a"); err != nil {
			t.Fatalf("SeedHost(%s): %v", h, err)
		}
	}

	repo := devicefeature.NewDeviceRepository(db.Client)
	// Ascending device_id on descending host_id. Scan order and the ordering
	// the API publishes are exact reverses of each other.
	seeded := []*device.Device{
		{ID: "page-dev-1", HostID: "page-host-b", Name: "d1", Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream},
		{ID: "page-dev-2", HostID: "page-host-a", Name: "d2", Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream},
	}
	for _, d := range seeded {
		if err := repo.Create(ctx, d); err != nil {
			t.Fatalf("Create(%s): %v", d.ID, err)
		}
	}

	// Create stamps its own timestamp, so collapse them afterwards. Distinct
	// timestamps would decide the order and the tie would never be exercised.
	shared := time.Now().UTC().Truncate(time.Microsecond)
	muts := make([]*spanner.Mutation, 0, len(seeded))
	for _, d := range seeded {
		muts = append(muts, spanner.UpdateMap("devices", map[string]any{
			"host_id": d.HostID, "device_id": d.ID, "created_at": shared,
		}))
	}
	if _, err := db.Client.Apply(ctx, muts); err != nil {
		t.Fatalf("collapsing created_at: %v", err)
	}

	full, err := repo.List(ctx, device.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	gotFull := make([]string, len(full))
	for i, d := range full {
		gotFull[i] = d.ID
	}
	if want := []string{"page-dev-2", "page-dev-1"}; !slices.Equal(gotFull, want) {
		t.Errorf("List order under equal created_at = %v, want %v", gotFull, want)
	}

	paged := make([]string, 0, len(seeded))
	for offset := range seeded {
		page, err := repo.List(ctx, device.ListFilter{Limit: 1, Offset: offset})
		if err != nil {
			t.Fatalf("List(offset=%d): %v", offset, err)
		}
		if len(page) != 1 {
			t.Fatalf("List(offset=%d) returned %d rows, want 1", offset, len(page))
		}
		paged = append(paged, page[0].ID)
	}
	if !slices.Equal(paged, gotFull) {
		t.Errorf("one-row pages walked %v, want %v, so offset paging skipped or repeated a row", paged, gotFull)
	}
}

// The reaper's two device listings hold the salted-sweep contract (see
// spannertest.AssertShuffledSweep), over a fixture that also carries the rows
// each predicate must keep excluding however the order moves.
func TestDeviceRepository_ExpirySweepsShuffle(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := devicefeature.NewDeviceRepository(db.Client)
	if err := db.SeedHost(ctx, "shuffle-host", "shuffle-host", "us-central1-a"); err != nil {
		t.Fatalf("SeedHost: %v", err)
	}

	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	const candidates = 12
	wantExpired := make([]string, 0, candidates)
	wantLapsed := make([]string, 0, candidates)
	for i := range candidates {
		expired := &device.Device{
			ID: fmt.Sprintf("shuffle-expired-%02d", i), HostID: "shuffle-host",
			Name:   fmt.Sprintf("shuffle-expired-%02d", i),
			Status: device.StatusRunning,
			Lifecycle: device.DeviceLifecycle{
				Mode: device.LifecycleModeEphemeral, ExpiresAt: &past,
			},
		}
		lapsed := &device.Device{
			ID: fmt.Sprintf("shuffle-lapsed-%02d", i), HostID: "shuffle-host",
			Name: fmt.Sprintf("shuffle-lapsed-%02d", i), Status: device.StatusRunning,
		}
		lapsed.Lifecycle.Pin("u@example.com", device.PinReasonOvernightTest, "", &past)
		for _, d := range []*device.Device{expired, lapsed} {
			if err := repo.Create(ctx, d); err != nil {
				t.Fatalf("Create %s: %v", d.ID, err)
			}
		}
		wantExpired = append(wantExpired, expired.ID)
		wantLapsed = append(wantLapsed, lapsed.ID)
	}
	// Neither sweep may ever see these, however the rows are ordered.
	unexpired := &device.Device{
		ID: "shuffle-unexpired", HostID: "shuffle-host", Name: "shuffle-unexpired",
		Status:    device.StatusRunning,
		Lifecycle: device.DeviceLifecycle{Mode: device.LifecycleModeEphemeral, ExpiresAt: &future},
	}
	if err := repo.Create(ctx, unexpired); err != nil {
		t.Fatalf("Create %s: %v", unexpired.ID, err)
	}
	activePin := &device.Device{
		ID: "shuffle-active-pin", HostID: "shuffle-host", Name: "shuffle-active-pin",
		Status: device.StatusRunning,
	}
	activePin.Lifecycle.Pin("u@example.com", device.PinReasonOvernightTest, "", &future)
	if err := repo.Create(ctx, activePin); err != nil {
		t.Fatalf("Create %s: %v", activePin.ID, err)
	}

	sweeps := []struct {
		name string
		list func(int) ([]*device.Device, error)
		want []string
	}{
		{"ListExpired", func(limit int) ([]*device.Device, error) { return repo.ListExpired(ctx, limit) }, wantExpired},
		{"ListLapsedPins", func(limit int) ([]*device.Device, error) { return repo.ListLapsedPins(ctx, limit) }, wantLapsed},
	}
	for _, sw := range sweeps {
		t.Run(sw.name, func(t *testing.T) {
			spannertest.AssertShuffledSweep(t, sw.name, sw.want, func(limit int) ([]string, error) {
				got, err := sw.list(limit)
				if err != nil {
					return nil, err
				}
				ids := make([]string, 0, len(got))
				for _, d := range got {
					ids = append(ids, d.ID)
				}
				return ids, nil
			})
		})
	}
}

// TestDeviceRepository_ListKeyset walks the list by keyset cursor while a newer
// device lands between pages. Two rows share a created_at, so the walk only
// visits each row once if the device_id tiebreak is in the predicate, and the
// insert would shift an offset walk onto a row it already returned.
func TestDeviceRepository_ListKeyset(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	const hostID = "keyset-host"
	if err := db.SeedHost(ctx, hostID, hostID, "us-central1-a"); err != nil {
		t.Fatalf("SeedHost: %v", err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)
	create := func(id string, at time.Time) {
		t.Helper()
		d := &device.Device{ID: id, HostID: hostID, Name: id, Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream}
		if err := repo.Create(ctx, d); err != nil {
			t.Fatalf("Create(%s): %v", id, err)
		}
		if _, err := db.Client.Apply(ctx, []*spanner.Mutation{spanner.UpdateMap("devices", map[string]any{
			"host_id": hostID, "device_id": id, "created_at": at,
		})}); err != nil {
			t.Fatalf("setting created_at of %s: %v", id, err)
		}
	}

	tied := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	create("keyset-dev-0", tied.Add(-time.Second))
	create("keyset-dev-1", tied)
	create("keyset-dev-2", tied)

	var walked []string
	filter := device.ListFilter{Limit: 1, Strong: true}
	for page := 0; ; page++ {
		if page > 5 {
			t.Fatalf("keyset walk did not end; walked %v", walked)
		}
		rows, err := repo.List(ctx, filter)
		if err != nil {
			t.Fatalf("List(page %d): %v", page, err)
		}
		if len(rows) == 0 {
			break
		}
		walked = append(walked, rows[0].ID)
		filter.AfterCreatedAt, filter.AfterID = rows[0].CreatedAt, rows[0].ID
		if page == 0 {
			create("keyset-dev-new", time.Now().UTC())
		}
	}
	if want := []string{"keyset-dev-2", "keyset-dev-1", "keyset-dev-0"}; !slices.Equal(walked, want) {
		t.Errorf("keyset walk = %v, want %v", walked, want)
	}

	total, err := repo.Count(ctx, filter)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if total != 4 {
		t.Errorf("Count with a cursor = %d, want 4 (the cursor must not narrow the total)", total)
	}
}

// TestDeviceRepository_ListByName checks that the name filter reads only live
// rows. Batches reuse names, so a deleted device and a live one share a name
// here; live_name is NULL on the deleted row and on a row deleted later.
func TestDeviceRepository_ListByName(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	const hostID = "name-host"
	if err := db.SeedHost(ctx, hostID, hostID, "us-central1-a"); err != nil {
		t.Fatalf("SeedHost: %v", err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)
	for _, d := range []*device.Device{
		{ID: "name-dev-live", Name: "ci-001", Status: device.StatusRunning},
		{ID: "name-dev-gone", Name: "ci-001", Status: device.StatusDeleted},
		{ID: "name-dev-other", Name: "ci-002", Status: device.StatusRunning},
	} {
		d.HostID, d.GPUMode = hostID, device.GPUModeGfxstream
		if err := repo.Create(ctx, d); err != nil {
			t.Fatalf("Create(%s): %v", d.ID, err)
		}
	}

	ids := func(filter device.ListFilter) []string {
		t.Helper()
		filter.Strong = true
		rows, err := repo.List(ctx, filter)
		if err != nil {
			t.Fatalf("List(%+v): %v", filter, err)
		}
		got := make([]string, len(rows))
		for i, d := range rows {
			got[i] = d.ID
		}
		return got
	}
	name := "ci-001"

	if got := ids(device.ListFilter{Name: &name}); !slices.Equal(got, []string{"name-dev-live"}) {
		t.Errorf("name filter = %v, want [name-dev-live]", got)
	}
	if got := ids(device.ListFilter{Name: &name, IncludeDeleted: true}); !slices.Equal(got, []string{"name-dev-live"}) {
		t.Errorf("name filter with IncludeDeleted = %v, want [name-dev-live]", got)
	}
	deleted := device.StatusDeleted
	if got := ids(device.ListFilter{Name: &name, Status: &deleted}); len(got) != 0 {
		t.Errorf("name filter with status=deleted = %v, want none", got)
	}
	if n, err := repo.Count(ctx, device.ListFilter{Name: &name}); err != nil || n != 1 {
		t.Errorf("Count(name) = %d, %v; want 1", n, err)
	}

	if _, err := db.Client.Apply(ctx, []*spanner.Mutation{spanner.UpdateMap("devices", map[string]any{
		"host_id": hostID, "device_id": "name-dev-live", "status": string(device.StatusDeleted),
	})}); err != nil {
		t.Fatalf("deleting name-dev-live: %v", err)
	}
	if got := ids(device.ListFilter{Name: &name}); len(got) != 0 {
		t.Errorf("name filter after delete = %v, want none", got)
	}
}
