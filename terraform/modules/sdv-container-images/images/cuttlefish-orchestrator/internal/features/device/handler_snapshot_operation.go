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
	"encoding/json"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// SnapshotVerbDefinition holds what distinguishes one snapshot verb from
// another: its actuation and its audit action. It sits beside
// VerbDefinition instead of inside it because these verbs address a
// SNAPSHOT of a device. The actuation needs the id from the stored request
// and returns a verb-specific response body, neither of which the plain
// in-place verbs have.
type SnapshotVerbDefinition struct {
	Verb    operation.Verb
	Label   string
	Action  audit.Action
	Actuate func(*DeviceService, context.Context, *operation.Record, string) (map[string]any, error)
}

// SnapshotVerbHandler actuates one snapshot verb from the queue and records
// its outcome on the operation handle. The redelivery discipline is copied
// from VerbOperationHandler: the done-guard protects the DEVICE from a second
// actuation, the terminal-write CAS protects the record, a transient failure
// with attempts left goes back to the queue, and a device that moved past the
// verb settles it on the first attempt.
type SnapshotVerbHandler struct {
	def SnapshotVerbDefinition
	svc *DeviceService
	ops OperationStore
	log *slog.Logger
}

// NewSnapshotVerbHandler builds the handler for one snapshot verb.
func NewSnapshotVerbHandler(def SnapshotVerbDefinition, svc *DeviceService, ops OperationStore,
	logger *slog.Logger,
) (*SnapshotVerbHandler, error) {
	if _, err := operation.PolicyFor(def.Verb); err != nil {
		return nil, fmt.Errorf("building %s handler: %w", def.Verb, err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SnapshotVerbHandler{def: def, svc: svc, ops: ops, log: logger}, nil
}

// TaskType is the queue dispatch key, from the registry so the handler and
// admission cannot disagree.
func (h *SnapshotVerbHandler) TaskType() string {
	policy, err := operation.PolicyFor(h.def.Verb)
	if err != nil {
		// Unreachable: the constructor refuses an unregistered verb.
		return ""
	}
	return policy.TaskType
}

// Handle actuates the verb and settles its operation.
func (h *SnapshotVerbHandler) Handle(ctx context.Context, t *task.Task) error {
	rec, err := h.ops.GetByTask(ctx, t.ID)
	if err != nil {
		return fmt.Errorf("loading operation for task %s: %w", t.ID, err)
	}

	span := trace.SpanFromContext(ctx)
	span.SetAttributes(
		attribute.String("cco.operation.id", rec.ID),
		attribute.String("cco.operation.verb", string(rec.Verb)),
	)

	if rec.Done {
		h.log.InfoContext(ctx, "operation already settled; skipping actuation",
			slog.String("operation_id", rec.ID),
			slog.String("verb", string(rec.Verb)),
			slog.String("device_id", rec.ResourceID))
		return nil
	}

	snapshotID, err := snapshotIDFromRequest(rec)
	if err != nil {
		// This is unactuatable and not transient. Terminate instead of
		// burning retries on a request that can never parse differently.
		if terr := h.ops.Terminate(ctx, rec.ID, web.Problem(err), nil); terr != nil {
			return fmt.Errorf("recording operation %s failure: %w", rec.ID, terr)
		}
		return err
	}
	span.SetAttributes(attribute.String("cco.snapshot.id", snapshotID))

	body, actuateErr := h.def.Actuate(h.svc, ctx, rec, snapshotID)
	if actuateErr == nil {
		response, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding %s response: %w", rec.Verb, err)
		}
		if err := h.ops.Terminate(ctx, rec.ID, nil, response); err != nil {
			err = fmt.Errorf("recording operation %s success: %w", rec.ID, err)
			h.log.ErrorContext(ctx, "snapshot verb succeeded but recording its success failed",
				slog.String("operation_id", rec.ID),
				slog.String("verb", string(rec.Verb)),
				slog.Any("error", err))
			return err
		}
		h.auditSuccess(rec, snapshotID)
		return nil
	}

	if handled, err := settleIfSuperseded(ctx, h.ops, h.log, rec, actuateErr); handled {
		return err
	}

	if !t.Exhausted() {
		return actuateErr
	}
	if err := h.ops.Terminate(ctx, rec.ID, web.Problem(actuateErr), nil); err != nil {
		h.log.ErrorContext(ctx, "recording terminal operation failure failed",
			slog.String("operation_id", rec.ID), slog.Any("error", err))
		return fmt.Errorf("recording operation %s failure: %w", rec.ID, err)
	}
	return actuateErr
}

// auditSuccess records who asked, taking the actor from the operation row
// because a worker context carries no principal.
func (h *SnapshotVerbHandler) auditSuccess(rec *operation.Record, snapshotID string) {
	if h.svc.auditLogger == nil {
		return
	}
	h.svc.auditLogger.LogAsync(audit.New(h.def.Action, audit.ResourceDevice).
		WithActor(rec.CreatedBy).
		WithResource(rec.ResourceID).
		WithMetadata(map[string]any{
			"operation": operation.ResourceName(rec.ID),
			"snapshot":  snapshotID,
		}).
		Build())
}

// wireSnapshotVerbHandlers builds one queue handler per snapshot verb. Same
// contract as wireVerbHandlers: the verb table is slice knowledge, the
// registry owns everything else.
func wireSnapshotVerbHandlers(svc *DeviceService, ops VerbOperationStore, logger *slog.Logger) ([]task.Handler, error) {
	if ops == nil {
		return nil, nil
	}
	verbs := []SnapshotVerbDefinition{
		{operation.VerbDeviceSnapshot, "snapshot", audit.ActionDeviceSnapshot, (*DeviceService).runSnapshotTake},
		{operation.VerbDeviceSnapshotDelete, "snapshot delete", audit.ActionDeviceSnapshotDelete, (*DeviceService).runSnapshotDelete},
		{operation.VerbDeviceSnapshotRestore, "snapshot restore", audit.ActionDeviceSnapshotRestore, (*DeviceService).runSnapshotRestore},
	}
	handlers := make([]task.Handler, 0, len(verbs))
	for _, def := range verbs {
		h, err := NewSnapshotVerbHandler(def, svc, ops, logger)
		if err != nil {
			return nil, err
		}
		handlers = append(handlers, h)
	}
	return handlers, nil
}
