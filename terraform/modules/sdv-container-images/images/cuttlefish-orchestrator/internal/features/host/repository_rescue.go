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

// This file is the lease-renewal cluster: which draining fleet hosts are worth
// keeping, and the guarded write that keeps one. Every write here moves an
// expiry FORWARD. That is what makes it safe without the live-device guard a
// lowering write needs, because the promise ordering a shortened lease can
// break is not reachable from a raise.

// warmthExpr counts a host's artifact-ledger rows: how much fetched build data
// its disk is known to hold. It is the ordering both reclamation directions
// read (renew the warmest, shorten the coldest), because which host to keep
// alive and which to let go are one decision seen from opposite ends.
//
// It is not the placement plane's affinity term, which asks whether one specific
// fetch key is present. That answers "will this create skip its fetch"; this
// answers "how much would be thrown away".
//
// A host mid-bake has none of these rows, since the ledger is written when the
// bake finishes, so it sorts coldest here. That is harmless in this direction
// (it is simply renewed last) and is exactly backwards in the other, which is
// why the shortening side needs the warm plane's in-flight exemption and this
// side does not.
const warmthExpr = `(SELECT COUNT(*) FROM host_artifact_cache c WHERE c.host_id = hosts.host_id)`

// paramFloor bounds how old a host may be and still be worth renewing.
const paramFloor = "floor"

// rescuableSQL selects the draining fleet hosts a renewal could return to
// inventory, warmest first, and at most the deficit's worth in each pool.
//
// It is the outflow the keeper already counts (the same idle terms under the
// same lease-running-out expression), narrowed to the fleet class and to hosts
// young enough for the renewal to be worth writing, with the longest remaining
// lease breaking a warmth tie.
//
// Built from the shared terms rather than restating them: the two must agree
// about what "draining" means, or the keeper would publish an outflow number
// that counts hosts this statement will not consider.
//
// Ranked per pool, the mirror of the shorten's ranking, because a deficit is a
// pool's: renewing another pool's draining machine spends the deficit of a pool
// that is not short and leaves the one that is exactly as short as it was.
func rescuableSQL() (string, map[string]any) {
	predicate, params := classFilter(host.ClassFleet)
	maps.Copy(params, poolParams())
	return perPoolOrdered(idleFleetPredicate+`
	        AND `+leaseRunningOutExpr+predicate+`
	        AND created_at >= @`+paramFloor,
		`warmth DESC, expires_at DESC`), params
}

// ListRescuable returns each pool's draining hosts worth renewing, warmest
// first, at most perPool of them in any one pool.
//
// The age floor is derived from the lease margin rather than being a plain
// "older than a day" test, and the difference is a write nobody wants: a
// renewal is capped at created_at + host.MaxFleetRescueAge, so a host inside a
// lead of that ceiling can be renewed to a lease too short to count as
// inventory, every cycle, forever, changing nothing. Requiring that the
// ceiling still leave a full lead makes the candidate list exactly the hosts a
// renewal can actually return to service.
//
// What is NOT filtered here is a NULL lease. The lease-running-out expression
// already requires expires_at IS NOT NULL, and NULL is the PROTECTED encoding, so
// a protected host cannot reach this list. ExtendLease re-checks it anyway,
// because that invariant lives in a different constant.
func (r *HostRepository) ListRescuable(ctx context.Context, lead time.Duration, perPool int) ([]*host.Host, error) {
	sql, params := rescuableSQL()
	maps.Copy(params, standbyParams(lead))
	params[paramFloor] = time.Now().Add(lead).Add(-host.MaxFleetRescueAge)
	candidates, err := spannerplatform.QueryAll(r.client.Single().Query(ctx, spanner.Statement{SQL: sql, Params: params}),
		"rescuable fleet hosts", (*models.HostRow).ToDomain)
	if err != nil {
		return nil, err
	}
	return capPerPool(candidates, perPool), nil
}

// errLeaseUnchanged aborts a lease transaction, in either direction, when the
// row, re-read under it, no longer wants the write. Returned rather than
// committing a no-op, so nothing riding the same commit, the audit entry above
// all, records a change that did not happen.
var errLeaseUnchanged = errors.New("lease unchanged")

// ExtendLease raises one host's lease to target, committing extra in the same
// transaction. It reports whether it wrote.
//
// The candidate was chosen from a read old enough for the row to have moved,
// and all three ways it can move are checked here under the transaction rather
// than assumed away:
//
//   - expires_at may have become NULL, which is the protected encoding a pinned
//     device writes (host.HostLifecycle.IncrementProtectedCount clears it).
//     Writing a timestamp onto that turns a protected host into an expiring one.
//     That is a corrupted encoding rather than a dead host, since the reaper
//     filters on lifecycle_mode and protected_device_count, which is why the
//     check is worth making and why a test that only asserts the host survives
//     would pass without it.
//   - a device may have committed and raised the lease past target already, in
//     which case this write would LOWER it. Nothing in provision_only may lower
//     a lease, so the refusal is required for correctness.
//   - the host may have crossed the age ceiling since it was listed.
//
// A refusal returns no error. It is the ordinary outcome of a fleet that moved
// while the cycle was deciding, and the next cycle re-reads the world.
func (r *HostRepository) ExtendLease(ctx context.Context, id string, target time.Time, extra ...*spanner.Mutation) (bool, error) {
	written := false
	_, err := r.Mutate(ctx, id, func(h *host.Host) error {
		written = false // Reset: the transaction body may run more than once.
		switch {
		case h.Lifecycle.ExpiresAt == nil:
			return errLeaseUnchanged
		case !target.After(*h.Lifecycle.ExpiresAt):
			return errLeaseUnchanged
		case target.After(h.CreatedAt.Add(host.MaxFleetRescueAge)):
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
		return false, fmt.Errorf("extending the lease of host %s: %w", id, err)
	}
	return written, nil
}
