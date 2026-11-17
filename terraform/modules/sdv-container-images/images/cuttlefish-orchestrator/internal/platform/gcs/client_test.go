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

package gcs

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"

	storagedomain "cloud-cuttlefish-orchestrator/internal/domain/storage"
)

func TestMapGCSError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want error // nil = unclassified (stays a 500)
	}{
		{"object not exist", storage.ErrObjectNotExist, storagedomain.ErrNotFound},
		{"bucket not exist", storage.ErrBucketNotExist, storagedomain.ErrNotFound},
		{"wrapped object not exist", fmt.Errorf("attrs: %w", storage.ErrObjectNotExist), storagedomain.ErrNotFound},
		{"googleapi 404", &googleapi.Error{Code: http.StatusNotFound}, storagedomain.ErrNotFound},
		{"googleapi 403", &googleapi.Error{Code: http.StatusForbidden}, storagedomain.ErrPermissionDenied},
		{"googleapi 500 unclassified", &googleapi.Error{Code: http.StatusInternalServerError}, nil},
		{"plain error unclassified", errors.New("connection reset"), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mapGCSError(tt.err, "gs://bucket/object")
			if tt.want != nil {
				if !errors.Is(got, tt.want) {
					t.Errorf("mapGCSError = %v, want %v in chain", got, tt.want)
				}
				return
			}
			if errors.Is(got, storagedomain.ErrNotFound) || errors.Is(got, storagedomain.ErrPermissionDenied) {
				t.Errorf("mapGCSError = %v, want unclassified", got)
			}
			// The resource context must survive so the 500 is diagnosable.
			if got.Error() == tt.err.Error() {
				t.Errorf("mapGCSError left the error unwrapped: %v", got)
			}
		})
	}
}

// fakeGCS serves the one JSON-API call a bare-bucket VerifyAccess makes, an
// objects listing, answering for test-bucket and denying everything else.
func fakeGCS(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/b/test-bucket/o") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"kind":"storage#objects","items":[{"name":"obj-1","bucket":"test-bucket"}]}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"code":403,"message":"denied"}}`))
	}))
}

func TestVerifyAccess(t *testing.T) {
	srv := fakeGCS(t)
	t.Cleanup(srv.Close)

	// The emulator env var makes the SDK client talk plain unauthenticated
	// HTTP to the fake (t.Setenv, so no t.Parallel here).
	t.Setenv("STORAGE_EMULATOR_HOST", strings.TrimPrefix(srv.URL, "http://"))
	sdk, err := storage.NewClient(t.Context())
	if err != nil {
		t.Fatalf("storage.NewClient: %v", err)
	}
	t.Cleanup(func() { sdk.Close() })

	client := NewClient(sdk, "test-project")

	resp := client.VerifyAccess(t.Context(), storagedomain.VerifyAccessRequest{Bucket: "test-bucket"})
	if !resp.CanProceed {
		t.Error("Expected CanProceed to be true")
	}
	if resp.GSURI != "gs://test-bucket/" {
		t.Errorf("Expected GSURI gs://test-bucket/, got %s", resp.GSURI)
	}
	if resp.OrchestratorAccess == nil || !resp.OrchestratorAccess.HasAccess {
		t.Error("Expected orchestrator access to be verified against the listing probe")
	}

	denied := client.VerifyAccess(t.Context(), storagedomain.VerifyAccessRequest{Bucket: "denied-bucket"})
	if denied.CanProceed {
		t.Error("Expected CanProceed to be false for a denied bucket")
	}
}
