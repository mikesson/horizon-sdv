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

package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestParseTrustedProxies_RejectsHalfSpaceAndZoneIDs(t *testing.T) {
	t.Parallel()

	invalid := []struct {
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

	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseTrustedProxies([]string{tc.entry}); err == nil {
				t.Fatalf("ParseTrustedProxies(%q) succeeded, want error", tc.entry)
			}
		})
	}
}

func TestProxyAuthenticator_RejectsAllASCIIControlChars(t *testing.T) {
	t.Parallel()

	controls := []struct {
		name  string
		value string
	}{
		{name: "tab", value: "alice\t@example.com"},
		{name: "vertical_tab", value: "alice\v@example.com"},
		{name: "escape", value: "alice\x1b@example.com"},
		{name: "del", value: "alice\x7f@example.com"},
	}

	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}

	for _, tc := range controls {
		t.Run("header_"+tc.name, func(t *testing.T) {
			t.Parallel()
			authCfg := AuthConfig{
				Mode:           AccessModeProxy,
				TrustedProxies: trusted,
			}
			handler := Auth(authCfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodGet, "/v2/devices", nil)
			req.RemoteAddr = "10.0.0.5:12345"
			req.Header.Set("X-Auth-Request-Email", tc.value)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("Auth() status = %d for header %q, want 401 Unauthorized", rec.Code, tc.value)
			}
		})

		t.Run("default_user_"+tc.name, func(t *testing.T) {
			t.Parallel()
			authCfg := AuthConfig{
				Mode:             AccessModeProxy,
				TrustedProxies:   trusted,
				ProxyDefaultUser: tc.value,
			}
			handler := Auth(authCfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodGet, "/v2/devices", nil)
			req.RemoteAddr = "10.0.0.5:12345"
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("Auth() status = %d for ProxyDefaultUser %q, want 401 Unauthorized", rec.Code, tc.value)
			}
		})
	}
}
