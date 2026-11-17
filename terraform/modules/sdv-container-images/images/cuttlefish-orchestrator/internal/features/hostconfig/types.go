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

package hostconfigfeature

import (
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
)

// ListHostImageConfigsInput is the input for listing host image configurations.
type ListHostImageConfigsInput struct {
	Limit       int  `query:"limit" minimum:"1" maximum:"200" default:"100" doc:"Maximum number of configs to return"`
	Offset      int  `query:"offset" minimum:"0" default:"0" doc:"Number of configs to skip"`
	EnabledOnly bool `query:"enabled_only" default:"false" doc:"Only return enabled configurations"`
}

// ListHostImageConfigsOutput is the output for listing host image configurations.
type ListHostImageConfigsOutput struct {
	Body struct {
		Items  []*hostconfig.HostImageConfig `json:"items" doc:"List of host image configurations"`
		Limit  int                           `json:"limit" doc:"Requested limit"`
		Offset int                           `json:"offset" doc:"Requested offset"`
		Total  int64                         `json:"total" doc:"Total number of configurations"`
	}
}

// GetHostImageConfigInput is the input for getting a host image configuration.
type GetHostImageConfigInput struct {
	MachineFamily string `path:"family" doc:"Machine family identifier (e.g., n1, g2, c4a)"`
}

// GetHostImageConfigOutput is the output for getting a host image configuration.
type GetHostImageConfigOutput struct {
	Body *hostconfig.HostImageConfig
}

// CreateHostImageConfigInput is the input for creating a host image configuration.
type CreateHostImageConfigInput struct {
	Body struct {
		MachineFamily string `json:"machine_family" minLength:"1" maxLength:"50" doc:"Machine family identifier (e.g., n1, g2, c4a)"`
		ImageFamily   string `json:"image_family" minLength:"1" maxLength:"100" doc:"GCE image family name (e.g., cco-host)"`
		SourceProject string `json:"source_project,omitempty" maxLength:"100" doc:"GCP project containing the image family; empty uses the deployment's CCO_HOST_IMAGE_PROJECT"`
		Description   string `json:"description,omitempty" maxLength:"500" doc:"Description of this configuration"`
		Enabled       *bool  `json:"enabled,omitempty" doc:"Whether this configuration is active (default: true)"`
	}
}

// CreateHostImageConfigOutput is the output for creating a host image configuration.
type CreateHostImageConfigOutput struct {
	Body *hostconfig.HostImageConfig
}

// UpdateHostImageConfigInput is the input for updating a host image configuration.
type UpdateHostImageConfigInput struct {
	MachineFamily string `path:"family" doc:"Machine family identifier to update"`
	Body          struct {
		ImageFamily   string `json:"image_family" minLength:"1" maxLength:"100" doc:"GCE image family name"`
		SourceProject string `json:"source_project,omitempty" maxLength:"100" doc:"GCP project containing the image family; empty uses the deployment's CCO_HOST_IMAGE_PROJECT"`
		Description   string `json:"description,omitempty" maxLength:"500" doc:"Description of this configuration"`
		Enabled       *bool  `json:"enabled,omitempty" doc:"Whether this configuration is active"`
	}
}

// UpdateHostImageConfigOutput is the output for updating a host image configuration.
type UpdateHostImageConfigOutput struct {
	Body *hostconfig.HostImageConfig
}

// DeleteHostImageConfigInput is the input for deleting a host image configuration.
type DeleteHostImageConfigInput struct {
	MachineFamily string `path:"family" doc:"Machine family identifier to delete"`
}

// DeleteHostImageConfigOutput is the output for deleting a host image configuration.
type DeleteHostImageConfigOutput struct {
	Body struct {
		Message string `json:"message" doc:"Confirmation message"`
	}
}
