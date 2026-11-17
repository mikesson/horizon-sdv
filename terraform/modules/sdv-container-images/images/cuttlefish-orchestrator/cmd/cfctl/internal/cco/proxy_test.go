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

package cco

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
)

func TestNewClient_AuthNoneWithoutProxyEmailDoesNotPanic(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
		case "/v2/me":
			if r.Header.Get("X-Auth-Request-Email") == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"title":  "Unauthorized",
					"status": http.StatusUnauthorized,
					"detail": "missing proxy identity header",
				})
				return
			}
			t.Errorf("unexpected X-Auth-Request-Email header: %q", r.Header.Get("X-Auth-Request-Email"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		Server:   srv.URL,
		AuthNone: true,
	}
	client, err := NewClient(t.Context(), srv.URL, cfg, 5*time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client.streamClient == nil || client.streamClient.Transport == nil {
		t.Fatal("NewClient() left streamClient or its Transport nil")
	}

	health, err := client.Health(t.Context())
	if err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if health.Status != "ok" {
		t.Fatalf("Health().Status = %q, want %q", health.Status, "ok")
	}

	_, err = client.Me(t.Context())
	if err == nil {
		t.Fatal("Me() succeeded without identity header, want HTTP 401 error")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("Me() error = %v, want *cco.Error with status 401", err)
	}
}

func TestNewClient_AuthNoneWithProxyEmailInjectsHeader(t *testing.T) {
	t.Parallel()

	var gotEmail string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/me" {
			http.NotFound(w, r)
			return
		}
		gotEmail = r.Header.Get("X-Auth-Request-Email")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(UserInfo{Email: gotEmail})
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		Server:     srv.URL,
		AuthNone:   true,
		ProxyEmail: "user@example.com",
	}
	client, err := NewClient(t.Context(), srv.URL, cfg, 5*time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	me, err := client.Me(t.Context())
	if err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if gotEmail != "user@example.com" {
		t.Fatalf("X-Auth-Request-Email = %q, want %q", gotEmail, "user@example.com")
	}
	if me.Email != "user@example.com" {
		t.Fatalf("Me().Email = %q, want %q", me.Email, "user@example.com")
	}
}
