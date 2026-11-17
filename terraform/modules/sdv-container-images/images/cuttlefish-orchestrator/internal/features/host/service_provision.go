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
	"net/http"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/api/googleapi"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/gce"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// tracer names the host feature's spans. Provisioning runs under the task
// worker's always-sampled consumer span, so these child spans are kept too.
var tracer = otel.Tracer(telemetry.ScopeName("host"))

// RunProvision provisions a host's GCE instance from a queued task. It is
// idempotent: it no-ops when the host is gone or already past provisioning, and
// returns an error for the worker to retry (or dead-letter when exhausted). The
// GCE work runs outside any database transaction. gceRequestID (empty to
// disable) makes the instance insert idempotent per delivery; see
// requestIDForDelivery.
func (s *HostService) RunProvision(ctx context.Context, hostID, gceRequestID string) error {
	h, err := s.hostRepo.Get(ctx, hostID)
	if err != nil {
		if errors.Is(err, host.ErrNotFound) {
			return nil // host was deleted; nothing to do
		}
		return fmt.Errorf("loading host %s: %w", hostID, err)
	}
	if h.Status != host.StatusPending && h.Status != host.StatusProvisioning {
		return nil // already running or terminal
	}
	return s.provisionGCEInstance(ctx, h, gceRequestID)
}

// adoptRetryDelay paces redeliveries while a pre-existing instance under the
// host's deterministic name is in a transient state, typically the dying husk
// of a preempted Spot VM (termination action DELETE removes it asynchronously,
// usually within a minute or two).
const adoptRetryDelay = 30 * time.Second

// hoReadyProbeTimeout bounds the best-effort Host Orchestrator probe at the end
// of provisioning. Short: it is only an opportunity to promote 'starting' ->
// 'running' now if HO already answers; the device controller owns the promotion
// when HO is still booting (often tens of seconds after the VM is RUNNING).
const hoReadyProbeTimeout = 3 * time.Second

// isInstanceNotFound reports whether a GCE instance read definitively returned
// not-found.
func isInstanceNotFound(err error) bool {
	apiErr, ok := errors.AsType[*googleapi.Error](err)
	return ok && apiErr.Code == http.StatusNotFound
}

// isInstanceAlreadyExists reports whether a GCE instance insert was rejected
// because the name is taken (HTTP 409). Matched by status code, never by
// error-message substring.
func isInstanceAlreadyExists(err error) bool {
	apiErr, ok := errors.AsType[*googleapi.Error](err)
	return ok && apiErr.Code == http.StatusConflict
}

// resolveBootImage picks the concrete image a host of this shape boots. The
// insert names the image itself, never the family, so the image recorded on
// the host is the one GCE booted even if the family moves mid-provision. A
// refusal is the resolver's *hostconfig.ImageUnavailableError, which the
// provision handler fails the host on at once.
func (s *HostService) resolveBootImage(ctx context.Context, shape machineTypeConfig) (*hostconfig.Image, error) {
	if s.images == nil {
		return nil, errors.New("host service has no image resolver configured")
	}
	img, err := s.images.ResolveImage(ctx, machinetype.FamilyOf(shape.machineType), shape.architecture)
	if err != nil {
		return nil, fmt.Errorf("resolving the boot image: %w", err)
	}
	return img, nil
}

// insertOrAdopt issues the GCE instance insert and waits for it, reporting
// adopting=true when an instance already holds this host's deterministic
// name. That is usually a prior attempt's insert surviving a crash, and can be
// the still-dying husk of a preempted Spot VM (termination action DELETE removes
// the instance asynchronously, and Retry reuses the name). The per-delivery
// requestId makes a redelivered insert return the original operation instead
// of alreadyExists.
func (s *HostService) insertOrAdopt(ctx context.Context, h *host.Host, instance *computepb.Instance, gceRequestID string) (bool, error) {
	insertReq := &computepb.InsertInstanceRequest{
		Project:          s.config.ProjectID,
		Zone:             h.Zone,
		InstanceResource: instance,
	}
	if gceRequestID != "" {
		insertReq.RequestId = &gceRequestID
	}
	op, err := s.instancesClient.Insert(ctx, insertReq)
	switch {
	case isInstanceAlreadyExists(err):
		s.logger.Info("GCE instance already exists; checking adoptability",
			slog.String("host_id", h.ID), slog.String("instance_name", h.GCEInstanceName))
		return true, nil
	case err != nil:
		return false, fmt.Errorf("failed to insert instance: %w", err)
	}
	// Group the operation wait (the ~50s VM boot) into one named, timed node.
	// Otherwise it is a scatter of raw GCE spans and the boot time is not
	// attributable. The server-side wait RPC answers at completion, where the
	// SDK's op.Wait polls on a backoff that notices it up to ~30s late.
	waitCtx, waitSpan := tracer.Start(ctx, "gce.wait_instance_running",
		trace.WithAttributes(attribute.String("gce.instance", h.GCEInstanceName)))
	werr := gce.WaitZoneOperation(waitCtx, s.operationsClient, s.config.ProjectID, h.Zone, op.Name())
	if werr != nil {
		telemetry.RecordError(waitSpan, werr)
	}
	waitSpan.End()
	if werr != nil {
		return false, fmt.Errorf("instance creation failed: %w", werr)
	}
	return false, nil
}

// checkAdoptable decides whether a pre-existing instance under this host's
// name can serve as its VM: nil to adopt, a reschedule to wait out a
// transient state, an error to burn the attempt.
func checkAdoptable(h *host.Host, inst *computepb.Instance) error {
	vmStatus := inst.GetStatus()
	switch {
	case vmStatus == host.GCEVMStateRunning || vmStatus == host.GCEVMStateStaging:
		// Live and serving (or booting): a prior attempt's insert succeeded.
		return nil
	case vmStatus == host.GCEVMStateProvisioning || host.GCEVMStateStopMotion(vmStatus):
		// Transient: a concurrent attempt's instance still coming up, or a
		// dying husk on its way out (a preempted Spot VM, a mid-teardown
		// predecessor). Adopting one would observe a non-serving state that
		// provisioning cannot reach, and return success with the host
		// stranded in provisioning and no task left. Wait for a definitive
		// state instead; adoptWaitCap bounds the total wait.
		return task.Reschedule(adoptRetryDelay, fmt.Sprintf(
			"instance %s is %s; waiting for it to settle", h.GCEInstanceName, vmStatus))
	default:
		// Parked (TERMINATED/SUSPENDED) or repairing under our name and not
		// coming up on its own. Burn the attempt so retries (and eventually
		// the classified terminal error) surface it instead of postponing
		// forever.
		return fmt.Errorf("pre-existing instance %s is %s: not adoptable", h.GCEInstanceName, vmStatus)
	}
}

// provisionGCEInstance creates the actual GCE VM for a host.
func (s *HostService) provisionGCEInstance(ctx context.Context, h *host.Host, gceRequestID string) error {
	// Cap concurrent GCE provisions to protect the operation quota.
	if s.gceSem != nil {
		select {
		case s.gceSem <- struct{}{}:
			defer func() { <-s.gceSem }()
		case <-ctx.Done():
			return fmt.Errorf("aborted waiting for a GCE provisioning slot: %w", ctx.Err())
		}
	}

	// Resolve machine type from database or use default
	shape, err := s.resolveMachineType(ctx, h.MachineType, h.GPUType, h.GPUCount)
	if err != nil {
		return fmt.Errorf("failed to resolve machine type: %w", err)
	}

	// Tag the ambient (consumer) span with the resolved shape so provisioning
	// traces are filterable by zone/machine/GPU, which the raw GCE HTTP spans
	// don't carry.
	trace.SpanFromContext(ctx).SetAttributes(
		attribute.String("gce.zone", h.Zone),
		attribute.String("gce.machine_type", shape.machineType),
		attribute.String("gce.gpu_type", shape.gpuType),
		attribute.Int64("gce.gpu_count", shape.gpuCount),
	)

	image, err := s.resolveBootImage(ctx, shape)
	if err != nil {
		return err
	}
	instance := buildInstance(s.config, h, shape, image.SelfLink)

	// Mark the host provisioning (a status no-op on a task retry that already
	// did) and record the image this attempt boots.
	h.BootImage = image.SelfLink
	if _, err := s.hostRepo.Mutate(ctx, h.ID, func(cur *host.Host) error {
		cur.BootImage = image.SelfLink
		if cur.Status == host.StatusProvisioning {
			return nil
		}
		return cur.TransitionTo(host.StatusProvisioning)
	}); err != nil {
		return fmt.Errorf("failed to update host status: %w", err)
	}

	s.logger.Info("provisioning GCE instance",
		slog.String("host_id", h.ID),
		slog.String("instance_name", h.GCEInstanceName),
		slog.String(attrZone, h.Zone),
		slog.String("machine_type", shape.machineType),
		slog.String("boot_image", image.SelfLink),
		slog.String("service_account", s.config.ServiceAccount),
		slog.Bool("has_service_account", len(instance.ServiceAccounts) > 0),
	)

	// Immutable-infrastructure hygiene: this incarnation's predecessor may
	// still exist. Only preempted Spot husks are deleted by GCE itself; an
	// error-state retry (boot timeout, parked TERMINATED instance) leaves
	// the failed VM behind and nothing else will ever delete it. Best effort,
	// re-tried by every provision attempt; the incarnation-aware orphan GC is
	// the durable cleanup.
	if h.Incarnation > 0 {
		s.deletePredecessorInstance(ctx, h)
	}

	adopting, err := s.insertOrAdopt(ctx, h, instance, gceRequestID)
	if err != nil {
		return err
	}

	if err := s.observeProvisioned(ctx, h, adopting); err != nil {
		return err
	}

	// A successful provision proves the (zone, family) cell has capacity
	// again: clear its placement cooldown so ranking stops steering away.
	if s.zoneHealth != nil {
		if cerr := s.zoneHealth.ClearCell(ctx, h.Zone, s.machineFamilyFor(ctx, h.MachineType)); cerr != nil {
			s.logger.Warn("failed to clear zone health cell after provision",
				slog.String(attrZone, h.Zone), slog.Any("error", cerr))
		}
	}

	s.auditPreemptionRecovered(h)

	s.logger.Info("GCE instance provisioned successfully",
		slog.String("host_id", h.ID),
		slog.String("instance_name", h.GCEInstanceName),
		slog.String("status", string(h.Status)),
		slog.String("ip_address", h.IPAddress),
	)

	return nil
}

// auditPreemptionRecovered records a completed preemption recovery: this
// provision superseded the preempted incarnation, so the host serves again.
// Host rows are torn down with the fleet, so the audit trail is what the
// operations view's recovery KPIs (count and duration) are derived from.
func (s *HostService) auditPreemptionRecovered(h *host.Host) {
	if s.auditLogger == nil {
		return
	}
	duration, recovered := recoveredPreemption(h)
	if !recovered {
		return
	}
	meta := map[string]any{
		attrZone:        h.Zone,
		"instance":      h.GCEInstanceName,
		attrMachineType: h.MachineType,
		"incarnation":   h.Incarnation,
	}
	if duration > 0 {
		meta["duration_seconds"] = int64(duration.Seconds())
	}
	s.auditLogger.LogAsync(audit.New(audit.ActionHostPreemptionRecovered, audit.ResourceHost).
		WithResource(h.ID).
		WithMetadata(meta).
		Build())
}

// recoveredPreemption reports whether this host's current incarnation is the
// recovery of a preemption (it directly supersedes the preempted one; later
// incarnations are ordinary retries) and how long the recovery took. Duration
// 0 means unknown.
func recoveredPreemption(h *host.Host) (time.Duration, bool) {
	if h.PreemptedIncarnation == nil || *h.PreemptedIncarnation != h.Incarnation-1 {
		return 0, false
	}
	if h.PreemptedAt == nil {
		return 0, true
	}
	return time.Since(*h.PreemptedAt), true
}

// deletePredecessorInstance fires a best-effort delete of the previous
// incarnation's instance (its name is deterministic, from host.InstanceName).
// It does not wait on the operation: the names differ, so the new insert
// cannot collide, and a failed delete is retried by the next provision
// attempt with the orphan GC as the fallback. It looks in the host's CURRENT
// zone. A zone failover's old instance is targeted at failover time instead
// (deleteStrayInstance with the old coordinates).
func (s *HostService) deletePredecessorInstance(ctx context.Context, h *host.Host) {
	prev := host.InstanceName(h.Name, h.ID, h.Incarnation-1)
	if prev == h.GCEInstanceName {
		return
	}
	s.deleteStrayInstance(ctx, h.Zone, prev, h.ID)
}

// deleteStrayInstance best-effort-deletes one instance by its exact
// coordinates: get (absent = done), delete without waiting on the operation.
// Failures only log; the cross-zone orphan sweep is the durable cleanup.
// A nil instances client (as in unit tests) leaves everything to the sweep.
func (s *HostService) deleteStrayInstance(ctx context.Context, zone, instance, hostID string) {
	if s.instancesClient == nil {
		return
	}
	_, err := s.instancesClient.Get(ctx, &computepb.GetInstanceRequest{
		Project:  s.config.ProjectID,
		Zone:     zone,
		Instance: instance,
	})
	if isInstanceNotFound(err) {
		return // already gone: Spot auto-delete, a prior attempt, or the GC
	}
	if err != nil {
		s.logger.Warn("could not check stray instance; leaving it to the orphan GC",
			slog.String("host_id", hostID), slog.String("instance", instance), slog.Any("error", err))
		return
	}
	if _, err := s.instancesClient.Delete(ctx, &computepb.DeleteInstanceRequest{
		Project:  s.config.ProjectID,
		Zone:     zone,
		Instance: instance,
	}); err != nil {
		s.logger.Warn("failed to delete stray instance; the orphan GC is the backstop",
			slog.String("host_id", hostID), slog.String("instance", instance), slog.Any("error", err))
		return
	}
	s.logger.Info("deleted stray instance",
		slog.String("host_id", hostID), slog.String(attrZone, zone), slog.String("instance", instance))
}

// errObservationSuperseded aborts committing a VM observation when the row no
// longer names the observed instance: a concurrent zone failover or preemption
// recovery re-homed the host (new zone, new incarnation, new instance name)
// while this attempt was still looking at the old VM. Committing anyway would
// stamp the old VM's IP, and could promote the host to starting, which
// RunProvision treats as nothing-to-do, leaving the new zone's instance never
// inserted until the missing-VM sweep converges the host the hard way.
var errObservationSuperseded = errors.New("VM observation superseded by a concurrent re-home")

// observeProvisioned reads the (created or adopted) instance back and commits
// its IPs and observed status to the host row, refreshing h. The commit
// re-reads the row, so a host deleted mid-provision is not resurrected
// (deleting -> running is rejected by the state machine), and it refuses to
// land when the row no longer names the observed instance (see
// errObservationSuperseded).
func (s *HostService) observeProvisioned(ctx context.Context, h *host.Host, adopting bool) error {
	inst, err := s.instancesClient.Get(ctx, &computepb.GetInstanceRequest{
		Project:  s.config.ProjectID,
		Zone:     h.Zone,
		Instance: h.GCEInstanceName,
	})
	switch {
	case err != nil && adopting && isInstanceNotFound(err):
		// The pre-existing instance vanished between Insert and Get: a dying
		// husk finished deleting. Redeliver; the next attempt inserts cleanly.
		return task.Reschedule(adoptRetryDelay, "pre-existing instance vanished; retrying with a clean insert")
	case err != nil && adopting:
		return fmt.Errorf("inspecting pre-existing instance %s: %w", h.GCEInstanceName, err)
	case err != nil:
		// Our own insert succeeded; the next GCE sync cycle reconciles the row
		// from the live instance.
		s.logger.Warn("failed to get instance after creation",
			slog.String("host_id", h.ID),
			slog.Any("error", err),
		)
		return nil
	}

	if adopting {
		if aerr := checkAdoptable(h, inst); aerr != nil {
			return aerr
		}
	}
	updated, err := s.hostRepo.Mutate(ctx, h.ID, func(cur *host.Host) error {
		if cur.Zone != h.Zone || cur.GCEInstanceName != h.GCEInstanceName {
			return errObservationSuperseded
		}
		gce.UpdateHostFromGCEInstance(cur, inst)
		cur.ApplyVMObservation(inst.GetStatus())
		// The VM exists, so the capacity refusal that fenced this host from
		// placement is history. Cleared in the same commit that records the
		// observation, so no read can see a serving host still marked.
		cur.ClearProvisionBlocked()
		return nil
	})
	if errors.Is(err, errObservationSuperseded) {
		// This is a wait with no failure recorded. The redelivery re-reads
		// the row and provisions the re-homed identity. A plain error here
		// would also feed the failure classifier, and a superseded
		// observation says nothing about the zone.
		return task.Reschedule(adoptRetryDelay, "host re-homed mid-observation; retrying against the fresh row")
	}
	if err != nil {
		return fmt.Errorf("failed to update host after provisioning: %w", err)
	}
	*h = *updated
	s.confirmHOReady(ctx, h)
	return nil
}

// confirmHOReady is a best-effort probe of a freshly-brought-up host's Host
// Orchestrator. If HO already answers, the host is promoted 'starting' ->
// 'running' now instead of waiting for the device controller's next cycle. That
// fast path also spares the CVD-create the not-ready postpone. If HO is
// not serving yet (the common case: it boots for tens of seconds after the VM
// reports RUNNING) the host stays 'starting' and the device controller promotes it
// once HO answers. It never fails the provision, and no-ops unless the host is
// actually in the first-boot window.
func (s *HostService) confirmHOReady(ctx context.Context, h *host.Host) {
	if s.hoFactory == nil || h.Status != host.StatusStarting || h.HostOrchestratorURL() == "" {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, hoReadyProbeTimeout)
	defer cancel()
	if _, err := s.hoFactory.ClientForHost(h.HostOrchestratorURL()).GetAllCVDStatus(probeCtx); err != nil {
		return // HO not serving yet; the device controller owns the promotion
	}
	promoted, err := s.hostRepo.MarkHOReady(ctx, h.ID)
	if err != nil {
		s.logger.Warn("recording HO-ready after provision failed",
			slog.String("host_id", h.ID), slog.Any("error", err))
		return
	}
	if promoted {
		h.Status = host.StatusRunning
		s.logger.Info("host orchestrator reachable at provision time; host running",
			slog.String("host_id", h.ID))
	}
}

// Instance names are derived by the pure host.InstanceName (domain): each
// re-provision bumps the incarnation the name embeds, so a recovered host
// targets a FRESH instance, so the still-dying husk of the previous incarnation
// can never collide with it, and the orphan GC regenerates every stale
// incarnation's exact name from the row.

// RunDeprovision tears down a host from a queued task: it deletes the GCE
// instance (idempotent, since a missing instance is success) then removes the
// host row and frees its quota atomically (cascading the interleaved device
// rows). It is idempotent: a redelivery after the row is gone no-ops.
// gceRequestID (empty to disable) makes the instance delete idempotent per
// delivery.
func (s *HostService) RunDeprovision(ctx context.Context, hostID, gceRequestID string) error {
	h, err := s.hostRepo.Get(ctx, hostID)
	if err != nil {
		if errors.Is(err, host.ErrNotFound) {
			return nil // already removed
		}
		return fmt.Errorf("loading host %s: %w", hostID, err)
	}

	if h.GCEInstanceName != "" {
		deleteReq := &computepb.DeleteInstanceRequest{
			Project:  s.config.ProjectID,
			Zone:     h.Zone,
			Instance: h.GCEInstanceName,
		}
		if gceRequestID != "" {
			deleteReq.RequestId = &gceRequestID
		}
		op, derr := s.instancesClient.Delete(ctx, deleteReq)
		switch {
		case isInstanceNotFound(derr):
			s.logger.Warn("GCE instance already gone during deprovision",
				slog.String("host_id", hostID), slog.String("instance_name", h.GCEInstanceName))
		case derr != nil:
			return fmt.Errorf("deleting GCE instance %s: %w", h.GCEInstanceName, derr)
		default:
			if err := gce.WaitZoneOperation(ctx, s.operationsClient, s.config.ProjectID, h.Zone, op.Name()); err != nil {
				return fmt.Errorf("waiting for GCE instance deletion: %w", err)
			}
		}
	}

	if err := s.hostRepo.DeleteWithQuotaDecrement(ctx, hostID); err != nil {
		return fmt.Errorf("deleting host %s: %w", hostID, err)
	}
	s.logger.Info("host deprovisioned", slog.String("host_id", hostID), slog.String(attrName, h.Name))
	return nil
}
