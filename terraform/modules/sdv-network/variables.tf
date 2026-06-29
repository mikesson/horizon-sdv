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

variable "region" {
  description = "Define the Region"
  type        = string
}

variable "network" {
  description = "Define the Network"
  type        = string
}

variable "subnetwork" {
  description = "Define the Sub Network"
  type        = string
}

variable "router_name" {
  description = "Define the router name"
  type        = string
}

variable "enable_arm64_dedicated_subnet" {
  description = "Create dedicated ARM64 subnet and NAT in arm64_region (not required when ARM64 uses primary sdv-subnet)."
  type        = bool
}

variable "arm64_region" {
  description = "Define the ARM64 region"
  type        = string
}

variable "arm64_subnetwork" {
  description = "Define the ARM64 Subnetwork name"
  type        = string
}

variable "pods_range" {
  type = string
}

variable "services_range" {
  type = string
}

variable "arm64_pods_range" {
  type = string
}

variable "arm64_services_range" {
  type = string
}

variable "arm64_pods_secondary_range_name" {
  description = "Secondary IP range name for pods on the ARM64 dedicated subnet (must match existing GCP names for brownfield)."
  type        = string
}

variable "arm64_services_secondary_range_name" {
  description = "Secondary IP range name for services on the ARM64 dedicated subnet (must match existing GCP names for brownfield)."
  type        = string
}

