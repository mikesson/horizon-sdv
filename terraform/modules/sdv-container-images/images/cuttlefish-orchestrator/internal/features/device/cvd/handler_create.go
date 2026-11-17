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
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

const (
	// hostWaitBudget bounds how long a CVD create waits for a host with no
	// provision task in flight (a host mid-`starting`, or one whose provision
	// vanished) before the device is failed. While a provision IS in flight
	// the wait is governed by hostProvisionWaitCap instead: the two loops'
	// budgets must not contradict each other.
	hostWaitBudget = 15 * time.Minute

	// hostProvisionWaitCap is the absolute limit on the wait while the
	// host's provision task is still live. Provisioning legitimately retries
	// up to 10 attempts with backoff capped at 5m, an envelope of ~25-30m, so
	// a budget shorter than that fails devices whose host was still making
	// progress and then came up. The value follows that envelope and is not a
	// tuning value: change the retry policy and this together.
	hostProvisionWaitCap = 45 * time.Minute

	// hostNotReadyRetry is the postpone delay while the host is provisioning.
	hostNotReadyRetry = 20 * time.Second

	// hostStartingRetry is the postpone delay while the host's VM is up but
	// its Host Orchestrator has not answered yet (`starting`). Shorter than
	// the provisioning delay: the HO boot window is tens of seconds, every
	// redelivery re-probes HO directly (breaker-safe, so the cadence cannot
	// trip the shared breaker), and each cycle of slack here is dead time on
	// the create path.
	hostStartingRetry = 5 * time.Second

	// launchInFlightRetry is the postpone delay while a prior attempt's CVD
	// launch is still running on the host; launches settle within minutes.
	launchInFlightRetry = 15 * time.Second

	// launchWaitCap bounds the launch-in-flight wait by TOTAL task age. Task
	// age can legitimately include the full host wait (hostProvisionWaitCap)
	// before the first launch even starts, plus one full HO operation window
	// (MaxWait, 10m) with slack. A group still transient past this is a
	// hung launcher, so the device is failed instead of postponing
	// forever.
	launchWaitCap = hostProvisionWaitCap + 15*time.Minute
)

// ActiveTaskChecker reports whether a live (pending or leased) task of the
// given type exists for a resource. The Spanner TaskQueue satisfies it.
type ActiveTaskChecker interface {
	HasActive(ctx context.Context, taskType, resourceID string) (bool, error)
}

// CreateHandler runs queued CVD-creation tasks, adapting the actuator to the
// task.Handler contract. The worker pool dispatches task.TypeDeviceCreateCVD
// tasks here; it is wired in main.
type CreateHandler struct {
	actuator *Actuator
	tasks    ActiveTaskChecker
}

// NewCreateHandler creates the handler for CVD-creation tasks. tasks lets
// the not-ready wait follow the host's provision task instead of a bare wall
// clock; nil degrades to the wall-clock cap alone.
func NewCreateHandler(actuator *Actuator, tasks ActiveTaskChecker) *CreateHandler {
	return &CreateHandler{actuator: actuator, tasks: tasks}
}

// TaskType is the task type this handler processes.
func (h *CreateHandler) TaskType() string { return task.TypeDeviceCreateCVD }

// Handle runs the device's CVD creation. A still-provisioning host postpones
// the task (no attempt burned) for as long as the host still has a live
// provision attempt; a group whose prior launch is still in flight postpones
// likewise until it settles (racing it poisons the host); a host
// that can never serve the create fails the device immediately (the host's
// own dead-letter carries the cause, so this task completes instead of
// double-dead-lettering). Any other non-nil error is retried by the worker
// (with backoff); on the final, exhausted attempt the device is marked failed
// so it does not linger in "creating".
func (h *CreateHandler) Handle(ctx context.Context, t *task.Task) error {
	err := h.actuator.RunCreateCVD(ctx, t.ResourceID, t)

	if notReady, ok := errors.AsType[*hostNotReadyError](err); ok {
		if failErr := h.hostWaitExpired(ctx, t, notReady); failErr != nil {
			h.actuator.markCreateFailed(ctx, t.ResourceID, failErr)
			return nil
		}
		return task.Reschedule(notReady.retryDelay(), notReady.Error())
	}
	if gone, ok := errors.AsType[*hostGoneError](err); ok {
		h.actuator.markCreateFailed(ctx, t.ResourceID, gone)
		return nil
	}
	if inFlight, ok := errors.AsType[*cvdLaunchInFlightError](err); ok {
		// A prior attempt's launch is still running server-side: wait for it
		// to settle instead of racing it (actuating on a mid-launch group
		// collides on tap devices and poisons the host). The
		// postpone burns no attempts; total task age bounds the wait.
		if time.Since(t.CreatedAt) > launchWaitCap {
			h.actuator.markCreateFailed(ctx, t.ResourceID,
				fmt.Errorf("CVD group launch did not settle within %v: %w", launchWaitCap, inFlight))
			return nil
		}
		return task.Reschedule(launchInFlightRetry, inFlight.Error())
	}
	if handled, out := awaitHostServing(t, err, h.actuator.logger); handled {
		// Only the torn-down-device sweep surfaces this: the device is
		// already deleted, and its group may still be on the host's parked
		// VM. Wait like the cleanup task does. The device is never failed,
		// because it has no state left to fail.
		return out
	}

	if err != nil && t.Exhausted() {
		h.actuator.markCreateFailed(ctx, t.ResourceID, err)
	}
	return err
}

// hostWaitExpired decides whether a not-ready host is still worth waiting
// for. It returns nil to keep waiting, or the error to fail the device with.
// Past the base budget the wait continues only while the host's provision
// task is still live. That task's retry envelope (~25-30m) exceeds any
// reasonable fixed budget here, and a host that succeeds on a late attempt
// must not find its device already failed. Without a live provision, nothing
// will make progress, so the base budget applies.
func (h *CreateHandler) hostWaitExpired(ctx context.Context, t *task.Task, notReady *hostNotReadyError) error {
	elapsed := time.Since(t.CreatedAt)
	switch {
	case elapsed > hostProvisionWaitCap:
		return fmt.Errorf("host not ready within %v: %w", hostProvisionWaitCap, notReady)
	case elapsed > hostWaitBudget:
		if h.tasks == nil {
			return fmt.Errorf("host not ready within %v: %w", hostWaitBudget, notReady)
		}
		active, err := h.tasks.HasActive(ctx, task.TypeHostProvision, notReady.hostID)
		if err == nil && !active {
			return fmt.Errorf("host not ready within %v and no provision in flight: %w",
				hostWaitBudget, notReady)
		}
		// An indeterminate check keeps waiting; the cap above still bounds it.
	}
	return nil
}
