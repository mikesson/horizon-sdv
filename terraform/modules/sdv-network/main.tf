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
# Main configuration file for the "sdv-network" module.
# Create a VPC, subnets with required IP CIDR ranges and routes.

data "google_project" "project" {}

data "google_compute_network" "existing_network" {
  count   = var.create_network ? 0 : 1
  name    = var.existing_network_name
  project = data.google_project.project.project_id
}

data "google_compute_subnetwork" "existing_subnetwork" {
  count   = var.create_network ? 0 : 1
  name    = var.existing_subnetwork_name
  region  = var.region
  project = data.google_project.project.project_id
}

module "vpc" {
  source  = "terraform-google-modules/network/google"
  version = "~> 9.1"
  count   = var.create_network ? 1 : 0

  project_id   = data.google_project.project.project_id
  network_name = var.network
  routing_mode = "GLOBAL"

  subnets = [
    {
      subnet_name              = var.subnetwork
      subnet_region            = var.region
      subnet_ip                = "10.1.0.0/24"
      enable_ula_internal_ipv6 = true
      private_ip_google_access = false
    }
  ]

  secondary_ranges = {
    "${var.subnetwork}" = [
      {
        range_name    = "pods-range"
        ip_cidr_range = "10.10.0.0/16"
      },
      {
        range_name    = "services-range"
        ip_cidr_range = "10.12.0.0/16"
      },
    ]
  }

  routes = [
    {
      name                     = var.router_name
      description              = "route through IGW to access internet"
      destination_range        = "0.0.0.0/0"
      tags                     = "egress-inet"
      next_hop_internet        = "true"
      private_ip_google_access = true
    }
  ]
}
