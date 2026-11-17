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
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"
	"github.com/google/uuid"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/gce"
)

// Stop-settle wait bounds: a VM still in stop motion after its stop operation
// reported DONE is re-read in steps of stopSettleRetry, for at most
// stopSettleWaitCap of task age. Past the cap the host reconciler's adoption
// owns the settle.
const (
	stopSettleRetry   = 5 * time.Second
	stopSettleWaitCap = 10 * time.Minute
)

// errStopSuperseded skips the failed-stop write when the host already left the
// stop it was asked for.
var errStopSuperseded = errors.New("host stop superseded")

// stopRequestID is the GCE requestId of the stop a host.stop_settle task
// tracks. It is fixed per task, unlike requestIDForDelivery: Stop issues the
// call before the task exists in the queue, and every delivery re-issues it to
// get the same operation back. A replayed failure is the answer the task
// wants, because a failed stop ends the journey.
func stopRequestID(taskID string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("cco:gce:stop:"+taskID)).String()
}

// RunStopSettle waits on a user stop's GCE operation and commits its outcome.
// A completed stop settles the host to stopped and its running devices with
// it in one commit, through the same CAS write the host reconciler's adoption
// uses. A failed stop returns the host's desired state to running. It is
// idempotent: a host no longer stopping toward stopped is someone else's to
// settle, and the call returns nil.
func (s *HostService) RunStopSettle(ctx context.Context, t *task.Task) error {
	h, err := s.hostRepo.Get(ctx, t.ResourceID)
	if err != nil {
		if errors.Is(err, host.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("loading host %s: %w", t.ResourceID, err)
	}
	if h.Status != host.StatusStopping || h.DesiredState != host.DesiredStopped || h.GCEInstanceName == "" {
		return nil
	}

	requestID := stopRequestID(t.ID)
	op, err := s.instancesClient.Stop(ctx, &computepb.StopInstanceRequest{
		Project:   s.config.ProjectID,
		Zone:      h.Zone,
		Instance:  h.GCEInstanceName,
		RequestId: &requestID,
	})
	switch {
	case isInstanceNotFound(err):
		// The VM is gone; the reconciler's lost-host path owns the host.
		return nil
	case err != nil:
		return fmt.Errorf("re-issuing stop of GCE instance %s: %w", h.GCEInstanceName, err)
	}
	done, err := gce.AwaitZoneOperation(ctx, s.operationsClient, s.config.ProjectID, h.Zone, op.Name())
	if err != nil {
		return fmt.Errorf("waiting for stop of GCE instance %s: %w", h.GCEInstanceName, err)
	}
	if failure := gce.OperationFailure(done); failure != nil {
		return s.restoreAfterFailedStop(ctx, h.ID, failure)
	}

	inst, err := s.instancesClient.Get(ctx, &computepb.GetInstanceRequest{
		Project:  s.config.ProjectID,
		Zone:     h.Zone,
		Instance: h.GCEInstanceName,
	})
	if err != nil {
		if isInstanceNotFound(err) {
			return nil
		}
		return fmt.Errorf("reading GCE instance %s after its stop: %w", h.GCEInstanceName, err)
	}
	return s.settleStopped(ctx, h, inst, t)
}

// settleStopped writes the stopped adoption for a host whose stop completed,
// when the VM reads parked.
func (s *HostService) settleStopped(ctx context.Context, h *host.Host, inst *computepb.Instance, t *task.Task) error {
	vmStatus := inst.GetStatus()
	if host.GCEVMStateStopMotion(vmStatus) {
		if time.Since(t.CreatedAt) > stopSettleWaitCap {
			return nil
		}
		return task.Reschedule(stopSettleRetry, fmt.Sprintf(
			"instance %s is %s after its stop completed", h.GCEInstanceName, vmStatus))
	}
	observed := *h
	gce.UpdateHostFromGCEInstance(&observed, inst)
	observed.VMStatus = vmStatus
	if derived, ok := host.DerivePhase(&observed, vmStatus); !ok || derived != host.StatusStopped {
		// Started again or reclassified (a preemption); the reconciler adopts
		// whatever the VM now is.
		return nil
	}
	applied, err := s.hostRepo.BatchUpdateGCEState(ctx, []host.GCEStateUpdate{stoppedAdoption(h.Status, &observed)})
	if err != nil {
		return fmt.Errorf("settling stopped host %s: %w", h.ID, err)
	}
	if applied == 0 {
		s.logger.Info("stop settle superseded by a concurrent transition", slog.String("host_id", h.ID))
	}
	return nil
}

// stoppedAdoption is the CAS write that records observed as stopped, planned
// from prev. BatchUpdateGCEState settles the host's devices with it.
func stoppedAdoption(prev host.Status, observed *host.Host) host.GCEStateUpdate {
	return host.GCEStateUpdate{
		ID:             observed.ID,
		VMStatus:       observed.VMStatus,
		PrevStatus:     prev,
		Status:         host.StatusStopped,
		IP:             observed.IPAddress,
		InternalIP:     observed.InternalIP,
		VMTerminatesAt: observed.VMTerminatesAt,
	}
}

// restoreAfterFailedStop returns a host whose stop failed to desired running
// and re-derives its phase, so it leaves stopping instead of waiting there for
// a stop that will never come. A failed write is returned so the task retries
// it; a host that already left the stop is not an error. Detached like the
// other terminal writes: the exhausted attempt's context may already be dead.
func (s *HostService) restoreAfterFailedStop(ctx context.Context, hostID string, cause error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalWriteTimeout)
	defer cancel()
	_, err := s.hostRepo.Mutate(ctx, hostID, func(h *host.Host) error {
		if h.Status != host.StatusStopping || h.DesiredState != host.DesiredStopped {
			return errStopSuperseded
		}
		h.DesiredState = host.DesiredRunning
		h.RecomputeStatus()
		return nil
	})
	switch {
	case err == nil:
		s.logger.Warn("host stop failed; desired state returned to running",
			slog.String("host_id", hostID), slog.Any("error", cause))
		return nil
	case errors.Is(err, errStopSuperseded), errors.Is(err, host.ErrNotFound):
		return nil
	default:
		return fmt.Errorf("recording failed stop of host %s (%w): %w", hostID, cause, err)
	}
}
