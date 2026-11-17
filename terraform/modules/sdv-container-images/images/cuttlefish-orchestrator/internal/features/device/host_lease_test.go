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

package devicefeature

import (
	"errors"
	"log/slog"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// leaseFixture is one ephemeral host on the emulator with a device service
// over the real device repository, so pin, unpin and extend commit through
// the production transactions and host-counter writes.
type leaseFixture struct {
	db   *spannertest.TestDB
	repo *DeviceRepository
	svc  *DeviceService
}

const leaseHostID = "lease-host"

// newLeaseFixture seeds an ephemeral host whose lease ends in an hour and
// whose VM stops at terminates (nil leaves vm_terminates_at NULL).
func newLeaseFixture(t *testing.T, terminates *time.Time) *leaseFixture {
	t.Helper()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedHost(ctx, leaseHostID, leaseHostID, "us-central1-a"); err != nil {
		t.Fatal(err)
	}
	var vmStop spanner.NullTime
	if terminates != nil {
		vmStop = spanner.NullTime{Time: *terminates, Valid: true}
	}
	if _, err := db.Client.Apply(ctx, []*spanner.Mutation{spanner.Update("hosts",
		[]string{"host_id", "lifecycle_mode", "ttl_seconds", "expires_at", "protected_device_count", "vm_terminates_at"},
		[]any{leaseHostID, string(host.LifecycleModeEphemeral), int64(3600), time.Now().Add(time.Hour), int64(0), vmStop},
	)}); err != nil {
		t.Fatalf("setting host lease: %v", err)
	}
	repo := NewDeviceRepository(db.Client)
	svc, err := NewDeviceService(repo, hosttest.NewFakeRepo(), newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithTaskEnqueuer(&fakeEnqueuer{}))
	if err != nil {
		t.Fatalf("building service: %v", err)
	}
	return &leaseFixture{db: db, repo: repo, svc: svc}
}

func (f *leaseFixture) createDevice(t *testing.T, id string, ttl time.Duration) *device.Device {
	t.Helper()
	d := &device.Device{ID: id, HostID: leaseHostID, Name: id, Status: device.StatusRunning}
	d.Lifecycle = device.NewEphemeralLifecycle(int64(ttl / time.Second))
	if err := f.repo.CreateWithHostIncrement(t.Context(), d); err != nil {
		t.Fatalf("creating device %s: %v", id, err)
	}
	return d
}

func (f *leaseFixture) protectedCount(t *testing.T) int64 {
	t.Helper()
	row, err := f.db.Client.Single().ReadRow(t.Context(), "hosts", spanner.Key{leaseHostID}, []string{"protected_device_count"})
	if err != nil {
		t.Fatalf("reading host: %v", err)
	}
	var n spanner.NullInt64
	if err := row.Columns(&n); err != nil {
		t.Fatalf("scanning host: %v", err)
	}
	return n.Int64
}

// A re-pin replaces pinned_until and keeps pinned_at, and the host's protected
// count does not move because the pin state did not flip. Before the upsert a
// second pin was refused outright.
func TestRepinMovesPinnedUntilAndKeepsProtectedCount(t *testing.T) {
	t.Parallel()
	f := newLeaseFixture(t, nil)
	ctx := t.Context()
	f.createDevice(t, "d1", time.Hour)

	first := time.Now().Add(2 * time.Hour)
	if _, err := f.svc.Pin(ctx, "d1", device.PinRequest{Reason: device.PinReasonOvernightTest, PinnedUntil: &first}, "a@example.com"); err != nil {
		t.Fatalf("first pin: %v", err)
	}
	// Read back so both pinned_at values carry the column's precision.
	pinned, err := f.repo.Get(ctx, "d1")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.protectedCount(t); got != 1 {
		t.Fatalf("protected count after pin = %d, want 1", got)
	}

	later := time.Now().Add(48 * time.Hour)
	repinned, err := f.svc.Pin(ctx, "d1", device.PinRequest{Reason: device.PinReasonLongRunningCI, PinnedUntil: &later}, "b@example.com")
	if err != nil {
		t.Fatalf("re-pin refused: %v", err)
	}
	if repinned.Lifecycle.PinnedUntil == nil || !repinned.Lifecycle.PinnedUntil.Equal(later) {
		t.Errorf("pinned_until = %v, want %v", repinned.Lifecycle.PinnedUntil, later)
	}
	if *repinned.Lifecycle.PinnedReason != device.PinReasonLongRunningCI {
		t.Errorf("pinned_reason = %v, want the re-pin's reason", *repinned.Lifecycle.PinnedReason)
	}
	if !repinned.Lifecycle.PinnedAt.Equal(*pinned.Lifecycle.PinnedAt) {
		t.Errorf("pinned_at moved from %v to %v on a re-pin", pinned.Lifecycle.PinnedAt, repinned.Lifecycle.PinnedAt)
	}
	if got := f.protectedCount(t); got != 1 {
		t.Errorf("protected count after re-pin = %d, want 1: a re-pin must not protect the host twice", got)
	}
}

// A pin that lapsed and was re-pinned before the reaper's unpin committed must
// stay pinned. Drop the pinned_until re-check in UnpinLapsed and the stale
// listing undoes the re-pin.
func TestUnpinLapsedRefusesARepinnedDevice(t *testing.T) {
	t.Parallel()
	f := newLeaseFixture(t, nil)
	ctx := t.Context()
	f.createDevice(t, "d1", time.Hour)

	soon := time.Now().Add(time.Second)
	if _, err := f.svc.Pin(ctx, "d1", device.PinRequest{Reason: device.PinReasonOvernightTest, PinnedUntil: &soon}, "a@example.com"); err != nil {
		t.Fatalf("pin: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	lapsed, err := f.repo.ListLapsedPins(ctx, 10)
	if err != nil || len(lapsed) != 1 {
		t.Fatalf("ListLapsedPins = %v, %v; want the one lapsed pin", lapsed, err)
	}

	later := time.Now().Add(24 * time.Hour)
	if _, err := f.svc.Pin(ctx, "d1", device.PinRequest{Reason: device.PinReasonOvernightTest, PinnedUntil: &later}, "a@example.com"); err != nil {
		t.Fatalf("re-pin: %v", err)
	}

	_, err = f.svc.UnpinLapsed(ctx, "d1", "system:reaper")
	if !errors.Is(err, device.ErrInvalidState) {
		t.Fatalf("UnpinLapsed on a re-pinned device = %v, want ErrInvalidState", err)
	}
	got, err := f.repo.Get(ctx, "d1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Lifecycle.IsPinned() || got.Lifecycle.PinnedUntil == nil || got.Lifecycle.PinnedUntil.Sub(later).Abs() > time.Millisecond {
		t.Errorf("device after the refused unpin: pinned=%v until=%v, want still pinned until %v",
			got.Lifecycle.IsPinned(), got.Lifecycle.PinnedUntil, later)
	}
	if n := f.protectedCount(t); n != 1 {
		t.Errorf("protected count = %d, want 1", n)
	}

	// A pin that has lapsed and was not re-pinned still unpins.
	if _, err := f.svc.Pin(ctx, "d1", device.PinRequest{Reason: device.PinReasonOvernightTest, PinnedUntil: new(time.Now().Add(time.Second))}, "a@example.com"); err != nil {
		t.Fatalf("short re-pin: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := f.svc.UnpinLapsed(ctx, "d1", "system:reaper"); err != nil {
		t.Fatalf("UnpinLapsed on a lapsed pin: %v", err)
	}
}

// A device extend whose new expiry plus the grace passes the VM's stop time is
// refused with a 409-class error and leaves the device and host rows as they
// were. Remove the horizon check and the extend commits a lease GCE would cut.
func TestDeviceExtendPastVMHorizonIsRefused(t *testing.T) {
	t.Parallel()
	terminates := time.Now().Add(3 * time.Hour)
	f := newLeaseFixture(t, &terminates)
	ctx := t.Context()
	before := f.createDevice(t, "d1", time.Hour)

	_, err := f.svc.Extend(ctx, "d1", device.ExtendRequest{Seconds: int64((4 * time.Hour) / time.Second)})
	if _, ok := errors.AsType[*host.VMHorizonError](err); !ok || !errors.Is(err, host.ErrInvalidState) {
		t.Fatalf("extend past the VM horizon = %v, want a *host.VMHorizonError matching ErrInvalidState", err)
	}
	got, err := f.repo.Get(ctx, "d1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Lifecycle.ExpiresAt.Sub(*before.Lifecycle.ExpiresAt).Abs() > time.Millisecond || got.Lifecycle.ExtendCount != 0 {
		t.Errorf("device after the refused extend: expires_at=%v extend_count=%d, want %v and 0",
			got.Lifecycle.ExpiresAt, got.Lifecycle.ExtendCount, before.Lifecycle.ExpiresAt)
	}

	// An extend that fits under the horizon still commits.
	if _, err := f.svc.Extend(ctx, "d1", device.ExtendRequest{Seconds: int64(time.Hour / time.Second)}); err != nil {
		t.Errorf("extend inside the horizon refused: %v", err)
	}
}

// A device create whose expiry plus grace passes the VM's stop time is refused
// before anything commits. A NULL vm_terminates_at (VM not yet observed) passes.
func TestDeviceCreatePastVMHorizonIsRefused(t *testing.T) {
	t.Parallel()
	terminates := time.Now().Add(30 * time.Minute)
	f := newLeaseFixture(t, &terminates)
	ctx := t.Context()

	d := &device.Device{ID: "d1", HostID: leaseHostID, Name: "d1", Status: device.StatusPending}
	d.Lifecycle = device.NewEphemeralLifecycle(3600)
	err := f.repo.CreateWithHostIncrement(ctx, d)
	if _, ok := errors.AsType[*host.VMHorizonError](err); !ok {
		t.Fatalf("create past the VM horizon = %v, want *host.VMHorizonError", err)
	}
	if _, err := f.repo.Get(ctx, "d1"); !errors.Is(err, device.ErrNotFound) {
		t.Errorf("refused create left a row behind: %v", err)
	}

	open := newLeaseFixture(t, nil)
	open.createDevice(t, "d2", time.Hour)
}

// The heal back to live raises the host lease without the horizon check: it
// restores time the user was already granted.
func TestHealToLiveBypassesVMHorizon(t *testing.T) {
	t.Parallel()
	f := newLeaseFixture(t, nil)
	ctx := t.Context()
	f.createDevice(t, "d1", 4*time.Hour)
	if err := f.repo.UpdateStatus(ctx, "d1", device.StatusError, "lost"); err != nil {
		t.Fatalf("to error: %v", err)
	}
	// The VM is now due to stop before the device's own expiry.
	if _, err := f.db.Client.Apply(ctx, []*spanner.Mutation{spanner.Update("hosts",
		[]string{"host_id", "vm_terminates_at"}, []any{leaseHostID, time.Now().Add(time.Hour)})}); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.UpdateStatus(ctx, "d1", device.StatusRunning, ""); err != nil {
		t.Errorf("heal to running refused by the horizon check: %v", err)
	}
}
