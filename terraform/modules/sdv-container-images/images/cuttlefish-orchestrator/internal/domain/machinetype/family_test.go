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
	"errors"
	"testing"
)

func TestFamilyOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		machineType string
		want        string
	}{
		{name: "standard n1 machine type", machineType: "n1-standard-8", want: "n1"},
		{name: "n2 machine type", machineType: "n2-standard-16", want: "n2"},
		{name: "n2d machine type", machineType: "n2d-highmem-32", want: "n2d"},
		{name: "c4a ARM machine type", machineType: "c4a-standard-4", want: "c4a"},
		{name: "g2 GPU machine type", machineType: "g2-standard-8", want: "g2"},
		{name: "a2 GPU machine type", machineType: "a2-highgpu-1g", want: "a2"},
		{name: "legacy custom machine type without prefix", machineType: "custom-4-16384", want: "n1"},
		{name: "n2 custom machine type with prefix", machineType: "n2-custom-4-16384", want: "n2"},
		{name: "n2d custom machine type with prefix", machineType: "n2d-custom-8-32768", want: "n2d"},
		{name: "e2 custom machine type", machineType: "e2-custom-4-8192", want: "e2"},
		{name: "empty string", machineType: "", want: ""},
		{name: "no hyphen in machine type", machineType: "n1standard8", want: "n1standard8"},
		// The n4 namespace holds three families with different processors and
		// different architectures. The separator is what keeps them apart, and
		// a prefix match would fold the Axion and AMD series into the Intel
		// one an operator curated.
		{name: "n4 Intel", machineType: "n4-standard-80", want: "n4"},
		{name: "n4a Axion", machineType: "n4a-standard-8", want: "n4a"},
		{name: "n4d AMD", machineType: "n4d-standard-8", want: "n4d"},
		// Suffix variants are ordinary name segments, not families.
		{name: "bare metal suffix", machineType: "c4-standard-288-metal", want: "c4"},
		{name: "local SSD and metal suffixes", machineType: "c4-standard-288-lssd-metal", want: "c4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := FamilyOf(tt.machineType); got != tt.want {
				t.Errorf("FamilyOf(%q) = %q, want %q", tt.machineType, got, tt.want)
			}
		})
	}
}

// The custom grammar recognizes the FORM and the series, and reads no numbers:
// what a family will actually build is machineTypes.get's answer, so nothing
// here has to know an increment or a memory band.
func TestCustomFamily(t *testing.T) {
	t.Parallel()
	tests := []struct {
		machineType string
		wantFamily  string
		wantOK      bool
	}{
		{machineType: "n4-custom-8-16384", wantFamily: "n4", wantOK: true},
		{machineType: "n2-custom-8-16384-ext", wantFamily: "n2", wantOK: true},
		{machineType: "custom-4-16384", wantFamily: "n1", wantOK: true},
		// E2's shared-core custom shapes spell a size where the others spell a
		// vCPU count. The form is still recognized, which is the point of not
		// parsing the numbers; E2 is excluded from this deployment for nested
		// virtualization anyway, so no family row admits it.
		{machineType: "e2-custom-medium-2048", wantFamily: "e2", wantOK: true},
		// Predefined shapes are not custom, and neither is a near-miss.
		{machineType: "n4-standard-80", wantOK: false},
		{machineType: "n4-customer-8", wantOK: false},
		{machineType: "custom", wantOK: false},
		{machineType: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.machineType, func(t *testing.T) {
			t.Parallel()
			family, ok := CustomFamily(tt.machineType)
			if ok != tt.wantOK {
				t.Fatalf("CustomFamily(%q) recognized = %v, want %v", tt.machineType, ok, tt.wantOK)
			}
			if ok && family != tt.wantFamily {
				t.Errorf("CustomFamily(%q) family = %q, want %q", tt.machineType, family, tt.wantFamily)
			}
		})
	}
}

// A display name is derived, never entered: the catalog holds hundreds of
// shapes nobody has ever typed.
func TestDisplayName(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"n4-standard-48":             "N4 Standard 48",
		"n2-highmem-128":             "N2 Highmem 128",
		"n2-highcpu-96":              "N2 Highcpu 96",
		"g2-standard-4":              "G2 Standard 4",
		"c4a-highmem-96-metal":       "C4A Highmem 96 Metal",
		"c4-standard-288-lssd-metal": "C4 Standard 288 Lssd Metal",
		"n4-custom-8-16384":          "N4 Custom 8 16384",
	}
	for machineType, want := range tests {
		t.Run(machineType, func(t *testing.T) {
			t.Parallel()
			if got := DisplayName(machineType); got != want {
				t.Errorf("DisplayName(%q) = %q, want %q", machineType, got, want)
			}
		})
	}
}

// A family's own name is derived the same way when an operator wrote none,
// which is what makes enabling a series Google shipped this morning one act
// with nothing else to fill in.
func TestFamilyDisplayName(t *testing.T) {
	t.Parallel()
	if got := FamilyDisplayName("n5", CategoryGeneralPurpose); got != "N5 General Purpose" {
		t.Errorf("FamilyDisplayName = %q, want %q", got, "N5 General Purpose")
	}
	if got := FamilyDisplayName("c4a", CategoryComputeOptimized); got != "C4A Compute Optimized" {
		t.Errorf("FamilyDisplayName = %q, want %q", got, "C4A Compute Optimized")
	}
	curated := &FamilyCuration{Family: "g2", Category: CategoryAcceleratorOptimized, Name: "G2 GPU (L4)"}
	if got := curated.DisplayName(); got != "G2 GPU (L4)" {
		t.Errorf("an operator's own name was overwritten: %q", got)
	}
}

func TestCheckGPUPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		maxGPUs int64
		count   int64
		wantErr bool
	}{
		{name: "no GPU policy", maxGPUs: 0, count: 0},
		{name: "count at the ceiling", maxGPUs: 2, count: 2},
		{name: "count under the ceiling", maxGPUs: 4, count: 1},
		{name: "count over the ceiling", maxGPUs: 1, count: 2, wantErr: true},
		{name: "count with no ceiling", maxGPUs: 0, count: 1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			family := &FamilyCuration{Family: "n1", MaxGPUs: tt.maxGPUs, DefaultGPUCount: tt.count}
			err := family.CheckGPUPolicy()
			if _, ok := errors.AsType[*GPUPolicyError](err); ok != tt.wantErr {
				t.Errorf("CheckGPUPolicy() = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}
