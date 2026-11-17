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

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// CaptureBugreportHandler runs queued capture-on-error tasks, adapting the
// device service to the task.Handler contract. Filed by the device controller's
// failed-capture detector for error devices when artifacts.capture_on_error is
// on, and wired in main.
type CaptureBugreportHandler struct {
	svc *DeviceService
}

// NewCaptureBugreportHandler creates the handler for capture-on-error tasks.
func NewCaptureBugreportHandler(svc *DeviceService) *CaptureBugreportHandler {
	return &CaptureBugreportHandler{svc: svc}
}

// TaskType is the task type this handler processes.
func (h *CaptureBugreportHandler) TaskType() string { return TaskTypeCaptureBugreport }

// Handle captures a bugreport for the error device. The service is idempotent
// and stamps handled rows so a redelivery no-ops; a non-nil error (a transient
// host/storage failure) is retried by the worker with backoff, then
// dead-letters while the row stays unmarked for the next detector cycle.
func (h *CaptureBugreportHandler) Handle(ctx context.Context, t *task.Task) error {
	return h.svc.CaptureBugreportOnError(ctx, t.ResourceID)
}
