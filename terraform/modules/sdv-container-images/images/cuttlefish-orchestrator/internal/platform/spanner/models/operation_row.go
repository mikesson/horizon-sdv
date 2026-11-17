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
	"encoding/json"
	"fmt"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/operation"
)

// Operations table and column names.
const (
	OperationsTable = "operations"

	ColOperationID   = "operation_id"
	ColOperationVerb = "verb"
	ColResourceType  = "resource_type"
	ColOperationDone = "done"
	ColRequest       = "request"
	ColResponse      = "response"
	ColError         = "error"
	ColExclusive     = "exclusive"
	ColMutating      = "mutating"

	// Timestamp columns, named here because the shared constants live in the
	// parent package and models may not import it.
	colOperationCreatedAt = "created_at"
	colOperationUpdatedAt = "updated_at"
	colOperationCreatedBy = "created_by"
	colOperationResource  = "resource_id"
	colOperationInstance  = "instance_name"
	colOperationTaskID    = "task_id"
)

// OperationSelectList is the column list a full operation read selects, in the
// order ScanOperation's positional read expects. The two are kept in step by
// the emulator repository tests, which select through this list, scan through
// that function and assert the field values, so a reordering of either shows up
// there as a type error or a swapped value.
const OperationSelectList = `operation_id, verb, resource_type, resource_id, instance_name, task_id,
	done, request, response, error, exclusive, mutating, created_by, created_at, updated_at`

// OperationInsert builds the insert for a new operation.
//
// The generated columns (exclusive_key, mutating_key, open_at) are absent
// because Spanner computes them, and naming one here is an error.
func OperationInsert(r *operation.Record, now time.Time) (*spanner.Mutation, error) {
	req, err := toNullJSONRaw(r.Request)
	if err != nil {
		return nil, fmt.Errorf("encoding operation request: %w", err)
	}
	return spanner.InsertMap(OperationsTable, map[string]any{
		ColOperationID:        r.ID,
		ColOperationVerb:      string(r.Verb),
		ColResourceType:       r.ResourceType,
		colOperationResource:  r.ResourceID,
		colOperationInstance:  ToNullString(r.InstanceName),
		colOperationTaskID:    ToNullString(r.TaskID),
		ColOperationDone:      r.Done,
		ColRequest:            req,
		ColExclusive:          r.Exclusive,
		ColMutating:           r.Mutating,
		colOperationCreatedBy: ToNullString(r.CreatedBy),
		colOperationCreatedAt: now,
		colOperationUpdatedAt: now,
	}), nil
}

// ScanOperation reads a row selected with OperationSelectList.
func ScanOperation(row *spanner.Row) (*operation.Record, error) {
	var (
		id, verb, resourceType, resourceID string
		instanceName, taskID, createdBy    spanner.NullString
		done                               bool
		request, response, problem         spanner.NullJSON
		exclusive, mutating                bool
		createdAt, updatedAt               time.Time
	)
	if err := row.Columns(&id, &verb, &resourceType, &resourceID, &instanceName, &taskID,
		&done, &request, &response, &problem, &exclusive, &mutating,
		&createdBy, &createdAt, &updatedAt); err != nil {
		return nil, fmt.Errorf("scanning operation row: %w", err)
	}

	rec := &operation.Record{
		ID:           id,
		Verb:         operation.Verb(verb),
		ResourceType: resourceType,
		ResourceID:   resourceID,
		InstanceName: FromNullString(instanceName),
		TaskID:       FromNullString(taskID),
		Done:         done,
		Request:      fromNullJSONRaw(request),
		Response:     fromNullJSONRaw(response),
		Exclusive:    exclusive,
		Mutating:     mutating,
		CreatedBy:    FromNullString(createdBy),
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
	}
	if raw := fromNullJSONRaw(problem); raw != nil {
		var p operation.Problem
		// A stored document that will not decode must not fail the read: the
		// operation's own outcome is still knowable, and returning nothing here
		// would hide it behind a 500.
		if err := json.Unmarshal(raw, &p); err == nil {
			rec.Error = &p
		}
	}
	return rec, nil
}

// toNullJSONRaw wraps pre-marshaled JSON for a nullable JSON column. The value
// round-trips through any so Spanner stores the document rather than a string.
func toNullJSONRaw(raw json.RawMessage) (spanner.NullJSON, error) {
	if len(raw) == 0 {
		return spanner.NullJSON{}, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return spanner.NullJSON{}, fmt.Errorf("value is not valid JSON: %w", err)
	}
	return spanner.NullJSON{Value: v, Valid: true}, nil
}

// fromNullJSONRaw re-marshals a JSON column back to raw bytes, returning nil
// when the column is null.
func fromNullJSONRaw(n spanner.NullJSON) json.RawMessage {
	if !n.Valid {
		return nil
	}
	raw, err := json.Marshal(n.Value)
	if err != nil {
		return nil
	}
	return raw
}
