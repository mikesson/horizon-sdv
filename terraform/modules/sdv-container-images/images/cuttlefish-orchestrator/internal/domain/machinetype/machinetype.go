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
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound is returned when a machine type is not in the catalog.
var ErrNotFound = errors.New("machine type not found")

// UnofferedReason names why the catalog does not offer a machine type.
type UnofferedReason string

const (
	// ReasonUnknown is a name no curated machine family reports a shape by, and
	// that Compute Engine builds no machine of either.
	ReasonUnknown UnofferedReason = "unknown"

	// ReasonFamilyNotCurated is a shape whose machine family carries no
	// curation row, so nothing under it is discovered or offered.
	ReasonFamilyNotCurated UnofferedReason = "family_not_curated"

	// ReasonBelowFloor is a shape Compute Engine offers under a curated family
	// whose cores and memory hold no Android VM, with no per-shape capacity
	// override saying otherwise.
	ReasonBelowFloor UnofferedReason = "below_floor"
)

// UnofferedError is a machine type the catalog does not offer, and the reason
// it does not.
//
// It exists because "not found" was answering three different questions with
// one sentence, and the remedies differ: a name nobody can fix, a family to
// enable, and a machine the arithmetic refuses that a per-shape capacity
// override admits. Every caller that looks a machine type up (a ladder write,
// a create naming a shape by hand, the console's 404) gets the same account.
//
// It matches ErrNotFound, so the HTTP mapping and every errors.Is on it stand.
type UnofferedError struct {
	MachineType string
	Reason      UnofferedReason

	// Family is the series the name belongs to, stated where the reason is
	// about the family.
	Family string

	// VCPUs and MemoryGB are the size Compute Engine stated, carried so the
	// below-floor sentence can name the arithmetic that refused it.
	VCPUs    int64
	MemoryGB int64
}

func (e *UnofferedError) Error() string {
	switch e.Reason {
	case ReasonFamilyNotCurated:
		return fmt.Sprintf(
			"%q is not a machine type the catalog offers: nothing admits its %s machine family; enable its machine family first",
			e.MachineType, e.Family)
	case ReasonBelowFloor:
		return fmt.Sprintf(
			"%q is not a machine type the catalog offers: %d vCPUs and %d GB hold no Android VM at all; "+
				"set a per-shape capacity override on it to admit it anyway",
			e.MachineType, e.VCPUs, e.MemoryGB)
	default:
		return fmt.Sprintf(
			"%q is not a machine type the catalog offers: no curated machine family reports it, "+
				"and Compute Engine builds no machine of that name",
			e.MachineType)
	}
}

// Is makes any *UnofferedError match the ErrNotFound sentinel via errors.Is,
// which is what keeps the status mapping and the existing callers unchanged
// while the message gains its reason.
func (e *UnofferedError) Is(target error) bool {
	return target == ErrNotFound
}

// ErrFamilyNotFound is returned when a machine family has no curation row.
var ErrFamilyNotFound = errors.New("machine family not curated")

// ErrCatalogUnavailable is returned when no shape discovery has landed yet, so
// the catalog has nothing to compose its entries from.
//
// It exists to keep two situations apart that would otherwise be one empty
// answer: a fleet whose operator has enabled nothing, and an instance that has
// not yet read what Compute Engine offers. Only the first is a fact about the
// deployment; reporting the second as "this machine type is not supported"
// would blame the operator's catalog for a cold start.
var ErrCatalogUnavailable = errors.New("the machine-type catalog has not been read from Compute Engine yet")

// Category represents the workload category of a machine type.
type Category string

const (
	// CategoryGeneralPurpose is for general-purpose workloads.
	CategoryGeneralPurpose Category = "general_purpose"
	// CategoryComputeOptimized is for compute-intensive workloads.
	CategoryComputeOptimized Category = "compute_optimized"
	// CategoryMemoryOptimized is for memory-intensive workloads.
	CategoryMemoryOptimized Category = "memory_optimized"
	// CategoryAcceleratorOptimized is for GPU/TPU workloads.
	CategoryAcceleratorOptimized Category = "accelerator_optimized"
	// CategoryStorageOptimized is for storage-intensive workloads.
	CategoryStorageOptimized Category = "storage_optimized"
	// CategoryCostOptimized is for cost-sensitive workloads.
	CategoryCostOptimized Category = "cost_optimized"
)

// Categories returns every workload category, in the order the list endpoint's
// enum tag declares them. It exists so that tag and this vocabulary can be
// compared: a struct tag cannot reference a constant, so the accepted set is
// written down twice and nothing but a test stops the copies diverging.
func Categories() []Category {
	return []Category{
		CategoryGeneralPurpose, CategoryComputeOptimized, CategoryMemoryOptimized,
		CategoryAcceleratorOptimized, CategoryStorageOptimized, CategoryCostOptimized,
	}
}

// Architecture represents the CPU architecture.
type Architecture string

const (
	// ArchX86 is the x86/x86_64 architecture.
	ArchX86 Architecture = "x86"
	// ArchArm is the ARM architecture.
	ArchArm Architecture = "arm"
)

// Architectures returns every CPU architecture, in the order the list
// endpoint's enum tag declares them. Same reason as Categories.
func Architectures() []Architecture {
	return []Architecture{ArchX86, ArchArm}
}

// GPU modes describe how a machine family exposes GPUs, which determines
// whether the GCE instance sets guestAccelerators. See
// https://cloud.google.com/compute/docs/gpus (N1 attaches; A/G-series are
// accelerator-optimized with the GPU built into the machine type).
const (
	// GPUModeNone: the machine type has no GPU support (e.g., E2, C4A).
	GPUModeNone = "none"
	// GPUModeAttachable: GPUs are attached via guestAccelerators (N1 only);
	// the count is caller-controlled and may be zero (CPU-only).
	GPUModeAttachable = "attachable"
	// GPUModeIntegrated: the GPU is part of the machine type (G2/G4/A-series);
	// guestAccelerators must NOT be set, because GCE rejects it.
	GPUModeIntegrated = "integrated"
)

// MachineType represents a GCP Compute Engine machine type.
type MachineType struct {
	// ID is the unique identifier (e.g., "n1-standard-8").
	ID string `json:"id"`

	// Family is the machine family (e.g., "n1", "c4", "g2").
	Family string `json:"family"`

	// Name is the human-readable display name.
	Name string `json:"name"`

	// Description provides additional details about the machine type.
	Description string `json:"description,omitempty"`

	// Category is the workload category.
	Category Category `json:"category"`

	// Architecture is the CPU architecture (x86 or arm).
	Architecture Architecture `json:"architecture"`

	// CPUType describes the CPU (e.g., "Intel Skylake", "AMD EPYC").
	CPUType string `json:"cpu_type,omitempty"`

	// VCPUs is the number of virtual CPUs.
	VCPUs int64 `json:"vcpus"`

	// MemoryGB is the memory in gigabytes.
	MemoryGB int64 `json:"memory_gb"`

	// Fit is how many default-shaped Android VMs a host of this shape holds
	// once the host keeps back its own reserve. It is the packing rule's own
	// arithmetic, so what the catalog shows and what placement packs to cannot
	// disagree. Zero for a shape whose size Compute Engine did not state.
	Fit int64 `json:"fit"`

	// FitOverridden marks a fit that is an operator's word rather than the
	// computed arithmetic, so a curation surface can show which it is editing.
	FitOverridden bool `json:"fit_overridden,omitempty"`

	// GPUSupport indicates if this machine type supports GPUs.
	GPUSupport bool `json:"gpu_support"`

	// MaxGPUs is the maximum number of GPUs that can be attached.
	MaxGPUs int64 `json:"max_gpus,omitempty"`

	// DefaultGPUType is the recommended GPU type for this machine.
	DefaultGPUType string `json:"default_gpu_type,omitempty"`

	// DefaultGPUCount is the default number of GPUs.
	DefaultGPUCount int64 `json:"default_gpu_count,omitempty"`

	// BootDiskType is the GCE disk type a host of this shape must boot from,
	// empty when the zonal default will do. Newer families (C4, N4) reject
	// pd-* outright and only take hyperdisk-balanced, and GCE's machine-type
	// resource does not say so, which is why it is catalog knowledge rather
	// than something the provisioner can work out.
	BootDiskType string `json:"boot_disk_type,omitempty"`

	// GPUMode indicates how GPUs work with this machine type. See the GPUMode*
	// constants: N1 attaches GPUs via guestAccelerators; G2/G4/A-series have the
	// GPU built into the machine type (guestAccelerators must NOT be set).
	GPUMode string `json:"gpu_mode,omitempty"`

	// IntegratedGPUType is the GPU type for machines with integrated GPUs (gpu_mode=integrated).
	IntegratedGPUType string `json:"integrated_gpu_type,omitempty"`

	// IntegratedGPUCount is the number of integrated GPUs.
	IntegratedGPUCount int64 `json:"integrated_gpu_count,omitempty"`

	// IsDefault indicates if this is the default machine type for new hosts.
	IsDefault bool `json:"is_default"`

	// IsEnabled indicates if this machine type is available for use.
	IsEnabled bool `json:"is_enabled"`

	// SortOrder determines display order in the UI.
	SortOrder int64 `json:"sort_order"`

	// Zones lists the zones where this machine type is available.
	// If empty, it's assumed to be available in all zones.
	Zones []string `json:"zones,omitempty"`

	// CreatedAt is when this record was created.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when this record was last updated.
	UpdatedAt time.Time `json:"updated_at"`
}

// IsBareMetal reports whether this is a bare-metal shape (GCE's "-metal"
// naming convention). Bare-metal shapes are first-class catalog entries:
// Cuttlefish on C4A/Axion requires the bare-metal variant, the only C4A form
// that supports it. GCE never offers them as Spot VMs, so Spot-only
// machinery (capacity-advice probes) must skip them.
func (m *MachineType) IsBareMetal() bool {
	return IsBareMetalID(m.ID)
}

// IsBareMetalID reports whether a machine type id names a bare-metal shape,
// for callers that hold the id rather than the catalog entry.
func IsBareMetalID(id string) bool {
	return strings.HasSuffix(id, "-metal")
}

// ListFilter defines filtering options for listing machine types.
type ListFilter struct {
	// Family filters by machine family (e.g., "n1").
	Family *string

	// Category filters by workload category.
	Category *Category

	// Architecture filters by CPU architecture.
	Architecture *Architecture

	// GPUSupport filters to only GPU-capable machines.
	GPUSupport *bool

	// IsEnabled filters by enabled status.
	IsEnabled *bool

	// Zone filters to machines available in a specific zone.
	Zone *string

	// Limit is the maximum number of results. Zero means unbounded, which is
	// what every serving path wants: the composed catalog is one small
	// in-memory slice, and a window over it once hid whole families from the
	// console. The one caller that sets it is the spot-advice prober, bounding
	// its own probe fan-out.
	Limit int
}

// Family summarizes the enabled machine types of one machine family.
type Family struct {
	// Family is the family identifier (e.g., "n1", "c4").
	Family string `json:"family"`

	// Name is the family's display name, the operator's when they wrote one.
	Name string `json:"name"`

	// Category is the workload category of the family's machine types.
	Category Category `json:"category"`

	// Architecture is the CPU architecture of the family's machine types.
	Architecture Architecture `json:"architecture"`

	// GPUSupport is true when any machine type in the family supports GPUs.
	GPUSupport bool `json:"gpu_support"`

	// TypeCount is the number of enabled machine types in the family.
	TypeCount int64 `json:"type_count"`
}

// MaxAttachableGPUs is the ceiling on GPUs attached to a single host of an
// attachable (N1) shape. The create-time gpu_count bound and the catalog's
// editable max_gpus bound both mirror it; a catalog promising more than a
// create can request would be a promise the API refuses to keep.
const MaxAttachableGPUs = 4

// AdminUpdate holds the curation fields an operator may change on one catalog
// entry; nil fields are left untouched. Display order and GPU policy belong to
// the shape's family.
type AdminUpdate struct {
	IsEnabled *bool
	IsDefault *bool

	// MaxDevices overrides the shape's computed guest fit; zero clears the
	// override back to the arithmetic.
	MaxDevices *int64
}

// attachableVCPUCeiling is how many vCPUs ONE GPU of a type supports on the
// families that attach it. It is Compute Engine's published constraint, which
// its machine-type resource does not state. T4 on N1: one card to 24 vCPUs,
// two to 48, four to 96, counts of three never offered.
var attachableVCPUCeiling = map[string]int64{
	"nvidia-tesla-t4": 24,
}

// AttachableGPUCount is how many GPUs of a type a shape of this width must
// attach: the smallest offered count (powers of two up to maxGPUs) whose
// ceiling covers the shape's vCPUs. ok is false when no legal count can: a
// shape wider than the type's maximum reach cannot be built with this GPU at
// all, and admitting it anyway would queue an insert Compute Engine refuses
// every time.
//
// A type this table does not know keeps the curated default floored at one:
// the platform cannot verify what it has no constraint for, and Compute Engine
// remains the authority at insert.
func AttachableGPUCount(gpuType string, vcpus, maxGPUs, defaultCount int64) (int64, bool) {
	floor := max(defaultCount, 1)
	ceiling := attachableVCPUCeiling[gpuType]
	if ceiling <= 0 || vcpus <= 0 {
		return floor, true
	}
	for count := int64(1); count <= max(maxGPUs, 1); count *= 2 {
		if vcpus <= count*ceiling {
			// The operator's curated count is a floor, never below what the
			// width requires: a family curated to two cards still gets four on
			// a shape only four can serve.
			return max(count, floor), true
		}
	}
	return 0, false
}

// Catalog is the read slice consumers use to choose provisioning shapes:
// the device slice's declarative resolver, the host service's shape
// defaulting, and the gcecatalog validator/ranker. The machine-type
// repository satisfies it.
type Catalog interface {
	// Get retrieves a machine type by ID.
	Get(ctx context.Context, id string) (*MachineType, error)

	// List retrieves machine types with filtering.
	List(ctx context.Context, filter ListFilter) ([]*MachineType, error)

	// GetDefault returns the default machine type.
	GetDefault(ctx context.Context) (*MachineType, error)
}

// Repository defines the persistence operations for machine types.
type Repository interface {
	Catalog

	// UpdateAdminSettings applies curation changes to a machine type and
	// returns the updated entry. Setting is_default=true clears the previous
	// default in the same transaction (single-default invariant).
	UpdateAdminSettings(ctx context.Context, id string, update AdminUpdate) (*MachineType, error)

	// ListFamilies aggregates the enabled machine types by family.
	ListFamilies(ctx context.Context) ([]*Family, error)

	// ListFamilyCatalog answers the view curation is done from: every curated
	// family with each shape Compute Engine offers under it, including the
	// shapes the served catalog withholds.
	ListFamilyCatalog(ctx context.Context) ([]*FamilyCatalog, error)
}
