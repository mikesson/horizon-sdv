# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Every variable is forwarded unchanged to modules/cco, which documents it,
# holds its default and validates it. A null here means "use the module's
# default".

variable "ingress" {
  type    = string
  default = null
}

variable "auth" {
  type    = string
  default = null
}

variable "device_ingress" {
  type    = string
  default = null
}

variable "client_cidrs" {
  type    = list(string)
  default = null
}

variable "iap_members" {
  type    = set(string)
  default = null
}

variable "cli_users" {
  type    = set(string)
  default = null
}

variable "deployers" {
  type    = set(string)
  default = null
}

variable "iap_oauth_client_id" {
  type    = string
  default = null
}

variable "project_id" {
  type = string
}

variable "deployment" {
  type    = string
  default = null
}

variable "region" {
  type = string
}

variable "service_name" {
  type    = string
  default = null
}

variable "image" {
  type    = string
  default = null
}

variable "artifact_repository_id" {
  type    = string
  default = null
}

variable "cloud_run_cpu" {
  type    = number
  default = null
}

variable "cloud_run_memory" {
  type    = string
  default = null
}

variable "cloud_run_min_instances" {
  type    = number
  default = null
}

variable "cloud_run_max_instances" {
  type    = number
  default = null
}

variable "cloud_run_concurrency" {
  type    = number
  default = null
}

variable "network_name" {
  type    = string
  default = null
}

variable "subnet_name" {
  type    = string
  default = null
}

variable "subnet_cidr" {
  type    = string
  default = null
}

variable "proxy_subnet_cidr" {
  type    = string
  default = null
}

variable "create_network" {
  type    = bool
  default = null
}

variable "firewall_rule_names" {
  type    = any
  default = null
}

variable "create_spanner" {
  type    = bool
  default = null
}

variable "spanner_instance_name" {
  type    = string
  default = null
}

variable "spanner_database_name" {
  type    = string
  default = null
}

variable "spanner_deletion_protection" {
  type    = bool
  default = null
}

variable "spanner_processing_units" {
  type    = number
  default = null
}

variable "seed_database" {
  type    = bool
  default = null
}

variable "labels" {
  type    = map(string)
  default = null
}

variable "host_service_account" {
  type    = string
  default = null
}

variable "host_image_project" {
  type    = string
  default = null
}

variable "create_host_service_account" {
  type    = bool
  default = null
}

variable "create_artifacts_bucket" {
  type    = bool
  default = null
}

variable "artifacts_bucket" {
  type    = string
  default = null
}

variable "artifacts_retention_days" {
  type    = number
  default = null
}

variable "staging_retention_days" {
  type    = number
  default = null
}

variable "turn_servers" {
  type    = list(string)
  default = null
}

variable "turn_shared_secret" {
  type      = string
  default   = null
  sensitive = true
}

variable "turn_realm" {
  type    = string
  default = null
}

variable "relay_image" {
  type    = string
  default = null
}

variable "relay_instance_count" {
  type    = number
  default = null
}

variable "relay_machine_type" {
  type    = string
  default = null
}

variable "relay_zones" {
  type    = list(string)
  default = null
}

variable "turns_domain" {
  type    = string
  default = null
}

variable "acme_email" {
  type    = string
  default = null
}

variable "acme_eab_kid" {
  type    = string
  default = null
}

variable "acme_eab_hmac_secret" {
  type    = string
  default = null
}

variable "spot_advice" {
  type    = bool
  default = null
}

variable "trace_sample_rate" {
  type    = number
  default = null
}
