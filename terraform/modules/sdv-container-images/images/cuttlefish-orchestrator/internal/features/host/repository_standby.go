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
	"maps"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// idleFleetTerms is the inventory definition every statement here builds on:
// what placement counts, idle. Placement-eligible statuses (including hosts
// still provisioning, or a keeper cycle would re-provision while its own top-ups
// are in flight and overshoot the floor several-fold), with zero devices,
// shareable and undegraded.
//
// A still-provisioning host counts only while GCE has not refused it for
// capacity. The same condition placement applies, so a blocked top-up stops
// being inventory the moment its insert is turned away, and the keeper buys the
// replacement through the shape ladder that knows about the stockout instead
// of reporting a buffer no create can land on.
//
// The lease is NOT here. It is what separates standby from draining, so it
// belongs to whatever asks that question, and holding it out keeps the four
// terms the two answers share in one place.
//
// Membership stays on current_device_count, which is what placement admits on.
// The tempting symmetry with the shorten guard's live-row count is wrong in a
// way that only shows up under failure: a host holding max_devices error rows
// has no live devices but placement still refuses it, so counting it as standby
// would report a buffer that cannot take a device, and misses beside a fleet
// sitting exactly at target.
// The terms are named individually because a second consumer has to read them
// one at a time: the fleet view reports WHICH of them excluded a host, and it
// projects these very strings as columns rather than restating the predicate.
// Written out again there, the page's answer and the keeper's arithmetic would
// disagree the first time one term moved, silently, and only for the hosts the
// question is asked about.
const (
	termPlaceable = `(status IN UNNEST(@statuses)
	            AND (status IN UNNEST(@provisioned) OR provision_blocked_at IS NULL))`
	termNotDegraded = `degraded_at IS NULL`
	termShareable   = `NOT exclusive`
	termIdle        = `current_device_count = 0`
)

// standingFleetPredicate is the inventory definition without the idle test:
// hosts placement can land on, whether or not devices already fill them.
//
// Split out because the fleet's two lower bounds are held against two different
// populations (the buffer against idle hosts, the floor against every host the
// fleet is running), and both are counted from the one statement below. Written
// once so a term moving cannot move one population and not the other.
const standingFleetPredicate = termPlaceable + `
	        AND ` + termNotDegraded + `
	        AND ` + termShareable

const idleFleetPredicate = standingFleetPredicate + `
	        AND ` + termIdle

// The same two definitions with their table, for the statements that scan it
// directly. Derived rather than written again, so a term can only move in both.
const standingFleetTerms = ` FROM hosts
	      WHERE ` + standingFleetPredicate

const idleFleetTerms = ` FROM hosts
	      WHERE ` + idleFleetPredicate

// The lease half of the inventory definition, in its two directions: a lease
// ending inside the provisioning lead is reaped before a replacement could land,
// so it is not inventory a create can count on.
//
// Exact complements over the rows idleFleetTerms admits (a NULL expiry is the
// protected encoding and holds forever, everything else compares against the one
// cutoff), so every idle host is on exactly one side. Each is written once and
// read by both the count and the listing that has to agree with it: the counts
// below, ListShortenable on the holding side, ListRescuable on the other. A
// second copy with the clause swapped is what made an edit land in one answer
// and not the other.
const (
	leaseHoldsExpr      = `(expires_at IS NULL OR expires_at >= @cutoff)`
	leaseRunningOutExpr = `(expires_at IS NOT NULL AND expires_at < @cutoff)`
)

// paramPoolDefault binds the pool an unstamped row belongs to.
const paramPoolDefault = "pool_default"

// poolKeyExpr is the pool a row belongs to, as every statement that groups or
// ranks by it reads it: a column holding no name is the default pool, never a
// pool of its own. host.NormalizePool is the same rule in Go, down to the empty
// string it also folds. A row keyed on a name the Go side would fold makes a
// pool the keeper can see and can never act on, whose surplus is offered for
// reclamation and then skipped host by host. This is its second copy for the
// same reason the class expression is: the pool decides how rows are
// PARTITIONED, so it cannot be a filter applied after the fact.
//
// Written once here because the count and the listings have to agree about
// which pool a machine is in: two copies would let one statement see a pool the
// other does not, and the disagreement would surface as a pool whose deficit
// never closes.
//
// The column is table-qualified, and the grouping below repeats this whole
// expression rather than naming the `pool` alias the projection carries: a name
// in GROUP BY resolves against the table before the SELECT list, so the alias
// would lose to the raw column and the unstamped rows would form a NULL group of
// their own, two rows for the default pool, the second overwriting the first in
// the map the caller folds.
const poolKeyExpr = `IFNULL(NULLIF(hosts.pool, ''), @` + paramPoolDefault + `)`

// poolParams binds what poolKeyExpr reads.
func poolParams() map[string]any {
	return map[string]any{paramPoolDefault: host.PoolDefault}
}

// inventorySQL counts standing inventory, its outflow and the whole standing
// fleet in ONE read, per pool, narrowed to the fleet class.
//
// One GROUP BY instead of a statement per pool, so pools cost rows in one
// answer and never round trips. A read per pool
// would make the keeper's statement count a function of how many pools an
// operator configured, on a loop that runs every thirty seconds forever.
//
// The first two arms are exact complements over the idle rows (every one has a
// lease that either holds or does not, and a NULL expiry is caught by the first
// arm), so standby + draining is the whole idle fleet-class population and
// neither count can include a host the other does. That is the property two
// separate reads could not offer: each computed its own @cutoff from its own
// clock, and a host expiring between them was counted twice or lost, silently,
// in the pair of numbers published side by side to be compared.
//
// The total is a third projection of the SAME statement for the same reason
// rather than a second query, and here the stakes are higher than a compared
// pair: the fleet's two lower bounds spend against these counts in one decision,
// and two statements would let the floor and the buffer see two fleets, buying
// a host the other had just counted, or refusing to release one because two
// reads disagreed by one row.
//
// Narrowed to the class, and this is the only statement that narrows: bounds the
// keeper acts on must count only hosts the keeper manages, or an idle host
// somebody created by hand suppresses a top-up the fleet actually needs, and
// then, once reclamation lands, gets shortened as though the fleet had put it
// there. standbyListSQL below is left wide on purpose.
func inventorySQL() (string, map[string]any) {
	predicate, params := classFilter(host.ClassFleet)
	maps.Copy(params, poolParams())
	return `SELECT ` + poolKeyExpr + ` AS pool,
	        COUNTIF(` + termIdle + ` AND ` + leaseHoldsExpr + `) AS standby,
	        COUNTIF(` + termIdle + ` AND ` + leaseRunningOutExpr + `) AS draining,
	        COUNT(*) AS total` +
		standingFleetTerms + predicate + `
	      GROUP BY ` + poolKeyExpr, params
}

// StandbyInventory counts what each pool holds, what is draining out of it and
// how large it is, at one instant and against one cutoff.
//
// A pool with no rows is absent from the map rather than present at zero, which
// is what the domain's zero inventory already means.
func (r *HostRepository) StandbyInventory(ctx context.Context, lead time.Duration) (host.FleetInventory, error) {
	sql, params := inventorySQL()
	maps.Copy(params, standbyParams(lead))
	rows, err := spannerplatform.QueryAll(
		r.client.Single().Query(ctx, spanner.Statement{SQL: sql, Params: params}),
		"standby inventory", func(row *inventoryRow) inventoryRow { return *row })
	if err != nil {
		return nil, err
	}
	inventory := make(host.FleetInventory, len(rows))
	for _, row := range rows {
		inventory[row.Pool] = host.StandbyInventory{
			Standby:  row.Standby,
			Draining: row.Draining,
			Total:    row.Total,
		}
	}
	return inventory, nil
}

// inventoryRow is one pool's three-count projection of inventorySQL.
type inventoryRow struct {
	Pool     string `spanner:"pool"`
	Standby  int64  `spanner:"standby"`
	Draining int64  `spanner:"draining"`
	Total    int64  `spanner:"total"`
}

// standbyListSQL lists the inventory itself, for the warm plane's prefetch
// targets. It is NOT narrowed to the class alongside the counts, because
// prefetching onto any idle shareable host is useful whoever created it, and the
// loss would be silent: fewer hosts warmed, no error anywhere.
//
// Oldest first so the limit truncates deterministically. A fleet larger than
// the walk's cap must drop the same hosts each cycle rather than a different
// arbitrary set. The order is no longer what assigns pinned builds: the walk
// derives that from the host itself, because assignment by listing position
// reshuffled every host each time one left the listing.
//
// It applies the lease term itself rather than inheriting one: this is a list of
// hosts worth baking onto, and a host whose lease is about to run out is not.
func standbyListSQL() string {
	return `SELECT ` + hostColumnsSQL + idleFleetTerms + `
	        AND ` + leaseHoldsExpr + `
	      ORDER BY created_at ASC
	      LIMIT @limit`
}

// perPoolOrdered wraps a candidate selection so its rows arrive grouped by
// pool and, inside each pool, in the order a reclamation would take them; the
// per-pool bound is capPerPool's, applied to the scanned rows.
//
// One statement whatever the pool count. The bound is applied in Go rather
// than by a ROW_NUMBER window because the Spanner emulator, the only executed
// verification this layer has, rejects analytic functions outright, and a
// form the emulator cannot run would leave the reclamation order proven
// nowhere. The rows the window form scanned are the same rows this returns;
// only what crosses the wire grows, never the statement count.
//
// The ranking orders by how much fetched build data a host's disk holds, which
// is a subquery and has to be a projected column before ORDER BY can use it.
// The outer projection is the host columns alone, because the row scanner
// rejects a column the host row has no field for.
func perPoolOrdered(predicate, ranking string) string {
	return `SELECT ` + hostColumnsSQL + ` FROM (
	        SELECT ` + hostColumnsSQL + `,
	          ` + poolKeyExpr + ` AS pool_key,
	          ` + warmthExpr + ` AS warmth
	        FROM hosts
	        WHERE ` + predicate + `)
	      ORDER BY pool_key ASC, ` + ranking
}

// capPerPool keeps the first perPool rows of each pool, in the order the
// listing returned them, which is the bound perPoolOrdered's callers promise.
func capPerPool(hosts []*host.Host, perPool int) []*host.Host {
	kept := make([]*host.Host, 0, len(hosts))
	taken := make(map[string]int, len(hosts))
	for _, h := range hosts {
		pool := host.NormalizePool(h.Pool)
		if taken[pool] >= perPool {
			continue
		}
		taken[pool]++
		kept = append(kept, h)
	}
	return kept
}

func standbyParams(lead time.Duration) map[string]any {
	return map[string]any{
		"statuses":    host.PlacementEligibleStatuses,
		"provisioned": host.ProvisionedStatuses,
		paramCutoff:   time.Now().Add(lead),
	}
}

// ListStandby returns the standby inventory itself, the warm plane's targets.
func (r *HostRepository) ListStandby(ctx context.Context, lead time.Duration, limit int) ([]*host.Host, error) {
	stmt := spanner.Statement{
		SQL:    standbyListSQL(),
		Params: standbyParams(lead),
	}
	stmt.Params[paramLimit] = int64(limit)
	return spannerplatform.QueryAll(r.client.Single().Query(ctx, stmt),
		"standby hosts", (*models.HostRow).ToDomain)
}
