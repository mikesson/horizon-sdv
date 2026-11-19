// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package hostfeature

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

func mountHostRouter(svc *HostService, principal *auth.Principal) (chi.Router, *HostHandler) {
	r := chi.NewRouter()
	if principal != nil {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(auth.ContextWithPrincipal(req.Context(), principal)))
			})
		})
	}
	api := humachi.New(r, huma.DefaultConfig("Test Host API", "1.0.0"))
	h := NewHostHandler(svc)
	h.Register(api)
	return r, h
}

func TestHostHandler_CRUDAndLifecycleRoutes(t *testing.T) {
	t.Parallel()

	t.Run("GetHost returns 200 for existing host and 404 for unknown host", func(t *testing.T) {
		repo := newAtomicHostRepo(&host.Host{
			ID: testHostID, Name: "web-1", Zone: "europe-west4-a", MaxDevices: 4, Status: host.StatusRunning,
		})
		svc, _ := newTestService(t, repo, nil)
		router, _ := mountHostRouter(svc, &auth.Principal{Email: "user@example.com"})

		recOK := httptest.NewRecorder()
		router.ServeHTTP(recOK, httptest.NewRequest(http.MethodGet, "/v2/hosts/"+testHostID, nil))
		if recOK.Code != http.StatusOK {
			t.Fatalf("GET /v2/hosts/{id} = %d, want 200: %s", recOK.Code, recOK.Body.String())
		}
		var got host.Host
		if err := json.NewDecoder(recOK.Body).Decode(&got); err != nil {
			t.Fatalf("decode GetHost: %v", err)
		}
		if got.ID != testHostID || got.Name != "web-1" {
			t.Fatalf("unexpected GetHost response: %+v", got)
		}

		recMissing := httptest.NewRecorder()
		router.ServeHTTP(recMissing, httptest.NewRequest(http.MethodGet, "/v2/hosts/99999999-2222-3333-4444-555555555555", nil))
		if recMissing.Code != http.StatusNotFound {
			t.Fatalf("GET missing host = %d, want 404", recMissing.Code)
		}
	})

	t.Run("CreateHost requires principal and handles valid creation", func(t *testing.T) {
		repo := newAtomicHostRepo()
		svc, _ := newTestService(t, repo, nil)

		unauthRouter, _ := mountHostRouter(svc, nil)
		unauthReq := httptest.NewRequest(http.MethodPost, "/v2/hosts", bytes.NewBufferString(`{"name":"my-host","zone":"europe-west4-a","machine_type":"n1-standard-8","max_devices":4}`))
		unauthReq.Header.Set("Content-Type", "application/json")
		unauthRec := httptest.NewRecorder()
		unauthRouter.ServeHTTP(unauthRec, unauthReq)
		if unauthRec.Code != http.StatusUnauthorized {
			t.Fatalf("POST /v2/hosts without principal = %d, want 401", unauthRec.Code)
		}

		authRouter, _ := mountHostRouter(svc, &auth.Principal{Email: "user@example.com"})
		req := httptest.NewRequest(http.MethodPost, "/v2/hosts", bytes.NewBufferString(`{"name":"my-host","zone":"europe-west4-a","machine_type":"n1-standard-8","max_devices":4}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		authRouter.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST /v2/hosts = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var created host.Host
		if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
			t.Fatalf("decode CreateHost: %v", err)
		}
		if created.Name != "my-host" || created.Zone != "europe-west4-a" || !created.ZonePinned {
			t.Fatalf("unexpected CreateHost body: %+v", created)
		}
	})

	t.Run("UpdateHost rejects nil max_devices and handles valid updates and conflicts", func(t *testing.T) {
		repo := newAtomicHostRepo(&host.Host{
			ID: testHostID, Name: "web-1", Zone: "europe-west4-a", MaxDevices: 4, CurrentDeviceCount: 2, Status: host.StatusRunning,
		})
		svc, _ := newTestService(t, repo, nil)
		router, h := mountHostRouter(svc, &auth.Principal{Email: "user@example.com"})

		// Direct call with nil MaxDevices returns 400.
		_, err := h.UpdateHost(t.Context(), &UpdateHostInput{ID: testHostID})
		var se huma.StatusError
		if err == nil || !errorAsStatus(err, &se) || se.GetStatus() != http.StatusBadRequest {
			t.Fatalf("UpdateHost(nil max_devices) err = %v, want 400", err)
		}

		// Valid resize via HTTP PATCH.
		patchReq := httptest.NewRequest(http.MethodPatch, "/v2/hosts/"+testHostID, bytes.NewBufferString(`{"max_devices": 6}`))
		patchReq.Header.Set("Content-Type", "application/json")
		patchRec := httptest.NewRecorder()
		router.ServeHTTP(patchRec, patchReq)
		if patchRec.Code != http.StatusOK {
			t.Fatalf("PATCH /v2/hosts/{id} = %d, want 200: %s", patchRec.Code, patchRec.Body.String())
		}
		var updated host.Host
		if err := json.NewDecoder(patchRec.Body).Decode(&updated); err != nil {
			t.Fatalf("decode UpdateHost: %v", err)
		}
		if updated.MaxDevices != 6 {
			t.Fatalf("MaxDevices = %d, want 6", updated.MaxDevices)
		}

		// Resizing a terminal host fails with 409 Conflict.
		repo.Hosts[testHostID].Status = host.StatusDeleted
		conflictReq := httptest.NewRequest(http.MethodPatch, "/v2/hosts/"+testHostID, bytes.NewBufferString(`{"max_devices": 8}`))
		conflictReq.Header.Set("Content-Type", "application/json")
		conflictRec := httptest.NewRecorder()
		router.ServeHTTP(conflictRec, conflictReq)
		if conflictRec.Code != http.StatusConflict {
			t.Fatalf("PATCH terminal host = %d, want 409: %s", conflictRec.Code, conflictRec.Body.String())
		}
	})

	t.Run("Start, Stop, Sync, Extend, Retry, and Delete host endpoints route cleanly", func(t *testing.T) {
		now := time.Now()
		exp := now.Add(4 * time.Hour)
		fake := &fakeGCE{
			instanceJSON: `{"name":"cf-web-1","status":"RUNNING","networkInterfaces":[{"accessConfigs":[{"natIP":"203.0.113.10"}],"networkIP":"10.0.0.5"}]}`,
		}
		client := newFakeGCEClient(t, fake)

		repo := newAtomicHostRepo(&host.Host{
			ID: testHostID, Name: "web-1", Zone: "europe-west4-a", MaxDevices: 4,
			Status: host.StatusStopped, GCEInstanceName: "cf-web-1", CreatedAt: now,
			Lifecycle: host.HostLifecycle{ExpiresAt: &exp},
		})
		svc, _ := newTestService(t, repo, client)
		router, _ := mountHostRouter(svc, &auth.Principal{Email: "user@example.com"})

		// Start stopped host -> 200.
		startRec := httptest.NewRecorder()
		router.ServeHTTP(startRec, httptest.NewRequest(http.MethodPost, "/v2/hosts/"+testHostID+"/start", nil))
		if startRec.Code != http.StatusOK {
			t.Fatalf("POST /start = %d, want 200: %s", startRec.Code, startRec.Body.String())
		}

		// Sync running host -> 204.
		repo.Hosts[testHostID].Status = host.StatusRunning
		syncRec := httptest.NewRecorder()
		router.ServeHTTP(syncRec, httptest.NewRequest(http.MethodPost, "/v2/hosts/"+testHostID+"/sync", nil))
		if syncRec.Code != http.StatusNoContent {
			t.Fatalf("POST /sync = %d, want 204: %s", syncRec.Code, syncRec.Body.String())
		}

		// Extend running reserved host -> 200.
		extendReq := httptest.NewRequest(http.MethodPost, "/v2/hosts/"+testHostID+"/extend", bytes.NewBufferString(`{"hours": 10}`))
		extendReq.Header.Set("Content-Type", "application/json")
		extendRec := httptest.NewRecorder()
		router.ServeHTTP(extendRec, extendReq)
		if extendRec.Code != http.StatusOK {
			t.Fatalf("POST /extend = %d, want 200: %s", extendRec.Code, extendRec.Body.String())
		}

		// Stop running host -> 200.
		stopRec := httptest.NewRecorder()
		router.ServeHTTP(stopRec, httptest.NewRequest(http.MethodPost, "/v2/hosts/"+testHostID+"/stop", nil))
		if stopRec.Code != http.StatusOK {
			t.Fatalf("POST /stop = %d, want 200: %s", stopRec.Code, stopRec.Body.String())
		}

		// Retry non-retryable host -> 409, then preempted Spot host -> 200 fallback to STANDARD.
		retryConflictReq := httptest.NewRequest(http.MethodPost, "/v2/hosts/"+testHostID+"/retry", bytes.NewBufferString(`{}`))
		retryConflictReq.Header.Set("Content-Type", "application/json")
		retryConflictRec := httptest.NewRecorder()
		router.ServeHTTP(retryConflictRec, retryConflictReq)
		if retryConflictRec.Code != http.StatusConflict {
			t.Fatalf("POST /retry non-retryable = %d, want 409: %s", retryConflictRec.Code, retryConflictRec.Body.String())
		}

		repo.Hosts[testHostID].Status = host.StatusPreempted
		repo.Hosts[testHostID].ProvisioningModel = host.ProvisioningModelSpot
		retryReq := httptest.NewRequest(http.MethodPost, "/v2/hosts/"+testHostID+"/retry", bytes.NewBufferString(`{"fallback_to_standard": true}`))
		retryReq.Header.Set("Content-Type", "application/json")
		retryRec := httptest.NewRecorder()
		router.ServeHTTP(retryRec, retryReq)
		if retryRec.Code != http.StatusOK {
			t.Fatalf("POST /retry preempted = %d, want 200: %s", retryRec.Code, retryRec.Body.String())
		}

		// Delete host -> 204.
		repo.Hosts[testHostID].Status = host.StatusRunning
		delRec := httptest.NewRecorder()
		router.ServeHTTP(delRec, httptest.NewRequest(http.MethodDelete, "/v2/hosts/"+testHostID, nil))
		if delRec.Code != http.StatusNoContent {
			t.Fatalf("DELETE /v2/hosts/{id} = %d, want 204: %s", delRec.Code, delRec.Body.String())
		}
	})
}

func errorAsStatus(err error, target *huma.StatusError) bool {
	if se, ok := err.(huma.StatusError); ok {
		*target = se
		return true
	}
	return false
}
