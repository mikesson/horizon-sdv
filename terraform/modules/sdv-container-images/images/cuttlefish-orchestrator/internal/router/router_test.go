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

package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/middleware"
)

// TestPathBasedAuth exercises the real pathBasedAuth middleware: only the exact
// public paths bypass authentication; everything else, including /info and the
// Huma docs/openapi, requires a verified identity. In iap mode a request with
// no IAP assertion header fails closed (401), so protected paths are rejected.
func TestPathBasedAuth(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := pathBasedAuth(
		middleware.AuthConfig{Mode: middleware.AccessModeIAP},
		[]string{"/health", "/ready"},
	)(next)

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{"health is public", "/health", http.StatusOK},
		{"ready is public", "/ready", http.StatusOK},
		{"info requires auth", "/info", http.StatusUnauthorized},
		{"openapi requires auth", "/openapi.json", http.StatusUnauthorized},
		{"devices require auth", "/v2/devices", http.StatusUnauthorized},
		{"me requires auth", "/v2/me", http.StatusUnauthorized},
		// Public paths match exactly, so a deeper path is not shadowed public.
		{"health subpath is not public", "/health/details", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("path %s: got status %d, want %d", tt.path, rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestHumaConfig_Defaults(t *testing.T) {
	t.Parallel()
	cfg := DefaultHumaConfig()

	if cfg.Title == "" {
		t.Error("expected non-empty title")
	}
	if cfg.Version == "" {
		t.Error("expected non-empty version")
	}
	if cfg.Description == "" {
		t.Error("expected non-empty description")
	}
}

func TestHumaConfig_WithPrefix(t *testing.T) {
	t.Parallel()
	cfg := DefaultHumaConfig()

	// Test NewHumaAPIWithPrefix creates valid config
	if cfg.Title != "Cloud Cuttlefish Orchestrator API" {
		t.Errorf("unexpected title: %s", cfg.Title)
	}
	if cfg.Version != "1.0.0" {
		t.Errorf("unexpected version: %s", cfg.Version)
	}
}

func TestDocsHandler(t *testing.T) {
	t.Parallel()
	handler := docsHandler("Test API Reference")

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/docs", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("got Content-Type %q", ct)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Error("expected a Content-Security-Policy header")
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<title>Test API Reference</title>",
		`data-url="openapi.json"`,
		`"darkMode":true`,
		scalarScriptURL,
		scalarScriptSRI,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("docs page missing %q", want)
		}
	}
}
