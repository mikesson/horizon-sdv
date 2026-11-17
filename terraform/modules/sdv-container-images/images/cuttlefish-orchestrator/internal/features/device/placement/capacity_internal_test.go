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

package placement

import (
	"strings"
	"testing"
)

// The pick and the fleet view were once two hand-maintained copies of the
// same predicate held together by a comment. This pins the replacement: both
// statements come out of placementStatement, and from " FROM hosts" onward
// they may differ ONLY in the trailing LIMIT. A predicate or ordering term
// that reaches one consumer and not the other goes red here.
func TestPlacementPredicateParity(t *testing.T) {
	t.Parallel()
	prefs := Prefs{AffineFetchKey: "@ab/12345/target", PreferStandard: true}
	pick := placementStatement(placementQuery{
		selectList: pickProjection,
		excluded:   []string{"h-retry"},
		limit:      pickWindow,
	}, true, prefs)
	list := placementStatement(placementQuery{
		selectList: "host_id, max_devices - current_device_count AS free",
	}, true, prefs)

	shared := func(sql string) string {
		i := strings.Index(sql, " FROM hosts")
		if i < 0 {
			t.Fatalf("statement lost its FROM clause: %q", sql)
		}
		return sql[i:]
	}
	pickShared := shared(pick.SQL)
	if !strings.HasSuffix(pickShared, "LIMIT @limit") {
		t.Fatalf("pick statement lost its LIMIT: %q", pickShared)
	}
	pickShared = strings.TrimSpace(strings.TrimSuffix(pickShared, "LIMIT @limit"))
	listShared := strings.TrimSpace(shared(list.SQL))
	if pickShared != listShared {
		t.Errorf("predicate/ordering diverged between consumers:\npick: %s\nlist: %s", pickShared, listShared)
	}

	// The retry filter is a parameter binding, never a structural difference;
	// the window size is the pick's one extra binding.
	if got := pick.Params["limit"]; got != int64(pickWindow) {
		t.Errorf("pick window binding = %v, want %d", got, pickWindow)
	}
	if _, ok := list.Params["limit"]; ok {
		t.Errorf("the fleet view must stay unlimited: %v", list.Params)
	}
	if len(pick.Params) != len(list.Params)+1 {
		t.Errorf("parameter sets diverged: pick %v, list %v", pick.Params, list.Params)
	}
}

// The class bits the spread groups by have to be the terms the ordering sorted
// by, or the "rows the ordering could not separate" the pick draws from are
// rows it could. A projection that drifted from the ORDER BY would leave the
// two agreeing only by coincidence, so both share one declaration.
func TestPickProjectionCarriesTheOrderingTerms(t *testing.T) {
	t.Parallel()
	stmt := placementStatement(placementQuery{selectList: pickProjection, limit: pickWindow},
		false, Prefs{PreferStandard: true})

	for _, want := range []string{
		preferStandardExpr + ` AS standard`,
		affinityWarmExpr + ` AS warm`,
		`current_device_count > 0 AS occupied`,
		provisionedExpr + ` AS ready`,
	} {
		if !strings.Contains(stmt.SQL, want) {
			t.Errorf("pick projection lost %q:\n%s", want, stmt.SQL)
		}
	}
	for _, term := range []string{preferStandardExpr, affinityWarmExpr, provisionedExpr} {
		if !strings.Contains(stmt.SQL, term+` DESC`) {
			t.Errorf("projected term %q is not an ordering term:\n%s", term, stmt.SQL)
		}
	}
}

// The ready term sits BELOW the bin-pack, and this pins that side of it: ranked
// above, every create arriving while a healthy host provisioned would open an
// idle running standby host instead of packing onto it, spending the standby
// host and buying its replacement. A stuck host is kept out by the predicate,
// not by the ordering, so the ordering owes nothing there.
func TestReadyTermRanksBelowTheBinPack(t *testing.T) {
	t.Parallel()
	stmt := placementStatement(placementQuery{selectList: pickProjection, limit: pickWindow},
		false, Prefs{})
	order := stmt.SQL[strings.Index(stmt.SQL, "ORDER BY"):]
	load, ready := strings.Index(order, "current_device_count DESC"), strings.Index(order, provisionedExpr+" DESC")
	if load < 0 || ready < 0 || ready < load {
		t.Errorf("ready term must follow the bin-pack in the ordering:\n%s", order)
	}
}

// The provision-blocked fence: a pending or provisioning host GCE has refused
// for capacity is not a candidate, and a host whose VM exists is exempt from
// the fence whatever its row still carries. Pinned on the statement text
// because the emulator cannot tell an exempt row from a never-marked one
// without a fixture for each, which capacity_test.go carries.
func TestPlacementFencesBlockedProvisioningHosts(t *testing.T) {
	t.Parallel()
	stmt := placementStatement(placementQuery{selectList: pickProjection, limit: pickWindow},
		false, Prefs{})
	want := `AND (` + provisionedExpr + ` OR provision_blocked_at IS NULL)`
	if !strings.Contains(stmt.SQL, want) {
		t.Errorf("predicate lost the provision-blocked fence %q:\n%s", want, stmt.SQL)
	}
	if got, ok := stmt.Params["provisioned"].([]string); !ok || len(got) == 0 {
		t.Errorf("the ready statuses are not bound: %v", stmt.Params["provisioned"])
	}
}

// spreadDraws is enough draws that a spread which really is uniform misses no
// row: over four rows a given one is skipped with probability (3/4)^400, while
// a selection that answers with one row shows up on the first draw.
const spreadDraws = 400

// candidate names the class a fixture row is about; the bits a case is not
// testing are held at one value across its whole window. Every row's VM exists
// unless a case says otherwise through notReady.
func candidate(id string, standard, warm, occupied bool) placementCandidate {
	return placementCandidate{hostID: id, standard: standard, warm: warm, occupied: occupied, ready: true}
}

// notReady is a candidate still coming up.
func notReady(c placementCandidate) placementCandidate {
	c.ready = false
	return c
}

// drawnHosts runs the spread over one window many times and reports how often
// each host answered.
func drawnHosts(t *testing.T, window []placementCandidate) map[string]int {
	t.Helper()
	seen := make(map[string]int)
	for range spreadDraws {
		pick, ok := spreadPick(window)
		if !ok {
			t.Fatalf("spreadPick found nothing in a window of %d", len(window))
		}
		seen[pick.hostID]++
	}
	return seen
}

// STANDARD is the boundary the Spot-fallback warning rests on: an interactive
// create told its session may be preempted has to be one for which no standard
// host was free. A spread over the whole window would break that promise three
// draws in four here.
func TestSpreadPickHoldsTheStandardBoundary(t *testing.T) {
	t.Parallel()
	got := drawnHosts(t, []placementCandidate{
		candidate("h-standard", true, false, true),
		candidate("h-spot-a", false, false, true),
		candidate("h-spot-b", false, false, true),
		candidate("h-spot-c", false, false, true),
	})
	if got["h-standard"] != spreadDraws {
		t.Errorf("draws by host = %v, want all %d on h-standard; Spot is where a free standard host stops being a fact", got, spreadDraws)
	}
}

// Affinity is the fetch a warm host skips. Sampling across the boundary would
// re-fetch artifacts a host with room already held, and would report the drawn
// row's own warmth, so the create is not even told what it lost.
func TestSpreadPickHoldsTheWarmBoundary(t *testing.T) {
	t.Parallel()
	window := []placementCandidate{
		candidate("h-warm", false, true, true),
		candidate("h-cold-a", false, false, true),
		candidate("h-cold-b", false, false, true),
		candidate("h-cold-c", false, false, true),
	}
	if got := drawnHosts(t, window); got["h-warm"] != spreadDraws {
		t.Errorf("draws by host = %v, want all %d on h-warm; a cold draw pays for a fetch that was already made", got, spreadDraws)
	}
	pick, ok := spreadPick(window)
	if !ok || !pick.warm {
		t.Errorf("pick = (%+v, %v), want the chosen row's own warm bit", pick, ok)
	}
}

// Standby membership is literally current_device_count = 0, so opening an empty
// host while an occupied one had room spends a standby host and buys its
// replacement, which at a three-host fleet is a VM per create.
func TestSpreadPickHoldsTheOccupiedBoundary(t *testing.T) {
	t.Parallel()
	got := drawnHosts(t, []placementCandidate{
		candidate("h-busy-a", false, false, true),
		candidate("h-busy-b", false, false, true),
		candidate("h-empty-a", false, false, false),
		candidate("h-empty-b", false, false, false),
	})
	if got["h-busy-a"]+got["h-busy-b"] != spreadDraws {
		t.Errorf("draws by host = %v, want all %d on the occupied pair", got, spreadDraws)
	}
}

// A host whose VM exists serves the create now; one still coming up makes it
// wait a provisioning leg. The ordering puts the ready one first among rows it
// ranked alike on everything above, and the spread has to hold that line, or a
// create is parked behind a boot while an equal host stood ready.
func TestSpreadPickHoldsTheReadyBoundary(t *testing.T) {
	t.Parallel()
	window := []placementCandidate{
		candidate("h-ready", false, false, true),
		notReady(candidate("h-booting-a", false, false, true)),
		notReady(candidate("h-booting-b", false, false, true)),
		notReady(candidate("h-booting-c", false, false, true)),
	}
	if got := drawnHosts(t, window); got["h-ready"] != spreadDraws {
		t.Errorf("draws by host = %v, want all %d on h-ready; a booting draw waits on a VM that does not exist yet", got, spreadDraws)
	}
}

// The spread itself: a window the ordering ranked alike has to answer with all
// of it, or concurrent creates still queue behind one host and burn their
// attempts on the same commit.
func TestSpreadPickSpreadsWithinOneClass(t *testing.T) {
	t.Parallel()
	got := drawnHosts(t, []placementCandidate{
		candidate("h-a", false, false, true),
		candidate("h-b", false, false, true),
		candidate("h-c", false, false, true),
		candidate("h-d", false, false, true),
	})
	if len(got) != 4 {
		t.Errorf("draws by host = %v over %d draws, want every host in the class", got, spreadDraws)
	}
}

// A window with one eligible row is the whole small-fleet case, and it has to
// behave exactly as the LIMIT 1 pick it replaced: the leading row, every time.
// An empty window is the no-capacity answer the caller provisions on.
func TestSpreadPickWithOneCandidate(t *testing.T) {
	t.Parallel()
	only := []placementCandidate{candidate("h-only", false, false, true)}
	if got := drawnHosts(t, only); got["h-only"] != spreadDraws {
		t.Errorf("draws by host = %v, want all %d on h-only", got, spreadDraws)
	}
	if pick, ok := spreadPick(nil); ok {
		t.Errorf("empty window answered %+v, want no candidate", pick)
	}
}
