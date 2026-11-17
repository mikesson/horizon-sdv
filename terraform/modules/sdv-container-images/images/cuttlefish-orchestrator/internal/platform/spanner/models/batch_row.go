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

	"cloud-cuttlefish-orchestrator/internal/domain/batch"
)

// BatchRow represents the Spanner table schema for batches: the batch-level
// policy plus the sweep's settle markers. Membership and status stay derived
// from the member device rows.
type BatchRow struct {
	BatchID             string             `spanner:"batch_id"`
	NamePrefix          string             `spanner:"name_prefix"`
	RequestedCount      int64              `spanner:"requested_count"`
	FillPolicy          string             `spanner:"fill_policy"`
	Placement           string             `spanner:"placement"`
	ReadyTimeoutSeconds spanner.NullInt64  `spanner:"ready_timeout_seconds"`
	SpecFingerprint     string             `spanner:"spec_fingerprint"`
	CreatedBy           spanner.NullString `spanner:"created_by"`
	CreatedAt           time.Time          `spanner:"created_at"`
	ReadyAt             spanner.NullTime   `spanner:"ready_at"`
	FailedAt            spanner.NullTime   `spanner:"failed_at"`
	FailureReason       spanner.NullString `spanner:"failure_reason"`
}

// ToDomain converts the row to the domain batch.
func (r *BatchRow) ToDomain() *batch.Batch {
	b := &batch.Batch{
		ID:              r.BatchID,
		NamePrefix:      r.NamePrefix,
		RequestedCount:  int(r.RequestedCount),
		FillPolicy:      batch.FillPolicy(r.FillPolicy),
		Placement:       batch.Placement(r.Placement),
		SpecFingerprint: r.SpecFingerprint,
		CreatedBy:       FromNullString(r.CreatedBy),
		CreatedAt:       r.CreatedAt,
		ReadyAt:         FromNullTimePtr(r.ReadyAt),
		FailedAt:        FromNullTimePtr(r.FailedAt),
		FailureReason:   FromNullString(r.FailureReason),
	}
	if r.ReadyTimeoutSeconds.Valid {
		b.ReadyTimeout = time.Duration(r.ReadyTimeoutSeconds.Int64) * time.Second
	}
	return b
}

// FromBatchDomain converts a domain batch to its row.
func FromBatchDomain(b *batch.Batch) *BatchRow {
	r := &BatchRow{
		BatchID:         b.ID,
		NamePrefix:      b.NamePrefix,
		RequestedCount:  int64(b.RequestedCount),
		FillPolicy:      string(b.FillPolicy),
		Placement:       string(b.Placement),
		SpecFingerprint: b.SpecFingerprint,
		CreatedBy:       ToNullString(b.CreatedBy),
		CreatedAt:       b.CreatedAt,
		ReadyAt:         ToNullTime(b.ReadyAt),
		FailedAt:        ToNullTime(b.FailedAt),
		FailureReason:   ToNullString(b.FailureReason),
	}
	if b.ReadyTimeout > 0 {
		r.ReadyTimeoutSeconds = spanner.NullInt64{Int64: int64(b.ReadyTimeout / time.Second), Valid: true}
	}
	return r
}
