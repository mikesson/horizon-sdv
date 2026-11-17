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

package hostfeature_test

import (
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// seedSnapshotRow plants a snapshot row directly; the parent host row must
// already exist.
func seedSnapshotRow(t *testing.T, db *spannertest.TestDB, hostID, snapshotID, deviceID string, state device.SnapshotState) {
	t.Helper()
	now := time.Now()
	if _, err := db.Client.Apply(t.Context(), []*spanner.Mutation{
		spanner.Insert("snapshots",
			[]string{
				"host_id", "snapshot_id", "device_id", "cvd_group", "cvd_name",
				"host_incarnation", "state", "created_at", "updated_at",
			},
			[]any{hostID, snapshotID, deviceID, "cvdg", "1", int64(1), string(state), now, now}),
	}); err != nil {
		t.Fatalf("seeding snapshot %s: %v", snapshotID, err)
	}
}

func snapshotStateAndLostAt(t *testing.T, db *spannertest.TestDB, hostID, snapshotID string) (string, spanner.NullTime) {
	t.Helper()
	row, err := db.Client.Single().ReadRow(t.Context(), "snapshots",
		spanner.Key{hostID, snapshotID}, []string{"state", "lost_at"})
	if err != nil {
		t.Fatalf("read snapshot %s: %v", snapshotID, err)
	}
	var state string
	var lostAt spanner.NullTime
	if err := row.Columns(&state, &lostAt); err != nil {
		t.Fatalf("scan snapshot: %v", err)
	}
	return state, lostAt
}

// A VM deleted outside CCO takes the disk and every snapshot on it: the mark
// must flip the rows in the same commit, and stamp lost_at so the schema's
// deletion policy eventually clears the record.
func TestMarkHostLostMarksSnapshotsLost(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)

	if err := db.SeedQuota(ctx, 10, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.SeedHost(ctx, "snap-lost-1", "snap-lost-host", "us-central1-a"); err != nil {
		t.Fatal(err)
	}
	seedSnapshotRow(t, db, "snap-lost-1", "snap-a", "dev-1", device.SnapshotStateReady)
	seedSnapshotRow(t, db, "snap-lost-1", "snap-b", "dev-1", device.SnapshotStateCreating)

	if err := repo.MarkHostLost(ctx, "snap-lost-1", "host VM deleted outside CCO"); err != nil {
		t.Fatalf("MarkHostLost: %v", err)
	}
	for _, id := range []string{"snap-a", "snap-b"} {
		state, lostAt := snapshotStateAndLostAt(t, db, "snap-lost-1", id)
		if state != string(device.SnapshotStateLost) {
			t.Errorf("snapshot %s state = %q, want lost", id, state)
		}
		if !lostAt.Valid {
			t.Errorf("snapshot %s has no lost_at; the deletion policy never fires", id)
		}
	}
}

// artifactCacheKeys reads the ledger keys currently on a host.
func artifactCacheKeys(t *testing.T, db *spannertest.TestDB, hostID string) []string {
	t.Helper()
	iter := db.Client.Single().Query(t.Context(), spanner.Statement{
		SQL:    `SELECT fetch_key FROM host_artifact_cache WHERE host_id = @host`,
		Params: map[string]any{"host": hostID},
	})
	var keys []string
	if err := iter.Do(func(row *spanner.Row) error {
		var k string
		if err := row.Column(0, &k); err != nil {
			return err
		}
		keys = append(keys, k)
		return nil
	}); err != nil {
		t.Fatalf("listing artifact cache of %s: %v", hostID, err)
	}
	return keys
}

// The artifact-cache ledger describes bytes on the host's disk, so the
// lost-VM mark must delete its rows in the same commit that tombstones the
// host. A surviving row would advertise warmth nothing can collect on.
func TestMarkHostLostClearsArtifactCache(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)

	if err := db.SeedQuota(ctx, 10, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.SeedHost(ctx, "cache-lost-1", "cache-lost-host", "us-central1-a"); err != nil {
		t.Fatal(err)
	}
	ledger := spannerplatform.NewArtifactCacheRepository(db.Client)
	if err := ledger.Record(ctx, "cache-lost-1", "@ab/12345/target-userdebug", "b-1", 0); err != nil {
		t.Fatalf("seeding ledger row: %v", err)
	}

	if err := repo.MarkHostLost(ctx, "cache-lost-1", "host VM deleted outside CCO"); err != nil {
		t.Fatalf("MarkHostLost: %v", err)
	}
	if keys := artifactCacheKeys(t, db, "cache-lost-1"); len(keys) != 0 {
		t.Errorf("ledger still holds %v after the lost mark", keys)
	}
}

// ArtifactCacheLostMutation is the building block the re-provision path folds
// into its incarnation-bump commit: a prefix delete that clears exactly the
// bumped host's rows.
func TestArtifactCacheLostMutationClearsOnlyThatHost(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()

	for _, id := range []string{"cache-bump-1", "cache-bump-2"} {
		if err := db.SeedHost(ctx, id, id+"-host", "us-central1-a"); err != nil {
			t.Fatal(err)
		}
		if err := spannerplatform.NewArtifactCacheRepository(db.Client).Record(ctx, id, "@ab/12345/target-userdebug", "b-1", 0); err != nil {
			t.Fatalf("seeding ledger row on %s: %v", id, err)
		}
	}

	if _, err := db.Client.Apply(ctx, []*spanner.Mutation{
		spannerplatform.ArtifactCacheLostMutation("cache-bump-1"),
	}); err != nil {
		t.Fatalf("applying ledger delete: %v", err)
	}
	if keys := artifactCacheKeys(t, db, "cache-bump-1"); len(keys) != 0 {
		t.Errorf("bumped host still holds %v", keys)
	}
	if keys := artifactCacheKeys(t, db, "cache-bump-2"); len(keys) != 1 {
		t.Errorf("unrelated host lost its ledger: %v", keys)
	}
}

// A ledger write against a deleted host must fail (interleave parent gone)
// rather than resurrect a row hierarchy, which pins that rows cannot outlive
// their host.
func TestArtifactCacheRecordRequiresParentHost(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	err := spannerplatform.NewArtifactCacheRepository(db.Client).Record(t.Context(), "no-such-host", "@ab/1/t", "b", 0)
	if err == nil {
		t.Fatal("recording onto a missing host succeeded; interleave should forbid it")
	}
	if spanner.ErrCode(err) != codes.NotFound {
		t.Fatalf("want NotFound for missing parent, got: %v", err)
	}
}

// SnapshotsLostMutations is the building block the re-provision path folds
// into its incarnation-bump commit: it must cover creating and ready rows and
// leave already-lost ones untouched (their original lost_at is the record of
// when the bytes died).
func TestSnapshotsLostMutationsMarksLiveRowsOnly(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)

	if err := db.SeedHost(ctx, "snap-retry-1", "snap-retry-host", "us-central1-a"); err != nil {
		t.Fatal(err)
	}
	seedSnapshotRow(t, db, "snap-retry-1", "snap-live", "dev-1", device.SnapshotStateReady)
	seedSnapshotRow(t, db, "snap-retry-1", "snap-gone", "dev-1", device.SnapshotStateLost)
	priorState, priorLostAt := snapshotStateAndLostAt(t, db, "snap-retry-1", "snap-gone")
	if priorState != string(device.SnapshotStateLost) || priorLostAt.Valid {
		// Seeded without lost_at on purpose: an update would make it Valid.
		t.Fatalf("seed precondition broken: state=%s lost_at=%v", priorState, priorLostAt)
	}

	muts, err := repo.SnapshotsLostMutations(ctx, "snap-retry-1")
	if err != nil {
		t.Fatalf("SnapshotsLostMutations: %v", err)
	}
	if len(muts) != 1 {
		t.Fatalf("got %d mutations, want 1 (only the live row)", len(muts))
	}
	if _, err := db.Client.Apply(ctx, muts); err != nil {
		t.Fatalf("applying lost marks: %v", err)
	}
	state, lostAt := snapshotStateAndLostAt(t, db, "snap-retry-1", "snap-live")
	if state != string(device.SnapshotStateLost) || !lostAt.Valid {
		t.Errorf("live row after mark: state=%q lost_at=%v, want lost with lost_at set", state, lostAt)
	}
	if _, lostAt := snapshotStateAndLostAt(t, db, "snap-retry-1", "snap-gone"); lostAt.Valid {
		t.Error("already-lost row was rewritten")
	}
}
