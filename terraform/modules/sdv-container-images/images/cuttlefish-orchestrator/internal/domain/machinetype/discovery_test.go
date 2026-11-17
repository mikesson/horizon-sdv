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

package machinetype

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// curatedN1 is a family admitted with nothing else to say, so a composition
// built on it reports the shape's own facts and no curation of its own.
func curatedN1() *FamilyCuration {
	return &FamilyCuration{
		Family: "n1", Category: CategoryGeneralPurpose, Architecture: ArchX86, IsEnabled: true,
	}
}

// The admission floor is computed from the guest shape. A machine that cannot
// hold one default-shaped Android VM is a machine no device can ever be
// placed on.
//
// The boundary is arithmetic, so it moves with the guest shape rather than
// with a number written here: 8 GB of guest RAM plus a 4 GB host reserve needs
// 12 GB, and Compute Engine offers plenty of shapes below it:
// n4a-standard-1 at 4 GB, n2-highcpu-4 at 4 GB, c4-standard-2 at 7 GB.
func TestAdmissibleFloor(t *testing.T) {
	t.Parallel()
	// The floor stated once, from the arithmetic, so this test cannot pass by
	// agreeing with a constant it also chose.
	floorGB := (device.HostReserveMemoryMB + device.DefaultInstanceMemoryMB) / 1024

	tests := map[string]struct {
		memoryGB int64
		want     bool
	}{
		"a shape too small for one guest": {memoryGB: floorGB - 1, want: false},
		"exactly one guest":               {memoryGB: floorGB, want: true},
		"n4a-standard-1":                  {memoryGB: 4, want: false},
		"c4-standard-2":                   {memoryGB: 7, want: false},
		"n1-standard-4":                   {memoryGB: 15, want: true},
		"g2-standard-4":                   {memoryGB: 16, want: true},
		"n4-standard-80":                  {memoryGB: 320, want: true},
		// A shape that states no size states no bound, and every other sizing
		// rule passes it for the same reason.
		"unmeasurable": {memoryGB: 0, want: true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			entry := Compose(Shape{Name: name, MemoryGB: tt.memoryGB}, curatedN1(), nil)
			if got := entry.Admissible(); got != tt.want {
				t.Errorf("Admissible() at %d GB = %v, want %v", tt.memoryGB, got, tt.want)
			}
		})
	}
}

// The floor is the packing rule's own arithmetic asked at its smallest
// interesting point. If the two ever disagreed, the catalog would offer a
// machine that packing then refuses to put anything on.
func TestAdmissionAgreesWithPacking(t *testing.T) {
	t.Parallel()
	for memoryGB := int64(1); memoryGB <= 64; memoryGB++ {
		entry := Compose(Shape{Name: "probe", VCPUs: 8, MemoryGB: memoryGB}, curatedN1(), nil)
		fit, ok := device.MemoryFitInstances(memoryGB)
		if got, want := entry.Admissible(), !ok || fit >= 1; got != want {
			t.Fatalf("at %d GB: Admissible() = %v, but the packing fit is %d (measurable %v)",
				memoryGB, got, fit, ok)
		}
	}
}

// The operator's stored capacity is PART of admission rather than a decoration
// on an entry admission already let through.
//
// Judged on the raw shape, the escape hatch is unreachable for the one machine
// it exists for: the catalog drops a below-floor shape before the override is
// ever laid over it, and every surface downstream (a shape ladder above all)
// then reports the machine as one this platform cannot run.
func TestStoredCapacityAdmitsAShapeTheArithmeticRefuses(t *testing.T) {
	t.Parallel()
	// 2 vCPUs is one over the host's own CPU reserve, so the effective-CPU
	// bound alone holds no guest however much memory the machine carries.
	shape := Shape{Name: "n1-highmem-2", Family: "n1", VCPUs: 2, MemoryGB: 13, Architecture: ArchX86}

	refused := Compose(shape, curatedN1(), nil)
	if refused.Fit != 0 || refused.Admissible() {
		t.Fatalf("un-overridden n1-highmem-2 = fit %d admissible %v, want fit 0 and refused",
			refused.Fit, refused.Admissible())
	}

	override := &ShapeOverride{MachineType: shape.Name, MaxDevices: new(int64(1))}
	admitted := Compose(shape, curatedN1(), override)
	if !admitted.Admissible() {
		t.Error("a stored capacity of one did not admit the shape; the override cannot reach admission")
	}
	if admitted.Fit != 1 || !admitted.FitOverridden {
		t.Errorf("admitted entry = fit %d overridden %v, want the operator's 1 marked as theirs",
			admitted.Fit, admitted.FitOverridden)
	}

	// The curation view answers the same question, so a shape the catalog
	// serves can never read as unviable on the page an operator curates from.
	view := FamilyShapeOf(shape, override)
	if !view.Viable || view.Fit != 0 {
		t.Errorf("curation view = viable %v fit %d, want viable with the computed 0 still shown",
			view.Viable, view.Fit)
	}
	if bare := FamilyShapeOf(shape, nil); bare.Viable {
		t.Error("the same shape reads as viable with no override stored")
	}
}

// The three refusals have three remedies, so each states its own. A single
// "not found" sent an operator to enable a family that was already enabled.
func TestUnofferedErrorNamesTheRealReason(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  *UnofferedError
		says string
	}{
		{
			"below the floor",
			&UnofferedError{MachineType: "n1-highmem-2", Reason: ReasonBelowFloor, VCPUs: 2, MemoryGB: 13},
			"2 vCPUs and 13 GB hold no Android VM at all; set a per-shape capacity override",
		},
		{
			"family not curated",
			&UnofferedError{MachineType: "n7-standard-8", Reason: ReasonFamilyNotCurated, Family: "n7"},
			"nothing admits its n7 machine family; enable its machine family first",
		},
		{
			"no such machine",
			&UnofferedError{MachineType: "n1-standard-999", Reason: ReasonUnknown},
			"Compute Engine builds no machine of that name",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(tc.err.Error(), tc.says) {
				t.Errorf("refusal = %q, want it to say %q", tc.err.Error(), tc.says)
			}
			if !errors.Is(tc.err, ErrNotFound) {
				t.Error("the typed refusal stopped matching ErrNotFound, which is what maps it to a 404")
			}
		})
	}
}

// The list endpoint's rows are synthesized now, so the shape they take on the
// wire is asserted field by field against a recorded row. The console and cfctl
// are drift-gated against this schema and neither was touched; a field renamed,
// dropped or re-typed here breaks both.
func TestComposedEntryWireShape(t *testing.T) {
	t.Parallel()
	stamped := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	family := &FamilyCuration{
		Family:       "n1",
		Description:  "General-purpose, GPU-capable",
		Category:     CategoryGeneralPurpose,
		Architecture: ArchX86,
		CPUType:      "Intel Skylake/Broadwell/Haswell",
		BootDiskType: "pd-balanced",
		GPUMode:      GPUModeAttachable,
		MaxGPUs:      4, DefaultGPUType: "nvidia-tesla-t4", DefaultGPUCount: 0,
		IsEnabled: true, SortOrder: 100,
		CreatedAt: stamped, UpdatedAt: stamped,
	}
	shape := Shape{
		Name: "n1-standard-8", Family: "n1", VCPUs: 8, MemoryGB: 30, Architecture: ArchX86,
	}
	override := &ShapeOverride{
		MachineType: "n1-standard-8", IsDefault: true,
		CreatedAt: stamped, UpdatedAt: stamped,
	}

	encoded, err := json.Marshal(Compose(shape, family, override))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	golden := `{"id":"n1-standard-8","family":"n1","name":"N1 Standard 8",` +
		`"description":"General-purpose, GPU-capable, 8 vCPUs, 30 GB RAM",` +
		`"category":"general_purpose","architecture":"x86",` +
		`"cpu_type":"Intel Skylake/Broadwell/Haswell","vcpus":8,"memory_gb":30,"fit":3,` +
		`"gpu_support":true,"max_gpus":4,"default_gpu_type":"nvidia-tesla-t4",` +
		`"boot_disk_type":"pd-balanced","gpu_mode":"attachable",` +
		`"is_default":true,"is_enabled":true,"sort_order":100,` +
		`"created_at":"2026-08-14T09:00:00Z","updated_at":"2026-08-14T09:00:00Z"}`
	if string(encoded) != golden {
		t.Errorf("the composed entry's wire shape changed.\n got: %s\nwant: %s", encoded, golden)
	}
}

// An integrated accelerator is read off the shape Compute Engine reported and
// outranks any family GPU policy: its count is what the hardware has, so a
// curated number would describe a machine that does not exist.
func TestComposeReadsTheIntegratedGPUFromDiscovery(t *testing.T) {
	t.Parallel()
	family := &FamilyCuration{
		Family: "g2", Category: CategoryAcceleratorOptimized, Architecture: ArchX86,
		// Wrong for the shape on purpose: attachable with four cards. The
		// discovered accelerator has to win.
		GPUMode: GPUModeAttachable, MaxGPUs: 4, DefaultGPUType: "nvidia-tesla-t4",
		IsEnabled: true,
	}
	shape := Shape{
		Name: "g2-standard-4", Family: "g2", VCPUs: 4, MemoryGB: 16,
		Architecture: ArchX86, GPUType: "nvidia-l4", GPUCount: 1,
	}

	entry := Compose(shape, family, nil)
	if entry.GPUMode != GPUModeIntegrated {
		t.Errorf("gpu_mode = %q, want integrated", entry.GPUMode)
	}
	if entry.IntegratedGPUType != "nvidia-l4" || entry.IntegratedGPUCount != 1 {
		t.Errorf("integrated GPU = %s x%d, want nvidia-l4 x1",
			entry.IntegratedGPUType, entry.IntegratedGPUCount)
	}
	if entry.MaxGPUs != 1 {
		t.Errorf("max_gpus = %d, want the hardware's 1, not the family's policy", entry.MaxGPUs)
	}
	if entry.DefaultGPUType != "" {
		t.Errorf("default_gpu_type = %q, want empty: an integrated GPU is not attached",
			entry.DefaultGPUType)
	}
}

// A shape Compute Engine reported without an architecture takes the family's,
// so a series whose shapes predate the field is not silently reclassified.
func TestComposeFallsBackToTheFamilyArchitecture(t *testing.T) {
	t.Parallel()
	family := &FamilyCuration{Family: "c4a", Architecture: ArchArm, IsEnabled: true}
	entry := Compose(Shape{Name: "c4a-highmem-96-metal", Family: "c4a", VCPUs: 96, MemoryGB: 768}, family, nil)
	if entry.Architecture != ArchArm {
		t.Errorf("architecture = %q, want the family's arm", entry.Architecture)
	}
	if !entry.IsBareMetal() {
		t.Error("the -metal suffix stopped reading as bare metal")
	}
}
