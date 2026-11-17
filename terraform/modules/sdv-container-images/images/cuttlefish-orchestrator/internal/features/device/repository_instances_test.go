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

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	devicefeature "cloud-cuttlefish-orchestrator/internal/features/device"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// The instance array crosses a real column, and the marshal, the projection and
// the DDL are three separate places it can be lost. The test database is built
// from the authoritative schema file, so this also proves the column exists.
func TestDeviceRepositoryInstancesRoundTrip(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	hostID := "host-instances-001"
	if err := db.SeedHost(ctx, hostID, "instances-host", "europe-west4-a"); err != nil {
		t.Fatalf("seeding host: %v", err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)

	// The slots are non-contiguous on purpose. cvd takes them host-globally,
	// lowest-free-first, so a group landing on a churned host holds exactly this
	// shape. A column round trip that renumbered them would look correct on any
	// 1,2,3 fixture.
	want := []device.Instance{
		{
			Name: "ivi", State: device.CVDStateRunning, WebRTCDeviceID: "cvdgrp-ivi-1", ADBPort: 6521, Slot: 2,
			Displays: []string{"1080 x 600 ( 120 )", "400 x 600 ( 120 )"},
		},
		{Name: "cluster", State: device.CVDStateRunning, WebRTCDeviceID: "cvdgrp-cluster-2", ADBPort: 6524, Slot: 5},
		{Name: "watch", State: device.CVDStateBootFailed, WebRTCDeviceID: "cvdgrp-watch-3", ADBPort: 6527, Slot: 8},
	}

	err := repo.Create(ctx, &device.Device{
		ID: "dev-instances-001", HostID: hostID, Name: "car-rig",
		Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream,
		CVDID: "cvdgrp/ivi", Mode: "advanced", InstanceCount: len(want),
		Instances: want,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("creating device: %v", err)
	}

	got, err := repo.Get(ctx, "dev-instances-001")
	if err != nil {
		t.Fatalf("reading device back: %v", err)
	}
	if len(got.Instances) != len(want) {
		t.Fatalf("read back %d instances, want %d: %+v", len(got.Instances), len(want), got.Instances)
	}
	for i := range want {
		g, w := got.Instances[i], want[i]
		if g.Name != w.Name || g.State != w.State || g.WebRTCDeviceID != w.WebRTCDeviceID ||
			g.ADBPort != w.ADBPort || g.Slot != w.Slot || !slices.Equal(g.Displays, w.Displays) {
			t.Errorf("instance %d = %+v, want %+v", i, g, w)
		}
	}
	if got.InstanceCount != len(want) {
		t.Errorf("InstanceCount = %d, want the desired %d", got.InstanceCount, len(want))
	}
	// The folded observation must be the failed instance's, and not the first
	// two instances' Running. That is why the array is persisted.
	if folded := device.FoldInstanceStates(got.Instances); folded != device.CVDStateBootFailed {
		t.Errorf("folded state = %q, want %q, since a dead instance must decide the group",
			folded, device.CVDStateBootFailed)
	}
}

// A device created before the column existed, or by a create that never reached
// the host, has to keep working. Every reachability path reads the array, so an
// empty one would make an ordinary running device unaddressable.
func TestDeviceRepositoryPreInstancesRowReadsAsOneInstance(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	hostID := "host-instances-002"
	if err := db.SeedHost(ctx, hostID, "legacy-host", "europe-west4-a"); err != nil {
		t.Fatalf("seeding host: %v", err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)

	// No Instances set, which is exactly the shape every row written before the
	// column existed has.
	err := repo.Create(ctx, &device.Device{
		ID: "dev-instances-002", HostID: hostID, Name: "legacy-device",
		Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream,
		CVDID: "cvdlegacy/legacy-device", ADBPort: 6520,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("creating device: %v", err)
	}

	got, err := repo.Get(ctx, "dev-instances-002")
	if err != nil {
		t.Fatalf("reading device back: %v", err)
	}
	if len(got.Instances) != 1 {
		t.Fatalf("read back %d instances, want exactly 1 synthesized: %+v", len(got.Instances), got.Instances)
	}
	if got.Instances[0].Name != "legacy-device" {
		t.Errorf("synthesized name = %q, want the CVD id's name half", got.Instances[0].Name)
	}
	if got.Instances[0].ADBPort != 6520 {
		t.Errorf("synthesized ADBPort = %d, want the row's 6520", got.Instances[0].ADBPort)
	}
}

// max_devices and current_device_count are denominated in instances, while the
// destructive-reset gate asks a different question (is anything alive here at
// all) and counts rows. One live device can want eight slots, so the two
// answers diverge, and a capacity gate reading the row count would shrink a
// host below what it already carries.
func TestDeviceRepositoryCountsLiveInstancesSeparatelyFromRows(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	hostID := "host-instances-003"
	if err := db.SeedHost(ctx, hostID, "counting-host", "europe-west4-a"); err != nil {
		t.Fatalf("seeding host: %v", err)
	}
	repo := devicefeature.NewDeviceRepository(db.Client)

	// One live 4-instance device, plus an error row that backs no CVD and must
	// be excluded from both answers.
	for _, d := range []*device.Device{
		{
			ID: "dev-live-4", HostID: hostID, Name: "quad",
			Status: device.StatusRunning, GPUMode: device.GPUModeGfxstream,
			Mode: "advanced", InstanceCount: 4,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		},
		{
			ID: "dev-failed", HostID: hostID, Name: "failed",
			Status: device.StatusError, GPUMode: device.GPUModeGfxstream,
			Mode: "advanced", InstanceCount: 2,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		},
	} {
		if err := repo.Create(ctx, d); err != nil {
			t.Fatalf("creating %s: %v", d.ID, err)
		}
	}

	rows, err := repo.CountLiveByHost(ctx, hostID)
	if err != nil {
		t.Fatalf("CountLiveByHost: %v", err)
	}
	if rows != 1 {
		t.Errorf("CountLiveByHost = %d, want 1 live device row", rows)
	}

	slots, err := repo.CountLiveInstancesByHost(ctx, hostID)
	if err != nil {
		t.Fatalf("CountLiveInstancesByHost: %v", err)
	}
	if slots != 4 {
		t.Errorf("CountLiveInstancesByHost = %d, want the 4 slots that one live device desires", slots)
	}
}
