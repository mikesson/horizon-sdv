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

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// draining is a lease short enough to have left the standby count: the renewal
// path's whole population.
const draining = 5 * time.Minute

// defaultPoolGap is a per-pool budget map for a fleet nobody has partitioned,
// which is what every fixture here seeds: unstamped rows all read as the
// default pool.
func defaultPoolGap(hosts int64) map[string]int64 {
	return map[string]int64{host.PoolDefault: hosts}
}

// seedLedger plants artifact-cache rows, which is what warmth IS: a host's
// warmth is the count of fetch keys its disk is known to hold, so the ordering
// cannot be exercised without them.
func seedLedger(t *testing.T, db *spannertest.TestDB, hostID string, keys ...string) {
	t.Helper()
	muts := make([]*spanner.Mutation, 0, len(keys))
	for _, k := range keys {
		muts = append(muts, spanner.InsertMap("host_artifact_cache", map[string]any{
			"host_id":          hostID,
			"fetch_key":        k,
			"build_id":         "b1",
			"host_incarnation": int64(0),
			"fetched_at":       time.Now(),
		}))
	}
	if _, err := db.Client.Apply(t.Context(), muts); err != nil {
		t.Fatalf("seed ledger for %s: %v", hostID, err)
	}
}

// leaseOf reads a host's expiry straight from the row. Straight from the row on
// purpose: every claim here is about the value that reached storage, and a
// domain round trip could smooth over a NULL.
func leaseOf(t *testing.T, db *spannertest.TestDB, hostID string) spanner.NullTime {
	t.Helper()
	row, err := db.Client.Single().ReadRow(t.Context(), "hosts", spanner.Key{hostID}, []string{"expires_at"})
	if err != nil {
		t.Fatalf("reading the lease of %s: %v", hostID, err)
	}
	var expires spanner.NullTime
	if err := row.Column(0, &expires); err != nil {
		t.Fatalf("scanning the lease of %s: %v", hostID, err)
	}
	return expires
}

// idsOf renders a host listing for assertion messages.
func idsOf(hosts []*host.Host) []string {
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, h.ID)
	}
	return out
}

// The renewal population is the fleet's own outflow and nothing else. Every
// other row here satisfies some of the terms (one is draining but a user's, one
// is fleet but not draining, one is exclusive, one holds a device, one is
// degraded, one is a batch host), so a predicate that dropped any single term
// picks up a host whose lease is not the autoscaler's to extend.
func TestListRescuableIsTheFleetsOutflowAlone(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)

	seedStandbyHost(t, db, standbySeed{id: "fleet-draining", origin: host.OriginPool, status: "running", expiresIn: draining})
	seedStandbyHost(t, db, standbySeed{id: "fleet-healthy", origin: host.OriginPool, status: "running", expiresIn: 2 * time.Hour})
	seedStandbyHost(t, db, standbySeed{id: "user-draining", origin: host.OriginUser, status: "running", expiresIn: draining})
	seedStandbyHost(t, db, standbySeed{id: "unstamped-draining", status: "running", expiresIn: draining})
	seedStandbyHost(t, db, standbySeed{id: "batch-draining", origin: host.OriginBatch, status: "running", expiresIn: draining})
	seedStandbyHost(t, db, standbySeed{id: "dedicated-draining", origin: host.OriginAuto, status: "running", exclusive: true, expiresIn: draining})
	seedStandbyHost(t, db, standbySeed{id: "busy-draining", origin: host.OriginPool, status: "running", devices: 1, expiresIn: draining})
	seedStandbyHost(t, db, standbySeed{id: "degraded-draining", origin: host.OriginPool, status: "running", degraded: true, expiresIn: draining})

	hosts, err := repo.ListRescuable(t.Context(), standbyLead, 10)
	if err != nil {
		t.Fatalf("ListRescuable: %v", err)
	}
	if got := idsOf(hosts); len(got) != 1 || got[0] != "fleet-draining" {
		t.Errorf("ListRescuable = %v, want [fleet-draining] alone", got)
	}
}

// A protected host never reaches the renewal path. NULL expires_at is the
// protected encoding, and the draining terms exclude NULL, so this holds
// today for a reason that lives in a different constant, which is exactly why
// it is asserted here rather than assumed.
func TestListRescuableNeverListsAProtectedHost(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyHost(t, db, standbySeed{id: "pinned", origin: host.OriginPool, status: "running", protected: true})

	hosts, err := repo.ListRescuable(t.Context(), standbyLead, 10)
	if err != nil {
		t.Fatalf("ListRescuable: %v", err)
	}
	if len(hosts) != 0 {
		t.Errorf("ListRescuable = %v, want none; a protected host has no lease to renew", idsOf(hosts))
	}
}

// Warmth decides who is renewed first, because warmth is what renewal is FOR:
// keeping a host whose disk already holds fetched artifacts rather than
// replacing it with a cold one. Ledger-row count first, longest remaining lease
// breaking the tie.
func TestListRescuableTakesTheWarmestFirst(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)

	seedStandbyHost(t, db, standbySeed{id: "cold", origin: host.OriginPool, status: "running", expiresIn: draining})
	seedStandbyHost(t, db, standbySeed{id: "warm", origin: host.OriginPool, status: "running", expiresIn: draining})
	seedStandbyHost(t, db, standbySeed{id: "warmest", origin: host.OriginPool, status: "running", expiresIn: draining})
	// Same warmth as "cold" and a longer lease, so the tiebreak is observable
	// rather than incidental.
	seedStandbyHost(t, db, standbySeed{id: "cold-but-later", origin: host.OriginPool, status: "running", expiresIn: draining + time.Minute})
	seedLedger(t, db, "warm", "@ab/1/t")
	seedLedger(t, db, "warmest", "@ab/1/t", "@ab/2/t")

	hosts, err := repo.ListRescuable(t.Context(), standbyLead, 10)
	if err != nil {
		t.Fatalf("ListRescuable: %v", err)
	}
	want := []string{"warmest", "warm", "cold-but-later", "cold"}
	got := idsOf(hosts)
	if len(got) != len(want) {
		t.Fatalf("ListRescuable = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListRescuable = %v, want %v (warmest first, longest lease breaking the tie)", got, want)
		}
	}
}

// The age bound is what keeps host images rolling through the fleet. Renewal
// prefers warmth, warmth only grows within an incarnation, so the oldest hosts
// are the warmest and an unbounded renewal converges the fleet onto hosts that
// outlive every image respin, with TTL attrition, the only mechanism that
// rolls images today, removed.
//
// The two rows sit either side of the bound. A renewal bounded only by the 7 d
// safety bound returns both and stays green for a day before the fleet silently
// stops turning over.
func TestListRescuableRefusesHostsPastThePolicyBound(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)

	seedStandbyHost(t, db, standbySeed{
		id: "inside-the-bound", origin: host.OriginPool, status: "running",
		expiresIn: draining, age: host.MaxFleetRescueAge - time.Hour,
	})
	// Past the point where the ceiling could still leave a full lead of lease:
	// renewing it would write a timestamp and return nothing to inventory.
	seedStandbyHost(t, db, standbySeed{
		id: "past-the-bound", origin: host.OriginPool, status: "running",
		expiresIn: draining, age: host.MaxFleetRescueAge - time.Minute,
	})

	hosts, err := repo.ListRescuable(t.Context(), standbyLead, 10)
	if err != nil {
		t.Fatalf("ListRescuable: %v", err)
	}
	if got := idsOf(hosts); len(got) != 1 || got[0] != "inside-the-bound" {
		t.Errorf("ListRescuable = %v, want [inside-the-bound]; the fleet must still roll over daily", got)
	}
}

// The renewal a host gets is capped at its own creation plus the policy bound,
// not merely refused above it, and the difference is a whole extra TTL of life.
//
// This host was created 20 h ago with a 10 h lease. An implementation that
// checked eligibility and then granted a fresh term writes now + 10 h and the
// host lives 30 h, while every test written against the refusal still passes.
// The ceiling puts it at creation + 24 h, which is 4 h from now.
func TestRescueCapsTheRenewalAtThePolicyBound(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	svc := createService(t, db)
	seedStandbyHost(t, db, standbySeed{
		id: "elderly", origin: host.OriginPool, status: "running",
		expiresIn: draining, age: 20 * time.Hour, ttlSeconds: host.DefaultHostTTLSeconds,
	})

	rescued, err := svc.RescueFleetHosts(ctx, standbyLead, defaultPoolGap(4))
	if err != nil {
		t.Fatalf("RescueFleetHosts: %v", err)
	}
	if rescued[host.PoolDefault] != 1 {
		t.Fatalf("rescued = %v, want 1 in the default pool", rescued)
	}

	lease := leaseOf(t, db, "elderly")
	if !lease.Valid {
		t.Fatal("the renewed host has no lease at all")
	}
	ceiling := time.Now().Add(4 * time.Hour)
	if lease.Time.Sub(ceiling) > time.Minute || ceiling.Sub(lease.Time) > time.Minute {
		t.Errorf("lease = %v, want about %v (creation + the policy bound, not a fresh full TTL)",
			lease.Time, ceiling)
	}
}

// Renewal is scale-out and can only ever move an expiry forward. A host whose
// lease a device commit already raised past what a renewal would grant is left
// exactly as it is, because a write here would LOWER it, which no mode below
// `on` may do and which this one may never do at all.
func TestExtendLeaseNeverLowersALease(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyHost(t, db, standbySeed{id: "promised", origin: host.OriginPool, status: "running", expiresIn: 6 * time.Hour})

	before := leaseOf(t, db, "promised")
	written, err := repo.ExtendLease(t.Context(), "promised", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ExtendLease: %v", err)
	}
	if written {
		t.Error("ExtendLease wrote a target below the current lease")
	}
	if after := leaseOf(t, db, "promised"); !after.Time.Equal(before.Time) {
		t.Errorf("lease moved from %v to %v", before.Time, after.Time)
	}
}

// A protected host's NULL lease survives. NULL is how a protected host's lease
// is encoded, so writing a timestamp onto it converts the host into an
// expiring one.
//
// The assertion is on the LEASE VALUE and not on the host surviving. The reaper
// filters on lifecycle_mode and protected_device_count, so it never touches this
// host whatever expires_at says, and a test asserting survival passes with the
// encoding already corrupted.
func TestExtendLeaseLeavesAProtectedLeaseNull(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyHost(t, db, standbySeed{id: "pinned", origin: host.OriginPool, status: "running", protected: true})

	written, err := repo.ExtendLease(t.Context(), "pinned", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ExtendLease: %v", err)
	}
	if written {
		t.Error("ExtendLease wrote a lease onto a protected host")
	}
	if lease := leaseOf(t, db, "pinned"); lease.Valid {
		t.Errorf("lease = %v, want NULL; the protected encoding must survive", lease.Time)
	}
}

// The renewal's trail commits with the lease it describes, and a refused
// renewal leaves neither.
//
// The second half is what the shared transaction buys. Host rows hard-delete,
// so this entry is the only durable record that a host outlived its original
// TTL; an entry written outside the transaction would claim renewals that never
// happened, and one written through the buffered sink would be dropped under
// exactly the load worth recording.
func TestRescueCommitsItsTrailWithTheLease(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)
	audits := spannerplatform.NewAuditRepository(db.Client)
	svc := createService(t, db)
	seedStandbyHost(t, db, standbySeed{id: "renewed", origin: host.OriginPool, status: "running", expiresIn: draining, age: time.Hour})
	seedStandbyHost(t, db, standbySeed{id: "promised", origin: host.OriginPool, status: "running", expiresIn: 6 * time.Hour, age: time.Hour})

	if _, err := svc.RescueFleetHosts(ctx, standbyLead, defaultPoolGap(4)); err != nil {
		t.Fatalf("RescueFleetHosts: %v", err)
	}
	entries := leaseEntries(t, db, audit.ActionHostLeaseExtend)
	if len(entries) != 1 {
		t.Fatalf("lease-renewal entries = %d, want 1 immediately after the renewal returned", len(entries))
	}
	if entries[0].ResourceID == nil || *entries[0].ResourceID != "renewed" {
		t.Errorf("entry resource = %v, want the renewed host", entries[0].ResourceID)
	}

	// A refused renewal writes nothing, entry included. The entry is built
	// before the transaction can know whether the row still wants a renewal, so
	// it is the shared commit and nothing else that keeps the trail accurate.
	// Write it separately and the log claims renewals that never happened.
	mut, err := audits.EntryMutation(audit.New(audit.ActionHostLeaseExtend, audit.ResourceHost).
		WithResource("promised").Build())
	if err != nil {
		t.Fatalf("building the entry: %v", err)
	}
	written, err := repo.ExtendLease(ctx, "promised", time.Now().Add(time.Hour), mut)
	if err != nil {
		t.Fatalf("ExtendLease: %v", err)
	}
	if written {
		t.Fatal("ExtendLease wrote a target below the current lease")
	}
	if after := leaseEntries(t, db, audit.ActionHostLeaseExtend); len(after) != 1 {
		t.Errorf("lease-renewal entries = %d after a refused renewal, want the original 1", len(after))
	}
}

// leaseEntries reads back every entry for one lease action, strongly: a stale
// read would tolerate an entry that had not committed, which is the difference
// these assertions exist to detect.
func leaseEntries(t *testing.T, db *spannertest.TestDB, action audit.Action) []audit.Entry {
	t.Helper()
	entries, err := spannerplatform.NewAuditRepository(db.Client).
		Query(t.Context(), audit.ListFilter{Action: &action})
	if err != nil {
		t.Fatalf("reading %s entries: %v", action, err)
	}
	return entries
}

// A host that is renewed comes back as inventory. The rescue exists so that
// the count the keeper subtracts its deficit from moves.
func TestRescueReturnsTheHostToStandbyInventory(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)
	svc := createService(t, db)
	seedStandbyHost(t, db, standbySeed{id: "draining-one", origin: host.OriginPool, status: "running", expiresIn: draining, age: time.Hour})

	before, err := repo.StandbyInventory(ctx, standbyLead)
	if err != nil {
		t.Fatalf("StandbyInventory: %v", err)
	}
	if before.Fleet().Standby != 0 {
		t.Fatalf("standby before = %d, want 0; the host is draining", before.Fleet().Standby)
	}
	if _, err := svc.RescueFleetHosts(ctx, standbyLead, defaultPoolGap(1)); err != nil {
		t.Fatalf("RescueFleetHosts: %v", err)
	}
	after, err := repo.StandbyInventory(ctx, standbyLead)
	if err != nil {
		t.Fatalf("StandbyInventory: %v", err)
	}
	if after.Fleet().Standby != 1 {
		t.Errorf("standby after = %d, want 1; a renewal that leaves the host out of inventory bought nothing",
			after.Fleet().Standby)
	}
}

// The renewal is bounded per pool through the same window form the shorten uses,
// and the service renews at most each pool's OWN deficit out of the rows it gets.
//
// The mutation this refuses is a renewal that answers one pool's deficit with
// another pool's draining machine: the pool that was short stays short, and the
// pool that was not gains a machine nobody asked it to keep. A fleet-wide count
// cannot see it: two renewals happened either way.
func TestRescueRenewsInsideEachPoolsOwnDeficit(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	svc := createService(t, db)
	for _, pool := range []string{host.PoolDefault, "gpu"} {
		for _, id := range []string{pool + "-a", pool + "-b"} {
			seedStandbyHost(t, db, standbySeed{
				id: id, origin: host.OriginPool, status: "running",
				pool: pool, expiresIn: draining, age: time.Hour,
			})
		}
	}

	// The default pool is short by two and the gpu pool by one, so the listing
	// is asked for two per pool and the gpu pool must still stop at one.
	rescued, err := svc.RescueFleetHosts(ctx, standbyLead, map[string]int64{
		host.PoolDefault: 2,
		"gpu":            1,
	})
	if err != nil {
		t.Fatalf("RescueFleetHosts: %v", err)
	}
	if rescued[host.PoolDefault] != 2 {
		t.Errorf("renewed %v in the default pool, want 2, its own deficit", rescued)
	}
	if rescued["gpu"] != 1 {
		t.Errorf("renewed %v in the gpu pool, want 1; its own deficit bounds it, not the fleet's", rescued)
	}
}

// The limit is the caller's deficit, and renewing past it would overshoot the
// target with hosts the fleet was about to stop paying for.
func TestRescueStopsAtTheDeficit(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	svc := createService(t, db)
	for _, id := range []string{"d1", "d2", "d3"} {
		seedStandbyHost(t, db, standbySeed{id: id, origin: host.OriginPool, status: "running", expiresIn: draining, age: time.Hour})
	}

	rescued, err := svc.RescueFleetHosts(ctx, standbyLead, defaultPoolGap(2))
	if err != nil {
		t.Fatalf("RescueFleetHosts: %v", err)
	}
	if rescued[host.PoolDefault] != 2 {
		t.Errorf("rescued = %v, want 2 in the default pool; the deficit is the bound", rescued)
	}
}
