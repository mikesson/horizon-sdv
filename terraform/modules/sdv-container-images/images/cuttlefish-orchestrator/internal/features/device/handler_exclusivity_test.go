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
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

const exclusivityHostID = "123e4567-e89b-12d3-a456-426614174000"

var (
	exclusivityPair = map[string]any{
		"host_id": exclusivityHostID,
		"name":    "sdv-pair",
		"mode":    "advanced",
		"advanced_config": map[string]any{"env_config": map[string]any{"instances": []any{
			map[string]any{"name": "core", "disk": map[string]any{"default_build": "gs://b/core.zip"}},
			map[string]any{"name": "ivi", "disk": map[string]any{"default_build": "gs://b/ivi.zip"}},
		}}},
	}
	exclusivitySingle = map[string]any{"host_id": exclusivityHostID, "name": "solo", "build_id": "12345678"}
)

// validateOnly returns body as a validate_only request.
func validateOnly(body map[string]any) map[string]any {
	b := maps.Clone(body)
	b["validate_only"] = true
	return b
}

// A validate_only create runs the same rules against the host and its device
// list that the create transaction runs. The fake repository does not mirror
// that transaction, so the commit path is covered by the emulator test
// TestDeviceRepository_CreateEnforcesHostExclusivity.
func TestDeviceHandler_PlanCreate_HostExclusivity(t *testing.T) {
	t.Parallel()
	groupOnHost := &device.Device{
		ID: "dev-pair", HostID: exclusivityHostID, Name: "pair", Status: device.StatusRunning, InstanceCount: 2,
	}
	singleOnHost := &device.Device{
		ID: "dev-solo", HostID: exclusivityHostID, Name: "solo-0", Status: device.StatusRunning, InstanceCount: 1,
	}
	deletedGroup := &device.Device{
		ID: "dev-gone", HostID: exclusivityHostID, Name: "gone", Status: device.StatusDeleted, InstanceCount: 2,
	}

	cases := []struct {
		name      string
		exclusive bool
		onHost    []*device.Device
		body      map[string]any
		want      string // empty means accepted
	}{
		{"pair on an empty shared host", false, nil, exclusivityPair, "needs a host to itself"},
		{"pair on an occupied shared host", false, []*device.Device{singleOnHost}, exclusivityPair, "needs a host to itself"},
		{"pair on an occupied exclusive host", true, []*device.Device{singleOnHost}, exclusivityPair, "needs an empty host"},
		{"single on a pair's host", true, []*device.Device{groupOnHost}, exclusivitySingle, "carries a multi-instance device"},
		{"pair on an empty exclusive host", true, nil, exclusivityPair, ""},
		{"single on an empty exclusive host", true, nil, exclusivitySingle, ""},
		{"single on an occupied shared host", false, []*device.Device{singleOnHost}, exclusivitySingle, ""},
		{"single on a reserved host beside a single", true, []*device.Device{singleOnHost}, exclusivitySingle, ""},
		{"single beside a deleted pair's tombstone", true, []*device.Device{singleOnHost, deletedGroup}, exclusivitySingle, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newHandlerAPI(t)
			var slots int64
			for _, d := range tc.onHost {
				cp := *d
				f.deviceRepo.Devices[d.ID] = &cp
				if d.Status != device.StatusDeleted {
					slots += int64(d.DesiredInstanceCount())
				}
			}
			f.hostRepo.Hosts[exclusivityHostID] = &host.Host{
				ID: exclusivityHostID, Status: host.StatusRunning, MaxDevices: 4,
				Exclusive: tc.exclusive, CurrentDeviceCount: slots,
			}

			resp := f.api.Post("/v2/devices", validateOnly(tc.body))
			if tc.want == "" {
				assert.Equal(t, 200, resp.Code, resp.Body.String())
				return
			}
			assert.Equal(t, 409, resp.Code, resp.Body.String())
			assert.Contains(t, resp.Body.String(), tc.want)
			assert.Contains(t, resp.Body.String(), "requirements and no host_id", "the refusal says what to do instead")
		})
	}
}

// A refusal from the create transaction reaches the caller as 409 and stores
// no device. The task mutation is built before the commit and rides the
// refused transaction, so the fake enqueuer records it; the row is the
// evidence.
func TestDeviceHandler_CreateDevice_ExclusivityRefusalIs409(t *testing.T) {
	t.Parallel()
	f := newHandlerAPI(t)
	f.hostRepo.Hosts[exclusivityHostID] = &host.Host{ID: exclusivityHostID, Status: host.StatusRunning, MaxDevices: 4}
	f.deviceRepo.CreateErr = checkHostExclusivity(hostOccupancy{HostID: exclusivityHostID}, 2)

	resp := f.api.Post("/v2/devices", exclusivityPair)
	assert.Equal(t, 409, resp.Code, resp.Body.String())
	assert.Contains(t, resp.Body.String(), "needs a host to itself")
	assert.Empty(t, f.deviceRepo.Devices)
}

// The rules on their own.
func TestCheckHostExclusivity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		host      hostOccupancy
		instances int
		refused   bool
	}{
		{"single on an empty shared host", hostOccupancy{}, 1, false},
		{"single on an occupied shared host", hostOccupancy{Slots: 3}, 1, false},
		{"pair on an empty shared host", hostOccupancy{}, 2, true},
		{"pair on an empty exclusive host", hostOccupancy{Exclusive: true}, 2, false},
		{"pair on an occupied exclusive host", hostOccupancy{Exclusive: true, Slots: 1}, 2, true},
		{"single on an empty exclusive host", hostOccupancy{Exclusive: true}, 1, false},
		{"single on a reserved host beside a single", hostOccupancy{Exclusive: true, Slots: 1}, 1, false},
		{"single on a pair's host", hostOccupancy{Exclusive: true, Slots: 2, CarriesGroup: true}, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := checkHostExclusivity(tc.host, tc.instances)
			if !tc.refused {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, host.ErrInvalidState, "want the 409-mapped host.ErrInvalidState")
		})
	}
}

// The device list is read only where the answer can change the verdict.
func TestHostOccupancyNeedsGroupRead(t *testing.T) {
	t.Parallel()
	assert.True(t, hostOccupancy{Exclusive: true, Slots: 1}.needsGroupRead(1))
	assert.False(t, hostOccupancy{Exclusive: true}.needsGroupRead(1), "an empty host carries nothing")
	assert.False(t, hostOccupancy{Slots: 1}.needsGroupRead(1), "a shared host never admits a group")
	assert.False(t, hostOccupancy{Exclusive: true, Slots: 1}.needsGroupRead(2), "a group is refused on any occupied host")
}
