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

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
)

// AuditRow represents the Spanner table schema for audit logs.
type AuditRow struct {
	LogID        string             `spanner:"log_id"`
	Timestamp    time.Time          `spanner:"timestamp"`
	ActorEmail   spanner.NullString `spanner:"actor_email"`
	Action       string             `spanner:"action"`
	ResourceType string             `spanner:"resource_type"`
	ResourceID   spanner.NullString `spanner:"resource_id"`
	RequestID    spanner.NullString `spanner:"request_id"`
	Changes      spanner.NullJSON   `spanner:"changes"`
	Metadata     spanner.NullJSON   `spanner:"metadata"`
}

// ToDomain reads an audit row. The nullable columns split two ways: the
// resource and request identifiers stay pointers, so an unattributed entry is
// distinguishable from one attributed to the empty string, while the actor's
// email degrades to "". A changes or metadata payload that is not a JSON
// object yields an empty map rather than failing the read.
func (r *AuditRow) ToDomain() audit.Entry {
	e := audit.Entry{
		ID:           r.LogID,
		Timestamp:    r.Timestamp,
		Action:       audit.Action(r.Action),
		ResourceType: audit.ResourceType(r.ResourceType),
	}
	if r.ActorEmail.Valid {
		e.ActorEmail = r.ActorEmail.StringVal
	}
	if r.ResourceID.Valid {
		e.ResourceID = &r.ResourceID.StringVal
	}

	if r.RequestID.Valid {
		e.RequestID = &r.RequestID.StringVal
	}
	e.Changes = FromNullJSONMap(r.Changes)
	e.Metadata = FromNullJSONMap(r.Metadata)
	return e
}

// FromAuditDomain builds the row for an append-only insert: nothing here
// defaults the log ID or the timestamp, so the caller must have set both. Nil
// changes or metadata leave the column NULL, while an empty-but-present map is
// written as an empty JSON object and reads back distinct from absent.
func FromAuditDomain(e audit.Entry) *AuditRow {
	r := &AuditRow{
		LogID:        e.ID,
		Timestamp:    e.Timestamp,
		Action:       string(e.Action),
		ResourceType: string(e.ResourceType),
	}
	r.ActorEmail = ToNullString(e.ActorEmail)
	r.ResourceID = ToNullStringPtr(e.ResourceID)
	r.RequestID = ToNullStringPtr(e.RequestID)
	if e.Changes != nil {
		r.Changes = spanner.NullJSON{Value: e.Changes, Valid: true}
	}
	if e.Metadata != nil {
		r.Metadata = spanner.NullJSON{Value: e.Metadata, Valid: true}
	}
	return r
}
