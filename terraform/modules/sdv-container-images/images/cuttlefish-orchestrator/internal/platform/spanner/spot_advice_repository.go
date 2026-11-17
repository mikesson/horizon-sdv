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

package spanner

import (
	"context"
	"fmt"
	"strings"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// colMachineType is shared with the catalog repositories' type-keyed tables.
const colMachineType = "machine_type"

var spotAdviceColumns = []string{
	colZone, colMachineType, "obtainability", "uptime_tier_s", "synced_at",
}

// SpotAdviceRepository persists synced Spot capacity advice cells in Cloud
// Spanner.
type SpotAdviceRepository struct {
	client *Client
}

// NewSpotAdviceRepository creates a spot-advice repository.
func NewSpotAdviceRepository(client *Client) *SpotAdviceRepository {
	return &SpotAdviceRepository{client: client}
}

// UpsertCells replaces the given cells in one commit, stamping their sync
// time. Cells not in the batch are left to age out under the table's 1-day
// deletion policy, because a shrunken probe set must not block the sync.
func (r *SpotAdviceRepository) UpsertCells(ctx context.Context, cells []*gcecatalog.SpotAdvice) error {
	if len(cells) == 0 {
		return nil
	}
	muts := make([]*spanner.Mutation, 0, len(cells))
	for _, c := range cells {
		muts = append(muts, spanner.InsertOrUpdate("spot_advice", spotAdviceColumns, []any{
			c.Zone, c.MachineType, c.Obtainability, c.UptimeTierSeconds, spanner.CommitTimestamp,
		}))
	}
	if _, err := r.client.Apply(ctx, muts); err != nil {
		return fmt.Errorf("upserting %d spot_advice cells: %w", len(cells), err)
	}
	return nil
}

// ListCells returns every synced advice cell. A bounded-staleness read:
// advice observed a few seconds late steers identically, and ranking must
// never contend with the sync ticker's writes.
func (r *SpotAdviceRepository) ListCells(ctx context.Context) ([]*gcecatalog.SpotAdvice, error) {
	stmt := spanner.Statement{
		SQL: "SELECT " + strings.Join(spotAdviceColumns, ", ") + " FROM spot_advice",
	}
	return QueryAll(r.client.StaleSingle().Query(ctx, stmt), "spot_advice cells", (*models.SpotAdviceRow).ToDomain)
}

// Ensure SpotAdviceRepository implements the domain store.
var _ gcecatalog.SpotAdviceStore = (*SpotAdviceRepository)(nil)
