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

package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/config"
	"cloud-cuttlefish-orchestrator/internal/middleware"
)

func TestBuildAuthConfig_ProxyModeOnGCP(t *testing.T) {
	t.Setenv("K_SERVICE", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")

	cfg := &config.Config{
		Auth: config.AuthConfig{
			Mode:             config.AuthModeProxy,
			TrustedProxies:   []string{"10.0.0.0/8", "172.16.0.1"},
			ProxyDefaultUser: " argo@example.com ",
		},
	}
	ac, err := buildAuthConfig(cfg, func() bool { return true })
	if err != nil {
		t.Fatalf("buildAuthConfig(proxy, onGCP=true): %v", err)
	}
	if ac.Mode != middleware.AccessModeProxy {
		t.Errorf("Mode = %q, want %q", ac.Mode, middleware.AccessModeProxy)
	}
	if len(ac.TrustedProxies) != 2 {
		t.Errorf("len(TrustedProxies) = %d, want 2", len(ac.TrustedProxies))
	}
	if ac.ProxyDefaultUser != "argo@example.com" {
		t.Errorf("ProxyDefaultUser = %q, want trimmed default user", ac.ProxyDefaultUser)
	}
}

func TestBuildAuthConfig_ProxyModeValidation(t *testing.T) {
	t.Setenv("K_SERVICE", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")

	tests := []struct {
		name  string
		auth  config.AuthConfig
		onGCP bool
	}{
		{
			name:  "dev mode with proxy still blocked on GCP",
			auth:  config.AuthConfig{Mode: config.AuthModeProxy, DevMode: true, TrustedProxies: []string{"10.0.0.0/8"}},
			onGCP: true,
		},
		{
			name:  "empty trusted proxies",
			auth:  config.AuthConfig{Mode: config.AuthModeProxy},
			onGCP: false,
		},
		{
			name:  "slash zero CIDR rejected",
			auth:  config.AuthConfig{Mode: config.AuthModeProxy, TrustedProxies: []string{"0.0.0.0/0"}},
			onGCP: false,
		},
		{
			name:  "control char in default user rejected",
			auth:  config.AuthConfig{Mode: config.AuthModeProxy, TrustedProxies: []string{"10.0.0.0/8"}, ProxyDefaultUser: "a\nb"},
			onGCP: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := buildAuthConfig(&config.Config{Auth: tt.auth}, func() bool { return tt.onGCP }); err == nil {
				t.Errorf("buildAuthConfig succeeded, want error for %s", tt.name)
			}
		})
	}
}

func TestWithSPADir_MissingVsPresent(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Handler", "api")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("api:" + r.URL.Path))
	})

	t.Run("missing SPA dir returns apiHandler directly", func(t *testing.T) {
		h := withSPADir(api, filepath.Join(t.TempDir(), "does-not-exist"))
		for _, path := range []string{"/", "/hosts", "/docs", "/v2/hosts"} {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Header().Get("X-Handler") != "api" {
				t.Errorf("path %q did not route to apiHandler", path)
			}
		}
	})

	t.Run("present SPA dir serves static assets and falls back to index.html", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>spa</html>"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		h := withSPADir(api, dir)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/hosts/123", http.NoBody)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Body.String() != "<html>spa</html>" {
			t.Errorf("SPA route body = %q, want index.html", rec.Body.String())
		}

		apiReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v2/hosts", http.NoBody)
		apiRec := httptest.NewRecorder()
		h.ServeHTTP(apiRec, apiReq)
		if apiRec.Header().Get("X-Handler") != "api" {
			t.Errorf("/v2/hosts did not route to apiHandler")
		}
	})
}
