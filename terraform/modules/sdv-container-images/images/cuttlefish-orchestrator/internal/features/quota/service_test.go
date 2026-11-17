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

package quotafeature

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/quota"
)

// mockQuotaRepo implements quota.Repository for testing.
type mockQuotaRepo struct {
	quotas map[string]*quota.Quota
	setErr error
}

func newMockQuotaRepo() *mockQuotaRepo {
	return &mockQuotaRepo{
		quotas: make(map[string]*quota.Quota),
	}
}

func (m *mockQuotaRepo) key(scope quota.Scope, scopeID string) string {
	return string(scope) + "/" + scopeID
}

func (m *mockQuotaRepo) Get(ctx context.Context, scope quota.Scope, scopeID string) (*quota.Quota, error) {
	q, ok := m.quotas[m.key(scope, scopeID)]
	if !ok {
		return nil, &quota.NotFoundError{Scope: scope, ScopeID: scopeID}
	}
	return q, nil
}

// Set mirrors the Spanner repo contract: create when absent, otherwise
// update only the limit. used_hosts is never written to an existing row.
func (m *mockQuotaRepo) Set(ctx context.Context, q *quota.Quota) error {
	if m.setErr != nil {
		return m.setErr
	}
	if existing, ok := m.quotas[m.key(q.Scope, q.ScopeID)]; ok {
		existing.MaxHosts = q.MaxHosts
		return nil
	}
	m.quotas[m.key(q.Scope, q.ScopeID)] = q
	return nil
}

// mockReconciler implements UsedHostsReconciler for testing: the
// transactional recount itself is exercised against the emulator in the host
// feature's tests; here only the service's delegation and logging matter.
type mockReconciler struct {
	counter, actual int64
	err             error
	calls           int
}

func (m *mockReconciler) ReconcileUsedHosts(context.Context) (int64, int64, error) {
	m.calls++
	if m.err != nil {
		return 0, 0, m.err
	}
	return m.counter, m.actual, nil
}

func TestSetLimit_CreatesWhenMissing(t *testing.T) {
	t.Parallel()
	repo := newMockQuotaRepo()
	svc := NewService(repo, nil, slog.Default())

	if err := svc.SetLimit(t.Context(), quota.ScopePlatform, quota.GlobalScopeID, 42); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	q, err := repo.Get(t.Context(), quota.ScopePlatform, quota.GlobalScopeID)
	if err != nil {
		t.Fatalf("quota not created: %v", err)
	}
	if q.MaxHosts != 42 {
		t.Errorf("expected MaxHosts 42, got %d", q.MaxHosts)
	}
	if q.UsedHosts != 0 {
		t.Errorf("expected UsedHosts 0, got %d", q.UsedHosts)
	}
}

func TestSetLimit_PreservesUsedHosts(t *testing.T) {
	t.Parallel()
	repo := newMockQuotaRepo()
	repo.quotas["platform/global"] = &quota.Quota{
		Scope:     quota.ScopePlatform,
		ScopeID:   quota.GlobalScopeID,
		MaxHosts:  10,
		UsedHosts: 5,
	}
	svc := NewService(repo, nil, slog.Default())

	if err := svc.SetLimit(t.Context(), quota.ScopePlatform, quota.GlobalScopeID, 50); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	q, _ := repo.Get(t.Context(), quota.ScopePlatform, quota.GlobalScopeID)
	if q.MaxHosts != 50 {
		t.Errorf("expected MaxHosts 50, got %d", q.MaxHosts)
	}
	if q.UsedHosts != 5 {
		t.Errorf("expected UsedHosts 5 to be preserved, got %d", q.UsedHosts)
	}
}

func TestSetLimit_RepoError(t *testing.T) {
	t.Parallel()
	repo := newMockQuotaRepo()
	repo.setErr = errors.New("spanner unavailable")
	svc := NewService(repo, nil, slog.Default())

	err := svc.SetLimit(t.Context(), quota.ScopePlatform, quota.GlobalScopeID, 42)
	if !errors.Is(err, repo.setErr) {
		t.Fatalf("expected wrapped repo error, got %v", err)
	}
}

func TestReconcileAll_NoHostRepo(t *testing.T) {
	t.Parallel()
	repo := newMockQuotaRepo()
	svc := NewService(repo, nil, slog.Default())

	// Should fail without a host counter configured.
	_, err := svc.ReconcileAll(t.Context())
	if err == nil {
		t.Fatal("expected error when host repo not configured")
	}
}

func TestReconcileAll_NoDrift(t *testing.T) {
	t.Parallel()
	rec := &mockReconciler{counter: 5, actual: 5}
	svc := NewService(newMockQuotaRepo(), rec, slog.Default())

	corrected, err := svc.ReconcileAll(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if corrected != 0 {
		t.Errorf("expected 0 corrections, got %d", corrected)
	}
	if rec.calls != 1 {
		t.Errorf("expected 1 reconciler call, got %d", rec.calls)
	}
}

func TestReconcileAll_PlatformDrift(t *testing.T) {
	t.Parallel()
	rec := &mockReconciler{counter: 7, actual: 3}
	svc := NewService(newMockQuotaRepo(), rec, slog.Default())

	corrected, err := svc.ReconcileAll(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if corrected != 1 {
		t.Errorf("expected 1 correction, got %d", corrected)
	}
}

func TestReconcileAll_ReconcilerError(t *testing.T) {
	t.Parallel()
	rec := &mockReconciler{err: errors.New("spanner unavailable")}
	svc := NewService(newMockQuotaRepo(), rec, slog.Default())

	_, err := svc.ReconcileAll(t.Context())
	if !errors.Is(err, rec.err) {
		t.Fatalf("expected wrapped reconciler error, got %v", err)
	}
}
