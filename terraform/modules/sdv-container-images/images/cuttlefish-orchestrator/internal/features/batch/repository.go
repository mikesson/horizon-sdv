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

package batchfeature

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/batch"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// batchColumns is the explicit projection for batch queries; SELECT * breaks
// on additive migrations because ToStruct rejects unknown columns.
func batchColumns() []string {
	return []string{
		"batch_id", "name_prefix", "requested_count", "fill_policy", "placement",
		"ready_timeout_seconds", "spec_fingerprint", "created_by", "created_at",
		"ready_at", "failed_at", "failure_reason",
	}
}

var batchColumnsSQL = strings.Join(batchColumns(), ", ")

// Repository persists batch policy rows in Cloud Spanner. Reads that gate
// decisions (Get, ListUnsettledAtomic) are strong; the dashboard-shaped List
// is bounded-stale per house policy.
type Repository struct {
	client *spannerplatform.Client
}

// NewRepository creates the batch repository.
func NewRepository(client *spannerplatform.Client) *Repository {
	return &Repository{client: client}
}

// Compile-time check that Repository implements the domain registry port.
var _ batch.Repository = (*Repository)(nil)

// Create inserts the batch policy row. A keyed batch id is deterministic, so
// losing the insert to a concurrent same-key admission returns ErrBatchExists;
// the service re-reads the winner's row and resumes against it (after
// verifying the spec fingerprint, since the two requests need not agree).
func (r *Repository) Create(ctx context.Context, b *batch.Batch) error {
	mut, err := spanner.InsertStruct("batches", models.FromBatchDomain(b))
	if err != nil {
		return fmt.Errorf("building batch insert: %w", err)
	}
	if _, err := r.client.Apply(ctx, []*spanner.Mutation{mut}); err != nil {
		if spanner.ErrCode(err) == codes.AlreadyExists {
			return ErrBatchExists
		}
		return fmt.Errorf("inserting batch: %w", err)
	}
	return nil
}

// Get returns the batch policy row. Strong read: Get anchors keyed-resume
// detection and the CI flow polls it right after create, inside the
// bounded-stale window. batch.ErrNotFound when the row does not exist.
func (r *Repository) Get(ctx context.Context, id string) (*batch.Batch, error) {
	row, err := r.client.Single().ReadRow(ctx, "batches", spanner.Key{id}, batchColumns())
	if err != nil {
		if spanner.ErrCode(err) == codes.NotFound {
			return nil, batch.ErrNotFound
		}
		return nil, fmt.Errorf("reading batch: %w", err)
	}
	var bRow models.BatchRow
	if err := row.ToStruct(&bRow); err != nil {
		return nil, fmt.Errorf("scanning batch row: %w", err)
	}
	return bRow.ToDomain(), nil
}

// List returns batch rows matching the filter, newest first. The table is
// bounded (rows are swept when emptied and TTL'd at 90 days), so there is no
// pagination.
func (r *Repository) List(ctx context.Context, filter batch.ListFilter) ([]*batch.Batch, error) {
	sql := `SELECT ` + batchColumnsSQL + ` FROM batches`
	params := make(map[string]any)
	if filter.CreatedBy != nil {
		sql += ` WHERE created_by = @created_by`
		params["created_by"] = *filter.CreatedBy
	}
	sql += ` ORDER BY created_at DESC`
	stmt := spanner.Statement{SQL: sql, Params: params}
	return r.queryBatches(ctx, r.client.StaleSingle(), stmt)
}

// The three sweep listings below are salted-shuffled instead of ordered by the
// column they filter on: the settle loop runs on every instance, and one order
// would march every instance through the batches in lockstep. See
// spannerplatform.SweepSalt for what the lost oldest-first service costs. For
// the unsettled scan, which has no LIMIT, it costs nothing at all, since every
// candidate comes back every cycle either way.
// List above is the dashboard's listing, not a sweep's, and keeps newest-first.

// ListUnsettledAtomic returns the atomic batches the sweep still owes a
// verdict: neither fill-complete (ready_at) nor tripped (failed_at). It takes a
// strong read, because a stale view could miss a just-admitted batch or
// re-judge a settled one.
func (r *Repository) ListUnsettledAtomic(ctx context.Context) ([]*batch.Batch, error) {
	stmt := spanner.Statement{
		SQL: `SELECT ` + batchColumnsSQL + ` FROM batches
		      WHERE fill_policy = @fill AND ready_at IS NULL AND failed_at IS NULL
		      ORDER BY FARM_FINGERPRINT(CONCAT(batch_id, @salt))`,
		Params: map[string]any{
			"fill":                         string(batch.FillAtomic),
			spannerplatform.SweepSaltParam: spannerplatform.SweepSalt(),
		},
	}
	return r.queryBatches(ctx, r.client.Single(), stmt)
}

// ListEmptyBefore returns ids of non-failed batches created before cutoff
// with no member device rows left (deleted members do not count). The sweep
// collects them so an emptied batch 404s instead of lingering to the row
// TTL. Failed batches are kept for post-mortems. The cutoff doubles as the
// keyed-resume grace window: a crashed admission's row must survive long
// enough for the retry to find it.
func (r *Repository) ListEmptyBefore(ctx context.Context, cutoff time.Time, limit int) ([]string, error) {
	stmt := spanner.Statement{
		SQL: `SELECT b.batch_id FROM batches b
		      WHERE b.failed_at IS NULL AND b.created_at < @cutoff
		        AND NOT EXISTS (
		          SELECT 1 FROM devices d
		          WHERE d.batch_id = b.batch_id AND d.status != @deleted)
		      ORDER BY FARM_FINGERPRINT(CONCAT(b.batch_id, @salt))
		      LIMIT @limit`,
		Params: map[string]any{
			"cutoff": cutoff, "deleted": "deleted", "limit": int64(limit),
			spannerplatform.SweepSaltParam: spannerplatform.SweepSalt(),
		},
	}
	var ids []string
	if err := r.client.Single().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		var id string
		if err := row.Columns(&id); err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("listing empty batches: %w", err)
	}
	return ids, nil
}

// ListFailedWithLiveMembers returns ids of tripped atomic batches that still
// have member devices outside teardown (neither deleting nor deleted). Such a
// batch is one whose winning sweep died between MarkFailed and the member
// deletes. The settle scan excludes tripped batches (they are settled), so
// this level check is the only path that finishes their teardown. It takes a
// strong read, because it feeds deletes.
func (r *Repository) ListFailedWithLiveMembers(ctx context.Context, limit int) ([]string, error) {
	stmt := spanner.Statement{
		SQL: `SELECT b.batch_id FROM batches b
		      WHERE b.fill_policy = @fill AND b.failed_at IS NOT NULL
		        AND EXISTS (
		          SELECT 1 FROM devices d
		          WHERE d.batch_id = b.batch_id AND d.status NOT IN ('deleting', 'deleted'))
		      ORDER BY FARM_FINGERPRINT(CONCAT(b.batch_id, @salt))
		      LIMIT @limit`,
		Params: map[string]any{
			"fill": string(batch.FillAtomic), "limit": int64(limit),
			spannerplatform.SweepSaltParam: spannerplatform.SweepSalt(),
		},
	}
	var ids []string
	if err := r.client.Single().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		var id string
		if err := row.Columns(&id); err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("listing tripped batches with live members: %w", err)
	}
	return ids, nil
}

// MarkReady latches ready_at, reporting whether this call won the transition.
// Guarded on the row being unsettled so leader churn cannot re-judge a batch.
func (r *Repository) MarkReady(ctx context.Context, id string, at time.Time) (bool, error) {
	return r.settle(ctx, spanner.Statement{
		SQL: `UPDATE batches SET ready_at = @at
		      WHERE batch_id = @id AND ready_at IS NULL AND failed_at IS NULL`,
		Params: map[string]any{"id": id, "at": at},
	})
}

// MarkFailed trips the atomic policy, reporting whether this call won the
// transition. Only the winner tears the members down, so a leader change
// mid-sweep cannot double-fail or double-delete.
func (r *Repository) MarkFailed(ctx context.Context, id string, at time.Time, reason string) (bool, error) {
	return r.settle(ctx, spanner.Statement{
		SQL: `UPDATE batches SET failed_at = @at, failure_reason = @reason
		      WHERE batch_id = @id AND ready_at IS NULL AND failed_at IS NULL`,
		Params: map[string]any{"id": id, "at": at, "reason": reason},
	})
}

func (r *Repository) settle(ctx context.Context, stmt spanner.Statement) (bool, error) {
	var count int64
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		var uerr error
		count, uerr = txn.Update(ctx, stmt)
		if uerr != nil {
			return fmt.Errorf("settling batch: %w", uerr)
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("batch settle transaction: %w", err)
	}
	return count == 1, nil
}

// Delete removes the batch policy row. Idempotent: deleting an absent row is
// a no-op.
func (r *Repository) Delete(ctx context.Context, id string) error {
	mut := spanner.Delete("batches", spanner.Key{id})
	if _, err := r.client.Apply(ctx, []*spanner.Mutation{mut}); err != nil {
		return fmt.Errorf("deleting batch: %w", err)
	}
	return nil
}

func (r *Repository) queryBatches(ctx context.Context, txn *spanner.ReadOnlyTransaction, stmt spanner.Statement) ([]*batch.Batch, error) {
	return spannerplatform.QueryAll(txn.Query(ctx, stmt), "batches", (*models.BatchRow).ToDomain)
}
