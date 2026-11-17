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

// The slice's first tests. The handler holds the concrete *gcs.Client instead
// of a port, which is the house rule with one consumer, so these drive a real
// SDK client pointed at a fake GCS over STORAGE_EMULATOR_HOST instead of
// injecting a double. That also makes them worth more than a mock would be: the
// status a caller sees is produced by the real googleapi error, mapGCSError and
// web.MapError in series, and a break anywhere along that chain shows up here.
//
// t.Setenv rules out t.Parallel in this file.
//
// One error arm here is left uncovered on purpose: VerifyAccessHuma's, because
// gcs.Client.VerifyAccess has a single `return res, nil` and cannot fail. A
// failed probe is data, not an error (see TestVerifyAccessReportsFailureAsNoAccess).
// The arm still has to exist, since Go will not let the caller ignore the error
// half of the signature, so it is unreachable and cannot be removed. Do not
// contort a test to reach it, and do not delete it.
package storagefeature

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/platform/gcs"
)

// fakeGCS answers every JSON-API and media call with the given status.
func fakeGCS(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("writing fake response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newRouter wires the handler to a chi router carrying both the Huma operations
// and the raw byte-stream routes, against a GCS that fails with status.
func newRouter(t *testing.T, status int, body string) chi.Router {
	t.Helper()
	srv := fakeGCS(t, status, body)
	t.Setenv("STORAGE_EMULATOR_HOST", strings.TrimPrefix(srv.URL, "http://"))

	sdk, err := storage.NewClient(t.Context())
	if err != nil {
		t.Fatalf("storage.NewClient: %v", err)
	}
	t.Cleanup(func() {
		if err := sdk.Close(); err != nil {
			t.Errorf("closing sdk: %v", err)
		}
	})

	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("Test API", "1.0.0"))
	h := NewStorageHandler(gcs.NewClient(sdk, "test-project"))
	h.Register(api)
	h.RegisterRaw(r)
	return r
}

func do(t *testing.T, r chi.Router, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestBackendFailuresSurface covers every read and write path's error arm at
// once. An unclassified GCS failure must reach the client as a 5xx rather than
// being flattened into a 404 or an empty success.
//
// The fake answers 400 instead of 500 on purpose: the storage SDK retries 5xx
// with backoff, so a fake that always 500s makes these tests hang for minutes
// instead of failing. 400 is not retried and lands in the same unclassified arm
// of mapGCSError.
func TestBackendFailuresSurface(t *testing.T) {
	tests := []struct {
		name         string
		method, path string
		body         string
	}{
		{"list buckets", http.MethodGet, "/v2/storage/buckets", ""},
		{"list objects", http.MethodGet, "/v2/storage/buckets/b1/objects", ""},
		{"delete object", http.MethodDelete, "/v2/storage/buckets/b1/objects/obj.img", ""},
		{"get object stream", http.MethodGet, "/v2/storage/buckets/b1/objects/dir/obj.img", ""},
		{"upload object stream", http.MethodPost, "/v2/storage/buckets/b1/objects/dir/obj.img", "payload"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRouter(t, http.StatusBadRequest, `{"error":{"code":400,"message":"backend rejected"}}`)
			rec := do(t, r, tc.method, tc.path, tc.body)
			if rec.Code < 500 {
				t.Errorf("status = %d, want a 5xx to surface the backend failure: %s",
					rec.Code, rec.Body.String())
			}
		})
	}
}

// TestVerifyAccessReportsFailureAsNoAccess pins the one endpoint that does NOT
// fail on a backend failure, and must not.
//
// VerifyAccess is a probe: being unable to read the bucket IS the answer the
// caller asked for, so it returns 200 carrying can_proceed=false and the reason.
// Grouping it with the endpoints above was the obvious mistake, and it would
// have forced a real behavior change to satisfy a test. The assertions here are
// that the failure is reported and not swallowed: a 200 alone would also be
// produced by a probe that reported access it never verified.
func TestVerifyAccessReportsFailureAsNoAccess(t *testing.T) {
	r := newRouter(t, http.StatusBadRequest, `{"error":{"code":400,"message":"backend rejected"}}`)
	rec := do(t, r, http.MethodPost, "/v2/storage/verify-access", `{"bucket":"b1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a failed probe is an answer): %s", rec.Code, rec.Body.String())
	}

	var body struct {
		CanProceed         bool `json:"can_proceed"`
		OrchestratorAccess *struct {
			HasAccess bool   `json:"has_access"`
			Error     string `json:"error"`
		} `json:"orchestrator_access"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.CanProceed {
		t.Error("can_proceed is true after the access probe failed")
	}
	if body.OrchestratorAccess == nil {
		t.Fatal("no orchestrator_access reported")
	}
	if body.OrchestratorAccess.HasAccess {
		t.Error("has_access is true after the access probe failed")
	}
	if body.OrchestratorAccess.Error == "" {
		t.Error("the probe failure was not reported, so the caller cannot tell it apart from a denial")
	}
}

// TestNotFoundIsClassified is the counterfactual to the test above: without it,
// a handler that answered 500 to everything would look correct. A GCS 404 has
// to arrive as a 404, and mapGCSError is what makes it one.
func TestNotFoundIsClassified(t *testing.T) {
	tests := []struct {
		name         string
		method, path string
	}{
		{"delete object", http.MethodDelete, "/v2/storage/buckets/b1/objects/gone.img"},
		{"get object stream", http.MethodGet, "/v2/storage/buckets/b1/objects/dir/gone.img"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRouter(t, http.StatusNotFound, `{"error":{"code":404,"message":"no such object"}}`)
			rec := do(t, r, tc.method, tc.path, "")
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestPermissionDeniedIsClassified pins the other classified code; a 403 that
// arrived as a 500 would send an operator looking at GCS availability instead of
// at the service account's grants.
func TestPermissionDeniedIsClassified(t *testing.T) {
	r := newRouter(t, http.StatusForbidden, `{"error":{"code":403,"message":"denied"}}`)
	rec := do(t, r, http.MethodDelete, "/v2/storage/buckets/b1/objects/obj.img", "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// TestDeleteRejectsDirectoryNames covers the guard that runs before any GCS
// call. The assertion that carries it is that the backend is never reached: a
// "directory" in GCS is a name prefix, so deleting one silently succeeds
// against a real bucket while removing nothing.
func TestDeleteRejectsDirectoryNames(t *testing.T) {
	// The fake would answer 200 to a delete, so reaching it means the guard
	// did not run and the test still sees a success it must not accept.
	r := newRouter(t, http.StatusOK, `{}`)
	rec := do(t, r, http.MethodDelete, "/v2/storage/buckets/b1/objects/dir%2F", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a trailing-slash object name: %s", rec.Code, rec.Body.String())
	}
}

// TestUploadRejectsEmptyName covers the raw handler's own guard.
func TestUploadRejectsEmptyName(t *testing.T) {
	r := newRouter(t, http.StatusOK, `{}`)
	rec := do(t, r, http.MethodPost, "/v2/storage/buckets/b1/objects/", "payload")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an empty object name: %s", rec.Code, rec.Body.String())
	}
}
