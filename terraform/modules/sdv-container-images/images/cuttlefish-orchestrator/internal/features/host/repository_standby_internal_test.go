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
	"slices"
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// The fleet's two lower bounds are spent against these counts in one decision,
// so they must come from ONE read: two statements let the floor and the buffer
// see two fleets, and the disagreement shows up as a host bought that the other
// bound had just counted, or a host refused release because two reads differed
// by a row.
//
// Every pool's counts come out of that same one read, which is the other half of
// the claim: a read per pool would make the keeper's statement count a function
// of how many pools an operator configured, on a loop that runs forever.
//
// Asserted on the statement's shape because no value can say it. A second query
// for the total, or one per pool, returns the same numbers in every quiet
// fixture, and the interleaving that makes it wrong is the one a test cannot
// schedule.
func TestInventoryIsOneStatement(t *testing.T) {
	t.Parallel()
	sql, _ := inventorySQL()
	for _, projection := range []string{"AS pool", "AS standby", "AS draining", "AS total"} {
		if !strings.Contains(sql, projection) {
			t.Errorf("the inventory statement does not project %q:\n%s", projection, sql)
		}
	}
	if !strings.Contains(sql, "GROUP BY") {
		t.Errorf("the inventory statement does not group, so it answers for one fleet rather than each pool:\n%s", sql)
	}
	if got := strings.Count(strings.ToUpper(sql), "SELECT"); got != 1 {
		t.Errorf("the inventory statement holds %d SELECTs, want 1:\n%s", got, sql)
	}
	if got := strings.Count(strings.ToUpper(sql), " FROM "); got != 1 {
		t.Errorf("the inventory statement holds %d FROM clauses, want 1:\n%s", got, sql)
	}
}

// The two reclamation listings order their rows by pool so capPerPool can
// apply each pool's budget to the scan of one statement.
//
// The shapes each of them must not hold: a statement per pool, which puts the
// pool count inside the cycle; a fleet-wide LIMIT, which one pool would spend;
// and an analytic function, which the emulator (the only executed
// verification of this layer) rejects outright, leaving the order proven
// nowhere. Asserted on the shape because a single-pool fixture cannot tell a
// global bound from a per-pool one: with one pool they return the same rows.
func TestTheReclamationListingsRankPerPool(t *testing.T) {
	t.Parallel()
	for name, build := range map[string]func() (string, map[string]any){
		"shortenable": shortenableSQL,
		"rescuable":   rescuableSQL,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sql, _ := build()
			if !strings.Contains(sql, "ORDER BY pool_key ASC,") {
				t.Errorf("the %s listing does not group its order by pool, so capPerPool cannot bound it:\n%s", name, sql)
			}
			if strings.Contains(sql, "OVER (") {
				t.Errorf("the %s listing uses an analytic function, which the emulator cannot run:\n%s", name, sql)
			}
			// The clause, not the parameter: matching the bare word would fire
			// on statements that legitimately bind @limit elsewhere.
			if strings.Contains(strings.ToUpper(sql), "LIMIT @") {
				t.Errorf("the %s listing carries a fleet-wide LIMIT, which one pool would spend:\n%s", name, sql)
			}
		})
	}
}

// capPerPool is the Go half of the per-pool bound: first rows win inside each
// pool, other pools' rows pass untouched, and the empty pool name folds to the
// default pool exactly as the SQL's pool key does.
func TestCapPerPoolBoundsEachPoolAlone(t *testing.T) {
	t.Parallel()
	rows := []*host.Host{
		{ID: "d1", Pool: ""},
		{ID: "d2", Pool: host.PoolDefault},
		{ID: "g1", Pool: "gpu"},
		{ID: "d3", Pool: host.PoolDefault},
		{ID: "g2", Pool: "gpu"},
	}
	kept := capPerPool(rows, 2)
	ids := make([]string, 0, len(kept))
	for _, h := range kept {
		ids = append(ids, h.ID)
	}
	if !slices.Equal(ids, []string{"d1", "d2", "g1", "g2"}) {
		t.Errorf("kept = %v, want [d1 d2 g1 g2]: two per pool, NULL folded into the default pool", ids)
	}
}
