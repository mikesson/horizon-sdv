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
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

const retiredDeviceID = "5f0f0f0f-1111-4222-8333-555555555555"

// countingHostRepo counts host lookups. Every route reaches a Host
// Orchestrator through one, so zero lookups means no host client was built.
type countingHostRepo struct {
	*hosttest.FakeRepo
	gets atomic.Int64
}

func (r *countingHostRepo) Get(ctx context.Context, id string) (*host.Host, error) {
	r.gets.Add(1)
	return r.FakeRepo.Get(ctx, id)
}

// retiredFixture serves every device route, Huma and raw, over a real service.
// The device's host is live and points at a Host Orchestrator that counts
// requests and fails them, so a route that goes past the device lookup shows
// up in both counters and in a status other than 404.
type retiredFixture struct {
	api    *testAPI
	hosts  *countingHostRepo
	hoHits *atomic.Int64
}

func newRetiredFixture(t *testing.T, status device.Status) *retiredFixture {
	t.Helper()
	hits := &atomic.Int64{}
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(ho.Close)
	hoURL, err := url.Parse(ho.URL)
	require.NoError(t, err)
	port, err := strconv.ParseInt(hoURL.Port(), 10, 64)
	require.NoError(t, err)

	hosts := &countingHostRepo{FakeRepo: hosttest.NewFakeRepo(&host.Host{
		ID: "h1", Status: host.StatusRunning, MaxDevices: 4,
		InternalIP: "127.0.0.1", HostOrchestratorPort: port,
	})}
	devices := newDeviceRepoFake()
	devices.Devices[retiredDeviceID] = &device.Device{
		ID: retiredDeviceID, Name: "gone", HostID: "h1", Status: status,
		CVDID:     "cvdd1/inst",
		Instances: []device.Instance{{Name: "inst", WebRTCDeviceID: "cvdd1-inst-1"}},
	}

	logger := slog.New(slog.DiscardHandler)
	factory := hoclient.NewFactory(hoclient.DefaultClientConfig())
	svc, err := NewDeviceService(devices, hosts, newMockBuildRepo(), factory, logger,
		WithArtifacts(&fakeUploader{}, &fakePolicy{values: map[string]string{}}, "artifact-bucket"))
	require.NoError(t, err)
	handler := NewDeviceHandler(svc, testICEComposer(),
		NewSignalingHandler(devices, hosts, factory, logger),
		NewWebSocketHandler(devices, hosts, factory, logger, false))

	api := newTestAPI(t)
	handler.Register(api.API)
	handler.RegisterRaw(api.router)
	return &retiredFixture{api: api, hosts: hosts, hoHits: hits}
}

func (f *retiredFixture) do(method, path string, body any) *testResponse {
	switch method {
	case http.MethodPost:
		if body == nil {
			return f.api.Post(path)
		}
		return f.api.Post(path, body)
	case http.MethodDelete:
		return f.api.Delete(path)
	default:
		return f.api.Get(path)
	}
}

// Every route that goes from a device to its host answers 404 for a deleting
// or deleted device, before any host is looked up or called.
func TestRetiredDeviceIsNotFoundOnEveryHostRoute(t *testing.T) {
	t.Parallel()

	routes := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"signaling connect", http.MethodPost, "/instances/inst/signaling/connect", nil},
		{"signaling forward", http.MethodPost, "/signaling/conn-1", map[string]any{"type": "offer"}},
		{"signaling poll", http.MethodGet, "/signaling/conn-1", nil},
		{"signaling websocket", http.MethodGet, "/instances/inst/signaling/ws", nil},
		{"adb websocket", http.MethodGet, "/instances/inst/adb", nil},
		{"ice config", http.MethodGet, "/ice-config", nil},
		{"list displays", http.MethodGet, "/instances/inst/displays", nil},
		{"add display", http.MethodPost, "/instances/inst/displays", map[string]any{"width": 720, "height": 1280}},
		{"remove display", http.MethodDelete, "/instances/inst/displays/1", nil},
		{"screenshot", http.MethodGet, "/instances/inst/displays/0/screenshot", nil},
		{"list recordings", http.MethodGet, "/instances/inst/recordings", nil},
		{"fetch recording", http.MethodPost, "/instances/inst/recordings/rec.webm/fetch", nil},
		{"start recording", http.MethodPost, "/instances/inst/recordings/start", nil},
		{"stop recording", http.MethodPost, "/instances/inst/recordings/stop", nil},
		{"bugreport", http.MethodPost, "/bugreport", map[string]any{}},
		{"restart", http.MethodPost, "/instances/inst/restart", nil},
		{"powerwash", http.MethodPost, "/instances/inst/powerwash", nil},
		{"power button", http.MethodPost, "/instances/inst/powerbtn", nil},
		{"take snapshot", http.MethodPost, "/snapshots", nil},
		{"restore snapshot", http.MethodPost, "/restore", map[string]any{"snapshot_id": "5f0f0f0f-1111-4222-8333-666666666666"}},
		{"start", http.MethodPost, "/start", nil},
		{"stop", http.MethodPost, "/stop", nil},
	}
	for _, status := range []device.Status{device.StatusDeleted, device.StatusDeleting} {
		for _, rt := range routes {
			t.Run(string(status)+"/"+rt.name, func(t *testing.T) {
				t.Parallel()
				f := newRetiredFixture(t, status)

				resp := f.do(rt.method, "/v2/devices/"+retiredDeviceID+rt.path, rt.body)

				assert.Equal(t, http.StatusNotFound, resp.Code, "body: %s", resp.Body.String())
				assert.Zero(t, f.hosts.gets.Load(), "host lookups")
				assert.Zero(t, f.hoHits.Load(), "Host Orchestrator requests")
			})
		}
	}
}

// A stopped device is live, so a running-only verb still refuses it with the
// state conflict, and still before the host is asked.
func TestStoppedDeviceStillConflictsOnARunningOnlyVerb(t *testing.T) {
	t.Parallel()
	f := newRetiredFixture(t, device.StatusStopped)

	resp := f.api.Post("/v2/devices/" + retiredDeviceID + "/instances/inst/powerbtn")

	assert.Equal(t, http.StatusConflict, resp.Code, "body: %s", resp.Body.String())
	assert.Zero(t, f.hosts.gets.Load(), "host lookups")
	assert.Zero(t, f.hoHits.Load(), "Host Orchestrator requests")
}

// The device read keeps serving a deleted row: its tombstone stays visible.
func TestGetDeviceStillReturnsADeletedRow(t *testing.T) {
	t.Parallel()
	f := newRetiredFixture(t, device.StatusDeleted)

	resp := f.api.Get("/v2/devices/" + retiredDeviceID)

	require.Equal(t, http.StatusOK, resp.Code, "body: %s", resp.Body.String())
	var got device.Device
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &got))
	assert.Equal(t, retiredDeviceID, got.ID)
	assert.Equal(t, device.StatusDeleted, got.Status)
}
