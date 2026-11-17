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

package operationfeature

import (
	"context"
	"fmt"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// Reader is the narrow port this slice needs. The Spanner OperationsRepository
// satisfies it; declared here so the slice depends on three methods rather than
// on the repository.
type Reader interface {
	Get(ctx context.Context, id string) (*operation.Record, error)
	List(ctx context.Context, f spanner.ListFilter) ([]*operation.Record, error)
	ProgressByTasks(ctx context.Context, taskIDs []string) (map[string]operation.Progress, error)
}

// Handler serves the operations collection.
type Handler struct {
	ops Reader
}

// NewHandler builds the handler.
func NewHandler(ops Reader) *Handler {
	return &Handler{ops: ops}
}

// Register mounts the operation endpoints.
//
// Both return 200. The Operation is the response itself and carries the
// outcome, so a 202 acknowledgement would describe it wrongly. The verbs that
// create one return 200 for the same reason.
func (h *Handler) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-operation",
		Method:      http.MethodGet,
		Path:        "/v2/operations/{id}",
		Summary:     "Get an operation",
		Description: "Returns a long-running operation's progress and, once done, its result or error",
		Tags:        []string{"Operations"},
		Security:    []map[string][]string{{"bearerAuth": {}}},
		Errors:      []int{http.StatusNotFound},
	}, h.GetOperation)

	huma.Register(api, huma.Operation{
		OperationID: "list-operations",
		Method:      http.MethodGet,
		Path:        "/v2/operations",
		Summary:     "List operations",
		Description: "Returns operations newest-first, optionally filtered by resource or completion",
		Tags:        []string{"Operations"},
		Security:    []map[string][]string{{"bearerAuth": {}}},
	}, h.ListOperations)
}

// GetOperation returns one operation with its progress derived from the live task.
func (h *Handler) GetOperation(ctx context.Context, input *GetOperationInput) (*GetOperationOutput, error) {
	rec, err := h.ops.Get(ctx, input.ID)
	if err != nil {
		return nil, web.MapError(err)
	}
	progress, err := h.progressFor(ctx, []*operation.Record{rec})
	if err != nil {
		return nil, web.MapError(err)
	}
	return &GetOperationOutput{Body: operation.FromRecord(rec, progress[rec.TaskID])}, nil
}

// ListOperations returns a page of operations.
func (h *Handler) ListOperations(ctx context.Context, input *ListOperationsInput) (*ListOperationsOutput, error) {
	recs, err := h.ops.List(ctx, spanner.ListFilter{
		ResourceID: input.ResourceID,
		Done:       input.DoneFilter(),
		Limit:      input.Limit,
		Offset:     input.Offset,
	})
	if err != nil {
		return nil, web.MapError(err)
	}
	progress, err := h.progressFor(ctx, recs)
	if err != nil {
		return nil, web.MapError(err)
	}

	out := &ListOperationsOutput{}
	out.Body.Operations = make([]operation.Operation, 0, len(recs))
	for _, rec := range recs {
		out.Body.Operations = append(out.Body.Operations, operation.FromRecord(rec, progress[rec.TaskID]))
	}
	out.Body.Count = len(out.Body.Operations)
	return out, nil
}

// progressFor reads every page member's task in ONE query.
//
// Settled operations are skipped: their outcome is the answer, and their task row
// is usually gone anyway, so including them would widen the query for nothing.
func (h *Handler) progressFor(ctx context.Context, recs []*operation.Record) (map[string]operation.Progress, error) {
	ids := make([]string, 0, len(recs))
	for _, rec := range recs {
		if rec.Done || rec.TaskID == "" {
			continue
		}
		ids = append(ids, rec.TaskID)
	}
	if len(ids) == 0 {
		return map[string]operation.Progress{}, nil
	}
	progress, err := h.ops.ProgressByTasks(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("reading operation progress: %w", err)
	}
	return progress, nil
}
