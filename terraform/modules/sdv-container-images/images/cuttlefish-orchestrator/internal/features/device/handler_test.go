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
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// testPrincipal returns a test principal for authenticated requests.
func testPrincipal() *auth.Principal {
	return &auth.Principal{
		Email: "test@example.com",
	}
}

// testAPI wraps a chi router with Huma API and provides test helper methods.
type testAPI struct {
	huma.API
	router chi.Router
	t      *testing.T
}

// newTestAPI creates a test API with authentication middleware that injects
// a test principal into the context. This simulates authenticated requests.
func newTestAPI(t *testing.T) *testAPI {
	t.Helper()
	r := chi.NewRouter()

	// Add middleware to inject test principal into context
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.ContextWithPrincipal(req.Context(), testPrincipal())
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})

	config := huma.DefaultConfig("Test API", "1.0.0")
	return &testAPI{
		API:    humachi.New(r, config),
		router: r,
		t:      t,
	}
}

// testResponse wraps httptest.ResponseRecorder for test assertions.
type testResponse struct {
	*httptest.ResponseRecorder
}

// Get performs a GET request and returns the response.
func (a *testAPI) Get(path string) *testResponse {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	return &testResponse{rec}
}

// Post performs a POST request with JSON body and returns the response.
func (a *testAPI) Post(path string, body ...any) *testResponse {
	a.t.Helper()
	var reqBody io.Reader
	if len(body) > 0 {
		jsonBytes, err := json.Marshal(body[0])
		require.NoError(a.t, err)
		reqBody = bytes.NewReader(jsonBytes)
	}
	req := httptest.NewRequest(http.MethodPost, path, reqBody)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	return &testResponse{rec}
}

// Delete performs a DELETE request and returns the response.
func (a *testAPI) Delete(path string) *testResponse {
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	return &testResponse{rec}
}

// handlerFixture bundles a registered test API with the fakes behind its
// real DeviceService, so transport tests can assert what actually reached
// the persistence layer (LastListFilter, Devices, enqueued).
type handlerFixture struct {
	api        *testAPI
	deviceRepo *atomicDeviceRepo
	hostRepo   *hosttest.FakeRepo
	enq        *fakeEnqueuer
}

// newHandlerAPI registers a handler over a real DeviceService built on the
// package fakes, so transport tests exercise the same wiring main uses.
func newHandlerAPI(t *testing.T) *handlerFixture {
	t.Helper()
	hostRepo := hosttest.NewFakeRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	enq := &fakeEnqueuer{}
	svc, err := NewDeviceService(deviceRepo, hostRepo, newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithTaskEnqueuer(enq))
	require.NoError(t, err)

	api := newTestAPI(t)
	NewDeviceHandler(svc, nil, nil, nil).Register(api.API)
	return &handlerFixture{api: api, deviceRepo: deviceRepo, hostRepo: hostRepo, enq: enq}
}

func TestDeviceHandler_ListDevices(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		f := newHandlerAPI(t)
		api, deviceRepo := f.api, f.deviceRepo
		deviceRepo.Devices["dev-1"] = &device.Device{
			ID:     "dev-1",
			Name:   "device-1",
			Status: device.StatusRunning,
		}

		resp := api.Get("/v2/devices")
		assert.Equal(t, 200, resp.Code)
		// Response includes $schema from Huma, so check key fields
		body := resp.Body.String()
		assert.Contains(t, body, `"id":"dev-1"`)
		assert.Contains(t, body, `"name":"device-1"`)
		assert.Contains(t, body, `"status":"running"`)
		assert.Contains(t, body, `"limit":20`)
		require.NotNil(t, deviceRepo.LastListFilter)
		// One row past the limit tells the handler whether a next page exists.
		assert.Equal(t, device.ListFilter{Limit: 21, Offset: 0}, *deviceRepo.LastListFilter)
		assert.Contains(t, body, `"next_page_token":""`)
	})

	t.Run("a full page carries a token that resumes after its last row", func(t *testing.T) {
		f := newHandlerAPI(t)
		api, deviceRepo := f.api, f.deviceRepo
		created := time.Date(2026, 9, 1, 12, 0, 0, 123456000, time.UTC)
		for _, id := range []string{"dev-a", "dev-b"} {
			deviceRepo.Devices[id] = &device.Device{ID: id, Name: id, Status: device.StatusRunning, CreatedAt: created}
		}

		resp := api.Get("/v2/devices?limit=1")
		require.Equal(t, 200, resp.Code)
		var page struct {
			Items         []device.Device `json:"items"`
			NextPageToken string          `json:"next_page_token"`
		}
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &page))
		require.Len(t, page.Items, 1)
		require.NotEmpty(t, page.NextPageToken)

		resp = api.Get("/v2/devices?limit=1&page_token=" + page.NextPageToken)
		require.Equal(t, 200, resp.Code)
		got := deviceRepo.LastListFilter
		require.NotNil(t, got)
		assert.Equal(t, page.Items[0].ID, got.AfterID)
		assert.True(t, created.Equal(got.AfterCreatedAt), "cursor time = %v, want %v", got.AfterCreatedAt, created)
	})

	t.Run("page_token with an offset is rejected", func(t *testing.T) {
		f := newHandlerAPI(t)
		api, deviceRepo := f.api, f.deviceRepo
		token := web.EncodePageToken(web.PageCursor{CreatedAt: time.Now(), ID: "dev-1"})

		resp := api.Get("/v2/devices?offset=20&page_token=" + token)
		assert.Equal(t, 400, resp.Code)
		assert.Nil(t, deviceRepo.LastListFilter, "the rejection must precede any repository read")
	})

	t.Run("a malformed page_token is rejected", func(t *testing.T) {
		f := newHandlerAPI(t)
		api, deviceRepo := f.api, f.deviceRepo

		resp := api.Get("/v2/devices?page_token=not-a-token")
		assert.Equal(t, 400, resp.Code)
		assert.Nil(t, deviceRepo.LastListFilter, "the rejection must precede any repository read")
	})

	t.Run("with filters", func(t *testing.T) {
		f := newHandlerAPI(t)
		api, deviceRepo := f.api, f.deviceRepo

		resp := api.Get("/v2/devices?host_id=host-1&status=running")
		assert.Equal(t, 200, resp.Code)

		got := deviceRepo.LastListFilter
		require.NotNil(t, got)
		require.NotNil(t, got.HostID)
		assert.Equal(t, "host-1", *got.HostID)
		require.NotNil(t, got.Status)
		assert.Equal(t, device.StatusRunning, *got.Status)
	})

	t.Run("name reaches the repository filter", func(t *testing.T) {
		f := newHandlerAPI(t)
		api, deviceRepo := f.api, f.deviceRepo

		resp := api.Get("/v2/devices?name=ci-001")
		assert.Equal(t, 200, resp.Code)

		got := deviceRepo.LastListFilter
		require.NotNil(t, got)
		require.NotNil(t, got.Name)
		assert.Equal(t, "ci-001", *got.Name)
		assert.True(t, got.Strong, "name-filtered list must use strong read for read-after-write consistency")
	})

	t.Run("unfiltered list keeps default stale-read setting", func(t *testing.T) {
		f := newHandlerAPI(t)
		api, deviceRepo := f.api, f.deviceRepo

		resp := api.Get("/v2/devices")
		assert.Equal(t, 200, resp.Code)

		got := deviceRepo.LastListFilter
		require.NotNil(t, got)
		assert.Nil(t, got.Name)
		assert.False(t, got.Strong)
	})

	t.Run("mine resolves to the caller", func(t *testing.T) {
		f := newHandlerAPI(t)
		api, deviceRepo := f.api, f.deviceRepo

		resp := api.Get("/v2/devices?mine=true")
		assert.Equal(t, 200, resp.Code)

		got := deviceRepo.LastListFilter
		require.NotNil(t, got)
		require.NotNil(t, got.CreatedBy)
		assert.Equal(t, "test@example.com", *got.CreatedBy)
	})

	t.Run("created_by filters by the given email", func(t *testing.T) {
		f := newHandlerAPI(t)
		api, deviceRepo := f.api, f.deviceRepo

		resp := api.Get("/v2/devices?created_by=other@example.com")
		assert.Equal(t, 200, resp.Code)

		got := deviceRepo.LastListFilter
		require.NotNil(t, got)
		require.NotNil(t, got.CreatedBy)
		assert.Equal(t, "other@example.com", *got.CreatedBy)
	})

	t.Run("mine and created_by are mutually exclusive", func(t *testing.T) {
		f := newHandlerAPI(t)
		api, deviceRepo := f.api, f.deviceRepo

		resp := api.Get("/v2/devices?mine=true&created_by=other@example.com")
		assert.Equal(t, 400, resp.Code)
		assert.Nil(t, deviceRepo.LastListFilter, "the rejection must precede any repository read")
	})
}

func TestDeviceHandler_CreateDevice(t *testing.T) {
	t.Parallel()
	hostID := "123e4567-e89b-12d3-a456-426614174000"

	t.Run("success", func(t *testing.T) {
		f := newHandlerAPI(t)
		api, deviceRepo, hostRepo, enq := f.api, f.deviceRepo, f.hostRepo, f.enq
		hostRepo.Hosts[hostID] = &host.Host{ID: hostID, Status: host.StatusRunning, MaxDevices: 4}

		resp := api.Post("/v2/devices", map[string]any{
			"host_id":  hostID,
			"name":     "my-device",
			"build_id": "12345678",
		})

		assert.Equal(t, 200, resp.Code)
		body := resp.Body.String()
		assert.Contains(t, body, `"name":"my-device"`)
		assert.Contains(t, body, `"status":"pending"`)

		require.Len(t, deviceRepo.Devices, 1)
		for _, d := range deviceRepo.Devices {
			assert.Equal(t, "12345678", d.BuildID)
			require.NotNil(t, d.CreatedBy)
			assert.Equal(t, "test@example.com", *d.CreatedBy, "created_by comes from the authenticated principal")
		}
		require.Len(t, enq.enqueued, 1, "a create enqueues exactly one CVD-creation task")
		assert.Equal(t, TaskTypeCreateCVD, enq.enqueued[0].Type)
	})

	// Simple mode with no build would reach `cvd load` with no default_build
	// and fail opaquely; the handler rejects it up front.
	t.Run("simple mode without a build is rejected", func(t *testing.T) {
		f := newHandlerAPI(t)
		api, deviceRepo, hostRepo, enq := f.api, f.deviceRepo, f.hostRepo, f.enq
		hostRepo.Hosts[hostID] = &host.Host{ID: hostID, Status: host.StatusRunning, MaxDevices: 4}

		resp := api.Post("/v2/devices", map[string]any{
			"host_id": hostID,
			"name":    "no-build",
		})

		assert.Equal(t, 400, resp.Code)
		assert.Contains(t, resp.Body.String(), "a build is required")
		assert.Empty(t, deviceRepo.Devices, "the rejection must precede the create")
		assert.Empty(t, enq.enqueued)
	})

	t.Run("validate_only fails the same way without a build", func(t *testing.T) {
		f := newHandlerAPI(t)
		api, deviceRepo, hostRepo := f.api, f.deviceRepo, f.hostRepo
		hostRepo.Hosts[hostID] = &host.Host{ID: hostID, Status: host.StatusRunning, MaxDevices: 4}

		resp := api.Post("/v2/devices", map[string]any{
			"host_id":       hostID,
			"name":          "no-build-plan",
			"validate_only": true,
		})

		assert.Equal(t, 400, resp.Code)
		assert.Empty(t, deviceRepo.Devices)
	})
}

// lifecycleHO fakes the HO CVD lifecycle surface: :stop and :start return an
// operation which the follow-up wait completes.
func lifecycleHO(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/operations/"):
			w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, ":stop") || strings.HasSuffix(r.URL.Path, ":start")):
			w.Write([]byte(`{"name": "op-1"}`))
		default:
			t.Errorf("unexpected HO request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestDeviceHandler_Actions(t *testing.T) {
	t.Parallel()
	deviceID := "123e4567-e89b-12d3-a456-426614174000"

	ho := lifecycleHO(t)
	t.Cleanup(ho.Close)
	hoURL, err := url.Parse(ho.URL)
	require.NoError(t, err)
	port, err := strconv.ParseInt(hoURL.Port(), 10, 64)
	require.NoError(t, err)

	seed := func(t *testing.T, status device.Status) (*testAPI, *atomicDeviceRepo, *fakeEnqueuer) {
		t.Helper()
		f := newHandlerAPI(t)
		api, deviceRepo, hostRepo, enq := f.api, f.deviceRepo, f.hostRepo, f.enq
		hostRepo.Hosts["h1"] = &host.Host{
			ID: "h1", Status: host.StatusRunning, MaxDevices: 4,
			IPAddress: "127.0.0.1", HostOrchestratorPort: port,
		}
		deviceRepo.Devices[deviceID] = &device.Device{
			ID: deviceID, Name: "test-device", HostID: "h1", Status: status,
			CVDID: "cvd-grp/test-device",
		}
		return api, deviceRepo, enq
	}

	t.Run("stop device", func(t *testing.T) {
		api, deviceRepo, _ := seed(t, device.StatusRunning)

		resp := api.Post("/v2/devices/" + deviceID + "/stop")
		assert.Equal(t, 200, resp.Code)
		assert.Contains(t, resp.Body.String(), `"status":"stopping"`)
		assert.Equal(t, device.StatusStopped, deviceRepo.Devices[deviceID].Status,
			"the committed transition, not the transport echo, is the source of truth")
	})

	t.Run("start device", func(t *testing.T) {
		api, deviceRepo, _ := seed(t, device.StatusStopped)

		resp := api.Post("/v2/devices/" + deviceID + "/start")
		assert.Equal(t, 200, resp.Code)
		assert.Contains(t, resp.Body.String(), `"status":"starting"`)
		assert.Equal(t, device.StatusRunning, deviceRepo.Devices[deviceID].Status)
	})

	t.Run("delete device", func(t *testing.T) {
		api, deviceRepo, enq := seed(t, device.StatusRunning)

		resp := api.Delete("/v2/devices/" + deviceID)
		assert.Equal(t, 204, resp.Code)
		assert.Equal(t, device.StatusDeleted, deviceRepo.Devices[deviceID].Status)
		require.Len(t, enq.enqueued, 1, "a delete enqueues durable CVD cleanup")
		assert.Equal(t, TaskTypeCleanupCVD, enq.enqueued[0].Type)
	})
}

// Advanced mode with no device-level build needs a build on EVERY instance:
// one instance's build does not fill another's disk, so the unbuilt member
// would reach `cvd load` with nothing to boot.
func TestDeviceHandler_CreateDevice_InstanceBuilds(t *testing.T) {
	t.Parallel()
	hostID := "123e4567-e89b-12d3-a456-426614174000"
	advanced := func(instances ...map[string]any) map[string]any {
		raw := make([]any, len(instances))
		for i, in := range instances {
			raw[i] = in
		}
		return map[string]any{
			"host_id":         hostID,
			"name":            "sdv-pair",
			"mode":            "advanced",
			"advanced_config": map[string]any{"env_config": map[string]any{"instances": raw}},
		}
	}

	t.Run("a pair with one unbuilt instance is rejected", func(t *testing.T) {
		t.Parallel()
		f := newHandlerAPI(t)
		f.hostRepo.Hosts[hostID] = &host.Host{ID: hostID, Status: host.StatusRunning, MaxDevices: 4}

		resp := f.api.Post("/v2/devices", advanced(
			map[string]any{"name": "core", "disk": map[string]any{"default_build": "gs://b/core.zip"}},
			map[string]any{"name": "ivi"},
		))
		assert.Equal(t, 400, resp.Code)
		assert.Contains(t, resp.Body.String(), "give every instance")
		assert.Empty(t, f.deviceRepo.Devices)
	})

	t.Run("an unregistered @build is a 400 naming the instance and id", func(t *testing.T) {
		t.Parallel()
		f := newHandlerAPI(t)
		f.hostRepo.Hosts[hostID] = &host.Host{ID: hostID, Status: host.StatusRunning, MaxDevices: 4}

		resp := f.api.Post("/v2/devices", advanced(
			map[string]any{"name": "core", "disk": map[string]any{"default_build": "gs://b/core.zip"}},
			map[string]any{"name": "ivi", "@build": "no-such-build"},
		))
		assert.Equal(t, 400, resp.Code)
		assert.Contains(t, resp.Body.String(), `instances[1].@build: build \"no-such-build\" is not registered`)
		assert.Empty(t, f.deviceRepo.Devices)
		assert.Empty(t, f.enq.enqueued)
	})

	t.Run("validate_only refuses the same unregistered @build", func(t *testing.T) {
		t.Parallel()
		f := newHandlerAPI(t)
		f.hostRepo.Hosts[hostID] = &host.Host{ID: hostID, Status: host.StatusRunning, MaxDevices: 4}

		body := advanced(map[string]any{"name": "core", "@build": "no-such-build"})
		body["validate_only"] = true
		resp := f.api.Post("/v2/devices", body)
		assert.Equal(t, 400, resp.Code)
		assert.Contains(t, resp.Body.String(), "is not registered")
	})

	t.Run("@build beside disk.default_build is rejected", func(t *testing.T) {
		t.Parallel()
		f := newHandlerAPI(t)
		f.hostRepo.Hosts[hostID] = &host.Host{ID: hostID, Status: host.StatusRunning, MaxDevices: 4}

		resp := f.api.Post("/v2/devices", advanced(map[string]any{
			"name": "core", "@build": "b-core",
			"disk": map[string]any{"default_build": "gs://b/core.zip"},
		}))
		assert.Equal(t, 400, resp.Code)
		assert.Contains(t, resp.Body.String(), "not both")
	})
}
