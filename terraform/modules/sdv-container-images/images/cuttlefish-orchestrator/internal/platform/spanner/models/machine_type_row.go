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

package models

import (
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// MachineFamilyRow represents the Spanner table schema for machine_families,
// the whole of the operator's machine curation. A row admits a series; every
// shape in it arrives from Compute Engine, sized by Compute Engine.
type MachineFamilyRow struct {
	Family          string             `spanner:"family"`
	Name            spanner.NullString `spanner:"name"`
	Description     spanner.NullString `spanner:"description"`
	Category        string             `spanner:"category"`
	Architecture    string             `spanner:"architecture"`
	CPUType         spanner.NullString `spanner:"cpu_type"`
	BootDiskType    spanner.NullString `spanner:"boot_disk_type"`
	GPUMode         string             `spanner:"gpu_mode"`
	MaxGPUs         spanner.NullInt64  `spanner:"max_gpus"`
	DefaultGPUType  spanner.NullString `spanner:"default_gpu_type"`
	DefaultGPUCount spanner.NullInt64  `spanner:"default_gpu_count"`
	IsEnabled       bool               `spanner:"is_enabled"`
	SortOrder       int64              `spanner:"sort_order"`
	CreatedAt       time.Time          `spanner:"created_at"`
	UpdatedAt       time.Time          `spanner:"updated_at"`
}

// ToDomain converts a MachineFamilyRow to a domain FamilyCuration.
func (r *MachineFamilyRow) ToDomain() *machinetype.FamilyCuration {
	return &machinetype.FamilyCuration{
		Family:          r.Family,
		Name:            r.Name.StringVal,
		Description:     r.Description.StringVal,
		Category:        machinetype.Category(r.Category),
		Architecture:    machinetype.Architecture(r.Architecture),
		CPUType:         r.CPUType.StringVal,
		BootDiskType:    r.BootDiskType.StringVal,
		GPUMode:         r.GPUMode,
		MaxGPUs:         r.MaxGPUs.Int64,
		DefaultGPUType:  r.DefaultGPUType.StringVal,
		DefaultGPUCount: r.DefaultGPUCount.Int64,
		IsEnabled:       r.IsEnabled,
		SortOrder:       r.SortOrder,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
}

// MachineTypeOverrideRow represents the Spanner table schema for
// machine_type_overrides: the per-shape curation laid over a family's.
//
// Every column but the key and is_default is nullable, and that is the
// contract: NULL means "the family answers", not zero. A row exists only
// where an operator excluded a shape, elected the default, or stored a
// capacity, so no shape ever needs one to be offered.
type MachineTypeOverrideRow struct {
	MachineType string            `spanner:"machine_type"`
	IsEnabled   spanner.NullBool  `spanner:"is_enabled"`
	IsDefault   bool              `spanner:"is_default"`
	MaxDevices  spanner.NullInt64 `spanner:"max_devices"`
	CreatedAt   time.Time         `spanner:"created_at"`
	UpdatedAt   time.Time         `spanner:"updated_at"`
}

// ToDomain converts a MachineTypeOverrideRow to a domain ShapeOverride. A NULL
// column arrives as a nil pointer, which is what keeps "unset" from reading as
// the zero value on the way through.
func (r *MachineTypeOverrideRow) ToDomain() *machinetype.ShapeOverride {
	override := &machinetype.ShapeOverride{
		MachineType: r.MachineType,
		IsDefault:   r.IsDefault,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
	if r.IsEnabled.Valid {
		override.IsEnabled = &r.IsEnabled.Bool
	}
	if r.MaxDevices.Valid {
		override.MaxDevices = &r.MaxDevices.Int64
	}
	return override
}
