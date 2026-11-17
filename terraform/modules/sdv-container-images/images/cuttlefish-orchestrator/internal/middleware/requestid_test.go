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

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveRequestID runs the RequestID middleware and returns the response plus
// the ID the inner handler saw in context.
func serveRequestID(t *testing.T, inbound string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	var ctxID string
	h := RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxID, _ = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	if inbound != "" {
		req.Header.Set("X-Request-ID", inbound)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, ctxID
}

func TestRequestIDGeneratedWhenAbsent(t *testing.T) {
	t.Parallel()
	rec, ctxID := serveRequestID(t, "")

	echoed := rec.Header().Get("X-Request-ID")
	if echoed == "" {
		t.Fatal("no X-Request-ID generated")
	}
	if ctxID != echoed {
		t.Errorf("context ID = %q, response header = %q, want identical", ctxID, echoed)
	}
}

func TestRequestIDEchoesValidInbound(t *testing.T) {
	t.Parallel()
	rec, ctxID := serveRequestID(t, "client-supplied-123")

	if got := rec.Header().Get("X-Request-ID"); got != "client-supplied-123" {
		t.Errorf("echoed ID = %q, want the inbound value", got)
	}
	if ctxID != "client-supplied-123" {
		t.Errorf("context ID = %q, want the inbound value", ctxID)
	}
}

func TestRequestIDReplacesHostileInbound(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		inbound string
	}{
		{"too long", strings.Repeat("a", 129)},
		{"control characters", "abc\x01def"},
		{"spaces", "not a token"},
		{"non-ASCII", "идентификатор"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, ctxID := serveRequestID(t, tt.inbound)

			got := rec.Header().Get("X-Request-ID")
			if got == tt.inbound {
				t.Errorf("hostile inbound ID %q echoed verbatim, want a fresh ID", tt.inbound)
			}
			if got == "" || ctxID != got {
				t.Errorf("header = %q, context = %q, want a matching generated ID", got, ctxID)
			}
		})
	}
}

func TestRequestIDEchoesMaxLengthInbound(t *testing.T) {
	t.Parallel()
	id := strings.Repeat("a", maxRequestIDLength)
	rec, _ := serveRequestID(t, id)
	if got := rec.Header().Get("X-Request-ID"); got != id {
		t.Errorf("a %d-char ID must be echoed, got %q", maxRequestIDLength, got)
	}
}

func TestRequestIDFromContextAbsent(t *testing.T) {
	t.Parallel()
	if id, ok := RequestIDFromContext(t.Context()); ok || id != "" {
		t.Errorf("RequestIDFromContext(empty ctx) = (%q, %t), want (\"\", false)", id, ok)
	}
}
