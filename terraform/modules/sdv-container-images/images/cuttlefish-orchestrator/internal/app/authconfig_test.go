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
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/config"
)

func TestBuildAuthConfig(t *testing.T) {
	offGCP := func() bool { return false }
	onGCP := func() bool { return true }

	tests := []struct {
		name    string
		auth    config.AuthConfig
		onGCP   func() bool
		env     map[string]string
		wantErr string
	}{
		{
			name: "iap with audience",
			auth: config.AuthConfig{Mode: config.AuthModeIAP, IAPAudience: "/projects/1/locations/r/services/s"},
		},
		{
			name:    "iap without audience fails closed at boot",
			auth:    config.AuthConfig{Mode: config.AuthModeIAP},
			wantErr: "IAP audience",
		},
		{
			name: "network needs no audience",
			auth: config.AuthConfig{Mode: config.AuthModeNetwork},
		},
		{
			name:    "unknown mode rejected",
			auth:    config.AuthConfig{Mode: "bearer"},
			wantErr: "unsupported auth mode",
		},
		{
			name: "dev mode allowed off GCP",
			auth: config.AuthConfig{DevMode: true, DevFallbackEmail: "dev@example.com"},
		},
		{
			name:    "dev mode refused in Cloud Run",
			auth:    config.AuthConfig{DevMode: true},
			env:     map[string]string{"K_SERVICE": "cuttlefish-orchestrator"},
			wantErr: "Cloud Run",
		},
		{
			// A GCE VM or GKE pod sets none of the env vars Cloud Run does;
			// the metadata probe is the only guard that holds there. This is
			// the case the old env-only test silently passed.
			name:    "dev mode refused when only the metadata server gives GCP away",
			auth:    config.AuthConfig{DevMode: true},
			onGCP:   onGCP,
			wantErr: "metadata server",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			detect := tt.onGCP
			if detect == nil {
				detect = offGCP
			}
			_, err := buildAuthConfig(&config.Config{Auth: tt.auth}, detect)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("buildAuthConfig() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("buildAuthConfig() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestRunningOnGCP exercises the probe against a fake metadata endpoint via
// GCE_METADATA_HOST, the override the Google client libraries honor.
func TestRunningOnGCP(t *testing.T) {
	tests := []struct {
		name   string
		flavor string
		want   bool
	}{
		{name: "metadata server present", flavor: "Google", want: true},
		{name: "an HTTP server that is not the metadata server", flavor: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.flavor != "" {
					w.Header().Set("Metadata-Flavor", tt.flavor)
				}
			}))
			defer srv.Close()
			t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(srv.URL, "http://"))

			if got := runningOnGCP(); got != tt.want {
				t.Errorf("runningOnGCP() = %v, want %v", got, tt.want)
			}
		})
	}
}
