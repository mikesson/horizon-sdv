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

package buildfeature

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	buildDomain "cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// pagingBuildRepo records the list filter and honors its limit and keyset
// cursor, so the handler's trim and token logic sees real page boundaries.
type pagingBuildRepo struct {
	*mockBuildRepo
	lastFilter *buildDomain.ListFilter
}

func (p *pagingBuildRepo) List(_ context.Context, filter buildDomain.ListFilter) ([]*buildDomain.Build, error) {
	p.lastFilter = &filter
	rows := make([]*buildDomain.Build, 0, len(p.builds))
	for _, b := range p.builds {
		rows = append(rows, b)
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].CreatedAt.After(rows[j].CreatedAt)
		}
		return rows[i].ID > rows[j].ID
	})
	var out []*buildDomain.Build
	for _, b := range rows {
		if filter.AfterID != "" {
			after := b.CreatedAt.Before(filter.AfterCreatedAt) ||
				(b.CreatedAt.Equal(filter.AfterCreatedAt) && b.ID < filter.AfterID)
			if !after {
				continue
			}
		}
		out = append(out, b)
	}
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func newPagingHandler(t *testing.T, ids ...string) (*BuildHandler, *pagingBuildRepo) {
	t.Helper()
	repo := &pagingBuildRepo{mockBuildRepo: newMockBuildRepo()}
	created := time.Date(2026, 9, 1, 12, 0, 0, 123456000, time.UTC)
	for _, id := range ids {
		repo.builds[id] = &buildDomain.Build{ID: id, CreatedAt: created}
	}
	svc, err := NewBuildService(repo)
	if err != nil {
		t.Fatalf("NewBuildService: %v", err)
	}
	return NewBuildHandler(svc), repo
}

func listIDs(out *ListBuildsOutput) []string {
	ids := make([]string, 0, len(out.Body.Items))
	for _, b := range out.Body.Items {
		ids = append(ids, b.ID)
	}
	return ids
}

func requireStatus(t *testing.T, err error, want int) {
	t.Helper()
	var se huma.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want a status error", err)
	}
	if got := se.GetStatus(); got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestBuildHandler_ListBuilds_LastPage(t *testing.T) {
	t.Parallel()
	h, repo := newPagingHandler(t, "b-1", "b-2")

	out, err := h.ListBuilds(t.Context(), &ListBuildsInput{Limit: 5})
	if err != nil {
		t.Fatalf("ListBuilds: %v", err)
	}
	if repo.lastFilter == nil || repo.lastFilter.Limit != 6 {
		t.Fatalf("repository limit = %+v, want 6", repo.lastFilter)
	}
	if len(out.Body.Items) != 2 {
		t.Fatalf("items = %v, want 2", listIDs(out))
	}
	if out.Body.NextPageToken != "" {
		t.Fatalf("next_page_token = %q, want empty", out.Body.NextPageToken)
	}
}

func TestBuildHandler_ListBuilds_TokenWalk(t *testing.T) {
	t.Parallel()
	h, _ := newPagingHandler(t, "b-1", "b-2", "b-3")

	var seen []string
	token := ""
	for range 5 {
		out, err := h.ListBuilds(t.Context(), &ListBuildsInput{Limit: 2, PageToken: token})
		if err != nil {
			t.Fatalf("ListBuilds: %v", err)
		}
		if len(out.Body.Items) > 2 {
			t.Fatalf("page holds %d rows, want at most 2", len(out.Body.Items))
		}
		seen = append(seen, listIDs(out)...)
		token = out.Body.NextPageToken
		if token == "" {
			break
		}
	}
	want := []string{"b-3", "b-2", "b-1"}
	if len(seen) != len(want) {
		t.Fatalf("walked %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("walked %v, want %v", seen, want)
		}
	}
}

func TestBuildHandler_ListBuilds_TokenCursor(t *testing.T) {
	t.Parallel()
	h, repo := newPagingHandler(t, "b-1", "b-2")

	first, err := h.ListBuilds(t.Context(), &ListBuildsInput{Limit: 1})
	if err != nil {
		t.Fatalf("ListBuilds: %v", err)
	}
	if first.Body.NextPageToken == "" {
		t.Fatal("a full page must carry a next_page_token")
	}
	if _, err := h.ListBuilds(t.Context(), &ListBuildsInput{Limit: 1, PageToken: first.Body.NextPageToken}); err != nil {
		t.Fatalf("ListBuilds: %v", err)
	}
	last := first.Body.Items[0]
	if repo.lastFilter.AfterID != last.ID || !repo.lastFilter.AfterCreatedAt.Equal(last.CreatedAt) {
		t.Fatalf("cursor = (%v, %q), want (%v, %q)",
			repo.lastFilter.AfterCreatedAt, repo.lastFilter.AfterID, last.CreatedAt, last.ID)
	}
}

func TestBuildHandler_ListBuilds_TokenWithOffset(t *testing.T) {
	t.Parallel()
	h, repo := newPagingHandler(t, "b-1")
	token := web.EncodePageToken(web.PageCursor{CreatedAt: time.Now(), ID: "b-1"})

	_, err := h.ListBuilds(t.Context(), &ListBuildsInput{Limit: 20, Offset: 20, PageToken: token})
	requireStatus(t, err, http.StatusBadRequest)
	if repo.lastFilter != nil {
		t.Fatal("the rejection must precede any repository read")
	}
}

func TestBuildHandler_ListBuilds_MalformedToken(t *testing.T) {
	t.Parallel()
	h, repo := newPagingHandler(t, "b-1")

	_, err := h.ListBuilds(t.Context(), &ListBuildsInput{Limit: 20, PageToken: "not-a-token"})
	requireStatus(t, err, http.StatusBadRequest)
	if repo.lastFilter != nil {
		t.Fatal("the rejection must precede any repository read")
	}
}

type stubHandlerUploadStore struct {
	exists       bool
	existsErr    error
	sessionURL   string
	startErr     error
	lastBucket   string
	lastObject   string
	lastMIMEType string
	lastSize     int64
}

func (f *stubHandlerUploadStore) ObjectExists(_ context.Context, bucket, name string) (bool, error) {
	f.lastBucket = bucket
	f.lastObject = name
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.exists, nil
}

func (f *stubHandlerUploadStore) StartResumableUpload(_ context.Context, bucket, name, contentType string, sizeBytes int64) (string, error) {
	f.lastBucket = bucket
	f.lastObject = name
	f.lastMIMEType = contentType
	f.lastSize = sizeBytes
	if f.startErr != nil {
		return "", f.startErr
	}
	return f.sessionURL, nil
}

func mountBuildRouter(svc *BuildService, principal *auth.Principal) chi.Router {
	r := chi.NewRouter()
	if principal != nil {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(auth.ContextWithPrincipal(req.Context(), principal)))
			})
		})
	}
	api := humachi.New(r, huma.DefaultConfig("Test Build API", "1.0.0"))
	NewBuildHandler(svc).Register(api)
	return r
}

func TestBuildHandler_CRUDAndUploadRoutes(t *testing.T) {
	t.Parallel()

	const testBuildUUID = "11111111-2222-3333-4444-555555555555"
	const sha256Hex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	t.Run("Create, Get, Update, and Delete build round-trip via HTTP", func(t *testing.T) {
		repo := newMockBuildRepo()
		svc, err := NewBuildService(repo)
		if err != nil {
			t.Fatalf("NewBuildService: %v", err)
		}
		router := mountBuildRouter(svc, &auth.Principal{Email: "dev@example.com"})

		// Create URL build.
		createBody := `{
			"name": "aaos-trunk",
			"source": "url",
			"artifact_specs": [
				{"type": "gcs", "url": "gs://bucket/aosp_cf-img.zip", "role": "default_build"},
				{"type": "gcs", "url": "gs://bucket/cvd-host_package.tar.gz", "role": "host_package"}
			],
			"description": "initial build"
		}`
		req := httptest.NewRequest(http.MethodPost, "/v2/builds", bytes.NewBufferString(createBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST /v2/builds status=%d body=%s", rec.Code, rec.Body.String())
		}
		var created buildDomain.Build
		if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
			t.Fatalf("decode created build: %v", err)
		}
		if created.ID == "" || created.CreatedBy == nil || *created.CreatedBy != "dev@example.com" || created.Name != "aaos-trunk" {
			t.Fatalf("unexpected created build: %+v", created)
		}

		// Get existing & missing builds.
		getReq := httptest.NewRequest(http.MethodGet, "/v2/builds/"+created.ID, nil)
		getRec := httptest.NewRecorder()
		router.ServeHTTP(getRec, getReq)
		if getRec.Code != http.StatusOK {
			t.Fatalf("GET /v2/builds/{id} status=%d body=%s", getRec.Code, getRec.Body.String())
		}

		missingReq := httptest.NewRequest(http.MethodGet, "/v2/builds/"+testBuildUUID, nil)
		missingRec := httptest.NewRecorder()
		router.ServeHTTP(missingRec, missingReq)
		if missingRec.Code != http.StatusNotFound {
			t.Fatalf("GET missing status=%d, want 404", missingRec.Code)
		}

		// Update build mutable fields + replacement artifact specs.
		patchBody := `{
			"name": "aaos-trunk-v2",
			"description": "updated build",
			"is_default": true,
			"artifact_specs": [
				{"type": "https", "url": "https://storage.googleapis.com/bucket/aosp_cf-img-v2.zip", "role": "default_build"},
				{"type": "https", "url": "https://storage.googleapis.com/bucket/cvd-host_package-v2.tar.gz", "role": "host_package"}
			]
		}`
		patchReq := httptest.NewRequest(http.MethodPatch, "/v2/builds/"+created.ID, bytes.NewBufferString(patchBody))
		patchReq.Header.Set("Content-Type", "application/json")
		patchRec := httptest.NewRecorder()
		router.ServeHTTP(patchRec, patchReq)
		if patchRec.Code != http.StatusOK {
			t.Fatalf("PATCH /v2/builds/{id} status=%d body=%s", patchRec.Code, patchRec.Body.String())
		}
		var updated buildDomain.Build
		if err := json.NewDecoder(patchRec.Body).Decode(&updated); err != nil {
			t.Fatalf("decode updated build: %v", err)
		}
		if updated.Name != "aaos-trunk-v2" || !updated.IsDefault || len(updated.ArtifactSpecs) != 2 {
			t.Fatalf("unexpected updated build: %+v", updated)
		}

		// Delete build.
		delReq := httptest.NewRequest(http.MethodDelete, "/v2/builds/"+created.ID, nil)
		delRec := httptest.NewRecorder()
		router.ServeHTTP(delRec, delReq)
		if delRec.Code != http.StatusNoContent {
			t.Fatalf("DELETE /v2/builds/{id} status=%d, want 204", delRec.Code)
		}
		if _, exists := repo.builds[created.ID]; exists {
			t.Fatal("build still present in repo after DELETE")
		}
	})

	t.Run("CreateBuild and StartBuildUpload require principal", func(t *testing.T) {
		svc, err := NewBuildService(newMockBuildRepo())
		if err != nil {
			t.Fatalf("NewBuildService: %v", err)
		}
		router := mountBuildRouter(svc, nil)

		req := httptest.NewRequest(http.MethodPost, "/v2/builds", bytes.NewBufferString(`{"name":"b","source":"android_build","build_id":"123","target":"cf_x86_64_phone-userdebug"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("POST /v2/builds without principal=%d, want 401", rec.Code)
		}

		upReq := httptest.NewRequest(http.MethodPost, "/v2/builds/uploads", bytes.NewBufferString(`{"file_name":"img.zip","size_bytes":100,"sha256":"`+sha256Hex+`"}`))
		upReq.Header.Set("Content-Type", "application/json")
		upRec := httptest.NewRecorder()
		router.ServeHTTP(upRec, upReq)
		if upRec.Code != http.StatusUnauthorized {
			t.Fatalf("POST /v2/builds/uploads without principal=%d, want 401", upRec.Code)
		}
	})

	t.Run("StartBuildUpload handles unconfigured staging, existing object, and minted session", func(t *testing.T) {
		noStageSvc, err := NewBuildService(newMockBuildRepo())
		if err != nil {
			t.Fatalf("NewBuildService: %v", err)
		}
		noStageRouter := mountBuildRouter(noStageSvc, &auth.Principal{Email: "dev@example.com"})
		upBody := `{"file_name":"img.zip","size_bytes":1024,"sha256":"` + sha256Hex + `","content_type":"application/zip"}`

		req503 := httptest.NewRequest(http.MethodPost, "/v2/builds/uploads", bytes.NewBufferString(upBody))
		req503.Header.Set("Content-Type", "application/json")
		rec503 := httptest.NewRecorder()
		noStageRouter.ServeHTTP(rec503, req503)
		if rec503.Code != http.StatusServiceUnavailable {
			t.Fatalf("unconfigured StartBuildUpload status=%d, want 503", rec503.Code)
		}

		store := &stubHandlerUploadStore{exists: true}
		stageSvc, err := NewBuildService(newMockBuildRepo(), WithUploadStaging(store, "staging-bkt"))
		if err != nil {
			t.Fatalf("NewBuildService: %v", err)
		}
		stageRouter := mountBuildRouter(stageSvc, &auth.Principal{Email: "dev@example.com"})

		reqExists := httptest.NewRequest(http.MethodPost, "/v2/builds/uploads", bytes.NewBufferString(upBody))
		reqExists.Header.Set("Content-Type", "application/json")
		recExists := httptest.NewRecorder()
		stageRouter.ServeHTTP(recExists, reqExists)
		if recExists.Code != http.StatusCreated {
			t.Fatalf("existing StartBuildUpload status=%d body=%s", recExists.Code, recExists.Body.String())
		}
		var outExists struct {
			ObjectURL     string `json:"object_url"`
			SessionURL    string `json:"session_url"`
			AlreadyExists bool   `json:"already_exists"`
		}
		if err := json.NewDecoder(recExists.Body).Decode(&outExists); err != nil {
			t.Fatalf("decode upload output: %v", err)
		}
		if !outExists.AlreadyExists || outExists.SessionURL != "" || outExists.ObjectURL == "" {
			t.Fatalf("unexpected already-exists payload: %+v", outExists)
		}

		store.exists = false
		store.sessionURL = "https://upload.example.com/session/abc"
		reqNew := httptest.NewRequest(http.MethodPost, "/v2/builds/uploads", bytes.NewBufferString(upBody))
		reqNew.Header.Set("Content-Type", "application/json")
		recNew := httptest.NewRecorder()
		stageRouter.ServeHTTP(recNew, reqNew)
		if recNew.Code != http.StatusCreated {
			t.Fatalf("new session StartBuildUpload status=%d body=%s", recNew.Code, recNew.Body.String())
		}
		var outNew struct {
			ObjectURL     string `json:"object_url"`
			SessionURL    string `json:"session_url"`
			AlreadyExists bool   `json:"already_exists"`
		}
		if err := json.NewDecoder(recNew.Body).Decode(&outNew); err != nil {
			t.Fatalf("decode upload output: %v", err)
		}
		if outNew.AlreadyExists || outNew.SessionURL != store.sessionURL {
			t.Fatalf("unexpected minted session payload: %+v", outNew)
		}
	})

	t.Run("DeleteBuild and UpdateBuild surface repository not-found and error codes", func(t *testing.T) {
		repo := newMockBuildRepo()
		svc, err := NewBuildService(repo)
		if err != nil {
			t.Fatalf("NewBuildService: %v", err)
		}
		router := mountBuildRouter(svc, &auth.Principal{Email: "dev@example.com"})

		patchReq := httptest.NewRequest(http.MethodPatch, "/v2/builds/"+testBuildUUID, bytes.NewBufferString(`{"name":"new"}`))
		patchReq.Header.Set("Content-Type", "application/json")
		patchRec := httptest.NewRecorder()
		router.ServeHTTP(patchRec, patchReq)
		if patchRec.Code != http.StatusNotFound {
			t.Fatalf("PATCH missing status=%d, want 404", patchRec.Code)
		}

		repo.deleteErr = errors.New("spanner failure")
		delReq := httptest.NewRequest(http.MethodDelete, "/v2/builds/"+testBuildUUID, nil)
		delRec := httptest.NewRecorder()
		router.ServeHTTP(delRec, delReq)
		if delRec.Code != http.StatusInternalServerError {
			t.Fatalf("DELETE error status=%d, want 500", delRec.Code)
		}
	})
}
