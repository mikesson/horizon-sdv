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

package devicecontroller

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/device/devicetest"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// fixedLiveOperations is a live-operation lens over a fabricated set.
//
// Nothing issues a per-instance verb yet, since the transport that will is
// separate work, so the (device, instance) pair the device controller keys its hold
// on has to be built here. That is also what makes these tests the whole
// safety net: a regression in the pairing stays invisible in production until
// that transport lands on top of it.
type fixedLiveOperations operation.LiveSet

func (f fixedLiveOperations) LiveByResources(_ context.Context, _ string, ids []string) (operation.LiveSet, error) {
	out := make(operation.LiveSet, len(f))
	for key := range f {
		if slices.Contains(ids, key.ResourceID) {
			out[key] = true
		}
	}
	return out, nil
}

// fleetHost stands up a Host Orchestrator whose fleet listing is exactly the
// given CVDs, and returns a running host row addressing it.
func fleetHost(t *testing.T, cvds string) *host.Host {
	t.Helper()
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cvds" && r.Method == http.MethodGet {
			w.Write([]byte(`{"cvds": ` + cvds + `}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(ho.Close)
	return &host.Host{
		ID: "h", Status: host.StatusRunning, InternalIP: "127.0.0.1",
		HostOrchestratorPort: int64(ho.Listener.Addr().(*net.TCPAddr).Port),
	}
}

// checkOneDevice runs a single health check over one device on a host whose
// fleet listing is cvds, with live standing in for the verbs in flight, and
// returns the repository and the one health update the cycle produced.
func checkOneDevice(t *testing.T, d *device.Device, cvds string, live operation.LiveSet,
	failureThreshold int,
) (*devicetest.FakeRepo, device.HealthUpdate) {
	t.Helper()
	h := fleetHost(t, cvds)
	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts[h.ID] = h
	deviceRepo := devicetest.NewFakeRepo(d)

	cfg := device.HealthCheckConfig{
		Interval: time.Minute, FailureThreshold: failureThreshold, ZombieThreshold: 10,
	}
	svc := New(cfg, deviceRepo, hostRepo, hoclient.NewFactory(hoclient.DefaultClientConfig()),
		slog.Default(), WithLiveOperations(fixedLiveOperations(live)))
	svc.checkHostDevices(t.Context(), h, []*device.Device{d})

	if len(deviceRepo.HealthBatches) != 1 || len(deviceRepo.HealthBatches[0]) != 1 {
		t.Fatalf("want one health update for one device, got %v", deviceRepo.HealthBatches)
	}
	return deviceRepo, deviceRepo.HealthBatches[0][0]
}

// A verb passing one instance through must not suppress the evidence that
// another instance is dead. Keyed by device, which is how the hold started
// when a device was one CVD, a restart of instance i2 exempts i5 as well, and
// the device sits healthy through a failure nothing is working on.
//
// The other half of the same claim: i2's own observation, which a relaunch
// legitimately passes through, must not settle the device to error.
func TestService_PerInstanceVerbLeavesTheOtherInstancesJudged(t *testing.T) {
	t.Parallel()
	d := &device.Device{
		ID: "multi0001", HostID: "h", CVDID: "cvdmulti0/i2",
		Status: device.StatusRunning, HealthStatus: device.HealthStatusHealthy,
		Instances: []device.Instance{
			{Name: "i2", State: device.CVDStateRunning},
			{Name: "i5", State: device.CVDStateRunning},
		},
	}
	cvds := `[{"group":"cvdmulti0","name":"i2","status":"Boot failed"},
	          {"group":"cvdmulti0","name":"i5","status":"Unreachable"}]`
	live := operation.LiveSet{{ResourceID: "multi0001", InstanceName: "i2"}: true}

	repo, update := checkOneDevice(t, d, cvds, live, 1)

	if update.Status != device.HealthStatusUnhealthy {
		t.Errorf("health = %q, want unhealthy: i5 is unreachable and no verb is touching it", update.Status)
	}
	if got, ok := repo.StatusUpdates[d.ID]; ok {
		t.Errorf("settled the lifecycle to %q from i2's mid-restart boot failure; the verb owns that instance", got)
	}
	if update.CVDStatus != device.CVDStateUnreachable {
		t.Errorf("cvd_status = %q, want %q; the fold runs over the instances no verb owns",
			update.CVDStatus, device.CVDStateUnreachable)
	}
}

// A verb with no instance name is device-wide (start, stop, delete and every
// group verb), so it exempts every instance, which is exactly what it is
// passing through. The raw observation is still recorded: held means observe
// and do not act, not observe nothing.
func TestService_DeviceWideVerbExemptsEveryInstance(t *testing.T) {
	t.Parallel()
	d := &device.Device{
		ID: "wide0001", HostID: "h", CVDID: "cvdwide00/i1",
		Status: device.StatusRunning, HealthStatus: device.HealthStatusHealthy,
		Instances: []device.Instance{
			{Name: "i1", State: device.CVDStateRunning},
			{Name: "i2", State: device.CVDStateRunning},
		},
	}
	cvds := `[{"group":"cvdwide00","name":"i1","status":"Stopped"},
	          {"group":"cvdwide00","name":"i2","status":"Unreachable"}]`
	live := operation.LiveSet{{ResourceID: "wide0001"}: true}

	repo, update := checkOneDevice(t, d, cvds, live, 1)

	if update.Status != device.HealthStatusHealthy {
		t.Errorf("health = %q, want the held healthy: a device-wide verb owns every instance", update.Status)
	}
	if got, ok := repo.StatusUpdates[d.ID]; ok {
		t.Errorf("settled the lifecycle to %q under a device-wide verb", got)
	}
	if update.CVDStatus != device.CVDStateUnreachable {
		t.Errorf("cvd_status = %q, want %q: a held device still records what was seen",
			update.CVDStatus, device.CVDStateUnreachable)
	}
}

// The device is the leased unit and its instances are all evidence about it.
// Reading only the CVD the device row names, which is always instance 0,
// reported a group with a dead second instance as entirely healthy.
func TestService_AGroupIsHealthyOnlyWhenEveryInstanceRuns(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		stored    []device.Instance
		cvds      string
		wantState string
		want      device.HealthStatus
	}{
		{
			name:   "every instance running",
			stored: []device.Instance{{Name: "i1"}, {Name: "i2"}},
			cvds: `[{"group":"cvdgroup0","name":"i1","status":"Running"},
			        {"group":"cvdgroup0","name":"i2","status":"Running"}]`,
			wantState: device.CVDStateRunning,
			want:      device.HealthStatusHealthy,
		},
		{
			name:   "one instance unreachable",
			stored: []device.Instance{{Name: "i1"}, {Name: "i2"}},
			cvds: `[{"group":"cvdgroup0","name":"i1","status":"Running"},
			        {"group":"cvdgroup0","name":"i2","status":"Unreachable"}]`,
			wantState: device.CVDStateUnreachable,
			want:      device.HealthStatusUnhealthy,
		},
		{
			// Absent outranks unreachable, so the group reports the absence.
			// That is what keeps the cheap restart rung (it fires on exactly
			// Unreachable) from spending its budget relaunching a group that
			// is missing a member.
			name:      "one instance absent from a listing that answered",
			stored:    []device.Instance{{Name: "i1"}, {Name: "i2"}},
			cvds:      `[{"group":"cvdgroup0","name":"i1","status":"Running"}]`,
			wantState: "",
			want:      device.HealthStatusUnhealthy,
		},
		{
			// A member the row does not know is still the device's, and the
			// host visit is the only place that drift is visible.
			name:   "a member the row does not know is dead",
			stored: []device.Instance{{Name: "i1"}},
			cvds: `[{"group":"cvdgroup0","name":"i1","status":"Running"},
			        {"group":"cvdgroup0","name":"i2","status":"Unreachable"}]`,
			wantState: device.CVDStateUnreachable,
			want:      device.HealthStatusUnhealthy,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := &device.Device{
				ID: "group001", HostID: "h", CVDID: "cvdgroup0/i1",
				Status: device.StatusRunning, HealthStatus: device.HealthStatusHealthy,
				Instances: tc.stored,
			}
			_, update := checkOneDevice(t, d, tc.cvds, nil, 1)

			if update.Status != tc.want {
				t.Errorf("health = %q, want %q", update.Status, tc.want)
			}
			if update.CVDStatus != tc.wantState {
				t.Errorf("cvd_status = %q, want %q folded over the group", update.CVDStatus, tc.wantState)
			}
		})
	}
}

// The steady state's hot path. A non-nil instance array forces the full row
// write past the TimestampOnly dedup, so an unchanged array must not ride one:
// sending it every cycle costs every healthy device in the fleet a full write
// per interval, forever, for data that did not change.
func TestService_UnchangedInstanceArrayKeepsTheCheapWritePath(t *testing.T) {
	t.Parallel()
	d := &device.Device{
		ID: "cheap001", HostID: "h", CVDID: "cvdcheap0/i1",
		Status: device.StatusRunning, HealthStatus: device.HealthStatusHealthy,
		CVDStatus: device.CVDStateRunning,
		Instances: []device.Instance{
			{Name: "i1", State: device.CVDStateRunning, WebRTCDeviceID: "cvdcheap0-i1-1", ADBPort: 6520},
			{Name: "i2", State: device.CVDStateRunning, WebRTCDeviceID: "cvdcheap0-i2-2", ADBPort: 6521},
		},
	}
	cvds := `[{"group":"cvdcheap0","name":"i1","status":"Running"},
	          {"group":"cvdcheap0","name":"i2","status":"Running"}]`

	_, update := checkOneDevice(t, d, cvds, nil, 3)

	if !update.TimestampOnly {
		t.Errorf("an unchanged healthy device did not take the timestamp-only path: %+v", update)
	}
	if update.Instances != nil {
		t.Errorf("sent an unchanged instance array (%+v); it forces the full row write for every healthy device, every cycle",
			update.Instances)
	}
}

// The fleet listing carries a name and a state per instance and nothing else.
// An array rebuilt from it alone would blank the operator device id every
// signaling and ADB path addresses an instance by, the port that records where
// it was published, the slot the create recovered from that port, and the
// displays the create recorded. The slot is established once, at create, so
// every refresh after it is the only thing keeping it on the row.
func TestService_ObservedInstancesKeepWhatTheListingCannotCarry(t *testing.T) {
	t.Parallel()
	d := &device.Device{
		ID: "enrich01", HostID: "h", CVDID: "cvdenrich/i1",
		Status: device.StatusRunning, HealthStatus: device.HealthStatusHealthy,
		Instances: []device.Instance{{
			Name: "i1", State: device.CVDStateRunning,
			WebRTCDeviceID: "cvdenrich-i1-1", ADBPort: 6524, Slot: 5,
			Displays: []string{"720 x 1280 ( 320 )"},
		}},
	}
	cvds := `[{"group":"cvdenrich","name":"i1","status":"Unreachable"}]`

	_, update := checkOneDevice(t, d, cvds, nil, 3)

	if len(update.Instances) != 1 {
		t.Fatalf("want the one observed instance written, got %+v", update.Instances)
	}
	got := update.Instances[0]
	if got.State != device.CVDStateUnreachable {
		t.Errorf("state = %q, want the freshly observed %q", got.State, device.CVDStateUnreachable)
	}
	if got.WebRTCDeviceID != "cvdenrich-i1-1" || got.ADBPort != 6524 || got.Slot != 5 ||
		!slices.Equal(got.Displays, []string{"720 x 1280 ( 320 )"}) {
		t.Errorf("observation blanked what the listing does not carry: %+v", got)
	}
}

// A member of the group that no device row records is real, and the host visit
// is the only place that drift is visible. CCO did not place it and cannot
// recover its slot from a listing that carries no port. It takes slot zero,
// meaning unknown, rather than a fabricated one that would seat it over a
// sibling in anything keyed on the slot.
func TestService_AnUnknownGroupMemberGetsNoInventedSlot(t *testing.T) {
	t.Parallel()
	d := &device.Device{
		ID: "drift001", HostID: "h", CVDID: "cvddrift0/i1",
		Status: device.StatusRunning, HealthStatus: device.HealthStatusHealthy,
		Instances: []device.Instance{{
			Name: "i1", State: device.CVDStateRunning, ADBPort: 6520, Slot: 1,
		}},
	}
	cvds := `[{"group":"cvddrift0","name":"i1","status":"Running"},
	          {"group":"cvddrift0","name":"i4","status":"Running"}]`

	_, update := checkOneDevice(t, d, cvds, nil, 3)

	if len(update.Instances) != 2 {
		t.Fatalf("want the known instance plus the drifted one, got %+v", update.Instances)
	}
	if update.Instances[0].Slot != 1 {
		t.Errorf("the placed instance's slot = %d, want the stored 1", update.Instances[0].Slot)
	}
	if got := update.Instances[1]; got.Name != "i4" || got.Slot != 0 {
		t.Errorf("drifted member = %+v, want name i4 at slot 0: nothing here knows its real slot", got)
	}
}
