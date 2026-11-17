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

package buildfeature_test

import (
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/build"
	buildfeature "cloud-cuttlefish-orchestrator/internal/features/build"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// urlSpecs returns the minimum valid spec set for a URL-source build,
// namespaced by prefix so fingerprints stay distinct across fixtures.
func urlSpecs(prefix string) []build.ArtifactSpec {
	return []build.ArtifactSpec{
		{Type: build.ArtifactSpecTypeGCS, URL: "gs://bucket/" + prefix + "/x-img-1.zip", Role: build.ArtifactRoleDefaultBuild},
		{Type: build.ArtifactSpecTypeGCS, URL: "gs://bucket/" + prefix + "/cvd-host_package.tar.gz", Role: build.ArtifactRoleHostPackage},
	}
}

// TestBuildRepository_CRUD tests basic CRUD operations.
func TestBuildRepository_CRUD(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	repo := buildfeature.NewBuildRepository(db.Client)

	t.Run("Create", func(t *testing.T) {
		b := &build.Build{
			ID:      "build-test-001",
			Name:    "Test Build",
			BuildID: "P12345678",
			Target:  "aosp_cf_x86_64_phone-userdebug",
			Branch:  "aosp-main",
			Source:  build.SourceAB,
		}

		err := repo.Create(ctx, b)
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
	})

	t.Run("Get", func(t *testing.T) {
		b, err := repo.Get(ctx, "build-test-001")
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}

		if b.Name != "Test Build" {
			t.Errorf("Expected name 'Test Build', got %q", b.Name)
		}
		if b.Target != "aosp_cf_x86_64_phone-userdebug" {
			t.Errorf("Expected target 'aosp_cf_x86_64_phone-userdebug', got %q", b.Target)
		}
	})

	t.Run("Update", func(t *testing.T) {
		b, err := repo.Get(ctx, "build-test-001")
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}

		b.Description = "Updated description"

		err = repo.Update(ctx, b)
		if err != nil {
			t.Fatalf("Update failed: %v", err)
		}

		// Verify update.
		updated, err := repo.Get(ctx, "build-test-001")
		if err != nil {
			t.Fatalf("Get after update failed: %v", err)
		}
		if updated.Description != "Updated description" {
			t.Errorf("Expected description 'Updated description', got %q", updated.Description)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		err := repo.Delete(ctx, "build-test-001")
		if err != nil {
			t.Fatalf("Delete failed: %v", err)
		}

		// Verify deletion.
		_, err = repo.Get(ctx, "build-test-001")
		if err == nil {
			t.Error("Expected error when getting deleted build")
		}
	})
}

// TestBuildRepository_GetByBuildID tests retrieval by Android build ID.
func TestBuildRepository_GetByBuildID(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	repo := buildfeature.NewBuildRepository(db.Client)

	// Create test build.
	b := &build.Build{
		ID:      "build-android-001",
		Name:    "Android Build",
		BuildID: "AB123456789",
		Target:  "aosp_cf_x86_64_phone-userdebug",
		Source:  build.SourceAB,
	}
	if err := repo.Create(ctx, b); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	t.Run("Found", func(t *testing.T) {
		found, err := repo.GetByBuildID(ctx, "AB123456789")
		if err != nil {
			t.Fatalf("GetByBuildID failed: %v", err)
		}
		if found.ID != "build-android-001" {
			t.Errorf("Expected ID 'build-android-001', got %q", found.ID)
		}
	})

	t.Run("NotFound", func(t *testing.T) {
		_, err := repo.GetByBuildID(ctx, "nonexistent")
		if err == nil {
			t.Error("Expected error for nonexistent build ID")
		}
	})
}

// TestBuildRepository_GetByFingerprint covers the fingerprint round-trip
// Register's URL-build dedup depends on.
func TestBuildRepository_GetByFingerprint(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	repo := buildfeature.NewBuildRepository(db.Client)

	specs := urlSpecs("fp")
	b := &build.Build{
		ID:            "build-url-001",
		Name:          "Pushed Build",
		Source:        build.SourceURL,
		ArtifactSpecs: specs,
		Fingerprint:   build.SpecsFingerprint(specs),
	}
	if err := repo.Create(ctx, b); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	t.Run("Found", func(t *testing.T) {
		found, err := repo.GetByFingerprint(ctx, b.Fingerprint)
		if err != nil {
			t.Fatalf("GetByFingerprint failed: %v", err)
		}
		if found.ID != b.ID {
			t.Errorf("Expected ID %q, got %q", b.ID, found.ID)
		}
	})

	t.Run("NotFound", func(t *testing.T) {
		_, err := repo.GetByFingerprint(ctx, "no-such-fingerprint")
		if err == nil {
			t.Error("Expected error for unknown fingerprint")
		}
	})
}

// TestBuildRepository_List tests listing builds with filters.
func TestBuildRepository_List(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	repo := buildfeature.NewBuildRepository(db.Client)

	// Create test builds.
	builds := []*build.Build{
		{ID: "b1", Name: "Build 1", Target: "target-a", Branch: "aosp-main", Source: build.SourceAB},
		{ID: "b2", Name: "Build 2", Target: "target-a", Branch: "aosp-main", Source: build.SourceAB},
		{ID: "b3", Name: "Build 3", Target: "target-b", Source: build.SourceURL, ArtifactSpecs: urlSpecs("b3")},
		{ID: "b4", Name: "Build 4", Target: "target-b", Source: build.SourceURL, ArtifactSpecs: urlSpecs("b4")},
	}

	for _, b := range builds {
		if err := repo.Create(ctx, b); err != nil {
			t.Fatalf("Failed to create build %s: %v", b.ID, err)
		}
	}

	t.Run("ListAll", func(t *testing.T) {
		result, err := repo.List(ctx, build.ListFilter{})
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}
		if len(result) != 4 {
			t.Errorf("Expected 4 builds, got %d", len(result))
		}
	})

	t.Run("ListByTarget", func(t *testing.T) {
		target := "target-a"
		result, err := repo.List(ctx, build.ListFilter{Target: &target})
		if err != nil {
			t.Fatalf("List with target filter failed: %v", err)
		}
		if len(result) != 2 {
			t.Errorf("Expected 2 builds for target-a, got %d", len(result))
		}
	})

	t.Run("ListBySource", func(t *testing.T) {
		source := build.SourceURL
		result, err := repo.List(ctx, build.ListFilter{Source: &source})
		if err != nil {
			t.Fatalf("List with source filter failed: %v", err)
		}
		if len(result) != 2 {
			t.Errorf("Expected 2 URL builds, got %d", len(result))
		}
	})

	t.Run("ListWithLimit", func(t *testing.T) {
		result, err := repo.List(ctx, build.ListFilter{Limit: 2})
		if err != nil {
			t.Fatalf("List with limit failed: %v", err)
		}
		if len(result) != 2 {
			t.Errorf("Expected 2 builds with limit, got %d", len(result))
		}
	})
}

// TestBuildRepository_GetNotFound tests the not found error.
func TestBuildRepository_GetNotFound(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	repo := buildfeature.NewBuildRepository(db.Client)

	_, err := repo.Get(ctx, "nonexistent-id")
	if err == nil {
		t.Error("Expected error when getting nonexistent build")
	}
}

// TestBuildRepository_ListOrderIsTotal pins the order a paging client depends
// on. With created_at tied (registrations in one script share a timestamp)
// build_id decides, so walking the list one row at a time visits each build
// exactly once instead of leaving the order to the scan.
func TestBuildRepository_ListOrderIsTotal(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	repo := buildfeature.NewBuildRepository(db.Client)
	ids := []string{"page-build-1", "page-build-2"}
	for _, id := range ids {
		b := &build.Build{ID: id, Name: id, BuildID: "P1000", Target: "aosp_cf_x86_64_phone-userdebug", Branch: "aosp-main", Source: build.SourceAB}
		if err := repo.Create(ctx, b); err != nil {
			t.Fatalf("Create(%s): %v", id, err)
		}
	}

	// Create stamps its own timestamp, so collapse them afterwards: distinct
	// timestamps would decide the order and the tie would never be exercised.
	shared := time.Now().UTC().Truncate(time.Microsecond)
	muts := make([]*spanner.Mutation, 0, len(ids))
	for _, id := range ids {
		muts = append(muts, spanner.UpdateMap("builds", map[string]any{"build_id": id, "created_at": shared}))
	}
	if _, err := db.Client.Apply(ctx, muts); err != nil {
		t.Fatalf("collapsing created_at: %v", err)
	}

	full, err := repo.List(ctx, build.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	gotFull := make([]string, len(full))
	for i, b := range full {
		gotFull[i] = b.ID
	}
	if want := []string{"page-build-2", "page-build-1"}; !slices.Equal(gotFull, want) {
		t.Errorf("List order under equal created_at = %v, want %v", gotFull, want)
	}

	paged := make([]string, 0, len(ids))
	for offset := range ids {
		page, err := repo.List(ctx, build.ListFilter{Limit: 1, Offset: offset})
		if err != nil {
			t.Fatalf("List(offset=%d): %v", offset, err)
		}
		if len(page) != 1 {
			t.Fatalf("List(offset=%d) returned %d rows, want 1", offset, len(page))
		}
		paged = append(paged, page[0].ID)
	}
	if !slices.Equal(paged, gotFull) {
		t.Errorf("one-row pages walked %v, want %v; offset paging skipped or repeated a row", paged, gotFull)
	}
}

// TestBuildRepository_ListKeyset walks the list by keyset cursor while a newer
// build lands between pages. Two rows share a created_at, so the walk only
// visits each row once if the build_id tiebreak is in the predicate, and the
// insert would shift an offset walk onto a row it already returned.
func TestBuildRepository_ListKeyset(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	repo := buildfeature.NewBuildRepository(db.Client)
	create := func(id string, at time.Time) {
		t.Helper()
		b := &build.Build{ID: id, Name: id, BuildID: "K" + id, Target: "aosp_cf_x86_64_phone-userdebug", Branch: "aosp-main", Source: build.SourceAB}
		if err := repo.Create(ctx, b); err != nil {
			t.Fatalf("Create(%s): %v", id, err)
		}
		if _, err := db.Client.Apply(ctx, []*spanner.Mutation{spanner.UpdateMap("builds", map[string]any{
			"build_id": id, "created_at": at,
		})}); err != nil {
			t.Fatalf("setting created_at of %s: %v", id, err)
		}
	}

	tied := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	create("keyset-build-0", tied.Add(-time.Second))
	create("keyset-build-1", tied)
	create("keyset-build-2", tied)

	var walked []string
	filter := build.ListFilter{Limit: 1}
	for page := 0; ; page++ {
		if page > 5 {
			t.Fatalf("keyset walk did not end; walked %v", walked)
		}
		rows, err := repo.List(ctx, filter)
		if err != nil {
			t.Fatalf("List(page %d): %v", page, err)
		}
		if len(rows) == 0 {
			break
		}
		walked = append(walked, rows[0].ID)
		filter.AfterCreatedAt, filter.AfterID = rows[0].CreatedAt, rows[0].ID
		if page == 0 {
			create("keyset-build-new", time.Now().UTC())
		}
	}
	if want := []string{"keyset-build-2", "keyset-build-1", "keyset-build-0"}; !slices.Equal(walked, want) {
		t.Errorf("keyset walk = %v, want %v", walked, want)
	}
}
