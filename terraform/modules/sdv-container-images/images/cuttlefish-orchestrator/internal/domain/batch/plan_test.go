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

package batch

import (
	"slices"
	"testing"
)

func TestPlan(t *testing.T) {
	t.Parallel()
	const mph = 4
	tests := []struct {
		name           string
		count          int
		placement      Placement
		existing       []HostSlot
		headroom       int
		wantNewHosts   int
		wantAdmitted   int
		wantOnExisting []Assignment
	}{
		{
			name: "isolated exact multiple", count: 8, placement: PlacementIsolated,
			headroom: 10, wantNewHosts: 2, wantAdmitted: 8,
		},
		{
			name: "isolated with remainder rounds up", count: 9, placement: PlacementIsolated,
			headroom: 10, wantNewHosts: 3, wantAdmitted: 9,
		},
		{
			name: "isolated count 1 still provisions a host", count: 1, placement: PlacementIsolated,
			headroom: 10, wantNewHosts: 1, wantAdmitted: 1,
		},
		{
			name: "isolated ignores existing free capacity", count: 4, placement: PlacementIsolated,
			existing: []HostSlot{{"h1", 4}}, headroom: 10, wantNewHosts: 1, wantAdmitted: 4,
		},
		{
			name: "pack fully absorbed by existing", count: 8, placement: PlacementPack,
			existing: []HostSlot{{"h1", 5}, {"h2", 5}}, headroom: 10,
			wantNewHosts: 0, wantAdmitted: 8,
			wantOnExisting: []Assignment{{"h1", 5}, {"h2", 3}},
		},
		{
			name: "pack fills existing then provisions remainder", count: 9, placement: PlacementPack,
			existing: []HostSlot{{"h1", 2}}, headroom: 10,
			wantNewHosts: 2, wantAdmitted: 9, // 2 on h1, 7 remaining -> ceil(7/4)=2
			wantOnExisting: []Assignment{{"h1", 2}},
		},
		{
			name: "pack skips zero-free hosts", count: 3, placement: PlacementPack,
			existing: []HostSlot{{"h1", 0}, {"h2", 2}}, headroom: 10,
			wantNewHosts: 1, wantAdmitted: 3,
			wantOnExisting: []Assignment{{"h2", 2}},
		},
		{
			name: "headroom exactly sufficient", count: 8, placement: PlacementIsolated,
			headroom: 2, wantNewHosts: 2, wantAdmitted: 8,
		},
		{
			name: "isolated clamps to headroom", count: 20, placement: PlacementIsolated,
			headroom: 3, wantNewHosts: 3, wantAdmitted: 12, // 3 hosts * 4
		},
		{
			name: "pack clamps remainder to headroom", count: 20, placement: PlacementPack,
			existing: []HostSlot{{"h1", 2}}, headroom: 2,
			wantNewHosts: 2, wantAdmitted: 10, // 2 existing + 2*4
			wantOnExisting: []Assignment{{"h1", 2}},
		},
		{
			name: "zero headroom pack rides existing only", count: 5, placement: PlacementPack,
			existing: []HostSlot{{"h1", 3}}, headroom: 0,
			wantNewHosts: 0, wantAdmitted: 3,
			wantOnExisting: []Assignment{{"h1", 3}},
		},
		{
			name: "count zero is a no-op plan", count: 0, placement: PlacementIsolated,
			headroom: 10, wantNewHosts: 0, wantAdmitted: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Compute(tc.count, tc.placement, tc.existing, mph, tc.headroom)
			if got.NewHosts != tc.wantNewHosts {
				t.Errorf("NewHosts = %d, want %d", got.NewHosts, tc.wantNewHosts)
			}
			if got.Admitted != tc.wantAdmitted {
				t.Errorf("Admitted = %d, want %d", got.Admitted, tc.wantAdmitted)
			}
			if !slices.Equal(got.OnExisting, tc.wantOnExisting) {
				t.Errorf("OnExisting = %v, want %v", got.OnExisting, tc.wantOnExisting)
			}
			// Invariant: expanding the plan yields exactly Admitted devices.
			if total := countAssigned(got); total != got.Admitted {
				t.Errorf("assigned device count = %d, want Admitted %d", total, got.Admitted)
			}
		})
	}
}

// countAssigned expands the plan (with synthetic new-host ids) and counts.
func countAssigned(p Plan) int {
	ids := make([]string, p.NewHosts)
	for i := range ids {
		ids[i] = "new"
	}
	return len(p.Assign(ids))
}

func TestPlanAssign(t *testing.T) {
	t.Parallel()
	// 2 on an existing host + 6 across 2 new hosts (4 + 2).
	p := Compute(8, PlacementPack, []HostSlot{{"h-existing", 2}}, 4, 10)
	if p.NewHosts != 2 || p.Admitted != 8 {
		t.Fatalf("unexpected plan: %+v", p)
	}
	got := p.Assign([]string{"new-a", "new-b"})
	want := []string{"h-existing", "h-existing", "new-a", "new-a", "new-a", "new-a", "new-b", "new-b"}
	if !slices.Equal(got, want) {
		t.Errorf("Assign = %v, want %v", got, want)
	}
}

func TestPlanAssignIsolated(t *testing.T) {
	t.Parallel()
	p := Compute(5, PlacementIsolated, nil, 4, 10) // 2 hosts: 4 + 1
	got := p.Assign([]string{"h0", "h1"})
	want := []string{"h0", "h0", "h0", "h0", "h1"}
	if !slices.Equal(got, want) {
		t.Errorf("Assign = %v, want %v", got, want)
	}
}
