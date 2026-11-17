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
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
)

func TestWebSocketHandler_RejectsCrossOriginBeforeDeviceLookupOrUpstreamDial(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Nil repo, hosts, and hoclient.Factory ensure any reach past Origin
	// validation into device/host lookup or upstream websocket.Dial panics.
	ws := NewWebSocketHandler(nil, nil, nil, logger, false)

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.ContextWithPrincipal(req.Context(), &auth.Principal{Email: "alice@example.com"})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Get("/v2/devices/{id}/instances/{name}/signaling/ws", ws.HandleSignaling)
	r.Get("/v2/devices/{id}/instances/{name}/adb", ws.HandleADB)

	for _, path := range []string{
		"/v2/devices/dev-1/instances/cvd-1/signaling/ws",
		"/v2/devices/dev-1/instances/cvd-1/adb",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Host = "orchestrator.example.com"
			req.Header.Set("Origin", "https://evil.example.com")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s status = %d, want 403 Forbidden", path, rec.Code)
			}
		})
	}
}

func TestSameOrAllowedWSOrigin(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		host           string
		origins        []string
		allowDevOrigin bool
		want           bool
	}{
		{name: "no_origin", host: "cco.example.com", origins: nil, want: true},
		{name: "empty_origin", host: "cco.example.com", origins: []string{""}, want: true},
		{name: "same_host", host: "cco.example.com", origins: []string{"https://cco.example.com"}, want: true},
		{name: "same_host_case_insensitive", host: "CCO.Example.Com", origins: []string{"https://cco.example.com"}, want: true},
		{name: "same_host_with_port", host: "cco.example.com:8443", origins: []string{"https://cco.example.com:8443"}, want: true},
		{name: "cross_origin", host: "cco.example.com", origins: []string{"https://evil.example"}, want: false},
		{name: "null_origin", host: "cco.example.com", origins: []string{"null"}, want: false},
		{name: "duplicate_origin", host: "cco.example.com", origins: []string{"https://cco.example.com", "https://evil.example"}, want: false},
		{name: "dev_origin_blocked_by_default", host: "cco.example.com", origins: []string{"http://localhost:4200"}, allowDevOrigin: false, want: false},
		{name: "dev_origin_allowed_when_enabled", host: "cco.example.com", origins: []string{"http://localhost:4200"}, allowDevOrigin: true, want: true},
		{name: "other_origin_still_blocked_with_dev", host: "cco.example.com", origins: []string{"https://evil.example"}, allowDevOrigin: true, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/v2/devices/d/instances/i/adb", nil)
			req.Host = tc.host
			for _, o := range tc.origins {
				req.Header.Add("Origin", o)
			}
			if got := sameOrAllowedWSOrigin(req, tc.allowDevOrigin); got != tc.want {
				t.Fatalf("sameOrAllowedWSOrigin() = %v, want %v", got, tc.want)
			}
		})
	}
}
