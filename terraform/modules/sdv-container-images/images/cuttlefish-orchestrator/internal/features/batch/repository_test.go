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

package batchfeature_test

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/batch"
	batchfeature "cloud-cuttlefish-orchestrator/internal/features/batch"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

func testBatch(id string) *batch.Batch {
	return &batch.Batch{
		ID:              id,
		NamePrefix:      "ci",
		RequestedCount:  3,
		FillPolicy:      batch.FillAtomic,
		Placement:       batch.PlacementIsolated,
		ReadyTimeout:    10 * time.Minute,
		SpecFingerprint: "fp-" + id,
		CreatedBy:       "ci@example.com",
		CreatedAt:       time.Now().UTC(),
	}
}

// seedMember inserts a bare device row tagged with the batch id (raw mutation:
// the repository under test owns batches, not devices).
func seedMember(t *testing.T, db *spannertest.TestDB, hostID, deviceID, batchID, status string) {
	t.Helper()
	mut := spanner.InsertMap("devices", map[string]any{
		"host_id":    hostID,
		"device_id":  deviceID,
		"name":       deviceID,
		"status":     status,
		"batch_id":   batchID,
		"created_at": time.Now(),
		"updated_at": time.Now(),
	})
	if _, err := db.Client.Apply(t.Context(), []*spanner.Mutation{mut}); err != nil {
		t.Fatalf("seeding member device: %v", err)
	}
}

func TestBatchRepository_CreateGetRoundtrip(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := batchfeature.NewRepository(db.Client)
	ctx := t.Context()

	want := testBatch("batch-rt-001")
	if err := repo.Create(ctx, want); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A keyed retry colliding on the deterministic id reports the loss so the
	// service can fingerprint-check the winner's row and resume against it.
	if err := repo.Create(ctx, want); !errors.Is(err, batchfeature.ErrBatchExists) {
		t.Fatalf("Create replay = %v, want ErrBatchExists", err)
	}

	got, err := repo.Get(ctx, want.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.NamePrefix != want.NamePrefix || got.RequestedCount != want.RequestedCount ||
		got.FillPolicy != want.FillPolicy || got.Placement != want.Placement ||
		got.ReadyTimeout != want.ReadyTimeout || got.CreatedBy != want.CreatedBy ||
		got.SpecFingerprint != want.SpecFingerprint {
		t.Errorf("roundtrip mismatch: got %+v want %+v", got, want)
	}
	if got.ReadyAt != nil || got.FailedAt != nil || got.Settled() {
		t.Errorf("fresh batch should be unsettled: %+v", got)
	}

	if _, err := repo.Get(ctx, "batch-absent"); !errors.Is(err, batch.ErrNotFound) {
		t.Errorf("Get absent = %v, want ErrNotFound", err)
	}
}

func TestBatchRepository_SettleCASReady(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := batchfeature.NewRepository(db.Client)
	ctx := t.Context()
	now := time.Now().UTC()

	ready := testBatch("batch-cas-ready")
	if err := repo.Create(ctx, ready); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if won, err := repo.MarkReady(ctx, ready.ID, now); err != nil || !won {
		t.Fatalf("MarkReady first = (%v, %v), want win", won, err)
	}
	if won, err := repo.MarkReady(ctx, ready.ID, now); err != nil || won {
		t.Fatalf("MarkReady repeat = (%v, %v), want lose", won, err)
	}
	// A filled batch can never be retroactively failed.
	if won, err := repo.MarkFailed(ctx, ready.ID, now, "too late"); err != nil || won {
		t.Fatalf("MarkFailed after ready = (%v, %v), want lose", won, err)
	}
}

// TestBatchRepository_SettleCASConcurrent races MarkReady against MarkFailed
// on the same unsettled batch: the WHERE-guarded UPDATE must elect exactly one
// winner, so concurrent sweepers (leader churn) can never double-judge a
// batch. This is the transactional race the CAS exists for. The sequential
// tests above cannot catch a guard that only holds without contention.
func TestBatchRepository_SettleCASConcurrent(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := batchfeature.NewRepository(db.Client)
	ctx := t.Context()

	for round := range 5 {
		b := testBatch(fmt.Sprintf("batch-cas-race-%d", round))
		if err := repo.Create(ctx, b); err != nil {
			t.Fatalf("Create: %v", err)
		}
		now := time.Now().UTC()
		var readyWon, failedWon bool
		var readyErr, failedErr error
		var wg sync.WaitGroup
		wg.Go(func() { readyWon, readyErr = repo.MarkReady(ctx, b.ID, now) })
		wg.Go(func() { failedWon, failedErr = repo.MarkFailed(ctx, b.ID, now, "raced") })
		wg.Wait()
		if readyErr != nil || failedErr != nil {
			t.Fatalf("settle race errors: ready=%v failed=%v", readyErr, failedErr)
		}
		if readyWon == failedWon {
			t.Fatalf("round %d: exactly one settle must win, got ready=%v failed=%v",
				round, readyWon, failedWon)
		}
	}
}

func TestBatchRepository_SettleCASFailed(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := batchfeature.NewRepository(db.Client)
	ctx := t.Context()
	now := time.Now().UTC()

	failed := testBatch("batch-cas-failed")
	if err := repo.Create(ctx, failed); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if won, err := repo.MarkFailed(ctx, failed.ID, now, "member cf-001 permanently failed"); err != nil || !won {
		t.Fatalf("MarkFailed first = (%v, %v), want win", won, err)
	}
	if won, err := repo.MarkFailed(ctx, failed.ID, now, "again"); err != nil || won {
		t.Fatalf("MarkFailed repeat = (%v, %v), want lose", won, err)
	}
	got, err := repo.Get(ctx, failed.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.FailedAt == nil || got.FailureReason != "member cf-001 permanently failed" {
		t.Errorf("failed batch = %+v, want failure marker + reason", got)
	}
}

// TestBatchRepository_ListByCreator tests the created_by scoping behind the
// mine/created_by list params.
func TestBatchRepository_ListByCreator(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := batchfeature.NewRepository(db.Client)
	ctx := t.Context()

	own := testBatch("batch-creator-own")
	foreign := testBatch("batch-creator-foreign")
	foreign.CreatedBy = "other@example.com"
	for _, b := range []*batch.Batch{own, foreign} {
		if err := repo.Create(ctx, b); err != nil {
			t.Fatalf("Create %s: %v", b.ID, err)
		}
	}

	all, err := repo.List(ctx, batch.ListFilter{})
	if err != nil {
		t.Fatalf("List unscoped: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("List unscoped returned %d batches, want 2", len(all))
	}

	creator := "ci@example.com"
	scoped, err := repo.List(ctx, batch.ListFilter{CreatedBy: &creator})
	if err != nil {
		t.Fatalf("List scoped: %v", err)
	}
	if len(scoped) != 1 || scoped[0].ID != own.ID {
		t.Errorf("List(created_by=ci) = %+v, want exactly %s", scoped, own.ID)
	}
}

func TestBatchRepository_ListUnsettledAtomic(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := batchfeature.NewRepository(db.Client)
	ctx := t.Context()
	now := time.Now().UTC()

	unsettled := testBatch("batch-sweep-unsettled")
	settled := testBatch("batch-sweep-settled")
	bestEffort := testBatch("batch-sweep-besteffort")
	bestEffort.FillPolicy = batch.FillBestEffort
	for _, b := range []*batch.Batch{unsettled, settled, bestEffort} {
		if err := repo.Create(ctx, b); err != nil {
			t.Fatalf("Create %s: %v", b.ID, err)
		}
	}
	if _, err := repo.MarkReady(ctx, settled.ID, now); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}

	got, err := repo.ListUnsettledAtomic(ctx)
	if err != nil {
		t.Fatalf("ListUnsettledAtomic: %v", err)
	}
	if len(got) != 1 || got[0].ID != unsettled.ID {
		t.Errorf("ListUnsettledAtomic = %+v, want exactly %s", got, unsettled.ID)
	}
}

func TestBatchRepository_ListEmptyBefore(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := batchfeature.NewRepository(db.Client)
	ctx := t.Context()

	hostID := "host-batch-gc"
	if err := db.SeedHost(ctx, hostID, "gc-host", "eu-west4-a"); err != nil {
		t.Fatalf("seeding host: %v", err)
	}

	old := time.Now().UTC().Add(-2 * time.Hour)
	empty := testBatch("batch-gc-empty")
	empty.CreatedAt = old
	withLive := testBatch("batch-gc-live")
	withLive.CreatedAt = old
	drained := testBatch("batch-gc-drained")
	drained.CreatedAt = old
	failedEmpty := testBatch("batch-gc-failed")
	failedEmpty.CreatedAt = old
	fresh := testBatch("batch-gc-fresh") // inside the grace window
	for _, b := range []*batch.Batch{empty, withLive, drained, failedEmpty, fresh} {
		if err := repo.Create(ctx, b); err != nil {
			t.Fatalf("Create %s: %v", b.ID, err)
		}
	}
	if won, err := repo.MarkFailed(ctx, failedEmpty.ID, time.Now().UTC(), "swept"); err != nil || !won {
		t.Fatalf("MarkFailed: %v", err)
	}
	seedMember(t, db, hostID, "dev-gc-live", withLive.ID, "running")
	// Deleted members do not keep a batch alive: teardown leaves rows in
	// 'deleted' until the tombstone TTL or host cascade removes them.
	seedMember(t, db, hostID, "dev-gc-gone", drained.ID, "deleted")

	cutoff := time.Now().UTC().Add(-time.Hour)
	ids, err := repo.ListEmptyBefore(ctx, cutoff, 10)
	if err != nil {
		t.Fatalf("ListEmptyBefore: %v", err)
	}
	want := map[string]bool{empty.ID: true, drained.ID: true}
	if len(ids) != len(want) {
		t.Fatalf("ListEmptyBefore = %v, want %v", ids, want)
	}
	for _, id := range ids {
		if !want[id] {
			t.Errorf("unexpected GC candidate %s (got %v)", id, ids)
		}
	}

	if err := repo.Delete(ctx, empty.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := repo.Delete(ctx, empty.ID); err != nil {
		t.Fatalf("Delete repeat: %v", err)
	}
	if _, err := repo.Get(ctx, empty.ID); !errors.Is(err, batch.ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
}

// ListFailedWithLiveMembers finds exactly the tripped batches whose teardown
// died mid-way: failed, with members still outside deleting/deleted. A
// cleanly torn-down failed batch and an unsettled batch stay invisible.
func TestBatchRepository_ListFailedWithLiveMembers(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := batchfeature.NewRepository(db.Client)
	ctx := t.Context()

	hostID := "host-batch-resume"
	if err := db.SeedHost(ctx, hostID, "resume-host", "eu-west4-a"); err != nil {
		t.Fatalf("seeding host: %v", err)
	}

	orphaned := testBatch("batch-resume-orphaned") // failed, live member
	cleaned := testBatch("batch-resume-cleaned")   // failed, teardown finished
	unsettled := testBatch("batch-resume-open")    // not failed, live member
	for _, b := range []*batch.Batch{orphaned, cleaned, unsettled} {
		if err := repo.Create(ctx, b); err != nil {
			t.Fatalf("Create %s: %v", b.ID, err)
		}
	}
	for _, id := range []string{orphaned.ID, cleaned.ID} {
		if won, err := repo.MarkFailed(ctx, id, time.Now().UTC(), "swept"); err != nil || !won {
			t.Fatalf("MarkFailed %s: won=%v err=%v", id, won, err)
		}
	}
	seedMember(t, db, hostID, "dev-resume-live", orphaned.ID, "running")
	seedMember(t, db, hostID, "dev-resume-going", cleaned.ID, "deleting")
	seedMember(t, db, hostID, "dev-resume-gone", cleaned.ID, "deleted")
	seedMember(t, db, hostID, "dev-resume-open", unsettled.ID, "running")

	ids, err := repo.ListFailedWithLiveMembers(ctx, 10)
	if err != nil {
		t.Fatalf("ListFailedWithLiveMembers: %v", err)
	}
	if len(ids) != 1 || ids[0] != orphaned.ID {
		t.Errorf("ListFailedWithLiveMembers = %v, want [%s]", ids, orphaned.ID)
	}
}

// The settle loop's three listings hold the salted-sweep contract (see
// spannertest.AssertShuffledSweep). The fixture keeps the three candidate sets
// disjoint (fresh atomic rows, aged best-effort rows, tripped rows with a live
// member), so a predicate that started admitting the salt shows up as one
// listing stealing another's rows instead of as a wash.
//
// The unsettled scan takes no limit, so its half of the contract is the set and
// the order alone.
func TestBatchRepository_SweepListingsShuffle(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := batchfeature.NewRepository(db.Client)
	ctx := t.Context()

	hostID := "host-batch-shuffle"
	if err := db.SeedHost(ctx, hostID, "shuffle-host", "eu-west4-a"); err != nil {
		t.Fatalf("seeding host: %v", err)
	}

	old := time.Now().UTC().Add(-2 * time.Hour)
	const candidates = 12
	wantUnsettled := make([]string, 0, candidates)
	wantEmpty := make([]string, 0, candidates)
	wantTripped := make([]string, 0, candidates)
	for i := range candidates {
		// Atomic and fresh: owed a verdict, and inside the GC grace window.
		unsettled := testBatch(fmt.Sprintf("batch-shuffle-open-%02d", i))
		// Best-effort, so no verdict is owed, and old enough to collect.
		empty := testBatch(fmt.Sprintf("batch-shuffle-empty-%02d", i))
		empty.FillPolicy = batch.FillBestEffort
		empty.CreatedAt = old
		// Tripped with a member still outside teardown: the resume arm's.
		tripped := testBatch(fmt.Sprintf("batch-shuffle-tripped-%02d", i))
		for _, b := range []*batch.Batch{unsettled, empty, tripped} {
			if err := repo.Create(ctx, b); err != nil {
				t.Fatalf("Create %s: %v", b.ID, err)
			}
		}
		if won, err := repo.MarkFailed(ctx, tripped.ID, time.Now().UTC(), "swept"); err != nil || !won {
			t.Fatalf("MarkFailed %s: won=%v err=%v", tripped.ID, won, err)
		}
		seedMember(t, db, hostID, fmt.Sprintf("dev-shuffle-%02d", i), tripped.ID, "running")

		wantUnsettled = append(wantUnsettled, unsettled.ID)
		wantEmpty = append(wantEmpty, empty.ID)
		wantTripped = append(wantTripped, tripped.ID)
	}

	cutoff := time.Now().UTC().Add(-time.Hour)
	spannertest.AssertShuffledSweep(t, "ListEmptyBefore", wantEmpty, func(limit int) ([]string, error) {
		return repo.ListEmptyBefore(ctx, cutoff, limit)
	})
	spannertest.AssertShuffledSweep(t, "ListFailedWithLiveMembers", wantTripped, func(limit int) ([]string, error) {
		return repo.ListFailedWithLiveMembers(ctx, limit)
	})

	orders := make([][]string, 0, 5)
	for range cap(orders) {
		got, err := repo.ListUnsettledAtomic(ctx)
		if err != nil {
			t.Fatalf("ListUnsettledAtomic: %v", err)
		}
		ids := make([]string, 0, len(got))
		for _, b := range got {
			ids = append(ids, b.ID)
		}
		sorted := slices.Clone(ids)
		slices.Sort(sorted)
		if !slices.Equal(sorted, wantUnsettled) {
			t.Fatalf("ListUnsettledAtomic returned %v, want exactly the unsettled set %v", sorted, wantUnsettled)
		}
		orders = append(orders, ids)
	}
	for _, o := range orders[1:] {
		if !slices.Equal(o, orders[0]) {
			return
		}
	}
	t.Errorf("ListUnsettledAtomic returned one order %v across %d calls; every instance would judge in the same sequence",
		orders[0], len(orders))
}
