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

package machinetypefeature

import (
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// ListMachineTypesInput is the input for listing machine types. There is no
// pagination: the composed catalog is one small in-memory slice, no client
// pages it, and a default window once silently hid whole families from the
// console.
type ListMachineTypesInput struct {
	Family       string `query:"family" doc:"Filter by machine family (e.g., n1, c4, g2)"`
	Category     string `query:"category" enum:"general_purpose,compute_optimized,memory_optimized,accelerator_optimized,storage_optimized,cost_optimized" doc:"Filter by workload category"`
	Architecture string `query:"architecture" enum:"x86,arm" doc:"Filter by CPU architecture"`
	Zone         string `query:"zone" doc:"Filter to machine types available in a zone (types with no zone list count as available everywhere)"`
	// Tri-state filters: Huma rejects pointer query params at Register time,
	// so absence is modeled as the empty string and converted to *bool in the
	// handler (see triStateBool).
	GPUSupport string `query:"gpu_support" enum:"true,false," doc:"Filter by GPU support (true, false, or omit for all)"`
	Enabled    string `query:"enabled" enum:"true,false," doc:"Filter by enabled status (true, false, or omit for all)"`
}

// ListMachineTypesOutput is the output for listing machine types: every entry
// the filter admits.
type ListMachineTypesOutput struct {
	Body struct {
		Items []*machinetype.MachineType `json:"items" doc:"List of available machine types"`
	}
}

// GetMachineTypeInput is the input for getting a machine type.
type GetMachineTypeInput struct {
	ID string `path:"id" doc:"Machine type ID (e.g., n1-standard-8)"`
}

// GetMachineTypeOutput is the output for getting a machine type.
type GetMachineTypeOutput struct {
	Body *machinetype.MachineType
}

// GetDefaultMachineTypeInput is the input for getting the default machine type.
type GetDefaultMachineTypeInput struct{}

// GetDefaultMachineTypeOutput is the output for getting the default machine type.
type GetDefaultMachineTypeOutput struct {
	Body *machinetype.MachineType
}

// ListMachineFamiliesInput is the input for listing unique machine families.
type ListMachineFamiliesInput struct{}

// ListMachineFamiliesOutput is the output for listing unique machine families.
type ListMachineFamiliesOutput struct {
	Body struct {
		Families []FamilySummary `json:"families" doc:"List of available machine families"`
	}
}

// FamilySummary provides a summary of a machine type family.
type FamilySummary struct {
	Family       string `json:"family" doc:"Family identifier (e.g., n1, c4)"`
	Name         string `json:"name" doc:"Display name"`
	Category     string `json:"category" doc:"Workload category"`
	Architecture string `json:"architecture" doc:"CPU architecture"`
	GPUSupport   bool   `json:"gpu_support" doc:"Whether this family supports GPUs"`
	TypeCount    int    `json:"type_count" doc:"Number of machine types in this family"`
}

// ListMachineFamiliesAdminInput is the input for the curation family listing.
type ListMachineFamiliesAdminInput struct{}

// ListMachineFamiliesAdminOutput is the curation view of the machine families.
type ListMachineFamiliesAdminOutput struct {
	Body struct {
		Families []AdminFamily `json:"families" doc:"Every curated machine family, in display order"`
	}
}

// AdminFamily is one machine family as the curation table renders it: the
// operator's row, and what Compute Engine offers under it.
//
// There is no custom-shape field. Whether a family builds a custom machine is
// Compute Engine's answer about one specific name, and GET /v2/machine-types/{id}
// asks exactly that. A per-family flag would be a staler copy of an answer the
// shape builder already gets exactly, and keeping it current would cost a probe
// call per family on every read of this list.
type AdminFamily struct {
	Family          string        `json:"family" doc:"Series identifier (e.g., n4, c4a)"`
	Name            string        `json:"name" doc:"Display name, the operator's when they wrote one"`
	Description     string        `json:"description,omitempty" doc:"Prose every shape of the family opens its description with"`
	Category        string        `json:"category" doc:"Workload category"`
	Architecture    string        `json:"architecture" doc:"CPU architecture"`
	CPUType         string        `json:"cpu_type,omitempty" doc:"Processor description"`
	BootDiskType    string        `json:"boot_disk_type,omitempty" doc:"Boot disk type hosts of this family must use; empty takes the zonal default"`
	GPUMode         string        `json:"gpu_mode" doc:"Whether GPUs attach to this family"`
	MaxGPUs         int64         `json:"max_gpus,omitempty" doc:"How many GPUs may be attached to a host of this family"`
	DefaultGPUType  string        `json:"default_gpu_type,omitempty" doc:"GPU attached when a caller asks for one without naming a type"`
	DefaultGPUCount int64         `json:"default_gpu_count,omitempty" doc:"GPUs attached by default"`
	IsEnabled       bool          `json:"is_enabled" doc:"Whether the family's shapes are offered for new hosts"`
	SortOrder       int64         `json:"sort_order" doc:"Where the family's shapes sit in the catalog's display order"`
	ShapeCount      int           `json:"shape_count" doc:"Shapes Compute Engine reports for this family"`
	ViableCount     int           `json:"viable_count" doc:"Of those, the shapes large enough to host an Android VM"`
	Shapes          []FamilyShape `json:"shapes" doc:"Every discovered shape, narrowest first"`
}

// FamilyShape is one discovered machine shape in the curation view. Nothing
// here is editable: discovery owns what a family holds, and the only per-shape
// curation is the exclusion flag beside it.
type FamilyShape struct {
	MachineType string `json:"machine_type" doc:"Machine type ID (e.g., n4-standard-8)"`
	VCPUs       int64  `json:"vcpus" doc:"Virtual CPUs"`
	MemoryGB    int64  `json:"memory_gb" doc:"Memory in gigabytes"`
	Fit         int64  `json:"fit" doc:"Default-shaped Android VMs a host of this shape holds"`
	Viable      bool   `json:"viable" doc:"Whether the shape can host Cuttlefish at all"`
	IsExcluded  bool   `json:"is_excluded" doc:"Whether an override withholds this shape from an admitted family"`
	IsDefault   bool   `json:"is_default" doc:"Whether this shape is the catalog default"`

	// The operator's stored capacity for this one shape, beside the computed
	// fit rather than replacing it: the view has to show both the arithmetic
	// and the word that outranks it.
	MaxDevicesOverride int64 `json:"max_devices_override,omitempty" doc:"Per-shape capacity override; absent when the computed fit rules"`
}

// UpdateMachineTypeAdminInput is the input for the admin curation PATCH.
// Pointer body fields distinguish "absent" from the zero value (PATCH
// tri-state), mirroring the hostconfig Enabled pattern. Display order and GPU
// policy are curated on the shape's family.
type UpdateMachineTypeAdminInput struct {
	ID   string `path:"id" doc:"Machine type ID (e.g., n1-standard-8)"`
	Body struct {
		IsEnabled *bool `json:"is_enabled,omitempty" doc:"Whether the machine type is offered for new hosts"`
		IsDefault *bool `json:"is_default,omitempty" doc:"Set true to make this the default machine type (clears the previous default); false is rejected, because the default moves and is never cleared"`
		// The per-shape capacity override: the operator's word where the
		// cores-and-memory arithmetic is wrong for a workload. Zero clears it
		// back to the computed fit.
		MaxDevices *int64 `json:"max_devices,omitempty" minimum:"0" maximum:"32" doc:"Device slots a host of this shape carries, overriding the computed fit; 0 restores the arithmetic"`
	}
}

// UpdateMachineTypeAdminOutput returns the updated machine type.
type UpdateMachineTypeAdminOutput struct {
	Body *machinetype.MachineType
}

// UpdateMachineFamilyAdminInput is the input for the family curation PATCH:
// the one act that admits a machine series and everything Compute Engine's
// machine-type resource does not state about it. Pointer body fields
// distinguish "absent" from the zero value.
type UpdateMachineFamilyAdminInput struct {
	Family string `path:"family" maxLength:"32" pattern:"^[a-z][a-z0-9]*$" doc:"Machine family (e.g., n4, c4a, g2)"`
	Body   struct {
		IsEnabled    *bool   `json:"is_enabled,omitempty" doc:"Whether the family's shapes are offered for new hosts"`
		Name         *string `json:"name,omitempty" maxLength:"255" doc:"Display name; empty derives one from the series and its category"`
		Description  *string `json:"description,omitempty" maxLength:"1000" doc:"Prose every shape of the family opens its own description with"`
		Category     *string `json:"category,omitempty" enum:"general_purpose,compute_optimized,memory_optimized,accelerator_optimized,storage_optimized,cost_optimized" doc:"Workload category the family's shapes report"`
		Architecture *string `json:"architecture,omitempty" enum:"x86,arm" doc:"CPU architecture, used only for a shape Compute Engine reported without one"`
		CPUType      *string `json:"cpu_type,omitempty" maxLength:"255" doc:"Processor description, e.g. Intel Emerald Rapids"`
		// The two facts a create fails on when they are wrong and Compute
		// Engine will not state: which boot disk the family accepts, and
		// whether it takes attached accelerators.
		BootDiskType *string `json:"boot_disk_type,omitempty" maxLength:"50" doc:"Boot disk type a host of this family must use; empty takes the zonal default"`
		GPUMode      *string `json:"gpu_mode,omitempty" enum:"none,attachable" doc:"Whether GPUs attach to this family; an integrated GPU is discovered, never curated"`

		MaxGPUs         *int64  `json:"max_gpus,omitempty" minimum:"1" maximum:"4" doc:"How many GPUs may be attached to a host of this family"`
		DefaultGPUType  *string `json:"default_gpu_type,omitempty" maxLength:"64" pattern:"^[a-z0-9-]*$" doc:"GPU attached when a caller asks for one without naming a type"`
		DefaultGPUCount *int64  `json:"default_gpu_count,omitempty" minimum:"0" doc:"GPUs attached by default (0 = none unless the caller asks)"`
		SortOrder       *int64  `json:"sort_order,omitempty" minimum:"0" doc:"Where the family's shapes sit in the catalog's display order"`
	}
}

// UpdateMachineFamilyAdminOutput returns the updated family curation.
type UpdateMachineFamilyAdminOutput struct {
	Body *machinetype.FamilyCuration
}
