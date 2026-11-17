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

package models

import (
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// SnapshotRow represents the Spanner table schema for snapshots.
type SnapshotRow struct {
	HostID          string             `spanner:"host_id"`
	SnapshotID      string             `spanner:"snapshot_id"`
	DeviceID        string             `spanner:"device_id"`
	CVDGroup        string             `spanner:"cvd_group"`
	CVDName         string             `spanner:"cvd_name"`
	HostIncarnation int64              `spanner:"host_incarnation"`
	State           string             `spanner:"state"`
	CreatedBy       spanner.NullString `spanner:"created_by"`
	CreatedAt       time.Time          `spanner:"created_at"`
	UpdatedAt       time.Time          `spanner:"updated_at"`

	// LostAt arms the row deletion policy when state becomes 'lost'. Storage
	// bookkeeping only. The domain reports the loss through State.
	LostAt spanner.NullTime `spanner:"lost_at"`
}

// ToDomain converts the row to its domain entity.
func (r *SnapshotRow) ToDomain() *device.Snapshot {
	return &device.Snapshot{
		ID:              r.SnapshotID,
		DeviceID:        r.DeviceID,
		HostID:          r.HostID,
		CVDGroup:        r.CVDGroup,
		CVDName:         r.CVDName,
		HostIncarnation: r.HostIncarnation,
		State:           device.SnapshotState(r.State),
		CreatedBy:       FromNullString(r.CreatedBy),
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
}

// FromSnapshotDomain converts a domain Snapshot to a SnapshotRow.
func FromSnapshotDomain(s *device.Snapshot) *SnapshotRow {
	return &SnapshotRow{
		HostID:          s.HostID,
		SnapshotID:      s.ID,
		DeviceID:        s.DeviceID,
		CVDGroup:        s.CVDGroup,
		CVDName:         s.CVDName,
		HostIncarnation: s.HostIncarnation,
		State:           string(s.State),
		CreatedBy:       ToNullString(s.CreatedBy),
		CreatedAt:       s.CreatedAt,
		UpdatedAt:       s.UpdatedAt,
	}
}
