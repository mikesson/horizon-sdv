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

package cvd

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/device/devicetest"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
)

// createdGroup is a fake Host Orchestrator that has no group yet and answers
// one create with the given /operations/:wait body, recording whether anything
// started or deleted the group afterwards.
type createdGroup struct {
	started atomic.Bool
	deleted atomic.Bool
	// startPath is the exact path the start was posted to. The group and the
	// per-instance start share a verb spelling, so "something started" cannot
	// tell a whole group coming up from only its first member.
	startPath atomic.Value
}

// newCreatedGroupActuator wires an actuator whose create wait resolves to
// waitBody, plus a pending device on a running host.
func newCreatedGroupActuator(t *testing.T, waitBody string) (*Actuator, *devicetest.FakeRepo, *createdGroup) {
	t.Helper()
	group := &createdGroup{}
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cvds" && r.Method == http.MethodGet:
			w.Write([]byte(`{"cvds": []}`)) // nothing to adopt
		case r.URL.Path == "/cvds" && r.Method == http.MethodPost:
			w.Write([]byte(`{"name": "op-create"}`))
		case strings.HasSuffix(r.URL.Path, "/:wait"):
			w.Write([]byte(waitBody))
		case strings.HasSuffix(r.URL.Path, "/:start"):
			group.started.Store(true)
			group.startPath.Store(r.URL.Path)
			w.Write([]byte(`{"name": "op-start"}`))
		case r.Method == http.MethodDelete:
			group.deleted.Store(true)
			w.Write([]byte(`{"name": "op-del"}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(ho.Close)
	hostURL, _ := url.Parse(ho.URL)
	port, _ := strconv.ParseInt(hostURL.Port(), 10, 64)

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{
		ID: "h", Status: host.StatusRunning, MaxDevices: 4,
		IPAddress: "127.0.0.1", HostOrchestratorPort: port,
	}
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["deadbeef-dev"] = &device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "alpha",
		Status: device.StatusPending, InstanceCount: 3,
	}
	return newTestActuator(t, deviceRepo, hostRepo), deviceRepo, group
}

// A create response covering a whole CVD group must persist every instance the
// host reported, in the host's order. The operator ids are fleet-unique and
// not derivable from group and name, so an array narrowed to the first entry
// leaves every instance past the first permanently unaddressable. Signaling
// and the ADB bridge have no other source for them.
func TestUpdateDeviceAfterCVDCreatePersistsEveryInstance(t *testing.T) {
	t.Parallel()
	act, deviceRepo, _ := newCreatedGroupActuator(t, `{"cvds":[
		{"group":"cvddeadbeef","name":"alpha","status":"Running","adb_serial":"0.0.0.0:6520",
		 "displays":["720 x 1280 ( 320 )"],"webrtc_device_id":"cvddeadbeef-alpha-1"},
		{"group":"cvddeadbeef","name":"beta","status":"Running","adb_serial":"0.0.0.0:6521",
		 "webrtc_device_id":"cvddeadbeef-beta-2"},
		{"group":"cvddeadbeef","name":"gamma","status":"Running","adb_serial":"0.0.0.0:6522",
		 "webrtc_device_id":"cvddeadbeef-gamma-3"}]}`)

	require.NoError(t, act.RunCreateCVD(t.Context(), "deadbeef-dev", nil))

	d := deviceRepo.Devices["deadbeef-dev"]
	assert.Equal(t, device.StatusRunning, d.Status)
	require.Len(t, d.Instances, 3, "the whole group is persisted, including the members after the first")

	names := make([]string, 0, len(d.Instances))
	operatorIDs := make([]string, 0, len(d.Instances))
	ports := make([]int64, 0, len(d.Instances))
	for _, inst := range d.Instances {
		names = append(names, inst.Name)
		operatorIDs = append(operatorIDs, inst.WebRTCDeviceID)
		ports = append(ports, inst.ADBPort)
	}
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, names, "the host's listing order is the array's order")
	assert.Equal(t, []string{"cvddeadbeef-alpha-1", "cvddeadbeef-beta-2", "cvddeadbeef-gamma-3"}, operatorIDs,
		"each instance keeps its own operator id; they differ by a trailing id only the host knows")
	assert.Equal(t, []int64{6520, 6521, 6522}, ports, "the ADB port is read per instance from its own adb_serial")
	assert.Equal(t, []string{"720 x 1280 ( 320 )"}, d.Instances[0].Displays)

	// The flat columns stay the device's own binding: instance 0.
	assert.Equal(t, "cvddeadbeef/alpha", d.CVDID)
	assert.Equal(t, int64(6520), d.ADBPort)
}

// Slots are host-global and cvd takes them one at a time, lowest-free-first,
// so a group placed on a host that has been churning holds whichever slots
// happened to be free. 2, 5 and 8 for three instances is an ordinary outcome.
// Each instance's slot is recovered from its own ADB port; anything derived
// from its position in the array seats every entry after the first hole
// against the wrong TAP device, vsock CID and radio ports.
func TestCreateRecordsTheHostsRealSlotsNotArrayPositions(t *testing.T) {
	t.Parallel()
	act, deviceRepo, _ := newCreatedGroupActuator(t, `{"cvds":[
		{"group":"cvddeadbeef","name":"alpha","status":"Running","adb_serial":"0.0.0.0:6521",
		 "webrtc_device_id":"cvddeadbeef-alpha-2"},
		{"group":"cvddeadbeef","name":"beta","status":"Running","adb_serial":"0.0.0.0:6524",
		 "webrtc_device_id":"cvddeadbeef-beta-5"},
		{"group":"cvddeadbeef","name":"gamma","status":"Running","adb_serial":"0.0.0.0:6527",
		 "webrtc_device_id":"cvddeadbeef-gamma-8"}]}`)

	require.NoError(t, act.RunCreateCVD(t.Context(), "deadbeef-dev", nil))

	instances := deviceRepo.Devices["deadbeef-dev"].Instances
	require.Len(t, instances, 3)
	slots := make([]int64, 0, len(instances))
	for _, inst := range instances {
		slots = append(slots, inst.Slot)
	}
	assert.Equal(t, []int64{2, 5, 8}, slots,
		"each slot comes from that instance's own ADB port, never from its index in the group")
}

// An instance the host described without an adb_serial has no recoverable
// slot, and zero is how the array says so. The subtraction has to be guarded
// for it: run on an absent port it yields -6519, which is neither a slot nor
// readable as unknown. Its siblings keep theirs.
func TestCreateLeavesTheSlotUnknownWhenTheHostPublishedNoADBPort(t *testing.T) {
	t.Parallel()
	act, deviceRepo, _ := newCreatedGroupActuator(t, `{"cvds":[
		{"group":"cvddeadbeef","name":"alpha","status":"Running","adb_serial":"0.0.0.0:6523",
		 "webrtc_device_id":"cvddeadbeef-alpha-4"},
		{"group":"cvddeadbeef","name":"beta","status":"Running",
		 "webrtc_device_id":"cvddeadbeef-beta-5"}]}`)

	require.NoError(t, act.RunCreateCVD(t.Context(), "deadbeef-dev", nil))

	instances := deviceRepo.Devices["deadbeef-dev"].Instances
	require.Len(t, instances, 2)
	assert.Equal(t, int64(4), instances[0].Slot, "the sibling with a serial still resolves its slot")
	require.Zero(t, instances[1].ADBPort, "the fixture's second instance publishes no adb_serial")
	assert.Equal(t, int64(0), instances[1].Slot,
		"an unreported port leaves the slot unknown; unguarded arithmetic would record -6519")
}

// Zero means unknown, so a slot is never negative: any port below the base is
// as unrecoverable as an absent one, and guarding on a port merely being
// positive would record the difference as a slot no TAP device answers to.
func TestCreateLeavesTheSlotUnknownWhenTheADBPortIsBelowTheBase(t *testing.T) {
	t.Parallel()
	act, deviceRepo, _ := newCreatedGroupActuator(t, `{"cvds":[
		{"group":"cvddeadbeef","name":"alpha","status":"Running","adb_serial":"0.0.0.0:5555",
		 "webrtc_device_id":"cvddeadbeef-alpha-1"}]}`)

	require.NoError(t, act.RunCreateCVD(t.Context(), "deadbeef-dev", nil))

	instances := deviceRepo.Devices["deadbeef-dev"].Instances
	require.Len(t, instances, 1)
	require.Equal(t, int64(5555), instances[0].ADBPort, "the fixture publishes a port below the base")
	assert.Equal(t, int64(0), instances[0].Slot, "a sub-base port leaves the slot unknown, never negative")
}

// A group whose worst instance failed to boot must not settle the device
// running: cvd resets the entire group when one instance fails, so the members
// that did come up are debris rather than a partial success. The create stays
// retryable and the husk is deleted, so the retry builds the group afresh
// instead of re-adopting it to exhaustion.
func TestUpdateDeviceAfterCVDCreateRefusesAPartlyBootedGroup(t *testing.T) {
	t.Parallel()
	act, deviceRepo, group := newCreatedGroupActuator(t, `{"cvds":[
		{"group":"cvddeadbeef","name":"alpha","status":"Running","webrtc_device_id":"cvddeadbeef-alpha-1"},
		{"group":"cvddeadbeef","name":"beta","status":"Boot failed","webrtc_device_id":"cvddeadbeef-beta-2"}]}`)

	err := act.RunCreateCVD(t.Context(), "deadbeef-dev", nil)

	require.Error(t, err, "a group that did not boot must surface to the worker")
	assert.Equal(t, device.StatusCreating, deviceRepo.Devices["deadbeef-dev"].Status,
		"one boot-failed instance means the device is not running")
	assert.True(t, group.deleted.Load(), "the dead group is deleted so the retry creates it from scratch")
	assert.False(t, group.started.Load(),
		"a boot-failed group is not startable; :start against it is a wasted round trip")
}

// A created group that did not come up is started with the GROUP verb. The
// host serves the group and per-instance starts as separate handlers under the
// same verb spelling, and reaching the per-instance one starts the first member
// while its siblings stay stopped. The fold then settles the whole device
// stopped and the create looks like a failure nobody caused. Both calls
// succeed against a host, so the path is the only thing that tells them apart.
func TestCreateStartsTheWholeGroupNotItsFirstInstance(t *testing.T) {
	t.Parallel()
	act, deviceRepo, group := newCreatedGroupActuator(t, `{"cvds":[
		{"group":"cvddeadbeef","name":"alpha","status":"Stopped","adb_serial":"0.0.0.0:6520",
		 "webrtc_device_id":"cvddeadbeef-alpha-1"},
		{"group":"cvddeadbeef","name":"beta","status":"Stopped","adb_serial":"0.0.0.0:6521",
		 "webrtc_device_id":"cvddeadbeef-beta-2"}]}`)

	require.NoError(t, act.RunCreateCVD(t.Context(), "deadbeef-dev", nil))

	require.True(t, group.started.Load(), "a group that is not running must be started")
	assert.Equal(t, "/cvds/cvddeadbeef/:start", group.startPath.Load(),
		"the group verb brings every instance up; the per-instance route leaves the siblings stopped")
	assert.False(t, group.deleted.Load(), "a startable group is not a husk")
	assert.Equal(t, device.StatusRunning, deviceRepo.Devices["deadbeef-dev"].Status)
}
