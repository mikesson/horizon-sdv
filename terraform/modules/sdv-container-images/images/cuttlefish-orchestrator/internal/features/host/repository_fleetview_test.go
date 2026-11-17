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
	"strconv"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// byName keys a fleet view by host name for assertions about single rows.
func byName(t *testing.T, hosts []host.FleetHost) map[string]host.FleetHost {
	t.Helper()
	out := make(map[string]host.FleetHost, len(hosts))
	for _, h := range hosts {
		out[h.Name] = h
	}
	return out
}

// THE agreement test: the per-host verdict and the inventory count are the same
// predicate, so they must partition the same fleet the same way.
//
// This is the binding that makes the verdict safe to publish. The count is one
// COUNTIF statement and the verdict is six projected columns; both are built
// from the same term constants, and this is what proves the composition did not
// drift: a term dropped from one side changes a number here, on a fixture where
// every term fails on exactly one host.
//
// Both arms, because standby and draining are complements over the idle fleet
// and a verdict that agreed with one while disagreeing with the other would be
// wrong about exactly the hosts whose leases are running out.
func TestFleetViewVerdictAgreesWithTheInventoryCount(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyFleet(t, db)

	hosts, err := repo.ListFleetView(t.Context(), standbyLead)
	if err != nil {
		t.Fatalf("ListFleetView: %v", err)
	}
	pools, err := repo.StandbyInventory(t.Context(), standbyLead)
	if err != nil {
		t.Fatalf("StandbyInventory: %v", err)
	}
	// The whole fleet, because the view's verdicts are per host and say nothing
	// about which pool a host is in.
	inventory := pools.Fleet()

	var standby, draining int64
	for _, h := range hosts {
		if h.IsStandby() {
			standby++
			continue
		}
		// The draining arm is the inventory terms passing and the lease alone
		// failing, the exact complement inventorySQL's second COUNTIF applies.
		if h.ClassIsFleet && h.Placeable && h.NotDegraded && h.Shareable && h.Idle {
			draining++
		}
	}
	if standby != inventory.Standby {
		t.Errorf("verdict counts %d standby, the inventory statement counts %d: "+
			"the two copies of the predicate have drifted", standby, inventory.Standby)
	}
	if draining != inventory.Draining {
		t.Errorf("verdict counts %d draining, the inventory statement counts %d", draining, inventory.Draining)
	}
}

// Which term excluded a host is the answer the page exists to give, so it is
// asserted per host rather than in aggregate: a verdict that named the wrong
// term would still make the counts above agree.
func TestFleetViewNamesTheTermThatExcludedEachHost(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyFleet(t, db)

	hosts, err := repo.ListFleetView(t.Context(), standbyLead)
	if err != nil {
		t.Fatalf("ListFleetView: %v", err)
	}
	view := byName(t, hosts)

	for name, want := range map[string]host.StandbyTerm{
		"standby-running":      "",
		"standby-provisioning": "",
		"standby-busy":         host.TermIdle,
		"standby-degraded":     host.TermNotDegraded,
		"standby-expiring":     host.TermLease,
		"standby-stopped":      host.TermPlaceable,
		// Exclusive plus a system origin derives DEDICATED, so the class term is
		// what fails first, and that is the right answer. Naming exclusivity
		// would describe the mechanism while hiding that this host belongs to a
		// group and is not the fleet's at all.
		"standby-exclusive": host.TermClass,
		"standby-user":      host.TermClass,
		"standby-unstamped": host.TermClass,
	} {
		h, ok := view[name]
		if !ok {
			t.Fatalf("%s missing from the fleet view", name)
		}
		if got := h.FailingTerm(); got != want {
			t.Errorf("%s: failing term = %q, want %q", name, got, want)
		}
	}
}

// A deleted host is a tombstone and holds no capacity. Drawing one would put a
// row on the band that no total counts and no operator can act on.
func TestFleetViewSkipsDeletedHosts(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyHost(t, db, standbySeed{id: "gone", origin: host.OriginPool, status: "deleted"})
	seedStandbyHost(t, db, standbySeed{id: "here", origin: host.OriginPool, status: "running"})

	hosts, err := repo.ListFleetView(t.Context(), standbyLead)
	if err != nil {
		t.Fatalf("ListFleetView: %v", err)
	}
	if len(hosts) != 1 || hosts[0].Name != "here" {
		t.Errorf("fleet view = %d host(s), want just the live one", len(hosts))
	}
}

// Which pool a host belongs to is what the fleet's arithmetic partitions on,
// and a row naming none belongs to the default pool, the fleet as it stood
// before there were several. Read as an empty name instead, such a host would
// sit in a pool no bound is held against.
func TestFleetViewReadsAnUnstampedPoolAsTheDefault(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyHost(t, db, standbySeed{id: "unstamped", origin: host.OriginPool, status: "running"})
	seedStandbyHost(t, db, standbySeed{id: "stamped", origin: host.OriginPool, status: "running", pool: "gpu"})

	hosts, err := repo.ListFleetView(t.Context(), standbyLead)
	if err != nil {
		t.Fatalf("ListFleetView: %v", err)
	}
	view := byName(t, hosts)
	if got := view["unstamped"].Pool; got != host.PoolDefault {
		t.Errorf("unstamped host reads pool %q, want %q", got, host.PoolDefault)
	}
	if got := view["stamped"].Pool; got != "gpu" {
		t.Errorf("stamped host reads pool %q, want gpu", got)
	}
}

// seedSizedDevice plants a device holding a stated number of instance slots,
// through the same desired_spec key the create path writes.
func seedSizedDevice(t *testing.T, db *spannertest.TestDB, hostID, deviceID, status string, instances int) {
	t.Helper()
	now := time.Now()
	row := map[string]any{
		"host_id": hostID, "device_id": deviceID, "name": deviceID,
		"status": status, "created_at": now, "updated_at": now,
	}
	if instances > 0 {
		row["desired_spec"] = `{"instance_count":` + strconv.Itoa(instances) + `}`
	}
	if _, err := db.Client.Apply(t.Context(), []*spanner.Mutation{spanner.InsertMap("devices", row)}); err != nil {
		t.Fatalf("seed device %s on %s: %v", deviceID, hostID, err)
	}
}

// Occupancy is denominated in INSTANCE SLOTS, never in device rows.
//
// A multi-instance group costs its host as many slots as it has guests, and a
// view that counted rows would draw a full host as nearly empty, the same
// arithmetic error that oversubscribes a host silently. The device with no
// stored count is the pre-spec row: zero reads as one, matching
// device.DesiredInstanceCount, because charging it nothing would let a host
// accept devices forever.
func TestFleetViewSizesOccupancyByInstanceSlots(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyHost(t, db, standbySeed{id: "packed", origin: host.OriginPool, status: "running", devices: 4})
	seedSizedDevice(t, db, "packed", "group-of-three", "running", 3)
	seedSizedDevice(t, db, "packed", "legacy-row", "running", 0)

	hosts, err := repo.ListFleetView(t.Context(), standbyLead)
	if err != nil {
		t.Fatalf("ListFleetView: %v", err)
	}
	h := byName(t, hosts)["packed"]
	if h.LiveInstances != 4 {
		t.Errorf("LiveInstances = %d, want 4 (a group of three plus an unsized row read as one)", h.LiveInstances)
	}
	if len(h.Occupants) != 2 {
		t.Fatalf("occupants = %d, want 2", len(h.Occupants))
	}
}

// An error row holds real slots with a dead thing in them. It is NOT an unbacked
// slot (that is a claim about the host image) and it is not free either: the
// host's counter still charges it, which is why a host full of failed creates
// refuses placement while running nothing.
func TestFleetViewSeparatesErrorHeldSlotsFromLiveOnes(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyHost(t, db, standbySeed{id: "haunted", origin: host.OriginPool, status: "running", devices: 3})
	seedSizedDevice(t, db, "haunted", "alive", "running", 1)
	seedSizedDevice(t, db, "haunted", "dead-pair", "error", 2)
	// A deleted row's slots are already refunded, so it must leave no trace.
	seedSizedDevice(t, db, "haunted", "gone", "deleted", 4)

	hosts, err := repo.ListFleetView(t.Context(), standbyLead)
	if err != nil {
		t.Fatalf("ListFleetView: %v", err)
	}
	h := byName(t, hosts)["haunted"]
	if h.LiveInstances != 1 {
		t.Errorf("LiveInstances = %d, want 1", h.LiveInstances)
	}
	if h.ErrorInstances != 2 {
		t.Errorf("ErrorInstances = %d, want 2; the dead pair still holds its slots", h.ErrorInstances)
	}
	if len(h.Occupants) != 2 {
		t.Errorf("occupants = %d, want 2; the deleted row is refunded and must not be drawn", len(h.Occupants))
	}
}

// BackedSlots is what every total sums, and it is bounded by the image rather
// than by the host's own promise. A host admitted above what the image
// preallocates TAP sets for has promised capacity nothing can launch against.
func TestFleetViewBackedSlotsAreBoundedByTheImage(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	over := host.HostImageCVDAccounts + 6
	if _, err := db.Client.Apply(t.Context(), []*spanner.Mutation{spanner.InsertMap("hosts", map[string]any{
		"host_id": "overpromised", "name": "overpromised", "zone": "us-central1-a",
		"status": "running", "machine_type": "n1-standard-8", "origin": string(host.OriginPool),
		"host_orchestrator_port": int64(2080), "max_devices": over, "current_device_count": int64(0),
		"exclusive": false, "lifecycle_mode": "ephemeral", "protected_device_count": int64(0),
		"created_at": time.Now(), "updated_at": time.Now(),
	})}); err != nil {
		t.Fatalf("seed overpromised host: %v", err)
	}

	hosts, err := repo.ListFleetView(t.Context(), standbyLead)
	if err != nil {
		t.Fatalf("ListFleetView: %v", err)
	}
	h := byName(t, hosts)["overpromised"]
	if h.MaxDevices != over {
		t.Errorf("MaxDevices = %d, want the promise %d unchanged", h.MaxDevices, over)
	}
	if h.BackedSlots() != host.HostImageCVDAccounts {
		t.Errorf("BackedSlots = %d, want %d; the image is the shorter of the two",
			h.BackedSlots(), host.HostImageCVDAccounts)
	}
}

// The shape census is what the band's summary row states. Its accelerator comes
// off the host row rather than the catalog, and its class is the same derivation
// the fleet view uses.
func TestCountByShapeGroupsByShapeClassAndStatus(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	seedStandbyHost(t, db, standbySeed{id: "cpu-1", origin: host.OriginPool, status: "running"})
	seedStandbyHost(t, db, standbySeed{id: "cpu-2", origin: host.OriginAuto, status: "running"})
	seedStandbyHost(t, db, standbySeed{id: "user-1", origin: host.OriginUser, status: "running"})
	seedStandbyHost(t, db, standbySeed{id: "gone-1", origin: host.OriginPool, status: "deleted"})

	shapes, err := repo.CountByShape(t.Context())
	if err != nil {
		t.Fatalf("CountByShape: %v", err)
	}
	counts := make(map[host.Class]int64)
	for _, s := range shapes {
		counts[s.Class] += s.Count
	}
	// origin=auto is FLEET, alongside pool. A census that counted only the
	// keeper's own top-ups would disagree with the target's own arithmetic.
	if counts[host.ClassFleet] != 2 {
		t.Errorf("fleet-class hosts = %d, want 2 (pool and auto are both the fleet's)", counts[host.ClassFleet])
	}
	if counts[host.ClassReserved] != 1 {
		t.Errorf("reserved hosts = %d, want 1", counts[host.ClassReserved])
	}
	if total := counts[host.ClassFleet] + counts[host.ClassReserved]; total != 3 {
		t.Errorf("census total = %d, want 3; the deleted tombstone must not be counted", total)
	}
}
