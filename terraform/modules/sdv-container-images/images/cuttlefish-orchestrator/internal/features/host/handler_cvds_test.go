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

package hostfeature

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

const (
	cvdTestHostID   = "11111111-1111-1111-1111-111111111111"
	cvdTestDeviceID = "abcdef12-3456-7890-abcd-ef1234567890"
	cvdDeletedID    = "99999999-3456-7890-abcd-ef1234567890"
)

type fakeHostGetter map[string]*host.Host

func (f fakeHostGetter) Get(_ context.Context, id string) (*host.Host, error) {
	h, ok := f[id]
	if !ok {
		return nil, &host.NotFoundError{ID: id}
	}
	return h, nil
}

type fakeCVDLister struct {
	cvds  []*hoclient.CVD
	err   error
	calls int
}

func (f *fakeCVDLister) ListCVDs(context.Context) ([]*hoclient.CVD, error) {
	f.calls++
	return f.cvds, f.err
}

// newCVDTestHandler builds a handler whose viewer reads one host with the
// given status, the device rows given, and the fake Host Orchestrator.
func newCVDTestHandler(status host.Status, devices []*device.Device, ho *fakeCVDLister) (*HostHandler, *string) {
	gotURL := new(string)
	h := NewHostHandler(nil)
	h.SetCVDViewer(&CVDViewer{
		hosts: fakeHostGetter{cvdTestHostID: {
			ID: cvdTestHostID, Status: status, InternalIP: "10.0.0.7", HostOrchestratorPort: 2080,
		}},
		devices: func(_ context.Context, hostID string) ([]*device.Device, error) {
			if hostID != cvdTestHostID {
				return nil, errors.New("wrong host")
			}
			return devices, nil
		},
		clientFor: func(hostURL string) hostCVDLister {
			*gotURL = hostURL
			return ho
		},
	})
	return h, gotURL
}

func statusOf(t *testing.T, err error) int {
	t.Helper()
	var se huma.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error %v (%T) carries no HTTP status", err, err)
	}
	return se.GetStatus()
}

func TestListHostCVDsJoinsDevicesAndKeepsForeignCVDs(t *testing.T) {
	t.Parallel()
	ho := &fakeCVDLister{cvds: []*hoclient.CVD{
		{Group: "cvd_1", Name: "1", Status: "Running", ADBSerial: "0.0.0.0:6520", WebRTCDeviceID: "cvd_1-1"},
		{Group: device.CVDGroupName(cvdTestDeviceID), Name: "2", Status: "Running", WebRTCDeviceID: "w2"},
		{Group: device.CVDGroupName(cvdTestDeviceID), Name: "1", Status: "Starting"},
		{Group: device.CVDGroupName(cvdDeletedID), Name: "1", Status: "Stopped"},
	}}
	devices := []*device.Device{
		{ID: cvdTestDeviceID, Status: device.StatusRunning},
		// A deleted row must not claim the CVD it left behind.
		{ID: cvdDeletedID, Status: device.StatusDeleted},
	}
	h, gotURL := newCVDTestHandler(host.StatusRunning, devices, ho)

	out, err := h.ListHostCVDs(t.Context(), &ListHostCVDsInput{ID: cvdTestHostID})
	if err != nil {
		t.Fatalf("ListHostCVDs: %v", err)
	}
	if *gotURL != "http://10.0.0.7:2080" {
		t.Errorf("HO URL = %q, want the host's internal address", *gotURL)
	}
	got := out.Body.CVDs
	owned := device.CVDGroupName(cvdTestDeviceID)
	want := []HostCVD{
		{Group: device.CVDGroupName(cvdDeletedID), Name: "1", Status: "Stopped"},
		{Group: "cvd_1", Name: "1", Status: "Running", ADBSerial: "0.0.0.0:6520", WebRTCDeviceID: "cvd_1-1"},
		{Group: owned, Name: "1", Status: "Starting", DeviceID: cvdTestDeviceID},
		{Group: owned, Name: "2", Status: "Running", WebRTCDeviceID: "w2", DeviceID: cvdTestDeviceID},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d CVDs, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cvd[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestListHostCVDsUnknownHostIs404(t *testing.T) {
	t.Parallel()
	ho := &fakeCVDLister{}
	h, _ := newCVDTestHandler(host.StatusRunning, nil, ho)

	_, err := h.ListHostCVDs(t.Context(), &ListHostCVDsInput{ID: "22222222-2222-2222-2222-222222222222"})
	if got := statusOf(t, err); got != http.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
	if ho.calls != 0 {
		t.Errorf("an unknown host reached the Host Orchestrator %d times", ho.calls)
	}
}

func TestListHostCVDsNotRunningIs409(t *testing.T) {
	t.Parallel()
	for _, status := range []host.Status{host.StatusStopped, host.StatusProvisioning, host.StatusError} {
		ho := &fakeCVDLister{}
		h, _ := newCVDTestHandler(status, nil, ho)

		_, err := h.ListHostCVDs(t.Context(), &ListHostCVDsInput{ID: cvdTestHostID})
		if got := statusOf(t, err); got != http.StatusConflict {
			t.Errorf("%s: status = %d, want 409", status, got)
		}
		if ho.calls != 0 {
			t.Errorf("%s: a host that is not running reached the Host Orchestrator", status)
		}
	}
}

func TestListHostCVDsRunningHostWithoutIPIs409(t *testing.T) {
	t.Parallel()
	ho := &fakeCVDLister{}
	h, _ := newCVDTestHandler(host.StatusRunning, nil, ho)
	h.cvds.hosts = fakeHostGetter{cvdTestHostID: {
		ID: cvdTestHostID, Status: host.StatusRunning, HostOrchestratorPort: 2080,
	}}

	_, err := h.ListHostCVDs(t.Context(), &ListHostCVDsInput{ID: cvdTestHostID})
	if got := statusOf(t, err); got != http.StatusConflict {
		t.Errorf("status = %d, want 409", got)
	}
	if ho.calls != 0 {
		t.Errorf("a host without an IP reached the Host Orchestrator %d times", ho.calls)
	}
}

func TestListHostCVDsDeletingAndErrorDevicesKeepTheirGroup(t *testing.T) {
	t.Parallel()
	const deletingID = "deadbeef-3456-7890-abcd-ef1234567890"
	ho := &fakeCVDLister{cvds: []*hoclient.CVD{
		{Group: device.CVDGroupName(cvdTestDeviceID), Name: "1", Status: "Stopped"},
		{Group: device.CVDGroupName(deletingID), Name: "1", Status: "Running"},
	}}
	devices := []*device.Device{
		{ID: cvdTestDeviceID, Status: device.StatusError},
		{ID: deletingID, Status: device.StatusDeleting},
	}
	h, _ := newCVDTestHandler(host.StatusRunning, devices, ho)

	out, err := h.ListHostCVDs(t.Context(), &ListHostCVDsInput{ID: cvdTestHostID})
	if err != nil {
		t.Fatalf("ListHostCVDs: %v", err)
	}
	owners := map[string]string{}
	for _, c := range out.Body.CVDs {
		owners[c.Group] = c.DeviceID
	}
	for _, id := range []string{cvdTestDeviceID, deletingID} {
		if got := owners[device.CVDGroupName(id)]; got != id {
			t.Errorf("group %s owner = %q, want %q", device.CVDGroupName(id), got, id)
		}
	}
}

func TestListHostCVDsHostOrchestratorFailureIs502(t *testing.T) {
	t.Parallel()
	ho := &fakeCVDLister{err: hoclient.ErrHostUnreachable}
	h, _ := newCVDTestHandler(host.StatusRunning, nil, ho)

	_, err := h.ListHostCVDs(t.Context(), &ListHostCVDsInput{ID: cvdTestHostID})
	if got := statusOf(t, err); got != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", got)
	}
}

func TestListHostCVDsWithoutViewerIs503(t *testing.T) {
	t.Parallel()
	_, err := NewHostHandler(nil).ListHostCVDs(t.Context(), &ListHostCVDsInput{ID: cvdTestHostID})
	if got := statusOf(t, err); got != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", got)
	}
}

func TestNewCVDViewerNeedsFactoryAndDevices(t *testing.T) {
	t.Parallel()
	if v := NewCVDViewer(fakeHostGetter{}, nil, hoclient.NewFactory(hoclient.ClientConfig{})); v != nil {
		t.Error("a viewer without a device lister must be nil")
	}
	lister := func(context.Context, string) ([]*device.Device, error) { return nil, nil }
	if v := NewCVDViewer(fakeHostGetter{}, lister, nil); v != nil {
		t.Error("a viewer without an HO factory must be nil")
	}
}
