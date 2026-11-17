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

package main

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"cloud-cuttlefish-orchestrator/internal/platform/relay/turnrs"
)

// fakeOrchestrator stands in for the authorize endpoint and counts calls, so
// the tests can see the cache working rather than infer it.
type fakeOrchestrator struct {
	*httptest.Server
	calls  atomic.Int64
	status atomic.Int64
	key    []byte
	expiry atomic.Int64
	delay  atomic.Int64 // nanoseconds
}

func newFakeOrchestrator(t *testing.T, key []byte, expiry time.Time) *fakeOrchestrator {
	t.Helper()
	f := &fakeOrchestrator{key: key}
	f.status.Store(http.StatusOK)
	f.expiry.Store(expiry.Unix())
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if d := f.delay.Load(); d > 0 {
			time.Sleep(time.Duration(d))
		}
		code := int(f.status.Load())
		if code != http.StatusOK {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"detail":"refused"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(authorizeResponse{
			Key:       base64.StdEncoding.EncodeToString(f.key),
			ExpiresAt: f.expiry.Load(),
		})
	}))
	t.Cleanup(f.Close)
	return f
}

func newTestHook(t *testing.T, f *fakeOrchestrator, now time.Time, maxCache time.Duration) *hookService {
	t.Helper()
	svc := newHookService(&authority{
		client:   f.Client(),
		endpoint: f.URL,
		timeout:  800 * time.Millisecond,
	}, maxCache, slog.New(slog.DiscardHandler))
	svc.now = func() time.Time { return now }
	return svc
}

func request(username string, algorithm turnrs.PasswordAlgorithm) *turnrs.GetTurnPasswordRequest {
	return &turnrs.GetTurnPasswordRequest{
		Username:  username,
		Realm:     "cco.test",
		Algorithm: algorithm,
	}
}

func TestGetPasswordReturnsKey(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	want := []byte("0123456789abcdef")
	f := newFakeOrchestrator(t, want, now.Add(time.Hour))
	hook := newTestHook(t, f, now, 5*time.Minute)

	got, err := hook.GetPassword(t.Context(), request("1769003600:u@e.com:dev-1", turnrs.PasswordAlgorithm_PASSWORD_ALGORITHM_MD5))
	if err != nil {
		t.Fatalf("GetPassword() error = %v, want nil", err)
	}
	if string(got.GetPassword()) != string(want) {
		t.Errorf("GetPassword() = %q, want %q", got.GetPassword(), want)
	}
}

// A refusal must deny, and must not be cached: the next call has to ask again.
func TestGetPasswordDeniesAndDoesNotCacheRefusals(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	f := newFakeOrchestrator(t, []byte("key"), now.Add(time.Hour))
	f.status.Store(http.StatusForbidden)
	hook := newTestHook(t, f, now, 5*time.Minute)

	for i := range 3 {
		_, err := hook.GetPassword(t.Context(), request("1769003600:u@e.com:dev-1", turnrs.PasswordAlgorithm_PASSWORD_ALGORITHM_MD5))
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("call %d: GetPassword() code = %v, want PermissionDenied", i, status.Code(err))
		}
	}
	if got := f.calls.Load(); got != 3 {
		t.Errorf("orchestrator calls = %d, want 3 (refusals must not be cached)", got)
	}
}

// An unreachable orchestrator must also deny. The relay reads any error as
// "no password", so failing closed is the whole safety property.
func TestGetPasswordDeniesWhenOrchestratorUnavailable(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	f := newFakeOrchestrator(t, []byte("key"), now.Add(time.Hour))
	f.status.Store(http.StatusInternalServerError)
	hook := newTestHook(t, f, now, 5*time.Minute)

	_, err := hook.GetPassword(t.Context(), request("1769003600:u@e.com:dev-1", turnrs.PasswordAlgorithm_PASSWORD_ALGORITHM_MD5))
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("GetPassword() code = %v, want PermissionDenied", status.Code(err))
	}
}

// The cache is what makes the relay's one-second budget reachable.
func TestGetPasswordCachesWithinCredentialLifetime(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	f := newFakeOrchestrator(t, []byte("key"), now.Add(time.Hour))
	hook := newTestHook(t, f, now, 5*time.Minute)

	for range 5 {
		if _, err := hook.GetPassword(t.Context(), request("1769003600:u@e.com:dev-1", turnrs.PasswordAlgorithm_PASSWORD_ALGORITHM_MD5)); err != nil {
			t.Fatalf("GetPassword() error = %v", err)
		}
	}
	if got := f.calls.Load(); got != 1 {
		t.Errorf("orchestrator calls = %d, want 1 (subsequent calls must be cached)", got)
	}
}

// Realm and algorithm are part of the digest, so they must not share an entry.
func TestGetPasswordCacheKeyCoversRealmAndAlgorithm(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	f := newFakeOrchestrator(t, []byte("key"), now.Add(time.Hour))
	hook := newTestHook(t, f, now, 5*time.Minute)

	md5Req := request("1769003600:u@e.com:dev-1", turnrs.PasswordAlgorithm_PASSWORD_ALGORITHM_MD5)
	shaReq := request("1769003600:u@e.com:dev-1", turnrs.PasswordAlgorithm_PASSWORD_ALGORITHM_SHA256)
	otherRealm := request("1769003600:u@e.com:dev-1", turnrs.PasswordAlgorithm_PASSWORD_ALGORITHM_MD5)
	otherRealm.Realm = "elsewhere"

	for _, req := range []*turnrs.GetTurnPasswordRequest{md5Req, shaReq, otherRealm} {
		if _, err := hook.GetPassword(t.Context(), req); err != nil {
			t.Fatalf("GetPassword() error = %v", err)
		}
	}
	if got := f.calls.Load(); got != 3 {
		t.Errorf("orchestrator calls = %d, want 3 (realm and algorithm must not share a cache entry)", got)
	}
}

// Nothing may outlive the credential it came from, however long the ceiling is.
func TestCacheNeverOutlivesTheCredential(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	credentialExpiry := now.Add(30 * time.Second)
	f := newFakeOrchestrator(t, []byte("key"), credentialExpiry)
	hook := newTestHook(t, f, now, time.Hour) // Ceiling far beyond the credential.

	req := request("1769000030:u@e.com:dev-1", turnrs.PasswordAlgorithm_PASSWORD_ALGORITHM_MD5)
	if _, err := hook.GetPassword(t.Context(), req); err != nil {
		t.Fatalf("GetPassword() error = %v", err)
	}

	hook.now = func() time.Time { return credentialExpiry }
	if _, err := hook.GetPassword(t.Context(), req); err != nil {
		t.Fatalf("GetPassword() error = %v", err)
	}
	if got := f.calls.Load(); got != 2 {
		t.Errorf("orchestrator calls = %d, want 2 (an expired entry must be refetched)", got)
	}
}

// The ceiling caps a long-lived credential, bounding how long a revocation
// upstream goes unnoticed.
func TestCacheCeilingCapsLongCredentials(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	f := newFakeOrchestrator(t, []byte("key"), now.Add(24*time.Hour))
	hook := newTestHook(t, f, now, time.Minute)

	req := request("1769086400:u@e.com:dev-1", turnrs.PasswordAlgorithm_PASSWORD_ALGORITHM_MD5)
	if _, err := hook.GetPassword(t.Context(), req); err != nil {
		t.Fatalf("GetPassword() error = %v", err)
	}
	hook.now = func() time.Time { return now.Add(time.Minute) }
	if _, err := hook.GetPassword(t.Context(), req); err != nil {
		t.Fatalf("GetPassword() error = %v", err)
	}
	if got := f.calls.Load(); got != 2 {
		t.Errorf("orchestrator calls = %d, want 2 (the ceiling must cap the entry)", got)
	}
}

// The relay's generated client fixes a one-second per-request timeout that
// overrides its own config. Every answer has to fit inside it.
func TestGetPasswordFitsTheRelayBudget(t *testing.T) {
	const relayBudget = time.Second

	now := time.Unix(1_769_000_000, 0)
	f := newFakeOrchestrator(t, []byte("key"), now.Add(time.Hour))
	hook := newTestHook(t, f, now, 5*time.Minute)
	req := request("1769003600:u@e.com:dev-1", turnrs.PasswordAlgorithm_PASSWORD_ALGORITHM_MD5)

	start := time.Now()
	if _, err := hook.GetPassword(t.Context(), req); err != nil {
		t.Fatalf("cold GetPassword() error = %v", err)
	}
	if cold := time.Since(start); cold >= relayBudget {
		t.Errorf("cold call took %v, want under the relay's %v budget", cold, relayBudget)
	}

	start = time.Now()
	for range 100 {
		if _, err := hook.GetPassword(t.Context(), req); err != nil {
			t.Fatalf("cached GetPassword() error = %v", err)
		}
	}
	if warm := time.Since(start); warm >= relayBudget {
		t.Errorf("100 cached calls took %v, want well under the relay's %v budget", warm, relayBudget)
	}
}

// An orchestrator slower than the budget must be abandoned, not waited on:
// holding the call past a second lets the relay time out on its own terms.
func TestSlowOrchestratorIsAbandonedBeforeTheBudget(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	f := newFakeOrchestrator(t, []byte("key"), now.Add(time.Hour))
	f.delay.Store(int64(1500 * time.Millisecond))
	hook := newTestHook(t, f, now, 5*time.Minute)

	start := time.Now()
	_, err := hook.GetPassword(t.Context(), request("1769003600:u@e.com:dev-1", turnrs.PasswordAlgorithm_PASSWORD_ALGORITHM_MD5))
	elapsed := time.Since(start)

	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("GetPassword() code = %v, want PermissionDenied", status.Code(err))
	}
	if elapsed >= time.Second {
		t.Errorf("slow call returned after %v, want under 1s", elapsed)
	}
}

func TestUnsupportedAlgorithmIsRejected(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	f := newFakeOrchestrator(t, []byte("key"), now.Add(time.Hour))
	hook := newTestHook(t, f, now, 5*time.Minute)

	_, err := hook.GetPassword(t.Context(), request("1769003600:u@e.com:dev-1", turnrs.PasswordAlgorithm(99)))
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("GetPassword() code = %v, want InvalidArgument", status.Code(err))
	}
	if got := f.calls.Load(); got != 0 {
		t.Errorf("orchestrator calls = %d, want 0 (rejected before any call)", got)
	}
}

func TestRequireLoopbackRefusesExposedAddresses(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:3001", "10.0.0.5:3001", "relay.example.com:3001"} {
		if err := requireLoopback(addr); err == nil {
			t.Errorf("requireLoopback(%q) = nil, want an error", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:3001", "[::1]:3001"} {
		if err := requireLoopback(addr); err != nil {
			t.Errorf("requireLoopback(%q) = %v, want nil", addr, err)
		}
	}
}

func TestValidateOrchestratorRefusesPlaintext(t *testing.T) {
	for _, raw := range []string{"", "http://cco.example.com", "not a url", "ftp://x"} {
		if _, err := validateOrchestrator(raw); err == nil {
			t.Errorf("validateOrchestrator(%q) = nil, want an error", raw)
		}
	}
	got, err := validateOrchestrator("https://cco.example.com/")
	if err != nil || got != "https://cco.example.com" {
		t.Errorf("validateOrchestrator() = %q, %v; want trailing slash trimmed", got, err)
	}
}
