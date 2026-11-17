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
	"fmt"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// Snapshots live on the host's local disk, so every path that destroys the
// disk while keeping the host row must flip their inventory rows to lost,
// where an operator can see it. The host feature owns these writes for the
// same reason it owns the device cascade in MarkHostLost: the mark must share
// the commit that dooms the disk.

// rowQuerier is the read half snapshotsLostMutations needs, satisfied by both
// a read-write transaction and a single-use read-only one.
type rowQuerier interface {
	Query(ctx context.Context, statement spanner.Statement) *spanner.RowIterator
}

// snapshotsLostMutations builds the updates marking every live snapshot row
// on a host lost. now stamps updated_at and lost_at; the latter arms the
// schema's row deletion policy, so a loss stays visible for a week and then
// stops cluttering the inventory.
func snapshotsLostMutations(ctx context.Context, q rowQuerier, hostID string, now time.Time) ([]*spanner.Mutation, error) {
	iter := q.Query(ctx, spanner.Statement{
		SQL: `SELECT snapshot_id FROM snapshots
		      WHERE host_id = @host AND state != @lost`,
		Params: map[string]any{"host": hostID, "lost": string(device.SnapshotStateLost)},
	})
	var muts []*spanner.Mutation
	if err := iter.Do(func(row *spanner.Row) error {
		var snapshotID string
		if err := row.Column(0, &snapshotID); err != nil {
			return fmt.Errorf("scanning snapshot id: %w", err)
		}
		muts = append(muts, spanner.Update("snapshots",
			[]string{"host_id", "snapshot_id", "state", "updated_at", "lost_at"},
			[]any{hostID, snapshotID, string(device.SnapshotStateLost), now, now}))
		return nil
	}); err != nil {
		return nil, fmt.Errorf("listing snapshots of host %s: %w", hostID, err)
	}
	return muts, nil
}

// SnapshotsLostMutations is the pre-read form, for callers folding the marks
// into their own commit. That is the re-provision path, whose incarnation bump
// is what dooms the disk. The read-then-commit window races nothing that
// matters: restore admission re-checks the snapshot against the host's live
// incarnation, so a row marked late mislabels a listing, never restores dead
// bytes.
func (r *HostRepository) SnapshotsLostMutations(ctx context.Context, hostID string) ([]*spanner.Mutation, error) {
	return snapshotsLostMutations(ctx, r.client.Single(), hostID, time.Now())
}
