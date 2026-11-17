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
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// OperationStore is the narrow port the verb handler needs from the operations
// repository. It is declared here, where it is consumed, so the handler depends
// on two methods instead of the whole repository.
type OperationStore interface {
	GetByTask(ctx context.Context, taskID string) (*operation.Record, error)
	Terminate(ctx context.Context, id string, problem *operation.Problem, response []byte) error
}

// VerbOperationStore is everything an async verb needs on the request path
// (admission) and in the worker (terminal writes). The slice takes one
// collaborator because one object satisfies both; the two ports stay separate
// so each consumer names only what it uses.
type VerbOperationStore interface {
	VerbAdmitter
	OperationStore
}

// VerbDefinition is what distinguishes one async in-place verb from another:
// which client method actuates it, what it is called in errors, and which audit
// action it records. Everything else about a verb (exclusivity, retry budget,
// response shape) comes from the registry in the domain.
type VerbDefinition struct {
	Verb   operation.Verb
	Label  string
	Action audit.Action
	Call   func(hoclient.Client, context.Context, string, string) error
}

// VerbOperationHandler actuates one in-place device verb from the queue and
// records its outcome on the operation handle.
//
// One handler type serves every such verb because they differ only in which `cvd`
// command runs. Their guards, resolution, terminal writes and audit are identical.
type VerbOperationHandler struct {
	def VerbDefinition
	svc *DeviceService
	ops OperationStore
	log *slog.Logger
}

// NewVerbOperationHandler builds the handler for one verb.
func NewVerbOperationHandler(def VerbDefinition, svc *DeviceService, ops OperationStore,
	logger *slog.Logger,
) (*VerbOperationHandler, error) {
	if _, err := operation.PolicyFor(def.Verb); err != nil {
		return nil, fmt.Errorf("building %s handler: %w", def.Verb, err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &VerbOperationHandler{def: def, svc: svc, ops: ops, log: logger}, nil
}

// TaskType is the queue dispatch key for this verb, taken from the registry so
// the handler and the admission path cannot disagree about it.
func (h *VerbOperationHandler) TaskType() string {
	policy, err := operation.PolicyFor(h.def.Verb)
	if err != nil {
		// Unreachable: the constructor refuses an unregistered verb.
		return ""
	}
	return policy.TaskType
}

// Handle actuates the verb and settles its operation.
//
// The FIRST act is to check whether the operation is already done, and that guard
// is what keeps an at-least-once redelivery from actuating twice. The window it
// closes is an ordinary one. The handler writes the terminal state before
// returning, and the queue's Complete runs afterwards and can fail, which is a
// sanctioned and logged path. Without this check the task is redelivered when its
// lease lapses and the verb runs a second time, on a device whose exclusivity was
// released by that same terminal write. The terminal-write CAS protects the
// RECORD; this protects the DEVICE, and they are not the same mechanism.
//
// It does not close a crash between the actuation and the terminal write. That
// window is milliseconds instead of the lease duration, and it is the same
// at-least-once exposure create carries and answers by observation.
func (h *VerbOperationHandler) Handle(ctx context.Context, t *task.Task) error {
	rec, err := h.ops.GetByTask(ctx, t.ID)
	if err != nil {
		return fmt.Errorf("loading operation for task %s: %w", t.ID, err)
	}

	span := trace.SpanFromContext(ctx)
	// Prefixed because hoclient already sets operation.name meaning the HOST
	// ORCHESTRATOR's operation; two unprefixed meanings on adjacent spans is the
	// collision nothing type-checks.
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

	actuateErr := h.actuate(ctx, rec)
	if actuateErr == nil {
		response, marshalErr := h.successResponse()
		if marshalErr != nil {
			return marshalErr
		}
		if err := h.ops.Terminate(ctx, rec.ID, nil, response); err != nil {
			err = fmt.Errorf("recording operation %s success: %w", rec.ID, err)
			// Returning the error hands the outcome back to the queue (retry,
			// then dead-letter, then the orphan sweep) instead of inventing a
			// third disposition here. Returning nil would leave the actuation
			// recorded nowhere and the operation reported as abandoned, which
			// is a routine wrong answer in place of a rare wrong retry.
			h.log.ErrorContext(ctx, "verb succeeded but recording its success failed",
				slog.String("operation_id", rec.ID),
				slog.String("verb", string(rec.Verb)),
				slog.Any("error", err))
			return err
		}
		h.auditSuccess(ctx, rec)
		return nil
	}

	// This runs ahead of both retry arms. A device that moved past the verb
	// cannot return to the state it needs, so there is nothing an attempt
	// could still find.
	if handled, err := settleIfSuperseded(ctx, h.ops, h.log, rec, actuateErr); handled {
		return err
	}

	// A transient failure leaves the operation open. Retries are invisible
	// progress, and the derived metadata reports them from the task row.
	if !t.Exhausted() {
		return actuateErr
	}

	// Last attempt: record the terminal failure with the same problem document
	// the synchronous path would have returned, then return the error so the
	// queue dead-letters as usual.
	if err := h.ops.Terminate(ctx, rec.ID, web.Problem(actuateErr), nil); err != nil {
		h.log.ErrorContext(ctx, "recording terminal operation failure failed",
			slog.String("operation_id", rec.ID), slog.Any("error", err))
		return fmt.Errorf("recording operation %s failure: %w", rec.ID, err)
	}
	return actuateErr
}

// settleIfSuperseded records a verb that lost the race to a later lifecycle
// change as its operation's outcome. It reports whether it handled the failure,
// and the error the task should return when it did.
//
// Both of the other arms are wrong for that failure. Retrying waits for a state
// that cannot come back, since the device is on its way down or already gone,
// and the dead-letter that follows exhaustion reports the system broken while
// the handler is holding a definitive answer. The user asked to restart a device
// they then stopped; the operation says exactly that, in the problem document
// the synchronous path would have returned, and the task is done on its first
// attempt.
//
// A failure it does not recognize is left to the caller's retry and
// dead-letter arms, which is where an unreachable host or a database error
// belongs. There the dead-letter is accurate.
func settleIfSuperseded(ctx context.Context, ops OperationStore, log *slog.Logger,
	rec *operation.Record, actuateErr error,
) (bool, error) {
	if !supersededByLifecycle(actuateErr) {
		return false, nil
	}
	if err := ops.Terminate(ctx, rec.ID, web.Problem(actuateErr), nil); err != nil {
		log.ErrorContext(ctx, "recording a superseded operation failed",
			slog.String("operation_id", rec.ID), slog.Any("error", err))
		return true, fmt.Errorf("recording operation %s failure: %w", rec.ID, err)
	}
	log.InfoContext(ctx, "device moved past the verb before it ran; settling the operation",
		slog.String("operation_id", rec.ID),
		slog.String("verb", string(rec.Verb)),
		slog.String("device_id", rec.ResourceID),
		slog.Any("cause", actuateErr))
	return true, nil
}

// supersededByLifecycle reports whether an actuation failed because the device
// moved past the verb rather than because the verb itself failed. The row is
// gone, or its status is one only a later lifecycle change produces.
//
// Status error is not one of them. A device marks error transiently and the
// device controller settles it back to running, so a retry through it makes
// progress.
func supersededByLifecycle(err error) bool {
	if errors.Is(err, device.ErrNotFound) {
		return true
	}
	conflict, ok := errors.AsType[*device.StateConflictError](err)
	if !ok {
		return false
	}
	switch conflict.Current {
	case device.StatusStopping, device.StatusStopped, device.StatusDeleting, device.StatusDeleted:
		return true
	default:
		return false
	}
}

// actuate re-derives the target and runs the verb.
//
// Resolution happens HERE and not at admission. Minutes can pass between
// enqueue and claim, and a host can move or be replaced in between. The guards
// are re-checked for the same reason. The pre-flight ones exist to produce good
// 4xx messages, never to establish invariants.
//
// The INSTANCE comes off the record and is never re-derived. It is the one
// part of the target the caller chose, so re-deriving it would silently retarget
// a per-instance verb at the group's first member.
func (h *VerbOperationHandler) actuate(ctx context.Context, rec *operation.Record) error {
	target, err := h.svc.resolveRunningInstance(ctx, rec.ResourceID, rec.InstanceName, h.def.Label)
	if err != nil {
		return err
	}
	if err := h.def.Call(h.svc.hoClientFactory.ClientForHost(target.hoURL), ctx, target.group, target.name); err != nil {
		return fmt.Errorf("failed to %s CVD: %w", h.def.Label, err)
	}
	return nil
}

// successResponse builds the verb's response payload. Every verb here returns a
// single timestamp. A response type cannot be widened after release, so an empty
// one would be unrecoverable.
func (h *VerbOperationHandler) successResponse() ([]byte, error) {
	policy, err := operation.PolicyFor(h.def.Verb)
	if err != nil {
		return nil, fmt.Errorf("building %s response: %w", h.def.Verb, err)
	}
	body := make(map[string]any, len(policy.ResponseFields))
	now := time.Now().UTC()
	for _, field := range policy.ResponseFields {
		body[field] = now
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding %s response: %w", h.def.Verb, err)
	}
	return raw, nil
}

// auditSuccess records who asked. A worker context carries no principal, so the
// actor comes from the operation row. Without it the audit trail silently loses
// its actor for every async verb, which is why created_by is stored.
func (h *VerbOperationHandler) auditSuccess(_ context.Context, rec *operation.Record) {
	if h.svc.auditLogger == nil {
		return
	}
	h.svc.auditLogger.LogAsync(audit.New(h.def.Action, audit.ResourceDevice).
		WithActor(rec.CreatedBy).
		WithResource(rec.ResourceID).
		WithMetadata(map[string]any{"operation": operation.ResourceName(rec.ID)}).
		Build())
}

// runningInstanceTarget is a resolved verb target. Host identity rides along
// because the snapshot verbs must pin which disk incarnation they touched.
// Resolution and the check belong to the same read or the answer can be
// stale.
type runningInstanceTarget struct {
	hoURL       string
	group       string
	name        string
	hostID      string
	incarnation int64
}

// resolveRunningInstance loads a running device and resolves its host and the
// named instance. It holds the guards runningInstanceOp applies inline, factored
// out so the request path and the worker apply exactly the same ones.
//
// instance names the Android VM the verb actuates. An empty name means the
// device's own CVD ID, which is the group's first instance. The snapshot verbs
// address that one, because a snapshot row records the group and name it was
// taken from and CCO has no per-instance snapshot inventory.
func (s *DeviceService) resolveRunningInstance(ctx context.Context, id, instance, label string) (runningInstanceTarget, error) {
	d, err := liveDevice(ctx, s.deviceRepo, id)
	if err != nil {
		return runningInstanceTarget{}, fmt.Errorf("device not found: %w", err)
	}
	if d.Status != device.StatusRunning {
		return runningInstanceTarget{}, &device.StateConflictError{
			Action: label, Required: device.StatusRunning, Current: d.Status,
		}
	}

	h, err := s.hostRepo.Get(ctx, d.HostID)
	if err != nil {
		return runningInstanceTarget{}, fmt.Errorf("failed to get host: %w", err)
	}
	hoURL := h.HostOrchestratorURL()
	if hoURL == "" {
		return runningInstanceTarget{}, fmt.Errorf("host %s has no IP address", h.ID)
	}
	group, name := device.ParseCVDID(d.CVDID)
	if group == "" || name == "" {
		return runningInstanceTarget{}, fmt.Errorf("device %s has malformed CVD ID %q", d.ID, d.CVDID)
	}
	if instance != "" {
		inst, err := findInstance(d, instance)
		if err != nil {
			return runningInstanceTarget{}, err
		}
		name = inst.Name
	}
	return runningInstanceTarget{hoURL: hoURL, group: group, name: name, hostID: h.ID, incarnation: h.Incarnation}, nil
}
