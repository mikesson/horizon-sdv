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

package hostreconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/gce"
)

// LostMarkReason is the tombstone reason MarkHostLost records for an
// out-of-band VM deletion; the lost-mark blast budget counts durable marks
// carrying it (CountLostMarksSince), and the robustness harness's drift
// checker matches it. The string is an identifier, so its text must not change.
const LostMarkReason = "host VM deleted outside CCO"

// driftClass tags a detected GCE↔inventory divergence for metrics: only a
// hostile signal (a vanished VM, counted via hosts_lost, or a blocked status
// write) warrants attention; benign and adopted churn is normal operation.
type driftClass int

const (
	driftNone    driftClass = iota
	driftBenign             // IP/metadata only
	driftAdopted            // out-of-band status change absorbed by the lifecycle
	driftBlocked            // GCE status conflicts with an app-owned lifecycle state
)

// missingSpotGrace bounds how long a Spot host's unexplained VM absence
// waits for operation evidence before degrading to the lost-host path.
// Operations surface within seconds; past the grace the pre-audit behavior
// (confirm + mark lost) applies, so the worst case is a delayed lost-mark,
// never a stuck host.
const missingSpotGrace = 5 * time.Minute

// staleTransitionalAfter is how long a transitional host may go without a
// status write before the sweep suspects its transition task is gone. Long
// enough to sit out a full provision retry backoff (cap 5m would bump
// updated_at only on success). Suspicion past this is verified before it is
// acted on, so a false positive costs one no-op reconcile.
const staleTransitionalAfter = 5 * time.Minute

// isTransitional reports whether a host status is owned by a transition task.
func isTransitional(s host.Status) bool {
	return s == host.StatusPending || s == host.StatusProvisioning || s == host.StatusDeleting
}

// SyncZone synchronizes hosts in a specific zone
func (s *Service) SyncZone(ctx context.Context, zone string) error {
	s.logger.Debug("syncing zone", slog.String("zone", zone))

	// Get all instances from GCE
	gceInstances, err := s.listGCEInstances(ctx, zone)
	if err != nil {
		return fmt.Errorf("failed to list GCE instances: %w", err)
	}

	// Get all hosts from database for this zone
	dbHosts, err := s.hostRepo.List(ctx, host.ListFilter{Zone: &zone})
	if err != nil {
		return fmt.Errorf("failed to list database hosts: %w", err)
	}

	// Create lookup map
	gceByName := make(map[string]*computepb.Instance)
	for _, inst := range gceInstances {
		gceByName[inst.GetName()] = inst
	}

	batchUpdates := s.planZoneUpdates(ctx, dbHosts, gceByName)

	// Apply all updates in a single batch (or batches of 500). Each row write
	// is preconditioned on the status it was planned from; a shortfall means a
	// concurrent lifecycle transition won the row and the stale plan was
	// discarded. The next cycle re-observes.
	var applied int64
	if len(batchUpdates) > 0 {
		var err error
		applied, err = s.hostRepo.BatchUpdateGCEState(ctx, batchUpdates)
		if err != nil {
			s.logger.Error("failed to batch update hosts",
				slog.Int("count", len(batchUpdates)),
				slog.Any("error", err),
			)
			return fmt.Errorf("failed to batch update hosts: %w", err)
		}
		s.driftUpdates.Add(applied)
		if missed := int64(len(batchUpdates)) - applied; missed > 0 {
			s.casMisses.Add(missed)
			s.logger.Info("stale drift updates skipped by concurrent transitions",
				slog.Int64("missed", missed),
				slog.String("zone", zone),
			)
		}
		s.logger.Info("batch updated hosts from GCE",
			slog.Int64("count", applied),
		)
	}

	s.logger.Debug("zone sync complete",
		slog.String("zone", zone),
		slog.Int("gce_instances", len(gceInstances)),
		slog.Int("db_hosts", len(dbHosts)),
		slog.Int64("updates_applied", applied),
	)

	return nil
}

// planZoneUpdates walks one zone's host rows against the GCE listing:
// stranded-transitional rows get reconcile suspicion, missing VMs go through
// the lost-host gates, and observed drift is planned into batch updates
// (collected so they apply in one commit, not N+1).
func (s *Service) planZoneUpdates(ctx context.Context, dbHosts []*host.Host, gceByName map[string]*computepb.Instance) []host.GCEStateUpdate {
	var batchUpdates []host.GCEStateUpdate
	for _, h := range dbHosts {
		// Stranded-transitional suspicion: a host sitting in a
		// transitional state with no status write for this long has likely
		// lost its transition task (transition enqueues are atomic with
		// their status writes, so this is rare: admin task deletion, a bug).
		// Enqueue reconcile suspicion; the handler verifies against the live
		// queue and heals. Repeated enqueues coalesce into one pending row.
		if s.reconcile != nil && isTransitional(h.Status) &&
			time.Since(h.UpdatedAt) > staleTransitionalAfter {
			if err := s.reconcile.EnqueueResync(ctx, task.TypeHostReconcile, h.ID, 0); err != nil {
				s.logger.Warn("failed to enqueue reconcile for stale transitional host",
					slog.String("host_id", h.ID), slog.Any("error", err))
			}
		}
		if h.GCEInstanceName == "" {
			continue
		}

		gceInst, exists := gceByName[h.GCEInstanceName]
		if !exists {
			s.handleMissingVM(ctx, h)
			continue
		}

		update, class, changed := s.planGCEUpdate(h, gceInst)
		switch class {
		case driftBenign:
			s.driftBenign.Add(1)
		case driftAdopted:
			s.driftAdopted.Add(1)
		case driftBlocked:
			s.driftBlocked.Add(1)
		case driftNone:
		}
		if !changed {
			continue
		}
		s.logger.Info("queueing host update from GCE",
			slog.String("host_id", h.ID),
			slog.String("status", string(update.Status)),
			slog.Bool("status_changed", update.Status != h.Status),
		)
		batchUpdates = append(batchUpdates, update)
	}
	return batchUpdates
}

// handleMissingVM decides whether a host absent from the zone listing was
// deleted outside CCO, and marks it lost, deleting the host and failing over
// its live devices in one commit, so they never linger as phantom "running"
// rows until their own TTL. Three gates make the decision safe:
//
//   - Absence is only meaningful in statuses where the VM must exist
//     (host.Status.ExpectsVM): before provisioning inserts the instance and
//     after teardown removes it, a missing VM is a normal lifecycle window,
//     and a preempted Spot VM is deleted by GCE itself pending recovery.
//   - A Spot host's absence must first be CLASSIFIED (spotAbsenceIsDeletion):
//     a vanished Spot VM is a preemption until proven otherwise, and reading
//     it as out-of-band deletion tombstones the host and forecloses the
//     recovery the preemption detector owns.
//   - Absence must be confirmed by a point read. The zone listing is a
//     snapshot taken before the host rows were read, so an instance inserted
//     between the two reads would otherwise be declared lost, and a degraded
//     listing (partial page, transient API failure) must never read as mass
//     extinction. Anything but a definitive not-found waits for the next
//     cycle.
//
// MarkHostLost re-verifies the status under its transaction, closing the
// remaining race with a concurrent delete or preemption.
func (s *Service) handleMissingVM(ctx context.Context, h *host.Host) {
	if !h.Status.ExpectsVM() {
		return
	}
	if h.IsSpotVM() && !s.spotAbsenceIsDeletion(h) {
		return
	}
	// Blast-radius gate: a mass disappearance is far more likely a GCE
	// listing/API anomaly than a real mass deletion. Beyond the budget,
	// degrade to alerting and let the next cycle re-observe. The budget is
	// counted from the durable marks themselves (tombstones carrying
	// LostMarkReason within one sync interval) and not from a process-local
	// counter. Leadership is best-effort, and during a handoff two instances
	// can sweep concurrently, so one counter each would double the cap exactly
	// when a mass anomaly is in flight. Concurrent sweeps can still overshoot
	// by one mark each (the count and the mark are separate commits), so the
	// gate is an approximate safety margin.
	limit := s.config.MaxLostMarksPerCycle
	if limit <= 0 {
		limit = defaultMaxLostMarksPerCycle
	}
	window := s.config.SyncInterval
	if window <= 0 {
		window = time.Minute
	}
	recent, err := s.hostRepo.CountLostMarksSince(ctx, time.Now().Add(-window), LostMarkReason)
	if err != nil {
		// No evidence, no destruction: without the count the gate cannot
		// tell one lost VM from a mass anomaly.
		s.remediationDeferred.Add(1)
		s.logger.Error("failed to count recent lost marks; deferring correction",
			slog.String("host_id", h.ID),
			slog.String("instance", h.GCEInstanceName),
			slog.Any("error", err),
		)
		return
	}
	if recent >= int64(limit) {
		s.remediationDeferred.Add(1)
		s.logger.Error("lost-mark budget exhausted; deferring correction because mass VM disappearance is anomalous",
			slog.String("host_id", h.ID),
			slog.String("instance", h.GCEInstanceName),
			slog.Int("budget", limit),
			slog.Int64("recent_marks", recent),
		)
		return
	}
	if !s.instanceGone(ctx, h) {
		return
	}
	s.logger.Info("marking host as lost (instance not found in GCE)",
		slog.String("host_id", h.ID),
		slog.String("instance", h.GCEInstanceName),
	)
	err = s.hostRepo.MarkHostLost(ctx, h.ID, LostMarkReason)
	if err == nil {
		s.hostsLost.Add(1)
		return
	}
	if _, ok := errors.AsType[*host.InvalidStateError](err); ok {
		// The host moved to a status that no longer expects a VM (raced a
		// delete or a preemption); whoever moved it owns the outcome.
		s.logger.Info("lost-host mark superseded by a concurrent transition",
			slog.String("host_id", h.ID), slog.Any("error", err))
		return
	}
	s.logger.Error("failed to mark host lost",
		slog.String("host_id", h.ID),
		slog.Any("error", err),
	)
}

// spotAbsenceIsDeletion classifies a Spot host's missing VM: true means the
// absence may be treated as out-of-band deletion (proceed to the lost path),
// false means it is explained or not yet explainable (leave the host alone).
//
//   - Preemption evidence (a preempted op, or a system-principal delete):
//     the ops scan, which runs before the zone sync, owns the host. It
//     marked it preempted, or a concurrent transition superseded the mark.
//     Never lost.
//   - An external-principal delete: intent from outside CCO, honored, lost.
//   - No evidence yet: operations lag their effects, and the preempted op is
//     not guaranteed at all (termination action DELETE), so wait out
//     missingSpotGrace before degrading to the lost path, so the worst case
//     is a delayed lost-mark, never a foreclosed recovery.
func (s *Service) spotAbsenceIsDeletion(h *host.Host) bool {
	ev, ok := s.zoneEvidence[h.GCEInstanceName]
	switch {
	case ok && (ev.preemptOpID != "" || ev.systemDelete):
		delete(s.missingSpotSince, h.ID)
		return false
	case ok && ev.externalDelete:
		delete(s.missingSpotSince, h.ID)
		return true
	default:
		first, seen := s.missingSpotSince[h.ID]
		now := time.Now()
		if !seen {
			s.missingSpotSince[h.ID] = now
			s.logger.Info("spot VM missing with no operation evidence; deferring classification",
				slog.String("host_id", h.ID),
				slog.String("instance", h.GCEInstanceName),
				slog.Duration("grace", missingSpotGrace))
			return false
		}
		if now.Sub(first) < missingSpotGrace {
			return false
		}
		delete(s.missingSpotSince, h.ID)
		return true
	}
}

// instanceGone reports whether a fresh point read of the host's instance
// definitively returned not-found.
func (s *Service) instanceGone(ctx context.Context, h *host.Host) bool {
	_, err := s.client.Get(ctx, &computepb.GetInstanceRequest{
		Project:  s.config.ProjectID,
		Zone:     h.Zone,
		Instance: h.GCEInstanceName,
	})
	if err == nil {
		return false
	}
	if apiErr, ok := errors.AsType[*googleapi.Error](err); ok && apiErr.Code == http.StatusNotFound {
		return true
	}
	s.logger.Warn("could not confirm missing instance; deferring lost-host mark",
		slog.String("host_id", h.ID),
		slog.String("instance", h.GCEInstanceName),
		slog.Any("error", err))
	return false
}

// planGCEUpdate computes the GCE-owned-column update for a database host from
// its GCE instance, mutating h's IP fields in the process, and classifies the
// divergence for metrics. It reports false when nothing needs writing. GCE
// state never overrides app-owned lifecycle transitions: a host
// mid-deprovision stays deleting even while its VM still reports RUNNING;
// invalid transitions keep the stored status (IPs may still be updated).
func (s *Service) planGCEUpdate(h *host.Host, gceInst *computepb.Instance) (host.GCEStateUpdate, driftClass, bool) {
	// Capture the database view before UpdateHostFromGCEInstance mutates h, so
	// the comparisons below detect actual drift. The lifecycle status is
	// DERIVED from the host's conditions and this raw observation via the single
	// host.DerivePhase (replacing this job's former private status mapping); an
	// phase carries no lifecycle opinion, so only the raw observation is recorded.
	// The CanTransitionTo gate below still fences the derivation out of app- and
	// recovery-owned states: a preempted host awaiting re-provision must not be
	// flipped back to running by a lagging RUNNING observation, and a host being
	// deleted or in error keeps that phase (DerivePhase already yields it, so the
	// gate is defense in depth).
	newStatus, derivable := host.DerivePhase(h, gceInst.GetStatus())
	if !derivable {
		newStatus = ""
	}
	prevStatus := h.Status
	prevIP := h.IPAddress
	prevInternalIP := h.InternalIP
	prevTerminatesAt := h.VMTerminatesAt
	gce.UpdateHostFromGCEInstance(h, gceInst)

	statusChanged := newStatus != "" && newStatus != prevStatus
	ipChanged := h.IPAddress != prevIP || h.InternalIP != prevInternalIP
	// The raw GCE state is an OBSERVATION: recorded whenever it changes,
	// even when the mapped lifecycle status is blocked by an app-owned
	// state. The phase derivation consumes it, and the console can
	// show accurate "VM says TERMINATED" detail regardless of who owns the enum.
	// The VM's stop time is the same kind of observation, and it moves on
	// every VM start.
	rawChanged := gceInst.GetStatus() != h.VMStatus || !sameInstant(prevTerminatesAt, h.VMTerminatesAt)
	if !statusChanged && !ipChanged && !rawChanged {
		return host.GCEStateUpdate{}, driftNone, false
	}

	class := driftBenign
	writeStatus := prevStatus
	if statusChanged {
		if prevStatus.CanTransitionTo(newStatus) {
			writeStatus = newStatus
			class = driftAdopted
		} else {
			class = driftBlocked
			s.logger.Warn("ignoring GCE status for host in app-owned lifecycle state",
				slog.String("host_id", h.ID),
				slog.String("db_status", string(prevStatus)),
				slog.String("gce_status", string(newStatus)),
			)
			// The enum write is blocked; with no IP change and no new raw
			// observation there is nothing left to record.
			if !ipChanged && !rawChanged {
				return host.GCEStateUpdate{}, class, false
			}
			// Otherwise the raw observation (and any IP change) still lands.
			// writeStatus stays prevStatus, so the CAS precondition
			// self-fences as usual.
		}
	}

	return host.GCEStateUpdate{
		ID:             h.ID,
		VMStatus:       gceInst.GetStatus(),
		PrevStatus:     prevStatus,
		Status:         writeStatus,
		IP:             h.IPAddress,
		InternalIP:     h.InternalIP,
		VMTerminatesAt: h.VMTerminatesAt,
	}, class, true
}

// sameInstant reports whether two optional timestamps name the same instant.
func sameInstant(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// listGCEInstances lists the zone's instances under the managed name prefix.
// It applies no label test: a host row owns the VM its instance name points
// at, whatever labels that VM carries, so a VM created before a label was
// introduced keeps adopting drift.
func (s *Service) listGCEInstances(ctx context.Context, zone string) ([]*computepb.Instance, error) {
	filter := s.managedNameFilter()

	req := &computepb.ListInstancesRequest{
		Project: s.config.ProjectID,
		Zone:    zone,
		Filter:  &filter,
	}

	var instances []*computepb.Instance
	it := s.client.List(ctx, req)
	for {
		inst, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("iterating zone instances: %w", err)
		}
		instances = append(instances, inst)
	}

	return instances, nil
}
