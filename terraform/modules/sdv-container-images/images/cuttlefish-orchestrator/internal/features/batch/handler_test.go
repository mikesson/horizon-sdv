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

package batchfeature

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// newHandlerAPI wires the batch handler onto a chi+Huma router with a principal
// injected (as the auth middleware does in production). Registering the handler
// here also proves the operation schemas are valid, since a malformed one
// panics.
func newHandlerAPI(t *testing.T) (http.Handler, *fakeDeviceOrch) {
	t.Helper()
	svc, dev, _ := newTestService(100)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.ContextWithPrincipal(req.Context(), &auth.Principal{Email: "ci@example.com"})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	api := humachi.New(r, huma.DefaultConfig("test", "1.0.0"))
	NewHandler(svc).Register(api)
	return r, dev
}

func doJSON(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func headlessBody(count int, validateOnly bool) map[string]any {
	return map[string]any{
		"name_prefix":   "ci",
		"count":         count,
		"requirements":  map[string]any{"interaction": "headless"},
		"validate_only": validateOnly,
	}
}

// The pool a batch names reaches the intent its host template is resolved from,
// which is what decides both the machines it buys and the machines it may pack
// onto. A body key Huma drops, or a mapping that leaves it behind, resolves the
// default pool and reports success.
func TestHandler_RequirementsCarryTheNamedPool(t *testing.T) {
	t.Parallel()
	h, dev := newHandlerAPI(t)
	body := headlessBody(2, true)
	body["requirements"] = map[string]any{"interaction": "headless", "pool": host.PoolGPU}

	rec := doJSON(t, h, http.MethodPost, "/v2/batches", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, host.PoolGPU, dev.hostSpecReq.Pool)
}

// The guest architecture reaches the same intent, and it has to: an arm64 guest
// runs on an arm64 host and nowhere else. Huma rejects a body key no input
// struct declares, so an input that dropped the field answers 422 for an
// unexpected property, which is what cfctl's --architecture met.
func TestHandler_RequirementsCarryTheGuestArchitecture(t *testing.T) {
	t.Parallel()
	h, dev := newHandlerAPI(t)
	body := headlessBody(2, true)
	body["requirements"] = map[string]any{"interaction": "headless", "architecture": "arm"}

	rec := doJSON(t, h, http.MethodPost, "/v2/batches", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, device.ArchitectureARM, dev.hostSpecReq.Architecture)
}

func TestHandler_ValidateOnly(t *testing.T) {
	t.Parallel()
	h, dev := newHandlerAPI(t)
	rec := doJSON(t, h, http.MethodPost, "/v2/batches", headlessBody(10, true))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got BatchSummary
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Empty(t, got.BatchID, "a plan creates no batch")
	assert.Equal(t, 10, got.Admitted)
	assert.Equal(t, 3, got.NewHosts)
	assert.Empty(t, dev.created, "validate_only creates nothing")
}

func TestHandler_CreateGetDelete(t *testing.T) {
	t.Parallel()
	h, dev := newHandlerAPI(t)

	// Create.
	rec := doJSON(t, h, http.MethodPost, "/v2/batches", headlessBody(6, false))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var created BatchSummary
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.NotEmpty(t, created.BatchID)
	assert.Equal(t, "provisioning", created.Status, "derived from the fresh members")
	assert.True(t, created.Reconciling)
	assert.Equal(t, 6, created.Total)
	assert.Len(t, dev.created, 6)

	// Get.
	rec = doJSON(t, h, http.MethodGet, "/v2/batches/"+created.BatchID, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got BatchSummary
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, created.BatchID, got.BatchID)
	assert.Equal(t, 6, got.Total)

	// List: the house items/total shape.
	rec = doJSON(t, h, http.MethodGet, "/v2/batches", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list struct {
		Items []BatchSummary `json:"items"`
		Total int            `json:"total"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Items, 1)
	assert.Equal(t, 1, list.Total)
	assert.Equal(t, created.BatchID, list.Items[0].BatchID)

	// Devices bundle.
	rec = doJSON(t, h, http.MethodGet, "/v2/batches/"+created.BatchID+"/devices", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Delete (204).
	rec = doJSON(t, h, http.MethodDelete, "/v2/batches/"+created.BatchID, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

func TestHandler_ListByCreator(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerAPI(t)

	rec := doJSON(t, h, http.MethodPost, "/v2/batches", headlessBody(2, false))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var list struct {
		Items []BatchSummary `json:"items"`
		Total int            `json:"total"`
	}
	// mine resolves to the injected principal, who created the batch.
	rec = doJSON(t, h, http.MethodGet, "/v2/batches?mine=true", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.Equal(t, 1, list.Total)

	// A foreign created_by matches nothing.
	rec = doJSON(t, h, http.MethodGet, "/v2/batches?created_by=other@example.com", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.Equal(t, 0, list.Total)

	rec = doJSON(t, h, http.MethodGet, "/v2/batches?mine=true&created_by=other@example.com", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandler_GetNotFound(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerAPI(t)
	rec := doJSON(t, h, http.MethodGet, "/v2/batches/nope", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandler_RejectsInvalidCount(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerAPI(t)
	// count 0 fails Huma's minimum:1 schema validation (422).
	rec := doJSON(t, h, http.MethodPost, "/v2/batches", headlessBody(0, false))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

// TestHandler_RejectsAmbiguousBuildSelection: registered and track are mutually
// exclusive, same rule as the device declarative create.
func TestHandler_RejectsAmbiguousBuildSelection(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerAPI(t)
	body := headlessBody(2, false)
	body["requirements"] = map[string]any{
		"interaction": "headless",
		"build": map[string]any{
			"registered": "build-1",
			"track":      map[string]any{"branch": "aosp-main", "target": "cf_x86_64_phone"},
		},
	}
	rec := doJSON(t, h, http.MethodPost, "/v2/batches", body)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}
