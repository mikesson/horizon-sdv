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
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/platform/ice"
	"cloud-cuttlefish-orchestrator/internal/platform/turn"
)

// Path IDs are declared format:"uuid", so the transport rejects anything else
// before the handler runs; these are well-formed but arbitrary.
const (
	displayDeviceID = "5f0f0f0f-1111-4222-8333-444444444444"
	absentDeviceID  = "5f0f0f0f-1111-4222-8333-999999999999"
)

// testICEComposer stands in for a relay deployment: relays on both addresses,
// clients pinned to relayed transport.
func testICEComposer() *ice.Composer {
	return ice.NewComposer(turn.NewCredentialService("test-secret", time.Hour), ice.Options{
		STUNServers:       []string{"stun:stun.example:19302"},
		ClientServers:     []string{"turn:turn.example.com:3478"},
		DeviceServers:     []string{"turn:10.0.0.7:3478"},
		PinClientsToRelay: true,
	})
}

// displayTestAPI wires a handler over a real service with fake repositories
// and a real ICE composer; only paths that fail before reaching a Host
// Orchestrator are exercised here.
func displayTestAPI(t *testing.T, seed ...*device.Device) *testAPI {
	t.Helper()
	api := newTestAPI(t)
	deviceRepo := newDeviceRepoFake()
	for _, d := range seed {
		deviceRepo.Devices[d.ID] = d
	}
	svc, err := NewDeviceService(deviceRepo, hosttest.NewFakeRepo(), newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
	require.NoError(t, err)
	handler := NewDeviceHandler(svc, testICEComposer(), nil, nil)
	handler.Register(api)
	return api
}

func TestListDisplaysDeviceNotFound(t *testing.T) {
	api := displayTestAPI(t)

	resp := api.Get("/v2/devices/" + absentDeviceID + "/instances/inst/displays")

	assert.Equal(t, http.StatusNotFound, resp.Code)
}

func TestAddDisplayUnknownPreset(t *testing.T) {
	api := displayTestAPI(t)

	resp := api.Post("/v2/devices/"+displayDeviceID+"/instances/inst/displays", map[string]any{"preset": "nope"})

	assert.Equal(t, http.StatusBadRequest, resp.Code)
}

func TestAddDisplayMissingDimensions(t *testing.T) {
	api := displayTestAPI(t)

	resp := api.Post("/v2/devices/"+displayDeviceID+"/instances/inst/displays", map[string]any{"dpi": 320})

	assert.Equal(t, http.StatusBadRequest, resp.Code)
}

func TestAddDisplaySchemaValidation(t *testing.T) {
	api := displayTestAPI(t)

	// Schema violation: width below minimum.
	resp := api.Post("/v2/devices/"+displayDeviceID+"/instances/inst/displays", map[string]any{"width": -1, "height": 1280})
	assert.Equal(t, http.StatusUnprocessableEntity, resp.Code)

	// Unknown fields are rejected and never silently dropped. The camelCase
	// spelling of refresh_rate_hz must fail loudly instead of losing data.
	resp = api.Post("/v2/devices/"+displayDeviceID+"/instances/inst/displays", map[string]any{
		"width": 720, "height": 1280, "refreshRateHz": 60,
	})
	assert.Equal(t, http.StatusUnprocessableEntity, resp.Code)
}

func TestRemoveDisplayInvalidNumber(t *testing.T) {
	api := displayTestAPI(t)

	resp := api.Delete("/v2/devices/" + displayDeviceID + "/instances/inst/displays/not-a-number")

	assert.Equal(t, http.StatusUnprocessableEntity, resp.Code)
}

func TestRemoveDisplayDeviceNotFound(t *testing.T) {
	api := displayTestAPI(t)

	resp := api.Delete("/v2/devices/" + absentDeviceID + "/instances/inst/displays/1")

	assert.Equal(t, http.StatusNotFound, resp.Code)
}

// The wire the console and cvdr both read: two legs on their own addresses,
// one credential, and the policy. The URIs are asserted per leg because
// serving the client leg to the device never establishes a connection.
func TestGetICEConfig(t *testing.T) {
	api := displayTestAPI(t, &device.Device{ID: displayDeviceID})

	resp := api.Get("/v2/devices/" + displayDeviceID + "/ice-config")

	assert.Equal(t, http.StatusOK, resp.Code)
	var cfg struct {
		ClientServers []struct {
			URLs       []string `json:"urls"`
			Username   string   `json:"username"`
			Credential string   `json:"credential"`
		} `json:"client_servers"`
		DeviceServers []struct {
			URLs     []string `json:"urls"`
			Username string   `json:"username"`
		} `json:"device_servers"`
		Policy string `json:"ice_transport_policy"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &cfg))

	require.Len(t, cfg.ClientServers, 1, "a pinned client is offered no STUN")
	require.Len(t, cfg.DeviceServers, 1)
	assert.Equal(t, []string{"turn:turn.example.com:3478"}, cfg.ClientServers[0].URLs)
	assert.Equal(t, []string{"turn:10.0.0.7:3478"}, cfg.DeviceServers[0].URLs)
	assert.Equal(t, "relay", cfg.Policy)

	// Both legs present one shared credential. Its username embeds the
	// authenticated caller and the device.
	assert.NotEmpty(t, cfg.ClientServers[0].Credential)
	assert.Equal(t, cfg.ClientServers[0].Username, cfg.DeviceServers[0].Username)
	assert.Contains(t, cfg.ClientServers[0].Username, "test@example.com")
	assert.Contains(t, cfg.ClientServers[0].Username, displayDeviceID)
}

func TestGetICEConfigDeviceNotFound(t *testing.T) {
	api := displayTestAPI(t)

	resp := api.Get("/v2/devices/" + absentDeviceID + "/ice-config")

	assert.Equal(t, http.StatusNotFound, resp.Code)
}

// An unreachable Host Orchestrator surfaces as the screenshot op's declared
// 502 with an RFC 9457 body. The client wraps connection failures in
// *HostError at its exit boundary and MapError converts the status carrier
// to problem details, never a masked 500 and never a raw struct dump.
func TestGetScreenshotUnreachableHostIs502(t *testing.T) {
	api := newTestAPI(t)
	deviceRepo := newDeviceRepoFake()
	deviceRepo.Devices[displayDeviceID] = &device.Device{
		ID: displayDeviceID, HostID: "h1", CVDID: "cvd/1",
		Instances: []device.Instance{{Name: "1"}},
	}
	hostRepo := hosttest.NewFakeRepo(&host.Host{ID: "h1", InternalIP: "127.0.0.1", HostOrchestratorPort: 1}) // Nothing listens on port 1.

	cfg := hoclient.DefaultClientConfig()
	cfg.MaxRetries = 0 // One refused dial is the whole signal.
	cfg.FallbackPorts = nil
	svc, err := NewDeviceService(deviceRepo, hostRepo, newMockBuildRepo(),
		hoclient.NewFactory(cfg), slog.Default())
	require.NoError(t, err)
	handler := NewDeviceHandler(svc, testICEComposer(), nil, nil)
	handler.Register(api)

	resp := api.Get("/v2/devices/" + displayDeviceID + "/instances/1/displays/0/screenshot")

	assert.Equal(t, http.StatusBadGateway, resp.Code)
	var problem struct {
		Title  string `json:"title"`
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &problem))
	assert.Equal(t, http.StatusBadGateway, problem.Status)
	assert.Equal(t, "Bad Gateway", problem.Title)
	assert.Contains(t, problem.Detail, "host orchestrator unreachable")
	assert.NotContains(t, resp.Body.String(), "StatusCode", "raw *HostError struct leaked to the wire")
}

// instanceHO fakes the Host Orchestrator's screenshot surface (the async
// operation and its :wait result) and records every path it was asked for.
//
// The image bytes are derived from the instance name in the path, so a fake
// that ignored its arguments could not produce the bytes asserted on. The
// payload and the address it was fetched from cannot drift apart here.
type instanceHO struct {
	*httptest.Server

	mu    sync.Mutex
	paths []string
}

func newInstanceHO(t *testing.T) *instanceHO {
	t.Helper()

	ho := &instanceHO{}
	ho.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ho.mu.Lock()
		ho.paths = append(ho.paths, r.URL.Path)
		ho.mu.Unlock()

		segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		// /cvds/{group}/{name}/displays/{n}/:screenshot. The image lives in
		// the operation's result, never in this response.
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/:screenshot"):
			json.NewEncoder(w).Encode(map[string]any{"name": "op-" + segments[2]})
		// /operations/{name}/:wait
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/:wait"):
			instance := strings.TrimPrefix(segments[1], "op-")
			json.NewEncoder(w).Encode(map[string]any{
				"screenshot_bytes":     base64.StdEncoding.EncodeToString([]byte(instance + "-pixels")),
				"screenshot_mime_type": "image/png",
			})
		default:
			t.Errorf("unexpected HO request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ho.Close)
	return ho
}

// requests returns the paths the host was asked for, in order.
func (h *instanceHO) requests() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.paths)
}

// instanceScreenshotAPI wires the device API over the fake host, holding a
// device whose CVD group carries two named instances.
func instanceScreenshotAPI(t *testing.T, ho *instanceHO) *testAPI {
	t.Helper()

	hoURL, err := url.Parse(ho.URL)
	require.NoError(t, err)
	port, err := strconv.ParseInt(hoURL.Port(), 10, 64)
	require.NoError(t, err)

	deviceRepo := newDeviceRepoFake()
	deviceRepo.Devices[displayDeviceID] = &device.Device{
		ID: displayDeviceID, HostID: "h1", Status: device.StatusRunning,
		CVDID:     "cvdgroup/alpha",
		Instances: []device.Instance{{Name: "alpha"}, {Name: "beta"}},
	}
	hostRepo := hosttest.NewFakeRepo(&host.Host{
		ID: "h1", InternalIP: hoURL.Hostname(), HostOrchestratorPort: port,
	})
	svc, err := NewDeviceService(deviceRepo, hostRepo, newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
	require.NoError(t, err)

	api := newTestAPI(t)
	NewDeviceHandler(svc, testICEComposer(), nil, nil).Register(api)
	return api
}

// The host names instances across its whole fleet, so a capture is only the
// caller's if it was addressed by the instance they named. Instance 0 is what
// the device-scoped route already serves. Serving it here would leave a dead
// instance 5 as invisible as it is today.
func TestGetInstanceScreenshotAddressesTheNamedInstance(t *testing.T) {
	ho := newInstanceHO(t)
	api := instanceScreenshotAPI(t, ho)

	resp := api.Get("/v2/devices/" + displayDeviceID + "/instances/beta/displays/1/screenshot")

	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	var body struct {
		ScreenshotBytes string `json:"screenshot_bytes"`
		MimeType        string `json:"screenshot_mime_type"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
	image, err := base64.StdEncoding.DecodeString(body.ScreenshotBytes)
	require.NoError(t, err)
	assert.Equal(t, "beta-pixels", string(image))
	assert.Equal(t, "image/png", body.MimeType)

	requests := ho.requests()
	require.NotEmpty(t, requests)
	assert.Equal(t, "/cvds/cvdgroup/beta/displays/1/:screenshot", requests[0])
}

// A name the device does not carry is answered from the persisted instances.
// Forwarding it would ask the host for a CVD that is either absent or another
// device's, and both answers are worse than the 404.
func TestGetInstanceScreenshotUnknownInstanceIs404WithoutAHostCall(t *testing.T) {
	ho := newInstanceHO(t)
	api := instanceScreenshotAPI(t, ho)

	resp := api.Get("/v2/devices/" + displayDeviceID + "/instances/gamma/displays/0/screenshot")

	assert.Equal(t, http.StatusNotFound, resp.Code)
	assert.Empty(t, ho.requests(), "an unresolved instance name reached the host")
}
