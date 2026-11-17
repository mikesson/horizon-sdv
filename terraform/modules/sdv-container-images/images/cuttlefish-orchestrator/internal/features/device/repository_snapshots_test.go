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
	"context"
	"errors"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	devicefeature "cloud-cuttlefish-orchestrator/internal/features/device"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// insertSnapshot applies the repository's insert mutation directly. In
// production the same mutation rides the take verb's admission transaction.
func insertSnapshot(t *testing.T, db *spannertest.TestDB, repo *devicefeature.DeviceRepository, s *device.Snapshot) {
	t.Helper()
	m, err := repo.SnapshotInsert(s)
	if err != nil {
		t.Fatalf("SnapshotInsert: %v", err)
	}
	if _, err := db.Client.Apply(t.Context(), []*spanner.Mutation{m}); err != nil {
		t.Fatalf("apply snapshot insert: %v", err)
	}
}

func newTestSnapshot(id, deviceID, hostID string, createdAt time.Time) *device.Snapshot {
	return &device.Snapshot{
		ID:              id,
		DeviceID:        deviceID,
		HostID:          hostID,
		CVDGroup:        "cvd" + deviceID[:min(8, len(deviceID))],
		CVDName:         "cvd-1",
		HostIncarnation: 1,
		State:           device.SnapshotStateCreating,
		CreatedBy:       "tester@example.com",
		CreatedAt:       createdAt,
		UpdatedAt:       createdAt,
	}
}

func seedSnapshotFixture(ctx context.Context, t *testing.T, db *spannertest.TestDB, hostID, deviceID string) *devicefeature.DeviceRepository {
	t.Helper()
	if err := db.SeedHost(ctx, hostID, "snap-host", "europe-west4-a"); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)
	d := &device.Device{
		ID: deviceID, HostID: hostID, Name: "snap-device",
		Status: device.StatusRunning, GPUMode: device.GPUModeAuto,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := repo.Create(ctx, d); err != nil {
		t.Fatalf("create device: %v", err)
	}
	return repo
}

func TestSnapshotRepository_RoundTrip(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := seedSnapshotFixture(ctx, t, db, "snap-host-001", "snap-device-001")

	now := time.Now().UTC().Truncate(time.Microsecond)
	s := newTestSnapshot("snap-0000-0001", "snap-device-001", "snap-host-001", now)
	insertSnapshot(t, db, repo, s)

	got, err := repo.GetSnapshot(ctx, "snap-device-001", "snap-0000-0001")
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	// Field by field. The row model maps every column by hand, so a column
	// dropped from the mapping comes back as a zero value here.
	if got.ID != s.ID || got.DeviceID != s.DeviceID || got.HostID != s.HostID {
		t.Errorf("identity = (%s, %s, %s), want (%s, %s, %s)",
			got.ID, got.DeviceID, got.HostID, s.ID, s.DeviceID, s.HostID)
	}
	if got.CVDGroup != s.CVDGroup || got.CVDName != s.CVDName {
		t.Errorf("cvd addressing = (%s, %s), want (%s, %s)", got.CVDGroup, got.CVDName, s.CVDGroup, s.CVDName)
	}
	if got.HostIncarnation != 1 {
		t.Errorf("host incarnation = %d, want 1", got.HostIncarnation)
	}
	if got.State != device.SnapshotStateCreating {
		t.Errorf("state = %q, want creating", got.State)
	}
	if got.CreatedBy != "tester@example.com" {
		t.Errorf("created_by = %q, want tester@example.com", got.CreatedBy)
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("created_at = %v, want %v", got.CreatedAt, now)
	}

	if err := repo.MarkSnapshotReady(ctx, "snap-host-001", "snap-0000-0001"); err != nil {
		t.Fatalf("MarkSnapshotReady: %v", err)
	}
	got, err = repo.GetSnapshot(ctx, "snap-device-001", "snap-0000-0001")
	if err != nil {
		t.Fatalf("GetSnapshot after ready: %v", err)
	}
	if got.State != device.SnapshotStateReady {
		t.Errorf("state after MarkSnapshotReady = %q, want ready", got.State)
	}
	if !got.UpdatedAt.After(now) {
		t.Errorf("updated_at %v did not advance past %v", got.UpdatedAt, now)
	}
}

func TestSnapshotRepository_GetUnknownIsNotFound(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := devicefeature.NewDeviceRepository(db.Client)

	_, err := repo.GetSnapshot(t.Context(), "no-such-device", "no-such-snap")
	var nf *device.SnapshotNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("GetSnapshot(unknown) = %v, want *SnapshotNotFoundError", err)
	}
	if nf.ID != "no-such-snap" {
		t.Errorf("error carries id %q, want no-such-snap", nf.ID)
	}
}

func TestSnapshotRepository_ListNewestFirstAndCount(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := seedSnapshotFixture(ctx, t, db, "snap-host-002", "snap-device-002")

	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	for i, id := range []string{"snap-old", "snap-mid", "snap-new"} {
		s := newTestSnapshot(id, "snap-device-002", "snap-host-002", base.Add(time.Duration(i)*time.Minute))
		insertSnapshot(t, db, repo, s)
	}

	snaps, err := repo.ListSnapshotsByDevice(ctx, "snap-device-002")
	if err != nil {
		t.Fatalf("ListSnapshotsByDevice: %v", err)
	}
	if len(snaps) != 3 {
		t.Fatalf("list returned %d snapshots, want 3", len(snaps))
	}
	if snaps[0].ID != "snap-new" || snaps[2].ID != "snap-old" {
		t.Errorf("order = [%s %s %s], want newest first", snaps[0].ID, snaps[1].ID, snaps[2].ID)
	}

	n, err := repo.CountLiveSnapshotsByDevice(ctx, "snap-device-002")
	if err != nil {
		t.Fatalf("CountLiveSnapshotsByDevice: %v", err)
	}
	if n != 3 {
		t.Errorf("live count = %d, want 3", n)
	}

	// A lost snapshot occupies nothing. It must fall out of the count that
	// gates new takes, or a lost host would permanently pin its
	// devices at the cap.
	if _, err := db.Client.Apply(ctx, []*spanner.Mutation{
		spanner.Update("snapshots",
			[]string{"host_id", "snapshot_id", "state", "lost_at", "updated_at"},
			[]any{"snap-host-002", "snap-mid", string(device.SnapshotStateLost), time.Now(), time.Now()}),
	}); err != nil {
		t.Fatalf("mark lost: %v", err)
	}
	n, err = repo.CountLiveSnapshotsByDevice(ctx, "snap-device-002")
	if err != nil {
		t.Fatalf("CountLiveSnapshotsByDevice after lost: %v", err)
	}
	if n != 2 {
		t.Errorf("live count after lost = %d, want 2", n)
	}
}

func TestSnapshotRepository_DeleteIsIdempotent(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := seedSnapshotFixture(ctx, t, db, "snap-host-003", "snap-device-003")

	s := newTestSnapshot("snap-del", "snap-device-003", "snap-host-003", time.Now().UTC())
	insertSnapshot(t, db, repo, s)

	if err := repo.DeleteSnapshotRow(ctx, "snap-host-003", "snap-del"); err != nil {
		t.Fatalf("DeleteSnapshotRow: %v", err)
	}
	if _, err := repo.GetSnapshot(ctx, "snap-device-003", "snap-del"); err == nil {
		t.Fatal("snapshot still readable after delete")
	}
	// The verb's redelivery budget assumes the second delete succeeds.
	if err := repo.DeleteSnapshotRow(ctx, "snap-host-003", "snap-del"); err != nil {
		t.Fatalf("second DeleteSnapshotRow: %v", err)
	}
}

// Deleting a device removes its snapshot rows in the same commit as the
// deleted mark. A tombstoned device row must not anchor dead inventory for
// its seven-day retention.
func TestSnapshotRepository_DeviceDeleteCascades(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := seedSnapshotFixture(ctx, t, db, "snap-host-006", "snap-device-006")

	now := time.Now().UTC()
	insertSnapshot(t, db, repo, newTestSnapshot("snap-x", "snap-device-006", "snap-host-006", now))
	insertSnapshot(t, db, repo, newTestSnapshot("snap-y", "snap-device-006", "snap-host-006", now.Add(time.Second)))

	transitioned, err := repo.MarkDeletedWithHostDecrement(ctx, "snap-host-006", "snap-device-006")
	if err != nil {
		t.Fatalf("MarkDeletedWithHostDecrement: %v", err)
	}
	if !transitioned {
		t.Fatal("delete did not transition the device")
	}
	left, err := repo.ListSnapshotsByDevice(ctx, "snap-device-006")
	if err != nil {
		t.Fatalf("ListSnapshotsByDevice: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("%d snapshot rows survived the device delete", len(left))
	}
}

// The interleave is the cleanup path for real host deletion. The rows must
// vanish with the parent, or deleted hosts would leak inventory forever.
func TestSnapshotRepository_HostRowDeleteCascades(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := seedSnapshotFixture(ctx, t, db, "snap-host-005", "snap-device-005")

	insertSnapshot(t, db, repo, newTestSnapshot("snap-cascade", "snap-device-005", "snap-host-005", time.Now().UTC()))

	if _, err := db.Client.Apply(ctx, []*spanner.Mutation{
		spanner.Delete("hosts", spanner.Key{"snap-host-005"}),
	}); err != nil {
		t.Fatalf("delete host row: %v", err)
	}

	snaps, err := repo.ListSnapshotsByDevice(ctx, "snap-device-005")
	if err != nil {
		t.Fatalf("ListSnapshotsByDevice: %v", err)
	}
	if len(snaps) != 0 {
		t.Errorf("%d snapshot rows survived host row deletion", len(snaps))
	}
}
