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

package hostfeature_test

import (
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// classSeed is one row of the origin × exclusive × batch_id grid. origin is a
// *host.Origin so a row can be seeded with the column left NULL, which is what
// a host created before the column existed, or by a create site that failed to
// stamp, reads back as.
type classSeed struct {
	id        string
	origin    *host.Origin
	exclusive bool
	batchID   string
}

// classGrid is the same grid the domain table test walks, seeded as real rows.
// Every cell is here rather than a sample because the SQL is a second copy of
// the derivation, and a copy is only pinned where it is compared.
var classGrid = []classSeed{
	{id: "cls-auto-shared", origin: new(host.OriginAuto)},
	{id: "cls-pool-shared", origin: new(host.OriginPool)},
	{id: "cls-auto-exclusive", origin: new(host.OriginAuto), exclusive: true},
	{id: "cls-pool-exclusive", origin: new(host.OriginPool), exclusive: true},
	{id: "cls-user-shared", origin: new(host.OriginUser)},
	{id: "cls-user-exclusive", origin: new(host.OriginUser), exclusive: true},
	{id: "cls-batch-origin", origin: new(host.OriginBatch), batchID: "b-1"},
	{id: "cls-batch-origin-exclusive", origin: new(host.OriginBatch), exclusive: true, batchID: "b-2"},
	{id: "cls-batch-id-auto", origin: new(host.OriginAuto), batchID: "b-3"},
	{id: "cls-batch-id-user", origin: new(host.OriginUser), exclusive: true, batchID: "b-4"},
	{id: "cls-batch-id-unstamped", batchID: "b-5"},
	{id: "cls-batch-origin-no-id", origin: new(host.OriginBatch)},
	{id: "cls-unstamped"},
	{id: "cls-unstamped-exclusive", exclusive: true},
	{id: "cls-unknown", origin: new(host.Origin("spot-fleet"))},
	{id: "cls-unknown-exclusive", origin: new(host.Origin("spot-fleet")), exclusive: true},
}

func seedClassGrid(t *testing.T, db *spannertest.TestDB) {
	t.Helper()
	now := time.Now()
	muts := make([]*spanner.Mutation, 0, len(classGrid))
	for _, s := range classGrid {
		row := map[string]any{
			"host_id":                s.id,
			"name":                   s.id,
			"zone":                   "us-central1-a",
			"status":                 "running",
			"machine_type":           "n1-standard-8",
			"host_orchestrator_port": int64(2080),
			"max_devices":            int64(4),
			"current_device_count":   int64(0),
			"exclusive":              s.exclusive,
			"created_at":             now,
			"updated_at":             now,
		}
		// Left out entirely when nil: an absent key is the NULL the fail-safe
		// arm exists for, and writing "" instead would test a different thing.
		if s.origin != nil {
			row["origin"] = string(*s.origin)
		}
		if s.batchID != "" {
			row["batch_id"] = s.batchID
		}
		muts = append(muts, spanner.InsertMap("hosts", row))
	}
	if _, err := db.Client.Apply(t.Context(), muts); err != nil {
		t.Fatalf("seeding the class grid: %v", err)
	}
}

// TestClassFilterAgreesWithDeriveClass is the agreement test between the two
// copies of the class derivation: host.DeriveClass in Go, and the CASE
// expression the repository filters with. They cannot be one copy, because the
// class has no column, so the filter has to evaluate the derivation in SQL or
// page over the wrong row set. This is what keeps them from drifting.
//
// It compares them cell by cell over the whole grid, in both directions: every
// row a class filter returns must derive that class in Go, and every row that
// derives it in Go must come back.
//
// Three mutations it catches, all of which compile and none of which any other
// test notices:
//
//   - Writing either origin test as a negation (`origin NOT IN (...)`) rather
//     than falling through to ELSE. NULL makes the negation NULL, not TRUE, so
//     the unstamped rows vanish from every class.
//   - Moving the batch arm below the origin arms: cls-batch-id-auto stops being
//     batch and turns up as fleet, which is a host the fleet would reclaim out
//     from under batch settlement.
//   - Keying the dedicated arm on exclusivity alone: cls-user-exclusive moves
//     from reserved to dedicated: a host a user fenced, offered up for reclaim.
func TestClassFilterAgreesWithDeriveClass(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedClassGrid(t, db)

	seen := make(map[string]host.Class, len(classGrid))
	for _, class := range host.Classes {
		want := make([]string, 0, len(classGrid))
		for _, s := range classGrid {
			var o host.Origin
			if s.origin != nil {
				o = *s.origin
			}
			if host.DeriveClass(o, s.exclusive, s.batchID) == class {
				want = append(want, s.id)
			}
		}

		hosts, err := repo.List(t.Context(), host.ListFilter{Class: &class})
		if err != nil {
			t.Fatalf("List(class=%s): %v", class, err)
		}
		got := make([]string, 0, len(hosts))
		for _, h := range hosts {
			got = append(got, h.ID)
			// The row's own derived class must agree with the filter that
			// selected it: this is the read path and the query, pinned to each
			// other rather than each to the test's expectations.
			if h.Class != class {
				t.Errorf("host %s came back from the %s filter but reads as %s", h.ID, class, h.Class)
			}
			if prev, dup := seen[h.ID]; dup {
				t.Errorf("host %s matched both %s and %s; the arms are not exclusive", h.ID, prev, class)
			}
			seen[h.ID] = class
		}

		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("List(class=%s) = %v\nhost.DeriveClass says  = %v", class, got, want)
		}
	}

	// Every seeded row belongs to exactly one class. Without this a predicate
	// that matched nothing at all would agree with a Go derivation the test
	// also got wrong, and four empty comparisons would pass.
	if len(seen) != len(classGrid) {
		t.Errorf("the four class filters covered %d of %d rows; the derivation is not total in SQL",
			len(seen), len(classGrid))
	}
}

// TestClassFilterComposesWithOtherFilters guards the mechanical half: the class
// predicate is appended to a shared WHERE clause and carries its own named
// parameters, so a collision or a lost AND would either error or silently widen
// the result. Count and List share buildHostFilter, so a paginated total that
// disagreed with the rows would be the visible symptom.
func TestClassFilterComposesWithOtherFilters(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedClassGrid(t, db)

	fleet := host.ClassFleet
	zone := "us-central1-a"
	other := "europe-west4-a"

	filter := host.ListFilter{Class: &fleet, Zone: &zone}
	hosts, err := repo.List(t.Context(), filter)
	if err != nil {
		t.Fatalf("List(class+zone): %v", err)
	}
	count, err := repo.Count(t.Context(), filter)
	if err != nil {
		t.Fatalf("Count(class+zone): %v", err)
	}
	if int64(len(hosts)) != count {
		t.Errorf("List returned %d rows but Count says %d", len(hosts), count)
	}
	if len(hosts) != 2 {
		t.Errorf("fleet hosts in %s = %d, want 2 (auto + pool, both shared)", zone, len(hosts))
	}

	empty, err := repo.List(t.Context(), host.ListFilter{Class: &fleet, Zone: &other})
	if err != nil {
		t.Fatalf("List(class+other zone): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("fleet hosts in %s = %d, want 0; the zone term was dropped", other, len(empty))
	}
}
