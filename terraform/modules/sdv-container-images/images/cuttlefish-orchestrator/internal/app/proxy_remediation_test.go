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
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/config"
	adminfeature "cloud-cuttlefish-orchestrator/internal/features/admin"
	devicefeature "cloud-cuttlefish-orchestrator/internal/features/device"
	storagefeature "cloud-cuttlefish-orchestrator/internal/features/storage"
)

func TestBuildAuthConfig_ProxyRejectsDevModeOffGCPAndControlChars(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Auth: config.AuthConfig{
			Mode:             config.AuthModeProxy,
			DevMode:          true,
			TrustedProxies:   []string{"127.0.0.1"},
			DevFallbackEmail: "dev@example.com",
		},
	}
	if _, err := buildAuthConfig(cfg, func() bool { return false }); err == nil {
		t.Fatal("buildAuthConfig() succeeded with Mode=proxy and DevMode=true off GCP, want error")
	}

	for _, ctrl := range []string{"alice\t@example.com", "alice\v@example.com", "alice\x1b@example.com", "alice\x7f@example.com"} {
		ctrlCfg := &config.Config{
			Auth: config.AuthConfig{
				Mode:             config.AuthModeProxy,
				TrustedProxies:   []string{"10.0.0.0/8"},
				ProxyDefaultUser: ctrl,
			},
		}
		if _, err := buildAuthConfig(ctrlCfg, func() bool { return false }); err == nil {
			t.Fatalf("buildAuthConfig() accepted ProxyDefaultUser=%q, want control-character error", ctrl)
		}
	}
}

func TestIsAPIPath_ExactDocsSchemasAndOpenAPI(t *testing.T) {
	t.Parallel()

	wantAPI := []string{
		"/auth",
		"/oauth2callback",
		"/deauth",
		"/health",
		"/ready",
		"/info",
		"/docs",
		"/docs/",
		"/docs/getting-started",
		"/schemas",
		"/schemas/",
		"/schemas/Device.json",
		"/openapi.json",
		"/openapi.yaml",
		"/openapi-3.0.json",
		"/openapi-3.0.yaml",
		"/api/v1/zones",
		"/v1/hosts",
		"/v2/devices",
		"/connect/abc/shell",
	}
	for _, p := range wantAPI {
		if !isAPIPath(p) {
			t.Errorf("isAPIPath(%q) = false, want true", p)
		}
	}

	wantSPA := []string{
		"/",
		"/devices",
		"/docsify",
		"/docs-overview",
		"/openapi-ui",
		"/openapi/custom",
		"/schemas-browser",
		"/auth/callback",
	}
	for _, p := range wantSPA {
		if isAPIPath(p) {
			t.Errorf("isAPIPath(%q) = true, want false", p)
		}
	}
}

func TestBuildProbeServer_EndpointsMethodsAndIsolation(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Server.ProbePort = 8081
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	probeSrv := buildProbeServer(cfg, logger, adminfeature.NewHealthHandler("1.0.1"))

	if probeSrv.Addr != ":8081" {
		t.Fatalf("probeSrv.Addr = %q, want :8081", probeSrv.Addr)
	}
	if probeSrv.ReadTimeout != 5*time.Second || probeSrv.WriteTimeout != 5*time.Second {
		t.Fatalf("unexpected probeSrv timeouts: read=%v write=%v, want 5s/5s", probeSrv.ReadTimeout, probeSrv.WriteTimeout)
	}

	for _, path := range []string{"/health", "/ready"} {
		t.Run("GET_"+path, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			probeSrv.Handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", path, rec.Code)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("GET %s invalid JSON %q: %v", path, rec.Body.String(), err)
			}
			if body["status"] != "ok" {
				t.Errorf("GET %s status = %v, want ok", path, body["status"])
			}
		})

		t.Run("HEAD_"+path, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodHead, path, nil)
			rec := httptest.NewRecorder()
			probeSrv.Handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("HEAD %s = %d, want 200", path, rec.Code)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("HEAD %s returned non-empty body %q", path, rec.Body.String())
			}
		})

		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			t.Run(method+"_"+path+"_405", func(t *testing.T) {
				t.Parallel()
				req := httptest.NewRequest(method, path, nil)
				rec := httptest.NewRecorder()
				probeSrv.Handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusMethodNotAllowed {
					t.Errorf("%s %s = %d, want 405", method, path, rec.Code)
				}
			})
		}
	}

	notFoundPaths := []string{
		"/",
		"/v2/hosts",
		"/v2/devices",
		"/docs",
		"/openapi.json",
		"/info",
		"/health/details",
		"/ready/details",
	}
	for _, path := range notFoundPaths {
		t.Run("404_"+path, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			probeSrv.Handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("GET %s on probe port = %d, want 404", path, rec.Code)
			}
		})
	}
}

func TestBuildServer_ProbePortAndPodCIDRTrustedProxies(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Server.ProbePort = 8081
	cfg.Auth = config.AuthConfig{
		Mode:           config.AuthModeProxy,
		TrustedProxies: []string{"10.10.0.0/16"},
	}

	authConfig, err := buildAuthConfig(cfg, func() bool { return true })
	if err != nil {
		t.Fatalf("buildAuthConfig: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &featureSet{
		deviceHandler:  devicefeature.NewDeviceHandler(nil, nil, nil, nil),
		storageHandler: storagefeature.NewStorageHandler(nil),
		healthHandler:  adminfeature.NewHealthHandler("1.0.1"),
	}

	mainSrv, err := buildServer(cfg, logger, f, nil, authConfig)
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}

	for _, path := range []string{"/health", "/ready"} {
		// Unauthenticated :8080 probe must return 401 when ProbePort > 0.
		unauthReq := httptest.NewRequest(http.MethodGet, path, nil)
		unauthReq.RemoteAddr = "10.10.1.53:33333"
		unauthRec := httptest.NewRecorder()
		mainSrv.Handler.ServeHTTP(unauthRec, unauthReq)
		if unauthRec.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated mainSrv GET %s = %d, want 401", path, unauthRec.Code)
		}

		// Trusted Pod CIDR (10.10.1.53) + proxy identity header succeeds (200).
		podReq := httptest.NewRequest(http.MethodGet, path, nil)
		podReq.RemoteAddr = "10.10.1.53:33333"
		podReq.Header.Set("X-Auth-Request-Email", "worker@example.com")
		podRec := httptest.NewRecorder()
		mainSrv.Handler.ServeHTTP(podRec, podReq)
		if podRec.Code != http.StatusOK {
			t.Errorf("authenticated pod CIDR GET %s = %d, want 200", path, podRec.Code)
		}

		// Node subnet IP (10.1.0.42) is NOT in CCO_TRUSTED_PROXIES (10.10.0.0/16) and must fail closed (401).
		nodeReq := httptest.NewRequest(http.MethodGet, path, nil)
		nodeReq.RemoteAddr = "10.1.0.42:33333"
		nodeReq.Header.Set("X-Auth-Request-Email", "spoof@example.com")
		nodeRec := httptest.NewRecorder()
		mainSrv.Handler.ServeHTTP(nodeRec, nodeReq)
		if nodeRec.Code != http.StatusUnauthorized {
			t.Errorf("node subnet IP GET %s = %d, want 401", path, nodeRec.Code)
		}
	}

	// API path from node IP (10.1.0.42) with spoofed proxy headers is also rejected (401).
	apiReq := httptest.NewRequest(http.MethodGet, "/v2/me", nil)
	apiReq.RemoteAddr = "10.1.0.42:33333"
	apiReq.Header.Set("X-Auth-Request-Email", "spoof@example.com")
	apiRec := httptest.NewRecorder()
	mainSrv.Handler.ServeHTTP(apiRec, apiReq)
	if apiRec.Code != http.StatusUnauthorized {
		t.Errorf("node subnet IP GET /v2/me = %d, want 401", apiRec.Code)
	}
}
