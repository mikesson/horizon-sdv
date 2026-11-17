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

// censusSeed is one host row for the zone × family census.
type censusSeed struct {
	id, zone, machineType, status string
	devices                       int64
}

// seedCensusHost plants one host row for the zone × family census.
func seedCensusHost(t *testing.T, db *spannertest.TestDB, s censusSeed) {
	t.Helper()
	now := time.Now()
	row := map[string]any{
		"host_id":                s.id,
		"name":                   s.id,
		"zone":                   s.zone,
		"status":                 s.status,
		"machine_type":           s.machineType,
		"host_orchestrator_port": int64(2080),
		"max_devices":            int64(4),
		"current_device_count":   s.devices,
		"exclusive":              false,
		"lifecycle_mode":         "ephemeral",
		"protected_device_count": int64(0),
		"created_at":             now,
		"updated_at":             now,
	}
	if _, err := db.Client.Apply(t.Context(), []*spanner.Mutation{spanner.InsertMap("hosts", row)}); err != nil {
		t.Fatalf("seed census host %s: %v", s.id, err)
	}
}

// find returns the census cell for one (zone, family) pair.
func findCell(t *testing.T, cells []host.ZoneFamilyCount, zone, family string) host.ZoneFamilyCount {
	t.Helper()
	for _, c := range cells {
		if c.Zone == zone && c.MachineFamily == family {
			return c
		}
	}
	t.Fatalf("no census cell for %s/%s in %+v", zone, family, cells)
	return host.ZoneFamilyCount{}
}

// The census folds a zone's machine types into their families, counts the
// devices they carry, and separates the machines still coming up from the ones
// already serving, while leaving deleted tombstones out entirely.
func TestCountByZoneFamily(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)

	// Two n1 shapes in one zone: different machine types, one family.
	seedCensusHost(t, db, censusSeed{id: "n1-running", zone: "europe-west4-a", machineType: "n1-standard-8", status: "running", devices: 3})
	seedCensusHost(t, db, censusSeed{id: "n1-bigger", zone: "europe-west4-a", machineType: "n1-standard-16", status: "running", devices: 1})
	seedCensusHost(t, db, censusSeed{id: "n1-arriving", zone: "europe-west4-a", machineType: "n1-standard-8", status: "provisioning"})
	// A second family in the same zone, and one in another.
	seedCensusHost(t, db, censusSeed{id: "c4-running", zone: "europe-west4-a", machineType: "c4-standard-8", status: "running", devices: 2})
	seedCensusHost(t, db, censusSeed{id: "n2-elsewhere", zone: "us-central1-a", machineType: "n2-standard-8", status: "pending"})
	// Torn down: a tombstone is not capacity.
	seedCensusHost(t, db, censusSeed{id: "n1-gone", zone: "europe-west4-a", machineType: "n1-standard-8", status: "deleted"})

	cells, err := repo.CountByZoneFamily(t.Context())
	if err != nil {
		t.Fatalf("CountByZoneFamily: %v", err)
	}
	if len(cells) != 3 {
		t.Fatalf("cells = %+v, want 3 (n1 and c4 in europe-west4-a, n2 in us-central1-a)", cells)
	}

	n1 := findCell(t, cells, "europe-west4-a", "n1")
	if n1.Machines != 3 {
		t.Errorf("n1 machines = %d, want 3: two shapes of one family fold into one cell, the deleted row into none", n1.Machines)
	}
	if n1.Arriving != 1 {
		t.Errorf("n1 arriving = %d, want 1: arriving is a subset of machines, never a term beside it", n1.Arriving)
	}
	if n1.Devices != 4 {
		t.Errorf("n1 devices = %d, want 4", n1.Devices)
	}

	c4 := findCell(t, cells, "europe-west4-a", "c4")
	if c4.Machines != 1 || c4.Devices != 2 || c4.Arriving != 0 {
		t.Errorf("c4 cell = %+v, want 1 machine / 2 devices / 0 arriving", c4)
	}

	n2 := findCell(t, cells, "us-central1-a", "n2")
	if n2.Machines != 1 || n2.Arriving != 1 {
		t.Errorf("n2 cell = %+v, want a single pending machine counted as arriving", n2)
	}
}
