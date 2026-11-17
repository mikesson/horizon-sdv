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

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
)

func TestParseTrustedProxies(t *testing.T) {
	prefixes, err := ParseTrustedProxies([]string{"10.0.0.0/8", "172.16.0.5", "fd00::/48", "::ffff:192.168.1.0/120"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	if len(prefixes) != 4 {
		t.Fatalf("len(prefixes) = %d, want 4", len(prefixes))
	}
	if !prefixes[0].Contains(netip.MustParseAddr("10.1.2.3")) {
		t.Error("10.0.0.0/8 should contain 10.1.2.3")
	}
	if !prefixes[3].Contains(netip.MustParseAddr("192.168.1.42")) {
		t.Error("unmapped ::ffff:192.168.1.0/120 should contain 192.168.1.42")
	}

	invalid := [][]string{
		nil,
		{""},
		{"bad-ip"},
		{"0.0.0.0/0"},
		{"::/0"},
		{"::ffff:0.0.0.0/96"},
		{"::ffff:10.0.0.0/80"},
	}
	for _, raw := range invalid {
		if _, err := ParseTrustedProxies(raw); err == nil {
			t.Errorf("ParseTrustedProxies(%v) succeeded, want error", raw)
		}
	}
}

func TestAuthMiddleware_ProxyMode(t *testing.T) {
	trusted, err := ParseTrustedProxies([]string{"10.0.0.0/8", "172.16.0.5", "fd00::/48", "::ffff:192.168.1.0/120"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}

	run := func(t *testing.T, cfg AuthConfig, remoteAddr string, headers http.Header) (*auth.Principal, int) {
		t.Helper()
		var gotPrincipal *auth.Principal
		handler := Auth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := UserFromContext(r.Context())
			if !ok {
				t.Fatal("missing principal in context")
			}
			gotPrincipal = p
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v2/hosts", http.NoBody)
		req.RemoteAddr = remoteAddr
		for k, vals := range headers {
			for _, v := range vals {
				req.Header.Add(k, v)
			}
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return gotPrincipal, rec.Code
	}

	t.Run("email and user from trusted CIDR", func(t *testing.T) {
		cfg := AuthConfig{Mode: AccessModeProxy, TrustedProxies: trusted}
		h := http.Header{
			"X-Auth-Request-Email": {" alice@example.com "},
			"X-Auth-Request-User":  {"Alice"},
		}
		p, code := run(t, cfg, "10.1.2.3:54321", h)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if p.Email != "alice@example.com" || p.DisplayName != "Alice" {
			t.Errorf("principal = %+v, want alice@example.com / Alice", p)
		}
	})

	t.Run("ipv4-mapped ipv6 peer unmapped cleanly", func(t *testing.T) {
		cfg := AuthConfig{Mode: AccessModeProxy, TrustedProxies: trusted}
		h := http.Header{"X-Auth-Request-Email": {"bob@example.com"}}
		p, code := run(t, cfg, "[::ffff:10.5.6.7]:1234", h)
		if code != http.StatusOK || p.Email != "bob@example.com" {
			t.Fatalf("status=%d principal=%+v, want 200 bob@example.com", code, p)
		}
	})

	t.Run("ipv4 peer matches unmapped ::ffff CIDR", func(t *testing.T) {
		cfg := AuthConfig{Mode: AccessModeProxy, TrustedProxies: trusted}
		h := http.Header{"X-Auth-Request-Email": {"carol@example.com"}}
		p, code := run(t, cfg, "192.168.1.9:1234", h)
		if code != http.StatusOK || p.Email != "carol@example.com" {
			t.Fatalf("status=%d principal=%+v, want 200 carol@example.com", code, p)
		}
	})

	t.Run("ipv6 peer matches trusted CIDR", func(t *testing.T) {
		cfg := AuthConfig{Mode: AccessModeProxy, TrustedProxies: trusted}
		h := http.Header{"X-Auth-Request-User": {"svc-ci"}}
		p, code := run(t, cfg, "[fd00::123]:443", h)
		if code != http.StatusOK || p.Email != "svc-ci" {
			t.Fatalf("status=%d principal=%+v, want 200 svc-ci", code, p)
		}
	})

	t.Run("untrusted peer rejected even with spoofed X-Forwarded-For", func(t *testing.T) {
		cfg := AuthConfig{Mode: AccessModeProxy, TrustedProxies: trusted, ProxyDefaultUser: "fallback@example.com"}
		h := http.Header{
			"X-Forwarded-For":      {"10.1.2.3"},
			"X-Real-IP":            {"172.16.0.5"},
			"X-Auth-Request-Email": {"attacker@example.com"},
		}
		_, code := run(t, cfg, "203.0.113.99:9999", h)
		if code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("missing headers fail closed when default user unset", func(t *testing.T) {
		cfg := AuthConfig{Mode: AccessModeProxy, TrustedProxies: trusted}
		_, code := run(t, cfg, "172.16.0.5:8080", nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("missing headers use configured default user", func(t *testing.T) {
		cfg := AuthConfig{Mode: AccessModeProxy, TrustedProxies: trusted, ProxyDefaultUser: "argo@example.com"}
		p, code := run(t, cfg, "172.16.0.5:8080", nil)
		if code != http.StatusOK || p.Email != "argo@example.com" {
			t.Fatalf("status=%d principal=%+v, want 200 argo@example.com", code, p)
		}
	})

	t.Run("duplicate email header rejected with 401", func(t *testing.T) {
		cfg := AuthConfig{Mode: AccessModeProxy, TrustedProxies: trusted}
		h := http.Header{"X-Auth-Request-Email": {"a@example.com", "b@example.com"}}
		_, code := run(t, cfg, "10.0.0.1:8080", h)
		if code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("duplicate user header rejected even when email single", func(t *testing.T) {
		cfg := AuthConfig{Mode: AccessModeProxy, TrustedProxies: trusted}
		h := http.Header{
			"X-Auth-Request-Email": {"a@example.com"},
			"X-Auth-Request-User":  {"u1", "u2"},
		}
		_, code := run(t, cfg, "10.0.0.1:8080", h)
		if code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("control characters in proxy headers rejected with 401", func(t *testing.T) {
		cfg := AuthConfig{Mode: AccessModeProxy, TrustedProxies: trusted}
		for _, hdr := range []http.Header{
			{"X-Auth-Request-Email": {"alice\r\n@example.com"}},
			{"X-Auth-Request-Email": {"alice@example.com"}, "X-Auth-Request-User": {"a\x00b"}},
		} {
			_, code := run(t, cfg, "10.0.0.1:8080", hdr)
			if code != http.StatusUnauthorized {
				t.Fatalf("status = %d for %v, want 401", code, hdr)
			}
		}
	})
}
