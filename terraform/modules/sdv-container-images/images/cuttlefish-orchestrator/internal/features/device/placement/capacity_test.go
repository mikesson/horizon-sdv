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

package placement_test

import (
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/batch"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/features/device/placement"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// placementSeed is one host row for placement tests (SeedHost hardcodes
// running/gpu_count=1, too coarse here).
type placementSeed struct {
	id, status, machineType       string
	devices, maxDevices, gpuCount int64
	createdAt                     time.Time
	exclusive                     bool
	provisioningModel             string
	// provisionBlocked stamps the row as one GCE refused for capacity.
	provisionBlocked bool
}

func seedPlacementHost(t *testing.T, db *spannertest.TestDB, s placementSeed) {
	t.Helper()
	row := map[string]any{
		"host_id":                s.id,
		"name":                   s.id,
		"zone":                   "us-central1-a",
		"status":                 s.status,
		"machine_type":           s.machineType,
		"gpu_count":              s.gpuCount,
		"host_orchestrator_port": int64(2080),
		"max_devices":            s.maxDevices,
		"current_device_count":   s.devices,
		"exclusive":              s.exclusive,
		"created_at":             s.createdAt,
		"updated_at":             s.createdAt,
	}
	if s.provisioningModel != "" {
		row["provisioning_model"] = s.provisioningModel
	}
	if s.provisionBlocked {
		row["provision_blocked_at"] = s.createdAt.Add(time.Minute)
		row["provision_blocked_reason"] = "The zone does not have enough resources"
	}
	_, err := db.Client.Apply(t.Context(), []*spanner.Mutation{spanner.InsertMap("hosts", row)})
	if err != nil {
		t.Fatalf("seed placement host %s: %v", s.id, err)
	}
}

func seedMachineFamily(t *testing.T, db *spannertest.TestDB, family string, enabled bool) {
	t.Helper()
	_, err := db.Client.Apply(t.Context(), []*spanner.Mutation{
		spanner.InsertMap("machine_families", map[string]any{
			"family":     family,
			"is_enabled": enabled,
		}),
	})
	if err != nil {
		t.Fatalf("seed machine family %s: %v", family, err)
	}
}

// TestPickHostForPlacement exercises the real placement SQL: status set,
// capacity, exclusion, bin-pack ordering, and both GPU predicates (attached
// gpu_count and the enabled-integrated machine-type subquery).
func TestPickHostForPlacement(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := placement.NewCapacityRepository(db.Client)
	ctx := t.Context()
	base := time.Now().Add(-time.Hour)

	// Seeded so the fixture resembles a real catalog; the placement predicate
	// reads no curation table at all, which TestGPUPlacementReadsTheHostRowAlone
	// is what actually proves.
	seedMachineFamily(t, db, "g2", true)

	seedPlacementHost(t, db, placementSeed{id: "h-stopped", status: "stopped", machineType: "n1-standard-8", devices: 0, maxDevices: 4, gpuCount: 0, createdAt: base})
	seedPlacementHost(t, db, placementSeed{id: "h-error", status: "error", machineType: "n1-standard-8", devices: 0, maxDevices: 4, gpuCount: 0, createdAt: base})
	seedPlacementHost(t, db, placementSeed{id: "h-full", status: "running", machineType: "n1-standard-8", devices: 4, maxDevices: 4, gpuCount: 0, createdAt: base})
	seedPlacementHost(t, db, placementSeed{id: "h-empty", status: "running", machineType: "n1-standard-8", devices: 0, maxDevices: 4, gpuCount: 0, createdAt: base})
	seedPlacementHost(t, db, placementSeed{id: "h-busy", status: "running", machineType: "n1-standard-8", devices: 3, maxDevices: 4, gpuCount: 0, createdAt: base})
	seedPlacementHost(t, db, placementSeed{id: "h-busy-younger", status: "running", machineType: "n1-standard-8", devices: 3, maxDevices: 4, gpuCount: 0, createdAt: base.Add(time.Minute)})
	// Exclusive fences from automatic placement even when it would win the
	// bin-pack (fuller and older than every open candidate).
	seedPlacementHost(t, db, placementSeed{id: "h-exclusive", status: "running", machineType: "n1-standard-8", devices: 3, maxDevices: 4, gpuCount: 0, createdAt: base.Add(-time.Minute), exclusive: true})

	// Bin-pack: the fullest hosts with capacity win, and the two tied on load
	// are hosts the ordering has no reason to separate, so the pick spreads
	// over both of them and over neither emptier one. created_at still decides
	// which rows reach the window; it no longer decides between rows already
	// inside it.
	const loadTied = 24
	if drawn := drawPicks(t, repo, placement.Prefs{}, loadTied); len(drawn) != 2 ||
		drawn["h-busy"]+drawn["h-busy-younger"] != loadTied {
		t.Errorf("%d picks drew %v, want both hosts the ordering ranked alike and neither emptier one", loadTied, drawn)
	}

	// Exclusion moves to the next candidate.
	got, _, err := repo.PickHostForPlacement(ctx, false, []string{"h-busy"}, placement.Prefs{})
	if err != nil {
		t.Fatalf("pick excluded: %v", err)
	}
	if got != "h-busy-younger" {
		t.Errorf("pick = %q, want h-busy-younger after exclusion", got)
	}

	// No GPU candidates yet: every host so far states gpu_count = 0.
	got, _, err = repo.PickHostForPlacement(ctx, true, nil, placement.Prefs{})
	if err != nil {
		t.Fatalf("pick gpu: %v", err)
	}
	if got != "" {
		t.Errorf("gpu pick = %q, want none", got)
	}

	// An attached GPU qualifies.
	seedPlacementHost(t, db, placementSeed{id: "h-gpu-attached", status: "running", machineType: "n1-standard-8", devices: 0, maxDevices: 4, gpuCount: 1, createdAt: base})
	got, _, err = repo.PickHostForPlacement(ctx, true, nil, placement.Prefs{})
	if err != nil {
		t.Fatalf("pick gpu attached: %v", err)
	}
	if got != "h-gpu-attached" {
		t.Errorf("gpu pick = %q, want h-gpu-attached", got)
	}

	// An integrated machine qualifies the same way. The create stamps its
	// welded-on accelerator onto the row, so both kinds of GPU host answer one
	// predicate. A provisioning host is a valid candidate, and it is fuller, so
	// it wins.
	seedPlacementHost(t, db, placementSeed{id: "h-gpu-integrated", status: "provisioning", machineType: "g2-standard-8", devices: 2, maxDevices: 4, gpuCount: 1, createdAt: base})
	got, _, err = repo.PickHostForPlacement(ctx, true, nil, placement.Prefs{})
	if err != nil {
		t.Fatalf("pick gpu integrated: %v", err)
	}
	if got != "h-gpu-integrated" {
		t.Errorf("gpu pick = %q, want h-gpu-integrated (fuller, provisioning counts)", got)
	}

	// A host on an integrated type whose row still says gpu_count = 0 is NOT a
	// candidate, however fully it would otherwise win. That is exactly the
	// population the one-shot backfill exists for: rows created before the stamp.
	seedPlacementHost(t, db, placementSeed{id: "h-gpu-unstamped", status: "running", machineType: "g2-standard-8", devices: 3, maxDevices: 4, gpuCount: 0, createdAt: base})
	got, _, err = repo.PickHostForPlacement(ctx, true, nil, placement.Prefs{})
	if err != nil {
		t.Fatalf("pick gpu unstamped: %v", err)
	}
	if got != "h-gpu-integrated" {
		t.Errorf("gpu pick = %q, want h-gpu-integrated; an unstamped row is not a GPU candidate", got)
	}
}

// withdrawFromCatalog takes a shape out of the offered catalog both ways an
// operator can, with the family disabled and the shape excluded by override,
// which is the action the reclassification hazard rode on.
func withdrawFromCatalog(t *testing.T, db *spannertest.TestDB, family, machineType string) {
	t.Helper()
	_, err := db.Client.Apply(t.Context(), []*spanner.Mutation{
		spanner.UpdateMap("machine_families", map[string]any{"family": family, "is_enabled": false}),
		spanner.InsertMap("machine_type_overrides", map[string]any{
			"machine_type": machineType,
			"is_enabled":   false,
		}),
	})
	if err != nil {
		t.Fatalf("withdraw %s from the catalog: %v", machineType, err)
	}
}

// A host's GPU is a fact about the host. The predicate used to fall back to a
// machine_types subquery for integrated shapes, which made capability a property
// of a table an operator edits: disabling g2 in the catalog silently stripped
// GPU-placeability from every running g2 host, and nothing said so.
//
// Both arms matter. The catalog is DELETED outright in the first, so a query that
// still consults machine_types cannot pass by accident. A grep for the subquery
// would prove nothing, since a rewritten one would read differently.
func TestGPUPlacementReadsTheHostRowAlone(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	base := time.Now().Add(-time.Hour)

	t.Run("with no catalog at all", func(t *testing.T) {
		t.Parallel()
		db := spannertest.MustNewTestDB(t)
		repo := placement.NewCapacityRepository(db.Client)
		// No catalog seed: the curation tables hold nothing about g2.
		seedPlacementHost(t, db, placementSeed{id: "h-gpu", status: "running", machineType: "g2-standard-8", devices: 0, maxDevices: 4, gpuCount: 1, createdAt: base})

		got, _, err := repo.PickHostForPlacement(ctx, true, nil, placement.Prefs{})
		if err != nil {
			t.Fatalf("pick gpu: %v", err)
		}
		if got != "h-gpu" {
			t.Errorf("gpu pick = %q, want h-gpu; the row states the GPU, so no catalog read may be needed", got)
		}
	})

	t.Run("after the operator withdraws the shape from the catalog", func(t *testing.T) {
		t.Parallel()
		db := spannertest.MustNewTestDB(t)
		repo := placement.NewCapacityRepository(db.Client)
		seedMachineFamily(t, db, "g2", true)
		seedPlacementHost(t, db, placementSeed{id: "h-gpu", status: "running", machineType: "g2-standard-8", devices: 0, maxDevices: 4, gpuCount: 1, createdAt: base})

		withdrawFromCatalog(t, db, "g2", "g2-standard-8")

		got, _, err := repo.PickHostForPlacement(ctx, true, nil, placement.Prefs{})
		if err != nil {
			t.Fatalf("pick gpu: %v", err)
		}
		if got != "h-gpu" {
			t.Errorf("gpu pick = %q, want h-gpu; disabling a type must not take the GPU off a running host", got)
		}
	})
}

// The CPU fence, which a swiftshader device's snapshottability depends on: its
// create pins the mode, but a relaunch re-assembles from defaults and the GPU
// auto walk on an accelerated host lands on a mode whose snapshot kills crosvm.
//
// It is a hard term, so both arms matter. It has to beat the ordering, since
// the GPU host below is fuller and would win the bin-pack outright, and it has
// to report NO candidate rather than fall back when every free host has a GPU.
func TestPickHostForPlacementCPUOnlyFence(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	base := time.Now().Add(-time.Hour)

	t.Run("passes over a GPU host that would win the bin-pack", func(t *testing.T) {
		t.Parallel()
		db := spannertest.MustNewTestDB(t)
		repo := placement.NewCapacityRepository(db.Client)
		seedPlacementHost(t, db, placementSeed{id: "h-gpu", status: "running", machineType: "g2-standard-8", devices: 3, maxDevices: 4, gpuCount: 1, createdAt: base})
		seedPlacementHost(t, db, placementSeed{id: "h-cpu", status: "running", machineType: "n1-standard-8", devices: 0, maxDevices: 4, gpuCount: 0, createdAt: base})

		// Unfenced first: the GPU host wins on load alone. That is what makes the
		// fenced arm a statement about the fence rather than about the ordering.
		got, _, err := repo.PickHostForPlacement(ctx, false, nil, placement.Prefs{})
		if err != nil {
			t.Fatalf("pick unfenced: %v", err)
		}
		if got != "h-gpu" {
			t.Fatalf("unfenced pick = %q, want h-gpu (fuller); the fixture no longer tests the fence", got)
		}

		got, _, err = repo.PickHostForPlacement(ctx, false, nil, placement.Prefs{CPUOnly: true})
		if err != nil {
			t.Fatalf("pick fenced: %v", err)
		}
		if got != "h-cpu" {
			t.Errorf("fenced pick = %q, want h-cpu; a GPU host is not a candidate however full it is", got)
		}
	})

	t.Run("reports no candidate when every free host has a GPU", func(t *testing.T) {
		t.Parallel()
		db := spannertest.MustNewTestDB(t)
		repo := placement.NewCapacityRepository(db.Client)
		seedPlacementHost(t, db, placementSeed{id: "h-gpu", status: "running", machineType: "g2-standard-8", devices: 0, maxDevices: 4, gpuCount: 1, createdAt: base})

		got, _, err := repo.PickHostForPlacement(ctx, false, nil, placement.Prefs{CPUOnly: true})
		if err != nil {
			t.Fatalf("pick fenced: %v", err)
		}
		if got != "" {
			t.Errorf("fenced pick = %q, want none; the miss provisions a CPU host", got)
		}
	})
}

// A provisioning host GCE has refused for capacity is not a candidate, however
// far ahead of the field the bin-pack would put it. The refusal is the whole
// finding this exists for: the create placed there waits out the host's retry
// ladder, and never reaches the resolution that would have bought a shape with
// capacity. Every arm below is a row the fence must treat differently from the
// one beside it, so a fence that read the wrong column, or fenced the wrong
// statuses, cannot pass the table.
func TestPickHostForPlacementSkipsBlockedProvisioningHost(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	base := time.Now().Add(-time.Hour)

	t.Run("passes over a blocked host that would win the bin-pack", func(t *testing.T) {
		t.Parallel()
		db := spannertest.MustNewTestDB(t)
		repo := placement.NewCapacityRepository(db.Client)
		seedPlacementHost(t, db, placementSeed{id: "h-stockout", status: "provisioning", machineType: "n4-standard-8", devices: 3, maxDevices: 4, createdAt: base, provisionBlocked: true})
		seedPlacementHost(t, db, placementSeed{id: "h-standby", status: "running", machineType: "c4-standard-8", devices: 0, maxDevices: 4, createdAt: base})

		if got, _ := mustPick(t, repo, placement.Prefs{}); got != "h-standby" {
			t.Errorf("pick = %q, want h-standby; a stocked-out host is not capacity a create can count on", got)
		}
		slots, err := repo.ListPlaceableCapacity(ctx, false, placement.Prefs{})
		if err != nil {
			t.Fatalf("ListPlaceableCapacity: %v", err)
		}
		if want := []batch.HostSlot{{HostID: "h-standby", Free: 4}}; !slices.Equal(slots, want) {
			t.Errorf("ListPlaceableCapacity = %+v, want %+v; the planner shares the fence", slots, want)
		}
	})

	t.Run("reports no candidate when the only host is blocked", func(t *testing.T) {
		t.Parallel()
		db := spannertest.MustNewTestDB(t)
		repo := placement.NewCapacityRepository(db.Client)
		seedPlacementHost(t, db, placementSeed{id: "h-stockout", status: "pending", machineType: "n4-standard-8", devices: 1, maxDevices: 4, createdAt: base, provisionBlocked: true})

		if got, _ := mustPick(t, repo, placement.Prefs{}); got != "" {
			t.Errorf("pick = %q, want none; the miss is what sends the create to ProvisionSpec and the shape ladder", got)
		}
	})

	t.Run("still packs onto a provisioning host nothing has refused", func(t *testing.T) {
		t.Parallel()
		db := spannertest.MustNewTestDB(t)
		repo := placement.NewCapacityRepository(db.Client)
		seedPlacementHost(t, db, placementSeed{id: "h-coming-up", status: "provisioning", machineType: "n4-standard-8", devices: 1, maxDevices: 4, createdAt: base})
		seedPlacementHost(t, db, placementSeed{id: "h-standby", status: "running", machineType: "n4-standard-8", devices: 0, maxDevices: 4, createdAt: base})

		if got, _ := mustPick(t, repo, placement.Prefs{}); got != "h-coming-up" {
			t.Errorf("pick = %q, want h-coming-up; a healthy provision is still where concurrent creates pack", got)
		}
	})

	t.Run("a host whose VM exists is exempt from the fence", func(t *testing.T) {
		t.Parallel()
		db := spannertest.MustNewTestDB(t)
		repo := placement.NewCapacityRepository(db.Client)
		// The marker outlived the provision: the insert succeeded and the
		// read-back that clears it failed, and the reconciler observed the VM.
		seedPlacementHost(t, db, placementSeed{id: "h-starting", status: "starting", machineType: "n4-standard-8", devices: 0, maxDevices: 4, createdAt: base, provisionBlocked: true})
		seedPlacementHost(t, db, placementSeed{id: "h-running", status: "running", machineType: "n4-standard-8", devices: 0, maxDevices: 4, createdAt: base.Add(time.Minute), provisionBlocked: true})

		drawn := drawPicks(t, repo, placement.Prefs{}, 24)
		if drawn["h-starting"]+drawn["h-running"] != 24 || len(drawn) != 2 {
			t.Errorf("picks drew %v, want both hosts; a stale marker must not fence a host that exists", drawn)
		}
	})
}

// Among hosts the bin-pack ranks alike, the one whose VM exists comes first:
// the create runs now instead of waiting a provisioning leg. Only among those,
// though. A fuller host still coming up keeps its place ahead of an emptier
// ready one, or every create landing while a healthy host provisioned would
// open a standby host and buy its replacement.
func TestPickHostForPlacementPrefersReadyAtEqualLoad(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := placement.NewCapacityRepository(db.Client)
	base := time.Now().Add(-time.Hour)

	// Same load, the provisioning host older, so created_at alone would pick it.
	seedPlacementHost(t, db, placementSeed{id: "h-booting", status: "provisioning", machineType: "n4-standard-8", devices: 1, maxDevices: 4, createdAt: base})
	seedPlacementHost(t, db, placementSeed{id: "h-ready", status: "running", machineType: "n4-standard-8", devices: 1, maxDevices: 4, createdAt: base.Add(time.Minute)})
	if got, _ := mustPick(t, repo, placement.Prefs{}); got != "h-ready" {
		t.Errorf("pick = %q, want h-ready at equal load", got)
	}

	// A fuller booting host outranks the ready one: the bin-pack still leads.
	seedPlacementHost(t, db, placementSeed{id: "h-booting-fuller", status: "provisioning", machineType: "n4-standard-8", devices: 2, maxDevices: 4, createdAt: base})
	if got, _ := mustPick(t, repo, placement.Prefs{}); got != "h-booting-fuller" {
		t.Errorf("pick = %q, want h-booting-fuller; readiness never outranks the bin-pack", got)
	}
}

// TestListPlaceableCapacity exercises the pack planner's fleet-capacity SQL:
// predicate parity with PickHostForPlacement (status, degraded, exclusive,
// full), free-slot arithmetic, and most-loaded-first ordering.
func TestListPlaceableCapacity(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := placement.NewCapacityRepository(db.Client)
	ctx := t.Context()
	base := time.Now().Add(-time.Hour)

	seedPlacementHost(t, db, placementSeed{id: "cap-busy", status: "running", machineType: "n1-standard-8", devices: 3, maxDevices: 4, createdAt: base})
	seedPlacementHost(t, db, placementSeed{id: "cap-half", status: "provisioning", machineType: "n1-standard-8", devices: 2, maxDevices: 4, createdAt: base})
	seedPlacementHost(t, db, placementSeed{id: "cap-empty", status: "running", machineType: "n1-standard-8", devices: 0, maxDevices: 4, createdAt: base})
	seedPlacementHost(t, db, placementSeed{id: "cap-full", status: "running", machineType: "n1-standard-8", devices: 4, maxDevices: 4, createdAt: base})
	seedPlacementHost(t, db, placementSeed{id: "cap-exclusive", status: "running", machineType: "n1-standard-8", devices: 1, maxDevices: 4, createdAt: base, exclusive: true})
	seedPlacementHost(t, db, placementSeed{id: "cap-stopped", status: "stopped", machineType: "n1-standard-8", devices: 0, maxDevices: 4, createdAt: base})

	slots, err := repo.ListPlaceableCapacity(ctx, false, placement.Prefs{})
	if err != nil {
		t.Fatalf("ListPlaceableCapacity: %v", err)
	}
	want := []batch.HostSlot{
		{HostID: "cap-busy", Free: 1},
		{HostID: "cap-half", Free: 2},
		{HostID: "cap-empty", Free: 4},
	}
	if !slices.Equal(slots, want) {
		t.Errorf("ListPlaceableCapacity = %+v, want %+v", slots, want)
	}
}

// TestPickHostForPlacementInstanceCapacity pins the capacity term at N > 1:
// current_device_count and max_devices are both counts of CVD instances, so a
// candidate needs room for the whole group. A host with two free slots is a
// candidate for a two-instance device and not for a three-instance one, even
// though it would still be under max_devices by device rows.
func TestPickHostForPlacementInstanceCapacity(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := placement.NewCapacityRepository(db.Client)
	base := time.Now().Add(-time.Hour)

	// One host, two of its four instance slots already spent.
	seedPlacementHost(t, db, placementSeed{id: "n-half", status: "running", machineType: "n1-standard-8", devices: 2, maxDevices: 4, createdAt: base})

	for _, tc := range []struct {
		instances int64
		want      string
	}{
		{0, "n-half"}, // unset reads as the one instance every device runs
		{1, "n-half"},
		{2, "n-half"}, // exactly fills it
		{3, ""},
		{8, ""},
	} {
		got, _, err := repo.PickHostForPlacement(t.Context(), false, nil, placement.Prefs{Instances: tc.instances})
		if err != nil {
			t.Fatalf("pick for %d instances: %v", tc.instances, err)
		}
		if got != tc.want {
			t.Errorf("pick for %d instances = %q, want %q", tc.instances, got, tc.want)
		}
	}
}

// The spread is bounded, so a fleet wider than the window still packs: the
// draw reaches the leading rows and stops, and everything behind them stays as
// unreachable as it was under a single-row pick. Six hosts the ordering cannot
// tell apart make the boundary observable. The two youngest are exactly the
// rows a pick that stopped bounding its window would start answering with.
func TestPickHostForPlacementSpreadStopsAtTheWindow(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := placement.NewCapacityRepository(db.Client)
	base := time.Now().Add(-time.Hour)

	// Same load, same capacity model, no ledger rows: created_at is the only
	// order left, so the window is exactly the four oldest.
	for i, id := range []string{"win-1", "win-2", "win-3", "win-4", "beyond-1", "beyond-2"} {
		seedPlacementHost(t, db, placementSeed{
			id: id, status: "running", machineType: "n1-standard-8",
			devices: 1, maxDevices: 4, createdAt: base.Add(time.Duration(i) * time.Minute),
		})
	}

	const draws = 80
	drawn := drawPicks(t, repo, placement.Prefs{}, draws)
	if drawn["beyond-1"]+drawn["beyond-2"] != 0 {
		t.Errorf("%d picks drew %v, want nothing from behind the window: the spread is bounded, not fleet-wide", draws, drawn)
	}
	if len(drawn) != 4 {
		t.Errorf("%d picks drew %v, want the whole window", draws, drawn)
	}
}

// A host row may state no capacity model at all: the column is nullable, and a
// host created without one writes NULL over the schema default. The STANDARD
// term is then NULL on that row, which the pick's projection has to read as a
// value. Scanning it as a plain bool fails the query and with it every
// declarative create the fleet is serving.
func TestPickHostForPlacementUnstampedProvisioningModel(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := placement.NewCapacityRepository(db.Client)

	seedPlacementHost(t, db, placementSeed{id: "h-unstamped", status: "running", machineType: "n1-standard-8", devices: 1, maxDevices: 4, createdAt: time.Now().Add(-time.Hour)})
	if _, err := db.Client.Apply(t.Context(), []*spanner.Mutation{spanner.UpdateMap("hosts", map[string]any{
		"host_id": "h-unstamped", "provisioning_model": spanner.NullString{}, "updated_at": time.Now(),
	})}); err != nil {
		t.Fatalf("clearing the provisioning model: %v", err)
	}

	// PreferStandard is what makes the term evaluate at all; without it the
	// row's NULL never reaches the projection.
	got, warm := mustPick(t, repo, placement.Prefs{PreferStandard: true})
	if got != "h-unstamped" || warm {
		t.Errorf("pick = (%q, warm=%v), want h-unstamped cold; a model-less host is still placeable", got, warm)
	}
}

// seedArtifactCacheRow plants an affinity-ledger row through the real
// repository write.
func seedArtifactCacheRow(t *testing.T, db *spannertest.TestDB, hostID, fetchKey string) {
	t.Helper()
	if err := cspanner.NewArtifactCacheRepository(db.Client).Record(t.Context(), hostID, fetchKey, "build-uuid", 0); err != nil {
		t.Fatalf("seed artifact cache row on %s: %v", hostID, err)
	}
}

// mustPick runs a non-GPU, no-exclusion pick and fails the test on error.
func mustPick(t *testing.T, repo *placement.CapacityRepository, prefs placement.Prefs) (string, bool) {
	t.Helper()
	id, warm, err := repo.PickHostForPlacement(t.Context(), false, nil, prefs)
	if err != nil {
		t.Fatalf("pick with %+v: %v", prefs, err)
	}
	return id, warm
}

// drawPicks repeats one pick and reports how often each host answered, which
// is the only way a spread is observable: a single call cannot tell a chosen
// host from the only host it would ever choose.
func drawPicks(t *testing.T, repo *placement.CapacityRepository, prefs placement.Prefs, draws int) map[string]int {
	t.Helper()
	seen := map[string]int{}
	for range draws {
		id, _ := mustPick(t, repo, prefs)
		seen[id]++
	}
	return seen
}

// TestPickHostForPlacementBuildAffinity pins the affinity term end to end on
// real SQL: a ledger row must flip the pick away from the bin-pack winner,
// the warm bit must report the flip, a non-matching key must change nothing,
// and the STANDARD-first preference must outrank affinity when both apply.
func TestPickHostForPlacementBuildAffinity(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := placement.NewCapacityRepository(db.Client)
	ctx := t.Context()
	base := time.Now().Add(-time.Hour)
	const key = "@ab/12345678/aosp_cf_x86_64_only_phone-userdebug"

	// af-cold is fuller AND occupied, so it leads on both of the terms affinity
	// has to beat, and it leads them alone, which is what keeps every arm below
	// a statement about the ordering rather than about the spread.
	seedPlacementHost(t, db, placementSeed{id: "af-cold", status: "running", machineType: "n1-standard-8", devices: 3, maxDevices: 4, createdAt: base})
	seedPlacementHost(t, db, placementSeed{id: "af-warm", status: "running", machineType: "n1-standard-8", devices: 0, maxDevices: 4, createdAt: base})
	seedArtifactCacheRow(t, db, "af-warm", key)

	if got, warm := mustPick(t, repo, placement.Prefs{}); got != "af-cold" || warm {
		t.Errorf("pick without prefs = (%q, warm=%v), want af-cold cold (bin-pack)", got, warm)
	}
	if got, warm := mustPick(t, repo, placement.Prefs{AffineFetchKey: key}); got != "af-warm" || !warm {
		t.Errorf("affine pick = (%q, warm=%v), want af-warm warm", got, warm)
	}
	// A wanted key nobody holds leaves the bin-pack outcome untouched.
	if got, warm := mustPick(t, repo, placement.Prefs{AffineFetchKey: "@ab/999/other-target"}); got != "af-cold" || warm {
		t.Errorf("cold-key pick = (%q, warm=%v), want af-cold cold", got, warm)
	}

	// The fleet view rides the same ordering: warm host first.
	slots, err := repo.ListPlaceableCapacity(ctx, false, placement.Prefs{AffineFetchKey: key})
	if err != nil {
		t.Fatalf("affine list: %v", err)
	}
	want := []batch.HostSlot{{HostID: "af-warm", Free: 4}, {HostID: "af-cold", Free: 1}}
	if !slices.Equal(slots, want) {
		t.Errorf("affine list = %+v, want %+v", slots, want)
	}

	// STANDARD-first outranks affinity: with the warm host on Spot, an
	// interactive create's preference wins and the pick reports cold.
	if _, err := db.Client.Apply(ctx, []*spanner.Mutation{spanner.UpdateMap("hosts", map[string]any{
		"host_id": "af-warm", "provisioning_model": "SPOT", "updated_at": time.Now(),
	})}); err != nil {
		t.Fatalf("flipping af-warm to SPOT: %v", err)
	}
	if got, warm := mustPick(t, repo, placement.Prefs{AffineFetchKey: key, PreferStandard: true}); got != "af-cold" || warm {
		t.Errorf("standard-first pick = (%q, warm=%v), want af-cold cold (session outlives a fetch)", got, warm)
	}
}

// A pool partitions the machines, so a host in another pool is not capacity for
// this create however much room it has: its shape, its lease and the bounds it
// is held to all belong to a population this create was never counted in. Drop
// the term and a GPU-pool create lands on a default-pool host, is served by
// software rendering, and the gpu pool's own deficit never closes.
//
// The unstamped row is the other half: NULL is the default pool, which is what
// every host that predates the column carries and what a default-pool create
// still writes. A term that compared the raw column would make the whole
// existing fleet unplaceable.
func TestPickHostForPlacementPoolFence(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := placement.NewCapacityRepository(db.Client)
	ctx := t.Context()
	base := time.Now().Add(-time.Hour)

	seedPlacementHost(t, db, placementSeed{id: "h-unstamped", status: "running", machineType: "n1-standard-8", devices: 1, maxDevices: 4, createdAt: base})
	seedPoolHost(t, db, placementSeed{id: "h-default", status: "running", machineType: "n1-standard-8", devices: 2, maxDevices: 4, createdAt: base}, host.PoolDefault)
	seedPoolHost(t, db, placementSeed{id: "h-gpu", status: "running", machineType: "g2-standard-8", devices: 3, maxDevices: 4, gpuCount: 1, createdAt: base}, host.PoolGPU)

	// The fullest host is in the gpu pool, so a default-pool create drawing
	// it would be the fence failing open. The pick is drawn many times
	// because it spreads inside a class. Which default-pool member it lands
	// on is not asserted here; this test covers only the fence.
	drawnDefault := drawPicks(t, repo, placement.Prefs{Pool: host.PoolDefault}, 24)
	if drawnDefault["h-gpu"] != 0 {
		t.Errorf("picks drew %v, want none from the gpu pool: the fuller gpu-pool host is not this create's capacity", drawnDefault)
	}
	if drawnDefault["h-default"]+drawnDefault["h-unstamped"] != 24 {
		t.Errorf("picks drew %v, want every draw from the default pool's machines", drawnDefault)
	}

	got, _, err := repo.PickHostForPlacement(ctx, false, nil, placement.Prefs{Pool: host.PoolGPU})
	if err != nil {
		t.Fatalf("pick in the gpu pool: %v", err)
	}
	if got != "h-gpu" {
		t.Errorf("pick = %q, want h-gpu: a create routed to a pool is placed on that pool's machines", got)
	}

	// An empty preference is the default pool, the same rule an unstamped row
	// follows, so the two meet and the row is placeable.
	drawn := drawPicks(t, repo, placement.Prefs{}, 24)
	if drawn["h-gpu"] != 0 {
		t.Errorf("picks drew %v, want nothing from the gpu pool for a create that named none", drawn)
	}
	if drawn["h-default"]+drawn["h-unstamped"] == 0 {
		t.Errorf("picks drew %v, want the default pool's machines, stamped or not", drawn)
	}

	slots, err := repo.ListPlaceableCapacity(ctx, false, placement.Prefs{Pool: host.PoolGPU})
	if err != nil {
		t.Fatalf("list gpu pool capacity: %v", err)
	}
	if len(slots) != 1 || slots[0].HostID != "h-gpu" {
		t.Errorf("gpu pool capacity = %+v, want only h-gpu; the planner packs inside one pool", slots)
	}
}

// seedPoolHost is seedPlacementHost with the pool column stamped.
func seedPoolHost(t *testing.T, db *spannertest.TestDB, s placementSeed, pool string) {
	t.Helper()
	seedPlacementHost(t, db, s)
	_, err := db.Client.Apply(t.Context(), []*spanner.Mutation{
		spanner.UpdateMap("hosts", map[string]any{"host_id": s.id, "pool": host.StoredPool(pool)}),
	})
	if err != nil {
		t.Fatalf("stamp pool on %s: %v", s.id, err)
	}
}
