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

package models

import (
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// Every field of every instance must survive the column, in the host's own
// order. The array is the only record of how to reach an instance past the
// first, so a field dropped in the marshal is a device that cannot be streamed
// or dialed, with nothing failing loudly to say so.
func TestDeviceRowInstancesRoundTrip(t *testing.T) {
	t.Parallel()
	want := []device.Instance{
		{
			Name:           "alpha",
			State:          device.CVDStateRunning,
			WebRTCDeviceID: "cvd7ee2466b-alpha-1",
			ADBPort:        6521,
			Slot:           2,
			Displays:       []string{"720 x 1280 ( 320 )"},
		},
		{
			Name:           "beta",
			State:          device.CVDStateStarting,
			WebRTCDeviceID: "cvd7ee2466b-beta-2",
			ADBPort:        6524,
			Slot:           5,
			Displays:       []string{"1080 x 600 ( 120 )", "400 x 600 ( 120 )"},
		},
		{
			Name:           "gamma",
			State:          device.CVDStateBootFailed,
			WebRTCDeviceID: "cvd7ee2466b-gamma-3",
			ADBPort:        6527,
			Slot:           8,
		},
	}

	got := FromDeviceDomain(&device.Device{
		ID: "dev-1", HostID: "host-1", Name: "dev-a",
		CVDID: "cvd7ee2466b/alpha", Instances: want,
	}).ToDomain().Instances

	if len(got) != len(want) {
		t.Fatalf("round-tripped %d instances, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i].Name {
			t.Errorf("instance %d: order or name changed: got %q, want %q", i, got[i].Name, want[i].Name)
		}
		if got[i].State != want[i].State {
			t.Errorf("instance %q: State = %q, want %q", want[i].Name, got[i].State, want[i].State)
		}
		if got[i].WebRTCDeviceID != want[i].WebRTCDeviceID {
			t.Errorf("instance %q: WebRTCDeviceID = %q, want %q",
				want[i].Name, got[i].WebRTCDeviceID, want[i].WebRTCDeviceID)
		}
		if got[i].ADBPort != want[i].ADBPort {
			t.Errorf("instance %q: ADBPort = %d, want %d", want[i].Name, got[i].ADBPort, want[i].ADBPort)
		}
		// Non-contiguous on purpose: slots are host-global and taken
		// lowest-free-first, so a marshal that renumbered them would still
		// satisfy any 1,2,3 fixture.
		if got[i].Slot != want[i].Slot {
			t.Errorf("instance %q: Slot = %d, want %d", want[i].Name, got[i].Slot, want[i].Slot)
		}
		if len(got[i].Displays) != len(want[i].Displays) {
			t.Errorf("instance %q: %d displays, want %d",
				want[i].Name, len(got[i].Displays), len(want[i].Displays))
			continue
		}
		for j := range want[i].Displays {
			if got[i].Displays[j] != want[i].Displays[j] {
				t.Errorf("instance %q display %d = %q, want %q",
					want[i].Name, j, got[i].Displays[j], want[i].Displays[j])
			}
		}
	}
}

// A row written before the column existed carries its one instance in the flat
// columns, and every reachability path reads the array. Without the synthesis
// those devices read as having no instances at all, which is indistinguishable
// from a group that never came up.
func TestDeviceRowSynthesizesInstanceFromCVDID(t *testing.T) {
	t.Parallel()
	d := (&DeviceRow{
		DeviceID: "dev-1", HostID: "host-1", Name: "dev-a",
		CVDID:     ToNullString("cvdabc123/alpha"),
		Status:    string(device.StatusRunning),
		CVDStatus: ToNullString(device.CVDStateRunning),
		ADBPort:   ToNullInt64(6520),
	}).ToDomain()

	if len(d.Instances) != 1 {
		t.Fatalf("synthesized %d instances, want exactly 1: %+v", len(d.Instances), d.Instances)
	}
	got := d.Instances[0]
	if got.Name != "alpha" {
		t.Errorf("Name = %q, want the CVD id's name half %q", got.Name, "alpha")
	}
	if got.State != device.CVDStateRunning {
		t.Errorf("State = %q, want the row's cvd_status %q", got.State, device.CVDStateRunning)
	}
	if got.ADBPort != 6520 {
		t.Errorf("ADBPort = %d, want the row's adb_port 6520", got.ADBPort)
	}
	// The host appends a fleet-unique instance id that cannot be derived here,
	// so a constructed value would address a device the operator never
	// registered. Empty is the accurate answer and callers fall back to asking
	// the host; a plausible guess would silently break streaming.
	if got.WebRTCDeviceID != "" {
		t.Errorf("WebRTCDeviceID = %q, want empty. It is not derivable from the group and name", got.WebRTCDeviceID)
	}
}

// A stored array always wins over the synthesis, or a multi-instance device
// would collapse back to its first instance on every read.
func TestDeviceRowStoredInstancesBeatTheSynthesis(t *testing.T) {
	t.Parallel()
	d := FromDeviceDomain(&device.Device{
		ID: "dev-1", HostID: "host-1", Name: "dev-a",
		CVDID: "cvdabc123/alpha",
		Instances: []device.Instance{
			{Name: "alpha", State: device.CVDStateRunning},
			{Name: "beta", State: device.CVDStateRunning},
		},
	}).ToDomain()

	if len(d.Instances) != 2 {
		t.Fatalf("read back %d instances, want the 2 that were stored", len(d.Instances))
	}
}

// A device with no CVD at all has nothing to synthesize from, and must not
// invent an instance: an empty array is what "the host has not answered" means,
// and the fold reads it as no observation rather than as a healthy group.
func TestDeviceRowWithoutCVDIDHasNoInstances(t *testing.T) {
	t.Parallel()
	d := (&DeviceRow{
		DeviceID: "dev-1", HostID: "host-1", Name: "dev-a",
		Status: string(device.StatusPending),
	}).ToDomain()

	if len(d.Instances) != 0 {
		t.Errorf("pending device synthesized %d instances, want none: %+v", len(d.Instances), d.Instances)
	}
}

// The desired count is spec and survives independently of the observation. The
// host counter is charged this many slots at admission and refunded the same
// number at delete, so a count derived from the observed array refunds zero for
// a create that failed before the host answered, and the counter floors at
// zero, which makes that leak permanent.
func TestDeviceRowDesiredCountSurvivesWithNoObservedInstances(t *testing.T) {
	t.Parallel()
	d := FromDeviceDomain(&device.Device{
		ID: "dev-1", HostID: "host-1", Name: "dev-a",
		Mode: "advanced", InstanceCount: 3,
	}).ToDomain()

	if d.InstanceCount != 3 {
		t.Errorf("InstanceCount = %d, want the desired 3", d.InstanceCount)
	}
	if len(d.Instances) != 0 {
		t.Errorf("observed %d instances, want none. The host never answered", len(d.Instances))
	}
}
