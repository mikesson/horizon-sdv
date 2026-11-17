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

package gcecatalog

import (
	"testing"
	"time"
)

func TestNextCooldownUntil(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		prevFailures int64
		want         time.Duration
	}{
		{0, 5 * time.Minute},
		{1, 10 * time.Minute},
		{2, 20 * time.Minute},
		{3, 40 * time.Minute},
		{4, time.Hour}, // 80m capped
		{5, time.Hour},
		{63, time.Hour}, // deep histories must not overflow past the cap
	}
	for _, tt := range tests {
		got := NextCooldownUntil(tt.prevFailures, now)
		if want := now.Add(tt.want); !got.Equal(want) {
			t.Errorf("NextCooldownUntil(%d) = %v after now, want %v",
				tt.prevFailures, got.Sub(now), tt.want)
		}
	}
}

func TestInCooldown(t *testing.T) {
	t.Parallel()
	now := time.Now()
	future := now.Add(time.Minute)
	past := now.Add(-time.Minute)

	if (&ZoneHealth{}).InCooldown(now) {
		t.Error("a cell with no cooldown must not repel placement")
	}
	if !(&ZoneHealth{CooldownUntil: &future}).InCooldown(now) {
		t.Error("a cell cooling until the future must repel placement")
	}
	if (&ZoneHealth{CooldownUntil: &past}).InCooldown(now) {
		t.Error("a lapsed cooldown must not repel placement")
	}
}

// One definition of a standing stockout, and both halves of it have to bite: a
// quota refusal says nothing about capacity, and a lapsed cooldown is a shape
// the walk prefers again. A page dating observations the walk has forgotten
// would explain a step that is not happening.
func TestFreshCapacityCellsAreTheOnesStillStanding(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Minute)
	past := now.Add(-time.Minute)

	cells := []*ZoneHealth{
		{Zone: "europe-west4-a", MachineFamily: "n4", BindingMetric: ZoneMetricCapacity, CooldownUntil: &future},
		{Zone: "europe-west4-b", MachineFamily: "n4", BindingMetric: ZoneMetricCapacity, CooldownUntil: &past},
		{Zone: "europe-west4-c", MachineFamily: "n4", BindingMetric: ZoneMetricQuotaCPU, CooldownUntil: &future},
	}

	fresh := FreshCapacityCells(cells, now)
	if len(fresh) != 1 || fresh[0].Zone != "europe-west4-a" {
		t.Fatalf("fresh = %+v, want the cooling capacity cell alone", fresh)
	}

	stocked := StockedOutZones(cells, now)
	if !stocked["n4"]["europe-west4-a"] || len(stocked["n4"]) != 1 {
		t.Errorf("stocked zones = %v, want europe-west4-a alone; the map must fold the same cells", stocked["n4"])
	}
}

func TestIsRegionalMetric(t *testing.T) {
	t.Parallel()
	regional := []string{
		ZoneMetricQuotaCPU, ZoneMetricQuotaGPU, ZoneMetricQuotaIP,
		ZoneMetricQuotaSSD, ZoneMetricQuotaInstance,
	}
	for _, m := range regional {
		if !IsRegionalMetric(m) {
			t.Errorf("IsRegionalMetric(%q) = false, want true (quota is regional)", m)
		}
	}
	zonal := []string{ZoneMetricCapacity, ZoneMetricAvailability, ZoneMetricPreemption, "bogus"}
	for _, m := range zonal {
		if IsRegionalMetric(m) {
			t.Errorf("IsRegionalMetric(%q) = true, want false", m)
		}
	}
}
