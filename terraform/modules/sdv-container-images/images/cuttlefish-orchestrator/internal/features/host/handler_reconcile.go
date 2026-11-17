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
	"errors"
	"fmt"
	"log/slog"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// ReconcileTaskQueue is the queue surface the host reconcile loop needs. It
// heals a stranded transitional host by idempotently ensuring a transition
// task exists (INSERT OR IGNORE on the transition_key unique index, so no
// TOCTOU and no duplicate).
type ReconcileTaskQueue interface {
	EnsureTransition(ctx context.Context, taskType, resourceID string, priority int64) (bool, error)
}

// ReconcileHostHandler runs host.reconcile tasks, the per-resource convergence
// entry point. Detectors (the host reconciler) enqueue suspicion, deduped while
// pending and serialized per key while leased; this handler verifies against
// current state and heals:
//
//   - preempted → durable re-provision via Retry (previously an inline
//     reconciler action; now it inherits the keyed queue's dedup + serialization)
//   - transitional (pending/provisioning/deleting) with NO live transition
//     task → re-enqueue the transition (a stranded row's only owner today is
//     the reaper's TTL)
//   - everything else → nothing to converge; complete
//
// The transition legs themselves (provision/deprovision) stay the owners of
// actuation. They are not absorbed here until the desired_state model reshapes
// the convergence function.
type ReconcileHostHandler struct {
	svc    *HostService
	tasks  ReconcileTaskQueue
	logger *slog.Logger
}

// NewReconcileHostHandler creates the handler for host.reconcile tasks.
func NewReconcileHostHandler(svc *HostService, tasks ReconcileTaskQueue, logger *slog.Logger) *ReconcileHostHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReconcileHostHandler{svc: svc, tasks: tasks, logger: logger}
}

// TaskType is the task type this handler processes.
func (h *ReconcileHostHandler) TaskType() string { return task.TypeHostReconcile }

// Handle converges one host. Idempotent: a host that no longer needs
// convergence (gone, superseded, already owned by a live task) completes as
// a no-op; a non-nil error is retried by the worker.
func (h *ReconcileHostHandler) Handle(ctx context.Context, t *task.Task) error {
	hst, err := h.svc.Get(ctx, t.ResourceID)
	if err != nil {
		if errors.Is(err, host.ErrNotFound) {
			return nil // host gone; nothing to converge
		}
		return fmt.Errorf("loading host %s: %w", t.ResourceID, err)
	}

	switch hst.Status {
	case host.StatusPreempted:
		// Durable re-provision. Retry validates CanRetry in-txn: a
		// concurrent transition (user delete, another recovery) supersedes
		// this one and completes as a no-op.
		if _, err := h.svc.Retry(ctx, hst.ID, false); err != nil {
			if _, ok := errors.AsType[*host.NotRetryableError](err); ok {
				h.logger.Info("preemption recovery superseded",
					slog.String("host_id", hst.ID), slog.Any("error", err))
				return nil
			}
			return fmt.Errorf("recovering preempted host %s: %w", hst.ID, err)
		}
		return nil
	case host.StatusPending, host.StatusProvisioning:
		return h.ensureTransitionTask(ctx, hst.ID, task.TypeHostProvision)
	case host.StatusDeleting:
		return h.ensureTransitionTask(ctx, hst.ID, task.TypeHostDeprovision)
	default:
		return nil // steady or terminal: the detectors own observation
	}
}

// ensureTransitionTask heals a transitional host that has no live owner by
// enqueueing its transition. EnsureTransition is idempotent, because the
// transition_key unique index drops the insert when a sibling already owns the
// transition, so there is no read-then-enqueue race. Stranded rows are rare
// (an admin-deleted task, a pre-outbox legacy row); without this, the reaper's
// TTL is their only owner.
func (h *ReconcileHostHandler) ensureTransitionTask(ctx context.Context, hostID, taskType string) error {
	inserted, err := h.tasks.EnsureTransition(ctx, taskType, hostID, task.PriorityEdge)
	if err != nil {
		return fmt.Errorf("ensuring %s task for host %s: %w", taskType, hostID, err)
	}
	if inserted {
		h.logger.Warn("transitional host had no live transition task; enqueued one",
			slog.String("host_id", hostID), slog.String("task_type", taskType))
	}
	return nil
}
