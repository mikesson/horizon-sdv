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
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// standbySeed is one host row for the standby-inventory statements.
type standbySeed struct {
	id, status string
	origin     host.Origin // "" leaves the column NULL, which derives reserved
	pool       string      // "" leaves the column NULL, which reads as the default pool
	devices    int64
	exclusive  bool
	degraded   bool
	// blocked stamps the row as a provision GCE refused for capacity.
	blocked    bool
	expiresIn  time.Duration // 0 = no expiry
	age        time.Duration // how long ago the row was created
	ttlSeconds int64         // 0 leaves the column NULL, read as the compiled default
	// protected parks the row in the encoding a pinned device produces:
	// lifecycle_mode protected and NO expiry at all. It is mutually exclusive
	// with expiresIn, because that is what "protected hosts don't expire" means.
	protected bool
}

func seedStandbyHost(t *testing.T, db *spannertest.TestDB, s standbySeed) {
	t.Helper()
	now := time.Now()
	row := map[string]any{
		"host_id":                s.id,
		"name":                   s.id,
		"zone":                   "us-central1-a",
		"status":                 s.status,
		"machine_type":           "n1-standard-8",
		"host_orchestrator_port": int64(2080),
		"max_devices":            int64(4),
		"current_device_count":   s.devices,
		"exclusive":              s.exclusive,
		"created_at":             now.Add(-s.age),
		"updated_at":             now,
	}
	// Omitted entirely rather than written as "": an unstamped row must be
	// truly NULL, which is the value the class expression's IN tests
	// answer NULL to.
	if s.origin != "" {
		row["origin"] = string(s.origin)
	}
	// Omitted for the same reason as the origin: an unstamped row must be
	// truly NULL, which is the value the default-pool read answers.
	if s.pool != "" {
		row["pool"] = s.pool
	}
	if s.degraded {
		row["degraded_at"] = now
	}
	if s.blocked {
		row["provision_blocked_at"] = now
	}
	// Written out rather than left to the column defaults: the shorten's
	// candidate listing filters on both, so a fixture that relied on DDL
	// defaults would make every one of those assertions turn on whether the
	// emulator applies them.
	row["lifecycle_mode"] = "ephemeral"
	row["protected_device_count"] = int64(0)
	if s.expiresIn != 0 {
		row["expires_at"] = now.Add(s.expiresIn)
	}
	if s.ttlSeconds != 0 {
		row["ttl_seconds"] = s.ttlSeconds
	}
	if s.protected {
		row["lifecycle_mode"] = "protected"
		row["protected_device_count"] = int64(1)
		delete(row, "expires_at")
	}
	if _, err := db.Client.Apply(t.Context(), []*spanner.Mutation{spanner.InsertMap("hosts", row)}); err != nil {
		t.Fatalf("seed standby host %s: %v", s.id, err)
	}
}

// seedStandbyFleet plants the fixture the statements are pinned against: two
// fleet-class hosts that are inventory, five that fail one term each, and two
// idle hosts that pass every term but are not the fleet's, the pair that
// makes the count and the listing correctly disagree.
//
// The two qualifiers are seeded at different ages so listing order is
// observable.
func seedStandbyFleet(t *testing.T, db *spannertest.TestDB) {
	t.Helper()
	seedStandbyHost(t, db, standbySeed{id: "standby-running", origin: host.OriginPool, status: "running", expiresIn: 2 * time.Hour, age: 3 * time.Hour})
	seedStandbyHost(t, db, standbySeed{id: "standby-provisioning", origin: host.OriginAuto, status: "provisioning", age: time.Hour})
	seedStandbyHost(t, db, standbySeed{id: "standby-busy", origin: host.OriginPool, status: "running", devices: 1})
	seedStandbyHost(t, db, standbySeed{id: "standby-exclusive", origin: host.OriginAuto, status: "running", exclusive: true})
	seedStandbyHost(t, db, standbySeed{id: "standby-degraded", origin: host.OriginPool, status: "running", degraded: true})
	seedStandbyHost(t, db, standbySeed{id: "standby-expiring", origin: host.OriginPool, status: "running", expiresIn: 5 * time.Minute})
	seedStandbyHost(t, db, standbySeed{id: "standby-stopped", origin: host.OriginPool, status: "stopped"})
	// Idle, shareable, undegraded, long-leased: inventory by every term the
	// old predicate had, and neither of them the fleet's to count. Distinct
	// ages, because the listing asserts an order and equal ages would make it
	// a coin toss.
	seedStandbyHost(t, db, standbySeed{id: "standby-user", origin: host.OriginUser, status: "running", age: 150 * time.Minute})
	seedStandbyHost(t, db, standbySeed{id: "standby-unstamped", status: "running", age: 2 * time.Hour})
}

// standbyLead is the provisioning lead the statements are exercised at, kept
// local so a change to the domain constant cannot silently move what these
// tests mean.
const standbyLead = 10 * time.Minute

// TestStandbyInventory pins the inventory definition against real SQL:
// placement-eligible statuses including still-provisioning top-ups (counting
// only running would overshoot the target several-fold while the keeper's own
// provisions are in flight, the mutation this exists for), idle only,
// shareable, undegraded, holding more lease than the provisioning lead, and
// fleet-class.
func TestStandbyInventory(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyFleet(t, db)

	inventory, err := repo.StandbyInventory(t.Context(), standbyLead)
	if err != nil {
		t.Fatalf("StandbyInventory: %v", err)
	}
	if inventory[host.PoolDefault].Standby != 2 {
		t.Errorf("Standby = %d, want 2 (standby-running + standby-provisioning)", inventory[host.PoolDefault].Standby)
	}
}

// A top-up GCE has refused for capacity is not inventory, and the count has to
// say so, or the keeper reports a buffer no create can land on and buys nothing
// while the stocked-out host walks its retry ladder. The fence stops at the
// VM: a host that exists is inventory whatever its row still carries, so a
// marker the provision read-back failed to clear cannot hide a standing host
// from the count once the reconciler observes it.
func TestStandbyInventorySkipsBlockedProvisioningHosts(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyHost(t, db, standbySeed{id: "stocked-out", origin: host.OriginPool, status: "provisioning", blocked: true})
	seedStandbyHost(t, db, standbySeed{id: "coming-up", origin: host.OriginPool, status: "provisioning"})
	seedStandbyHost(t, db, standbySeed{id: "marked-but-up", origin: host.OriginPool, status: "starting", blocked: true})

	inventory, err := repo.StandbyInventory(t.Context(), standbyLead)
	if err != nil {
		t.Fatalf("StandbyInventory: %v", err)
	}
	if got := inventory[host.PoolDefault]; got.Standby != 2 || got.Total != 2 {
		t.Errorf("inventory = %+v, want standby 2 / total 2 (coming-up + marked-but-up); the refused provision is neither", got)
	}
}

// Unmanaged capacity must not suppress a top-up. A user's idle host and an
// unstamped one both satisfy every other standby term, so a count that has not
// narrowed to the fleet class reports 4 here and the keeper holds two hosts
// fewer than the operator asked for, while, once reclamation lands, offering
// somebody else's host as surplus.
//
// Asserted as its own test rather than as a number inside the row above,
// because "4 instead of 2" reads as an off-by-two in the fixture; this says
// which two and why.
func TestStandbyInventoryExcludesHostsTheFleetDoesNotOwn(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyHost(t, db, standbySeed{id: "user-idle", origin: host.OriginUser, status: "running"})
	seedStandbyHost(t, db, standbySeed{id: "unstamped-idle", status: "running"})
	seedStandbyHost(t, db, standbySeed{id: "batch-idle", origin: host.OriginBatch, status: "running"})

	inventory, err := repo.StandbyInventory(t.Context(), standbyLead)
	if err != nil {
		t.Fatalf("StandbyInventory: %v", err)
	}
	if inventory[host.PoolDefault].Standby != 0 {
		t.Errorf("Standby = %d, want 0; none of these hosts is the fleet's", inventory[host.PoolDefault].Standby)
	}
	// And the class term binds both arms, including the one the keeper does not
	// spend against: a user's host running its lease down is not the fleet's
	// outflow.
	if inventory[host.PoolDefault].Draining != 0 {
		t.Errorf("Draining = %d, want 0; the class term must narrow both arms", inventory[host.PoolDefault].Draining)
	}
}

// The count narrows and the listing does not, and that divergence is the whole
// reason they are two statements. Prefetching onto any idle shareable host is
// useful whoever created it; counting one toward the fleet's target is not.
//
// This is the assertion that goes red if the class term is ever hoisted into
// the shared term set: a change that otherwise compiles, keeps every count
// test green, and silently stops the warm plane baking onto most of the fleet.
func TestListStandbyIsNotNarrowedToTheFleet(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyFleet(t, db)

	hosts, err := repo.ListStandby(t.Context(), standbyLead, 10)
	if err != nil {
		t.Fatalf("ListStandby: %v", err)
	}
	inventory, err := repo.StandbyInventory(t.Context(), standbyLead)
	if err != nil {
		t.Fatalf("StandbyInventory: %v", err)
	}
	if int64(len(hosts)) <= inventory[host.PoolDefault].Standby {
		t.Fatalf("ListStandby returned %d hosts and the standby count is %d: the listing must stay wider than the count",
			len(hosts), inventory[host.PoolDefault].Standby)
	}
	seen := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		seen[h.ID] = true
	}
	for _, id := range []string{"standby-user", "standby-unstamped"} {
		if !seen[id] {
			t.Errorf("ListStandby omitted %s: the warm plane bakes onto any idle shareable host", id)
		}
	}
}

// TestListStandby holds the listing to the terms it does share with the count,
// and to the two properties only it has: oldest first, so a fleet larger than
// the warm walk's cap truncates to the same hosts every cycle, and an honored
// limit.
func TestListStandby(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyFleet(t, db)

	hosts, err := repo.ListStandby(t.Context(), standbyLead, 10)
	if err != nil {
		t.Fatalf("ListStandby: %v", err)
	}
	got := make([]string, 0, len(hosts))
	for _, h := range hosts {
		got = append(got, h.ID)
	}
	want := []string{"standby-running", "standby-user", "standby-unstamped", "standby-provisioning"}
	if len(got) != len(want) {
		t.Fatalf("ListStandby = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListStandby = %v, want %v (oldest first)", got, want)
		}
	}

	capped, err := repo.ListStandby(t.Context(), standbyLead, 1)
	if err != nil {
		t.Fatalf("ListStandby limited: %v", err)
	}
	if len(capped) != 1 || capped[0].ID != "standby-running" {
		t.Errorf("ListStandby with limit 1 = %v, want just the oldest (standby-running)", capped)
	}
}

// Draining is the buffer's outflow: a fleet host that is idle and placeable
// but whose remaining lease is too short to count on. It is the complement of
// the standby arm's lease term over the same rows, so a host is never both, and
// a user's host is neither, because the fleet does not own its lease.
//
// The partition is asserted as a partition, which is what one read buys and two
// could not: the arms are read at one cutoff, so their sum is exactly the idle
// fleet-class population. Under two reads a host expiring between two clock
// values landed in both or in neither, and the two numbers this card publishes
// side by side had no way to show it.
func TestStandbyInventoryPartitionsTheIdleFleet(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyFleet(t, db)
	// A user's host inside the same lease window: it is draining in the plain
	// sense and still not the fleet's.
	seedStandbyHost(t, db, standbySeed{id: "user-expiring", origin: host.OriginUser, status: "running", expiresIn: 5 * time.Minute})

	inventory, err := repo.StandbyInventory(t.Context(), standbyLead)
	if err != nil {
		t.Fatalf("StandbyInventory: %v", err)
	}
	if inventory[host.PoolDefault].Draining != 1 {
		t.Errorf("Draining = %d, want 1 (standby-expiring alone)", inventory[host.PoolDefault].Draining)
	}
	if inventory[host.PoolDefault].Standby != 2 {
		t.Errorf("Standby = %d, want 2; draining must not overlap standby", inventory[host.PoolDefault].Standby)
	}
	// standby-running, standby-provisioning and standby-expiring: every
	// fleet-class host the idle terms admit, each on exactly one side.
	if got := inventory[host.PoolDefault].Standby + inventory[host.PoolDefault].Draining; got != 3 {
		t.Errorf("standby + draining = %d, want 3; the two arms must partition the idle fleet, "+
			"counting every such host once and none of them twice", got)
	}
}

// The total is what the fleet FLOOR is held against. It must include the host
// the two idle counts cannot see, because one filling with devices is still a
// host the fleet is running. Counting only idle hosts here would make every
// create look like a machine lost, and the floor would buy a replacement for
// each one.
func TestStandbyInventoryCountsTheStandingFleet(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyFleet(t, db)

	inventory, err := repo.StandbyInventory(t.Context(), standbyLead)
	if err != nil {
		t.Fatalf("StandbyInventory: %v", err)
	}
	// standby-running, standby-provisioning, standby-expiring and standby-busy:
	// the idle fleet plus the one host devices have taken.
	if inventory[host.PoolDefault].Total != 4 {
		t.Errorf("Total = %d, want 4 (the idle fleet of 3, plus standby-busy)", inventory[host.PoolDefault].Total)
	}
	if inventory[host.PoolDefault].Total <= inventory[host.PoolDefault].Standby+inventory[host.PoolDefault].Draining {
		t.Errorf("Total = %d against %d idle: a fleet whose total is its idle count proves nothing about occupancy",
			inventory[host.PoolDefault].Total, inventory[host.PoolDefault].Standby+inventory[host.PoolDefault].Draining)
	}
}

// The two exclusions the total keeps from the idle counts, and neither is
// cosmetic. A degraded host is fenced out of placement, so a fleet holding its
// floor in degraded hosts holds nothing a create can use; and a host of another
// class is not the fleet's to count, exactly as it is not the fleet's to hold
// free. One row per term, so a mutation that dropped either is named by the
// failure rather than found by subtraction.
func TestStandbyInventoryTotalIsFleetClassAndPlaceable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		seed standbySeed
	}{
		{"a degraded fleet host", standbySeed{id: "poisoned", origin: host.OriginPool, status: "running", degraded: true}},
		{"a host fenced to one group", standbySeed{id: "dedicated", origin: host.OriginAuto, status: "running", exclusive: true}},
		{"a batch host", standbySeed{id: "batched", origin: host.OriginBatch, status: "running"}},
		{"a user's host", standbySeed{id: "someones", origin: host.OriginUser, status: "running"}},
		{"an unstamped host", standbySeed{id: "unstamped", status: "running"}},
		{"a stopped host", standbySeed{id: "halted", origin: host.OriginPool, status: "stopped"}},
		{"a provisioning host GCE refused for capacity", standbySeed{id: "stocked-out", origin: host.OriginPool, status: "provisioning", blocked: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := spannertest.MustNewTestDB(t)
			repo := hostfeature.NewHostRepository(db.Client)
			// One host the total must find, so a statement that had stopped
			// counting altogether cannot pass this table.
			seedStandbyHost(t, db, standbySeed{id: "counted", origin: host.OriginPool, status: "running", devices: 1})
			seedStandbyHost(t, db, tc.seed)

			inventory, err := repo.StandbyInventory(t.Context(), standbyLead)
			if err != nil {
				t.Fatalf("StandbyInventory: %v", err)
			}
			if inventory[host.PoolDefault].Total != 1 {
				t.Errorf("Total = %d, want 1; only the fleet host placement can land on counts", inventory[host.PoolDefault].Total)
			}
		})
	}
}

// The counts are per pool out of one read, and a machine whose column was never
// stamped belongs to the default pool rather than to a pool of its own.
//
// Both halves matter. Without the grouping every pool's bounds would be judged
// against the whole fleet's counts, so a gpu pool holding nothing would read as
// satisfied by the default pool's machines. Without the NULL rule the fleet the
// keeper has been running since before pools would appear under a nameless pool
// no policy configures, and its floor would buy the whole fleet a second time.
func TestStandbyInventoryCountsEachPoolFromOneRead(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyHost(t, db, standbySeed{id: "unstamped-idle", origin: host.OriginPool, status: "running"})
	seedStandbyHost(t, db, standbySeed{
		id: "default-idle", origin: host.OriginPool, status: "running", pool: host.PoolDefault,
	})
	seedStandbyHost(t, db, standbySeed{
		id: "gpu-idle", origin: host.OriginPool, status: "running", pool: "gpu",
	})
	seedStandbyHost(t, db, standbySeed{
		id: "gpu-busy", origin: host.OriginPool, status: "running", pool: "gpu", devices: 1,
	})
	seedStandbyHost(t, db, standbySeed{
		id: "gpu-draining", origin: host.OriginPool, status: "running", pool: "gpu", expiresIn: 5 * time.Minute,
	})

	inventory, err := repo.StandbyInventory(t.Context(), standbyLead)
	if err != nil {
		t.Fatalf("StandbyInventory: %v", err)
	}
	if got := inventory[host.PoolDefault]; got.Standby != 2 || got.Draining != 0 || got.Total != 2 {
		t.Errorf("the default pool = %+v, want 2 standby out of 2; the unstamped host is the default pool's", got)
	}
	if got := inventory["gpu"]; got.Standby != 1 || got.Draining != 1 || got.Total != 3 {
		t.Errorf("the gpu pool = %+v, want 1 standby, 1 draining, 3 standing", got)
	}
	if len(inventory) != 2 {
		t.Errorf("inventory holds %d pools (%v), want exactly the two the fleet has machines in",
			len(inventory), inventory)
	}
	// The fleet is what its pools hold, which is what every fleet-wide surface
	// publishes and what the per-cycle budget is a share of.
	if fleet := inventory.Fleet(); fleet.Standby != 3 || fleet.Draining != 1 || fleet.Total != 5 {
		t.Errorf("the fleet = %+v, want the two pools added", fleet)
	}
}
