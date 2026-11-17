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

package hostfeature

import (
	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// ListHostsInput is the input for listing hosts.
type ListHostsInput struct {
	Limit     int    `query:"limit" minimum:"1" maximum:"100" default:"20" doc:"Maximum number of hosts to return"`
	Offset    int    `query:"offset" minimum:"0" default:"0" doc:"Number of hosts to skip"`
	PageToken string `query:"page_token" doc:"Opaque token from a previous page's next_page_token; cannot be combined with a non-zero offset"`
	Name      string `query:"name" doc:"Filter by exact host name; never matches a deleted host"`
	Zone      string `query:"zone" doc:"Filter by zone"`
	Status    string `query:"status" enum:"pending,provisioning,starting,running,stopping,stopped,deleting,deleted,error,unreachable,preempted" doc:"Filter by status"`
	CreatedBy string `query:"created_by" doc:"Filter by creator email"`
	Mine      bool   `query:"mine" doc:"Only hosts created by the caller (mutually exclusive with created_by)"`
	Class     string `query:"class" enum:"reserved,fleet,dedicated,batch" doc:"Filter by host class: whose lifecycle rules apply to the host"`
}

// ListHostsOutput is the output for listing hosts.
type ListHostsOutput struct {
	Body struct {
		Items  []*host.Host `json:"items" doc:"List of hosts"`
		Limit  int          `json:"limit" doc:"Requested limit"`
		Offset int          `json:"offset" doc:"Requested offset"`
		Total  int          `json:"total,omitempty" doc:"Total number of items"`
		// NextPageToken continues after this page even when rows are created
		// or deleted between requests, which an offset cannot.
		NextPageToken string `json:"next_page_token" doc:"Token for the next page; empty on the last page"`
	}
}

// GetHostInput is the input for getting a host.
type GetHostInput struct {
	ID string `path:"id" format:"uuid" doc:"Host ID"`
}

// GetHostOutput is the output for getting a host.
type GetHostOutput struct {
	Body *host.Host
}

// CreateHostInput is the input for creating a host.
type CreateHostInput struct {
	IdempotencyKey string `header:"Idempotency-Key" doc:"Optional key making host creation safe to retry; a repeat returns the original host"`
	Body           struct {
		Name              string `json:"name" required:"true" minLength:"1" maxLength:"63" pattern:"^[a-z][a-z0-9-]*$" doc:"Host name"`
		Zone              string `json:"zone,omitempty" doc:"GCE zone (e.g., us-central1-a). Naming a zone pins the host to it (no automatic failover); omitting it lets the server rank placement across the enabled catalog"`
		MachineType       string `json:"machine_type,omitempty" doc:"GCE machine type (e.g., n1-standard-8)"`
		GPUType           string `json:"gpu_type,omitempty" doc:"GPU to attach on N1 machine types (e.g., nvidia-tesla-t4). Ignored for machine types with a built-in GPU (G2/G4) or none (E2/C4A)."`
		GPUCount          int64  `json:"gpu_count,omitempty" minimum:"0" maximum:"4" doc:"Number of GPUs to attach on N1 (0 = CPU-only; GPUs are opt-in). Ignored for built-in-GPU and non-GPU machine types."`
		MinCPUPlatform    string `json:"min_cpu_platform,omitempty" maxLength:"100" doc:"Minimum CPU platform (e.g. Intel Cascade Lake), passed to GCE verbatim; only meaningful on machine families that support it. Empty = no constraint."`
		BootDiskSizeGB    int64  `json:"boot_disk_size_gb,omitempty" minimum:"0" maximum:"65536" doc:"Boot disk size in GB (0 = the 400 GB default). Non-zero values below 10 are rejected."`
		MaxDevices        int64  `json:"max_devices,omitempty" minimum:"1" maximum:"10" default:"4" doc:"Maximum number of CVD instances the host will accept; a device with several instances costs one slot each"`
		ProvisioningModel string `json:"provisioning_model,omitempty" enum:"STANDARD,SPOT" doc:"VM provisioning model (STANDARD or SPOT)"`
		TTLHours          int64  `json:"ttl_hours,omitempty" minimum:"1" maximum:"168" doc:"Host TTL in hours before automatic teardown (default 10). Device activity can extend the resulting expiry."`
		Exclusive         bool   `json:"exclusive,omitempty" doc:"Fence the host from automatic placement: only creates that target it by host_id land here. Protects tests on this host from noisy CI neighbors."`
	}
}

// CreateHostOutput is the output for creating a host.
type CreateHostOutput struct {
	Body *host.Host
}

// DeleteHostInput is the input for deleting a host.
type DeleteHostInput struct {
	ID string `path:"id" format:"uuid" doc:"Host ID"`
}

// DeleteHostOutput is the output for deleting a host.
type DeleteHostOutput struct{}

// SyncHostInput is the input for syncing a host.
type SyncHostInput struct {
	ID string `path:"id" format:"uuid" doc:"Host ID"`
}

// SyncHostOutput is the output for syncing a host.
type SyncHostOutput struct{}

// StartHostInput is the input for starting a host.
type StartHostInput struct {
	ID string `path:"id" format:"uuid" doc:"Host ID"`
}

// StartHostOutput is the output for starting a host.
type StartHostOutput struct {
	Body struct {
		Message string `json:"message" doc:"Status message"`
	}
}

// StopHostInput is the input for stopping a host.
type StopHostInput struct {
	ID string `path:"id" format:"uuid" doc:"Host ID"`
}

// StopHostOutput is the output for stopping a host.
type StopHostOutput struct {
	Body struct {
		Message string `json:"message" doc:"Status message"`
	}
}

// RetryHostInput is the input for retrying a preempted or failed host.
type RetryHostInput struct {
	ID   string `path:"id" format:"uuid" doc:"Host ID"`
	Body struct {
		FallbackToStandard bool `json:"fallback_to_standard,omitempty" doc:"Fall back to standard provisioning if Spot capacity unavailable"`
	}
}

// RetryHostOutput is the output for retrying a host.
type RetryHostOutput struct {
	Body *host.Host
}

// UpdateHostInput is the input for reshaping a live host. The bound mirrors
// the create-time one (types_bound_test.go binds the two tags).
type UpdateHostInput struct {
	ID   string `path:"id" format:"uuid" doc:"Host ID"`
	Body struct {
		MaxDevices *int64 `json:"max_devices,omitempty" minimum:"1" maximum:"10" doc:"Maximum number of CVD instances the host will accept; a device with several instances costs one slot each. Cannot be set below the instances it already carries"`
	}
}

// UpdateHostOutput returns the reshaped host.
type UpdateHostOutput struct {
	Body *host.Host
}

// ExtendHostInput is the input for raising a host's lease. The maximum bounds
// one request and mirrors host.MaxHostTTLHours (types_bound_test.go binds
// them); repeated extends have no cumulative cap.
type ExtendHostInput struct {
	ID   string `path:"id" format:"uuid" doc:"Host ID"`
	Body struct {
		Hours int64 `json:"hours" minimum:"1" maximum:"168" doc:"Hours to add to the host's lease, counted from the later of now and its current expiry"`
	}
}

// ExtendHostOutput returns the extended host.
type ExtendHostOutput struct {
	Body *host.Host
}
