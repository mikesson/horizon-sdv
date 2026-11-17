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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

func TestSystemConfigRepository_GetNotFound(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewSystemConfigRepository(db.Client)

	_, err := repo.Get(t.Context(), "missing")
	require.ErrorIs(t, err, cspanner.ErrKeyNotFound)
}

func TestSystemConfigRepository_SetCreatesThenUpdates(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewSystemConfigRepository(db.Client)
	ctx := t.Context()

	require.NoError(t, repo.Set(ctx, "test.example_key", "proj-a", "first"))
	created, err := repo.Get(ctx, "test.example_key")
	require.NoError(t, err)
	assert.Equal(t, "proj-a", created.Value)
	assert.Equal(t, "first", created.Description)
	assert.False(t, created.CreatedAt.IsZero())

	require.NoError(t, repo.Set(ctx, "test.example_key", "proj-b", "second"))
	got, err := repo.Get(ctx, "test.example_key")
	require.NoError(t, err)
	assert.Equal(t, "proj-b", got.Value)
	assert.Equal(t, "second", got.Description)
	assert.True(t, got.CreatedAt.Equal(created.CreatedAt), "created_at must be preserved on update")
	assert.True(t, got.UpdatedAt.After(created.UpdatedAt), "updated_at must advance on update")
}

// The claim SetMany exists for. The settings groups above it are read back as
// one fact, and the autoscaler's pair is the loudest: `mode: on` stored while
// `standby: 0` is not gives the keeper a target of zero beside standing
// inventory, which it starts handing back within one cycle.
//
// The failure is driven through the SECOND entry on purpose. Under the
// sequential writes this replaces, the first key would already be committed and
// visible here, so this is the assertion that goes red against the old shape,
// which "write two good keys and read them back" never would.
//
// The injection is a key past the column's STRING(100): if the require.Error
// below ever fails, the write was accepted rather than the transaction being
// non-atomic, and the injection needs replacing rather than the code.
func TestSystemConfigRepository_SetManyAppliesWhollyOrNotAtAll(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewSystemConfigRepository(db.Client)
	ctx := t.Context()

	err := repo.SetMany(ctx, []cspanner.ConfigEntry{
		{Key: "fleet.autoscale_mode", Value: "on", Description: "the mode"},
		{Key: strings.Repeat("k", 101), Value: "0", Description: "past the column width"},
	})
	require.Error(t, err, "an unwritable entry must fail the group")

	_, err = repo.Get(ctx, "fleet.autoscale_mode")
	require.ErrorIs(t, err, cspanner.ErrKeyNotFound,
		"the first entry committed while its partner did not: the group is not atomic")
}

// A group mixing a new key with one already stored keeps each half's own
// created_at, in one commit. That is the property Set had per key, and SetMany
// must not lose it by treating the group as a single blind upsert.
func TestSystemConfigRepository_SetManyMixesNewAndExistingKeys(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewSystemConfigRepository(db.Client)
	ctx := t.Context()

	require.NoError(t, repo.Set(ctx, "fleet.demand_horizon_sec", "3600", "first"))
	existing, err := repo.Get(ctx, "fleet.demand_horizon_sec")
	require.NoError(t, err)

	require.NoError(t, repo.SetMany(ctx, []cspanner.ConfigEntry{
		{Key: "fleet.demand_horizon_sec", Value: "1800", Description: "second"},
		{Key: "fleet.drain_grace_sec", Value: "300", Description: "new"},
	}))

	updated, err := repo.Get(ctx, "fleet.demand_horizon_sec")
	require.NoError(t, err)
	assert.Equal(t, "1800", updated.Value)
	assert.True(t, updated.CreatedAt.Equal(existing.CreatedAt),
		"created_at belongs to the row's first write, not to the group's")

	created, err := repo.Get(ctx, "fleet.drain_grace_sec")
	require.NoError(t, err)
	assert.Equal(t, "300", created.Value)
	assert.False(t, created.CreatedAt.IsZero(), "a key first written in a group still gets its created_at")
}

// An empty group is the no-op it looks like, which is what lets a sweep-policy
// patch naming no switch stay a write of nothing rather than a write of all
// four.
func TestSystemConfigRepository_SetManyWritesNothingForAnEmptyGroup(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewSystemConfigRepository(db.Client)

	require.NoError(t, repo.SetMany(t.Context(), nil))
}

func TestSystemConfigRepository_Delete(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewSystemConfigRepository(db.Client)
	ctx := t.Context()

	require.NoError(t, repo.Set(ctx, "k", "v", ""))
	require.NoError(t, repo.Delete(ctx, "k"))

	_, err := repo.Get(ctx, "k")
	require.ErrorIs(t, err, cspanner.ErrKeyNotFound)
}

// SetManyIfMatch writes only while the caller's tag still matches the stored
// group, and the tag it returns is the one a fresh read reports.
func TestSystemConfigRepository_SetManyIfMatch(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewSystemConfigRepository(db.Client)
	ctx := t.Context()
	keys := []string{"etag.a", "etag.b"}

	unset, err := repo.Fingerprint(ctx, keys)
	require.NoError(t, err)

	// An empty stored value differs from an absent key.
	require.NoError(t, repo.Set(ctx, "etag.a", "", "a"))
	empty, err := repo.Fingerprint(ctx, keys)
	require.NoError(t, err)
	assert.NotEqual(t, unset, empty)

	written, err := repo.SetManyIfMatch(ctx, keys, empty, []cspanner.ConfigEntry{
		{Key: "etag.a", Value: "1", Description: "a"},
		{Key: "etag.b", Value: "2", Description: "b"},
	})
	require.NoError(t, err)
	reread, err := repo.Fingerprint(ctx, keys)
	require.NoError(t, err)
	assert.Equal(t, reread, written, "the returned tag must be what a fresh read reports")
	assert.NotEqual(t, empty, written)

	// A writer holding the tag from before that write loses, and stores nothing.
	_, err = repo.SetManyIfMatch(ctx, keys, empty, []cspanner.ConfigEntry{
		{Key: "etag.a", Value: "stale", Description: "a"},
	})
	require.ErrorIs(t, err, hostconfig.ErrSettingChanged)
	got, err := repo.Get(ctx, "etag.a")
	require.NoError(t, err)
	assert.Equal(t, "1", got.Value)

	// No If-Match writes unconditionally.
	_, err = repo.SetManyIfMatch(ctx, keys, "", []cspanner.ConfigEntry{
		{Key: "etag.b", Value: "3", Description: "b"},
	})
	require.NoError(t, err)
	got, err = repo.Get(ctx, "etag.b")
	require.NoError(t, err)
	assert.Equal(t, "3", got.Value)
}
