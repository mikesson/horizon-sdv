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

package config

import (
	"strings"
	"testing"
	"time"
)

// baseValidConfig returns the minimum that passes Validate, so tests can vary a
// single dimension.
func baseValidConfig() *Config {
	return &Config{
		DeviceIngress: DeviceIngressPublic,
		Deployment:    "cco",
		Auth:          AuthConfig{Mode: AuthModeIAP},
		GCP:           GCPConfig{ProjectID: "test-project", HostImageProject: "image-project"},
		Database: DatabaseConfig{
			Type: DatabaseTypeSpanner,
			Spanner: SpannerConfig{
				Project:  "test-project",
				Instance: "test-instance",
				Database: "test-db",
			},
		},
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	withTURN := func(c *Config) {
		c.TURN.Servers = []string{"turn:turn.example.com:3478"}
		c.TURN.SharedSecret = "secret"
	}
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string // substring; empty means valid
	}{
		{
			name:   "base config is valid",
			mutate: func(*Config) {},
		},
		{
			name:    "unknown database type rejected",
			mutate:  func(c *Config) { c.Database.Type = "memory" },
			wantErr: "unsupported database type",
		},
		{
			name:    "empty database type rejected",
			mutate:  func(c *Config) { c.Database.Type = "" },
			wantErr: "unsupported database type",
		},
		{
			name:    "missing device_ingress rejected",
			mutate:  func(c *Config) { c.DeviceIngress = "" },
			wantErr: "invalid device_ingress",
		},
		{
			name:    "invalid device_ingress rejected",
			mutate:  func(c *Config) { c.DeviceIngress = "vpn" },
			wantErr: "invalid device_ingress",
		},
		{
			name:   "device_ingress public is valid",
			mutate: func(c *Config) { c.DeviceIngress = DeviceIngressPublic },
		},
		{
			name:   "device_ingress private is valid without TURN",
			mutate: func(c *Config) { c.DeviceIngress = DeviceIngressPrivate },
		},
		{
			name:    "device_ingress relay without TURN servers rejected",
			mutate:  func(c *Config) { c.DeviceIngress = DeviceIngressRelay },
			wantErr: "TURN servers",
		},
		{
			name: "device_ingress relay without TURN secret rejected",
			mutate: func(c *Config) {
				c.DeviceIngress = DeviceIngressRelay
				c.TURN.Servers = []string{"turn:turn.example.com:3478"}
			},
			wantErr: "shared_secret",
		},
		{
			name: "device_ingress relay with TURN accepted",
			mutate: func(c *Config) {
				c.DeviceIngress = DeviceIngressRelay
				withTURN(c)
			},
		},
		{
			name:    "invalid auth mode rejected",
			mutate:  func(c *Config) { c.Auth.Mode = "vpn" },
			wantErr: "invalid auth mode",
		},
		{
			name:    "deleted auth mode bearer rejected",
			mutate:  func(c *Config) { c.Auth.Mode = "bearer" },
			wantErr: "invalid auth mode",
		},
		{
			name:   "auth mode network accepted",
			mutate: func(c *Config) { c.Auth.Mode = AuthModeNetwork },
		},
		{
			name:    "missing spanner project rejected",
			mutate:  func(c *Config) { c.Database.Spanner.Project = "" },
			wantErr: "spanner project",
		},
		{
			name:    "missing GCP project rejected",
			mutate:  func(c *Config) { c.GCP.ProjectID = "" },
			wantErr: "project_id",
		},
		{
			name:    "missing host image project rejected",
			mutate:  func(c *Config) { c.GCP.HostImageProject = "" },
			wantErr: "CCO_HOST_IMAGE_PROJECT",
		},
		{
			name:    "missing deployment rejected",
			mutate:  func(c *Config) { c.Deployment = "" },
			wantErr: "CCO_DEPLOYMENT",
		},
		{
			name:    "deployment that is not a label value rejected",
			mutate:  func(c *Config) { c.Deployment = "Team_A" },
			wantErr: "invalid deployment",
		},
		{
			name:    "deployment over ten characters rejected",
			mutate:  func(c *Config) { c.Deployment = "abcdefghijk" },
			wantErr: "invalid deployment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDeviceIngressUsesExternalIP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ingress DeviceIngress
		want    bool
	}{
		{DeviceIngressPublic, true},
		{DeviceIngressPrivate, false},
		{DeviceIngressRelay, false},
	}
	for _, tt := range tests {
		if got := tt.ingress.UsesExternalIP(); got != tt.want {
			t.Errorf("%q.UsesExternalIP() = %v, want %v", tt.ingress, got, tt.want)
		}
	}
}

// TestLoadFromEnv verifies CCO_* env vars populate the config, list values
// split+trim, bool (dev_mode) parses, durations keep their code defaults, and
// the derived Spanner path resolves. Uses t.Setenv (no parallel).
func TestLoadFromEnv(t *testing.T) {
	t.Setenv("CCO_DEVICE_INGRESS", "public")
	t.Setenv("CCO_AUTH_MODE", "iap")
	t.Setenv("CCO_IAP_AUDIENCE", "aud")
	t.Setenv("CCO_SPANNER_PROJECT", "proj")
	t.Setenv("CCO_SPANNER_INSTANCE", "inst")
	t.Setenv("CCO_SPANNER_DATABASE", "db")
	t.Setenv("CCO_PROJECT_ID", "proj")
	t.Setenv("CCO_HOST_IMAGE_PROJECT", "images")
	t.Setenv("CCO_DEPLOYMENT", "cco-b")
	t.Setenv("CCO_TURN_SERVERS", "turn:a:3478, turn:b:3478")
	t.Setenv("CCO_DEV_MODE", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DeviceIngress != DeviceIngressPublic {
		t.Errorf("device_ingress = %q, want public", cfg.DeviceIngress)
	}
	if cfg.GCP.HostImageProject != "images" {
		t.Errorf("host image project = %q, want images", cfg.GCP.HostImageProject)
	}
	if cfg.Deployment != "cco-b" {
		t.Errorf("deployment = %q, want cco-b", cfg.Deployment)
	}
	if cfg.GCP.ProjectID != "proj" || cfg.Database.Spanner.Instance != "inst" {
		t.Errorf("env not applied: gcp=%+v spanner=%+v", cfg.GCP, cfg.Database.Spanner)
	}
	if len(cfg.TURN.Servers) != 2 || cfg.TURN.Servers[1] != "turn:b:3478" {
		t.Errorf("turn servers = %v, want 2 split+trimmed", cfg.TURN.Servers)
	}
	if !cfg.Auth.DevMode {
		t.Error("CCO_DEV_MODE=true not applied")
	}
	if want := "projects/proj/instances/inst/databases/db"; cfg.Database.Spanner.FullDatabasePath != want {
		t.Errorf("FullDatabasePath = %q, want %q", cfg.Database.Spanner.FullDatabasePath, want)
	}
	// Durations come from the code defaults (there is no config file).
	if time.Duration(cfg.Server.ReadTimeout) != 30*time.Second {
		t.Errorf("server.read_timeout = %v, want 30s default", time.Duration(cfg.Server.ReadTimeout))
	}
	if time.Duration(cfg.HostReconciler.Interval) != time.Minute {
		t.Errorf("host reconciler interval = %v, want 1m default", time.Duration(cfg.HostReconciler.Interval))
	}
}

// TestLoadRequiresHostImageProject pins that there is no compiled image
// project: a deployment that does not name one fails to start instead of
// failing every host create later.
func TestLoadRequiresHostImageProject(t *testing.T) {
	t.Setenv("CCO_DEVICE_INGRESS", "public")
	t.Setenv("CCO_AUTH_MODE", "iap")
	t.Setenv("CCO_SPANNER_PROJECT", "proj")
	t.Setenv("CCO_SPANNER_INSTANCE", "inst")
	t.Setenv("CCO_SPANNER_DATABASE", "db")
	t.Setenv("CCO_PROJECT_ID", "proj")
	t.Setenv("CCO_DEPLOYMENT", "cco")
	t.Setenv("CCO_HOST_IMAGE_PROJECT", "")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CCO_HOST_IMAGE_PROJECT") {
		t.Fatalf("Load() = %v, want an error naming CCO_HOST_IMAGE_PROJECT", err)
	}
}

// TestLoadRequiresDeployment pins that there is no compiled deployment name:
// two installs in one project that both fell back to a default would share a
// label and read each other's hosts as orphans.
func TestLoadRequiresDeployment(t *testing.T) {
	t.Setenv("CCO_DEVICE_INGRESS", "public")
	t.Setenv("CCO_AUTH_MODE", "iap")
	t.Setenv("CCO_SPANNER_PROJECT", "proj")
	t.Setenv("CCO_SPANNER_INSTANCE", "inst")
	t.Setenv("CCO_SPANNER_DATABASE", "db")
	t.Setenv("CCO_PROJECT_ID", "proj")
	t.Setenv("CCO_HOST_IMAGE_PROJECT", "images")
	t.Setenv("CCO_DEPLOYMENT", "")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CCO_DEPLOYMENT") {
		t.Fatalf("Load() = %v, want an error naming CCO_DEPLOYMENT", err)
	}
}

// TestLoadRejectsBadEnvInt pins the typed-env error path.
func TestLoadRejectsBadEnvInt(t *testing.T) {
	t.Setenv("CCO_HOST_ORCHESTRATOR_PORT", "not-a-port")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "invalid integer") {
		t.Errorf("err = %v, want invalid-integer error", err)
	}
}
