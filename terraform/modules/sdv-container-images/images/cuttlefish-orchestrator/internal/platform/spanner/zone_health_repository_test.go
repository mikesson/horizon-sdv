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
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

func TestZoneHealthRecordFailureEscalatesCooldown(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewZoneHealthRepository(db.Client)
	ctx := t.Context()

	first, err := repo.RecordFailure(ctx, "europe-west4-a", "n1",
		gcecatalog.ZoneMetricCapacity, "stockout")
	require.NoError(t, err)
	assert.Equal(t, int64(1), first.ConsecutiveFailures)
	require.NotNil(t, first.CooldownUntil)
	firstCooldown := time.Until(*first.CooldownUntil)
	assert.Greater(t, firstCooldown, 4*time.Minute)
	assert.Less(t, firstCooldown, 6*time.Minute)

	second, err := repo.RecordFailure(ctx, "europe-west4-a", "n1",
		gcecatalog.ZoneMetricCapacity, "stockout again")
	require.NoError(t, err)
	assert.Equal(t, int64(2), second.ConsecutiveFailures)
	secondCooldown := time.Until(*second.CooldownUntil)
	assert.Greater(t, secondCooldown, 9*time.Minute, "second failure must double the cooldown")

	// The cooldown caps at an hour regardless of failure count.
	for range 8 {
		_, err = repo.RecordFailure(ctx, "europe-west4-a", "n1",
			gcecatalog.ZoneMetricCapacity, "still out")
		require.NoError(t, err)
	}
	capped, err := repo.RecordFailure(ctx, "europe-west4-a", "n1",
		gcecatalog.ZoneMetricCapacity, "still out")
	require.NoError(t, err)
	assert.LessOrEqual(t, time.Until(*capped.CooldownUntil), time.Hour)
}

func TestZoneHealthConcurrentRecordFailuresAllCount(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewZoneHealthRepository(db.Client)
	ctx := t.Context()

	// Concurrent failure records must serialize through the read-modify-write
	// transaction: none may be lost to a stale read.
	const writers = 5
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Go(func() {
			_, errs[i] = repo.RecordFailure(ctx, "us-central1-a", "n1",
				gcecatalog.ZoneMetricCapacity, "stockout")
		})
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}

	cells, err := repo.ListCells(ctx)
	require.NoError(t, err)
	require.Len(t, cells, 1)
	assert.Equal(t, int64(writers), cells[0].ConsecutiveFailures)
}

func TestZoneHealthClearCellAndActiveListing(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewZoneHealthRepository(db.Client)
	ctx := t.Context()

	_, err := repo.RecordFailure(ctx, "europe-west4-a", "n1",
		gcecatalog.ZoneMetricQuotaCPU, "cpu quota")
	require.NoError(t, err)
	_, err = repo.RecordFailure(ctx, "europe-west4-b", "g2",
		gcecatalog.ZoneMetricCapacity, "stockout")
	require.NoError(t, err)

	cells, err := repo.ListCells(ctx)
	require.NoError(t, err)
	assert.Len(t, cells, 2)

	// A successful provision clears exactly its cell; clearing a missing cell
	// is a no-op.
	require.NoError(t, repo.ClearCell(ctx, "europe-west4-a", "n1"))
	require.NoError(t, repo.ClearCell(ctx, "europe-west4-a", "n1"))

	cells, err = repo.ListCells(ctx)
	require.NoError(t, err)
	require.Len(t, cells, 1)
	assert.Equal(t, "europe-west4-b", cells[0].Zone)
	assert.Equal(t, gcecatalog.ZoneMetricCapacity, cells[0].BindingMetric)

	// A cell whose cooldown has lapsed is still listed (ranking uses it for
	// score decay) but no longer reads as cooling.
	_, err = db.Client.Apply(ctx, []*spanner.Mutation{
		spanner.UpdateMap("zone_health", map[string]any{
			"zone":           "europe-west4-b",
			"machine_family": "g2",
			"cooldown_until": time.Now().Add(-time.Minute),
			"updated_at":     spanner.CommitTimestamp,
		}),
	})
	require.NoError(t, err)
	cells, err = repo.ListCells(ctx)
	require.NoError(t, err)
	require.Len(t, cells, 1)
	assert.False(t, cells[0].InCooldown(time.Now()))
}
