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
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
)

// countingSource mints "tok-N" tokens that expire after ttl and counts calls.
type countingSource struct {
	calls int
	ttl   time.Duration
	now   func() time.Time
	err   error
}

func (s *countingSource) Token() (*oauth2.Token, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &oauth2.Token{AccessToken: "tok-" + string(rune('0'+s.calls)), Expiry: s.now().Add(s.ttl)}, nil
}

func newTestCache(t *testing.T, path string, src *countingSource, key string, now func() time.Time) *diskCachedTokenSource {
	t.Helper()
	return &diskCachedTokenSource{mint: src.Token, path: path, key: key, now: now}
}

func TestDiskCachedTokenSourceReusesAcrossInvocations(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sub", "token-cache.json")
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	src := &countingSource{ttl: 50 * time.Minute, now: now}

	first, err := newTestCache(t, path, src, "sa a", now).Token()
	if err != nil {
		t.Fatalf("first Token: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("cache not written: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("cache mode = %o, want 600", mode)
	}

	// A second invocation within the window reuses the stored token.
	clock = clock.Add(40 * time.Minute)
	second, err := newTestCache(t, path, src, "sa a", now).Token()
	if err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if src.calls != 1 || second.AccessToken != first.AccessToken {
		t.Errorf("calls = %d, token %q; want one mint and the cached %q", src.calls, second.AccessToken, first.AccessToken)
	}

	// Under five minutes of validity left, it mints again.
	clock = clock.Add(6 * time.Minute)
	third, err := newTestCache(t, path, src, "sa a", now).Token()
	if err != nil {
		t.Fatalf("third Token: %v", err)
	}
	if src.calls != 2 || third.AccessToken == first.AccessToken {
		t.Errorf("calls = %d, token %q; want a fresh mint near expiry", src.calls, third.AccessToken)
	}
}

func TestDiskCachedTokenSourceKeysBySAAndAudience(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "token-cache.json")
	now := time.Now
	src := &countingSource{ttl: time.Hour, now: now}

	if _, err := newTestCache(t, path, src, "sa-1 https://a/*", now).Token(); err != nil {
		t.Fatal(err)
	}
	if _, err := newTestCache(t, path, src, "sa-1 https://b/*", now).Token(); err != nil {
		t.Fatal(err)
	}
	if _, err := newTestCache(t, path, src, "sa-2 https://a/*", now).Token(); err != nil {
		t.Fatal(err)
	}
	if src.calls != 3 {
		t.Errorf("calls = %d, want 3 (one per distinct SA and audience)", src.calls)
	}
}

func TestDiskCachedTokenSourceIgnoresBrokenCache(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "token-cache.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := &countingSource{ttl: time.Hour, now: time.Now}
	if _, err := newTestCache(t, corrupt, src, "k", time.Now).Token(); err != nil {
		t.Fatalf("corrupt cache failed the command: %v", err)
	}

	// The parent is a regular file, so the cache cannot be written.
	unwritable := filepath.Join(corrupt, "token-cache.json")
	if _, err := newTestCache(t, unwritable, src, "k", time.Now).Token(); err != nil {
		t.Fatalf("unwritable cache failed the command: %v", err)
	}

	// A signing failure still surfaces.
	failing := &countingSource{err: errors.New("denied"), now: time.Now}
	if _, err := newTestCache(t, filepath.Join(dir, "other.json"), failing, "k", time.Now).Token(); err == nil {
		t.Fatal("signing error was swallowed")
	}
}

func TestClearTokenCache(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	if err := ClearTokenCache(); err != nil {
		t.Fatalf("missing cache must not be an error: %v", err)
	}
	path, err := TokenCachePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte(`{"entries":{}}`)); err != nil {
		t.Fatal(err)
	}
	if err := ClearTokenCache(); err != nil {
		t.Fatalf("ClearTokenCache: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cache still present after clear: %v", err)
	}
}
