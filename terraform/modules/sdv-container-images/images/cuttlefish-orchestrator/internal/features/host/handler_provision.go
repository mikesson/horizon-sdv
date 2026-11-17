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
	"time"

	"github.com/google/uuid"

	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/gce"
)

// requestIDForDelivery derives the GCE idempotency requestId for one task
// delivery. GCE dedupes on it (it must be a literal UUID, per AIP-155), so a
// redelivery of the same attempt is absorbed server-side while a real retry
// (new attempt) gets a fresh ID. Deriving it per resource or per generation
// instead would replay the first attempt's cached, possibly failed, operation
// forever. Attempts alone does not name a delivery. Postpone restores the
// pre-claim value, so postpone-separated deliveries repeat it and would replay
// a stale response for GCE's whole dedup window (≥60 min); the lease expiry is
// unique per claim and completes the identity.
func requestIDForDelivery(t *task.Task) string {
	return uuid.NewSHA1(uuid.NameSpaceURL,
		fmt.Appendf(nil, "cco:gce:%s:%d:%d", t.ID, t.Attempts, t.LeaseExpiry.UnixNano())).String()
}

// withGCERetryHint attaches a server-guided minimum retry delay when the
// failure is a GCE rate limit, so 429s do not come back on the generic
// backoff curve.
func withGCERetryHint(err error) error {
	if delay, ok := gce.SuggestedRetryDelay(err); ok {
		return &task.DelayHintError{After: delay, Err: err}
	}
	return err
}

// ProvisionHostHandler runs queued host-provisioning tasks, adapting the host
// service to the task.Handler contract. The worker pool dispatches
// TaskTypeProvisionHost tasks here; it is wired in main.
type ProvisionHostHandler struct {
	svc *HostService
}

// NewProvisionHostHandler creates the handler for host-provisioning tasks.
func NewProvisionHostHandler(svc *HostService) *ProvisionHostHandler {
	return &ProvisionHostHandler{svc: svc}
}

// TaskType is the task type this handler processes.
func (h *ProvisionHostHandler) TaskType() string { return TaskTypeProvisionHost }

// adoptWaitCap bounds provision postpones by TOTAL task age. Waiting states
// (a dying Spot husk under the host's name, a vanished pre-existing instance)
// settle within minutes. A task still postponing past this is stuck, by a
// parked instance nothing will remove or by a GCE requestId replaying a cached
// response for its dedup window, and must fail instead of postponing forever
// (provision has no other wall clock; the host TTL is hours away).
const adoptWaitCap = 30 * time.Minute

// Handle provisions the host's GCE instance. A reschedule (waiting out a
// transient pre-existing instance, e.g. a dying Spot husk) postpones without
// burning an attempt and must never mark the host failed, until the wait
// exceeds adoptWaitCap, at which point the host fails with the wait's reason.
// A machine family with no bootable image fails the host at once. Any other
// non-nil error is retried by the worker (with backoff); a
// classified capacity failure additionally teaches placement (zone_health)
// and may move the host to another ranked zone before the next delivery.
// Failover is a side effect on this existing ladder and adds no control flow.
// The attempt still burns, and the redelivery inserts into the new zone with
// its fresh per-delivery request ID. On the final, exhausted attempt the host
// is marked failed with classified error details so it does not linger in
// "provisioning".
func (h *ProvisionHostHandler) Handle(ctx context.Context, t *task.Task) error {
	err := h.svc.RunProvision(ctx, t.ResourceID, requestIDForDelivery(t))
	if resched, waiting := errors.AsType[*task.RescheduleError](err); waiting {
		if time.Since(t.CreatedAt) > adoptWaitCap {
			h.svc.markProvisionFailed(ctx, t.ResourceID,
				fmt.Errorf("provisioning blocked for over %v: %s", adoptWaitCap, resched.Reason))
			return nil
		}
		return err
	}
	// No image is a configuration fact that no retry or other zone changes, so
	// the host fails now instead of after the whole retry ladder.
	if _, noImage := errors.AsType[*hostconfig.ImageUnavailableError](err); noImage {
		h.svc.markProvisionFailed(ctx, t.ResourceID, err)
		return nil
	}
	if err != nil {
		// An exhausted task gets its cooldown recorded but no move, because
		// there is no delivery left to act on it.
		h.svc.FailoverZone(ctx, t.ResourceID, err, !t.Exhausted())
		if t.Exhausted() {
			h.svc.markProvisionFailed(ctx, t.ResourceID, err)
		}
	}
	return withGCERetryHint(err)
}

// ActiveTaskChecker reports whether a live (pending or leased) task of the
// given type exists for a resource. The Spanner TaskQueue satisfies it.
type ActiveTaskChecker interface {
	HasActive(ctx context.Context, taskType, resourceID string) (bool, error)
}

// Deprovision drain-wait bounds: a teardown pauses while the host's provision
// task is still live, in postpone steps of provisionDrainRetry, for at most
// provisionDrainCap of task age.
const (
	provisionDrainRetry = 15 * time.Second
	provisionDrainCap   = 10 * time.Minute
)

// DeprovisionHostHandler runs queued host-teardown tasks. On exhaustion the task
// dead-letters (the host stays in "deleting" for operator review via the DLQ).
type DeprovisionHostHandler struct {
	svc   *HostService
	tasks ActiveTaskChecker
}

// NewDeprovisionHostHandler creates the handler for host-deprovisioning
// tasks. tasks lets teardown wait out a live provision instead of racing it;
// nil skips the wait.
func NewDeprovisionHostHandler(svc *HostService, tasks ActiveTaskChecker) *DeprovisionHostHandler {
	return &DeprovisionHostHandler{svc: svc, tasks: tasks}
}

// TaskType is the task type this handler processes.
func (h *DeprovisionHostHandler) TaskType() string { return TaskTypeDeprovisionHost }

// Handle tears down the host's GCE instance and removes its record. A non-nil
// error is retried by the worker; on the final, exhausted attempt the host is
// marked Stalled (staying "deleting") so host.reconcile keeps re-driving the
// idempotent delete. A failed delete is transient in GCE, never terminal.
func (h *DeprovisionHostHandler) Handle(ctx context.Context, t *task.Task) error {
	// A teardown racing a LIVE provision can observe "no instance yet"
	// (notFound), remove the row, and leave the in-flight insert's VM
	// unclaimed forever. Wait the provision out; its next attempt sees
	// status=deleting and self-completes. The wait is bounded by task age so a
	// stuck provision cannot hold teardown hostage.
	if h.tasks != nil && time.Since(t.CreatedAt) < provisionDrainCap {
		if active, err := h.tasks.HasActive(ctx, task.TypeHostProvision, t.ResourceID); err == nil && active {
			return task.Reschedule(provisionDrainRetry, "waiting for the host's live provision task to settle")
		}
	}
	err := h.svc.RunDeprovision(ctx, t.ResourceID, requestIDForDelivery(t))
	if err != nil && t.Exhausted() {
		h.svc.markDeprovisionStalled(ctx, t.ResourceID, err)
	}
	return withGCERetryHint(err)
}
