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
	"log/slog"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

const (
	// hostServingRetry is the postpone delay while the deleted device's host
	// is not serving but its VM still exists (stopped, unreachable,
	// mid-restart): the group may still be on the VM, so cleanup re-checks
	// once the host settles into serving or a terminal path.
	hostServingRetry = 30 * time.Second

	// hostServingWaitCap bounds that wait by TOTAL task age. A host can sit
	// stopped or unreachable indefinitely; past the cap the cleanup completes
	// and leaves reclamation to the orphan-CVD census and host teardown
	// rather than postponing forever.
	hostServingWaitCap = time.Hour
)

// awaitHostServing is the shared translation of hostNotServingError for the
// three teardown-driving handlers (cleanup, the create task's torn-down-device
// sweep, reconcile): postpone while the task is young, give up and complete
// once hostServingWaitCap passes. The orphan-CVD census and host teardown own
// reclamation from there. The first result is false when err is something else.
func awaitHostServing(t *task.Task, err error, logger *slog.Logger) (bool, error) {
	notServing, ok := errors.AsType[*hostNotServingError](err)
	if !ok {
		return false, nil
	}
	if time.Since(t.CreatedAt) > hostServingWaitCap {
		logger.Warn("CVD cleanup gave up waiting for the host to serve again; census or host teardown reclaims the group",
			slog.String("device_id", t.ResourceID),
			slog.String("cause", notServing.Error()))
		return true, nil
	}
	return true, task.Reschedule(hostServingRetry, notServing.Error())
}

// CleanupHandler runs queued CVD-teardown tasks after a device is deleted,
// adapting the actuator to the task.Handler contract. The worker pool
// dispatches task.TypeDeviceCleanupCVD tasks here; it is wired in main.
type CleanupHandler struct {
	actuator *Actuator
}

// NewCleanupHandler creates the handler for CVD-cleanup tasks.
func NewCleanupHandler(actuator *Actuator) *CleanupHandler {
	return &CleanupHandler{actuator: actuator}
}

// TaskType is the task type this handler processes.
func (h *CleanupHandler) TaskType() string { return task.TypeDeviceCleanupCVD }

// Handle deletes the device's CVD on its host. A group whose launch is still
// in flight (device deleted mid-create) postpones until it settles, because
// deleting it would leak the launcher's processes. Total task age bounds
// the wait, past which the delete proceeds best-effort (a launcher
// still transient after the cap is hung; VM teardown removes any leftover). A
// host that is not serving but whose VM still exists postpones likewise
// (bounded by hostServingWaitCap): the group may still be on the VM, and
// completing early is how a returning host leaks it. Any other non-nil error
// is retried by the worker; on exhaustion it dead-letters (the CVD is
// otherwise reclaimed when the host is deprovisioned).
func (h *CleanupHandler) Handle(ctx context.Context, t *task.Task) error {
	force := time.Since(t.CreatedAt) > launchWaitCap
	err := h.actuator.runCleanupCVD(ctx, t.ResourceID, force)
	if inFlight, ok := errors.AsType[*cvdLaunchInFlightError](err); ok {
		return task.Reschedule(launchInFlightRetry, inFlight.Error())
	}
	if handled, out := awaitHostServing(t, err, h.actuator.logger); handled {
		return out
	}
	return err
}
