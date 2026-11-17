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
	"slices"
	"testing"
)

func setMinimalValidConfigEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CCO_DEVICE_INGRESS", "private")
	t.Setenv("CCO_SPANNER_PROJECT", "test-proj")
	t.Setenv("CCO_SPANNER_INSTANCE", "test-inst")
	t.Setenv("CCO_SPANNER_DATABASE", "test-db")
	t.Setenv("CCO_PROJECT_ID", "test-proj")
	t.Setenv("CCO_DEPLOYMENT", "dev")
	t.Setenv("CCO_HOST_IMAGE_PROJECT", "img-proj")
	t.Setenv("CCO_AUTH_MODE", "")
	t.Setenv("CCO_TRUSTED_PROXIES", "")
	t.Setenv("CCO_DEFAULT_USER", "")
}

func TestLoadAndValidate_ProxyMode(t *testing.T) {
	setMinimalValidConfigEnv(t)
	t.Setenv("CCO_AUTH_MODE", "proxy")
	t.Setenv("CCO_TRUSTED_PROXIES", "10.0.0.0/8, 172.16.0.10, fd00::/48")
	t.Setenv("CCO_DEFAULT_USER", "argo@example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.Mode != AuthModeProxy {
		t.Errorf("Auth.Mode = %q, want %q", cfg.Auth.Mode, AuthModeProxy)
	}
	wantProxies := []string{"10.0.0.0/8", "172.16.0.10", "fd00::/48"}
	if !slices.Equal(cfg.Auth.TrustedProxies, wantProxies) {
		t.Errorf("TrustedProxies = %v, want %v", cfg.Auth.TrustedProxies, wantProxies)
	}
	if cfg.Auth.ProxyDefaultUser != "argo@example.com" {
		t.Errorf("ProxyDefaultUser = %q, want %q", cfg.Auth.ProxyDefaultUser, "argo@example.com")
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate failed for valid proxy config: %v", err)
	}
}

func TestValidate_ProxyModeErrors(t *testing.T) {
	baseCfg := func() Config {
		return Config{
			DeviceIngress: DeviceIngressPrivate,
			Deployment:    "dev",
			Database: DatabaseConfig{
				Type: DatabaseTypeSpanner,
				Spanner: SpannerConfig{
					Project:  "test-proj",
					Instance: "test-inst",
					Database: "test-db",
				},
			},
			GCP: GCPConfig{
				ProjectID:        "test-proj",
				HostImageProject: "img-proj",
			},
			Auth: AuthConfig{
				Mode:           AuthModeProxy,
				TrustedProxies: []string{"10.0.0.0/8"},
			},
		}
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{
			name:   "empty trusted proxies",
			mutate: func(c *Config) { c.Auth.TrustedProxies = nil },
		},
		{
			name:   "blank entry in trusted proxies",
			mutate: func(c *Config) { c.Auth.TrustedProxies = []string{"10.0.0.1", " "} },
		},
		{
			name:   "invalid IP or CIDR",
			mutate: func(c *Config) { c.Auth.TrustedProxies = []string{"not-an-ip"} },
		},
		{
			name:   "invalid CIDR mask",
			mutate: func(c *Config) { c.Auth.TrustedProxies = []string{"10.0.0.0/33"} },
		},
		{
			name:   "reject ipv4 all-zeros slash zero",
			mutate: func(c *Config) { c.Auth.TrustedProxies = []string{"0.0.0.0/0"} },
		},
		{
			name:   "reject ipv6 all-zeros slash zero",
			mutate: func(c *Config) { c.Auth.TrustedProxies = []string{"::/0"} },
		},
		{
			name:   "reject ipv4-mapped ipv6 all-zeros",
			mutate: func(c *Config) { c.Auth.TrustedProxies = []string{"::ffff:0.0.0.0/96"} },
		},
		{
			name:   "reject control character in default user",
			mutate: func(c *Config) { c.Auth.ProxyDefaultUser = "user\r\n@example.com" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseCfg()
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() succeeded, want error for %s", tt.name)
			}
		})
	}
}
