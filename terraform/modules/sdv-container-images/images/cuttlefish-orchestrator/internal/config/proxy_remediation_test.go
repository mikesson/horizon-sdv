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

import "testing"

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
