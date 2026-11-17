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

package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/storage"
)

// TestRespondError verifies the raw chi handlers' error sink classifies the
// same way the Huma operations do: domain and storage sentinels keep their
// status, 5xx never leaks internals, and unknown errors stay a generic 500.
func TestRespondError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		err         error
		wantCode    int
		wantType    string
		wantGeneric bool // true = the generic 500 message, false = err.Error()
	}{
		{
			name:     "AppError passes through",
			err:      NewNotFoundError("gone"),
			wantCode: http.StatusNotFound,
			wantType: "not_found",
		},
		{
			name:     "storage not-found maps to 404",
			err:      fmt.Errorf("%w: gs://bucket/missing.img", storage.ErrNotFound),
			wantCode: http.StatusNotFound,
			wantType: "not_found",
		},
		{
			name:     "storage permission-denied maps to 403",
			err:      fmt.Errorf("%w: gs://locked", storage.ErrPermissionDenied),
			wantCode: http.StatusForbidden,
			wantType: "forbidden",
		},
		{
			name:     "device not-found maps to 404 (signaling path)",
			err:      fmt.Errorf("reading device: %w", device.ErrNotFound),
			wantCode: http.StatusNotFound,
			wantType: "not_found",
		},
		{
			name:        "mapped 5xx keeps its status but not its message",
			err:         fmt.Errorf("no room: %w", host.ErrCapacityExceeded),
			wantCode:    http.StatusServiceUnavailable,
			wantType:    "service_unavailable",
			wantGeneric: true,
		},
		{
			name:        "unknown error stays a generic 500",
			err:         errors.New("spanner exploded: internal detail"),
			wantCode:    http.StatusInternalServerError,
			wantType:    "internal_server_error",
			wantGeneric: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			RespondError(rec, tt.err)

			if rec.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			var body AppError
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decoding body: %v", err)
			}
			if body.Type != tt.wantType {
				t.Errorf("type = %q, want %q", body.Type, tt.wantType)
			}
			if tt.wantGeneric && body.Message != genericServerError {
				t.Errorf("message = %q, want the generic message", body.Message)
			}
			if !tt.wantGeneric && body.Message == genericServerError {
				t.Errorf("message is generic, want the classified error text")
			}
		})
	}
}
