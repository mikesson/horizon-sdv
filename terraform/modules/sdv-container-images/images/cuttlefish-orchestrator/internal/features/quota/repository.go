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

package quotafeature

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// Table and column names repeated across the quota reads and writes (goconst).
const (
	tableQuotas  = "quotas"
	colScope     = "scope"
	colScopeID   = "scope_id"
	colMaxHosts  = "max_hosts"
	colUsedHosts = "used_hosts"
	colUpdatedAt = "updated_at"
)

// quotaColumns is the full projection for quota reads; SELECT * breaks on
// additive migrations because ToStruct rejects unknown columns.
func quotaColumns() []string {
	return []string{colScope, colScopeID, colMaxHosts, colUsedHosts, "high_water_used_hosts", "high_water_at", "created_at", colUpdatedAt}
}

// Repository implements quota.Repository using Cloud Spanner.
type Repository struct {
	client *spannerplatform.Client
}

// NewRepository creates a new quota repository.
func NewRepository(client *spannerplatform.Client) *Repository {
	return &Repository{client: client}
}

// Get retrieves a quota by scope. Returns NotFoundError if not found.
func (r *Repository) Get(ctx context.Context, scope quota.Scope, scopeID string) (*quota.Quota, error) {
	row, err := r.client.Single().ReadRow(ctx, tableQuotas,
		spanner.Key{string(scope), scopeID}, quotaColumns())
	if err != nil {
		if spanner.ErrCode(err) == codes.NotFound {
			return nil, &quota.NotFoundError{Scope: scope, ScopeID: scopeID}
		}
		return nil, fmt.Errorf("getting quota: %w", err)
	}

	var qRow models.QuotaRow
	if err := row.ToStruct(&qRow); err != nil {
		return nil, fmt.Errorf("scanning quota: %w", err)
	}

	return qRow.ToDomain(), nil
}

// Set creates a quota or updates the limit of an existing one.
//
// The existence check and the write share one read-write transaction, and an
// existing row only gets max_hosts/updated_at written: used_hosts is owned by
// the host feature's atomic create/delete commits (with Reconcile as the
// drift healer) and created_at is immutable, so a counter commit racing an
// admin limit change can never be reverted.
func (r *Repository) Set(ctx context.Context, q *quota.Quota) error {
	if err := q.Validate(); err != nil {
		return fmt.Errorf("invalid quota: %w", err)
	}

	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		now := time.Now()

		_, err := txn.ReadRow(ctx, tableQuotas,
			spanner.Key{string(q.Scope), q.ScopeID}, []string{colScope})
		switch {
		case err == nil:
			return txn.BufferWrite([]*spanner.Mutation{spanner.Update(tableQuotas,
				[]string{colScope, colScopeID, colMaxHosts, colUpdatedAt},
				[]any{string(q.Scope), q.ScopeID, q.MaxHosts, now},
			)})

		case spanner.ErrCode(err) == codes.NotFound:
			createdAt := q.CreatedAt
			if createdAt.IsZero() {
				createdAt = now
			}
			return txn.BufferWrite([]*spanner.Mutation{spanner.InsertMap(tableQuotas, map[string]any{
				colScope:     string(q.Scope),
				colScopeID:   q.ScopeID,
				colMaxHosts:  q.MaxHosts,
				colUsedHosts: q.UsedHosts,
				"created_at": createdAt,
				colUpdatedAt: now,
			})})

		default:
			return fmt.Errorf("reading quota: %w", err)
		}
	})
	if err != nil {
		return fmt.Errorf("setting quota: %w", err)
	}

	return nil
}

// Ensure Repository implements quota.Repository.
var _ quota.Repository = (*Repository)(nil)
