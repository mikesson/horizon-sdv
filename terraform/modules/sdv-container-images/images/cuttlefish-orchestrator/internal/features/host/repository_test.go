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

package hostfeature_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// TestHostRepository_CRUD tests basic CRUD operations.
//
//nolint:gocyclo // One straight create->get->mutate->delete walk: the count is per-field assertions, not branching.
func TestHostRepository_CRUD(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	repo := hostfeature.NewHostRepository(db.Client)

	t.Run("Create", func(t *testing.T) {
		h := &host.Host{
			ID:                   "host-test-001",
			Name:                 "test-host",
			Zone:                 "us-central1-a",
			Status:               host.StatusPending,
			GPUCount:             1,
			MinCPUPlatform:       "Intel Cascade Lake",
			BootDiskSizeGB:       750,
			MaxDevices:           4,
			CurrentDeviceCount:   0,
			HostOrchestratorPort: 2080,
			Exclusive:            true,
			ForeignCVDs:          true,
			CreatedAt:            time.Now(),
			UpdatedAt:            time.Now(),
		}

		err := repo.Create(ctx, h)
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
	})

	t.Run("Get", func(t *testing.T) {
		h, err := repo.Get(ctx, "host-test-001")
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}

		if h.Name != "test-host" {
			t.Errorf("Expected name 'test-host', got %q", h.Name)
		}
		if h.Zone != "us-central1-a" {
			t.Errorf("Expected zone 'us-central1-a', got %q", h.Zone)
		}
		if h.Status != host.StatusPending {
			t.Errorf("Expected status 'pending', got %q", h.Status)
		}
		// The provision task rebuilds the VM from the row alone, so losing
		// either of these on the round-trip means a retry/failover silently
		// reverts the shape the caller asked for.
		if h.MinCPUPlatform != "Intel Cascade Lake" {
			t.Errorf("min cpu platform = %q, want Intel Cascade Lake round-tripped", h.MinCPUPlatform)
		}
		if h.BootDiskSizeGB != 750 {
			t.Errorf("boot disk = %d GB, want 750 round-tripped", h.BootDiskSizeGB)
		}
		// Losing either flag on the round-trip silently re-arms what it
		// fences: placement (exclusive) or the orphan census (foreign_cvds).
		if !h.Exclusive {
			t.Error("exclusive = false, want true round-tripped")
		}
		if !h.ForeignCVDs {
			t.Error("foreign_cvds = false, want true round-tripped")
		}
	})

	t.Run("GetByIDs", func(t *testing.T) {
		hosts, err := repo.GetByIDs(ctx, []string{"host-test-001", "missing-host"})
		if err != nil {
			t.Fatalf("GetByIDs failed: %v", err)
		}

		if len(hosts) != 1 {
			t.Fatalf("Expected 1 host, got %d", len(hosts))
		}
		if hosts["host-test-001"] == nil || hosts["host-test-001"].Name != "test-host" {
			t.Errorf("Expected host-test-001 with name 'test-host', got %+v", hosts["host-test-001"])
		}
	})

	t.Run("Mutate", func(t *testing.T) {
		updated, err := repo.Mutate(ctx, "host-test-001", func(h *host.Host) error {
			h.Status = host.StatusRunning
			h.IPAddress = "10.128.0.50"
			h.MachineType = "n1-standard-8"
			return nil
		})
		if err != nil {
			t.Fatalf("Mutate failed: %v", err)
		}
		if updated.Status != host.StatusRunning {
			t.Errorf("Expected status 'running', got %q", updated.Status)
		}

		// Verify the change was persisted.
		got, err := repo.Get(ctx, "host-test-001")
		if err != nil {
			t.Fatalf("Get after mutate failed: %v", err)
		}
		if got.Status != host.StatusRunning {
			t.Errorf("Expected status 'running', got %q", got.Status)
		}
		if got.IPAddress != "10.128.0.50" {
			t.Errorf("Expected IP '10.128.0.50', got %q", got.IPAddress)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		err := repo.Delete(ctx, "host-test-001")
		if err != nil {
			t.Fatalf("Delete failed: %v", err)
		}

		// Verify deletion.
		_, err = repo.Get(ctx, "host-test-001")
		if !errors.Is(err, host.ErrNotFound) {
			t.Errorf("Expected ErrNotFound, got %v", err)
		}
	})
}

// The pool a create stamps survives the write and comes back on the read, and a
// row that stamped none comes back as the default pool's. The column rides the
// projection every host read shares, so a name missing from either side of it
// fails here rather than at run time as a host belonging to no pool.
func TestHostRepository_PoolRoundTrip(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)

	for _, tc := range []struct{ id, stamped, want string }{
		{"pool-unstamped", "", host.PoolDefault},
		{"pool-gpu", "gpu", "gpu"},
	} {
		h := &host.Host{
			ID: tc.id, Name: tc.id, Zone: "us-central1-a", Status: host.StatusPending,
			MaxDevices: 4, HostOrchestratorPort: 2080, Pool: tc.stamped,
		}
		if err := repo.Create(ctx, h); err != nil {
			t.Fatalf("Create(%s): %v", tc.id, err)
		}
		got, err := repo.Get(ctx, tc.id)
		if err != nil {
			t.Fatalf("Get(%s): %v", tc.id, err)
		}
		if got.Pool != tc.want {
			t.Errorf("%s: pool = %q, want %q", tc.id, got.Pool, tc.want)
		}
	}
}

// TestHostRepository_MarkPreemptedPreservesDeviceCount pins the fix for the
// Host-reconciler lost-update: MarkPreempted re-reads the row transactionally, so it
// records the preemption without clobbering current_device_count with a stale
// value (the blind full-row Update it replaced did exactly that).
func TestHostRepository_MarkPreemptedPreservesDeviceCount(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)

	h := &host.Host{
		ID:                 "host-preempt-001",
		Name:               "spot-host",
		Zone:               "us-central1-a",
		Status:             host.StatusRunning,
		ProvisioningModel:  host.ProvisioningModelSpot,
		MaxDevices:         4,
		CurrentDeviceCount: 2, // devices already placed on this host
	}
	if err := repo.Create(ctx, h); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	updated, err := repo.MarkPreempted(ctx, h.ID, "op-42")
	if err != nil {
		t.Fatalf("MarkPreempted failed: %v", err)
	}
	if updated.Status != host.StatusPreempted {
		t.Errorf("status = %q, want preempted", updated.Status)
	}
	if updated.LastPreemptionID != "op-42" {
		t.Errorf("last_preemption_id = %q, want op-42", updated.LastPreemptionID)
	}
	if updated.PreemptionCount != 1 {
		t.Errorf("preemption_count = %d, want 1", updated.PreemptionCount)
	}
	if updated.CurrentDeviceCount != 2 {
		t.Errorf("current_device_count = %d, want 2 (must not be clobbered)", updated.CurrentDeviceCount)
	}

	// Re-read to confirm the PERSISTED row rather than the returned in-memory
	// copy. The distinction is what the test turns on: last_preemption_id was
	// written in memory and silently dropped by the row model for five schema
	// versions, and only the in-memory value was ever asserted.
	got, err := repo.Get(ctx, h.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.CurrentDeviceCount != 2 {
		t.Errorf("persisted current_device_count = %d, want 2", got.CurrentDeviceCount)
	}
	if got.LastPreemptionID != "op-42" {
		t.Errorf("persisted last_preemption_id = %q, want op-42", got.LastPreemptionID)
	}
	if got.PreemptedAt == nil {
		t.Error("persisted preempted_at missing")
	}
}

// TestHostRepository_MarkPreemptedValidatesTransition pins the in-transaction
// FSM check: a host mid-deletion cannot be preempted-marked. Without it, a
// preemption racing a user delete wrote preempted over deleting, CanRetry then
// passed, and auto-recovery re-provisioned a host the user had just deleted.
func TestHostRepository_MarkPreemptedValidatesTransition(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)

	h := &host.Host{
		ID: "host-preempt-002", Name: "spot-deleting", Zone: "us-central1-a",
		Status:            host.StatusDeleting,
		ProvisioningModel: host.ProvisioningModelSpot,
		MaxDevices:        4,
	}
	if err := repo.Create(ctx, h); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	_, err := repo.MarkPreempted(ctx, h.ID, "op-43")
	if _, ok := errors.AsType[*host.InvalidStateError](err); !ok {
		t.Fatalf("MarkPreempted on a deleting host = %v, want *host.InvalidStateError", err)
	}

	got, err := repo.Get(ctx, h.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.Status != host.StatusDeleting {
		t.Errorf("status = %q, want deleting (row must be untouched)", got.Status)
	}
	if got.LastPreemptionID != "" || got.PreemptionCount != 0 {
		t.Errorf("preemption fields written despite rejected transition: id=%q count=%d",
			got.LastPreemptionID, got.PreemptionCount)
	}
}

// TestHostRepository_BatchUpdateGCEStateCAS pins the compare-and-set on the
// drift writer: an update planned from a stale snapshot must be skipped when
// the row's status moved concurrently. The scenario is a user delete racing
// the sync cycle, where the blind write this replaced resurrected the host
// (deleting -> running) and placement could then schedule onto a host
// mid-teardown.
func TestHostRepository_BatchUpdateGCEStateCAS(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)

	fresh := &host.Host{
		ID: "host-cas-fresh", Name: "fresh", Zone: "us-central1-a",
		Status: host.StatusRunning, MaxDevices: 4,
	}
	raced := &host.Host{
		ID: "host-cas-raced", Name: "raced", Zone: "us-central1-a",
		Status:     host.StatusDeleting, // moved after the sync's snapshot saw running
		MaxDevices: 4,
	}
	for _, h := range []*host.Host{fresh, raced} {
		if err := repo.Create(ctx, h); err != nil {
			t.Fatalf("Create(%s) failed: %v", h.ID, err)
		}
	}

	applied, err := repo.BatchUpdateGCEState(ctx, []host.GCEStateUpdate{
		{ID: fresh.ID, PrevStatus: host.StatusRunning, Status: host.StatusStopped, IP: "34.1.2.3"},
		{ID: raced.ID, PrevStatus: host.StatusRunning, Status: host.StatusRunning, IP: "34.9.9.9"},
	})
	if err != nil {
		t.Fatalf("BatchUpdateGCEState failed: %v", err)
	}
	if applied != 1 {
		t.Errorf("applied = %d, want 1 (stale update must miss)", applied)
	}

	got, err := repo.Get(ctx, fresh.ID)
	if err != nil {
		t.Fatalf("Get(fresh) failed: %v", err)
	}
	if got.Status != host.StatusStopped || got.IPAddress != "34.1.2.3" {
		t.Errorf("fresh row = (%s, %s), want (stopped, 34.1.2.3)", got.Status, got.IPAddress)
	}

	got, err = repo.Get(ctx, raced.ID)
	if err != nil {
		t.Fatalf("Get(raced) failed: %v", err)
	}
	if got.Status != host.StatusDeleting {
		t.Errorf("raced row status = %s, want deleting (must not be resurrected)", got.Status)
	}
	if got.IPAddress != "" {
		t.Errorf("raced row IP = %q, want unchanged empty (stale write must skip the whole row)", got.IPAddress)
	}
}

// TestHostRepository_BatchUpdateGCEStateWritesVMTerminatesAt pins the drift
// writer's persistence of the VM stop time. The horizon check on device
// create, device extend and host extend reads only this column, so a write
// that dropped it would let every lease pass the check. A nil value must write
// NULL: a stopped VM has no stop time, and a stale one would refuse leases.
func TestHostRepository_BatchUpdateGCEStateWritesVMTerminatesAt(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)

	stale := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Microsecond)
	running := &host.Host{
		ID: "host-vmterm-running", Name: "running", Zone: "us-central1-a",
		Status: host.StatusRunning, MaxDevices: 4,
	}
	stopped := &host.Host{
		ID: "host-vmterm-stopped", Name: "stopped", Zone: "us-central1-a",
		Status: host.StatusRunning, MaxDevices: 4, VMTerminatesAt: &stale,
	}
	for _, h := range []*host.Host{running, stopped} {
		if err := repo.Create(ctx, h); err != nil {
			t.Fatalf("Create(%s) failed: %v", h.ID, err)
		}
	}

	terminates := time.Now().Add(120 * 24 * time.Hour).UTC().Truncate(time.Microsecond)
	applied, err := repo.BatchUpdateGCEState(ctx, []host.GCEStateUpdate{
		{ID: running.ID, PrevStatus: host.StatusRunning, Status: host.StatusRunning, VMTerminatesAt: &terminates},
		{ID: stopped.ID, PrevStatus: host.StatusRunning, Status: host.StatusStopped},
	})
	if err != nil {
		t.Fatalf("BatchUpdateGCEState failed: %v", err)
	}
	if applied != 2 {
		t.Fatalf("applied = %d, want 2", applied)
	}

	got, err := repo.Get(ctx, running.ID)
	if err != nil {
		t.Fatalf("Get(running) failed: %v", err)
	}
	if got.VMTerminatesAt == nil || !got.VMTerminatesAt.Equal(terminates) {
		t.Errorf("running vm_terminates_at = %v, want %v", got.VMTerminatesAt, terminates)
	}

	got, err = repo.Get(ctx, stopped.ID)
	if err != nil {
		t.Fatalf("Get(stopped) failed: %v", err)
	}
	if got.VMTerminatesAt != nil {
		t.Errorf("stopped vm_terminates_at = %v, want NULL (nil update must clear the stale value)", got.VMTerminatesAt)
	}
}

// TestHostRepository_List tests listing hosts with filters.
func TestHostRepository_List(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	repo := hostfeature.NewHostRepository(db.Client)

	// Create test hosts.
	hosts := []*host.Host{
		{ID: "host-1", Name: "host-1", Zone: "us-central1-a", Status: host.StatusRunning, GPUCount: 1, MaxDevices: 4, HostOrchestratorPort: 2080},
		{ID: "host-2", Name: "host-2", Zone: "us-central1-a", Status: host.StatusRunning, GPUCount: 1, MaxDevices: 4, HostOrchestratorPort: 2080},
		{ID: "host-3", Name: "host-3", Zone: "us-central1-b", Status: host.StatusStopped, GPUCount: 1, MaxDevices: 4, HostOrchestratorPort: 2080},
		{ID: "host-4", Name: "host-4", Zone: "us-central1-b", Status: host.StatusDeleted, GPUCount: 1, MaxDevices: 4, HostOrchestratorPort: 2080},
	}

	for _, h := range hosts {
		if err := repo.Create(ctx, h); err != nil {
			t.Fatalf("Failed to create host %s: %v", h.ID, err)
		}
	}

	t.Run("ListAll", func(t *testing.T) {
		result, err := repo.List(ctx, host.ListFilter{})
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}
		// Should exclude deleted hosts by default.
		if len(result) != 3 {
			t.Errorf("Expected 3 hosts, got %d", len(result))
		}
	})

	t.Run("ListByZone", func(t *testing.T) {
		zone := "us-central1-a"
		result, err := repo.List(ctx, host.ListFilter{Zone: &zone})
		if err != nil {
			t.Fatalf("List with zone filter failed: %v", err)
		}
		if len(result) != 2 {
			t.Errorf("Expected 2 hosts in us-central1-a, got %d", len(result))
		}
	})

	t.Run("ListByStatus", func(t *testing.T) {
		status := host.StatusRunning
		result, err := repo.List(ctx, host.ListFilter{Status: &status})
		if err != nil {
			t.Fatalf("List with status filter failed: %v", err)
		}
		if len(result) != 2 {
			t.Errorf("Expected 2 running hosts, got %d", len(result))
		}
	})

	t.Run("ListIncludeDeleted", func(t *testing.T) {
		result, err := repo.List(ctx, host.ListFilter{IncludeDeleted: true})
		if err != nil {
			t.Fatalf("List with include deleted failed: %v", err)
		}
		if len(result) != 4 {
			t.Errorf("Expected 4 hosts (including deleted), got %d", len(result))
		}
	})

	t.Run("ListWithLimit", func(t *testing.T) {
		result, err := repo.List(ctx, host.ListFilter{Limit: 2})
		if err != nil {
			t.Fatalf("List with limit failed: %v", err)
		}
		if len(result) != 2 {
			t.Errorf("Expected 2 hosts with limit, got %d", len(result))
		}
	})
}

// TestHostRepository_ListByRequestedOrPhysicalZone tests the legacy /v1
// listing partition: a host created under a zone token stays listed under it
// wherever placement or failover physically lands it, while auto hosts (no
// requested zone) appear under their physical zone.
func TestHostRepository_ListByRequestedOrPhysicalZone(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)

	hosts := []*host.Host{
		// Created via the token us-central1-b, failed over to europe-west4-a.
		{
			ID: "h-moved", Name: "h-moved", Zone: "europe-west4-a", RequestedZone: "us-central1-b",
			Status: host.StatusRunning, GPUCount: 1, MaxDevices: 4, HostOrchestratorPort: 2080,
		},
		// A /v2-auto host that physically landed in us-central1-b.
		{
			ID: "h-auto", Name: "h-auto", Zone: "us-central1-b",
			Status: host.StatusRunning, GPUCount: 1, MaxDevices: 4, HostOrchestratorPort: 2080,
		},
		// Unrelated zone on both axes.
		{
			ID: "h-other", Name: "h-other", Zone: "asia-east1-a",
			Status: host.StatusRunning, GPUCount: 1, MaxDevices: 4, HostOrchestratorPort: 2080,
		},
	}
	for _, h := range hosts {
		if err := repo.Create(ctx, h); err != nil {
			t.Fatalf("Failed to create host %s: %v", h.ID, err)
		}
	}

	token := "us-central1-b"
	result, err := repo.List(ctx, host.ListFilter{RequestedOrPhysicalZone: &token})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	got := map[string]bool{}
	for _, h := range result {
		got[h.ID] = true
	}
	if len(got) != 2 || !got["h-moved"] || !got["h-auto"] {
		t.Errorf("listed = %v, want exactly h-moved (by token) and h-auto (by physical zone)", got)
	}

	count, err := repo.Count(ctx, host.ListFilter{RequestedOrPhysicalZone: &token})
	if err != nil {
		t.Fatalf("Count failed: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

// TestHostRepository_ListByCreator tests the created_by filter behind the
// mine/created_by list params: List and Count scope to one creator's rows.
func TestHostRepository_ListByCreator(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	repo := hostfeature.NewHostRepository(db.Client)

	hosts := []*host.Host{
		{ID: "host-own-1", Name: "own-1", Zone: "us-central1-a", Status: host.StatusRunning, MaxDevices: 4, HostOrchestratorPort: 2080, CreatedBy: new("alice@example.com")},
		{ID: "host-own-2", Name: "own-2", Zone: "us-central1-a", Status: host.StatusStopped, MaxDevices: 4, HostOrchestratorPort: 2080, CreatedBy: new("alice@example.com")},
		{ID: "host-foreign", Name: "foreign", Zone: "us-central1-b", Status: host.StatusRunning, MaxDevices: 4, HostOrchestratorPort: 2080, CreatedBy: new("bob@example.com")},
		{ID: "host-anon", Name: "anon", Zone: "us-central1-b", Status: host.StatusRunning, MaxDevices: 4, HostOrchestratorPort: 2080}, // NULL created_by: never matches a creator filter
	}
	for _, h := range hosts {
		if err := repo.Create(ctx, h); err != nil {
			t.Fatalf("create %s: %v", h.ID, err)
		}
	}

	result, err := repo.List(ctx, host.ListFilter{CreatedBy: new("alice@example.com")})
	if err != nil {
		t.Fatalf("List(CreatedBy): %v", err)
	}
	if len(result) != 2 {
		t.Errorf("List(CreatedBy=alice) returned %d hosts, want 2", len(result))
	}
	for _, h := range result {
		if h.CreatedBy == nil || *h.CreatedBy != "alice@example.com" {
			t.Errorf("host %s created_by = %v, want alice@example.com", h.ID, h.CreatedBy)
		}
	}

	count, err := repo.Count(ctx, host.ListFilter{CreatedBy: new("alice@example.com")})
	if err != nil {
		t.Fatalf("Count(CreatedBy): %v", err)
	}
	if count != 2 {
		t.Errorf("Count(CreatedBy=alice) = %d, want 2", count)
	}
}

// TestHostRepository_FindAvailable tests finding available hosts.
func TestHostRepository_FindAvailable(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	repo := hostfeature.NewHostRepository(db.Client)

	// Create test hosts with different capacity.
	hosts := []*host.Host{
		{ID: "host-avail-1", Name: "avail-1", Zone: "us-central1-a", Status: host.StatusRunning, GPUCount: 1, MaxDevices: 4, CurrentDeviceCount: 2, HostOrchestratorPort: 2080},
		{ID: "host-avail-2", Name: "avail-2", Zone: "us-central1-a", Status: host.StatusRunning, GPUCount: 1, MaxDevices: 4, CurrentDeviceCount: 3, HostOrchestratorPort: 2080},
		{ID: "host-avail-3", Name: "avail-3", Zone: "us-central1-a", Status: host.StatusRunning, GPUCount: 1, MaxDevices: 4, CurrentDeviceCount: 4, HostOrchestratorPort: 2080}, // Full
		{ID: "host-avail-4", Name: "avail-4", Zone: "us-central1-b", Status: host.StatusRunning, GPUCount: 1, MaxDevices: 4, CurrentDeviceCount: 0, HostOrchestratorPort: 2080}, // Different zone
	}

	for _, h := range hosts {
		if err := repo.Create(ctx, h); err != nil {
			t.Fatalf("Create failed: %v", err)
		}
	}

	t.Run("FindWithCapacity", func(t *testing.T) {
		result, err := repo.FindAvailableHosts(ctx, "us-central1-a", 1)
		if err != nil {
			t.Fatalf("FindAvailableHosts failed: %v", err)
		}
		if len(result) != 2 {
			t.Errorf("Expected 2 available hosts, got %d", len(result))
		}

		// Should be ordered by available capacity (most first).
		if result[0].ID != "host-avail-1" {
			t.Errorf("Expected host-avail-1 first (2 slots), got %s", result[0].ID)
		}
	})

	t.Run("FindWithMultipleSlots", func(t *testing.T) {
		result, err := repo.FindAvailableHosts(ctx, "us-central1-a", 2)
		if err != nil {
			t.Fatalf("FindAvailableHosts failed: %v", err)
		}
		if len(result) != 1 {
			t.Errorf("Expected 1 host with 2+ slots, got %d", len(result))
		}
	})

	t.Run("NoAvailableHosts", func(t *testing.T) {
		result, err := repo.FindAvailableHosts(ctx, "us-east1-b", 1)
		if err != nil {
			t.Fatalf("FindAvailableHosts failed: %v", err)
		}
		if len(result) != 0 {
			t.Errorf("Expected 0 hosts in non-existent zone, got %d", len(result))
		}
	})
}

// TestHostRepository_CountOperations tests counting operations.
func TestHostRepository_CountOperations(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	repo := hostfeature.NewHostRepository(db.Client)

	// Create test hosts.
	hosts := []*host.Host{
		{ID: "cnt-h1", Name: "cnt-1", Zone: "us-central1-a", Status: host.StatusRunning, GPUCount: 1, MaxDevices: 4, HostOrchestratorPort: 2080},
		{ID: "cnt-h2", Name: "cnt-2", Zone: "us-central1-a", Status: host.StatusRunning, GPUCount: 1, MaxDevices: 4, HostOrchestratorPort: 2080},
		{ID: "cnt-h3", Name: "cnt-3", Zone: "us-central1-b", Status: host.StatusStopped, GPUCount: 1, MaxDevices: 4, HostOrchestratorPort: 2080},
	}
	for _, h := range hosts {
		if err := repo.Create(ctx, h); err != nil {
			t.Fatalf("Create failed: %v", err)
		}
	}

	t.Run("Count", func(t *testing.T) {
		zone := "us-central1-a"
		count, err := repo.Count(ctx, host.ListFilter{Zone: &zone})
		if err != nil {
			t.Fatalf("Count failed: %v", err)
		}
		if count != 2 {
			t.Errorf("Expected 2 hosts in us-central1-a, got %d", count)
		}
	})

	t.Run("CountByStatus", func(t *testing.T) {
		counts, err := repo.CountByStatus(ctx)
		if err != nil {
			t.Fatalf("CountByStatus failed: %v", err)
		}
		if counts[host.StatusRunning] != 2 {
			t.Errorf("Expected 2 running hosts, got %d", counts[host.StatusRunning])
		}
		if counts[host.StatusStopped] != 1 {
			t.Errorf("Expected 1 stopped host, got %d", counts[host.StatusStopped])
		}
	})
}

// MarkHostLost must mark the host deleted and fail over its live devices in
// the same commit; terminal/mid-teardown devices are untouched, and a second
// call is a no-op.
func TestHostRepository_MarkHostLost(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)

	h := &host.Host{
		ID: "lost-host", Name: "lost", Zone: "us-central1-a",
		Status: host.StatusRunning, MaxDevices: 4, HostOrchestratorPort: 2080,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := repo.Create(ctx, h); err != nil {
		t.Fatalf("Create host: %v", err)
	}
	now := time.Now()
	seedDevice := func(id, status string) *spanner.Mutation {
		return spanner.InsertMap("devices", map[string]any{
			"host_id": "lost-host", "device_id": id, "name": id,
			"status": status, "created_at": now, "updated_at": now,
		})
	}
	if _, err := db.Client.Apply(ctx, []*spanner.Mutation{
		seedDevice("dev-running", "running"),
		seedDevice("dev-pending", "pending"),
		seedDevice("dev-deleted", "deleted"),
	}); err != nil {
		t.Fatalf("seeding devices: %v", err)
	}

	if err := repo.MarkHostLost(ctx, "lost-host", "host VM deleted outside CCO"); err != nil {
		t.Fatalf("MarkHostLost: %v", err)
	}

	got, err := repo.Get(ctx, "lost-host")
	if err != nil {
		t.Fatalf("Get host: %v", err)
	}
	if got.Status != host.StatusDeleted || got.ErrorMessage != "host VM deleted outside CCO" {
		t.Errorf("host = %q/%q, want deleted with reason", got.Status, got.ErrorMessage)
	}

	assertDeviceStatus := func(id, want string) {
		t.Helper()
		row, err := db.Client.Single().ReadRow(ctx, "devices",
			spanner.Key{"lost-host", id}, []string{"status", "error_message"})
		if err != nil {
			t.Fatalf("reading device %s: %v", id, err)
		}
		var status string
		var msg spanner.NullString
		if err := row.Columns(&status, &msg); err != nil {
			t.Fatalf("scanning device %s: %v", id, err)
		}
		if status != want {
			t.Errorf("device %s status = %q, want %q", id, status, want)
		}
	}
	assertDeviceStatus("dev-running", "error")
	assertDeviceStatus("dev-pending", "error")
	assertDeviceStatus("dev-deleted", "deleted")

	// Idempotent: a second call on the now-deleted host is a no-op.
	if err := repo.MarkHostLost(ctx, "lost-host", "again"); err != nil {
		t.Fatalf("second MarkHostLost: %v", err)
	}

	// The mark is durable budget evidence: CountLostMarksSince sees it by
	// reason and window, and neither a different reason nor a window that
	// starts after the mark counts it.
	for _, tc := range []struct {
		name   string
		since  time.Time
		reason string
		want   int64
	}{
		{"matching reason inside window", now.Add(-time.Minute), "host VM deleted outside CCO", 1},
		{"foreign reason", now.Add(-time.Minute), "some other reason", 0},
		{"window after the mark", time.Now().Add(time.Minute), "host VM deleted outside CCO", 0},
	} {
		count, err := repo.CountLostMarksSince(ctx, tc.since, tc.reason)
		if err != nil {
			t.Fatalf("CountLostMarksSince (%s): %v", tc.name, err)
		}
		if count != tc.want {
			t.Errorf("CountLostMarksSince (%s) = %d, want %d", tc.name, count, tc.want)
		}
	}
}

// TestHostRepository_ListEmptyBatchHosts exercises the drained-batch-host
// query: batch linkage, the live-device NOT EXISTS (error/deleted rows do not
// block), the creation-grace cutoff, and status fencing.
func TestHostRepository_ListEmptyBatchHosts(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)

	old := time.Now().Add(-time.Hour)
	seed := func(id, status, batchID string, createdAt time.Time) {
		t.Helper()
		row := map[string]any{
			"host_id": id, "name": id, "zone": "eu-west4-a", "status": status,
			"gpu_count": int64(0), "host_orchestrator_port": int64(2080),
			"max_devices": int64(4), "current_device_count": int64(0),
			"created_at": createdAt, "updated_at": createdAt,
		}
		if batchID != "" {
			row["batch_id"] = batchID
		}
		if _, err := db.Client.Apply(ctx, []*spanner.Mutation{spanner.InsertMap("hosts", row)}); err != nil {
			t.Fatalf("seed host %s: %v", id, err)
		}
	}
	seedDevice := func(hostID, deviceID, status string) {
		t.Helper()
		mut := spanner.InsertMap("devices", map[string]any{
			"host_id": hostID, "device_id": deviceID, "name": deviceID,
			"status": status, "created_at": time.Now(), "updated_at": time.Now(),
		})
		if _, err := db.Client.Apply(ctx, []*spanner.Mutation{mut}); err != nil {
			t.Fatalf("seed device %s: %v", deviceID, err)
		}
	}

	seed("bh-drained", "running", "batch-a", old)
	seed("bh-dead-tenants", "running", "batch-a", old) // only error+deleted rows: reapable
	seed("bh-live-tenant", "running", "batch-a", old)  // live device: kept
	seed("bh-fresh", "running", "batch-a", time.Now()) // inside grace: kept
	seed("bh-deleting", "deleting", "batch-a", old)    // already tearing down: kept out
	seed("bh-user-host", "running", "", old)           // not batch-provisioned: never
	seedDevice("bh-dead-tenants", "dead-1", "error")
	seedDevice("bh-dead-tenants", "dead-2", "deleted")
	seedDevice("bh-live-tenant", "alive-1", "running")

	got, err := repo.ListEmptyBatchHosts(ctx, time.Now().Add(-30*time.Minute), 10)
	if err != nil {
		t.Fatalf("ListEmptyBatchHosts: %v", err)
	}
	ids := make([]string, 0, len(got))
	for _, h := range got {
		ids = append(ids, h.ID)
	}
	want := []string{"bh-dead-tenants", "bh-drained"}
	slices.Sort(ids)
	if !slices.Equal(ids, want) {
		t.Errorf("ListEmptyBatchHosts = %v, want %v", ids, want)
	}
}

// TestHostRepository_ListOrderIsTotal pins the order a paging client depends
// on: with created_at tied (hosts provisioned together share a timestamp)
// host_id decides, so walking the list one row at a time visits each host
// exactly once instead of leaving the order to the scan.
func TestHostRepository_ListOrderIsTotal(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	repo := hostfeature.NewHostRepository(db.Client)
	ids := []string{"page-host-1", "page-host-2"}
	for _, id := range ids {
		h := &host.Host{ID: id, Name: id, Zone: "us-central1-a", MachineType: "n1-standard-8", Status: host.StatusRunning, MaxDevices: 4}
		if err := repo.Create(ctx, h); err != nil {
			t.Fatalf("Create(%s): %v", id, err)
		}
	}

	// Create stamps its own timestamp, so collapse them afterwards: distinct
	// timestamps would decide the order and the tie would never be exercised.
	shared := time.Now().UTC().Truncate(time.Microsecond)
	muts := make([]*spanner.Mutation, 0, len(ids))
	for _, id := range ids {
		muts = append(muts, spanner.UpdateMap("hosts", map[string]any{"host_id": id, "created_at": shared}))
	}
	if _, err := db.Client.Apply(ctx, muts); err != nil {
		t.Fatalf("collapsing created_at: %v", err)
	}

	full, err := repo.List(ctx, host.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	gotFull := make([]string, len(full))
	for i, h := range full {
		gotFull[i] = h.ID
	}
	if want := []string{"page-host-2", "page-host-1"}; !slices.Equal(gotFull, want) {
		t.Errorf("List order under equal created_at = %v, want %v", gotFull, want)
	}

	paged := make([]string, 0, len(ids))
	for offset := range ids {
		page, err := repo.List(ctx, host.ListFilter{Limit: 1, Offset: offset})
		if err != nil {
			t.Fatalf("List(offset=%d): %v", offset, err)
		}
		if len(page) != 1 {
			t.Fatalf("List(offset=%d) returned %d rows, want 1", offset, len(page))
		}
		paged = append(paged, page[0].ID)
	}
	if !slices.Equal(paged, gotFull) {
		t.Errorf("one-row pages walked %v, want %v; offset paging skipped or repeated a row", paged, gotFull)
	}
}

// TestHostRepository_ListKeysetSurvivesDeletion walks the list one row per
// page and deletes the row the first page returned before reading the second.
// An offset walk would then step over the next row. Two rows share a
// created_at, so the walk also needs the host_id tiebreak in the predicate.
func TestHostRepository_ListKeysetSurvivesDeletion(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	repo := hostfeature.NewHostRepository(db.Client)
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	stamps := map[string]time.Time{
		"keyset-host-0": base.Add(-2 * time.Second),
		"keyset-host-1": base.Add(-time.Second),
		"keyset-host-2": base,
		"keyset-host-3": base,
	}
	muts := make([]*spanner.Mutation, 0, len(stamps))
	for id, at := range stamps {
		h := &host.Host{ID: id, Name: id, Zone: "us-central1-a", Status: host.StatusRunning, MaxDevices: 4, HostOrchestratorPort: 2080}
		if err := repo.Create(ctx, h); err != nil {
			t.Fatalf("Create(%s): %v", id, err)
		}
		muts = append(muts, spanner.UpdateMap("hosts", map[string]any{"host_id": id, "created_at": at}))
	}
	if _, err := db.Client.Apply(ctx, muts); err != nil {
		t.Fatalf("setting created_at: %v", err)
	}

	var walked []string
	filter := host.ListFilter{Limit: 1}
	for page := 0; ; page++ {
		if page > 6 {
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
			if _, err := db.Client.Apply(ctx, []*spanner.Mutation{spanner.UpdateMap("hosts", map[string]any{
				"host_id": rows[0].ID, "status": string(host.StatusDeleted),
			})}); err != nil {
				t.Fatalf("deleting %s between pages: %v", rows[0].ID, err)
			}
		}
	}
	if want := []string{"keyset-host-3", "keyset-host-2", "keyset-host-1", "keyset-host-0"}; !slices.Equal(walked, want) {
		t.Errorf("keyset walk = %v, want %v", walked, want)
	}

	total, err := repo.Count(ctx, filter)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if total != 3 {
		t.Errorf("Count with a cursor = %d, want 3 (the cursor must not narrow the total)", total)
	}
}

// TestHostRepository_ListByName checks that the name filter reads only live
// rows: live_name is NULL once a host is deleted, so a deleted host with the
// same name is never a match, and neither is a host deleted after it was
// written.
func TestHostRepository_ListByName(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	repo := hostfeature.NewHostRepository(db.Client)
	for _, h := range []*host.Host{
		{ID: "name-live", Name: "shared-name", Status: host.StatusRunning},
		{ID: "name-gone", Name: "shared-name", Status: host.StatusDeleted},
		{ID: "name-other", Name: "other-name", Status: host.StatusRunning},
	} {
		h.Zone, h.MaxDevices, h.HostOrchestratorPort = "us-central1-a", 4, 2080
		if err := repo.Create(ctx, h); err != nil {
			t.Fatalf("Create(%s): %v", h.ID, err)
		}
	}

	ids := func(filter host.ListFilter) []string {
		t.Helper()
		rows, err := repo.List(ctx, filter)
		if err != nil {
			t.Fatalf("List(%+v): %v", filter, err)
		}
		got := make([]string, len(rows))
		for i, h := range rows {
			got[i] = h.ID
		}
		return got
	}
	name := "shared-name"

	if got := ids(host.ListFilter{Name: &name}); !slices.Equal(got, []string{"name-live"}) {
		t.Errorf("name filter = %v, want [name-live]", got)
	}
	if got := ids(host.ListFilter{Name: &name, IncludeDeleted: true}); !slices.Equal(got, []string{"name-live"}) {
		t.Errorf("name filter with IncludeDeleted = %v, want [name-live]", got)
	}
	deleted := host.StatusDeleted
	if got := ids(host.ListFilter{Name: &name, Status: &deleted}); len(got) != 0 {
		t.Errorf("name filter with status=deleted = %v, want none", got)
	}
	if n, err := repo.Count(ctx, host.ListFilter{Name: &name}); err != nil || n != 1 {
		t.Errorf("Count(name) = %d, %v; want 1", n, err)
	}

	if _, err := db.Client.Apply(ctx, []*spanner.Mutation{spanner.UpdateMap("hosts", map[string]any{
		"host_id": "name-live", "status": string(host.StatusDeleted),
	})}); err != nil {
		t.Fatalf("deleting name-live: %v", err)
	}
	if got := ids(host.ListFilter{Name: &name}); len(got) != 0 {
		t.Errorf("name filter after delete = %v, want none", got)
	}
}

// The reaper's expiry listing and the settle loop's drained-batch-host listing
// hold the salted-sweep contract (see spannertest.AssertShuffledSweep), over a
// fixture that also carries the rows each predicate must keep excluding however
// the order moves.
func TestHostRepository_SweepListingsShuffle(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)

	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	seed := func(id, batchID string, expiresAt time.Time) {
		t.Helper()
		row := map[string]any{
			"host_id": id, "name": id, "zone": "eu-west4-a", "status": "running",
			"gpu_count": int64(0), "host_orchestrator_port": int64(2080),
			"max_devices": int64(4), "current_device_count": int64(0),
			"lifecycle_mode": "ephemeral", "protected_device_count": int64(0),
			"expires_at": expiresAt, "created_at": past, "updated_at": past,
		}
		if batchID != "" {
			row["batch_id"] = batchID
		}
		if _, err := db.Client.Apply(ctx, []*spanner.Mutation{spanner.InsertMap("hosts", row)}); err != nil {
			t.Fatalf("seed host %s: %v", id, err)
		}
	}

	const candidates = 12
	wantExpired := make([]string, 0, candidates)
	wantDrained := make([]string, 0, candidates)
	for i := range candidates {
		expired := fmt.Sprintf("shuffle-expired-%02d", i)
		seed(expired, "", past)
		wantExpired = append(wantExpired, expired)

		drained := fmt.Sprintf("shuffle-drained-%02d", i)
		seed(drained, "batch-shuffle", future)
		wantDrained = append(wantDrained, drained)
	}
	// Live, and not batch-provisioned: outside both candidate sets.
	seed("shuffle-live", "", future)
	// Batch-provisioned but still carrying a tenant.
	seed("shuffle-tenanted", "batch-shuffle", future)
	if _, err := db.Client.Apply(ctx, []*spanner.Mutation{spanner.InsertMap("devices", map[string]any{
		"host_id": "shuffle-tenanted", "device_id": "shuffle-tenant", "name": "shuffle-tenant",
		"status": "running", "created_at": time.Now(), "updated_at": time.Now(),
	})}); err != nil {
		t.Fatalf("seed tenant device: %v", err)
	}

	sweeps := []struct {
		name string
		list func(int) ([]*host.Host, error)
		want []string
	}{
		{"ListExpired", func(limit int) ([]*host.Host, error) { return repo.ListExpired(ctx, limit) }, wantExpired},
		{"ListEmptyBatchHosts", func(limit int) ([]*host.Host, error) {
			return repo.ListEmptyBatchHosts(ctx, time.Now(), limit)
		}, wantDrained},
	}
	for _, sw := range sweeps {
		t.Run(sw.name, func(t *testing.T) {
			spannertest.AssertShuffledSweep(t, sw.name, sw.want, func(limit int) ([]string, error) {
				got, err := sw.list(limit)
				if err != nil {
					return nil, err
				}
				ids := make([]string, 0, len(got))
				for _, h := range got {
					ids = append(ids, h.ID)
				}
				return ids, nil
			})
		})
	}
}
