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

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
)

func TestProxyEmailHTTPClient(t *testing.T) {
	t.Run("Resolve records ProxyEmail detail under MethodNone", func(t *testing.T) {
		mode := Resolve(&config.Config{AuthNone: true, ProxyEmail: " argo@example.com "})
		if mode.Method != MethodNone || mode.Detail != "argo@example.com" {
			t.Errorf("mode = %+v, want MethodNone with argo@example.com", mode)
		}
	})

	t.Run("AuthNone with ProxyEmail sets X-Auth-Request-Email", func(t *testing.T) {
		var gotHeader string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotHeader = r.Header.Get("X-Auth-Request-Email")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		client, err := NewHTTPClient(t.Context(), &config.Config{
			AuthNone:   true,
			ProxyEmail: "argo@example.com",
		}, srv.URL)
		if err != nil {
			t.Fatalf("NewHTTPClient: %v", err)
		}

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/v2/hosts", http.NoBody)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		_ = resp.Body.Close()
		if gotHeader != "argo@example.com" {
			t.Errorf("X-Auth-Request-Email = %q, want argo@example.com", gotHeader)
		}
		if req.Header.Get("X-Auth-Request-Email") != "" {
			t.Error("RoundTrip mutated original request headers")
		}
	})

	t.Run("AuthNone without ProxyEmail sends no proxy header", func(t *testing.T) {
		var gotHeader string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotHeader = r.Header.Get("X-Auth-Request-Email")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		client, err := NewHTTPClient(t.Context(), &config.Config{AuthNone: true}, srv.URL)
		if err != nil {
			t.Fatalf("NewHTTPClient: %v", err)
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		_ = resp.Body.Close()
		if gotHeader != "" {
			t.Errorf("X-Auth-Request-Email = %q, want empty", gotHeader)
		}
	})

	t.Run("control characters in ProxyEmail fail cleanly", func(t *testing.T) {
		if _, err := NewHTTPClient(t.Context(), &config.Config{
			AuthNone:   true,
			ProxyEmail: "bad\r\n@example.com",
		}, "http://localhost:8080"); err == nil {
			t.Fatal("NewHTTPClient succeeded with CRLF in ProxyEmail, want error")
		}
	})
}
