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

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
)

// hostListBackend returns four hosts and counts list requests: two live hosts
// with distinct names, a second live host named alpha, and a deleted host.
func hostListBackend(t *testing.T, hits *atomic.Int32) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/hosts" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		hits.Add(1)
		fmt.Fprint(w, `{"items":[{"id":"h-1","name":"alpha","zone":"z","status":"running"},
			{"id":"h-2","name":"beta","zone":"z","status":"running"},
			{"id":"h-3","name":"alpha","zone":"z","status":"stopped"},
			{"id":"h-4","name":"gone","zone":"z","status":"deleted"}]}`)
	})
}

func TestCompletionValues_FetchesAndCaches(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	var hits atomic.Int32

	// The cache is keyed by server URL, so both calls must reach one server.
	srv := httptest.NewServer(hostListBackend(t, &hits))
	t.Cleanup(srv.Close)

	out, err := runCLIAgainst(t, srv, "completion", "values", "host")
	if err != nil {
		t.Fatalf("completion values: %v", err)
	}
	// Names, each once, and none of a deleted host.
	if out != "alpha\nbeta\n" {
		t.Errorf("unexpected values output:\n%q", out)
	}
	if hits.Load() != 1 {
		t.Errorf("expected 1 backend hit, got %d", hits.Load())
	}

	// Within the TTL the cache answers; the server must not be consulted.
	out, err = runCLIAgainst(t, srv, "completion", "values", "host")
	if err != nil {
		t.Fatalf("second completion values: %v", err)
	}
	if out != "alpha\nbeta\n" {
		t.Errorf("cached output differs:\n%q", out)
	}
	if hits.Load() != 1 {
		t.Errorf("fresh cache still hit the server: %d hits", hits.Load())
	}
}

// seedCompletionCache writes a cache file with the given servers' entries.
func seedCompletionCache(t *testing.T, servers map[string]map[string]completionEntry) {
	t.Helper()
	dir, err := config.Dir()
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.Marshal(completionCache{Servers: servers})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "completion-cache.json"), data, 0o600); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
}

func TestCompletionValues_ServesStaleCacheWhenFetchFails(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	seedCompletionCache(t, map[string]map[string]completionEntry{
		srv.URL: {"host": {Values: []string{"h-stale"}, FetchedAt: time.Now().Add(-time.Hour)}},
	})

	out, err := runCLIAgainst(t, srv, "completion", "values", "host")
	if err != nil {
		t.Fatalf("completion values must not error on a dead server: %v", err)
	}
	if out != "h-stale\n" {
		t.Errorf("expected the stale value, got:\n%q", out)
	}
}

// A fresh entry cached for another server must never be offered.
func TestCompletionValues_CacheIsPerServer(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	var hits atomic.Int32

	srv := httptest.NewServer(hostListBackend(t, &hits))
	t.Cleanup(srv.Close)
	seedCompletionCache(t, map[string]map[string]completionEntry{
		"https://other.example.com": {"host": {Values: []string{"h-other"}, FetchedAt: time.Now()}},
	})

	out, err := runCLIAgainst(t, srv, "completion", "values", "host")
	if err != nil {
		t.Fatalf("completion values: %v", err)
	}
	if out != "alpha\nbeta\n" {
		t.Errorf("served another server's cache:\n%q", out)
	}
	if hits.Load() != 1 {
		t.Errorf("expected 1 backend hit, got %d", hits.Load())
	}
}

// Profile names come from the config file, sorted, with no server call even
// when the selected profile names a server.
func TestCompletionValues_ProfilesFromConfigFile(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("profile completion called the server: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(srv.Close)

	file := &config.File{CurrentProfile: "prod", Profiles: map[string]config.Config{
		"staging": {Server: srv.URL, AuthNone: true},
		"prod":    {Server: srv.URL, AuthNone: true},
	}}
	var c CLI
	parser, err := NewParser(&c, file, "test")
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	kctx, err := parser.Parse([]string{"completion", "values", "profile"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	kctx.BindTo(t.Context(), (*context.Context)(nil))
	out, err := captureStdout(t, func() error { return kctx.Run() })
	if err != nil {
		t.Fatalf("completion values profile: %v", err)
	}
	if out != "prod\nstaging\n" {
		t.Errorf("profile values = %q, want prod and staging in order", out)
	}
}

func TestCompletionValues_SilentWithoutServer(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())

	var c CLI
	parser, err := NewParser(&c, nil, "test")
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	kctx, err := parser.Parse([]string{"completion", "values", "host"})
	if err != nil {
		t.Fatalf("Parse must succeed without a server: %v", err)
	}
	kctx.BindTo(t.Context(), (*context.Context)(nil))
	out, err := captureStdout(t, func() error { return kctx.Run() })
	if err != nil {
		t.Fatalf("completion values must not error without a server: %v", err)
	}
	if out != "" {
		t.Errorf("expected silence without a server, got:\n%q", out)
	}
}
