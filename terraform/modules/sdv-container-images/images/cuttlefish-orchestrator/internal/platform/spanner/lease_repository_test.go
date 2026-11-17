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

package spanner_test

import (
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

func TestLeaseAcquireContendRenew(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	repo := cspanner.NewLeaseRepository(db.Client)
	ctx := t.Context()
	const lease = "hostreconciler"

	// A acquires the free lease.
	ok, err := repo.TryAcquire(ctx, lease, "A", time.Hour)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "A", leaseHolder(t, db, lease))

	// B cannot take a live lease held by A.
	ok, err = repo.TryAcquire(ctx, lease, "B", time.Hour)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, "A", leaseHolder(t, db, lease))

	// A renews its own lease; acquired_at survives the renewal (only a
	// takeover or re-acquisition restarts it).
	first := leaseAcquiredAt(t, db, lease)
	ok, err = repo.TryAcquire(ctx, lease, "A", time.Hour)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.True(t, leaseAcquiredAt(t, db, lease).Equal(first),
		"renewal must preserve acquired_at")
}

func TestLeaseExpiryHandoff(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	repo := cspanner.NewLeaseRepository(db.Client)
	ctx := t.Context()
	const lease = "reaper"

	ok, err := repo.TryAcquire(ctx, lease, "A", time.Hour)
	require.NoError(t, err)
	require.True(t, ok)

	// Simulate A crashing by expiring its lease in the past.
	_, err = db.Client.Apply(ctx, []*spanner.Mutation{
		spanner.UpdateMap("leader_leases", map[string]any{
			"lease_name":   lease,
			"lease_expiry": time.Now().Add(-time.Minute),
		}),
	})
	require.NoError(t, err)

	// B takes over the expired lease.
	ok, err = repo.TryAcquire(ctx, lease, "B", time.Hour)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "B", leaseHolder(t, db, lease))
}

func TestLeaseRelease(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	repo := cspanner.NewLeaseRepository(db.Client)
	ctx := t.Context()
	const lease = "sweeper"

	ok, err := repo.TryAcquire(ctx, lease, "A", time.Hour)
	require.NoError(t, err)
	require.True(t, ok)

	// A non-owner release is a no-op.
	require.NoError(t, repo.Release(ctx, lease, "B"))
	ok, err = repo.TryAcquire(ctx, lease, "B", time.Hour)
	require.NoError(t, err)
	assert.False(t, ok, "B still cannot take A's live lease")

	// The owner releases, letting B acquire immediately.
	require.NoError(t, repo.Release(ctx, lease, "A"))
	holder, valid := leaseHolderRaw(t, db, lease)
	assert.False(t, valid && holder != "", "holder cleared after release")

	ok, err = repo.TryAcquire(ctx, lease, "B", time.Hour)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "B", leaseHolder(t, db, lease))
}

func leaseHolder(t *testing.T, db *spannertest.TestDB, name string) string {
	t.Helper()
	h, _ := leaseHolderRaw(t, db, name)
	return h
}

func leaseHolderRaw(t *testing.T, db *spannertest.TestDB, name string) (string, bool) {
	t.Helper()
	row, err := db.Client.Single().ReadRow(t.Context(), "leader_leases",
		spanner.Key{name}, []string{"holder"})
	require.NoError(t, err)
	var h spanner.NullString
	require.NoError(t, row.Columns(&h))
	return h.StringVal, h.Valid
}

func leaseAcquiredAt(t *testing.T, db *spannertest.TestDB, name string) time.Time {
	t.Helper()
	row, err := db.Client.Single().ReadRow(t.Context(), "leader_leases",
		spanner.Key{name}, []string{"acquired_at"})
	require.NoError(t, err)
	var ts spanner.NullTime
	require.NoError(t, row.Columns(&ts))
	require.True(t, ts.Valid, "acquired_at must be set on a held lease")
	return ts.Time
}

func TestLeaseListLeases(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	repo := cspanner.NewLeaseRepository(db.Client)
	ctx := t.Context()

	ok, err := repo.TryAcquire(ctx, "hostreconciler", "inst-a", time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = repo.TryAcquire(ctx, "reaper", "inst-b", time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, repo.Release(ctx, "reaper", "inst-b"))

	leases, err := repo.ListLeases(ctx)
	require.NoError(t, err)
	require.Len(t, leases, 2)
	assert.Equal(t, "hostreconciler", leases[0].Name, "ordered by name")
	assert.Equal(t, "inst-a", leases[0].Holder)
	assert.False(t, leases[0].AcquiredAt.IsZero())
	assert.False(t, leases[0].Expiry.IsZero())
	assert.False(t, leases[0].RenewedAt.IsZero())
	assert.Equal(t, "reaper", leases[1].Name)
	assert.Empty(t, leases[1].Holder, "released lease is unheld")
	assert.True(t, leases[1].Expiry.IsZero(), "released lease has no expiry")
	assert.False(t, leases[1].RenewedAt.IsZero(), "the release itself stamps updated_at")
}
