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
	"context"
	"errors"
	"net/http"
	"testing"

	buildDomain "cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// Mock repository for testing
type mockBuildRepo struct {
	builds          map[string]*buildDomain.Build
	getErr          error
	getByBuildIDErr error
	createErr       error
	updateErr       error
	deleteErr       error
}

func newMockBuildRepo() *mockBuildRepo {
	return &mockBuildRepo{
		builds: make(map[string]*buildDomain.Build),
	}
}

func (m *mockBuildRepo) Get(ctx context.Context, id string) (*buildDomain.Build, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	b, ok := m.builds[id]
	if !ok {
		return nil, &buildDomain.NotFoundError{ID: id}
	}
	return b, nil
}

func (m *mockBuildRepo) GetByBuildID(ctx context.Context, buildID string) (*buildDomain.Build, error) {
	if m.getByBuildIDErr != nil {
		return nil, m.getByBuildIDErr
	}
	for _, b := range m.builds {
		if b.BuildID == buildID {
			return b, nil
		}
	}
	// Typed not-found, matching the Spanner repo contract.
	return nil, &buildDomain.NotFoundError{ID: buildID}
}

func (m *mockBuildRepo) GetByFingerprint(ctx context.Context, fingerprint string) (*buildDomain.Build, error) {
	for _, b := range m.builds {
		if b.Fingerprint == fingerprint {
			return b, nil
		}
	}
	// Typed not-found, matching the Spanner repo contract.
	return nil, &buildDomain.NotFoundError{ID: fingerprint}
}

func (m *mockBuildRepo) List(ctx context.Context, filter buildDomain.ListFilter) ([]*buildDomain.Build, error) {
	result := make([]*buildDomain.Build, 0, len(m.builds))
	for _, b := range m.builds {
		result = append(result, b)
	}
	return result, nil
}

func (m *mockBuildRepo) Create(ctx context.Context, b *buildDomain.Build) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.builds[b.ID] = b
	return nil
}

func (m *mockBuildRepo) Update(ctx context.Context, b *buildDomain.Build) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.builds[b.ID] = b
	return nil
}

func (m *mockBuildRepo) Delete(ctx context.Context, id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.builds, id)
	return nil
}

func (m *mockBuildRepo) Count(ctx context.Context, filter buildDomain.ListFilter) (int64, error) {
	return int64(len(m.builds)), nil
}

// Tests

func TestBuildService_Get(t *testing.T) {
	t.Parallel()
	repo := newMockBuildRepo()

	// Add a test build
	testBuild := &buildDomain.Build{
		ID:     "test-build-id",
		Name:   "Test Build",
		Target: "aosp_cf_x86_64_phone-userdebug",
		Source: buildDomain.SourceAB,
	}
	repo.builds[testBuild.ID] = testBuild

	svc, _ := NewBuildService(repo)

	t.Run("Success", func(t *testing.T) {
		b, err := svc.Get(t.Context(), "test-build-id")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if b.ID != testBuild.ID {
			t.Errorf("got build ID %s, want %s", b.ID, testBuild.ID)
		}
		if b.Name != testBuild.Name {
			t.Errorf("got build name %s, want %s", b.Name, testBuild.Name)
		}
	})

	t.Run("NotFound", func(t *testing.T) {
		_, err := svc.Get(t.Context(), "nonexistent")
		if err == nil {
			t.Error("expected error for nonexistent build")
		}
	})
}

func TestBuildService_GetByBuildID(t *testing.T) {
	t.Parallel()
	repo := newMockBuildRepo()

	// Add a test build
	testBuild := &buildDomain.Build{
		ID:      "test-build-id",
		Name:    "Test Build",
		BuildID: "12345678",
		Target:  "aosp_cf_x86_64_phone-userdebug",
		Source:  buildDomain.SourceAB,
	}
	repo.builds[testBuild.ID] = testBuild

	svc, _ := NewBuildService(repo)

	t.Run("Success", func(t *testing.T) {
		b, err := svc.GetByBuildID(t.Context(), "12345678")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if b.BuildID != "12345678" {
			t.Errorf("got build ID %s, want 12345678", b.BuildID)
		}
	})

	t.Run("NotFound", func(t *testing.T) {
		_, err := svc.GetByBuildID(t.Context(), "nonexistent")
		if err == nil {
			t.Error("expected error for nonexistent build ID")
		}
	})
}

func TestBuildService_List(t *testing.T) {
	t.Parallel()
	repo := newMockBuildRepo()

	// Add test builds
	repo.builds["build-1"] = &buildDomain.Build{
		ID:     "build-1",
		Name:   "Build One",
		Target: "aosp_cf_x86_64_phone-userdebug",
		Source: buildDomain.SourceAB,
	}
	repo.builds["build-2"] = &buildDomain.Build{
		ID:     "build-2",
		Name:   "Build Two",
		Target: "aosp_cf_x86_64_tablet-userdebug",
		Source: buildDomain.SourceURL,
	}

	svc, _ := NewBuildService(repo)

	builds, err := svc.List(t.Context(), buildDomain.ListFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(builds) != 2 {
		t.Errorf("got %d builds, want 2", len(builds))
	}
}

func TestBuildService_Register_Success(t *testing.T) {
	t.Parallel()
	repo := newMockBuildRepo()

	svc, _ := NewBuildService(repo)

	req := buildDomain.CreateRequest{
		Name:    "New Build",
		BuildID: "99999999",
		Target:  "aosp_cf_x86_64_phone-userdebug",
		Source:  buildDomain.SourceAB,
	}

	b, err := svc.Register(t.Context(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if b.ID == "" {
		t.Error("expected build ID to be set")
	}
	if b.Name != "New Build" {
		t.Errorf("got name %s, want New Build", b.Name)
	}

	// Verify build was stored
	if _, ok := repo.builds[b.ID]; !ok {
		t.Error("build not stored in repository")
	}
}

func TestBuildService_Register_ExistingBuild(t *testing.T) {
	t.Parallel()
	repo := newMockBuildRepo()

	// Add existing build
	existingBuild := &buildDomain.Build{
		ID:      "existing-id",
		Name:    "Existing Build",
		BuildID: "12345678",
		Target:  "aosp_cf_x86_64_phone-userdebug",
		Source:  buildDomain.SourceAB,
	}
	repo.builds[existingBuild.ID] = existingBuild

	svc, _ := NewBuildService(repo)

	// Try to register with same BuildID
	req := buildDomain.CreateRequest{
		Name:    "New Build",
		BuildID: "12345678", // Same as existing
		Target:  "aosp_cf_x86_64_phone-userdebug",
		Source:  buildDomain.SourceAB,
	}

	b, err := svc.Register(t.Context(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return the existing build
	if b.ID != existingBuild.ID {
		t.Errorf("got build ID %s, want existing ID %s", b.ID, existingBuild.ID)
	}
}

func TestBuildService_Register_URLFingerprintDedup(t *testing.T) {
	t.Parallel()
	repo := newMockBuildRepo()
	svc, _ := NewBuildService(repo)

	specs := []buildDomain.ArtifactSpec{
		{Type: buildDomain.ArtifactSpecTypeGCS, URL: "gs://b/x-img-1.zip", Role: buildDomain.ArtifactRoleDefaultBuild},
		{Type: buildDomain.ArtifactSpecTypeGCS, URL: "gs://b/cvd-host_package.tar.gz", Role: buildDomain.ArtifactRoleHostPackage},
	}
	req := buildDomain.CreateRequest{
		Name:          "Local push",
		Source:        buildDomain.SourceURL,
		ArtifactSpecs: specs,
	}

	first, err := svc.Register(t.Context(), req)
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	if first.Fingerprint == "" {
		t.Fatal("expected a derived fingerprint on a URL-source build")
	}

	// Same spec set (order reversed) must return the existing row, not a
	// duplicate. URL builds have no build_id for the usual idempotency.
	req.ArtifactSpecs = []buildDomain.ArtifactSpec{specs[1], specs[0]}
	second, err := svc.Register(t.Context(), req)
	if err != nil {
		t.Fatalf("second register: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("expected dedup to return existing build %s, got %s", first.ID, second.ID)
	}
	if len(repo.builds) != 1 {
		t.Errorf("expected 1 stored build after re-registration, got %d", len(repo.builds))
	}

	// A different artifact set is a different build.
	req.ArtifactSpecs = []buildDomain.ArtifactSpec{
		{Type: buildDomain.ArtifactSpecTypeGCS, URL: "gs://b/x-img-2.zip", Role: buildDomain.ArtifactRoleDefaultBuild},
		{Type: buildDomain.ArtifactSpecTypeGCS, URL: "gs://b/cvd-host_package.tar.gz", Role: buildDomain.ArtifactRoleHostPackage},
	}
	third, err := svc.Register(t.Context(), req)
	if err != nil {
		t.Fatalf("third register: %v", err)
	}
	if third.ID == first.ID {
		t.Error("expected a distinct build for a different artifact set")
	}
}

type fakeUploadStore struct {
	existing   map[string]bool
	minted     []string
	sessionURL string
}

func (f *fakeUploadStore) ObjectExists(ctx context.Context, bucket, name string) (bool, error) {
	return f.existing[name], nil
}

func (f *fakeUploadStore) StartResumableUpload(ctx context.Context, bucket, name, contentType string, sizeBytes int64) (string, error) {
	f.minted = append(f.minted, name)
	return f.sessionURL, nil
}

func TestBuildService_StartUpload(t *testing.T) {
	t.Parallel()
	const sha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	object := "staging/sha256/" + sha + "/x-img-1.zip"

	t.Run("mints a session for a new artifact", func(t *testing.T) {
		store := &fakeUploadStore{sessionURL: "https://storage.googleapis.com/upload/session-1"}
		svc, _ := NewBuildService(newMockBuildRepo(), WithUploadStaging(store, "bkt"))

		staged, err := svc.StartUpload(t.Context(), "x-img-1.zip", 42, sha, "", "user@example.com")
		if err != nil {
			t.Fatalf("StartUpload: %v", err)
		}
		if staged.AlreadyExists || staged.SessionURL != store.sessionURL {
			t.Errorf("staged = %+v, want a fresh session", staged)
		}
		if want := "gs://bkt/" + object; staged.ObjectURL != want {
			t.Errorf("ObjectURL = %q, want %q", staged.ObjectURL, want)
		}
		if len(store.minted) != 1 || store.minted[0] != object {
			t.Errorf("minted = %v, want the content-addressed object key", store.minted)
		}
	})

	t.Run("short-circuits an already-staged artifact", func(t *testing.T) {
		store := &fakeUploadStore{existing: map[string]bool{object: true}}
		svc, _ := NewBuildService(newMockBuildRepo(), WithUploadStaging(store, "bkt"))

		staged, err := svc.StartUpload(t.Context(), "x-img-1.zip", 42, sha, "", "user@example.com")
		if err != nil {
			t.Fatalf("StartUpload: %v", err)
		}
		if !staged.AlreadyExists || staged.SessionURL != "" {
			t.Errorf("staged = %+v, want already_exists with no session", staged)
		}
		if len(store.minted) != 0 {
			t.Error("a session was minted for an already-staged artifact")
		}
	})

	t.Run("rejects path separators in the filename", func(t *testing.T) {
		svc, _ := NewBuildService(newMockBuildRepo(), WithUploadStaging(&fakeUploadStore{}, "bkt"))
		if _, err := svc.StartUpload(t.Context(), "../escape.zip", 42, sha, "", "u"); err == nil {
			t.Error("expected a validation error for a slashy filename")
		}
	})

	t.Run("reports unavailable when staging is not configured", func(t *testing.T) {
		svc, _ := NewBuildService(newMockBuildRepo())
		_, err := svc.StartUpload(t.Context(), "x.zip", 42, sha, "", "u")
		var appErr *web.AppError
		if !errors.As(err, &appErr) || appErr.Code != http.StatusServiceUnavailable {
			t.Errorf("err = %v, want a 503 AppError", err)
		}
	})
}

func TestBuildService_Update_RecomputesFingerprint(t *testing.T) {
	t.Parallel()
	repo := newMockBuildRepo()
	svc, _ := NewBuildService(repo)

	first, err := svc.Register(t.Context(), buildDomain.CreateRequest{
		Name:   "Local push",
		Source: buildDomain.SourceURL,
		ArtifactSpecs: []buildDomain.ArtifactSpec{
			{Type: buildDomain.ArtifactSpecTypeGCS, URL: "gs://b/x-img-1.zip", Role: buildDomain.ArtifactRoleDefaultBuild},
			{Type: buildDomain.ArtifactSpecTypeGCS, URL: "gs://b/cvd-host_package.tar.gz", Role: buildDomain.ArtifactRoleHostPackage},
		},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// Capture by value: the mock repo hands out its stored pointer, so
	// reading first.Fingerprint after Update would see the mutation.
	originalFingerprint := first.Fingerprint

	newSpecs := []buildDomain.ArtifactSpec{
		{Type: buildDomain.ArtifactSpecTypeGCS, URL: "gs://b/x-img-2.zip", Role: buildDomain.ArtifactRoleDefaultBuild},
		{Type: buildDomain.ArtifactSpecTypeGCS, URL: "gs://b/cvd-host_package.tar.gz", Role: buildDomain.ArtifactRoleHostPackage},
	}
	updated, err := svc.Update(t.Context(), first.ID, buildDomain.UpdateRequest{ArtifactSpecs: newSpecs})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	// A stale fingerprint would let Register dedup a future registration of
	// the OLD spec set onto this row, which no longer holds those artifacts.
	if updated.Fingerprint == originalFingerprint {
		t.Error("expected the fingerprint to change with the spec set")
	}
	if want := buildDomain.SpecsFingerprint(newSpecs); updated.Fingerprint != want {
		t.Errorf("fingerprint = %q, want SpecsFingerprint of the new set %q", updated.Fingerprint, want)
	}
}

func TestBuildService_Register_LookupError_DoesNotCreate(t *testing.T) {
	t.Parallel()
	repo := newMockBuildRepo()
	repo.getByBuildIDErr = errors.New("spanner unavailable")

	svc, _ := NewBuildService(repo)

	_, err := svc.Register(t.Context(), buildDomain.CreateRequest{
		Name:    "New Build",
		BuildID: "12345678",
		Target:  "aosp_cf_x86_64_phone-userdebug",
		Source:  buildDomain.SourceAB,
	})
	if !errors.Is(err, repo.getByBuildIDErr) {
		t.Fatalf("expected wrapped lookup error, got %v", err)
	}

	// A transient lookup failure must never be treated as not-found: creating
	// here is what produces duplicate registrations.
	if len(repo.builds) != 0 {
		t.Errorf("expected no build created on lookup error, got %d", len(repo.builds))
	}
}

func TestBuildService_Register_ValidationError(t *testing.T) {
	t.Parallel()
	repo := newMockBuildRepo()

	svc, _ := NewBuildService(repo)

	// Missing required fields
	req := buildDomain.CreateRequest{
		Name:   "",
		Target: "",
		Source: buildDomain.SourceAB,
	}

	_, err := svc.Register(t.Context(), req)
	if err == nil {
		t.Error("expected validation error")
	}
}

func TestBuildService_Delete(t *testing.T) {
	t.Parallel()
	repo := newMockBuildRepo()

	// Add a build
	repo.builds["test-build"] = &buildDomain.Build{
		ID:     "test-build",
		Name:   "Test Build",
		Target: "aosp_cf_x86_64_phone-userdebug",
	}

	svc, _ := NewBuildService(repo)

	err := svc.Delete(t.Context(), "test-build")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify deleted
	if _, ok := repo.builds["test-build"]; ok {
		t.Error("build should have been deleted")
	}
}
