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
)

func TestRegionalQuotaDerivations(t *testing.T) {
	t.Parallel()
	q := &RegionalQuota{LimitValue: 100, UsageValue: 92}

	if got := q.Available(); got != 8 {
		t.Errorf("Available() = %d, want 8", got)
	}
	if !q.CanProvision(8) || q.CanProvision(9) {
		t.Errorf("CanProvision: want true at 8 and false at 9 (available %d)", q.Available())
	}
	if got := q.Percentage(); got != 92 {
		t.Errorf("Percentage() = %v, want 92", got)
	}
	if !q.IsNearLimit(85) || q.IsNearLimit(95) {
		t.Error("IsNearLimit: 92% usage must trip an 85% threshold and not a 95% one")
	}
	if !q.IsNearLimit(0) {
		t.Error("IsNearLimit(0) must apply the default 85% threshold")
	}
	if q.IsExhausted() {
		t.Error("IsExhausted() = true with capacity remaining")
	}

	over := &RegionalQuota{LimitValue: 10, UsageValue: 12}
	if got := over.Available(); got != 0 {
		t.Errorf("over-limit Available() = %d, want 0 (never negative)", got)
	}
	if !over.IsExhausted() {
		t.Error("over-limit quota must read as exhausted")
	}

	zero := &RegionalQuota{}
	if got := zero.Percentage(); got != 0 {
		t.Errorf("zero-limit Percentage() = %v, want 0 (no divide-by-zero)", got)
	}
}

func TestPreemptibleQuotaTwins(t *testing.T) {
	t.Parallel()
	twins := map[string]string{
		QuotaTypeCPUs:           QuotaTypePreemptibleCPUs,
		QuotaTypeNvidiaT4GPUs:   QuotaTypePreemptibleNvidiaT4GPUs,
		QuotaTypeNvidiaL4GPUs:   QuotaTypePreemptibleNvidiaL4GPUs,
		QuotaTypeNvidiaP4GPUs:   QuotaTypePreemptibleNvidiaP4GPUs,
		QuotaTypeNvidiaV100GPUs: QuotaTypePreemptibleNvidiaV100GPUs,
		QuotaTypeNvidiaA100GPUs: QuotaTypePreemptibleNvidiaA100GPUs,
		QuotaTypeNvidiaH100GPUs: QuotaTypePreemptibleNvidiaH100GPUs,
	}
	for standard, twin := range twins {
		if got := ToPreemptibleQuotaType(standard); got != twin {
			t.Errorf("ToPreemptibleQuotaType(%q) = %q, want %q", standard, got, twin)
		}
		if IsPreemptibleQuotaType(standard) {
			t.Errorf("IsPreemptibleQuotaType(%q) = true, want false", standard)
		}
		if !IsPreemptibleQuotaType(twin) {
			t.Errorf("IsPreemptibleQuotaType(%q) = false, want true", twin)
		}
	}
	// Types with no preemptible pool pass through unchanged, because the
	// caller must never invent a nonexistent quota metric.
	for _, unmapped := range []string{QuotaTypeInUseAddresses, QuotaTypeSSDTotalGB, QuotaTypeInstances} {
		if got := ToPreemptibleQuotaType(unmapped); got != unmapped {
			t.Errorf("ToPreemptibleQuotaType(%q) = %q, want unchanged", unmapped, got)
		}
	}
}

// Each machine family must resolve to the pool Compute Engine actually charges
// it: the shared flat pool for n1, a flat pool of its own for n2 and c3, the
// dimensioned per-family pool for everything newer, and nothing at all for g2.
func TestCPUQuotaTypeForFamily(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"n1":  QuotaTypeCPUs,
		"e2":  QuotaTypeCPUs,
		"n2":  QuotaTypeN2CPUs,
		"c3":  QuotaTypeC3CPUs,
		"n4":  "CPUS_PER_VM_FAMILY:N4",
		"c4":  "CPUS_PER_VM_FAMILY:C4",
		"c4a": "CPUS_PER_VM_FAMILY:C4A",
		"g2":  "",
		"":    "",
	}
	for family, want := range tests {
		if got := CPUQuotaTypeForFamily(family); got != want {
			t.Errorf("CPUQuotaTypeForFamily(%q) = %q, want %q", family, got, want)
		}
	}
}

func TestDimensionedQuotaTypeRoundTrip(t *testing.T) {
	t.Parallel()
	qualified := DimensionedQuotaType(QuotaTypeCPUsPerVMFamily, "C4")
	metric, dimension := QuotaTypeDimension(qualified)
	if metric != QuotaTypeCPUsPerVMFamily || dimension != "C4" {
		t.Errorf("QuotaTypeDimension(%q) = (%q, %q), want (%q, C4)",
			qualified, metric, dimension, QuotaTypeCPUsPerVMFamily)
	}
	if got := DimensionedQuotaType(QuotaTypeCPUs, ""); got != QuotaTypeCPUs {
		t.Errorf("undimensioned type = %q, want %q", got, QuotaTypeCPUs)
	}
	if _, dimension := QuotaTypeDimension(QuotaTypeCPUs); dimension != "" {
		t.Errorf("flat type carries dimension %q, want none", dimension)
	}
	// Every CPU pool draws the one flat preemptible grant.
	if got := ToPreemptibleQuotaType(qualified); got != QuotaTypePreemptibleCPUs {
		t.Errorf("ToPreemptibleQuotaType(%q) = %q, want %q", qualified, got, QuotaTypePreemptibleCPUs)
	}
	for _, cpu := range []string{QuotaTypeN2CPUs, QuotaTypeC3CPUs} {
		if got := ToPreemptibleQuotaType(cpu); got != QuotaTypePreemptibleCPUs {
			t.Errorf("ToPreemptibleQuotaType(%q) = %q, want %q", cpu, got, QuotaTypePreemptibleCPUs)
		}
	}
}

// A hyperdisk-balanced boot disk fills a different pool from a pd-* one, and
// counts against no other.
func TestBootDiskQuotaType(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"hyperdisk-balanced": QuotaTypeHDBTotalGB,
		"pd-balanced":        QuotaTypeSSDTotalGB,
		"pd-ssd":             QuotaTypeSSDTotalGB,
		"pd-standard":        "",
		"":                   "",
	}
	for diskType, want := range tests {
		if got := BootDiskQuotaType(diskType); got != want {
			t.Errorf("BootDiskQuotaType(%q) = %q, want %q", diskType, got, want)
		}
	}
	// The disk axes have no preemptible twin: a boot disk is charged the same
	// pool whichever provisioning model the host runs under.
	for _, disk := range []string{QuotaTypeSSDTotalGB, QuotaTypeHDBTotalGB} {
		if got := ToPreemptibleQuotaType(disk); got != disk {
			t.Errorf("ToPreemptibleQuotaType(%q) = %q, want unchanged", disk, got)
		}
	}
}

// The fallback to the standard pools is a property of the region, not of the
// row: one granted preemptible pool puts every Spot VM in the region on
// preemptible quota.
func TestHasPreemptibleGrant(t *testing.T) {
	t.Parallel()
	zeroGrants := make([]*RegionalQuota, 0, 3)
	zeroGrants = append(zeroGrants,
		&RegionalQuota{QuotaType: QuotaTypePreemptibleCPUs, LimitValue: 0},
		&RegionalQuota{QuotaType: QuotaTypeCPUs, LimitValue: 3000})
	if HasPreemptibleGrant(zeroGrants) {
		t.Error("a region whose only preemptible row is granted 0 has no preemptible grant")
	}
	granted := append(zeroGrants,
		&RegionalQuota{QuotaType: QuotaTypePreemptibleNvidiaL4GPUs, LimitValue: 16})
	if !HasPreemptibleGrant(granted) {
		t.Error("one granted preemptible pool puts the whole region on preemptible quota")
	}
	if HasPreemptibleGrant(nil) {
		t.Error("a region with no rows at all cannot show a grant")
	}
}

func TestQuotaTypeForGPU(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"nvidia-tesla-t4":   QuotaTypeNvidiaT4GPUs,
		"nvidia-l4":         QuotaTypeNvidiaL4GPUs,
		"nvidia-tesla-p4":   QuotaTypeNvidiaP4GPUs,
		"nvidia-tesla-v100": QuotaTypeNvidiaV100GPUs,
		"nvidia-tesla-a100": QuotaTypeNvidiaA100GPUs,
		"nvidia-a100-80gb":  QuotaTypeNvidiaA100GPUs,
		"nvidia-h100-80gb":  QuotaTypeNvidiaH100GPUs,
		"tpu-v5":            "",
		"":                  "",
	}
	for gpuType, want := range tests {
		if got := QuotaTypeForGPU(gpuType); got != want {
			t.Errorf("QuotaTypeForGPU(%q) = %q, want %q", gpuType, got, want)
		}
	}
	// Every mapped metric must have a preemptible twin: a Spot spec's GPU
	// demand always resolves to a real preemptible pool.
	for gpuType, standard := range tests {
		if standard == "" {
			continue
		}
		if twin := ToPreemptibleQuotaType(standard); twin == standard {
			t.Errorf("GPU %q maps to %q which has no preemptible twin", gpuType, standard)
		}
	}
}
