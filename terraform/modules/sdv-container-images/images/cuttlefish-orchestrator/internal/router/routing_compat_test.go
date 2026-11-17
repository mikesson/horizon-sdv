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
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestChiServesLegacyPathShapes proves go-chi can serve the exact gorilla/mux
// path shapes the legacy Cloud Orchestrator uses, so the /v1 surface stays
// byte-for-byte compatible: the colon custom-verb ":wait", the "{host}/*"
// reverse-proxy catch-all, and a specific route (infra_config) that lives
// UNDER the same prefix as the catch-all coexisting with it.
func TestChiServesLegacyPathShapes(t *testing.T) {
	t.Parallel()
	r := chi.NewRouter()

	var lastParams map[string]string
	hit := func(name string) (http.HandlerFunc, *string) {
		got := new(string)
		return func(_ http.ResponseWriter, req *http.Request) {
			*got = name
			rc := chi.RouteContext(req.Context())
			lastParams = map[string]string{}
			for i, k := range rc.URLParams.Keys {
				lastParams[k] = rc.URLParams.Values[i]
			}
		}, got
	}

	waitH, waitHit := hit("wait")
	proxyH, proxyHit := hit("proxy")
	infraH, infraHit := hit("infra")
	listZonesH, listZonesHit := hit("listZones")
	listHostsH, listHostsHit := hit("listHosts")
	v2H, v2Hit := hit("v2")

	// Legacy CO paths (exact).
	r.Post("/v1/zones/{zone}/operations/{operation}/:wait", waitH)
	r.Get("/v1/zones", listZonesH)
	r.Get("/v1/zones/{zone}/hosts", listHostsH)
	r.Get("/v1/zones/{zone}/hosts/{host}/infra_config", infraH)
	r.Handle("/v1/zones/{zone}/hosts/{host}/*", proxyH)
	// Native v2 static path coexisting with the /v1 param subtree.
	r.Get("/v2/zones/availability", v2H)

	cases := []struct {
		name, method, path, wantHit string
		wantParams                  map[string]string
	}{
		{
			"colon-wait", http.MethodPost, "/v1/zones/us/operations/op-123/:wait", "wait",
			map[string]string{"zone": "us", "operation": "op-123"},
		},
		{"list-zones", http.MethodGet, "/v1/zones", "listZones", nil},
		{
			"list-hosts", http.MethodGet, "/v1/zones/us/hosts", "listHosts",
			map[string]string{"zone": "us"},
		},
		{
			"infra-config-specific", http.MethodGet, "/v1/zones/us/hosts/h1/infra_config", "infra",
			map[string]string{"zone": "us", "host": "h1"},
		},
		{
			"proxy-cvds", http.MethodGet, "/v1/zones/us/hosts/h1/cvds", "proxy",
			map[string]string{"zone": "us", "host": "h1", "*": "cvds"},
		},
		{
			"proxy-deep-colon-verb", http.MethodGet, "/v1/zones/us/hosts/h1/cvds/grp/:start", "proxy",
			map[string]string{"zone": "us", "host": "h1", "*": "cvds/grp/:start"},
		},
		{"v2-static", http.MethodGet, "/v2/zones/availability", "v2", nil},
	}

	hits := map[string]*string{
		"wait": waitHit, "proxy": proxyHit, "infra": infraHit,
		"listZones": listZonesHit, "listHosts": listHostsHit, "v2": v2Hit,
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, p := range hits {
				*p = ""
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (route not matched)", rec.Code)
			}
			if got := *hits[tc.wantHit]; got != tc.wantHit {
				t.Fatalf("handler hit = %q, want %q", got, tc.wantHit)
			}
			for k, want := range tc.wantParams {
				if lastParams[k] != want {
					t.Errorf("param %q = %q, want %q", k, lastParams[k], want)
				}
			}
		})
	}
}
