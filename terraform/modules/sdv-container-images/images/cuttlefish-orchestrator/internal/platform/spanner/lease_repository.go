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
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/grpc/codes"
)

const (
	leaderLeasesTable = "leader_leases"

	colLeaseName = "lease_name"

	paramName = "name"
)

// LeaseRepository implements singleton coordination over the leader_leases
// table. It restores the cross-instance leadership the v27 single-tenant cut
// removed with distributed_locks, so a periodic job runs on exactly one
// instance even when several are serving.
type LeaseRepository struct {
	client *Client
}

// NewLeaseRepository creates a leader-lease repository.
func NewLeaseRepository(client *Client) *LeaseRepository {
	return &LeaseRepository{client: client}
}

// TryAcquire acquires or renews the named lease for holder, extending it by
// ttl. It reports whether holder owns the lease afterward. A lease is takeable
// when it is unheld, already held by holder, or expired. The takeability
// predicate IS the DML's WHERE clause, and both the expiry comparison and the
// new expiry are evaluated server-side (CURRENT_TIMESTAMP(), TrueTime-derived),
// so leadership never depends on instance clocks agreeing.
func (r *LeaseRepository) TryAcquire(ctx context.Context, name, holder string, ttl time.Duration) (bool, error) {
	acquired := false
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		acquired = false
		rows, err := txn.Update(ctx, spanner.Statement{
			// acquired_at survives a same-holder renewal; any takeover (or a
			// re-acquisition after release/expiry) restarts it.
			SQL: `UPDATE leader_leases
			      SET holder = @holder,
			          acquired_at = IF(holder = @holder AND acquired_at IS NOT NULL,
			                           acquired_at, CURRENT_TIMESTAMP()),
			          lease_expiry = TIMESTAMP_ADD(CURRENT_TIMESTAMP(), INTERVAL @ttl_ms MILLISECOND),
			          updated_at = CURRENT_TIMESTAMP()
			      WHERE lease_name = @name
			        AND (holder IS NULL OR holder = '' OR holder = @holder
			             OR lease_expiry IS NULL OR lease_expiry < CURRENT_TIMESTAMP())`,
			Params: map[string]any{paramName: name, paramHolder: holder, "ttl_ms": ttl.Milliseconds()},
		})
		if err != nil {
			return fmt.Errorf("updating lease: %w", err)
		}
		if rows > 0 {
			acquired = true
			return nil
		}
		// Zero rows: either a live lease held elsewhere, or no row yet.
		if _, err := txn.ReadRow(ctx, leaderLeasesTable, spanner.Key{name}, []string{colLeaseName}); err == nil {
			return nil // a different instance holds a live lease
		} else if spanner.ErrCode(err) != codes.NotFound {
			return fmt.Errorf("reading lease: %w", err)
		}
		if _, err := txn.Update(ctx, spanner.Statement{
			SQL: `INSERT INTO leader_leases (lease_name, holder, acquired_at, lease_expiry, updated_at)
			      VALUES (@name, @holder, CURRENT_TIMESTAMP(),
			              TIMESTAMP_ADD(CURRENT_TIMESTAMP(), INTERVAL @ttl_ms MILLISECOND),
			              CURRENT_TIMESTAMP())`,
			Params: map[string]any{paramName: name, paramHolder: holder, "ttl_ms": ttl.Milliseconds()},
		}); err != nil {
			return fmt.Errorf("creating lease: %w", err)
		}
		acquired = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("acquiring lease %q: %w", name, err)
	}
	return acquired, nil
}

// LeaderLease is one leader-election lease row, read for observability.
// Holder is empty and the timestamps zero when the lease is unheld.
type LeaderLease struct {
	Name       string
	Holder     string
	AcquiredAt time.Time // when the current holder first took the lease
	Expiry     time.Time // when the lease lapses without a renewal
	RenewedAt  time.Time // last acquire/renew/release write
}

// ListLeases returns every leader lease. Strong read: renewal recency is the
// liveness signal for the periodic jobs, and bounded staleness would alias a
// live renewal cadence into a false stall.
func (r *LeaseRepository) ListLeases(ctx context.Context) ([]LeaderLease, error) {
	var leases []LeaderLease
	iter := r.client.Single().Query(ctx, spanner.Statement{
		SQL: `SELECT lease_name, holder, acquired_at, lease_expiry, updated_at
		      FROM leader_leases ORDER BY lease_name`,
	})
	if err := iter.Do(func(row *spanner.Row) error {
		var (
			name               string
			holder             spanner.NullString
			acquiredAt, expiry spanner.NullTime
			updatedAt          time.Time
		)
		if err := row.Columns(&name, &holder, &acquiredAt, &expiry, &updatedAt); err != nil {
			return err
		}
		leases = append(leases, LeaderLease{
			Name:       name,
			Holder:     holder.StringVal,
			AcquiredAt: acquiredAt.Time,
			Expiry:     expiry.Time,
			RenewedAt:  updatedAt,
		})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("listing leader leases: %w", err)
	}
	return leases, nil
}

// Release relinquishes the lease if holder still owns it, letting another
// instance take over immediately rather than waiting for expiry. Releasing a
// lease held by someone else (or a missing lease) is a no-op, because the
// ownership check is the DML's WHERE clause.
func (r *LeaseRepository) Release(ctx context.Context, name, holder string) error {
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		if _, err := txn.Update(ctx, spanner.Statement{
			SQL: `UPDATE leader_leases
			      SET holder = NULL, lease_expiry = NULL, updated_at = CURRENT_TIMESTAMP()
			      WHERE lease_name = @name AND holder = @holder`,
			Params: map[string]any{paramName: name, paramHolder: holder},
		}); err != nil {
			return fmt.Errorf("updating lease: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("releasing lease %q: %w", name, err)
	}
	return nil
}
