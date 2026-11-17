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
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	devicedomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	devicefeature "cloud-cuttlefish-orchestrator/internal/features/device"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

type hostLifecycleRow struct {
	deviceCount    int64
	protectedCount int64
	mode           string
	expiresAt      spanner.NullTime
}

func readHostLifecycle(t *testing.T, db *spannertest.TestDB, hostID string) hostLifecycleRow {
	t.Helper()
	row, err := db.Client.Single().ReadRow(t.Context(), "hosts", spanner.Key{hostID},
		[]string{"current_device_count", "protected_device_count", "lifecycle_mode", "expires_at"})
	if err != nil {
		t.Fatalf("read host %s: %v", hostID, err)
	}
	var out hostLifecycleRow
	var protected spanner.NullInt64
	var mode spanner.NullString
	if err := row.Columns(&out.deviceCount, &protected, &mode, &out.expiresAt); err != nil {
		t.Fatalf("scan host lifecycle: %v", err)
	}
	out.protectedCount = protected.Int64
	out.mode = mode.StringVal
	return out
}

// TestCreateWithHostIncrementBornPinned pins the create-time protection
// invariant. A device created already pinned protects its host in the same
// commit (protected_device_count incremented, lifecycle_mode protected,
// expires_at cleared), and deleting it releases the protection symmetrically.
// Before the fix the pin flip lived only in Mutate, so a born-pinned device
// left its host ephemeral and the reaper could delete the host under the pin.
func TestCreateWithHostIncrementBornPinned(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	const hostID = "pin-host"
	if err := db.SeedHost(ctx, hostID, "pin-host", "us-central1-a"); err != nil {
		t.Fatal(err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)

	d := &devicedomain.Device{
		ID:     "born-pinned",
		HostID: hostID,
		Name:   "born-pinned",
		Status: devicedomain.StatusPending,
	}
	d.Lifecycle = devicedomain.NewEphemeralLifecycle(3600)
	d.Lifecycle.Pin("tester@example.com", devicedomain.PinReasonDemoPreparation, "", nil)
	if !d.Lifecycle.IsPinned() {
		t.Fatal("fixture is not pinned")
	}

	if err := repo.CreateWithHostIncrement(ctx, d); err != nil {
		t.Fatalf("CreateWithHostIncrement: %v", err)
	}
	got := readHostLifecycle(t, db, hostID)
	if got.deviceCount != 1 || got.protectedCount != 1 {
		t.Errorf("counts = %d devices / %d protected, want 1/1", got.deviceCount, got.protectedCount)
	}
	if got.mode != "protected" {
		t.Errorf("lifecycle_mode = %q, want protected", got.mode)
	}
	if got.expiresAt.Valid {
		t.Errorf("expires_at = %v, want NULL while protected", got.expiresAt.Time)
	}

	// Deleting the pinned device releases the protection. The host goes back
	// to ephemeral with a fresh TTL-based expiry.
	if _, err := repo.MarkDeletedWithHostDecrement(ctx, hostID, d.ID); err != nil {
		t.Fatalf("MarkDeletedWithHostDecrement: %v", err)
	}
	got = readHostLifecycle(t, db, hostID)
	if got.deviceCount != 0 || got.protectedCount != 0 {
		t.Errorf("counts after delete = %d/%d, want 0/0", got.deviceCount, got.protectedCount)
	}
	if got.mode != "ephemeral" || !got.expiresAt.Valid {
		t.Errorf("after delete mode=%q expires_at.Valid=%v, want ephemeral with an expiry", got.mode, got.expiresAt.Valid)
	}
}

// TestCreateWithHostIncrementUnpinnedRaisesLease pins the pre-existing
// behavior the born-pinned fix must leave alone. An unpinned create leaves
// protection untouched and monotonically raises the ephemeral host's expiry
// to cover the device's TTL promise.
func TestCreateWithHostIncrementUnpinnedRaisesLease(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	const hostID = "lease-host"
	if err := db.SeedHost(ctx, hostID, "lease-host", "us-central1-a"); err != nil {
		t.Fatal(err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)

	d := &devicedomain.Device{
		ID:     "plain-device",
		HostID: hostID,
		Name:   "plain-device",
		Status: devicedomain.StatusPending,
	}
	d.Lifecycle = devicedomain.NewEphemeralLifecycle(4 * 3600)
	if err := repo.CreateWithHostIncrement(ctx, d); err != nil {
		t.Fatalf("CreateWithHostIncrement: %v", err)
	}

	got := readHostLifecycle(t, db, hostID)
	if got.protectedCount != 0 || got.mode == "protected" {
		t.Errorf("unpinned create flipped protection: %+v", got)
	}
	if !got.expiresAt.Valid || got.expiresAt.Time.Before(d.Lifecycle.ExpiresAt.Add(time.Minute)) {
		t.Errorf("expires_at = %v, want raised past device promise %v", got.expiresAt, d.Lifecycle.ExpiresAt)
	}
}

// TestListNeedingHealthCheckRunsOnEmulator pins the emulator compatibility of
// the device controller's listing (an explicit NULLS FIRST used to make it error
// on every cycle, leaving the whole loop untested), and the never-checked-
// first ordering that ASC null sorting provides.
func TestListNeedingHealthCheckRunsOnEmulator(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	const hostID = "health-host"
	if err := db.SeedHost(ctx, hostID, "health-host", "us-central1-a"); err != nil {
		t.Fatal(err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)

	for _, id := range []string{"hc-a", "hc-b"} {
		d := &devicedomain.Device{ID: id, HostID: hostID, Name: id, Status: devicedomain.StatusRunning}
		d.Lifecycle = devicedomain.NewEphemeralLifecycle(3600)
		if err := repo.CreateWithHostIncrement(ctx, d); err != nil {
			t.Fatalf("creating %s: %v", id, err)
		}
	}
	// A stopped device is listed too. The host visit is the only place a CVD
	// still running under a stopped row becomes visible.
	stopped := &devicedomain.Device{ID: "hc-stopped", HostID: hostID, Name: "hc-stopped", Status: devicedomain.StatusStopped}
	stopped.Lifecycle = devicedomain.NewEphemeralLifecycle(3600)
	if err := repo.CreateWithHostIncrement(ctx, stopped); err != nil {
		t.Fatalf("creating hc-stopped: %v", err)
	}
	// hc-b was checked recently enough to still be due; hc-a never checked
	// and must sort first (NULL last_health_check).
	if _, err := db.Client.Apply(ctx, []*spanner.Mutation{spanner.Update("devices",
		[]string{"host_id", "device_id", "last_health_check"},
		[]any{hostID, "hc-b", time.Now().Add(-10 * time.Minute)})}); err != nil {
		t.Fatal(err)
	}

	devices, err := repo.ListNeedingHealthCheck(ctx, time.Minute)
	if err != nil {
		t.Fatalf("ListNeedingHealthCheck: %v", err)
	}
	if len(devices) != 3 {
		t.Fatalf("got %d devices, want 3 (running, running, stopped)", len(devices))
	}
	if devices[0].ID != "hc-a" {
		t.Errorf("first device = %s, want never-checked hc-a first", devices[0].ID)
	}
	if !slices.ContainsFunc(devices, func(d *devicedomain.Device) bool { return d.ID == "hc-stopped" }) {
		t.Error("stopped device missing: drift under a stopped row would never be observed")
	}
}
