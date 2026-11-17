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

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// RemediateHostHandler runs queued host-poison remediation tasks, adapting
// the host service to the task.Handler contract. The device controller's
// degraded-host sweep enqueues it for a degraded host with zero live devices.
// It is wired in main.
type RemediateHostHandler struct {
	svc *HostService
}

// NewRemediateHostHandler creates the handler for host-remediation tasks.
func NewRemediateHostHandler(svc *HostService) *RemediateHostHandler {
	return &RemediateHostHandler{svc: svc}
}

// TaskType is the task type this handler processes.
func (h *RemediateHostHandler) TaskType() string { return TaskTypeRemediateHost }

// Handle attempts to reset the poisoned host. A non-nil error is retried by the
// worker with backoff; on exhaustion the task dead-letters while the host stays
// fenced from placement for operator review. There is no terminal status write:
// remediation only ever moves a host from degraded back to healthy, never to a
// worse state.
func (h *RemediateHostHandler) Handle(ctx context.Context, t *task.Task) error {
	return h.svc.RunRemediateHost(ctx, t.ResourceID)
}
