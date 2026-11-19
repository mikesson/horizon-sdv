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
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

type recordedHOReq struct {
	method     string
	requestURI string
	body       string
}

type signalingHOFake struct {
	mu          sync.Mutex
	reqs        []recordedHOReq
	notFoundGet bool
}

func (f *signalingHOFake) recorded() []recordedHOReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedHOReq, len(f.reqs))
	copy(out, f.reqs)
	return out
}

func signalingTestRouter(t *testing.T, hoFake *signalingHOFake) (*testAPI, *atomicDeviceRepo) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var bodyBuf bytes.Buffer
		_, _ = bodyBuf.ReadFrom(r.Body)
		hoFake.mu.Lock()
		hoFake.reqs = append(hoFake.reqs, recordedHOReq{
			method:     r.Method,
			requestURI: r.RequestURI,
			body:       bodyBuf.String(),
		})
		notFound := hoFake.notFoundGet
		hoFake.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/polled_connections":
			_, _ = w.Write([]byte(`{"connection_id":"conn-123"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/:forward"):
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/messages"):
			if notFound {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"missing"}`))
				return
			}
			_, _ = w.Write([]byte(`[{"type":"answer","sdp":"v=0"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	hoURL, err := url.Parse(srv.URL)
	require.NoError(t, err)
	port, err := strconv.ParseInt(hoURL.Port(), 10, 64)
	require.NoError(t, err)

	hostRepo := hosttest.NewFakeRepo(&host.Host{
		ID:                   "h1",
		InternalIP:           hoURL.Hostname(),
		HostOrchestratorPort: port,
	})
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	deviceRepo.Devices[displayDeviceID] = &device.Device{
		ID:     displayDeviceID,
		HostID: "h1",
		Status: device.StatusRunning,
		CVDID:  "grp/inst-1",
		Instances: []device.Instance{
			{Name: "inst-1", WebRTCDeviceID: "webrtc-dev-1"},
		},
	}

	hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())
	svc, err := NewDeviceService(deviceRepo, hostRepo, newMockBuildRepo(),
		hoFactory, slog.Default())
	require.NoError(t, err)

	api := newTestAPI(t)
	sigHandler := NewSignalingHandler(deviceRepo, hostRepo, hoFactory, slog.Default())
	h := NewDeviceHandler(svc, testICEComposer(), sigHandler, nil)
	h.Register(api.API)
	h.RegisterRaw(api.router)
	return api, deviceRepo
}

func TestSignalingHandler_CreateForwardGetAndValidation(t *testing.T) {
	t.Parallel()

	t.Run("create_forward_get_success", func(t *testing.T) {
		t.Parallel()
		ho := &signalingHOFake{}
		api, _ := signalingTestRouter(t, ho)

		createResp := api.Post("/v2/devices/" + displayDeviceID + "/instances/inst-1/signaling/connect")
		require.Equal(t, http.StatusOK, createResp.Code, createResp.Body.String())
		assert.Contains(t, createResp.Body.String(), `"connection_id":"conn-123"`)

		fwdResp := api.Post("/v2/devices/"+displayDeviceID+"/signaling/conn-123", map[string]any{"type": "offer"})
		require.Equal(t, http.StatusOK, fwdResp.Code, fwdResp.Body.String())

		getResp := api.Get("/v2/devices/" + displayDeviceID + "/signaling/conn-123?start=4")
		require.Equal(t, http.StatusOK, getResp.Code, getResp.Body.String())
		assert.Contains(t, getResp.Body.String(), `"answer"`)

		reqs := ho.recorded()
		require.Len(t, reqs, 3)
		assert.Equal(t, "/polled_connections", reqs[0].requestURI)
		assert.JSONEq(t, `{"device_id":"webrtc-dev-1"}`, reqs[0].body)
		assert.Equal(t, "/polled_connections/conn-123/:forward", reqs[1].requestURI)
		assert.JSONEq(t, `{"payload":{"type":"offer"}}`, reqs[1].body)
		assert.Equal(t, "/polled_connections/conn-123/messages?start=4", reqs[2].requestURI)
	})

	t.Run("rejects_invalid_conn_ids_before_host_call", func(t *testing.T) {
		t.Parallel()
		badConnIDs := []string{
			"a%23frag",
			"..%2F..%2Fcvds",
			"bad.conn",
			"bad%20space",
		}
		for _, connID := range badConnIDs {
			ho := &signalingHOFake{}
			api, _ := signalingTestRouter(t, ho)

			reqPost := httptest.NewRequest(http.MethodPost, "/v2/devices/"+displayDeviceID+"/signaling/"+connID, strings.NewReader(`{"type":"offer"}`))
			recPost := httptest.NewRecorder()
			api.router.ServeHTTP(recPost, reqPost)
			assert.Equal(t, http.StatusBadRequest, recPost.Code, "POST connID=%s", connID)

			reqGet := httptest.NewRequest(http.MethodGet, "/v2/devices/"+displayDeviceID+"/signaling/"+connID, nil)
			recGet := httptest.NewRecorder()
			api.router.ServeHTTP(recGet, reqGet)
			assert.Equal(t, http.StatusBadRequest, recGet.Code, "GET connID=%s", connID)

			assert.Empty(t, ho.recorded(), "invalid connID=%s must not reach host orchestrator", connID)
		}
	})

	t.Run("rejects_oversized_forward_body", func(t *testing.T) {
		t.Parallel()
		ho := &signalingHOFake{}
		api, _ := signalingTestRouter(t, ho)

		oversized, err := json.Marshal(map[string]string{
			"sdp": strings.Repeat("A", (1<<20)+1024),
		})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/v2/devices/"+displayDeviceID+"/signaling/conn_1", bytes.NewReader(oversized))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		api.router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
		assert.Empty(t, ho.recorded(), "oversized body must not be forwarded to host orchestrator")
	})

	t.Run("get_messages_maps_ho_404", func(t *testing.T) {
		t.Parallel()
		ho := &signalingHOFake{notFoundGet: true}
		api, _ := signalingTestRouter(t, ho)

		resp := api.Get("/v2/devices/" + displayDeviceID + "/signaling/conn-missing")
		assert.Equal(t, http.StatusNotFound, resp.Code)
	})
}
