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

package hostconfig

import (
	"context"
	"errors"
	"time"
)

// HostImageConfig represents the mapping between a machine family and its
// corresponding GCE image family used for host provisioning.
type HostImageConfig struct {
	// MachineFamily is the machine type prefix (e.g., "n1", "g2", "c4a").
	// This is the primary key.
	MachineFamily string `json:"machine_family"`

	// ImageFamily is the GCE image family name (e.g., "cco-host").
	ImageFamily string `json:"image_family"`

	// SourceProject is the GCP project containing the image family.
	// If empty, the deployment's CCO_HOST_IMAGE_PROJECT is used.
	SourceProject string `json:"source_project,omitempty"`

	// Description provides context about this configuration.
	Description string `json:"description,omitempty"`

	// Enabled indicates whether this configuration is active.
	// Disabled configurations will not be used for host provisioning.
	Enabled bool `json:"enabled"`

	// CreatedAt is when this configuration was created.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when this configuration was last modified.
	UpdatedAt time.Time `json:"updated_at"`

	// CreatedBy is the email/identifier of who created this config.
	CreatedBy string `json:"created_by,omitempty"`

	// UpdatedBy is the email/identifier of who last modified this config.
	UpdatedBy string `json:"updated_by,omitempty"`

	// Resolved is the image this row boots today, or why it boots nothing.
	// Output only: the admin reads fill it from the same resolution a host
	// create runs.
	Resolved *ImageStatus `json:"resolved,omitempty" doc:"The image this row resolves to now, or why it cannot boot a host"`
}

// ListFilter defines filtering options for listing host image configs.
type ListFilter struct {
	// EnabledOnly filters to only enabled configurations.
	EnabledOnly bool

	// Limit is the maximum number of results.
	Limit int64

	// Offset is the number of results to skip.
	Offset int64
}

// Repository defines the persistence operations for host image configurations.
type Repository interface {
	// Get retrieves a host image config by machine family.
	Get(ctx context.Context, machineFamily string) (*HostImageConfig, error)

	// List retrieves all host image configs with optional filtering.
	List(ctx context.Context, filter ListFilter) ([]*HostImageConfig, error)

	// Create creates a new host image config.
	Create(ctx context.Context, config *HostImageConfig) error

	// Update updates an existing host image config.
	Update(ctx context.Context, config *HostImageConfig) error

	// Delete removes a host image config by machine family.
	Delete(ctx context.Context, machineFamily string) error

	// Count returns the total number of configs matching the filter.
	Count(ctx context.Context, filter ListFilter) (int64, error)
}

// ErrSettingChanged is returned when a settings write carries an If-Match
// that no longer matches what is stored.
var ErrSettingChanged = errors.New("the setting changed since it was read; read it again and retry")

// NotFoundError is returned when a host image config is not found.
type NotFoundError struct {
	MachineFamily string
}

func (e *NotFoundError) Error() string {
	return "host image config not found for machine family: " + e.MachineFamily
}
