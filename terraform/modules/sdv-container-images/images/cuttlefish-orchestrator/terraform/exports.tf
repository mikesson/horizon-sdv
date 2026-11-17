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

# Every output of modules/cco, re-exported so `terraform output` works
# from this directory. The module documents each one.

output "service_url" {
  value = module.cco.service_url
}

output "build" {
  value = module.cco.build
}

output "cloud_run_service_name" {
  value = module.cco.cloud_run_service_name
}

output "ingress" {
  value = module.cco.ingress
}

output "auth" {
  value = module.cco.auth
}

output "device_ingress" {
  value = module.cco.device_ingress
}

output "iap_audience" {
  value = module.cco.iap_audience
}

output "cfctl_audience" {
  value = module.cco.cfctl_audience
}

output "service_account_email" {
  value = module.cco.service_account_email
}

output "internal_lb_url" {
  value = module.cco.internal_lb_url
}

output "spanner_instance" {
  value = module.cco.spanner_instance
}

output "spanner_database" {
  value = module.cco.spanner_database
}

output "network_name" {
  value = module.cco.network_name
}

output "network_id" {
  value = module.cco.network_id
}

output "subnet_name" {
  value = module.cco.subnet_name
}

output "subnetwork_id" {
  value = module.cco.subnetwork_id
}

output "artifact_registry" {
  value = module.cco.artifact_registry
}

output "next_steps" {
  value = module.cco.next_steps
}

output "cli_service_account" {
  value = module.cco.cli_service_account
}
