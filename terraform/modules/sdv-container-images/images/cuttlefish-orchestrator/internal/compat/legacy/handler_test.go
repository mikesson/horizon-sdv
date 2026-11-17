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

package legacy

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/compat/legacy/coapi"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// TestStatusFromError pins every arm of the /v1 status contract. These are the
// legacy wire answers, and they are not compared against web.MapError. /v2 is
// free to reclassify a family, and this table is what keeps that from reaching
// clients CCO cannot update.
func TestStatusFromError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want int
	}{
		{"app error keeps its own status", web.NewBadRequestError("bad"), http.StatusBadRequest},
		{"catalog validation", &host.CatalogValidationError{
			Errors: []host.CatalogValidationDetail{{Field: "machine_type", Message: "unavailable in zone"}},
		}, http.StatusUnprocessableEntity},
		{"boot disk out of range", &host.BootDiskSizeError{RequestedGB: 5}, http.StatusBadRequest},
		{"quota exceeded", &quota.ExceededError{Requested: 1, MaxHosts: 10}, http.StatusTooManyRequests},
		{"host not found", &host.NotFoundError{ID: "h1"}, http.StatusNotFound},
		{"invalid state", &host.InvalidStateError{Current: "provisioning", Expected: "running"}, http.StatusConflict},
		{"capacity exceeded", &host.CapacityExceededError{HostID: "h1"}, http.StatusServiceUnavailable},
		{"unclassified", errors.New("spanner unavailable"), http.StatusInternalServerError},
		// checkOperation wraps every host lookup, so a classification that did
		// not survive a wrap would never reach a :wait caller.
		{"wrapped sentinel", fmt.Errorf("resolving create operation: %w", &host.NotFoundError{}), http.StatusNotFound},
		{"wrapped typed", fmt.Errorf("creating host: %w", &quota.ExceededError{}), http.StatusTooManyRequests},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := statusFromError(tc.err); got != tc.want {
				t.Errorf("statusFromError(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestWriteServiceError_EnvelopeCarriesTheStatus proves the classification
// reaches the legacy {code, error} body as well as the status line, because
// the legacy clients read the envelope.
func TestWriteServiceError_EnvelopeCarriesTheStatus(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	newTestHandler(nil, nil).writeServiceError(rec, &host.InvalidStateError{
		Current:  "provisioning",
		Expected: "running",
	})

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	var apiErr coapi.Error
	mustDecode(t, rec, &apiErr)
	if apiErr.Code != http.StatusConflict {
		t.Errorf("envelope code = %d, want 409", apiErr.Code)
	}
	if apiErr.ErrorMsg == "" {
		t.Error("envelope carries no error message")
	}
}

// TestWriteServiceError_MasksUnclassifiedErrors pins the upstream
// unhandled-error envelope byte for byte: {"code":0,"error":"Internal Server
// Error"}, zero Code and trailing newline included. The raw err.Error() is
// backend detail and must never reach the wire on this path.
func TestWriteServiceError_MasksUnclassifiedErrors(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	newTestHandler(nil, nil).writeServiceError(rec, errors.New("spanner unavailable: session pool exhausted"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := rec.Body.String(); got != "{\"code\":0,\"error\":\"Internal Server Error\"}\n" {
		t.Errorf("body = %q, want the upstream unhandled-error envelope", got)
	}
}
