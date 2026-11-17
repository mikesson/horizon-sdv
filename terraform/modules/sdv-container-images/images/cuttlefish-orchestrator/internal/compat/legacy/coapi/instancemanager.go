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

// Package coapi holds the legacy Cloud (Android) Orchestrator REST contract,
// copied verbatim from github.com/google/cloud-android-orchestration/api/v1 so
// that the wire shape (JSON field names and structure) is byte-for-byte
// identical. Existing Google integrations depend on these exact types; do not
// rename fields or change tags. CCO serves them at the original /v1 paths.
package coapi

// CreateHostRequest is the body of POST /v1/zones/{zone}/hosts.
type CreateHostRequest struct {
	// [REQUIRED]
	HostInstance *HostInstance `json:"host_instance"`
}

// Zone is a compute zone in which hosts can be created.
type Zone struct {
	Name string `json:"name"`
}

// HostInstance describes a host VM, the top-level Cloud Orchestrator resource.
type HostInstance struct {
	// [Output Only] Instance name.
	Name string `json:"name,omitempty"`
	// GCP specific properties.
	GCP *GCPInstance `json:"gcp,omitempty"`
	// Docker specific properties.
	Docker *DockerInstance `json:"docker,omitempty"`
}

// DockerInstance holds Docker-backed host properties.
type DockerInstance struct {
	// Specifies the docker image name.
	ImageName string `json:"image_name"`
	// IP address of docker instance.
	IPAddress string `json:"ip_address"`
}

// GCPInstance holds GCE-backed host properties.
type GCPInstance struct {
	// [REQUIRED] Specifies the machine type of the VM Instance.
	// Check https://cloud.google.com/compute/docs/regions-zones#available for available values.
	MachineType string `json:"machine_type"`
	// Specifies a minimum CPU platform for the VM instance.
	MinCPUPlatform string `json:"min_cpu_platform"`
	// List of accelerator configurations.
	AcceleratorConfigs []*AcceleratorConfig `json:"accelerator_configs,omitempty"`
	// Boot disk size in GB; Defaults to SourceImage disk size if unset
	BootDiskSizeGB int64 `json:"boot_disk_size_gb,omitempty"`
}

// AcceleratorConfig requests GPUs/accelerators on a GCP host.
type AcceleratorConfig struct {
	// Number of accelerators.
	AcceleratorCount int64 `json:"accelerator_count,omitempty"`
	// Name of the accelerator type (e.g. nvidia-tesla-p100).
	AcceleratorType string `json:"accelerator_type,omitempty"`
}

// Operation is a long-running operation handle returned by async methods.
type Operation struct {
	Name string `json:"name"`
	// Service-specific metadata associated with the operation.  It typically
	// contains progress information and common metadata such as create time.
	Metadata any `json:"metadata,omitempty"`
	// If the value is `false`, it means the operation is still in progress.
	// If `true`, the operation is completed, and either `error` or `response` is
	// available.
	Done bool `json:"done"`
}

// ListZonesResponse is the body of GET /v1/zones.
type ListZonesResponse struct {
	Items []*Zone `json:"items"`
}

// ListHostsResponse is the body of GET /v1/zones/{zone}/hosts.
type ListHostsResponse struct {
	Items []*HostInstance `json:"items"`
	// This token allows you to get the next page of results for list requests.
	// If the number of results is larger than maxResults, use the `nextPageToken`
	// as a value for the query parameter `pageToken` in the next list request.
	// Subsequent list requests will have their own `nextPageToken` to continue
	// paging through out all the results.
	NextPageToken string `json:"nextPageToken,omitempty"`
}

// Config is the body of GET /v1/config.
//
// To be separated in to new file if the config needs to contain intormation other than instance manager
type Config struct {
	InstanceManagerType string `json:"instance_manager_type"`
}
