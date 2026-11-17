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

package hostfeature

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// This file is the quota-coupled cluster: every write that admits a host
// against the global host quota, or releases it from that quota, so the host
// row and its quota adjustment always share one commit.

// Column names repeated across the quota-atomic mutations (goconst).
const (
	colScope     = "scope"
	colScopeID   = "scope_id"
	colUsedHosts = "used_hosts"
	colUpdatedAt = "updated_at"
)

// CreateWithQuotaIncrement atomically creates a host, increments the global host
// quota, and commits any extra mutations (e.g. the provision-task enqueue), so
// the host row, its quota usage, and its durable work all commit together or not
// at all. It returns a *quota.ExceededError (mapped to HTTP 429) when the limit
// is reached, leaving nothing written.
//
// The read-check-increment on the quota row is write-skew-prone. It is safe
// only under Spanner's default SERIALIZABLE isolation, which validates the
// read at commit. Under repeatable read two concurrent creates would both see
// headroom and both commit. See docs/development/architecture-decisions.md
// (ADR-1) before touching isolation anywhere.
func (r *HostRepository) CreateWithQuotaIncrement(ctx context.Context, h *host.Host, extra ...*spanner.Mutation) error {
	if h.ID == "" {
		h.ID = uuid.New().String()
	}
	now := time.Now()
	h.CreatedAt = now
	h.UpdatedAt = now
	if err := h.Validate(); err != nil {
		return fmt.Errorf("invalid host: %w", err)
	}
	hostMut, err := spanner.InsertStruct("hosts", models.FromHostDomain(h))
	if err != nil {
		return fmt.Errorf("failed to create host mutation: %w", err)
	}

	_, err = r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		row, err := txn.ReadRow(ctx, "quotas",
			spanner.Key{string(quota.ScopePlatform), quota.GlobalScopeID},
			[]string{"max_hosts", colUsedHosts, "high_water_used_hosts", "high_water_at"})
		if err != nil {
			if spanner.ErrCode(err) == codes.NotFound {
				return &quota.NotFoundError{Scope: quota.ScopePlatform, ScopeID: quota.GlobalScopeID}
			}
			return fmt.Errorf("reading quota: %w", err)
		}
		var maxHosts, usedHosts int64
		var highWater spanner.NullInt64
		var highWaterAt spanner.NullTime
		if err := row.Columns(&maxHosts, &usedHosts, &highWater, &highWaterAt); err != nil {
			return fmt.Errorf("scanning quota: %w", err)
		}
		if usedHosts+1 > maxHosts {
			return &quota.ExceededError{
				Scope:     quota.ScopePlatform,
				ScopeID:   quota.GlobalScopeID,
				Requested: 1,
				Available: maxHosts - usedHosts,
				MaxHosts:  maxHosts,
			}
		}
		newUsed := usedHosts + 1
		cols := []string{colScope, colScopeID, colUsedHosts, colUpdatedAt}
		vals := []any{string(quota.ScopePlatform), quota.GlobalScopeID, newUsed, now}
		// Usage only rises here, so maintaining the watermark on this one path
		// captures every peak. A watermark older than the window is replaced
		// instead of raised, so the gauge reads "peak this week" and not
		// all-time.
		windowLapsed := !highWaterAt.Valid || now.Sub(highWaterAt.Time) > quota.HighWaterWindow
		if windowLapsed || newUsed > highWater.Int64 {
			cols = append(cols, "high_water_used_hosts", "high_water_at")
			vals = append(vals, newUsed, now)
		}
		quotaMut := spanner.Update("quotas", cols, vals)
		return txn.BufferWrite(append([]*spanner.Mutation{hostMut, quotaMut}, extra...))
	})
	if err != nil {
		// Preserve typed quota errors unwrapped so the API maps them (e.g. 429).
		if exceeded, ok := errors.AsType[*quota.ExceededError](err); ok {
			return exceeded
		}
		return fmt.Errorf("failed to create host atomically: %w", err)
	}
	return nil
}

// usedHostsDecrement builds the quota update releasing one host, and is empty
// when no quota row exists. Every path that removes a host from service (the
// deprovision teardown and the lost-VM mark) folds this into the same commit
// as the host write, so freed quota can never be lost to a crash between
// writes or leak behind a host no delete flow will ever visit again.
func usedHostsDecrement(ctx context.Context, txn *spanner.ReadWriteTransaction) ([]*spanner.Mutation, error) {
	row, err := txn.ReadRow(ctx, "quotas",
		spanner.Key{string(quota.ScopePlatform), quota.GlobalScopeID},
		[]string{colUsedHosts})
	if spanner.ErrCode(err) == codes.NotFound {
		return nil, nil // No quota row; nothing to release.
	}
	if err != nil {
		return nil, fmt.Errorf("reading quota: %w", err)
	}
	var usedHosts int64
	if err := row.Columns(&usedHosts); err != nil {
		return nil, fmt.Errorf("scanning quota: %w", err)
	}
	return []*spanner.Mutation{spanner.Update("quotas",
		[]string{colScope, colScopeID, colUsedHosts, colUpdatedAt},
		[]any{string(quota.ScopePlatform), quota.GlobalScopeID, max(usedHosts-1, 0), time.Now()})}, nil
}

// ReconcileUsedHosts is the level-based correction for the used_hosts counter:
// it recounts quota-holding hosts and heals the global counter when the two
// disagree. The count, the counter read, and the healing write share ONE
// read-write transaction, so the count's read set joins serializable
// validation: a host create or delete committing mid-reconcile aborts and
// re-runs this transaction instead of having its counter update silently
// overwritten by a recount that predates it. Returns the values the
// transaction settled on; it healed exactly when they differ.
//
// The status set counts hosts that hold (or are bringing up) a real GCE VM.
// Omitting 'starting' would free in-use quota during the first-boot window
// and over-provision.
func (r *HostRepository) ReconcileUsedHosts(ctx context.Context) (int64, int64, error) {
	var counter, actual int64
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		counter, actual = 0, 0 // Reset: the transaction body may run more than once.
		stmt := spanner.Statement{
			SQL: `SELECT COUNT(*) as cnt
			      FROM hosts
			      WHERE status IN ('running', 'pending', 'provisioning', 'starting', 'stopped')`,
		}
		count, err := spannerplatform.QueryCount(txn.Query(ctx, stmt), "quota-holding hosts")
		if err != nil {
			return err
		}
		actual = count
		row, err := txn.ReadRow(ctx, "quotas",
			spanner.Key{string(quota.ScopePlatform), quota.GlobalScopeID},
			[]string{colUsedHosts})
		if err != nil {
			if spanner.ErrCode(err) == codes.NotFound {
				return &quota.NotFoundError{Scope: quota.ScopePlatform, ScopeID: quota.GlobalScopeID}
			}
			return fmt.Errorf("reading quota: %w", err)
		}
		if err := row.Columns(&counter); err != nil {
			return fmt.Errorf("scanning quota: %w", err)
		}
		if counter == actual {
			return nil
		}
		return txn.BufferWrite([]*spanner.Mutation{spanner.Update("quotas",
			[]string{colScope, colScopeID, colUsedHosts, colUpdatedAt},
			[]any{string(quota.ScopePlatform), quota.GlobalScopeID, actual, time.Now()})})
	})
	if err != nil {
		if notFound, ok := errors.AsType[*quota.NotFoundError](err); ok {
			return 0, 0, notFound
		}
		return 0, 0, fmt.Errorf("reconciling used_hosts: %w", err)
	}
	return counter, actual, nil
}

// DeleteWithQuotaDecrement atomically deletes a host and decrements the global
// host quota, so the removal and the freed quota commit together. Deleting the
// host cascades its interleaved device rows.
func (r *HostRepository) DeleteWithQuotaDecrement(ctx context.Context, id string) error {
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		// Idempotent: gate both writes on the host still existing. A deprovision
		// task is at-least-once, so this can run twice (a redelivery whose first
		// run committed but never acked, or two concurrent deliveries). Without
		// this read the second run would blind-decrement used_hosts again,
		// under-counting quota until ReconcileAll heals it. The read joins the
		// serializable read-set, so concurrent second runs abort and re-observe
		// the row gone rather than both decrementing.
		if _, err := txn.ReadRow(ctx, "hosts", spanner.Key{id}, []string{colHostID}); err != nil {
			if spanner.ErrCode(err) == codes.NotFound {
				return nil // already deleted; quota already freed
			}
			return fmt.Errorf("reading host: %w", err)
		}
		dec, err := usedHostsDecrement(ctx, txn)
		if err != nil {
			return err
		}
		muts := append([]*spanner.Mutation{spanner.Delete("hosts", spanner.Key{id})}, dec...)
		return txn.BufferWrite(muts)
	})
	if err != nil {
		return fmt.Errorf("failed to delete host atomically: %w", err)
	}
	return nil
}
