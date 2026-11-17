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
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// preemptionLookback is the fixed scan window for preemption operations. The
// scan is stateless: it re-reads the same recent window every cycle and relies
// on per-host operation-ID dedupe (LastPreemptionID) for idempotency, so a
// leader failover or restart cannot drop a watermark and miss an event.
const preemptionLookback = 30 * time.Minute

// Zone-wide preemption breaker: when more than zoneBreakerFraction of a
// zone's Spot hosts (and at least zoneBreakerMinHosts exist) are preempted in
// one scan, auto-recovery is suspended for the cycle. Re-provisioning a
// whole zone's fleet into the capacity crunch that just reclaimed it thrashes
// quota and money. Hosts are still marked preempted; recovery resumes when
// the scan calms down (or an operator retries manually).
const (
	zoneBreakerMinHosts = 4
	zoneBreakerFraction = 0.4
)

// recoveryJitterMax spreads a zone-wide reclaim's recovery enqueues so they
// do not re-provision the whole fleet in one synchronized wave.
const recoveryJitterMax = 30 * time.Second

// instanceEvidence classifies the out-of-band operations observed for one
// instance name within the scan window.
type instanceEvidence struct {
	preemptOpID string // newest compute.instances.preempted operation ID ("" = none)
	deleteOpID  string // newest compute.instances.delete operation ID ("" = none)
	// Flags describe the newest delete operation's principal: a system
	// principal is the Spot reclaim tail (preemption evidence even when the
	// preempted operation never appears); anything else is intent from
	// outside CCO's control plane and must be honored as deletion.
	systemDelete   bool
	externalDelete bool
}

// checkPreemptions gathers a zone's out-of-band operation evidence
// (preemptions and deletes) and marks preempted hosts. It runs BEFORE the
// zone sync in each cycle, and its evidence stays available to the sync's
// missing-VM classification (s.zoneEvidence).
//
// Preemption evidence comes on two channels: the compute.instances.preempted
// operation, and an instances.delete issued by a system principal, which is
// the reclaim's visible tail. The second channel is needed because with
// termination action DELETE the preempted operation is NOT guaranteed to
// appear (GCE docs).
func (s *Service) checkPreemptions(ctx context.Context, zone string) error {
	s.zoneEvidence = nil
	if s.operationsClient == nil {
		return nil
	}

	evidence, err := s.scanZoneOperations(ctx, zone)
	if err != nil {
		return err
	}
	s.zoneEvidence = evidence

	byInstance := make(map[string]string)
	for name, ev := range evidence {
		switch {
		case ev.preemptOpID != "":
			byInstance[name] = ev.preemptOpID
		case ev.systemDelete:
			byInstance[name] = ev.deleteOpID
		}
	}
	if len(byInstance) == 0 {
		return nil
	}
	if err := s.handlePreemptions(ctx, zone, byInstance); err != nil {
		return fmt.Errorf("failed to handle preemptions: %w", err)
	}
	return nil
}

// scanZoneOperations lists recent preempted and delete operations for
// managed instances in a zone, newest-per-instance, windowed client-side.
func (s *Service) scanZoneOperations(ctx context.Context, zone string) (map[string]instanceEvidence, error) {
	collect := newEvidenceCollector()
	for _, opType := range []string{opTypePreempted, opTypeDelete} {
		if err := s.scanOperationsOfType(ctx, zone, opType, collect); err != nil {
			return nil, err
		}
	}
	return collect.evidence, nil
}

// scanOperationsOfType folds one operation type's recent zone operations
// into the collector, windowed client-side.
//
// The scan is a full iteration by necessity, verified live: zoneOperations
// .list rejects BOTH an insertTime range in the filter ("Invalid list filter
// expression") AND orderBy=creationTimestamp desc (400 on a real project,
// 2026-07-12, because the sortable-list support other collections have does
// not extend to operations), so neither a server-side window nor a newest-first
// early break is available. The client-side type check stays because a fake
// or proxy may ignore the server-side filter.
func (s *Service) scanOperationsOfType(ctx context.Context, zone, opType string, collect *evidenceCollector) error {
	windowStart := time.Now().Add(-preemptionLookback)
	filter := fmt.Sprintf("operationType=%q", opType)
	it := s.operationsClient.List(ctx, &computepb.ListZoneOperationsRequest{
		Project: s.config.ProjectID,
		Zone:    zone,
		Filter:  &filter,
	})
	for {
		op, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("listing %s operations: %w", opType, err)
		}
		if op.GetOperationType() != opType {
			continue
		}
		var insertedAt time.Time
		if op.InsertTime != nil {
			if t, perr := time.Parse(time.RFC3339Nano, *op.InsertTime); perr == nil {
				insertedAt = t
			}
		}
		if !insertedAt.IsZero() && insertedAt.Before(windowStart) {
			continue
		}
		name := extractInstanceName(op.GetTargetLink())
		if name == "" || !s.isManagedInstance(name) || op.Id == nil {
			continue
		}
		collect.add(opType, name, strconv.FormatUint(*op.Id, 10), op.GetUser(), insertedAt)
	}
}

// evidenceCollector folds operations into per-instance evidence, keeping the
// newest operation per (instance, type): a host preempted twice in the
// window must be judged by the later event, or the ID dedupe would mask the
// second.
type evidenceCollector struct {
	evidence      map[string]instanceEvidence
	newestPreempt map[string]time.Time
	newestDelete  map[string]time.Time
}

func newEvidenceCollector() *evidenceCollector {
	return &evidenceCollector{
		evidence:      make(map[string]instanceEvidence),
		newestPreempt: make(map[string]time.Time),
		newestDelete:  make(map[string]time.Time),
	}
}

func (c *evidenceCollector) add(opType, name, opID, user string, at time.Time) {
	ev := c.evidence[name]
	switch opType {
	case opTypePreempted:
		if prev, ok := c.newestPreempt[name]; !ok || at.After(prev) {
			c.newestPreempt[name] = at
			ev.preemptOpID = opID
		}
	case opTypeDelete:
		if prev, ok := c.newestDelete[name]; !ok || at.After(prev) {
			c.newestDelete[name] = at
			ev.deleteOpID = opID
			ev.systemDelete = isSystemPrincipal(user)
			ev.externalDelete = !ev.systemDelete
		}
	}
	c.evidence[name] = ev
}

// Operation types the evidence scan reads.
const (
	opTypePreempted = "compute.instances.preempted"
	opTypeDelete    = "compute.instances.delete"
)

// isSystemPrincipal reports whether an operation's user names GCE itself,
// the principal on the delete tail of a Spot reclaim. The match is exact:
// misreading a human/service-account delete as a preemption would resurrect
// a host its owner intended gone, so anything unrecognized classifies as
// external (worst case: a real preemption degrades to the lost-host path,
// the pre-audit behavior).
func isSystemPrincipal(user string) bool {
	return strings.EqualFold(user, "system") || strings.EqualFold(user, "system@google.com")
}

// handlePreemptions marks preempted hosts and applies the recovery decision,
// under the zone recovery breaker's judgment.
func (s *Service) handlePreemptions(ctx context.Context, zone string, preemptedInstances map[string]string) error {
	hosts, err := s.hostRepo.List(ctx, host.ListFilter{Zone: &zone})
	if err != nil {
		return fmt.Errorf("failed to list hosts: %w", err)
	}

	breakerTripped := s.evaluateZoneBreaker(ctx, zone, hosts, preemptedInstances)

	for _, h := range hosts {
		if h.Status == host.StatusPreempted {
			// The level half of recovery: a host can sit preempted with no
			// live recovery task (the mark's enqueue failed, or the breaker
			// suspended that cycle) and no edge detector ever revisits it
			// (the operation dedupe skips handled events, and the evidence
			// ages out of the lookback entirely). Re-file whenever nothing
			// live owns the recovery.
			s.ensureRecoveryFiled(ctx, h, breakerTripped)
			continue
		}
		if h.GCEInstanceName == "" {
			continue
		}
		opID, preempted := preemptedInstances[h.GCEInstanceName]
		switch {
		case !preempted:
			continue
		case h.LastPreemptionID == opID:
			// Already handled: the stateless lookback re-reads recent
			// operations every cycle, so an event this host was already
			// recovered from must not re-preempt it. The per-host operation
			// ID is the dedupe.
			continue
		case !h.IsSpotVM():
			// Shouldn't happen, but defensive check.
			s.logger.Warn("preemption detected for non-Spot VM",
				slog.String("host_id", h.ID),
				slog.String("instance", h.GCEInstanceName),
			)
			continue
		}
		s.markHostPreempted(ctx, h, opID, breakerTripped)
	}

	return nil
}

// evaluateZoneBreaker decides the zone recovery breaker: when most of a
// zone's Spot fleet is preempted in one scan, the zone is out of capacity.
// Re-provisioning everything into that crunch thrashes, so auto-recovery is
// suspended for the cycle (hosts are still marked preempted). A tripped
// breaker also graduates the evidence into placement memory
// (recordPreemptionStorm).
func (s *Service) evaluateZoneBreaker(ctx context.Context, zone string, hosts []*host.Host, preempted map[string]string) bool {
	var spotHosts, affected int
	for _, h := range hosts {
		if h.GCEInstanceName == "" || !h.IsSpotVM() {
			continue
		}
		spotHosts++
		if _, hit := preempted[h.GCEInstanceName]; hit {
			affected++
		}
	}
	tripped := spotHosts >= zoneBreakerMinHosts &&
		float64(affected) > zoneBreakerFraction*float64(spotHosts)
	if !tripped {
		return false
	}
	s.remediationDeferred.Add(int64(affected))
	s.logger.Error("zone-wide preemption detected; suspending auto-recovery this cycle",
		slog.String("zone", zone),
		slog.Int("spot_hosts", spotHosts),
		slog.Int("preempted", affected),
	)
	s.recordPreemptionStorm(ctx, zone, hosts, preempted, spotHosts, affected)
	return true
}

// markHostPreempted transactionally marks one host preempted, applies the
// auto-recovery decision, and audits the event. MarkPreempted re-reads the
// row under transaction locks, so a device create/delete that bumps
// current_device_count concurrently is never clobbered by a stale full-row
// write, and the transition is validated in the same transaction: a host the
// user is deleting is never preempted-marked, because preempted-over-deleting
// would pass CanRetry and auto-recovery would re-provision a host the user just
// deleted. The returned host carries the transactionally-fresh preemption
// count used for the recovery decision.
func (s *Service) markHostPreempted(ctx context.Context, h *host.Host, opID string, breakerTripped bool) {
	s.logger.Info("marking host as preempted",
		slog.String("host_id", h.ID),
		slog.String("instance", h.GCEInstanceName),
		slog.String("operation_id", opID),
	)
	updated, err := s.hostRepo.MarkPreempted(ctx, h.ID, opID)
	if err != nil {
		if _, ok := errors.AsType[*host.InvalidStateError](err); ok {
			// Whoever moved the host owns the outcome; skip recovery too.
			s.logger.Info("preemption mark superseded by a concurrent transition",
				slog.String("host_id", h.ID), slog.Any("error", err))
			return
		}
		s.logger.Error("failed to update preempted host",
			slog.String("host_id", h.ID),
			slog.Any("error", err),
		)
		return
	}
	s.preemptionsDetected.Add(1)
	disposition := s.recoverAfterPreemption(ctx, updated, breakerTripped)
	s.auditPreempted(updated, opID, disposition)
}

// recoverAfterPreemption applies the auto-recovery decision for one preempted
// host and reports its disposition (for the audit trail). The detector
// detects; the keyed reconcile loop actuates: recovery is a jittered
// host.reconcile enqueue that inherits pending dedup, per-key serialization,
// and resync priority, and the reconcile handler backs it with the provision
// queue, which retries and dead-letters to host=error, so a preempted host
// is not left stranded. A failed enqueue and a tripped zone breaker both
// leave the host preempted for ensureRecoveryFiled to re-file on a later
// cycle; a host preempted past the cap stays manual (no recreate thrash).
func (s *Service) recoverAfterPreemption(ctx context.Context, h *host.Host, breakerTripped bool) string {
	if breakerTripped {
		return "suspended_zone_breaker"
	}
	if s.recovery == nil {
		return "manual"
	}
	limit := s.config.MaxPreemptionRecoveries
	if limit <= 0 {
		limit = defaultMaxPreemptionRecoveries
	}
	if h.PreemptionCount > int64(limit) {
		s.logger.Warn("host exceeded auto-recovery limit; leaving preempted for manual retry",
			slog.String("host_id", h.ID), slog.Int64("preemption_count", h.PreemptionCount))
		return "recovery_cap_exceeded"
	}
	jitter := time.Duration(rand.Int64N(int64(recoveryJitterMax))) //nolint:gosec // schedule jitter; not security-sensitive
	if err := s.recovery.EnqueueResync(ctx, task.TypeHostReconcile, h.ID, jitter); err != nil {
		s.logger.Error("failed to auto-recover preempted host",
			slog.String("host_id", h.ID), slog.Any("error", err))
		return "recovery_enqueue_failed"
	}
	s.preemptionsRecovered.Add(1)
	return "recovery_enqueued"
}

// ensureRecoveryFiled is the level half of preemption recovery, run for every
// host already sitting in preempted: the edge half (recoverAfterPreemption)
// can leave one stranded (its enqueue can fail, and a tripped zone breaker
// suspends the whole cycle) and nothing else ever revisits the status. It is
// silent when healthy: it files only when the breaker is clear, the recovery cap
// still allows automation, and no live host.reconcile task exists.
func (s *Service) ensureRecoveryFiled(ctx context.Context, h *host.Host, breakerTripped bool) {
	if breakerTripped || s.recovery == nil {
		return
	}
	limit := s.config.MaxPreemptionRecoveries
	if limit <= 0 {
		limit = defaultMaxPreemptionRecoveries
	}
	if h.PreemptionCount > int64(limit) {
		return // recoverAfterPreemption warned at mark time; stays manual
	}
	active, err := s.recovery.HasActive(ctx, task.TypeHostReconcile, h.ID)
	if err != nil {
		s.logger.Warn("failed to probe for a live recovery task",
			slog.String("host_id", h.ID), slog.Any("error", err))
		return
	}
	if active {
		return
	}
	jitter := time.Duration(rand.Int64N(int64(recoveryJitterMax))) //nolint:gosec // schedule jitter; not security-sensitive
	if err := s.recovery.EnqueueResync(ctx, task.TypeHostReconcile, h.ID, jitter); err != nil {
		s.logger.Error("failed to re-file recovery for preempted host",
			slog.String("host_id", h.ID), slog.Any("error", err))
		return
	}
	s.preemptionsRecovered.Add(1)
	s.logger.Warn("preempted host had no live recovery task; re-filed",
		slog.String("host_id", h.ID),
		slog.Int64("preemption_count", h.PreemptionCount))
}

// auditPreempted records one detected preemption in the audit log.
func (s *Service) auditPreempted(h *host.Host, opID, disposition string) {
	if s.auditLogger == nil {
		return
	}
	s.auditLogger.LogAsync(audit.New(audit.ActionHostPreempted, audit.ResourceHost).
		WithResource(h.ID).
		WithMetadata(map[string]any{
			"zone":             h.Zone,
			"instance":         h.GCEInstanceName,
			"machine_type":     h.MachineType,
			"operation_id":     opID,
			"preemption_count": h.PreemptionCount,
			"recovery":         disposition,
		}).
		Build())
}

// recordPreemptionStorm graduates the breaker's evidence into the placement
// plane: one zone_health preemption cell per affected (zone, machine family).
// It is best-effort, because placement memory must never fail the sync cycle,
// and cleared like any other cell by the next successful provision in the cell.
func (s *Service) recordPreemptionStorm(ctx context.Context, zone string, hosts []*host.Host, preempted map[string]string, spotHosts, affected int) {
	if s.zoneHealth == nil {
		return
	}
	reason := fmt.Sprintf("zone-wide preemption storm: %d of %d Spot hosts preempted in one scan", affected, spotHosts)
	families := make(map[string]bool)
	for _, h := range hosts {
		if _, hit := preempted[h.GCEInstanceName]; !hit || !h.IsSpotVM() {
			continue
		}
		if family := machinetype.FamilyOf(h.MachineType); family != "" {
			families[family] = true
		}
	}
	for family := range families {
		if _, err := s.zoneHealth.RecordFailure(ctx, zone, family, gcecatalog.ZoneMetricPreemption, reason); err != nil {
			s.logger.Warn("failed to record preemption storm in zone health",
				slog.String("zone", zone),
				slog.String("machine_family", family),
				slog.Any("error", err))
		}
	}
}

// extractInstanceName extracts the instance name from a GCE resource URL.
func extractInstanceName(targetLink string) string {
	// Format: https://compute.googleapis.com/compute/v1/projects/{project}/zones/{zone}/instances/{instance}
	parts := strings.Split(targetLink, "/")
	if len(parts) >= 2 && parts[len(parts)-2] == "instances" {
		return parts[len(parts)-1]
	}
	return ""
}
