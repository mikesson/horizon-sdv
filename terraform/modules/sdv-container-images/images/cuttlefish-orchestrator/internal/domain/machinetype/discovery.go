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
	"fmt"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// Shape is one machine type as Compute Engine describes it: the facts only
// Google can state, read from the aggregated machine-type list.
//
// Nothing here is ever written by hand. A shape exists because Compute Engine
// reported it (or, for a custom shape, because its name says what it is), and
// its size is whatever Compute Engine said, which is what makes a family the
// unit of curation and the shape the unit of discovery.
type Shape struct {
	// Name is the machine type id ("n4-standard-48").
	Name string

	// Family is the series the shape belongs to.
	Family string

	// VCPUs is guestCpus.
	VCPUs int64

	// MemoryGB is memoryMb rendered in the catalog's unit. Truncating is
	// chosen: it under-reports a shape by at most a gigabyte, and every
	// sizing rule that reads it treats more memory as more permissive.
	MemoryGB int64

	// Architecture is what Compute Engine said, empty when it said nothing.
	// The family's curated value answers then.
	Architecture Architecture

	// GPUType and GPUCount are the accelerator built into the machine type,
	// from the shape's own accelerators list. A shape with one has an
	// integrated GPU by construction: guestAccelerators must not be set for it.
	GPUType  string
	GPUCount int64
}

// Discovery is what Compute Engine offers for the curated families, read from
// the in-process snapshot the platform keeps refreshed.
//
// Facts about Google are held by Google. The catalog composes its rows from
// this every time it is read rather than mirroring it into Spanner, so a shape
// Compute Engine retires stops being offered on the next refresh and nothing
// has to remember to delete it.
type Discovery interface {
	// DiscoveredShapes answers every shape Compute Engine reported for the
	// curated families, keyed by machine type id, and whether a discovery has
	// landed at all.
	//
	// The second result is the whole difference between an empty catalog and
	// an unread one. Before the first refresh lands there is nothing to
	// compose from, and answering "no machine types" there would tell a caller
	// that the fleet has no shapes when what is true is that this instance has
	// not looked yet.
	DiscoveredShapes(ctx context.Context) (map[string]Shape, bool)

	// ResolveShape asks Compute Engine to describe one machine type by name,
	// for the shapes the aggregated list does not carry.
	//
	// This is how a custom shape gets its size: Compute Engine synthesizes the
	// resource for a legal name and refuses an illegal one with the rule it
	// broke, so the same call answers "may this machine exist" and "how big is
	// it" without CCO holding an opinion about either. ErrNotFound for a name
	// no family will build.
	ResolveShape(ctx context.Context, machineType string) (Shape, error)
}

// admissible is the one admission rule, asked of a machine's EFFECTIVE
// capacity: the operator's stored per-shape capacity where one exists, and the
// packing arithmetic otherwise.
//
// A host that can hold no guest is a host no device can ever be placed on, so
// offering it would put a machine in the catalog whose every create fails on
// packing. The arithmetic is the packing rule's own, asked at its smallest
// interesting point, so a shape this admits and packing then refuses is
// unrepresentable.
//
// The operator's word outranks the arithmetic here as it does everywhere else
// capacity is read, which is what makes the per-shape override reach the gate
// that decides whether the shape is offered at all. A stored capacity is never
// below one (zero clears it) so an override always admits. A shape whose size
// Compute Engine did not state is admitted with no fit to show for it: no
// stated size states no bound.
func admissible(vcpus, memoryGB, capacity int64, overridden bool) bool {
	if overridden {
		return capacity >= 1
	}
	return device.HoldsOneGuest(vcpus, memoryGB)
}

// Admissible reports whether the catalog offers this composed entry.
//
// Judged on the composition rather than on the raw shape, so the entry a list
// serves, the entry a get returns and the entry a shape ladder is validated
// against are one composition and one admission.
func (m *MachineType) Admissible() bool {
	return admissible(m.VCPUs, m.MemoryGB, m.Fit, m.FitOverridden)
}

// GuestFit is how many default-shaped Android VMs a host of this shape holds
// once the host keeps back its own reserve.
//
// Zero for a shape whose size Compute Engine did not state, which is not the
// same reading Admissible gives that shape: no stated size is no stated bound,
// so the shape is admitted with no fit to show for it.
func (s Shape) GuestFit() int64 {
	fit, _ := device.GuestFitInstances(s.VCPUs, s.MemoryGB)
	return fit
}

// FamilyCatalog is one curated family with everything Compute Engine offers
// under it: the view curation is done from, where a shape too small to hold an
// Android VM is reported rather than dropped.
//
// The served catalog does drop those, because a machine no device can land on
// is a machine every create against it fails on. An operator deciding what to
// admit still has to see them, or a family's shape count reads as a discovery
// that lost rows.
type FamilyCatalog struct {
	// Curation is the family row the operator wrote.
	Curation *FamilyCuration

	// Shapes is every shape Compute Engine reported for the family.
	Shapes []FamilyShape

	// Viable is how many of those shapes the catalog offers.
	Viable int64
}

// FamilyShape is one discovered shape as the curation view reports it: the size
// Compute Engine stated, what it holds, and the per-shape curation on top.
type FamilyShape struct {
	MachineType string
	VCPUs       int64
	MemoryGB    int64

	// Fit is how many default-shaped guests the machine's cores and memory
	// hold, always the arithmetic and never the operator's word. The view
	// exists to show the two apart.
	Fit int64

	// Viable is the admission rule's own answer about the composed entry, so a
	// shape the catalog serves never reads as unviable here. It is not
	// Fit >= 1 in either direction: a shape that states no size states no bound
	// and is admitted with no fit, and a stored capacity admits a shape whose
	// arithmetic refused it.
	Viable bool

	// Excluded is the per-shape override withholding this one shape from a
	// family the operator otherwise admitted.
	Excluded bool

	// MaxDevicesOverride is the operator's per-shape capacity where one is
	// stored; nil is the computed fit. Served beside Fit rather than replacing
	// it, so the curation view can show both the arithmetic and the word that
	// outranks it.
	MaxDevicesOverride *int64

	// IsDefault marks the one shape elected as the catalog default.
	IsDefault bool
}

// FamilyShapeOf renders one discovered shape for the curation view.
func FamilyShapeOf(shape Shape, override *ShapeOverride) FamilyShape {
	entry := FamilyShape{
		MachineType: shape.Name,
		VCPUs:       shape.VCPUs,
		MemoryGB:    shape.MemoryGB,
		Fit:         shape.GuestFit(),
	}
	if override != nil {
		entry.Excluded = override.IsEnabled != nil && !*override.IsEnabled
		entry.IsDefault = override.IsDefault
		entry.MaxDevicesOverride = override.MaxDevices
	}
	capacity, overridden := entry.Fit, entry.MaxDevicesOverride != nil
	if overridden {
		capacity = *entry.MaxDevicesOverride
	}
	entry.Viable = admissible(shape.VCPUs, shape.MemoryGB, capacity, overridden)
	return entry
}

// Compose renders one catalog entry from the three things that each know a
// piece of it: what Compute Engine reported about the shape, the family
// curation the operator wrote, and the per-shape override on top.
//
// This is the only place a MachineType is built, so the entry a list serves,
// the entry an admin PATCH returns and the entry placement sizes a host from
// are the same composition by construction. Admission is asked of the result
// rather than of the shape it started from, for the same reason: a catalog that
// judged the raw shape would withhold machines the operator's own capacity
// admits.
func Compose(shape Shape, family *FamilyCuration, override *ShapeOverride) *MachineType {
	mt := &MachineType{
		ID:           shape.Name,
		Family:       shape.Family,
		Name:         DisplayName(shape.Name),
		Description:  composeDescription(family.Description, shape),
		Category:     family.Category,
		Architecture: shape.Architecture,
		CPUType:      family.CPUType,
		VCPUs:        shape.VCPUs,
		MemoryGB:     shape.MemoryGB,
		Fit:          shape.GuestFit(),
		BootDiskType: family.BootDiskType,
		IsEnabled:    family.IsEnabled,
		SortOrder:    family.SortOrder,
		CreatedAt:    family.CreatedAt,
		UpdatedAt:    family.UpdatedAt,
	}
	if mt.Architecture == "" {
		mt.Architecture = family.Architecture
	}
	composeGPU(mt, shape, family)
	applyOverride(mt, override)
	return mt
}

// composeDescription states the family's prose and the machine's size, which
// between them are everything the old hand-written descriptions said.
func composeDescription(familyDescription string, shape Shape) string {
	size := fmt.Sprintf("%d vCPUs, %d GB RAM", shape.VCPUs, shape.MemoryGB)
	if familyDescription == "" {
		return size
	}
	return familyDescription + ", " + size
}

// composeGPU decides the entry's GPU facts. An accelerator on the shape itself
// is an integrated GPU, part of the machine type with its count fixed by the
// hardware, and it outranks any family policy, which describes attachment.
func composeGPU(mt *MachineType, shape Shape, family *FamilyCuration) {
	if shape.GPUType != "" && shape.GPUCount > 0 {
		mt.GPUMode = GPUModeIntegrated
		mt.GPUSupport = true
		mt.IntegratedGPUType = shape.GPUType
		mt.IntegratedGPUCount = shape.GPUCount
		mt.MaxGPUs = shape.GPUCount
		return
	}
	if family.GPUMode != GPUModeAttachable {
		mt.GPUMode = GPUModeNone
		return
	}
	mt.GPUMode = GPUModeAttachable
	mt.GPUSupport = true
	mt.MaxGPUs = family.MaxGPUs
	mt.DefaultGPUType = family.DefaultGPUType
	mt.DefaultGPUCount = family.DefaultGPUCount
}

// applyOverride lays the per-shape curation over the family's answer: an
// exclusion, the default election and a capacity. Display order and GPU policy
// are the family's alone.
func applyOverride(mt *MachineType, override *ShapeOverride) {
	if override == nil {
		return
	}
	mt.IsDefault = override.IsDefault
	if override.IsEnabled != nil {
		// A disabled family disables every shape in it: an override may
		// exclude a shape from an admitted family, never smuggle one out of a
		// family the operator withdrew.
		mt.IsEnabled = mt.IsEnabled && *override.IsEnabled
	}
	if override.MaxDevices != nil {
		mt.Fit = *override.MaxDevices
		mt.FitOverridden = true
	}
	if override.UpdatedAt.After(mt.UpdatedAt) {
		mt.UpdatedAt = override.UpdatedAt
	}
	if mt.CreatedAt.IsZero() {
		mt.CreatedAt = override.CreatedAt
	}
}
