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
	"fmt"
	"strings"
	"time"
)

// FamilyOf names the machine family a machine type belongs to.
//
//   - "n1-standard-8" → "n1"
//   - "c4a-highmem-96-metal" → "c4a"
//   - "n2-custom-4-16384" → "n2"
//   - "custom-4-16384" → "n1" (the unprefixed custom form is N1's)
//
// The separator is what makes this a split rather than a prefix test: "c4-"
// and "c4a-" are different families, and a prefix match on "c4" would fold one
// into the other.
func FamilyOf(machineType string) string {
	if machineType == "" {
		return ""
	}
	if strings.HasPrefix(machineType, customSegment+"-") {
		return "n1"
	}
	if idx := strings.Index(machineType, "-"); idx > 0 {
		return machineType[:idx]
	}
	return machineType
}

// customSegment is the name segment that marks a custom shape.
const customSegment = "custom"

// CustomFamily recognizes a custom machine type by its name and answers the
// family it is built on.
//
// Custom shapes never appear in the machineTypes list, because Compute Engine
// constructs the resource on demand, so a catalog that only enumerated what
// the aggregated list reports could never offer one. CCO's grammar is just the
// shape of the name and the series in front of it.
//
//	n4-custom-8-16384        → n4
//	n2-custom-8-16384-ext    → n2
//	e2-custom-medium-2048    → e2
//	custom-4-16384           → n1 (the unprefixed form is N1's)
//
// Nothing here reads the numbers. What a family permits (the vCPU increment,
// the memory band, the shared-core spellings, whether it offers custom shapes
// at all) is Compute Engine's answer, and machineTypes.get gives it for a
// specific name along with the machine's real size. Restating those rules here
// would be a per-family table to maintain every time Google ships a series,
// which is exactly the maintenance this catalog exists to abolish.
func CustomFamily(machineType string) (string, bool) {
	segments := strings.Split(machineType, "-")
	switch {
	case len(segments) >= 2 && segments[0] == customSegment:
		return "n1", true
	case len(segments) >= 3 && segments[1] == customSegment && segments[0] != "":
		return segments[0], true
	default:
		return "", false
	}
}

// DisplayName renders a machine type's name for a human: "n4-standard-48"
// becomes "N4 Standard 48", "c4a-highmem-96-metal" becomes
// "C4A Highmem 96 Metal".
//
// Derived rather than stored, because the catalog holds hundreds of shapes it
// never saw before and a stored name is a name somebody has to write. The
// family segment is the machine series and reads as an initialism; every other
// segment is an ordinary word or a number.
func DisplayName(machineType string) string {
	segments := strings.Split(machineType, "-")
	for i, segment := range segments {
		if i == 0 {
			segments[i] = strings.ToUpper(segment)
			continue
		}
		segments[i] = titleSegment(segment)
	}
	return strings.Join(segments, " ")
}

// titleSegment capitalizes a name segment, leaving numbers alone.
func titleSegment(segment string) string {
	if segment == "" {
		return segment
	}
	return strings.ToUpper(segment[:1]) + segment[1:]
}

// FamilyDisplayName renders a family's name for a human when an operator has
// written none: the series in caps beside what its workload category is called.
func FamilyDisplayName(family string, category Category) string {
	name := strings.ToUpper(family)
	if label := categoryLabel(category); label != "" {
		return name + " " + label
	}
	return name
}

// categoryLabel renders a workload category as the words a family name uses.
func categoryLabel(category Category) string {
	switch category {
	case CategoryGeneralPurpose:
		return "General Purpose"
	case CategoryComputeOptimized:
		return "Compute Optimized"
	case CategoryMemoryOptimized:
		return "Memory Optimized"
	case CategoryAcceleratorOptimized:
		return "Accelerator Optimized"
	case CategoryStorageOptimized:
		return "Storage Optimized"
	case CategoryCostOptimized:
		return "Cost Optimized"
	default:
		return ""
	}
}

// FamilyCuration is one machine family an operator has admitted, carrying the
// facts about it Compute Engine's machine-type resource does not state.
//
// Enabling a family is the whole curation act: every shape the family offers
// arrives from discovery, sized by Compute Engine, and a family Google ships
// tomorrow needs this row and nothing else. Anything per-shape here would be
// maintenance the catalog exists to abolish.
type FamilyCuration struct {
	// Family is the series identifier ("n1", "c4a").
	Family string `json:"family"`

	// Name is the operator's display name; empty renders FamilyDisplayName.
	Name string `json:"name,omitempty"`

	// Description is the prose every shape of the family opens its own
	// description with ("General-purpose, CPU-only").
	Description string `json:"description,omitempty"`

	// Category is the workload category the family's shapes report.
	Category Category `json:"category"`

	// Architecture is the family's CPU architecture, used for a shape Compute
	// Engine reported without one.
	Architecture Architecture `json:"architecture"`

	// CPUType describes the processor ("Intel Emerald Rapids"). Compute
	// Engine's machine-type resource does not carry it.
	CPUType string `json:"cpu_type,omitempty"`

	// BootDiskType is the GCE disk type a host of this family must boot from,
	// empty when the zonal default will do. C4 and N4 reject pd-* outright and
	// the machine-type resource does not say so, which is why it is curation.
	BootDiskType string `json:"boot_disk_type,omitempty"`

	// GPUMode is how the family exposes GPUs when discovery finds no
	// accelerator on the shape itself: GPUModeAttachable for N1, else
	// GPUModeNone. An integrated GPU is discovered, never curated.
	GPUMode string `json:"gpu_mode,omitempty"`

	// The attachable-GPU policy: what a GPU host of this family gets. Only
	// meaningful under GPUModeAttachable.
	MaxGPUs         int64  `json:"max_gpus,omitempty"`
	DefaultGPUType  string `json:"default_gpu_type,omitempty"`
	DefaultGPUCount int64  `json:"default_gpu_count,omitempty"`

	// IsEnabled admits the family: discovery fetches its shapes and the
	// catalog offers them.
	IsEnabled bool `json:"is_enabled"`

	// SortOrder is where the family's shapes sit in the catalog's display
	// order.
	SortOrder int64 `json:"sort_order"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DisplayName renders the family's name, the operator's when they wrote one.
func (f *FamilyCuration) DisplayName() string {
	if f.Name != "" {
		return f.Name
	}
	return FamilyDisplayName(f.Family, f.Category)
}

// CheckGPUPolicy refuses a family whose default GPU count exceeds its
// ceiling. Placement takes the default as a floor, so a stored count above
// max_gpus would build hosts with more GPUs than the operator allowed.
func (f *FamilyCuration) CheckGPUPolicy() error {
	if f.DefaultGPUCount > f.MaxGPUs {
		return &GPUPolicyError{Family: f.Family, DefaultGPUCount: f.DefaultGPUCount, MaxGPUs: f.MaxGPUs}
	}
	return nil
}

// GPUPolicyError reports a family curation whose default GPU count exceeds
// its max_gpus, counting both the patch and the stored row.
type GPUPolicyError struct {
	Family          string
	DefaultGPUCount int64
	MaxGPUs         int64
}

func (e *GPUPolicyError) Error() string {
	return fmt.Sprintf("default_gpu_count %d exceeds max_gpus %d on family %s",
		e.DefaultGPUCount, e.MaxGPUs, e.Family)
}

// FamilyUpdate holds the curation fields an operator may change on a family;
// nil fields are left untouched. A family the table does not hold yet is
// created by the first update that enables it, so admitting a series Google
// shipped this morning is one call and no schema work.
type FamilyUpdate struct {
	Name            *string
	Description     *string
	Category        *Category
	Architecture    *Architecture
	CPUType         *string
	BootDiskType    *string
	GPUMode         *string
	MaxGPUs         *int64
	DefaultGPUType  *string
	DefaultGPUCount *int64
	IsEnabled       *bool
	SortOrder       *int64
}

// ShapeOverride is the per-shape curation an operator has applied on top of a
// family's. Every field is optional: an absent one takes the family's answer.
//
// It exists for the exclusions and the single election a family-level row
// cannot express (this shape is not offered, this shape is the default) and
// for a per-shape capacity. Display order and GPU policy come from the family
// alone. It is never where a shape's size or existence comes from, so no row
// here is ever required for a shape to be offered.
type ShapeOverride struct {
	// MachineType is the shape the override applies to.
	MachineType string

	// IsEnabled excludes (or re-admits) one shape of an enabled family.
	IsEnabled *bool

	// IsDefault elects the catalog default. Exactly one override row carries
	// it; the repository clears the previous one in the same commit.
	IsDefault bool

	// MaxDevices overrides the computed guest fit for this one shape: the
	// operator's word where the CPU-and-memory arithmetic is wrong for a
	// workload. Nil is the computed fit; a value is taken as given, floor and
	// ceiling included, because an override exists to disagree with the model.
	MaxDevices *int64

	CreatedAt time.Time
	UpdatedAt time.Time
}
