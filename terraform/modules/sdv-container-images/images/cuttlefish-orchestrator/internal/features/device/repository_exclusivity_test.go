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
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	devicefeature "cloud-cuttlefish-orchestrator/internal/features/device"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// seedExclusiveHost seeds a running four-slot host and marks it exclusive.
func seedExclusiveHost(t *testing.T, db *spannertest.TestDB, hostID string) {
	t.Helper()
	if err := db.SeedHost(t.Context(), hostID, hostID, "europe-west4-a"); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	if _, err := db.Client.Apply(t.Context(), []*spanner.Mutation{
		spanner.Update("hosts", []string{"host_id", "exclusive"}, []any{hostID, true}),
	}); err != nil {
		t.Fatalf("marking host %s exclusive: %v", hostID, err)
	}
}

// wantHostCount fails the test unless the host's current_device_count is want.
func wantHostCount(t *testing.T, db *spannertest.TestDB, hostID string, want int64, when string) {
	t.Helper()
	if got := hostDeviceCount(t, db, hostID); got != want {
		t.Fatalf("%s: %s current_device_count = %d, want %d", when, hostID, got, want)
	}
}

// exclusivityDevice is a pending device of the given instance count.
func exclusivityDevice(id, hostID string, instances int) *device.Device {
	return &device.Device{
		ID: id, HostID: hostID, Name: id, Status: device.StatusPending,
		GPUMode: device.GPUModeAuto, Mode: "advanced", InstanceCount: instances,
	}
}

// The exclusivity refusals, read inside the create transaction, and the
// creates they leave alone.
func TestDeviceRepository_CreateEnforcesHostExclusivity(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := devicefeature.NewDeviceRepository(db.Client)
	dev := exclusivityDevice

	refused := func(name string, d *device.Device, want string) {
		t.Helper()
		before := hostDeviceCount(t, db, d.HostID)
		err := repo.CreateWithHostIncrement(ctx, d)
		if !errors.Is(err, host.ErrInvalidState) {
			t.Fatalf("%s: CreateWithHostIncrement = %v, want host.ErrInvalidState", name, err)
		}
		if got := err.Error(); !strings.Contains(got, want) {
			t.Errorf("%s: error %q does not say %q", name, got, want)
		}
		if got := hostDeviceCount(t, db, d.HostID); got != before {
			t.Errorf("%s: refused create charged the host: %d -> %d", name, before, got)
		}
		if _, err := repo.Get(ctx, d.ID); !errors.Is(err, device.ErrNotFound) {
			t.Errorf("%s: refused create left a device row: Get = %v", name, err)
		}
	}
	accepted := func(name string, d *device.Device) {
		t.Helper()
		if err := repo.CreateWithHostIncrement(ctx, d); err != nil {
			t.Fatalf("%s: CreateWithHostIncrement = %v, want success", name, err)
		}
	}

	// A pair on a shared host, empty or not.
	if err := db.SeedHost(ctx, "host-excl-shared", "host-excl-shared", "europe-west4-a"); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	refused("pair on an empty shared host", dev("dev-excl-pair-shared", "host-excl-shared", 2), "needs a host to itself")
	accepted("single on a shared host", dev("dev-excl-single-shared", "host-excl-shared", 1))
	accepted("second single on a shared host", dev("dev-excl-single-shared-2", "host-excl-shared", 1))

	// A pair takes an exclusive host only while it is empty, and keeps it.
	seedExclusiveHost(t, db, "host-excl-pair")
	accepted("pair on an empty exclusive host", dev("dev-excl-pair", "host-excl-pair", 2))
	refused("second pair on the pair's host", dev("dev-excl-pair-2", "host-excl-pair", 2), "needs an empty host")
	refused("single on the pair's host", dev("dev-excl-single-on-pair", "host-excl-pair", 1), "carries a multi-instance device")

	// A failed pair keeps its group on the host as evidence, so it still
	// holds the host.
	if err := repo.UpdateStatus(ctx, "dev-excl-pair", device.StatusError, "boot failed"); err != nil {
		t.Fatalf("failing the pair: %v", err)
	}
	refused("single beside a failed pair", dev("dev-excl-single-on-failed", "host-excl-pair", 1), "carries a multi-instance device")

	// Deleting the pair frees the host. Its tombstone stays until the row
	// deletion policy purges it and must not count.
	if _, err := repo.MarkDeletedWithHostDecrement(ctx, "host-excl-pair", "dev-excl-pair"); err != nil {
		t.Fatalf("deleting the pair: %v", err)
	}
	accepted("single after the pair is deleted", dev("dev-excl-single-after-pair", "host-excl-pair", 1))
	accepted("second single beside the tombstone", dev("dev-excl-single-after-pair-2", "host-excl-pair", 1))

	// A reserved host holding singles stays fillable by host_id, and takes no
	// pair.
	seedExclusiveHost(t, db, "host-excl-reserved")
	accepted("single on a reserved host", dev("dev-excl-reserved-1", "host-excl-reserved", 1))
	accepted("second single on a reserved host", dev("dev-excl-reserved-2", "host-excl-reserved", 1))
	refused("pair on an occupied reserved host", dev("dev-excl-reserved-pair", "host-excl-reserved", 2), "needs an empty host")
	wantHostCount(t, db, "host-excl-reserved", 2, "after the reserved host's creates")
}

// Two pairs racing onto one empty exclusive host: the host-row read and the
// counter write share one serializable transaction, so exactly one commits.
// Under repeatable read both would see the host empty.
func TestDeviceRepository_ConcurrentPairsOnOneExclusiveHost(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := devicefeature.NewDeviceRepository(db.Client)
	const hostID = "host-excl-race"
	seedExclusiveHost(t, db, hostID)

	const racers = 4
	errs := make([]error, racers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range racers {
		wg.Go(func() {
			<-start
			errs[i] = repo.CreateWithHostIncrement(t.Context(),
				exclusivityDevice("dev-excl-race-"+string(rune('a'+i)), hostID, 2))
		})
	}
	close(start)
	wg.Wait()

	var won int
	for i, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, host.ErrInvalidState), errors.Is(err, host.ErrCapacityExceeded):
		default:
			t.Errorf("racer %d: CreateWithHostIncrement = %v, want success or a refusal", i, err)
		}
	}
	if won != 1 {
		t.Fatalf("%d of %d racing pairs committed onto one exclusive host, want exactly 1", won, racers)
	}
	wantHostCount(t, db, hostID, 2, "after the race")
}
