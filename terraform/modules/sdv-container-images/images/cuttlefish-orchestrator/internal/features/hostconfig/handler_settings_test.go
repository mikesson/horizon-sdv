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

package hostconfigfeature_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hostconfigfeature "cloud-cuttlefish-orchestrator/internal/features/hostconfig"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// settingsAPI serves the settings handlers over the emulator.
func settingsAPI(t *testing.T) http.Handler {
	t.Helper()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewSystemConfigRepository(db.Client)
	svc := hostconfigfeature.NewService(nil, repo, hostconfigfeature.Images{}, nil, slog.New(slog.DiscardHandler), "")
	r := chi.NewRouter()
	hostconfigfeature.NewHandler(svc).Register(humachi.New(r, huma.DefaultConfig("Test API", "1.0.0")))
	return r
}

func settingsRequest(t *testing.T, h http.Handler, method, path, ifMatch string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&payload).Encode(body))
	}
	req := httptest.NewRequest(method, path, &payload)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A write carrying the tag of an earlier read loses to a write that landed in
// between, and stores nothing.
func TestSettingsETag_StaleIfMatchIsRefused(t *testing.T) {
	t.Parallel()
	api := settingsAPI(t)
	const path = "/v2/admin/system-config/lifetimes"
	policy := map[string]any{"device_ttl_hours": 2, "extend_hours": 1, "extend_count": 3, "host_ttl_hours": 24}

	read := settingsRequest(t, api, http.MethodGet, path, "", nil)
	require.Equal(t, http.StatusOK, read.Code, read.Body.String())
	readTag := read.Header().Get("ETag")
	require.NotEmpty(t, readTag)

	first := settingsRequest(t, api, http.MethodPut, path, readTag, policy)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	writtenTag := first.Header().Get("ETag")
	require.NotEmpty(t, writtenTag)
	assert.NotEqual(t, readTag, writtenTag)

	policy["device_ttl_hours"] = 5
	stale := settingsRequest(t, api, http.MethodPut, path, readTag, policy)
	assert.Equal(t, http.StatusPreconditionFailed, stale.Code, stale.Body.String())

	after := settingsRequest(t, api, http.MethodGet, path, "", nil)
	require.Equal(t, http.StatusOK, after.Code)
	assert.Equal(t, writtenTag, after.Header().Get("ETag"))
	assert.Contains(t, after.Body.String(), `"device_ttl_hours":2`, "the refused write must store nothing")

	// Without If-Match the write is unconditional, which is what the console sends.
	blind := settingsRequest(t, api, http.MethodPut, path, "", policy)
	assert.Equal(t, http.StatusOK, blind.Code, blind.Body.String())
}

// The scalar pairs share the generic registration, so one of them stands for
// the rest.
func TestSettingsETag_ScalarPair(t *testing.T) {
	t.Parallel()
	api := settingsAPI(t)
	const path = "/v2/admin/system-config/zone-pin"

	read := settingsRequest(t, api, http.MethodGet, path, "", nil)
	require.Equal(t, http.StatusOK, read.Code)
	readTag := read.Header().Get("ETag")
	require.NotEmpty(t, readTag)

	ok := settingsRequest(t, api, http.MethodPut, path, readTag, map[string]any{"value": "us-central1-a"})
	require.Equal(t, http.StatusOK, ok.Code, ok.Body.String())
	assert.NotEqual(t, readTag, ok.Header().Get("ETag"))

	stale := settingsRequest(t, api, http.MethodPut, path, readTag, map[string]any{"value": "us-east1-b"})
	assert.Equal(t, http.StatusPreconditionFailed, stale.Code, stale.Body.String())
}

// Every fleet write shares one tag, so a client chaining several writes passes
// each write's returned tag to the next.
func TestSettingsETag_FleetWritesChain(t *testing.T) {
	t.Parallel()
	api := settingsAPI(t)

	read := settingsRequest(t, api, http.MethodGet, "/v2/admin/system-config/fleet", "", nil)
	require.Equal(t, http.StatusOK, read.Code)
	tag := read.Header().Get("ETag")

	tuning := settingsRequest(t, api, http.MethodPut, "/v2/admin/system-config/fleet/tuning", tag,
		map[string]any{"demand_horizon_sec": 600, "drain_grace_sec": 300})
	require.Equal(t, http.StatusOK, tuning.Code, tuning.Body.String())
	next := tuning.Header().Get("ETag")
	require.NotEqual(t, tag, next)

	mode := settingsRequest(t, api, http.MethodPut, "/v2/admin/system-config/fleet/autoscaler", next,
		map[string]any{"mode": "off", "standby": 1, "floor": 0})
	assert.Equal(t, http.StatusOK, mode.Code, mode.Body.String())

	// The first read's tag is stale for every fleet write after the chain.
	stale := settingsRequest(t, api, http.MethodPut, "/v2/admin/system-config/fleet/tuning", tag,
		map[string]any{"demand_horizon_sec": 900, "drain_grace_sec": 300})
	assert.Equal(t, http.StatusPreconditionFailed, stale.Code, stale.Body.String())
}
