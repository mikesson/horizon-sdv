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
	"maps"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// This file is the lease-shortening cluster: which hosts a lease could be taken
// back from, and the guarded write that takes it. Two populations reach that one
// write, the fleet's standing surplus and empty dedicated hosts, because they
// are chosen for opposite reasons and reclaimed the same way.
//
// Every write here moves an expiry BACKWARD, the direction that can strand a
// promise already made, so it carries a guard the renewal side has no need of,
// and answers to a mode the renewal side does not.
//
// The reaper stays the sole executioner. Nothing here deletes anything; a
// shortened lease simply reaches the reaper sooner.

// paramSettled bounds how young a host may be and still be read as drained.
const paramSettled = "settled"

// liveDeviceExclusions is the NOT-IN set that separates a tenant from a dead
// row: an error row is a failed create with no CVD behind it, which is poison
// evidence rather than a tenant, and a deleting one is already going.
//
// One constant because both statements below read it (the write's guard and
// the collapse's emptiness term), and the guard is what makes the listing safe.
// Two copies could disagree about what "empty" means, and the disagreement would
// show up only as a host reclaimed while something was still running on it.
const liveDeviceExclusions = `('error', 'deleting', 'deleted')`

// liveDeviceCountSQL counts the tenants a shorten must not strand.
const liveDeviceCountSQL = `SELECT COUNT(*) FROM devices
	      WHERE host_id = @host_id AND status NOT IN ` + liveDeviceExclusions

// shortenableSQL selects the standing fleet inventory a shorten could reclaim,
// coldest first, and at most the budget's worth in each pool.
//
// It is the standing inventory itself, the very hosts the keeper counts toward
// its target, under the same idle terms and the same lease-holds expression,
// because surplus is by definition inventory there is too much of. It is
// narrowed three ways and then ordered as the exact mirror of rescuableSQL:
// warmth ascending with the nearest expiry breaking the tie, against that one's
// warmth descending with the longest. One ordering read from both ends, because
// which host to keep and which to let go are one decision.
//
// Ranked per pool because the surplus is per pool: a fleet with one pool deep in
// surplus and another exactly met would otherwise spend the whole budget on rows
// the caller has to throw away, and would do it from the top of a global order
// the second pool never appears in.
//
// The three narrowings:
//
//   - the fleet class, so nothing a user or a batch created can ever be offered
//     as the fleet's surplus;
//   - ephemeral and unprotected, which are the terms the REAPER itself trusts
//     (see ListExpired). The lease-holds expression admits expires_at IS NULL and
//     NULL is the protected encoding, so without this a pinned host is a
//     candidate whose only defense is the live-device guard inferring protection
//     three steps later, through code this statement does not own. Filtered here,
//     a NULL lease that still reaches the write means drift with nothing pinned
//     on it, and repairing that is unambiguous;
//   - old enough that emptiness means drained rather than still filling.
//
// What is NOT filtered: hosts whose remaining lease is already shorter than the
// grace a shorten would leave. The lease-holds expression requires a lease of at
// least the provisioning lead, and the drain grace is bounded strictly below that
// lead, so every candidate has more lease than the write will leave it. That
// bound is what stops this loop walking the whole fleet, and it is the reason it
// is stated as a hard maximum rather than left to taste.
func shortenableSQL() (string, map[string]any) {
	predicate, params := classFilter(host.ClassFleet)
	maps.Copy(params, poolParams())
	return perPoolOrdered(idleFleetPredicate+`
	        AND `+leaseHoldsExpr+predicate+`
	        AND lifecycle_mode = 'ephemeral'
	        AND protected_device_count = 0
	        AND created_at < @`+paramSettled,
		`warmth ASC, expires_at ASC`), params
}

// ListShortenable returns each pool's surplus candidates, coldest first.
//
// perPool is the deepest surplus any single pool has to answer, so a caller
// takes what that pool needs from its own rows and leaves the rest. The bound is
// per pool rather than total because a total would be spent by whichever pool
// sorted first.
//
// lead is the standby lease margin, so the candidate set is exactly the
// inventory StandbyInventory counted as standby. A host this returns is a host
// the keeper is currently counting toward that pool's target, which is what
// makes subtracting one from the other meaningful.
func (r *HostRepository) ListShortenable(ctx context.Context, lead time.Duration, perPool int) ([]*host.Host, error) {
	sql, params := shortenableSQL()
	maps.Copy(params, standbyParams(lead))
	params[paramSettled] = time.Now().Add(-host.MinShortenAge)
	candidates, err := spannerplatform.QueryAll(r.client.Single().Query(ctx, spanner.Statement{SQL: sql, Params: params}),
		"shortenable fleet hosts", (*models.HostRow).ToDomain)
	if err != nil {
		return nil, err
	}
	return capPerPool(candidates, perPool), nil
}

// collapsibleSQL selects the empty dedicated hosts a collapse could reclaim,
// longest remaining lease first.
//
// A dedicated host is one the orchestrator fenced for a single group. Placement
// can never use it again, so once the group is gone it is pure cost, and nothing
// else in the system reclaims it: it is excluded from standby by its own
// exclusivity, which puts it outside the fleet's inventory, its outflow, and
// both of the statements above. Its only other end is the full TTL.
//
// It is NOT built on standbyTerms, and the two terms it does not inherit are
// the reason:
//
//   - placement-eligible statuses. That set answers "may a device land here",
//     which is a question this host has already been fenced out of. What matters
//     is only that the row is not already on its way out, which is the reaper's
//     own term.
//   - degraded_at IS NULL. A degraded host is one placement refuses, so a
//     degraded EMPTY dedicated host is the most expensive row this statement can
//     ever see: nothing will place on it, nothing else reclaims it, and it bills
//     its whole lease. Inheriting the inventory term would skip exactly that
//     host.
//
// What it does state, because it has no parent to inherit it from:
//
//   - the lease floor. The shorten above gets this free, because standbyTerms
//     requires a lease of at least the provisioning lead, and the drain grace is
//     bounded strictly below that lead, so its candidates always have more lease
//     than the write will leave. Here, without the floor, a host this cycle collapsed is
//     offered again next cycle and every cycle after, spending the per-cycle
//     budget on writes ShortenLease refuses. NULL is admitted alongside, because
//     an unprotected NULL lease is drift rather than a short one and a bare
//     comparison is NULL for those rows, dropping the one case that needs
//     repairing;
//   - emptiness from live device ROWS rather than current_device_count. The
//     counter includes error rows, so a group whose devices all failed leaves a
//     host that is unplaceable forever and invisible to a counted predicate,
//     which is the case most likely to produce one of these. That does not reopen
//     the standby inventory's settled choice: standby membership counts
//     the column because placement admits on the column, and this host is never
//     admitted to anything. ListEmptyBatchHosts reads its own population the same
//     way for the same reason.
//
// Named for the class and never for exclusivity alone: exclusive is a field
// users set to fence a host from placement, so a user's exclusive host derives
// reserved and must never be collapsed, and an exclusive batch host is batch
// settlement's.
func collapsibleSQL() (string, map[string]any) {
	predicate, params := classFilter(host.ClassDedicated)
	return `SELECT ` + hostColumnsSQL + ` FROM hosts
	      WHERE status NOT IN ('deleted', 'deleting')` + predicate + `
	        AND lifecycle_mode = 'ephemeral'
	        AND protected_device_count = 0
	        AND created_at < @` + paramSettled + `
	        AND (expires_at IS NULL OR expires_at >= @` + paramCutoff + `)
	        AND NOT EXISTS (
	          SELECT 1 FROM devices d
	          WHERE d.host_id = hosts.host_id
	            AND d.status NOT IN ` + liveDeviceExclusions + `)
	      ORDER BY expires_at IS NULL DESC, expires_at DESC
	      LIMIT @` + paramLimit, params
}

// ListCollapsible returns the empty dedicated hosts worth reclaiming, longest
// remaining lease first.
//
// The ordering decides the saving. Warmth decides the other two directions, and here
// it would decide nothing: the warm plane bakes onto standby hosts, which are
// shareable by definition, so no dedicated host ever holds a ledger row and
// every candidate would tie, leaving a limited query to return an arbitrary
// subset. What differs between these hosts is how much lease is left to give
// back, so the most expensive goes first.
//
// A leaseless host leads, and that is the same rule rather than an exception to
// it. NULL on an unprotected ephemeral host is drift, and the reaper requires a
// lease, so nothing in the system will ever collect it: its remaining lease is
// unbounded, which makes it the largest saving here and the only place it can be
// repaired. Sorted by expiry alone it would trail every real candidate, because
// GoogleSQL orders NULL last under DESC, and a steady supply of ordinary ones
// would starve it out of a budgeted query indefinitely.
//
// lead is the same margin the shorten uses, and it is the lease floor here: the
// drain grace a collapse writes is bounded strictly below it, so every candidate
// has more lease than the write will leave.
func (r *HostRepository) ListCollapsible(ctx context.Context, lead time.Duration, limit int) ([]*host.Host, error) {
	sql, params := collapsibleSQL()
	params[paramSettled] = time.Now().Add(-host.MinShortenAge)
	params[paramCutoff] = time.Now().Add(lead)
	params[paramLimit] = int64(limit)
	return spannerplatform.QueryAll(r.client.Single().Query(ctx, spanner.Statement{SQL: sql, Params: params}),
		"collapsible dedicated hosts", (*models.HostRow).ToDomain)
}

// ShortenLease lowers one host's lease to target, committing extra in the same
// transaction. It reports whether it wrote.
//
// The three refusals, each re-read under the transaction rather than trusted
// from the listing:
//
//   - a live device exists. This is the guard, and it is the ONLY thing keeping
//     the promise: a device commit raises its host's expiry to the device's own
//     expiry plus the teardown grace, and a shorten arriving afterwards would
//     compute a target far below that and lower it. Writing the smaller of the
//     two values does not help, because the smaller one is the wrong one.
//     Counted from device ROWS and never from current_device_count, which is the
//     column the listing already filtered on: a guard reading the same column
//     agrees with the selection by construction and can only fail where the
//     counter has drifted, which is the one case it exists to catch.
//   - the host is protected. The listing excludes these, but a timestamp written
//     onto a protected host's NULL lease converts it into an expiring one, so the
//     encoding is re-checked where the write is.
//   - the lease is already at or below the target. A NULL lease is NOT this
//     case: NULL on an unprotected host means no lease at all rather than a short
//     one, and it is written down to the target.
//
// The host's age is NOT re-checked. It can only have grown since the listing,
// so the guard cannot go stale in the direction that matters.
//
// A refusal returns no error. It is the ordinary outcome of a fleet that moved
// while the cycle was deciding, and the next cycle re-reads the world.
func (r *HostRepository) ShortenLease(ctx context.Context, id string, target time.Time, extra ...*spanner.Mutation) (bool, error) {
	written := false
	_, err := r.mutateTx(ctx, id, func(ctx context.Context, txn *spanner.ReadWriteTransaction, h *host.Host) error {
		written = false // Reset: the transaction body may run more than once.
		live, err := spannerplatform.QueryCount(txn.Query(ctx, spanner.Statement{
			SQL:    liveDeviceCountSQL,
			Params: map[string]any{colHostID: id},
		}), "live devices on host "+id)
		if err != nil {
			return err
		}
		switch {
		case live > 0:
			return errLeaseUnchanged
		case h.Lifecycle.IsProtected():
			return errLeaseUnchanged
		case h.Lifecycle.ExpiresAt != nil && !target.Before(*h.Lifecycle.ExpiresAt):
			return errLeaseUnchanged
		}
		h.Lifecycle.ExpiresAt = &target
		written = true
		return nil
	}, extra...)
	if errors.Is(err, errLeaseUnchanged) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("shortening the lease of host %s: %w", id, err)
	}
	return written, nil
}
