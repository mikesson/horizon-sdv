// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package quotafeature

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
)

type fakeQuotaService struct {
	getFn       func(ctx context.Context, scope quota.Scope, scopeID string) (*quota.Quota, error)
	setLimitFn  func(ctx context.Context, scope quota.Scope, scopeID string, maxHosts int64) error
	reconcileFn func(ctx context.Context) (int, error)

	getCalls      int
	setLimitCalls int
	lastScope     quota.Scope
	lastScopeID   string
	lastMaxHosts  int64
}

func (f *fakeQuotaService) Get(ctx context.Context, scope quota.Scope, scopeID string) (*quota.Quota, error) {
	f.getCalls++
	if f.getFn != nil {
		return f.getFn(ctx, scope, scopeID)
	}
	return nil, &quota.NotFoundError{Scope: scope, ScopeID: scopeID}
}

func (f *fakeQuotaService) SetLimit(ctx context.Context, scope quota.Scope, scopeID string, maxHosts int64) error {
	f.setLimitCalls++
	f.lastScope = scope
	f.lastScopeID = scopeID
	f.lastMaxHosts = maxHosts
	if f.setLimitFn != nil {
		return f.setLimitFn(ctx, scope, scopeID, maxHosts)
	}
	return nil
}

func (f *fakeQuotaService) ReconcileAll(ctx context.Context) (int, error) {
	if f.reconcileFn != nil {
		return f.reconcileFn(ctx)
	}
	return 0, nil
}

type recordingAuditSink struct {
	entries []audit.Entry
}

func (s *recordingAuditSink) LogAsync(entry audit.Entry) {
	s.entries = append(s.entries, entry)
}

func mountQuotaRouter(svc quota.Service, sink audit.Sink, principal *auth.Principal) (chi.Router, *QuotaHandler) {
	r := chi.NewRouter()
	if principal != nil {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(auth.ContextWithPrincipal(req.Context(), principal)))
			})
		})
	}
	api := humachi.New(r, huma.DefaultConfig("Test Quota API", "1.0.0"))
	h := NewQuotaHandler(svc)
	h.SetAuditLogger(sink)
	h.Register(api)
	return r, h
}

func TestQuotaHandler_GetCurrentAndPlatformQuota(t *testing.T) {
	t.Parallel()

	peakAt := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	svc := &fakeQuotaService{
		getFn: func(_ context.Context, scope quota.Scope, scopeID string) (*quota.Quota, error) {
			assert.Equal(t, quota.ScopePlatform, scope)
			assert.Equal(t, quota.GlobalScopeID, scopeID)
			return &quota.Quota{
				Scope:              quota.ScopePlatform,
				ScopeID:            quota.GlobalScopeID,
				MaxHosts:           20,
				UsedHosts:          5,
				HighWaterUsedHosts: 8,
				HighWaterAt:        &peakAt,
			}, nil
		},
	}
	router, _ := mountQuotaRouter(svc, nil, nil)

	for _, path := range []string{"/v2/quota", "/v2/quota/platform"} {
		t.Run("GET "+path+" returns serialized quota", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code)
			var body QuotaResponse
			require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
			assert.Equal(t, string(quota.ScopePlatform), body.Scope)
			assert.Equal(t, quota.GlobalScopeID, body.ScopeID)
			assert.Equal(t, int64(20), body.MaxHosts)
			assert.Equal(t, int64(5), body.UsedHosts)
			assert.Equal(t, int64(15), body.Available)
			assert.InDelta(t, 25.0, body.Percentage, 0.001)
			assert.Equal(t, int64(8), body.HighWaterUsedHosts)
			require.NotNil(t, body.HighWaterAt)
			assert.True(t, body.HighWaterAt.Equal(peakAt))
		})
	}

	t.Run("GET returns 404 on NotFoundError", func(t *testing.T) {
		notFoundSvc := &fakeQuotaService{
			getFn: func(_ context.Context, scope quota.Scope, scopeID string) (*quota.Quota, error) {
				return nil, &quota.NotFoundError{Scope: scope, ScopeID: scopeID}
			},
		}
		nfRouter, _ := mountQuotaRouter(notFoundSvc, nil, nil)

		req := httptest.NewRequest(http.MethodGet, "/v2/quota", nil)
		rec := httptest.NewRecorder()
		nfRouter.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)

		reqPlatform := httptest.NewRequest(http.MethodGet, "/v2/quota/platform", nil)
		recPlatform := httptest.NewRecorder()
		nfRouter.ServeHTTP(recPlatform, reqPlatform)
		assert.Equal(t, http.StatusNotFound, recPlatform.Code)
	})

	t.Run("GET returns 500 on unexpected repository error", func(t *testing.T) {
		errSvc := &fakeQuotaService{
			getFn: func(_ context.Context, _ quota.Scope, _ string) (*quota.Quota, error) {
				return nil, errors.New("spanner unavailable")
			},
		}
		errRouter, _ := mountQuotaRouter(errSvc, nil, nil)

		req := httptest.NewRequest(http.MethodGet, "/v2/quota/platform", nil)
		rec := httptest.NewRecorder()
		errRouter.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

func TestQuotaHandler_UpdatePlatformQuota(t *testing.T) {
	t.Parallel()

	t.Run("happy path updates limit once, returns recomputed usage, and emits audit entry", func(t *testing.T) {
		sink := &recordingAuditSink{}
		peakAt := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
		svc := &fakeQuotaService{
			getFn: func(_ context.Context, _ quota.Scope, _ string) (*quota.Quota, error) {
				return &quota.Quota{
					Scope:              quota.ScopePlatform,
					ScopeID:            quota.GlobalScopeID,
					MaxHosts:           10,
					UsedHosts:          4,
					HighWaterUsedHosts: 7,
					HighWaterAt:        &peakAt,
				}, nil
			},
		}
		router, _ := mountQuotaRouter(svc, sink, &auth.Principal{Email: "admin@example.com"})

		req := httptest.NewRequest(http.MethodPut, "/v2/quota/platform", bytes.NewBufferString(`{"max_hosts": 40}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		// Single pre-update Get + single SetLimit; no post-write Get that could fail after commit.
		assert.Equal(t, 1, svc.getCalls)
		assert.Equal(t, 1, svc.setLimitCalls)
		assert.Equal(t, quota.ScopePlatform, svc.lastScope)
		assert.Equal(t, quota.GlobalScopeID, svc.lastScopeID)
		assert.Equal(t, int64(40), svc.lastMaxHosts)

		var body QuotaResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
		assert.Equal(t, int64(40), body.MaxHosts)
		assert.Equal(t, int64(4), body.UsedHosts)
		assert.Equal(t, int64(36), body.Available)
		assert.InDelta(t, 10.0, body.Percentage, 0.001)
		assert.Equal(t, int64(7), body.HighWaterUsedHosts)

		require.Len(t, sink.entries, 1)
		entry := sink.entries[0]
		assert.Equal(t, audit.ActionQuotaUpdate, entry.Action)
		assert.Equal(t, audit.ResourceQuota, entry.ResourceType)
		require.NotNil(t, entry.ResourceID)
		assert.Equal(t, quota.GlobalScopeID, *entry.ResourceID)
		assert.Equal(t, "admin@example.com", entry.ActorEmail)
		assert.Equal(t, int64(10), entry.Metadata["max_hosts_before"])
		assert.Equal(t, int64(40), entry.Metadata["max_hosts_after"])
	})

	t.Run("nil audit logger succeeds without panicking", func(t *testing.T) {
		svc := &fakeQuotaService{
			getFn: func(_ context.Context, _ quota.Scope, _ string) (*quota.Quota, error) {
				return &quota.Quota{
					Scope:     quota.ScopePlatform,
					ScopeID:   quota.GlobalScopeID,
					MaxHosts:  10,
					UsedHosts: 2,
				}, nil
			},
		}
		router, h := mountQuotaRouter(svc, &recordingAuditSink{}, nil)
		h.SetAuditLogger(nil)

		req := httptest.NewRequest(http.MethodPut, "/v2/quota/platform", bytes.NewBufferString(`{"max_hosts": 25}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var body QuotaResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
		assert.Equal(t, int64(25), body.MaxHosts)
		assert.Equal(t, int64(23), body.Available)
	})

	t.Run("negative limit rejected before read or write via HTTP and direct call", func(t *testing.T) {
		sink := &recordingAuditSink{}
		svc := &fakeQuotaService{
			getFn: func(_ context.Context, _ quota.Scope, _ string) (*quota.Quota, error) {
				return &quota.Quota{Scope: quota.ScopePlatform, ScopeID: quota.GlobalScopeID, MaxHosts: 10}, nil
			},
		}
		router, h := mountQuotaRouter(svc, sink, nil)

		req := httptest.NewRequest(http.MethodPut, "/v2/quota/platform", bytes.NewBufferString(`{"max_hosts": -1}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

		input := &UpdatePlatformQuotaInput{}
		input.Body.MaxHosts = -1
		out, err := h.UpdatePlatformQuota(t.Context(), input)
		require.Error(t, err)
		assert.Nil(t, out)
		var statusErr huma.StatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, http.StatusBadRequest, statusErr.GetStatus())
		assert.Equal(t, 0, svc.getCalls)
		assert.Equal(t, 0, svc.setLimitCalls)
		assert.Empty(t, sink.entries)
	})

	t.Run("SetLimit error aborts without logging audit entry", func(t *testing.T) {
		sink := &recordingAuditSink{}
		svc := &fakeQuotaService{
			getFn: func(_ context.Context, _ quota.Scope, _ string) (*quota.Quota, error) {
				return &quota.Quota{Scope: quota.ScopePlatform, ScopeID: quota.GlobalScopeID, MaxHosts: 10}, nil
			},
			setLimitFn: func(_ context.Context, _ quota.Scope, _ string, _ int64) error {
				return errors.New("transaction aborted")
			},
		}
		router, _ := mountQuotaRouter(svc, sink, nil)

		req := httptest.NewRequest(http.MethodPut, "/v2/quota/platform", bytes.NewBufferString(`{"max_hosts": 50}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Equal(t, 1, svc.setLimitCalls)
		assert.Empty(t, sink.entries)
	})

	t.Run("missing quota row returns 404 before SetLimit or audit", func(t *testing.T) {
		sink := &recordingAuditSink{}
		svc := &fakeQuotaService{}
		router, _ := mountQuotaRouter(svc, sink, nil)

		req := httptest.NewRequest(http.MethodPut, "/v2/quota/platform", bytes.NewBufferString(`{"max_hosts": 50}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, 0, svc.setLimitCalls)
		assert.Empty(t, sink.entries)
	})
}
