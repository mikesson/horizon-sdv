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

package gcecatalogfeature_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gcecatalogfeature "cloud-cuttlefish-orchestrator/internal/features/gcecatalog"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

func newZonePolicy(t *testing.T) (*gcecatalogfeature.ZonePolicy, *cspanner.SystemConfigRepository) {
	t.Helper()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewSystemConfigRepository(db.Client)
	return gcecatalogfeature.NewZonePolicy(repo, slog.New(slog.DiscardHandler)), repo
}

// The enabled set round-trips through one system_config key, and an absent key
// is where every deployment starts: no zone enabled, no placement.
func TestZonePolicyRoundTrip(t *testing.T) {
	t.Parallel()
	policy, _ := newZonePolicy(t)
	ctx := t.Context()

	zones, err := policy.ListEnabledZones(ctx)
	require.NoError(t, err)
	assert.Empty(t, zones, "an unseeded key is no enabled zone")

	require.NoError(t, policy.SetEnabledZones(ctx, []string{"us-central1-a", "europe-west4-a"}))
	zones, err = policy.ListEnabledZones(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"europe-west4-a", "us-central1-a"}, zones, "the set comes back sorted")

	// Replaces whole, and an empty set is a legal statement.
	require.NoError(t, policy.SetEnabledZones(ctx, nil))
	zones, err = policy.ListEnabledZones(ctx)
	require.NoError(t, err)
	assert.Empty(t, zones)
}

// The whole-set fail-safe, end to end: a corrupted value reads as no enabled
// zones, which stops provisioning visibly rather than widening the placement
// space past the subnets that exist.
func TestZonePolicyUnreadableValueEnablesNothing(t *testing.T) {
	t.Parallel()
	policy, repo := newZonePolicy(t)
	ctx := t.Context()

	require.NoError(t, repo.Set(ctx, models.ConfigKeyZonesEnabled, "europe-west4-a", ""))
	zones, err := policy.ListEnabledZones(ctx)
	require.NoError(t, err, "an unreadable value is a policy answer, not an infrastructure failure")
	assert.Empty(t, zones)
}
