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
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// colZone is shared with the catalog repositories' zone-keyed tables.
const colZone = "zone"

var zoneHealthColumns = []string{
	colZone, "machine_family", "binding_metric", "consecutive_failures",
	"last_failure_at", "last_reason", "cooldown_until", ColUpdatedAt,
}

// ZoneHealthRepository persists placement's record of capacity failures
// (zone_health cells) in Cloud Spanner.
type ZoneHealthRepository struct {
	client *Client
}

// NewZoneHealthRepository creates a zone-health repository.
func NewZoneHealthRepository(client *Client) *ZoneHealthRepository {
	return &ZoneHealthRepository{client: client}
}

// RecordFailure counts one classified capacity failure against the
// (zone, machineFamily) cell inside a read-modify-write transaction, replacing
// its binding metric and reason and extending the cooldown exponentially.
func (r *ZoneHealthRepository) RecordFailure(ctx context.Context, zone, machineFamily, bindingMetric, reason string) (*gcecatalog.ZoneHealth, error) {
	var result *gcecatalog.ZoneHealth
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		result = nil // Reset: the transaction body may run more than once.
		var prevFailures int64
		row, err := txn.ReadRow(ctx, "zone_health", spanner.Key{zone, machineFamily},
			[]string{"consecutive_failures"})
		switch {
		case spanner.ErrCode(err) == codes.NotFound:
			// First failure in this cell.
		case err != nil:
			return fmt.Errorf("reading zone_health cell: %w", err)
		default:
			if err := row.Columns(&prevFailures); err != nil {
				return fmt.Errorf("scanning zone_health cell: %w", err)
			}
		}

		now := time.Now()
		// The escalation schedule is domain policy (gcecatalog owns it); this
		// repository only persists the computed value.
		cooldownUntil := gcecatalog.NextCooldownUntil(prevFailures, now)
		result = &gcecatalog.ZoneHealth{
			Zone:                zone,
			MachineFamily:       machineFamily,
			BindingMetric:       bindingMetric,
			ConsecutiveFailures: prevFailures + 1,
			LastFailureAt:       &now,
			LastReason:          reason,
			CooldownUntil:       &cooldownUntil,
		}
		return txn.BufferWrite([]*spanner.Mutation{
			spanner.InsertOrUpdate("zone_health", zoneHealthColumns, []any{
				zone, machineFamily, bindingMetric, prevFailures + 1,
				now, TruncateRunes(reason, 200), cooldownUntil, spanner.CommitTimestamp,
			}),
		})
	})
	if err != nil {
		return nil, fmt.Errorf("recording zone_health failure for %s/%s: %w", zone, machineFamily, err)
	}
	return result, nil
}

// ClearCell removes the (zone, machineFamily) cell; a missing cell is a no-op
// (mutation deletes are idempotent).
func (r *ZoneHealthRepository) ClearCell(ctx context.Context, zone, machineFamily string) error {
	_, err := r.client.Apply(ctx, []*spanner.Mutation{
		spanner.Delete("zone_health", spanner.Key{zone, machineFamily}),
	})
	if err != nil {
		return fmt.Errorf("clearing zone_health cell %s/%s: %w", zone, machineFamily, err)
	}
	return nil
}

// ListCells returns every zone_health cell (the table only ever holds
// recently-failing cells, since quiet ones age out under its deletion policy).
// A bounded-staleness read: a cooldown observed a few seconds late still cools,
// and ranking must never contend with the failure path's writes.
func (r *ZoneHealthRepository) ListCells(ctx context.Context) ([]*gcecatalog.ZoneHealth, error) {
	stmt := spanner.Statement{
		SQL: "SELECT " + strings.Join(zoneHealthColumns, ", ") + " FROM zone_health",
	}
	return QueryAll(r.client.StaleSingle().Query(ctx, stmt), "zone_health cells", (*models.ZoneHealthRow).ToDomain)
}

// ListCapacityCells returns the stockout cells recorded for these machine
// families. One statement whatever the family count: the families ride a bound
// array parameter through IN UNNEST rather than a predicate built per family,
// so a shape ladder asking about eight rungs costs the same read as one asking
// about one.
func (r *ZoneHealthRepository) ListCapacityCells(ctx context.Context, machineFamilies []string) ([]*gcecatalog.ZoneHealth, error) {
	if len(machineFamilies) == 0 {
		return nil, nil
	}
	stmt := spanner.Statement{
		SQL: "SELECT " + strings.Join(zoneHealthColumns, ", ") + " FROM zone_health" +
			" WHERE machine_family IN UNNEST(@families) AND binding_metric = @metric",
		Params: map[string]any{
			"families": machineFamilies,
			"metric":   gcecatalog.ZoneMetricCapacity,
		},
	}
	return QueryAll(r.client.StaleSingle().Query(ctx, stmt), "zone_health capacity cells",
		(*models.ZoneHealthRow).ToDomain)
}

// Ensure ZoneHealthRepository implements the domain store.
var _ gcecatalog.ZoneHealthStore = (*ZoneHealthRepository)(nil)
