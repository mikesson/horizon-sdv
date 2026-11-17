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
	"strings"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// Orphan-VM sweep bounds. The grace window is the correctness guard: a host
// row always exists BEFORE its instance is inserted (the create is an atomic
// row+task write), so a managed VM younger than the window is never an
// orphan candidate. It only looks unclaimed when the row read raced the
// listing. Deletions are capped per cycle (blast radius). The cap is a
// process-local counter, so concurrently sweeping instances (a leadership
// handoff overlap) briefly share no budget and can delete up to the cap
// EACH. Accepted: a fired delete leaves no durable row to count (unlike the
// lost-mark budget, which counts its tombstones), and every candidate is an
// unclaimed instance past the grace window, so the cap paces a wanted action
// rather than guarding a destructive misread.
const (
	orphanGraceWindow          = 15 * time.Minute
	maxOrphanVMDeletesPerCycle = 2
)

// mayDeleteOrphans answers whether this cycle may delete what it finds. A
// failed read stays detect-only: a Spanner blip must never be the reason a
// loop starts deleting VMs.
func (s *Service) mayDeleteOrphans(ctx context.Context) bool {
	if s.deletePolicy == nil {
		return s.config.DeleteOrphanVMs
	}
	allowed, err := s.deletePolicy(ctx)
	if err != nil {
		s.logger.Warn("reading the orphan-VM deletion policy failed; staying detect-only",
			slog.Any("error", err))
		return false
	}
	return allowed
}

// sweepOrphanVMs lists every managed instance in the project and flags those
// no live host row claims. A legitimate instance always has a row older than
// itself (the create commits the row before inserting the VM), so rows are
// read AFTER the listing and anything younger than orphanGraceWindow is
// skipped. The only orphan candidates are instances that have been
// unclaimed for longer than any create could explain. Dry-run by default:
// detection logs and gauges; deletion requires the runtime policy and is
// capped per cycle.
func (s *Service) sweepOrphanVMs(ctx context.Context) error {
	if s.client == nil {
		return nil // no GCE boundary wired (mirrors the checkPreemptions guard)
	}
	mayDelete := s.mayDeleteOrphans(ctx)
	instancesByZone, err := s.listAllManagedInstances(ctx)
	if err != nil {
		return fmt.Errorf("aggregated instance listing: %w", err)
	}

	hosts, err := s.hostRepo.List(ctx, host.ListFilter{IncludeDeleted: true})
	if err != nil {
		return fmt.Errorf("listing host rows: %w", err)
	}
	claims := buildHostClaims(hosts)

	var orphans, deleted int64
	for zone, instances := range instancesByZone {
		for _, inst := range instances {
			name := inst.GetName()
			if claims.claimed[name] {
				continue
			}
			created, perr := time.Parse(time.RFC3339, inst.GetCreationTimestamp())
			if perr != nil || time.Since(created) < orphanGraceWindow {
				continue
			}
			husk := claims.staleHusk[name]
			orphans++
			s.logger.Warn("orphan VM detected: managed instance with no live host row",
				slog.String("instance", name),
				slog.String("zone", zone),
				slog.Duration("age", time.Since(created).Round(time.Second)),
				slog.Bool("tombstone_claimed", claims.tombstoned[name]),
				slog.Bool("stale_incarnation_husk", husk),
				slog.Bool("deletion_enabled", mayDelete || husk),
			)
			if !mayDelete && !husk {
				continue
			}
			if deleted >= maxOrphanVMDeletesPerCycle {
				s.remediationDeferred.Add(1)
				continue
			}
			if s.deleteOrphanVM(ctx, zone, name) {
				deleted++
			}
		}
	}
	s.orphanVMs.Store(orphans)
	return nil
}

// hostClaims indexes the host rows' view of instance names for the orphan
// judgment.
type hostClaims struct {
	claimed    map[string]bool
	tombstoned map[string]bool
	staleHusk  map[string]bool
}

func buildHostClaims(hosts []*host.Host) hostClaims {
	c := hostClaims{
		claimed:    make(map[string]bool, len(hosts)),
		tombstoned: make(map[string]bool),
		staleHusk:  make(map[string]bool),
	}
	for _, h := range hosts {
		if h.GCEInstanceName == "" {
			continue
		}
		if h.Status == host.StatusDeleted {
			c.tombstoned[h.GCEInstanceName] = true
			continue
		}
		c.claimed[h.GCEInstanceName] = true
		// Every EARLIER incarnation's name is CCO's own structural garbage:
		// the names are deterministic (host.InstanceName), and once the row
		// claims a later incarnation, no flow will ever visit the old
		// instance again. These are deleted even in dry-run mode: expected
		// husks are not the anomaly the dry-run default protects operators
		// from, and leaving them runs up real cost.
		for inc := range h.Incarnation {
			c.staleHusk[host.InstanceName(h.Name, h.ID, inc)] = true
		}
	}
	return c
}

// deleteOrphanVM fires one orphan delete without waiting on the operation:
// the next sweep re-observes, and a failed delete just logs and retries next
// cycle.
func (s *Service) deleteOrphanVM(ctx context.Context, zone, name string) bool {
	if _, err := s.client.Delete(ctx, &computepb.DeleteInstanceRequest{
		Project:  s.config.ProjectID,
		Zone:     zone,
		Instance: name,
	}); err != nil {
		s.logger.Error("failed to delete orphan VM",
			slog.String("instance", name), slog.String("zone", zone), slog.Any("error", err))
		return false
	}
	s.orphanVMsDeleted.Add(1)
	s.logger.Warn("orphan VM deleted",
		slog.String("instance", name), slog.String("zone", zone))
	return true
}

// listAllManagedInstances returns the instances across every zone in the
// project that carry the managed name prefix and every one of this install's
// labels, keyed by zone name. Another install's hosts in the same project
// share the prefix and managed-by but not cco-deployment, so they never reach
// the orphan judgment.
func (s *Service) listAllManagedInstances(ctx context.Context) (map[string][]*computepb.Instance, error) {
	filter := s.managedNameFilter()
	it := s.client.AggregatedList(ctx, &computepb.AggregatedListInstancesRequest{
		Project: s.config.ProjectID,
		Filter:  &filter,
		// Partial success is SAFE for this consumer and strictly better for
		// availability: orphan detection is presence-based, since an instance
		// must be LISTED to be judged unclaimed (the claimed-set comes from
		// Spanner; absence decisions use point reads elsewhere), so an
		// omitted unreachable zone can only make the sweep MISS orphans for a
		// cycle, never fabricate one. Do not flip this without re-deriving
		// that argument for every consumer of the listing.
		ReturnPartialSuccess: new(true),
	})
	out := make(map[string][]*computepb.Instance)
	for {
		pair, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("iterating aggregated instances: %w", err)
		}
		zone := strings.TrimPrefix(pair.Key, "zones/")
		for _, inst := range pair.Value.GetInstances() {
			if s.hasRequiredLabels(inst.GetLabels()) {
				out[zone] = append(out[zone], inst)
			}
		}
	}
	return out, nil
}
