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

package cvd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// ReconcileTaskQueue is the queue surface the device reconcile loop needs: it
// idempotently ensures a create task exists for a stranded device (INSERT OR
// IGNORE on the transition_key unique index, so no TOCTOU and no duplicate). The
// create re-derives from the row's persisted desired_spec, so no payload is
// needed.
type ReconcileTaskQueue interface {
	EnsureTransition(ctx context.Context, taskType, resourceID string, priority int64) (bool, error)
}

// TeardownCompleter finishes a deleting device's row journey once its CVD is
// gone: tombstone, host-counter decrement and pin release in the same
// idempotent commit the delete path uses. The device repository satisfies it.
// The reconcile handler needs it because Delete's best-effort 'deleting'
// status write and its tombstone transaction are separate commits. A crash
// between them leaves a device in deleting with no cleanup task and the
// counter still holding it, and nothing else ever finishes that journey.
type TeardownCompleter interface {
	MarkDeletedWithHostDecrement(ctx context.Context, hostID, deviceID string, extra ...*spanner.Mutation) (bool, error)
}

// ReconcileHandler runs device.reconcile tasks, the device-side keyed
// convergence entry point. Teardown convergence runs the idempotent
// probe-first cleanup directly; a stranded CREATE is healed by re-enqueueing
// a create task that re-derives from the row's persisted desired_spec.
// A device from before desired_spec existed (pre-v41) has no
// persisted spec and is surfaced rather than rebuilt from defaults.
// Health-driven convergence (zombie recreate) lands later with the conditions
// model.
type ReconcileHandler struct {
	actuator  *Actuator
	tasks     ReconcileTaskQueue
	completer TeardownCompleter
	logger    *slog.Logger
}

// NewReconcileHandler creates the handler for device.reconcile tasks.
func NewReconcileHandler(actuator *Actuator, tasks ReconcileTaskQueue, completer TeardownCompleter, logger *slog.Logger) *ReconcileHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReconcileHandler{actuator: actuator, tasks: tasks, completer: completer, logger: logger}
}

// TaskType is the task type this handler processes.
func (h *ReconcileHandler) TaskType() string { return task.TypeDeviceReconcile }

// Handle converges one device. Idempotent; a non-nil error is retried.
func (h *ReconcileHandler) Handle(ctx context.Context, t *task.Task) error {
	d, err := h.actuator.devices.Get(ctx, t.ResourceID)
	if err != nil {
		if errors.Is(err, device.ErrNotFound) {
			return nil // device gone; nothing to converge
		}
		return fmt.Errorf("loading device %s: %w", t.ResourceID, err)
	}

	switch d.Status {
	case device.StatusDeleting, device.StatusDeleted:
		// Teardown convergence: probe-first, no-ops when already clean. Total
		// task age bounds the launch-in-flight wait, exactly as in the cleanup
		// handler: past the settle cap a still-transient group means a hung
		// launcher, and the best-effort delete beats postponing forever.
		// Postpone burns no attempts, so nothing else would ever end the loop.
		force := time.Since(t.CreatedAt) > launchWaitCap
		err := h.actuator.runCleanupCVD(ctx, d.ID, force)
		if inFlight, ok := errors.AsType[*cvdLaunchInFlightError](err); ok {
			return task.Reschedule(launchInFlightRetry, inFlight.Error())
		}
		if handled, out := awaitHostServing(t, err, h.logger); handled {
			return out
		}
		if err != nil {
			return fmt.Errorf("converging teardown of device %s: %w", d.ID, err)
		}
		// The CVD is gone (or never existed). A device still in 'deleting'
		// owes the rest of the delete commit: Delete's best-effort status
		// write and its tombstone transaction are separate, and a crash
		// between them left exactly this shape: deleting, no cleanup task,
		// the host counter still holding the device. Finish the journey with
		// the same idempotent commit the delete path uses (the robustness
		// chaos harness caught the stranded row).
		if d.Status == device.StatusDeleting && h.completer != nil {
			if _, err := h.completer.MarkDeletedWithHostDecrement(ctx, d.HostID, d.ID); err != nil {
				return fmt.Errorf("completing teardown of device %s: %w", d.ID, err)
			}
			h.logger.Warn("finished an interrupted delete: device tombstoned by reconcile",
				slog.String("device_id", d.ID), slog.String("host_id", d.HostID))
		}
		return nil
	case device.StatusPending, device.StatusCreating:
		return h.ensureCreateTask(ctx, d)
	case device.StatusRunning:
		return h.recoverZombie(ctx, d)
	default:
		return nil
	}
}

// recoverZombie re-drives a running device whose CVD died under it
// (health_status=zombie) through the create path from its persisted
// desired_spec, or lets the service fail it once the recreate budget is spent.
// A non-zombie running device is a no-op. The producer (the device controller
// zombie suspector) only files reconcile for running+zombie devices and only
// when zombie recovery is enabled, so this branch is otherwise unreached.
func (h *ReconcileHandler) recoverZombie(ctx context.Context, d *device.Device) error {
	if d.HealthStatus != device.HealthStatusZombie {
		return nil
	}
	// Step 1: a CVD that is present but unresponsive (cvd_status Unreachable) is
	// relaunched in place, which is cheap and needs no artifact re-fetch,
	// before the heavier recreate. The nudge leaves the device running+zombie;
	// the next health cycle confirms recovery or the reconcile escalates.
	restarted, err := h.actuator.RestartZombieCVD(ctx, d.ID)
	if err != nil {
		return fmt.Errorf("restarting zombie device %s: %w", d.ID, err)
	}
	if restarted {
		h.logger.Info("zombie device: relaunched its CVD in place before recreating",
			slog.String("device_id", d.ID))
		return nil
	}
	// Steps 2 and 3: recreate from desired_spec, or fail once the budget is
	// spent.
	recreate, err := h.actuator.RecoverZombie(ctx, d.ID)
	if err != nil {
		return fmt.Errorf("recovering zombie device %s: %w", d.ID, err)
	}
	if !recreate {
		return nil // failed (budget spent / no spec) or recovered on its own
	}
	// Resync priority: a recreate must never preempt a user's create. Idempotent
	// via transition_key; if this crashes before the enqueue, the device is now
	// 'creating' and ensureCreateTask heals it next cycle.
	inserted, err := h.tasks.EnsureTransition(ctx, task.TypeDeviceCreateCVD, d.ID, task.PriorityResync)
	if err != nil {
		return fmt.Errorf("ensuring recreate task for device %s: %w", d.ID, err)
	}
	if inserted {
		h.logger.Warn("zombie device: recreating CVD from desired_spec",
			slog.String("device_id", d.ID))
	}
	return nil
}

// ensureCreateTask re-drives a stranded create from the persisted desired_spec
// when no live create task owns it. A device with no persisted
// spec (pre-v41, before desired_spec) is surfaced rather than rebuilt from
// defaults, which could turn an advanced device into a simple one. Mode is
// always set at create, so its absence is the reliable "no persisted spec"
// signal.
func (h *ReconcileHandler) ensureCreateTask(ctx context.Context, d *device.Device) error {
	if d.Mode == "" {
		h.logger.Warn("device stranded mid-create has no persisted spec; not healable",
			slog.String("device_id", d.ID), slog.String("status", string(d.Status)))
		return nil
	}
	inserted, err := h.tasks.EnsureTransition(ctx, task.TypeDeviceCreateCVD, d.ID, task.PriorityEdge)
	if err != nil {
		return fmt.Errorf("ensuring create task for device %s: %w", d.ID, err)
	}
	if inserted {
		h.logger.Warn("stranded device had no live create task; enqueued one from desired_spec",
			slog.String("device_id", d.ID), slog.String("status", string(d.Status)))
	}
	return nil
}
