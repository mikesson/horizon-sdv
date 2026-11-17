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
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/gce"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// Placement instruments, shared by ranked creates and failover. Lazy: the
// meter provider exists only in production wiring.
var (
	placementOnce     sync.Once
	placementSelected metric.Int64Counter
	placementFailover metric.Int64Counter
)

func placementInstruments() (metric.Int64Counter, metric.Int64Counter) {
	placementOnce.Do(func() {
		meter := otel.Meter(telemetry.ScopeName("host"))
		placementSelected, _ = meter.Int64Counter("cco.placement.selected",
			metric.WithDescription("Hosts bound to a zone by ranked placement."))
		placementFailover, _ = meter.Int64Counter("cco.placement.failover",
			metric.WithDescription("Provisioning hosts moved to another ranked zone after a classified capacity failure."))
	})
	return placementSelected, placementFailover
}

// resolvePlacement fills in what a create left to the service: the catalog's
// default machine type when it names none, then a ranked zone when it gives
// none. Everything after it, both prechecks and the host row, sees the shape
// and zone that are built.
func (s *HostService) resolvePlacement(ctx context.Context, req host.CreateRequest) (host.CreateRequest, error) {
	if req.MachineType == "" && s.machineTypes != nil {
		mt, err := s.machineTypes.GetDefault(ctx)
		if err != nil {
			return req, fmt.Errorf("resolving the default machine type: %w", err)
		}
		req.MachineType = mt.ID
	}
	if req.Zone == "" {
		zone, err := s.rankPlacementZone(ctx, req)
		if err != nil {
			return req, err
		}
		req.Zone = zone
	}
	return req, nil
}

// rankPlacementZone picks the zone for a create that named none, biased by
// the request's location hint. GPU intent is a hard filter by construction:
// the request's GPU fields came from the shape resolution, so a CPU spec
// never ranks GPU capacity and vice versa.
func (s *HostService) rankPlacementZone(ctx context.Context, req host.CreateRequest) (string, error) {
	if s.catalogValidator == nil {
		return "", errors.New("host create without a zone requires the catalog validator")
	}
	candidates, err := s.catalogValidator.RankZones(ctx, gcecatalog.ValidationRequest{
		MachineType:       req.MachineType,
		GPUType:           req.GPUType,
		GPUCount:          req.GPUCount,
		ProvisioningModel: string(req.ProvisioningModel),
	}, gcecatalog.RankOptions{PreferredRegion: preferredRegionOf(req.LocationHint)})
	if err != nil {
		return "", fmt.Errorf("ranking placement zones: %w", err)
	}
	if len(candidates) == 0 {
		return "", &host.CatalogValidationError{Errors: []host.CatalogValidationDetail{{
			Field:      attrZone,
			Code:       gcecatalog.ErrorCodeZoneNotFound,
			Message:    "no enabled zone has capacity for the requested shape",
			Suggestion: "Enable a zone in the catalog or free regional quota",
		}}}
	}
	best := candidates[0]
	s.logger.Info("ranked placement zone selected",
		slog.String(attrZone, best.Zone),
		slog.String(attrMachineType, req.MachineType),
		slog.Any("reasons", best.Reasons))
	return best.Zone, nil
}

// countRankedBinding counts one host bound to a zone by ranked placement
// (cco.placement.selected). It runs at the create commit point so every
// ranked binding counts exactly once, whether ranked in this service (auto)
// or upstream in a declarative/batch template (pre-ranked). A pinned zone is
// the caller's placement, not the ranker's, and does not count.
func countRankedBinding(ctx context.Context, zone string) {
	selected, _ := placementInstruments()
	selected.Add(ctx, 1, metric.WithAttributes(attribute.String("zone", zone)))
}

// failoverMetric maps a classified provisioning error to the zone_health
// binding metric it should cool, reporting false for errors that say nothing
// about the zone (a rate limit, a permission problem, an image typo).
// Throttling is structurally excluded: it classifies as its own
// ErrorTypeRateLimit and falls through to the default, because being throttled
// says nothing about capacity.
func failoverMetric(classified *gce.ClassifiedError) (string, bool) {
	if classified == nil {
		return "", false
	}
	switch classified.Type {
	case gce.ErrorTypeResourceExhausted:
		return gcecatalog.ZoneMetricCapacity, true
	case gce.ErrorTypeQuotaCPU:
		return gcecatalog.ZoneMetricQuotaCPU, true
	case gce.ErrorTypeQuotaGPU:
		return gcecatalog.ZoneMetricQuotaGPU, true
	case gce.ErrorTypeQuotaIP:
		return gcecatalog.ZoneMetricQuotaIP, true
	case gce.ErrorTypeQuotaSSD:
		return gcecatalog.ZoneMetricQuotaSSD, true
	case gce.ErrorTypeQuotaHyperdisk:
		return gcecatalog.ZoneMetricQuotaHyperdisk, true
	case gce.ErrorTypeQuotaInstance:
		return gcecatalog.ZoneMetricQuotaInstance, true
	case gce.ErrorTypeZoneDown, gce.ErrorTypeZoneNotFound,
		gce.ErrorTypeMachineTypeNotInZone, gce.ErrorTypeGPUNotInZone:
		return gcecatalog.ZoneMetricAvailability, true
	default:
		return "", false
	}
}

// errFailoverSuperseded aborts a zone move whose host a concurrent transition
// (a delete, a completed provision) already took past pre-serving state.
var errFailoverSuperseded = errors.New("zone failover superseded by a concurrent transition")

// FailoverZone reacts to one classified provisioning failure: it records the
// failure in zone_health, which is shared placement memory, so every pending
// provision steers away immediately, pinned hosts included. When the ladder
// still has attempts and the host is not pinned, it also moves the host to the
// best other ranked zone before the next delivery. The move is pure intent
// mutation (a pre-serving host has no devices and no Host Orchestrator),
// CAS-guarded on pending|provisioning, and bumps the incarnation so the new
// zone's instance name can never collide with anything the old zone holds.
// Never returns an error: the provisioning error it reacted to is the one the
// caller must surface, and the ladder's redelivery is the retry.
func (s *HostService) FailoverZone(ctx context.Context, hostID string, cause error, moveAllowed bool) {
	classified := gce.ClassifyError(cause)
	bindingMetric, actionable := failoverMetric(classified)
	if !actionable {
		return
	}
	h, err := s.hostRepo.Get(ctx, hostID)
	if err != nil {
		if !errors.Is(err, host.ErrNotFound) {
			s.logger.Warn("failover: loading host failed", slog.String("host_id", hostID), slog.Any("error", err))
		}
		return
	}
	if h.Status != host.StatusPending && h.Status != host.StatusProvisioning {
		return // the failure lost a race with a delete or a completed provision
	}

	// Before the zone-health record and the move, because this is the write
	// that takes the host out of placement's candidate set. The cooldown
	// steers NEW provisions away from the cell; this keeps new creates off
	// THIS host, which the cooldown cannot do, since placement reads host
	// rows and not zone cells. A create placed here now would wait out the
	// whole retry ladder instead of provisioning a shape with capacity.
	s.markProvisionBlocked(ctx, h, classified.Message)

	family := s.machineFamilyFor(ctx, h.MachineType)
	if s.zoneHealth != nil {
		cell, err := s.zoneHealth.RecordFailure(ctx, h.Zone, family, bindingMetric, classified.Message)
		if err != nil {
			s.logger.Warn("failover: recording zone health failed",
				slog.String(attrZone, h.Zone), slog.Any("error", err))
		} else {
			s.fileStockoutReprobe(ctx, h, bindingMetric, cell)
		}
	}
	if !moveAllowed || h.ZonePinned || s.catalogValidator == nil {
		return // cooldown recorded; a pinned host retries in place and reports the real failure
	}

	s.reHome(ctx, h, bindingMetric)
}

// markProvisionBlocked stamps the classified capacity failure on the host row,
// CAS-guarded on pending|provisioning like every other failover write: a host
// a concurrent transition took past pre-serving state serves creates, and must
// not be fenced by a failure that arrived late. Best effort, because the
// provisioning error it reacts to is the outcome the caller surfaces; a lost
// write costs one more create parked behind this host, never a wrong host
// state. The reason is capped by the domain (capReason) to the column width.
func (s *HostService) markProvisionBlocked(ctx context.Context, h *host.Host, reason string) {
	if _, err := s.hostRepo.Mutate(ctx, h.ID, func(cur *host.Host) error {
		if cur.Status != host.StatusPending && cur.Status != host.StatusProvisioning {
			return errFailoverSuperseded
		}
		cur.MarkProvisionBlocked(reason)
		return nil
	}); err != nil && !errors.Is(err, errFailoverSuperseded) {
		s.logger.Warn("failover: recording the provision-blocked condition failed",
			slog.String("host_id", h.ID), slog.Any("error", err))
	}
}

// fileStockoutReprobe asks for a fresh Spot capacity-advice reading of the one
// cell this failure just proved empty, so ranking repels on a measurement
// rather than waiting out the sync interval.
//
// Two conditions, and both are the bound rather than a filter. Only a stockout:
// the advice API answers "is Spot obtainable here", which a quota refusal or a
// missing shape says nothing about. And only the failure that OPENED the cell:
// the cooldown row the failure just wrote counts consecutive failures, and it
// reads 1 exactly once per cooldown episode, so a capacity storm hitting one
// cell fifty times files one probe, with no second table and no timer to keep.
func (s *HostService) fileStockoutReprobe(
	ctx context.Context, h *host.Host, bindingMetric string, cell *gcecatalog.ZoneHealth,
) {
	if s.stockoutReprober == nil || bindingMetric != gcecatalog.ZoneMetricCapacity {
		return
	}
	if cell == nil || cell.ConsecutiveFailures != 1 || h.MachineType == "" {
		return
	}
	t := task.New(task.TypeCatalogReprobeSpotAdvice, h.Zone+"/"+h.MachineType)
	// Below user intent: a refreshed score steers the next create, and holding
	// one back to take it would be the advisory signal outranking the work.
	t.Priority = task.PriorityResync
	if err := s.stockoutReprober.Enqueue(ctx, t); err != nil {
		s.logger.Warn("failover: filing the capacity-advice re-probe failed",
			slog.String(attrZone, h.Zone), slog.String(attrMachineType, h.MachineType),
			slog.Any("error", err))
	}
}

// reHome moves a failed provisioning host somewhere it might succeed: down the
// shape ladder when this shape has no zone left, else across to the best other
// ranked zone.
//
// The two axes in one place because their order is the whole walk. Zones are
// the inner loop, so the shape only steps once every zone ranked for it is out
// of capacity. Only a stockout can step it, because a quota error, a refused
// permission or a bad config would otherwise turn a misconfiguration into a
// silent shape change.
func (s *HostService) reHome(ctx context.Context, h *host.Host, bindingMetric string) {
	candidates, err := s.catalogValidator.RankZones(ctx, gcecatalog.ValidationRequest{
		MachineType:       h.MachineType,
		GPUType:           h.GPUType,
		GPUCount:          h.GPUCount,
		ProvisioningModel: string(h.ProvisioningModel),
	}, gcecatalog.RankOptions{PreferredRegion: preferredRegionOf(h.RequestedZone)})
	if err != nil {
		s.logger.Warn("failover: ranking zones failed", slog.String("host_id", h.ID), slog.Any("error", err))
		return
	}
	if bindingMetric == gcecatalog.ZoneMetricCapacity && s.stepShapeLadder(ctx, h, candidates) {
		return
	}
	if len(candidates) == 0 || candidates[0].Zone == h.Zone {
		return // nowhere better; the retry ladder tries again in place
	}
	s.moveHostZone(ctx, h, candidates[0], bindingMetric)
}

// ridesShapeLadder reports whether this host's machine shape is the fleet's to
// choose. The ladder is fleet policy, so it governs the hosts the fleet
// provisioned for itself: a declarative create that found no capacity, and a
// keeper top-up. A host somebody asked for by shape keeps the shape they asked
// for; so does a batch's, whose template was sized for the group it carries.
//
// A host with an accelerator attached is out too: the ladder is the CPU path's,
// and swapping a GPU host onto a CPU rung would answer a stockout by silently
// removing the capability the host exists for.
func ridesShapeLadder(h *host.Host) bool {
	return (h.Origin == host.OriginAuto || h.Origin == host.OriginPool) &&
		h.GPUType == "" && h.GPUCount == 0
}

// stepShapeLadder is the walk's outer loop: when every zone ranked for this
// host's shape is out of capacity, move it onto the next shape the operator
// listed. It reports whether it moved the host.
//
// Its position on the ladder is read from the host's own machine type every
// time, never remembered. A pointer that outlived its stockout would demote the
// fleet until somebody noticed; deriving it means the walk always starts from
// the operator's preference and re-proves the step from observations that
// decay.
//
// The statement count is fixed whatever the ladder holds: one policy read, one
// catalog listing, one stockout-memory read covering every rung's family at
// once, and one ranking for the rung it steps to. Every skip in between is
// decided in memory.
func (s *HostService) stepShapeLadder(ctx context.Context, h *host.Host, candidates []gcecatalog.ZoneCandidate) bool {
	if !ridesShapeLadder(h) || s.zoneHealth == nil || s.machineTypes == nil {
		return false
	}
	rungs, rows := s.shapeLadder(ctx, h.Pool)
	at := hostconfig.LadderIndex(rungs, h.MachineType)
	if at < 0 || at+1 >= len(rungs) {
		// No ladder, a shape that has left it, or the last rung: the zone walk
		// is the whole answer, exactly as it was before the ladder existed.
		return false
	}

	cells, err := s.zoneHealth.ListCapacityCells(ctx, hostconfig.LadderFamilies(rungs))
	if err != nil {
		s.logger.Warn("failover: stockout memory unavailable; staying on this shape",
			slog.String("host_id", h.ID), slog.Any("error", err))
		return false
	}
	pool := make([]string, len(candidates))
	for i, c := range candidates {
		pool[i] = c.Zone
	}
	stocked := gcecatalog.StockedOutZones(cells, time.Now())
	if !rungs[at].StockedOut(pool, stocked) {
		return false // a zone is still worth trying for the shape we have
	}
	next, ok := hostconfig.FirstProbableRung(rungs, at+1, h.MaxDevices, pool, stocked)
	if !ok {
		return false // the ladder is exhausted; the terminal error is the right answer
	}

	rung := rungs[next]
	ranked, err := s.catalogValidator.RankZones(ctx, gcecatalog.ValidationRequest{
		MachineType:       rung.MachineType,
		ProvisioningModel: string(h.ProvisioningModel),
	}, gcecatalog.RankOptions{PreferredRegion: preferredRegionOf(h.RequestedZone)})
	if err != nil || len(ranked) == 0 {
		s.logger.Warn("failover: the next shape ranks nowhere; staying on this one",
			slog.String("host_id", h.ID), slog.String("next_machine_type", rung.MachineType),
			slog.Any("error", err))
		return false
	}
	return s.moveHostShape(ctx, h, rows[rung.MachineType], ranked[0])
}

// shapeLadder reads the machine-shape preference order of the pool this host
// belongs to, admitted against the catalog. One policy read, and one catalog
// listing only when that pool has a ladder stored.
//
// The host's own pool, because a rung is admissible only in the pool that owns
// it: stepping a machine onto another pool's preferred shape would move it out
// of the population its pool's bounds are counted against. Named hostPool, not
// pool: the walk above holds a ranked ZONE pool under that name, and the two
// meet in this file.
func (s *HostService) shapeLadder(ctx context.Context, hostPool string) ([]hostconfig.LadderRung, map[string]*machinetype.MachineType) {
	if s.systemConfig == nil {
		return nil, nil
	}
	cfg, err := s.systemConfig.Get(ctx, models.ConfigKeyFleetPoolLadders)
	if err != nil {
		if !errors.Is(err, spannerplatform.ErrKeyNotFound) {
			s.logger.Warn("failover: reading the shape ladders failed", slog.Any("error", err))
		}
		return nil, nil
	}
	ladders, ok := hostconfig.ParsePoolLadders(cfg.AsString())
	if !ok {
		s.logger.Warn("failover: the stored shape ladders are unreadable; staying on this shape")
		return nil, nil
	}
	pool := host.NormalizePool(hostPool)
	stored := ladders[pool]
	if len(stored) == 0 {
		return nil, nil
	}
	catalog, err := s.machineTypes.List(ctx, machinetype.ListFilter{})
	if err != nil {
		s.logger.Warn("failover: listing machine types for the shape ladder failed", slog.Any("error", err))
		return nil, nil
	}
	rows := make(map[string]*machinetype.MachineType, len(catalog))
	for _, mt := range catalog {
		rows[mt.ID] = mt
	}
	return hostconfig.ResolveLadder(pool, stored, catalog), rows
}

// moveHostShape commits a rung step: CAS the host onto the next shape and its
// ranked zone with a fresh incarnation, then best-effort-clean the zone it
// came from.
//
// The incarnation bump matters here. The shape is applied at incarnation birth
// (the boot disk type and the host image are both resolved from the machine
// type at provision time), so a new shape needs a new instance, and the new
// name is what keeps whatever the old attempt left behind from colliding with
// it.
func (s *HostService) moveHostShape(ctx context.Context, h *host.Host, mt *machinetype.MachineType, best gcecatalog.ZoneCandidate) bool {
	if mt == nil {
		return false
	}
	fromZone, fromInstance, fromType := h.Zone, h.GCEInstanceName, h.MachineType
	updated, err := s.hostRepo.Mutate(ctx, h.ID, func(cur *host.Host) error {
		if (cur.Status != host.StatusPending && cur.Status != host.StatusProvisioning) ||
			cur.MachineType != fromType {
			return errFailoverSuperseded
		}
		cur.MachineType = mt.ID
		cur.Zone = best.Zone
		cur.Incarnation++
		cur.GCEInstanceName = host.InstanceName(cur.Name, cur.ID, cur.Incarnation)
		return nil
	})
	if err != nil {
		if errors.Is(err, errFailoverSuperseded) {
			s.logger.Info("shape step superseded by a concurrent transition", slog.String("host_id", h.ID))
		} else {
			s.logger.Error("shape step write failed", slog.String("host_id", h.ID), slog.Any("error", err))
		}
		return false
	}

	s.logger.Info("host fell back to another machine shape",
		slog.String("host_id", h.ID),
		slog.String("preferred_machine_type", fromType),
		slog.String(attrMachineType, mt.ID),
		slog.String("from_zone", fromZone),
		slog.String(attrZone, best.Zone),
		slog.Any("ranking", best.Reasons))
	_, failover := placementInstruments()
	failover.Add(ctx, 1, metric.WithAttributes(
		attribute.String("from", fromZone),
		attribute.String("to", best.Zone),
		attribute.String("reason", gcecatalog.ZoneMetricCapacity)))
	if s.auditLogger != nil {
		// The shape it wanted goes in beside the shape it got. The host row
		// carries only the shape it ended on, so without both the entry cannot
		// be told from an ordinary create on that shape.
		s.auditLogger.LogAsync(audit.New(audit.ActionHostShapeFallback, audit.ResourceHost).
			WithResource(h.ID).
			WithMetadata(map[string]any{
				attrPreferredType: fromType, attrMachineType: mt.ID,
				"from_zone": fromZone, attrZone: best.Zone,
				attrNewInstance: updated.GCEInstanceName,
			}).
			Build())
	}
	// The old zone's instance is targeted by its exact old coordinates, the
	// same reason moveHostZone does it: the next provision attempt's
	// predecessor delete looks in the NEW zone and would miss it.
	s.deleteStrayInstance(ctx, fromZone, fromInstance, h.ID)
	return true
}

// moveHostZone commits the failover: CAS the host onto the ranked zone with a
// fresh incarnation, then best-effort-clean the old zone.
func (s *HostService) moveHostZone(ctx context.Context, h *host.Host, best gcecatalog.ZoneCandidate, bindingMetric string) {
	fromZone, fromInstance := h.Zone, h.GCEInstanceName
	updated, err := s.hostRepo.Mutate(ctx, h.ID, func(cur *host.Host) error {
		if (cur.Status != host.StatusPending && cur.Status != host.StatusProvisioning) ||
			cur.Zone != fromZone {
			return errFailoverSuperseded
		}
		cur.Zone = best.Zone
		// Fresh incarnation: the instance name changes, so nothing the old
		// zone still holds (a half-created VM, a pending insert) can ever
		// collide with the new zone's insert, and the orphan sweep deletes
		// the old name as a stale-incarnation husk across all zones.
		cur.Incarnation++
		cur.GCEInstanceName = host.InstanceName(cur.Name, cur.ID, cur.Incarnation)
		return nil
	})
	if err != nil {
		if errors.Is(err, errFailoverSuperseded) {
			s.logger.Info("zone failover superseded by a concurrent transition", slog.String("host_id", h.ID))
		} else {
			s.logger.Error("zone failover write failed", slog.String("host_id", h.ID), slog.Any("error", err))
		}
		return
	}

	s.logger.Info("host failed over to another zone",
		slog.String("host_id", h.ID),
		slog.String("from_zone", fromZone),
		slog.String("to_zone", best.Zone),
		slog.String("reason", bindingMetric),
		slog.Any("ranking", best.Reasons))
	_, failover := placementInstruments()
	failover.Add(ctx, 1, metric.WithAttributes(
		attribute.String("from", fromZone),
		attribute.String("to", best.Zone),
		attribute.String("reason", bindingMetric)))
	if s.auditLogger != nil {
		s.auditLogger.LogAsync(audit.New(audit.ActionHostZoneFailover, audit.ResourceHost).
			WithResource(h.ID).
			WithMetadata(map[string]any{
				"from": fromZone, "to": best.Zone, "reason": bindingMetric,
				attrNewInstance: updated.GCEInstanceName,
			}).
			Build())
	}

	// Usually a no-op (a stockout-failed insert creates nothing); covers the
	// insert-succeeded/wait-failed edge. The predecessor delete on the next
	// provision attempt looks in the NEW zone and would miss this instance,
	// so it is targeted here by its exact old coordinates; the cross-zone
	// orphan sweep is the durable cleanup.
	s.deleteStrayInstance(ctx, fromZone, fromInstance, h.ID)
}

// machineFamilyFor resolves the machine family the zone_health cell is keyed
// by, matching how ranking resolves the shape: an empty machine type means
// the catalog default.
func (s *HostService) machineFamilyFor(ctx context.Context, machineType string) string {
	if machineType == "" && s.machineTypes != nil {
		if mt, err := s.machineTypes.GetDefault(ctx); err == nil && mt != nil {
			if mt.Family != "" {
				return mt.Family
			}
			machineType = mt.ID
		}
	}
	return machinetype.FamilyOf(machineType)
}

// preferredRegionOf compiles a persisted request token into the ranking hint:
// a region passes through, a zone token (single-letter final segment, the /v1
// vocabulary) contributes its region, absent means none.
func preferredRegionOf(requested string) string {
	if requested == "" {
		return ""
	}
	if i := strings.LastIndex(requested, "-"); i >= 0 && len(requested)-i == 2 {
		return gcecatalog.RegionFromZone(requested)
	}
	return requested
}
