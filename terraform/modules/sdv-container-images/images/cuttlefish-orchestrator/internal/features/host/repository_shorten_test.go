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
	"fmt"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// settled is an age past host.MinShortenAge: every shorten candidate must be old
// enough that its emptiness means drained rather than still filling.
const settled = time.Hour

// standing is a lease long enough to count as standby inventory, which is where
// surplus comes from.
const standing = 2 * time.Hour

// shortenGrace is the lease a shortened host keeps in these tests. It is the
// compiled default, and it is far below the standby lease above by design:
// the drain grace is bounded strictly under the provisioning lead, which is what
// stops a shortened host still counting as standby and the surplus loop walking
// the whole fleet.
const shortenGrace = 8 * time.Minute

// seedDeviceRow plants one device row on a host WITHOUT touching the host's
// current_device_count. The counter is what the candidate listing filters on,
// and the rows are what the shorten guard reads.
// A fixture that moved both together could never tell the two apart.
func seedDeviceRow(t *testing.T, db *spannertest.TestDB, hostID, deviceID, status string, expires *time.Time) {
	t.Helper()
	now := time.Now()
	row := map[string]any{
		"host_id": hostID, "device_id": deviceID, "name": deviceID,
		"status": status, "created_at": now, "updated_at": now,
	}
	if expires != nil {
		row["lifecycle_mode"] = "ephemeral"
		row["expires_at"] = *expires
	}
	if _, err := db.Client.Apply(t.Context(), []*spanner.Mutation{spanner.InsertMap("devices", row)}); err != nil {
		t.Fatalf("seed device %s on %s: %v", deviceID, hostID, err)
	}
}

// The surplus population is the fleet's own standing inventory and nothing else.
// Every other row satisfies some of the terms (one is a user's, one unstamped,
// one batch, one exclusive, one busy, one degraded, one already draining, one
// protected, one too young), so a predicate that dropped any single term offers
// somebody else's host, or a host still being filled, as the fleet's surplus.
func TestListShortenableIsTheFleetsStandingInventoryAlone(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)

	seedStandbyHost(t, db, standbySeed{id: "fleet-standing", origin: host.OriginPool, status: "running", expiresIn: standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "user-standing", origin: host.OriginUser, status: "running", expiresIn: standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "unstamped-standing", status: "running", expiresIn: standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "batch-standing", origin: host.OriginBatch, status: "running", expiresIn: standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "dedicated-standing", origin: host.OriginAuto, status: "running", exclusive: true, expiresIn: standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "busy-standing", origin: host.OriginPool, status: "running", devices: 1, expiresIn: standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "degraded-standing", origin: host.OriginPool, status: "running", degraded: true, expiresIn: standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "already-draining", origin: host.OriginPool, status: "running", expiresIn: 5 * time.Minute, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "pinned", origin: host.OriginPool, status: "running", protected: true, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "just-provisioned", origin: host.OriginPool, status: "running", expiresIn: standing})

	hosts, err := repo.ListShortenable(t.Context(), standbyLead, 10)
	if err != nil {
		t.Fatalf("ListShortenable: %v", err)
	}
	if got := idsOf(hosts); len(got) != 1 || got[0] != "fleet-standing" {
		t.Errorf("ListShortenable = %v, want [fleet-standing] alone", got)
	}
}

// A protected host is filtered out by the LISTING, not left to the write's guard
// to infer.
//
// The standby terms this listing is built on admit expires_at IS NULL, and NULL
// is exactly the protected encoding, so without the ephemeral and unprotected
// terms a pinned host is a shorten candidate whose only defense is the
// live-device guard reasoning three steps later that protection implies a pinned
// device implies a live row. Filtered here, the write's NULL branch is
// unambiguous: a NULL lease reaching it means drift with nothing pinned on it.
func TestListShortenableNeverListsAProtectedHost(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyHost(t, db, standbySeed{id: "pinned", origin: host.OriginPool, status: "running", protected: true, age: settled})

	hosts, err := repo.ListShortenable(t.Context(), standbyLead, 10)
	if err != nil {
		t.Fatalf("ListShortenable: %v", err)
	}
	if len(hosts) != 0 {
		t.Errorf("ListShortenable = %v, want none; a protected host is not surplus", idsOf(hosts))
	}
}

// A host is empty from the moment its row is inserted until a device commits
// onto it, and a whole provisioning leg separates the two. Emptiness younger
// than host.MinShortenAge therefore means "create in flight" and not "drained".
// Shortening there would cut the lease of the very host the create is filling.
func TestListShortenableSparesYoungHosts(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)

	seedStandbyHost(t, db, standbySeed{
		id: "settled", origin: host.OriginPool, status: "running",
		expiresIn: standing, age: host.MinShortenAge + time.Minute,
	})
	seedStandbyHost(t, db, standbySeed{
		id: "still-filling", origin: host.OriginPool, status: "running",
		expiresIn: standing, age: host.MinShortenAge - time.Minute,
	})

	hosts, err := repo.ListShortenable(t.Context(), standbyLead, 10)
	if err != nil {
		t.Fatalf("ListShortenable: %v", err)
	}
	if got := idsOf(hosts); len(got) != 1 || got[0] != "settled" {
		t.Errorf("ListShortenable = %v, want [settled]; a host still being filled is not surplus", got)
	}
}

// Reclamation gives up the coldest host, which is the exact mirror of renewal
// keeping the warmest: ledger-row count ascending, with the NEAREST expiry
// breaking the tie against renewal's longest. One ordering read from both ends.
//
// The tie row is what makes the direction observable rather than incidental: an
// implementation that reused renewal's DESC/DESC returns this list reversed.
func TestListShortenableTakesTheColdestFirst(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)

	seedStandbyHost(t, db, standbySeed{id: "cold", origin: host.OriginPool, status: "running", expiresIn: standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "cold-but-sooner", origin: host.OriginPool, status: "running", expiresIn: standing - time.Minute, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "warm", origin: host.OriginPool, status: "running", expiresIn: standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "warmest", origin: host.OriginPool, status: "running", expiresIn: standing, age: settled})
	seedLedger(t, db, "warm", "@ab/1/t")
	seedLedger(t, db, "warmest", "@ab/1/t", "@ab/2/t")

	hosts, err := repo.ListShortenable(t.Context(), standbyLead, 10)
	if err != nil {
		t.Fatalf("ListShortenable: %v", err)
	}
	want := []string{"cold-but-sooner", "cold", "warm", "warmest"}
	got := idsOf(hosts)
	if len(got) != len(want) {
		t.Fatalf("ListShortenable = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListShortenable = %v, want %v (coldest first, nearest expiry breaking the tie)", got, want)
		}
	}
}

// The budget is spent PER POOL, and it is the window function that makes that
// one statement: each pool's candidates are ranked inside it, and the bound is
// on the rank rather than on the result.
//
// Two pools, three settled candidates each, a budget of two: a fleet-wide LIMIT
// returns two rows from whichever pool sorts first and leaves the other pool
// unable to reclaim anything at all, every cycle, for as long as the first pool
// has surplus. The ordering inside each pool is the same coldest-first rule, so
// this also pins that ranking per pool rather than across the fleet.
//
// The window form is proven here against the emulator. Acceptance on real
// Spanner with bound parameters is a separate step the emulator cannot stand in
// for; the null-filtered-index hints set that precedent.
func TestListShortenableSpendsItsBudgetInEachPool(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)

	for _, pool := range []string{host.PoolDefault, "gpu"} {
		for i, id := range []string{pool + "-cold", pool + "-warm", pool + "-warmest"} {
			seedStandbyHost(t, db, standbySeed{
				id: id, origin: host.OriginPool, status: "running",
				pool: pool, expiresIn: standing, age: settled,
			})
			for k := range i {
				seedLedger(t, db, id, fmt.Sprintf("@ab/%d/t", k))
			}
		}
	}

	hosts, err := repo.ListShortenable(t.Context(), standbyLead, 2)
	if err != nil {
		t.Fatalf("ListShortenable: %v", err)
	}
	byPool := map[string][]string{}
	for _, h := range hosts {
		byPool[host.NormalizePool(h.Pool)] = append(byPool[host.NormalizePool(h.Pool)], h.ID)
	}
	for _, pool := range []string{host.PoolDefault, "gpu"} {
		want := []string{pool + "-cold", pool + "-warm"}
		got := byPool[pool]
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("the %s pool offered %v, want %v; the budget is per pool, coldest first", pool, got, want)
		}
	}
}

// The guard reads device ROWS, and the listing filters on current_device_count.
// That divergence is the whole design: a guard reading the same column the
// selection read agrees with it by construction and can only fail where the
// counter has drifted, which is the one case it exists to catch.
//
// Here the device lands after the candidate was listed, exactly as it does in
// production between a cycle's read and its write. The counter still says zero.
// A guard gated on the counter shortens the lease of a host with a live tenant
// on it; the row-based guard refuses.
func TestShortenRefusesAHostThatGainedADeviceAfterListing(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyHost(t, db, standbySeed{id: "raced", origin: host.OriginPool, status: "running", expiresIn: standing, age: settled})

	hosts, err := repo.ListShortenable(ctx, standbyLead, 10)
	if err != nil {
		t.Fatalf("ListShortenable: %v", err)
	}
	if got := idsOf(hosts); len(got) != 1 || got[0] != "raced" {
		t.Fatalf("ListShortenable = %v, want [raced] before the race", got)
	}

	before := leaseOf(t, db, "raced")
	seedDeviceRow(t, db, "raced", "landed", "running", nil)

	written, err := repo.ShortenLease(ctx, "raced", time.Now().Add(shortenGrace))
	if err != nil {
		t.Fatalf("ShortenLease: %v", err)
	}
	if written {
		t.Error("ShortenLease cut the lease of a host holding a live device")
	}
	if after := leaseOf(t, db, "raced"); !after.Time.Equal(before.Time) {
		t.Errorf("lease moved from %v to %v under a live device", before.Time, after.Time)
	}
}

// The promise a device holds outlives any surplus arithmetic: a host must
// outlive every device on it by host.GracePeriodSeconds, which is what the
// device-side monotone raise writes when a device commits.
//
// The assertion is against GracePeriodSeconds and NOT the drain grace, and the
// difference is the whole value of the test: the drain grace is what a shorten
// writes, so asserting the lease against it passes no matter what the guard
// does. Fifteen minutes is what the DEVICE was promised.
//
// Error and tearing-down rows do not count. They back no CVD (an error row is a
// failed create, which is poison evidence rather than a tenant), so a host
// holding only those is truly drained and is reclaimed.
func TestShortenNeverStrandsALiveDevicesPromise(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)

	// The post-commit state the device path leaves behind: the device's own
	// expiry, and a host lease raised to that plus the teardown margin.
	devicePromise := time.Now().Add(90 * time.Minute)
	hostFloor := devicePromise.Add(time.Duration(host.GracePeriodSeconds) * time.Second)
	seedStandbyHost(t, db, standbySeed{id: "tenanted", origin: host.OriginPool, status: "running", age: settled})
	if _, err := db.Client.Apply(ctx, []*spanner.Mutation{spanner.Update("hosts",
		[]string{"host_id", "expires_at"}, []any{"tenanted", hostFloor})}); err != nil {
		t.Fatalf("raising the host lease to the device promise: %v", err)
	}
	seedDeviceRow(t, db, "tenanted", "tenant", "running", &devicePromise)

	// A second host carrying only dead rows, to prove the guard counts live
	// tenants rather than device rows.
	seedStandbyHost(t, db, standbySeed{id: "dead-rows", origin: host.OriginPool, status: "running", expiresIn: standing, age: settled})
	seedDeviceRow(t, db, "dead-rows", "failed", "error", nil)
	seedDeviceRow(t, db, "dead-rows", "going", "deleting", nil)

	target := time.Now().Add(shortenGrace)
	written, err := repo.ShortenLease(ctx, "tenanted", target)
	if err != nil {
		t.Fatalf("ShortenLease: %v", err)
	}
	if written {
		t.Error("ShortenLease lowered the lease of a host with a live device")
	}
	lease := leaseOf(t, db, "tenanted")
	if !lease.Valid || lease.Time.Before(hostFloor) {
		t.Errorf("lease = %v, want at least %v; the device was promised %v plus the %ds teardown margin",
			lease.Time, hostFloor, devicePromise, host.GracePeriodSeconds)
	}

	written, err = repo.ShortenLease(ctx, "dead-rows", target)
	if err != nil {
		t.Fatalf("ShortenLease on dead rows: %v", err)
	}
	if !written {
		t.Error("ShortenLease spared a host holding only error and deleting rows")
	}
}

// A protected host's NULL lease survives a shorten aimed straight at it. NULL is
// the encoding a pinned device writes, not an absent value, so a timestamp
// written onto it converts a protected host into an expiring one.
//
// The assertion is on the LEASE VALUE, because the reaper filters on
// lifecycle_mode and protected_device_count and would never collect this host
// whatever expires_at says: a test asserting the host survives passes with the
// encoding already corrupted.
func TestShortenLeavesAProtectedLeaseNull(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyHost(t, db, standbySeed{id: "pinned", origin: host.OriginPool, status: "running", protected: true, age: settled})
	seedDeviceRow(t, db, "pinned", "pinned-tenant", "running", nil)

	written, err := repo.ShortenLease(t.Context(), "pinned", time.Now().Add(shortenGrace))
	if err != nil {
		t.Fatalf("ShortenLease: %v", err)
	}
	if written {
		t.Error("ShortenLease wrote a lease onto a protected host")
	}
	if lease := leaseOf(t, db, "pinned"); lease.Valid {
		t.Errorf("lease = %v, want NULL; the protected encoding must survive", lease.Time)
	}
}

// An unprotected host with no lease at all is drift, and it is shortened to an
// explicit timestamp. Reaching for LEAST here is the trap: LEAST(NULL, x) is
// NULL, so the write would silently no-op, the host would stay in the surplus
// forever, and it would eat a slot of the per-cycle cap on every cycle.
func TestShortenWritesOntoALeaselessHost(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	// Ephemeral and unprotected, with no expiry: not the protected encoding.
	seedStandbyHost(t, db, standbySeed{id: "immortal", origin: host.OriginPool, status: "running", age: settled})

	target := time.Now().Add(shortenGrace)
	written, err := repo.ShortenLease(t.Context(), "immortal", target)
	if err != nil {
		t.Fatalf("ShortenLease: %v", err)
	}
	if !written {
		t.Fatal("ShortenLease refused a host with no lease at all")
	}
	lease := leaseOf(t, db, "immortal")
	if !lease.Valid {
		t.Fatal("lease is still NULL; a LEAST-based write no-ops here forever")
	}
	if lease.Time.Sub(target).Abs() > time.Second {
		t.Errorf("lease = %v, want about %v", lease.Time, target)
	}
}

// Reclamation only ever moves an expiry backward. A host whose lease is already
// shorter than the grace a shorten would leave is untouched, so the write is
// idempotent across cycles instead of nudging the same host forward each time.
func TestShortenNeverRaisesALease(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyHost(t, db, standbySeed{id: "nearly-done", origin: host.OriginPool, status: "running", expiresIn: time.Minute, age: settled})

	before := leaseOf(t, db, "nearly-done")
	written, err := repo.ShortenLease(t.Context(), "nearly-done", time.Now().Add(shortenGrace))
	if err != nil {
		t.Fatalf("ShortenLease: %v", err)
	}
	if written {
		t.Error("ShortenLease pushed an expiry further out")
	}
	if after := leaseOf(t, db, "nearly-done"); !after.Time.Equal(before.Time) {
		t.Errorf("lease moved from %v to %v", before.Time, after.Time)
	}
}

// The shorten's trail commits with the lease it describes, and a refused shorten
// leaves none.
//
// Host rows hard-delete, so this entry is the only durable answer to why a host
// died at a given minute, and unlike the renewal's it records the decision
// that CAUSED the death. Written outside the transaction it would claim
// reclamations that never happened; written through the buffered sink it would
// be dropped under exactly the load worth recording.
func TestShortenCommitsItsTrailWithTheLease(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	svc := createService(t, db)
	seedStandbyHost(t, db, standbySeed{id: "surplus", origin: host.OriginPool, status: "running", expiresIn: standing, age: settled})

	shortened, err := svc.ShortenSurplusHosts(ctx, standbyLead, defaultPoolGap(1), shortenGrace, nil)
	if err != nil {
		t.Fatalf("ShortenSurplusHosts: %v", err)
	}
	if shortened != 1 {
		t.Fatalf("shortened = %d, want 1", shortened)
	}
	entries := leaseEntries(t, db, audit.ActionHostLeaseShorten)
	if len(entries) != 1 {
		t.Fatalf("lease-shorten entries = %d, want 1 immediately after the shorten returned", len(entries))
	}
	if entries[0].ResourceID == nil || *entries[0].ResourceID != "surplus" {
		t.Errorf("entry resource = %v, want the shortened host", entries[0].ResourceID)
	}
	// The lease it replaced is the one fact the row's deletion destroys, and
	// nothing else in the trail carries an expiry to compare against.
	if _, ok := entries[0].Metadata["previous_expires_at"]; !ok {
		t.Error("the entry does not record the lease it replaced")
	}

	// A second cycle over the now-shortened host writes neither lease nor entry.
	if _, err := svc.ShortenSurplusHosts(ctx, standbyLead, defaultPoolGap(1), shortenGrace, nil); err != nil {
		t.Fatalf("second ShortenSurplusHosts: %v", err)
	}
	if after := leaseEntries(t, db, audit.ActionHostLeaseShorten); len(after) != 1 {
		t.Errorf("lease-shorten entries = %d after a refused shorten, want the original 1", len(after))
	}
}

// The surplus is the bound: shortening past it would cut below the target the
// operator asked for and leave the fleet buying the hosts back next cycle.
func TestShortenStopsAtTheSurplus(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	svc := createService(t, db)
	for _, id := range []string{"s1", "s2", "s3"} {
		seedStandbyHost(t, db, standbySeed{id: id, origin: host.OriginPool, status: "running", expiresIn: standing, age: settled})
	}

	shortened, err := svc.ShortenSurplusHosts(ctx, standbyLead, defaultPoolGap(2), shortenGrace, nil)
	if err != nil {
		t.Fatalf("ShortenSurplusHosts: %v", err)
	}
	if shortened != 2 {
		t.Errorf("shortened = %d, want 2; the surplus is the bound", shortened)
	}
}

// A pool gives back only its own surplus. One pool spare and another exactly at
// its target: the reclamation takes machines from the first and none from the
// second, whatever the other pool's rows look like.
//
// The mutation this refuses is a shorten that spends one pool's surplus on the
// coldest machines in the fleet: the pool nobody asked to shrink loses capacity,
// and a count of what was reclaimed reads correct either way.
func TestShortenGivesBackOnlyThePoolsOwnSurplus(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	svc := createService(t, db)
	// The gpu pool's machines are colder than the default pool's, so a
	// fleet-wide ordering would take them first.
	seedStandbyHost(t, db, standbySeed{
		id: "gpu-cold", origin: host.OriginPool, status: "running", pool: "gpu",
		expiresIn: standing, age: settled,
	})
	for _, id := range []string{"default-a", "default-b"} {
		seedStandbyHost(t, db, standbySeed{
			id: id, origin: host.OriginPool, status: "running", pool: host.PoolDefault,
			expiresIn: standing, age: settled,
		})
		seedLedger(t, db, id, "@ab/1/t")
	}

	shortened, err := svc.ShortenSurplusHosts(ctx, standbyLead, defaultPoolGap(2), shortenGrace, nil)
	if err != nil {
		t.Fatalf("ShortenSurplusHosts: %v", err)
	}
	if shortened != 2 {
		t.Errorf("shortened = %d, want 2; the default pool's own surplus", shortened)
	}
	if lease := leaseOf(t, db, "gpu-cold"); !lease.Valid || time.Until(lease.Time) < standing-time.Minute {
		t.Errorf("the gpu pool's machine was reclaimed for the default pool's surplus: lease %v", lease)
	}
}

// A host mid-bake is spared, and the exemption is keyed to the disk rather than
// the host.
//
// A bake writes no artifact-ledger rows until it finishes, so the host closest to
// becoming warm reads as the coldest in the fleet and sorts first for
// destruction. The incarnation must match: a bump is a fresh cold disk, and an
// exemption that ignored it would spare a host for a bake whose artifacts are
// gone. The over-read is what lets the truly cold host below it still be
// found instead of the cycle shortening nothing.
func TestShortenSparesAHostMidBake(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	svc := createService(t, db)
	seedStandbyHost(t, db, standbySeed{id: "baking", origin: host.OriginPool, status: "running", expiresIn: standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "truly-cold", origin: host.OriginPool, status: "running", expiresIn: standing + time.Minute, age: settled})

	shortened, err := svc.ShortenSurplusHosts(ctx, standbyLead, defaultPoolGap(1), shortenGrace, map[string]int64{"baking": 0})
	if err != nil {
		t.Fatalf("ShortenSurplusHosts: %v", err)
	}
	if shortened != 1 {
		t.Fatalf("shortened = %d, want 1", shortened)
	}
	if lease := leaseOf(t, db, "baking"); lease.Time.Sub(time.Now().Add(standing)).Abs() > time.Minute {
		t.Errorf("the baking host's lease moved to %v; it was mid-bake and should have been spared", lease.Time)
	}

	// A stale incarnation exempts nothing: that disk is gone.
	shortened, err = svc.ShortenSurplusHosts(ctx, standbyLead, defaultPoolGap(1), shortenGrace, map[string]int64{"baking": 7})
	if err != nil {
		t.Fatalf("ShortenSurplusHosts with a stale incarnation: %v", err)
	}
	if shortened != 1 {
		t.Errorf("shortened = %d, want 1; an exemption for a disk that no longer exists spares nobody", shortened)
	}
}

// The collapse population is empty dedicated hosts and nothing else. Every other
// row fails exactly one term (a user's exclusive host, an unstamped one, a
// batch host, an ordinary fleet host, a protected one, one too young, one whose
// group is still running, one already collapsed, one already being deleted), so
// dropping any single term reclaims a host somebody is relying on, or one whose
// group has not landed yet.
//
// The user's exclusive host is the row that matters most. exclusive is a field
// USERS set, to fence a host from placement while a test runs on it; keyed on
// exclusivity rather than on the class, this statement takes that host away
// between the very runs it was fenced for.
func TestListCollapsibleIsEmptyDedicatedHostsAlone(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)

	seedStandbyHost(t, db, standbySeed{id: "dedicated-empty", origin: host.OriginAuto, status: "running", exclusive: true, expiresIn: standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "user-exclusive", origin: host.OriginUser, status: "running", exclusive: true, expiresIn: standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "unstamped-exclusive", status: "running", exclusive: true, expiresIn: standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "batch-exclusive", origin: host.OriginBatch, status: "running", exclusive: true, expiresIn: standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "fleet-shareable", origin: host.OriginPool, status: "running", expiresIn: standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "dedicated-pinned", origin: host.OriginAuto, status: "running", exclusive: true, protected: true, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "dedicated-young", origin: host.OriginAuto, status: "running", exclusive: true, expiresIn: standing})
	seedStandbyHost(t, db, standbySeed{id: "dedicated-deleting", origin: host.OriginAuto, status: "deleting", exclusive: true, expiresIn: standing, age: settled})
	// Already collapsed: its lease is inside the lead, so a further write would
	// refuse. Listing it again spends the cycle's budget on nothing.
	seedStandbyHost(t, db, standbySeed{id: "dedicated-collapsed", origin: host.OriginAuto, status: "running", exclusive: true, expiresIn: 5 * time.Minute, age: settled})
	// Its group is still running, which is what a dedicated host is FOR.
	seedStandbyHost(t, db, standbySeed{id: "dedicated-serving", origin: host.OriginAuto, status: "running", exclusive: true, expiresIn: standing, age: settled})
	seedDeviceRow(t, db, "dedicated-serving", "member-1", "running", nil)

	hosts, err := repo.ListCollapsible(t.Context(), standbyLead, 10)
	if err != nil {
		t.Fatalf("ListCollapsible: %v", err)
	}
	if got := idsOf(hosts); len(got) != 1 || got[0] != "dedicated-empty" {
		t.Errorf("ListCollapsible = %v, want [dedicated-empty] alone", got)
	}
}

// The two hosts the fleet's own inventory terms would hide, and the reason this
// listing is written from scratch rather than narrowed from the standby set.
//
// A degraded host is one placement refuses, so a degraded empty dedicated host is
// the most expensive row in the system: unreachable, unreclaimed by anything
// else, billing its whole lease. A host holding only error rows is the same
// story arrived at differently: current_device_count includes those rows, so a
// counted predicate reads it as occupied forever, while what it actually holds
// is the wreckage of a group that never started.
//
// Both are reclaimed here. Building this listing on standbyTerms (the obvious
// symmetry with the surplus, and the shape first described) silently drops
// exactly these two.
func TestListCollapsibleFindsWhatTheInventoryTermsWouldHide(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)

	seedStandbyHost(t, db, standbySeed{
		id: "dedicated-degraded", origin: host.OriginAuto, status: "running",
		exclusive: true, degraded: true, expiresIn: standing, age: settled,
	})
	// The counter says two devices; both rows are wreckage. A listing gated on
	// current_device_count never sees this host again.
	seedStandbyHost(t, db, standbySeed{
		id: "dedicated-wreckage", origin: host.OriginAuto, status: "running",
		exclusive: true, devices: 2, expiresIn: standing, age: settled,
	})
	seedDeviceRow(t, db, "dedicated-wreckage", "failed-1", "error", nil)
	seedDeviceRow(t, db, "dedicated-wreckage", "failed-2", "error", nil)

	hosts, err := repo.ListCollapsible(t.Context(), standbyLead, 10)
	if err != nil {
		t.Fatalf("ListCollapsible: %v", err)
	}
	got := idsOf(hosts)
	if len(got) != 2 {
		t.Fatalf("ListCollapsible = %v, want both the degraded and the error-row host", got)
	}
	seen := map[string]bool{got[0]: true, got[1]: true}
	if !seen["dedicated-degraded"] {
		t.Error("a degraded empty dedicated host was not offered: nothing can place on it and nothing else reclaims it")
	}
	if !seen["dedicated-wreckage"] {
		t.Error("a dedicated host holding only error rows was not offered: the counter is not what emptiness means here")
	}
}

// The lease floor, which the surplus listing inherits and this one must state.
//
// Without it a host this cycle collapsed is a candidate again next cycle and
// every cycle after: the write refuses, so nothing changes, and the per-cycle
// budget is spent on refusals forever while truly idle hosts below it are
// never reached. The refusal makes it silent: no error, no metric, no log.
//
// A NULL lease is admitted alongside, and the same statement has to do both.
// Written as a bare comparison the floor is NULL for that row and drops it, and
// a leaseless ephemeral host is the one case nothing else in the system can
// repair: the reaper requires a lease, so it never expires at all.
func TestListCollapsibleSkipsWhatItAlreadyCollapsed(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)

	seedStandbyHost(t, db, standbySeed{id: "already-collapsed", origin: host.OriginAuto, status: "running", exclusive: true, expiresIn: shortenGrace, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "leaseless", origin: host.OriginAuto, status: "running", exclusive: true, age: settled})

	hosts, err := repo.ListCollapsible(t.Context(), standbyLead, 10)
	if err != nil {
		t.Fatalf("ListCollapsible: %v", err)
	}
	if got := idsOf(hosts); len(got) != 1 || got[0] != "leaseless" {
		t.Errorf("ListCollapsible = %v, want [leaseless]; a collapsed host must leave the candidate set, a leaseless one must not", got)
	}
}

// The most expensive host goes first, because here the saving IS the remaining
// lease. Warmth cannot decide this: the warm plane bakes onto shareable hosts
// only, so every dedicated host has a warmth of zero and reusing either of the
// other orderings leaves a budgeted query returning an arbitrary subset.
//
// The leaseless host leads, which is the same rule and not an exception: nothing
// reaps a host with no lease, so its remaining life is unbounded. Ordered by
// expiry alone it sorts last (NULL is the smallest value under DESC) and a
// steady supply of ordinary candidates starves the one host that will otherwise
// live forever.
func TestListCollapsibleTakesTheLongestLeaseFirst(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)

	seedStandbyHost(t, db, standbySeed{id: "shortest", origin: host.OriginAuto, status: "running", exclusive: true, expiresIn: standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "longest", origin: host.OriginAuto, status: "running", exclusive: true, expiresIn: 3 * standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "middling", origin: host.OriginAuto, status: "running", exclusive: true, expiresIn: 2 * standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "immortal", origin: host.OriginAuto, status: "running", exclusive: true, age: settled})

	hosts, err := repo.ListCollapsible(t.Context(), standbyLead, 10)
	if err != nil {
		t.Fatalf("ListCollapsible: %v", err)
	}
	want := []string{"immortal", "longest", "middling", "shortest"}
	got := idsOf(hosts)
	if len(got) != len(want) {
		t.Fatalf("ListCollapsible = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListCollapsible = %v, want %v (the most lease left goes first, leaseless ahead of all)", got, want)
		}
	}
}

// The collapse's trail commits with the lease it describes, and a refused
// collapse leaves none, the same contract the surplus shorten holds, asserted
// separately because it reaches the write down a different path.
//
// The metadata values are spelled out here rather than read from the constants
// they come from. These strings are the durable record, read back long after
// the host row is gone, so a rename that reclassified every historical entry
// would pass a test written against the constant.
func TestCollapseCommitsItsTrailWithTheLease(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	svc := createService(t, db)
	seedStandbyHost(t, db, standbySeed{id: "fenced", origin: host.OriginAuto, status: "running", exclusive: true, expiresIn: standing, age: settled})

	collapsed, err := svc.CollapseEmptyDedicated(ctx, standbyLead, 1, shortenGrace)
	if err != nil {
		t.Fatalf("CollapseEmptyDedicated: %v", err)
	}
	if collapsed != 1 {
		t.Fatalf("collapsed = %d, want 1", collapsed)
	}
	entries := leaseEntries(t, db, audit.ActionHostLeaseShorten)
	if len(entries) != 1 {
		t.Fatalf("lease-shorten entries = %d, want 1 immediately after the collapse returned", len(entries))
	}
	if entries[0].ResourceID == nil || *entries[0].ResourceID != "fenced" {
		t.Errorf("entry resource = %v, want the collapsed host", entries[0].ResourceID)
	}
	if got, _ := entries[0].Metadata["reclaim"].(string); got != "dedicated_empty" {
		t.Errorf("reclaim = %q, want dedicated_empty; the trail cannot tell why the lease was cut", got)
	}
	if _, ok := entries[0].Metadata["previous_expires_at"]; !ok {
		t.Error("the entry does not record the lease it replaced")
	}

	// A second cycle over the now-collapsed host writes neither lease nor entry.
	if _, err := svc.CollapseEmptyDedicated(ctx, standbyLead, 1, shortenGrace); err != nil {
		t.Fatalf("second CollapseEmptyDedicated: %v", err)
	}
	if after := leaseEntries(t, db, audit.ActionHostLeaseShorten); len(after) != 1 {
		t.Errorf("lease-shorten entries = %d after a refused collapse, want the original 1", len(after))
	}
}

// Both reclamations name themselves, and neither leans on the key being absent.
//
// One action carries two decisions an operator asks different questions about,
// "the fleet was told to hold fewer" and "this group ended", and the host rows
// they describe are gone by the time anyone reads them. Letting one path mean
// "no reclaim key" would make the answer depend on which revision wrote the
// entry, and these entries are kept because they outlive revisions.
func TestBothReclamationsNameThemselvesInTheTrail(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	svc := createService(t, db)
	seedStandbyHost(t, db, standbySeed{id: "surplus", origin: host.OriginPool, status: "running", expiresIn: standing, age: settled})
	seedStandbyHost(t, db, standbySeed{id: "fenced", origin: host.OriginAuto, status: "running", exclusive: true, expiresIn: standing, age: settled})

	if _, err := svc.ShortenSurplusHosts(ctx, standbyLead, defaultPoolGap(1), shortenGrace, nil); err != nil {
		t.Fatalf("ShortenSurplusHosts: %v", err)
	}
	if _, err := svc.CollapseEmptyDedicated(ctx, standbyLead, 1, shortenGrace); err != nil {
		t.Fatalf("CollapseEmptyDedicated: %v", err)
	}

	want := map[string]string{"surplus": "fleet_surplus", "fenced": "dedicated_empty"}
	entries := leaseEntries(t, db, audit.ActionHostLeaseShorten)
	if len(entries) != 2 {
		t.Fatalf("lease-shorten entries = %d, want 2", len(entries))
	}
	for _, e := range entries {
		if e.ResourceID == nil {
			t.Fatalf("entry %v carries no resource", e)
		}
		got, _ := e.Metadata["reclaim"].(string)
		if got != want[*e.ResourceID] {
			t.Errorf("host %s recorded reclaim %q, want %q", *e.ResourceID, got, want[*e.ResourceID])
		}
	}
}

// A group that comes back between the listing and the write keeps its host. The
// collapse chooses on emptiness read a moment ago and the shared guard re-reads
// it under the write's own transaction, which is the only thing standing between
// "empty when we looked" and a running group losing the host underneath it.
func TestCollapseRefusesAHostWhoseGroupCameBack(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	svc := createService(t, db)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyHost(t, db, standbySeed{id: "raced", origin: host.OriginAuto, status: "running", exclusive: true, expiresIn: standing, age: settled})

	hosts, err := repo.ListCollapsible(ctx, standbyLead, 10)
	if err != nil {
		t.Fatalf("ListCollapsible: %v", err)
	}
	if got := idsOf(hosts); len(got) != 1 || got[0] != "raced" {
		t.Fatalf("ListCollapsible = %v, want [raced] before the race", got)
	}

	before := leaseOf(t, db, "raced")
	seedDeviceRow(t, db, "raced", "member-1", "running", nil)

	collapsed, err := svc.CollapseEmptyDedicated(ctx, standbyLead, 1, shortenGrace)
	if err != nil {
		t.Fatalf("CollapseEmptyDedicated: %v", err)
	}
	if collapsed != 0 {
		t.Errorf("collapsed = %d, want 0; the group is running on that host", collapsed)
	}
	if after := leaseOf(t, db, "raced"); !after.Time.Equal(before.Time) {
		t.Errorf("lease moved from %v to %v under a live group", before.Time, after.Time)
	}
}

// A dedicated host with no lease at all is repaired rather than left immortal.
// Nothing else can: the reaper requires a lease, so this row would otherwise
// outlive every mechanism in the system. The listing has to admit NULL and the
// write has to turn it into a timestamp, and only the two together get there.
func TestCollapseRepairsALeaselessDedicatedHost(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	svc := createService(t, db)
	seedStandbyHost(t, db, standbySeed{id: "immortal", origin: host.OriginAuto, status: "running", exclusive: true, age: settled})

	collapsed, err := svc.CollapseEmptyDedicated(ctx, standbyLead, 1, shortenGrace)
	if err != nil {
		t.Fatalf("CollapseEmptyDedicated: %v", err)
	}
	if collapsed != 1 {
		t.Fatalf("collapsed = %d, want 1; nothing else will ever reclaim a leaseless host", collapsed)
	}
	lease := leaseOf(t, db, "immortal")
	if !lease.Valid {
		t.Fatal("lease is still NULL; the host remains beyond the reaper's reach")
	}
	if want := time.Now().Add(shortenGrace); lease.Time.Sub(want).Abs() > time.Second {
		t.Errorf("lease = %v, want about %v", lease.Time, want)
	}
}

// The budget is the bound, and it bounds the LISTING as well as the writes: an
// unbudgeted read would walk every dedicated host in the fleet each cycle to
// then act on two of them.
func TestCollapseStopsAtItsBudget(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	svc := createService(t, db)
	for _, id := range []string{"f1", "f2", "f3"} {
		seedStandbyHost(t, db, standbySeed{id: id, origin: host.OriginAuto, status: "running", exclusive: true, expiresIn: standing, age: settled})
	}

	collapsed, err := svc.CollapseEmptyDedicated(ctx, standbyLead, 2, shortenGrace)
	if err != nil {
		t.Fatalf("CollapseEmptyDedicated: %v", err)
	}
	if collapsed != 2 {
		t.Errorf("collapsed = %d, want 2; the budget is the bound", collapsed)
	}
}

// The two directions compose: a host shortened as surplus is renewed back into
// inventory when demand returns inside its drain window, which is the fleet
// giving capacity back and taking it again without buying anything.
//
// This is the pair the whole design turns on, and it is also the sawtooth the
// warm plane's bookkeeping had to survive: the shortened host leaves the
// standby listing, and coming back it must not read as a cold, unbaked disk.
func TestAShortenedHostIsRenewedBackIntoInventory(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)
	svc := createService(t, db)
	seedStandbyHost(t, db, standbySeed{
		id: "sawtooth", origin: host.OriginPool, status: "running",
		expiresIn: standing, age: settled, ttlSeconds: host.DefaultHostTTLSeconds,
	})

	if _, err := svc.ShortenSurplusHosts(ctx, standbyLead, defaultPoolGap(1), shortenGrace, nil); err != nil {
		t.Fatalf("ShortenSurplusHosts: %v", err)
	}
	inventory, err := repo.StandbyInventory(ctx, standbyLead)
	if err != nil {
		t.Fatalf("StandbyInventory after the shorten: %v", err)
	}
	if inventory.Fleet().Standby != 0 {
		t.Fatalf("standby = %d after the shorten, want 0; a shortened host must leave inventory", inventory.Fleet().Standby)
	}

	rescued, err := svc.RescueFleetHosts(ctx, standbyLead, defaultPoolGap(1))
	if err != nil {
		t.Fatalf("RescueFleetHosts: %v", err)
	}
	if rescued[host.PoolDefault] != 1 {
		t.Fatalf("rescued = %v, want 1; a create landing inside the drain window must be able to take the host back", rescued)
	}
	inventory, err = repo.StandbyInventory(ctx, standbyLead)
	if err != nil {
		t.Fatalf("StandbyInventory after the renewal: %v", err)
	}
	if inventory.Fleet().Standby != 1 {
		t.Errorf("standby = %d after the renewal, want 1", inventory.Fleet().Standby)
	}
}
