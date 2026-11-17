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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

func TestSpotAdviceUpsertAndList(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewSpotAdviceRepository(db.Client)
	ctx := t.Context()

	require.NoError(t, repo.UpsertCells(ctx, nil), "an empty batch is a no-op")

	require.NoError(t, repo.UpsertCells(ctx, []*gcecatalog.SpotAdvice{
		{Zone: "europe-west4-a", MachineType: "n1-standard-8", Obtainability: 0.9, UptimeTierSeconds: gcecatalog.SpotUptimeTierHour},
		{Zone: "europe-west4-b", MachineType: "n1-standard-8", Obtainability: 0.4, UptimeTierSeconds: gcecatalog.SpotUptimeTier10Min},
	}))

	cells, err := repo.ListCells(ctx)
	require.NoError(t, err)
	require.Len(t, cells, 2)
	byZone := map[string]*gcecatalog.SpotAdvice{}
	for _, c := range cells {
		byZone[c.Zone] = c
		assert.False(t, c.SyncedAt.IsZero(), "synced_at must carry the commit timestamp")
	}
	assert.InDelta(t, 0.9, byZone["europe-west4-a"].Obtainability, 1e-9)
	assert.Equal(t, int64(gcecatalog.SpotUptimeTierHour), byZone["europe-west4-a"].UptimeTierSeconds)

	// A re-probe replaces the cell in place (same key, fresh values).
	require.NoError(t, repo.UpsertCells(ctx, []*gcecatalog.SpotAdvice{
		{Zone: "europe-west4-a", MachineType: "n1-standard-8", Obtainability: 0.2, UptimeTierSeconds: gcecatalog.SpotUptimeTierMinute},
	}))
	cells, err = repo.ListCells(ctx)
	require.NoError(t, err)
	require.Len(t, cells, 2, "upsert replaces, never duplicates")
	for _, c := range cells {
		if c.Zone == "europe-west4-a" {
			assert.InDelta(t, 0.2, c.Obtainability, 1e-9)
			assert.Equal(t, int64(gcecatalog.SpotUptimeTierMinute), c.UptimeTierSeconds)
		}
	}
}
