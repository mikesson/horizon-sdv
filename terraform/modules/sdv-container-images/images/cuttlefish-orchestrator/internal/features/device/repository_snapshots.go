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

package devicefeature

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	deviceDomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// Snapshot rows are the platform's snapshot inventory. The Host Orchestrator
// keeps none, so these methods are the record of what exists and whether it
// is still restorable. Rows are keyed (host_id, snapshot_id), interleaved in
// hosts; every reader here addresses them through the device index.

var snapshotColumnsSQL = strings.Join(snapshotColumns(), ", ")

func snapshotColumns() []string {
	return []string{
		colHostID, colSnapshotID, colDeviceID,
		"cvd_group", "cvd_name", "host_incarnation",
		"state", "created_by", colCreatedAtDev, colUpdatedAtDev, "lost_at",
	}
}

// SnapshotInsert returns the insert mutation for a snapshot row, for folding
// into the take verb's admission transaction. The row and the operation must
// appear atomically or an admitted take has no inventory row to settle.
func (r *DeviceRepository) SnapshotInsert(s *deviceDomain.Snapshot) (*spanner.Mutation, error) {
	m, err := spanner.InsertStruct("snapshots", models.FromSnapshotDomain(s))
	if err != nil {
		return nil, fmt.Errorf("failed to build snapshot insert: %w", err)
	}
	return m, nil
}

// GetSnapshot retrieves one of a device's snapshots.
func (r *DeviceRepository) GetSnapshot(ctx context.Context, deviceID, snapshotID string) (*deviceDomain.Snapshot, error) {
	stmt := spanner.Statement{
		SQL: `SELECT ` + snapshotColumnsSQL + ` FROM snapshots
		      WHERE device_id = @deviceID AND snapshot_id = @snapshotID`,
		Params: map[string]any{paramDeviceID: deviceID, "snapshotID": snapshotID},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, &deviceDomain.SnapshotNotFoundError{ID: snapshotID}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get snapshot: %w", err)
	}
	var sRow models.SnapshotRow
	if err := row.ToStruct(&sRow); err != nil {
		return nil, fmt.Errorf("failed to scan snapshot row: %w", err)
	}
	return sRow.ToDomain(), nil
}

// ListSnapshotsByDevice returns a device's snapshots, newest first.
func (r *DeviceRepository) ListSnapshotsByDevice(ctx context.Context, deviceID string) ([]*deviceDomain.Snapshot, error) {
	stmt := spanner.Statement{
		SQL: `SELECT ` + snapshotColumnsSQL + ` FROM snapshots
		      WHERE device_id = @deviceID ORDER BY created_at DESC`,
		Params: map[string]any{paramDeviceID: deviceID},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	var out []*deviceDomain.Snapshot
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("failed to list snapshots: %w", err)
		}
		var sRow models.SnapshotRow
		if err := row.ToStruct(&sRow); err != nil {
			return nil, fmt.Errorf("failed to scan snapshot row: %w", err)
		}
		out = append(out, sRow.ToDomain())
	}
}

// CountLiveSnapshotsByDevice counts a device's creating and ready snapshots,
// the ones occupying host disk (or about to). Lost rows are history, not
// occupancy, and must not block a new take.
func (r *DeviceRepository) CountLiveSnapshotsByDevice(ctx context.Context, deviceID string) (int64, error) {
	stmt := spanner.Statement{
		SQL: `SELECT COUNT(*) FROM snapshots
		      WHERE device_id = @deviceID AND state IN ('creating', 'ready')`,
		Params: map[string]any{paramDeviceID: deviceID},
	}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if err != nil {
		return 0, fmt.Errorf("failed to count snapshots: %w", err)
	}
	var n int64
	if err := row.Columns(&n); err != nil {
		return 0, fmt.Errorf("failed to scan snapshot count: %w", err)
	}
	return n, nil
}

// MarkSnapshotReady settles a successful take: creating → ready.
func (r *DeviceRepository) MarkSnapshotReady(ctx context.Context, hostID, snapshotID string) error {
	_, err := r.client.Apply(ctx, []*spanner.Mutation{
		spanner.Update("snapshots",
			[]string{colHostID, colSnapshotID, "state", colUpdatedAtDev},
			[]any{hostID, snapshotID, string(deviceDomain.SnapshotStateReady), time.Now()}),
	})
	if err != nil {
		return fmt.Errorf("failed to mark snapshot ready: %w", err)
	}
	return nil
}

// DeleteSnapshotRow removes a snapshot's inventory row, after a host-side
// delete, after a failed take (nothing durable was produced), or when the
// host holding the bytes is already gone.
//
// Idempotent: deleting an absent row succeeds, matching the host-side remove
// it mirrors, so the delete verb's redelivery budget is safe.
func (r *DeviceRepository) DeleteSnapshotRow(ctx context.Context, hostID, snapshotID string) error {
	_, err := r.client.Apply(ctx, []*spanner.Mutation{
		spanner.Delete("snapshots", spanner.Key{hostID, snapshotID}),
	})
	if err != nil {
		return fmt.Errorf("failed to delete snapshot row: %w", err)
	}
	return nil
}

// snapshotDeleteMutations returns delete mutations for every snapshot row of
// a device, read within txn. It is the device-delete cascade. A snapshot
// cannot outlive its device (restore needs the instance), and the tombstoned
// device row would otherwise anchor dead inventory for a week.
func snapshotDeleteMutations(ctx context.Context, txn *spanner.ReadWriteTransaction, deviceID string) ([]*spanner.Mutation, error) {
	iter := txn.Query(ctx, spanner.Statement{
		SQL:    `SELECT host_id, snapshot_id FROM snapshots WHERE device_id = @deviceID`,
		Params: map[string]any{paramDeviceID: deviceID},
	})
	var muts []*spanner.Mutation
	if err := iter.Do(func(row *spanner.Row) error {
		var hostID, snapshotID string
		if err := row.Columns(&hostID, &snapshotID); err != nil {
			return fmt.Errorf("scanning snapshot key: %w", err)
		}
		muts = append(muts, spanner.Delete("snapshots", spanner.Key{hostID, snapshotID}))
		return nil
	}); err != nil {
		return nil, fmt.Errorf("listing snapshots of device %s: %w", deviceID, err)
	}
	return muts, nil
}
