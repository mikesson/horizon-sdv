# Copyright (c) 2024-2025 Accenture, All Rights Reserved.
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
#
# Description:
# Configuration file containing outputs for the "sdv-network" module.
# Outputs can be used by other modules or resources.

output "network_name" {
  description = "The name of the VPC network."
  value       = var.create_network ? module.vpc[0].network_name : data.google_compute_network.existing_network[0].name
}

output "subnetwork_name" {
  description = "The name of the subnetwork."
  value       = var.create_network ? module.vpc[0].subnets_names[0] : data.google_compute_subnetwork.existing_subnetwork[0].name
}

output "vpc_nat_router_name" {
  description = "The name of the created router for NAT."
  value       = var.create_network ? google_compute_router.vpc_nat_router[0].name : null
}

output "vpc_nat_name" {
  description = "The name of the created NAT."
  value       = var.create_network ? google_compute_router_nat.vpc_nat[0].name : null
}

output "vpc_nat_ip_name" {
  description = "The name of the created NAT ip address"
  value       = var.create_network ? google_compute_address.vpc_nat_ip[0].name : null
}
