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

package quotafeature_test

import (
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	quotafeature "cloud-cuttlefish-orchestrator/internal/features/quota"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

func TestRepository_GetNotFound(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := quotafeature.NewRepository(db.Client)

	_, err := repo.Get(t.Context(), quota.ScopePlatform, quota.GlobalScopeID)
	var notFound *quota.NotFoundError
	require.ErrorAs(t, err, &notFound)
}

func TestRepository_SetCreatesQuota(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := quotafeature.NewRepository(db.Client)
	ctx := t.Context()

	require.NoError(t, repo.Set(ctx, &quota.Quota{
		Scope:    quota.ScopePlatform,
		ScopeID:  quota.GlobalScopeID,
		MaxHosts: 10,
	}))

	got, err := repo.Get(ctx, quota.ScopePlatform, quota.GlobalScopeID)
	require.NoError(t, err)
	assert.EqualValues(t, 10, got.MaxHosts)
	assert.Zero(t, got.UsedHosts)
	assert.False(t, got.CreatedAt.IsZero())
}

// A committed counter write must survive a limit change: Set on an existing
// row may only write max_hosts, never the used_hosts counter or created_at,
// even when handed a quota value carrying a stale UsedHosts of 0. (In
// production the counter is written by the host feature's atomic commits; a
// direct used_hosts write stands in for one here.)
func TestRepository_SetPreservesUsedHostsAndCreatedAt(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := quotafeature.NewRepository(db.Client)
	ctx := t.Context()

	require.NoError(t, repo.Set(ctx, &quota.Quota{
		Scope:    quota.ScopePlatform,
		ScopeID:  quota.GlobalScopeID,
		MaxHosts: 10,
	}))
	created, err := repo.Get(ctx, quota.ScopePlatform, quota.GlobalScopeID)
	require.NoError(t, err)

	_, err = db.Client.Apply(ctx, []*spanner.Mutation{spanner.Update("quotas",
		[]string{"scope", "scope_id", "used_hosts", "updated_at"},
		[]any{string(quota.ScopePlatform), quota.GlobalScopeID, int64(3), time.Now()})})
	require.NoError(t, err)

	require.NoError(t, repo.Set(ctx, &quota.Quota{
		Scope:    quota.ScopePlatform,
		ScopeID:  quota.GlobalScopeID,
		MaxHosts: 50,
	}))

	got, err := repo.Get(ctx, quota.ScopePlatform, quota.GlobalScopeID)
	require.NoError(t, err)
	assert.EqualValues(t, 50, got.MaxHosts)
	assert.EqualValues(t, 3, got.UsedHosts, "used_hosts written between Get and Set must not be clobbered")
	assert.True(t, got.CreatedAt.Equal(created.CreatedAt), "created_at must be preserved on update")
}
