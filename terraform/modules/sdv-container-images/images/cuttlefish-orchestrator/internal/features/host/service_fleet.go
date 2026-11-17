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
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// The lease-renewal entry point the fleet keeper drives. It lives on the
// service rather than the repository because the renewal is audited, and the
// audit mutation is the service's to build. The repository owns the
// transaction, the service owns what rides in it.

// Lease audit metadata. The new expiry and the host's age at the moment it was
// written, which together are the two facts that cannot be recovered once the
// row hard-deletes: how long the fleet chose to keep this host, and how close to
// the policy ceiling that choice was. A shorten adds the lease it replaced,
// because no other entry carries an expiry to compare against, so without it
// the trail cannot say whether a host was cut short or was nearly done anyway.
//
// The reclaim key itself is audit.MetaReclaim and its values are that package's
// too, because the console's activity figures read them back and platform may
// not import a feature.
//
// Neither is stamped through attrOrigin, whatever the resemblance: that key
// feeds a generated column whose index is null-filtered, so a lease write
// carrying one would enter the index the demand signal scans, where the action
// predicate excludes it again.
const (
	attrExpiresAt     = "expires_at"
	attrAgeSeconds    = "age_seconds"
	attrPrevExpiresAt = "previous_expires_at"
)

// fleetRenewalActor names the autoscaler in the trail of both lease directions.
// Informational like every actor here, and never the frozen "poolkeeper"
// literal: that string is the keeper's created_by marker, which the robustness
// harness censuses as a SQL literal, so another copy of it here would join
// those in breaking silently.
const fleetRenewalActor = "fleet-autoscaler"

// RescueFleetHosts renews the leases of draining fleet hosts, warmest first
// inside each pool, and reports how many leases it actually raised per pool.
//
// It is the scale-out verb that spends nothing: meeting a deficit from a host
// the pool already has returns warm capacity within a cycle, where a top-up
// buys a cold VM and waits a provisioning leg for it. Under sustained demand
// that is the difference between a warm standby host continuing to serve and
// being replaced by a cold one.
//
// The renewal is a fresh term of the host's OWN TTL, capped so the host cannot
// outlive host.MaxFleetRescueAge. Both halves matter: the fresh term is what
// keeps the warm host, and the cap is what keeps host images rolling through
// the fleet by attrition.
//
// deficits are the callers' per-pool gaps. There is no per-cycle cap of its
// own, and none is missing: this creates no VM and lowers no lease, and the
// deficit already bounds it, which the target's own clamp bounds in turn.
//
// ONE listing however many pools are short. The candidates come back ranked
// inside their own pool, so the deepest deficit's bound is what the read asks
// for and each pool takes what it needed from its own rows.
func (s *HostService) RescueFleetHosts(ctx context.Context, lead time.Duration, deficits map[string]int64) (map[string]int64, error) {
	rescued := make(map[string]int64, len(deficits))
	perPool := deepest(deficits)
	if perPool <= 0 {
		return rescued, nil
	}
	candidates, err := s.hostRepo.ListRescuable(ctx, lead, int(perPool))
	if err != nil {
		return rescued, fmt.Errorf("listing rescuable fleet hosts: %w", err)
	}

	now := time.Now()
	for _, h := range candidates {
		pool := host.NormalizePool(h.Pool)
		if rescued[pool] >= deficits[pool] {
			continue
		}
		target := rescueTarget(h, now)
		if target.IsZero() {
			continue
		}
		extra, err := s.leaseAuditMutation(audit.ActionHostLeaseExtend, h.ID, leaseAuditMeta(h, target, now))
		if err != nil {
			return rescued, err
		}
		written, err := s.hostRepo.ExtendLease(ctx, h.ID, target, extra...)
		if err != nil {
			// The count so far is returned WITH the error, not discarded. A
			// renewal that committed before a later one failed is inventory
			// either way, and a caller that subtracted nothing would provision
			// a replacement for a host it already has.
			return rescued, err
		}
		if !written {
			continue
		}
		rescued[pool]++
		s.logger.Info("fleet host lease renewed",
			slog.String("host_id", h.ID),
			slog.String("pool", pool),
			slog.Time("expires_at", target),
			slog.Duration("age", now.Sub(h.CreatedAt)))
	}
	return rescued, nil
}

// deepest is the largest per-pool budget in a map, which is what a per-pool
// listing has to be asked for: a bound of the deepest gap gives every pool at
// least the rows it can use, and the caller ignores the rest.
func deepest(budgets map[string]int64) int64 {
	var most int64
	for _, budget := range budgets {
		most = max(most, budget)
	}
	return most
}

// rescueTarget is the expiry a renewal would write, or the zero time when this
// host wants none.
//
// The ceiling is applied to the VALUE and not merely to eligibility, which are
// different rules with different fleets behind them. Refusing hosts already
// past host.MaxFleetRescueAge still hands a fresh full TTL to one a minute
// short of it, so hosts would live for the bound plus a whole TTL and the fleet
// would stop turning over daily, while every test written against the refusal
// still passed.
//
// The zero return covers the two cases where the ceiling has already been
// reached: a host with no lease at all (protected, which the listing excludes
// but which is not this function's assumption to make) and one whose remaining
// lease is already longer than the renewal would grant, where writing would
// LOWER it.
func rescueTarget(h *host.Host, now time.Time) time.Time {
	ttl := h.Lifecycle.TTLSeconds
	if ttl <= 0 {
		ttl = host.DefaultHostTTLSeconds
	}
	target := now.Add(time.Duration(ttl) * time.Second)
	if ceiling := h.CreatedAt.Add(host.MaxFleetRescueAge); target.After(ceiling) {
		target = ceiling
	}
	if h.Lifecycle.ExpiresAt == nil || !target.After(*h.Lifecycle.ExpiresAt) {
		return time.Time{}
	}
	return target
}

// ShortenSurplusHosts gives capacity back by lowering the leases of surplus
// fleet hosts, coldest first inside each pool, and reports how many leases it
// lowered in total.
//
// It is the only verb in the autoscaler that can shorten a host's life, which is
// why it answers to `on` alone and why the repository re-validates every write
// under its own transaction. Nothing here deletes anything: a shortened lease
// reaches the reaper sooner, so the reaper stays the only thing that deletes and
// every guard it already applies still applies.
//
// surplus is what each pool may give back, and no pool may give back another's:
// a machine spare in one pool is not capacity the other could have used, so
// letting it count would empty a pool nobody asked to shrink.
//
// baking exempts hosts the warm plane is mid-bake on, keyed host id to the
// incarnation being baked. A host mid-bake holds no artifact-ledger rows, since
// the ledger is written when the bake finishes, so it reads as the coldest host
// in the fleet and sorts first for destruction, which is exactly backwards: it
// is the host closest to becoming warm. The incarnation must match, because a
// bump is a fresh cold disk and exempting a host for a bake on a disk that is
// gone spares the wrong host. The exemption fails open (after a restart the map
// is empty) and that is affordable, because a worse victim is chosen but the
// decision to shorten something was correct either way.
func (s *HostService) ShortenSurplusHosts(ctx context.Context, lead time.Duration, surplus map[string]int64, grace time.Duration, baking map[string]int64) (int64, error) {
	perPool := deepest(surplus)
	if perPool <= 0 {
		return 0, nil
	}
	// Over-read by exactly the number of hosts that could be exempted, so a
	// pool whose coldest hosts are all mid-bake still finds the truly cold
	// ones below them instead of shortening nothing this cycle.
	candidates, err := s.hostRepo.ListShortenable(ctx, lead, int(perPool)+len(baking))
	if err != nil {
		return 0, fmt.Errorf("listing surplus fleet hosts: %w", err)
	}

	now := time.Now()
	target := now.Add(grace)
	shortened := int64(0)
	perPoolShortened := make(map[string]int64, len(surplus))
	for _, h := range candidates {
		pool := host.NormalizePool(h.Pool)
		if perPoolShortened[pool] >= surplus[pool] {
			continue
		}
		// Two-value form, because incarnation 0 is a real incarnation and a
		// one-value lookup would exempt every host absent from the map.
		if inc, ok := baking[h.ID]; ok && inc == h.Incarnation {
			continue
		}
		written, err := s.shortenOne(ctx, h, target, now, audit.ReclaimFleetSurplus)
		if err != nil {
			// The count so far is returned WITH the error, for the reason the
			// renewal does it: what committed is real, and a caller that
			// discarded it would believe the fleet still held capacity it has
			// already given away.
			return shortened, err
		}
		if !written {
			continue
		}
		shortened++
		perPoolShortened[pool]++
		s.logger.Info("fleet host lease shortened",
			slog.String("host_id", h.ID),
			slog.String("pool", pool),
			slog.Time("expires_at", target),
			slog.Duration("age", now.Sub(h.CreatedAt)))
	}
	return shortened, nil
}

// CollapseEmptyDedicated reclaims dedicated hosts whose group is gone, longest
// remaining lease first, and reports how many leases it actually lowered.
//
// A dedicated host was fenced for one group and placement can never use it
// again, so an empty one holds capacity nobody can reach and gives it back to
// nobody. Every other reclamation in this file is arithmetic against a target;
// a dedicated host is not inventory and never enters that arithmetic. An empty
// one is waste, and the only other thing that ends it is the host's full TTL.
//
// Reclaimed through the same guarded write as the surplus, and that shared write
// is what makes this safe: the collapse chooses on emptiness read a moment ago,
// and the write refuses under its own transaction if a device has landed since.
//
// budget is the caller's per-cycle cap. It carries no bake exemption, and none
// is missing: the warm plane bakes onto standby hosts, which are shareable by
// definition, so it can never be mid-bake on an exclusive one.
func (s *HostService) CollapseEmptyDedicated(ctx context.Context, lead time.Duration, budget int64, grace time.Duration) (int64, error) {
	if budget <= 0 {
		return 0, nil
	}
	candidates, err := s.hostRepo.ListCollapsible(ctx, lead, int(budget))
	if err != nil {
		return 0, fmt.Errorf("listing empty dedicated hosts: %w", err)
	}

	now := time.Now()
	target := now.Add(grace)
	collapsed := int64(0)
	for _, h := range candidates {
		written, err := s.shortenOne(ctx, h, target, now, audit.ReclaimDedicatedEmpty)
		if err != nil {
			return collapsed, err
		}
		if !written {
			continue
		}
		collapsed++
		s.logger.Info("empty dedicated host collapsed",
			slog.String("host_id", h.ID),
			slog.Time("expires_at", target),
			slog.Duration("age", now.Sub(h.CreatedAt)))
	}
	return collapsed, nil
}

// shortenOne lowers one host's lease with its audit entry riding the same
// commit, and reports whether the write happened.
//
// Shared by both reclamations rather than written twice: the entry and the write
// have to be one commit, and a second copy of that pairing is a second place for
// it to come apart. The trail would then report a reclamation that never
// occurred, of a host whose row no longer exists to contradict it.
func (s *HostService) shortenOne(ctx context.Context, h *host.Host, target, now time.Time, reclaim string) (bool, error) {
	meta := leaseAuditMeta(h, target, now)
	meta[audit.MetaReclaim] = reclaim
	if h.Lifecycle.ExpiresAt != nil {
		meta[attrPrevExpiresAt] = h.Lifecycle.ExpiresAt.UTC().Format(time.RFC3339)
	}
	extra, err := s.leaseAuditMutation(audit.ActionHostLeaseShorten, h.ID, meta)
	if err != nil {
		return false, err
	}
	return s.hostRepo.ShortenLease(ctx, h.ID, target, extra...)
}

// leaseAuditMeta is the metadata both lease directions stamp. Shared so the
// trail reads the same whichever way the lease moved; the shorten adds one key
// of its own.
func leaseAuditMeta(h *host.Host, target, now time.Time) map[string]any {
	return map[string]any{
		attrName:       h.Name,
		attrExpiresAt:  target.UTC().Format(time.RFC3339),
		attrAgeSeconds: int64(now.Sub(h.CreatedAt).Seconds()),
	}
}

// leaseAuditMutation builds a lease write's audit entry, to commit with the
// write it describes. A nil mutator leaves the write unaudited rather than
// falling back to the buffered sink, matching the create path's stance for the
// same reason: a fallback is indistinguishable from the wired path on every
// happy-path test while silently restoring the drop.
//
// It does not stamp an origin. The audit_logs origin column is generated from
// that metadata key and its index is null-filtered, so stamping one here would
// enter every lease write into the index the demand signal scans, where the
// action predicate then excludes it again.
func (s *HostService) leaseAuditMutation(action audit.Action, hostID string, meta map[string]any) ([]*spanner.Mutation, error) {
	if s.auditMutator == nil {
		return nil, nil
	}
	mut, err := s.auditMutator.EntryMutation(audit.New(action, audit.ResourceHost).
		WithActor(fleetRenewalActor).
		WithResource(hostID).
		WithMetadata(meta).
		Build())
	if err != nil {
		return nil, fmt.Errorf("building the %s audit entry: %w", action, err)
	}
	return []*spanner.Mutation{mut}, nil
}
