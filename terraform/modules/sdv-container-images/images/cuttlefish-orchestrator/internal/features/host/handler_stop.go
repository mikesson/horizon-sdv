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

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// StopSettleHostHandler runs host.stop_settle tasks, which HostService.Stop
// enqueues in the commit that marks the host stopping. It is wired in main.
type StopSettleHostHandler struct {
	svc *HostService
}

// NewStopSettleHostHandler creates the handler for host stop-settle tasks.
func NewStopSettleHostHandler(svc *HostService) *StopSettleHostHandler {
	return &StopSettleHostHandler{svc: svc}
}

// TaskType is the task type this handler processes.
func (h *StopSettleHostHandler) TaskType() string { return TaskTypeStopSettleHost }

// Handle settles one host stop. A non-nil error is retried by the worker with
// backoff. On the final, exhausted attempt the stop is treated as failed and
// the host's desired state returns to running, so the host never waits in
// stopping for a settle that has stopped trying.
func (h *StopSettleHostHandler) Handle(ctx context.Context, t *task.Task) error {
	err := h.svc.RunStopSettle(ctx, t)
	if _, waiting := errors.AsType[*task.RescheduleError](err); waiting {
		return err
	}
	if err != nil && t.Exhausted() {
		if restoreErr := h.svc.restoreAfterFailedStop(ctx, t.ResourceID, err); restoreErr != nil {
			err = errors.Join(err, restoreErr)
		}
	}
	return withGCERetryHint(err)
}
