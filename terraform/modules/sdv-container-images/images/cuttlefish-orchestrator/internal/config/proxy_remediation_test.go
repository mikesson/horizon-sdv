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
)

func TestValidate_ProxyModeRejectsDevModeAndBroadPrefixesAndZones(t *testing.T) {
	t.Parallel()

	t.Run("proxy_with_dev_mode", func(t *testing.T) {
		t.Parallel()
		cfg := baseValidConfig()
		cfg.Auth = AuthConfig{
			Mode:           AuthModeProxy,
			DevMode:        true,
			TrustedProxies: []string{"127.0.0.1"},
		}
		if err := cfg.Validate(); err == nil {
			t.Fatal("Validate() succeeded with proxy mode and dev_mode=true, want error")
		}
	})

	invalidProxies := []struct {
		name  string
		entry string
	}{
		{name: "ipv4_slash1_low", entry: "0.0.0.0/1"},
		{name: "ipv4_slash1_high", entry: "128.0.0.0/1"},
		{name: "ipv6_slash1_low", entry: "::/1"},
		{name: "ipv6_slash1_high", entry: "8000::/1"},
		{name: "ipv4_mapped_slash96", entry: "::ffff:0.0.0.0/96"},
		{name: "ipv4_mapped_slash97_low", entry: "::ffff:0.0.0.0/97"},
		{name: "ipv4_mapped_slash97_high", entry: "::ffff:128.0.0.0/97"},
		{name: "ipv6_zone_id", entry: "fe80::1%eth0"},
		{name: "ipv6_zone_id_cidr", entry: "fe80::1%eth0/64"},
	}
	for _, tc := range invalidProxies {
		t.Run("proxy_"+tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := baseValidConfig()
			cfg.Auth = AuthConfig{
				Mode:           AuthModeProxy,
				TrustedProxies: []string{tc.entry},
			}
			if err := cfg.Validate(); err == nil {
				t.Fatalf("Validate() accepted CCO_TRUSTED_PROXIES=%q, want error", tc.entry)
			}
		})
	}

	controls := []struct {
		name  string
		value string
	}{
		{name: "tab", value: "alice\t@example.com"},
		{name: "vertical_tab", value: "alice\v@example.com"},
		{name: "escape", value: "alice\x1b@example.com"},
		{name: "del", value: "alice\x7f@example.com"},
	}
	for _, tc := range controls {
		t.Run("default_user_"+tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := baseValidConfig()
			cfg.Auth = AuthConfig{
				Mode:             AuthModeProxy,
				TrustedProxies:   []string{"10.0.0.0/8"},
				ProxyDefaultUser: tc.value,
			}
			if err := cfg.Validate(); err == nil {
				t.Fatalf("Validate() accepted CCO_DEFAULT_USER=%q, want error", tc.value)
			}
		})
	}
}

func TestValidate_ProbePort(t *testing.T) {
	t.Run("default_disabled_is_valid", func(t *testing.T) {
		cfg := baseValidConfig()
		cfg.Server.Address = ":8080"
		cfg.Server.ProbePort = 0
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() unexpected error: %v", err)
		}
	})

	t.Run("distinct_probe_port_is_valid", func(t *testing.T) {
		cfg := baseValidConfig()
		cfg.Server.Address = ":8080"
		cfg.Server.ProbePort = 8081
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() unexpected error: %v", err)
		}
	})

	invalidCases := []struct {
		name      string
		address   string
		portEnv   string
		probePort int
		wantErr   string
	}{
		{
			name:      "negative_port",
			address:   ":8080",
			probePort: -1,
			wantErr:   "between 0 and 65535",
		},
		{
			name:      "above_65535",
			address:   ":8080",
			probePort: 65536,
			wantErr:   "between 0 and 65535",
		},
		{
			name:      "collides_with_server_address",
			address:   ":8080",
			probePort: 8080,
			wantErr:   "must differ from main server port",
		},
		{
			name:      "collides_with_host_qualified_address",
			address:   "0.0.0.0:9090",
			probePort: 9090,
			wantErr:   "must differ from main server port",
		},
		{
			name:      "collides_with_PORT_env_fallback",
			address:   "",
			portEnv:   "9091",
			probePort: 9091,
			wantErr:   "must differ from main server port",
		},
		{
			name:      "collides_with_default_8080_when_address_empty",
			address:   "",
			portEnv:   "",
			probePort: 8080,
			wantErr:   "must differ from main server port",
		},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.address == "" {
				t.Setenv("PORT", tc.portEnv)
			}
			cfg := baseValidConfig()
			cfg.Server.Address = tc.address
			cfg.Server.ProbePort = tc.probePort
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() succeeded with address=%q PORT=%q probePort=%d, want error %q", tc.address, tc.portEnv, tc.probePort, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() err = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}

	t.Run("valid_when_address_empty_and_different_PORT_env", func(t *testing.T) {
		t.Setenv("PORT", "9090")
		cfg := baseValidConfig()
		cfg.Server.Address = ""
		cfg.Server.ProbePort = 8081
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() unexpected error: %v", err)
		}
	})
}

func TestLoad_ProbePortFromEnv(t *testing.T) {
	setMinimalEnv := func(t *testing.T) {
		t.Helper()
		t.Setenv("CCO_DEVICE_INGRESS", "public")
		t.Setenv("CCO_AUTH_MODE", "iap")
		t.Setenv("CCO_SPANNER_PROJECT", "proj")
		t.Setenv("CCO_SPANNER_INSTANCE", "inst")
		t.Setenv("CCO_SPANNER_DATABASE", "db")
		t.Setenv("CCO_PROJECT_ID", "proj")
		t.Setenv("CCO_HOST_IMAGE_PROJECT", "images")
		t.Setenv("CCO_DEPLOYMENT", "cco")
	}

	t.Run("default_zero", func(t *testing.T) {
		setMinimalEnv(t)
		t.Setenv("CCO_PROBE_PORT", "")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() unexpected error: %v", err)
		}
		if cfg.Server.ProbePort != 0 {
			t.Fatalf("ProbePort = %d, want 0", cfg.Server.ProbePort)
		}
	})

	t.Run("configured_8081", func(t *testing.T) {
		setMinimalEnv(t)
		t.Setenv("CCO_PROBE_PORT", "8081")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() unexpected error: %v", err)
		}
		if cfg.Server.ProbePort != 8081 {
			t.Fatalf("ProbePort = %d, want 8081", cfg.Server.ProbePort)
		}
	})

	t.Run("non_numeric_rejected", func(t *testing.T) {
		setMinimalEnv(t)
		t.Setenv("CCO_PROBE_PORT", "notaport")
		if _, err := Load(); err == nil {
			t.Fatal("Load() accepted non-integer CCO_PROBE_PORT, want error")
		}
	})

	t.Run("collision_rejected_on_load", func(t *testing.T) {
		setMinimalEnv(t)
		t.Setenv("CCO_PROBE_PORT", "8080")
		if _, err := Load(); err == nil {
			t.Fatal("Load() accepted colliding CCO_PROBE_PORT=8080, want error")
		}
	})
}
