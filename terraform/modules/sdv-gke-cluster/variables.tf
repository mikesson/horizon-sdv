# Copyright (c) 2024-2026 Accenture, All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#         http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

variable "project_id" {
  description = "Define the project id"
  type        = string
}

variable "cluster_name" {
  description = "Name of the cluster"
  type        = string
}

variable "cluster_version" {
  description = "GKE cluster control plane version (e.g. 1.33.5-gke.2172001). Used when not using release_channel; pins the cluster and allows node pools to set auto_upgrade = false."
  type        = string
}

variable "node_pool_name" {
  description = "Name of the cluster node pool"
  type        = string
}

variable "build_node_pool_name" {
  description = "Name of the build node pool"
  type        = string
}

variable "build_node_pool_node_count" {
  description = "Number of nodes for the build node pool"
  type        = number
}

variable "build_node_pool_machine_type" {
  description = "Type fo the machine for the build node pool"
  type        = string
}

variable "build_node_pool_min_node_count" {
  description = "Number of minimum of nodes for the build node pool"
  type        = number
  default     = 0
}

variable "build_node_pool_max_node_count" {
  description = "Number of max of nodes for the build node pool"
  type        = number
  default     = 3
}

variable "abfs_build_node_pool_name" {
  description = "Name of the ABFS build node pool"
  type        = string
}

variable "abfs_build_node_pool_node_count" {
  description = "Number of nodes for the ABFS build node pool"
  type        = number
}

variable "abfs_build_node_pool_machine_type" {
  description = "Type for the machine for the ABFS build node pool"
  type        = string
}

variable "abfs_build_node_pool_min_node_count" {
  description = "Number of minimum nodes for the ABFS build node pool"
  type        = number
  default     = 0
}

variable "abfs_build_node_pool_max_node_count" {
  description = "Number max of nodes for the ABFS build node pool"
  type        = number
  default     = 3
}

variable "abfs_build_node_pool_version" {
  description = "Kubernetes version for the ABFS build node pool (e.g. 1.32.7-gke.1079000). Pins the node pool to this GKE version."
  type        = string
}

variable "openbsw_build_node_pool_name" {
  description = "Name of the OpenBSW build node pool"
  type        = string
}

variable "openbsw_build_node_pool_node_count" {
  description = "Number of nodes for the OpenBSW build node pool"
  type        = number
}

variable "openbsw_build_node_pool_machine_type" {
  description = "Type for the machine for the OpenBSW build node pool"
  type        = string
}

variable "openbsw_build_node_pool_min_node_count" {
  description = "Number of minimum nodes for the OpenBSW build node pool"
  type        = number
  default     = 0
}

variable "openbsw_build_node_pool_max_node_count" {
  description = "Number max of nodes for the OpenBSW build node pool"
  type        = number
  default     = 3
}

variable "utility_node_pool_name" {
  description = "Name of the utility node pool (Vertex/Gemini CLI and similar; not Android-specific)"
  type        = string
}

variable "utility_node_pool_node_count" {
  description = "Number of nodes for the utility node pool"
  type        = number
}

variable "utility_node_pool_machine_type" {
  description = "Machine type for the utility node pool. Size for pods with limits up to 32 CPU / 96Gi (e.g. workloads/common/agentic-ai/gemini/helm/values.yaml); n2-standard-32 allocatable CPU is often slightly under 32 cores after kube-reserved, so n2-standard-48 or larger is safer unless CPU limits are reduced."
  type        = string
}

variable "utility_node_pool_min_node_count" {
  description = "Minimum number of nodes for the utility node pool"
  type        = number
  default     = 0
}

variable "utility_node_pool_max_node_count" {
  description = "Maximum number of nodes for the utility node pool"
  type        = number
  default     = 5
}

variable "network" {
  description = "Name of the network"
  type        = string
}

variable "subnetwork" {
  description = "Name of the subnetwork"
  type        = string
}


variable "location" {
  description = "Define the default location for the project"
  type        = string
}

variable "machine_type" {
  description = "Define the machine type of the node poll"
  type        = string
  default     = "e2-medium"
}

variable "service_account" {
  description = "Define the service account of the node poll"
  type        = string
}

variable "node_locations" {
  description = "Define the location of the nodes"
  type        = list(string)
}

variable "node_count" {
  description = "Define the number of node count"
  type        = number
  default     = 1
}

variable "node_pool_min_node_count" {
  description = "Minimum number of nodes for the main node pool"
  type        = number
  default     = 1
}

variable "node_pool_max_node_count" {
  description = "Maximum number of nodes for the main node pool"
  type        = number
  default     = 6
}

variable "enable_kms_encryption" {
  description = "Enable KMS encryption for Kubernetes secrets"
  type        = bool
  default     = false
}

variable "kms_crypto_key_id" {
  description = "KMS crypto key ID for GKE secrets encryption"
  type        = string
  default     = ""
}

