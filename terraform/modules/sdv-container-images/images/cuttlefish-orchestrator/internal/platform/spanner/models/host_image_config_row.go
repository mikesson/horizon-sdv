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

	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
)

// HostImageConfigRow represents the Spanner table schema for host_image_configs.
type HostImageConfigRow struct {
	MachineFamily string             `spanner:"machine_family"`
	ImageFamily   string             `spanner:"image_family"`
	SourceProject spanner.NullString `spanner:"source_project"`
	Description   spanner.NullString `spanner:"description"`
	Enabled       bool               `spanner:"enabled"`
	CreatedAt     time.Time          `spanner:"created_at"`
	UpdatedAt     time.Time          `spanner:"updated_at"`
	CreatedBy     spanner.NullString `spanner:"created_by"`
	UpdatedBy     spanner.NullString `spanner:"updated_by"`
}

// ToDomain converts a HostImageConfigRow to a domain HostImageConfig.
func (r *HostImageConfigRow) ToDomain() *hostconfig.HostImageConfig {
	config := &hostconfig.HostImageConfig{
		MachineFamily: r.MachineFamily,
		ImageFamily:   r.ImageFamily,
		Enabled:       r.Enabled,
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
	}

	if r.SourceProject.Valid {
		config.SourceProject = r.SourceProject.StringVal
	}
	if r.Description.Valid {
		config.Description = r.Description.StringVal
	}
	if r.CreatedBy.Valid {
		config.CreatedBy = r.CreatedBy.StringVal
	}
	if r.UpdatedBy.Valid {
		config.UpdatedBy = r.UpdatedBy.StringVal
	}

	return config
}

// FromHostImageConfigDomain converts a domain HostImageConfig to a HostImageConfigRow.
func FromHostImageConfigDomain(config *hostconfig.HostImageConfig) *HostImageConfigRow {
	r := &HostImageConfigRow{
		MachineFamily: config.MachineFamily,
		ImageFamily:   config.ImageFamily,
		Enabled:       config.Enabled,
		CreatedAt:     config.CreatedAt,
		UpdatedAt:     config.UpdatedAt,
	}

	r.SourceProject = ToNullString(config.SourceProject)
	r.Description = ToNullString(config.Description)
	r.CreatedBy = ToNullString(config.CreatedBy)
	r.UpdatedBy = ToNullString(config.UpdatedBy)

	return r
}

// HostImageConfigColumns returns the column names for the host_image_configs table.
func HostImageConfigColumns() []string {
	return []string{
		"machine_family",
		"image_family",
		"source_project",
		"description",
		"enabled",
		"created_at",
		"updated_at",
		"created_by",
		"updated_by",
	}
}
