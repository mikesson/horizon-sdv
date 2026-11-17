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
	"testing"

	"cloud-cuttlefish-orchestrator/internal/config"
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
