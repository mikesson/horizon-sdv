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

# The whole deployment. This root only configures the providers
# (providers.tf) and passes every variable through.

module "cco" {
  source = "./modules/cco"

  ingress                     = var.ingress
  auth                        = var.auth
  device_ingress              = var.device_ingress
  client_cidrs                = var.client_cidrs
  iap_members                 = var.iap_members
  cli_users                   = var.cli_users
  deployers                   = var.deployers
  iap_oauth_client_id         = var.iap_oauth_client_id
  project_id                  = var.project_id
  deployment                  = var.deployment
  region                      = var.region
  service_name                = var.service_name
  image                       = var.image
  artifact_repository_id      = var.artifact_repository_id
  cloud_run_cpu               = var.cloud_run_cpu
  cloud_run_memory            = var.cloud_run_memory
  cloud_run_min_instances     = var.cloud_run_min_instances
  cloud_run_max_instances     = var.cloud_run_max_instances
  cloud_run_concurrency       = var.cloud_run_concurrency
  network_name                = var.network_name
  subnet_name                 = var.subnet_name
  subnet_cidr                 = var.subnet_cidr
  proxy_subnet_cidr           = var.proxy_subnet_cidr
  create_network              = var.create_network
  firewall_rule_names         = var.firewall_rule_names
  create_spanner              = var.create_spanner
  spanner_instance_name       = var.spanner_instance_name
  spanner_database_name       = var.spanner_database_name
  spanner_deletion_protection = var.spanner_deletion_protection
  spanner_processing_units    = var.spanner_processing_units
  seed_database               = var.seed_database
  labels                      = var.labels
  host_service_account        = var.host_service_account
  host_image_project          = var.host_image_project
  create_host_service_account = var.create_host_service_account
  create_artifacts_bucket     = var.create_artifacts_bucket
  artifacts_bucket            = var.artifacts_bucket
  artifacts_retention_days    = var.artifacts_retention_days
  staging_retention_days      = var.staging_retention_days
  turn_servers                = var.turn_servers
  turn_shared_secret          = var.turn_shared_secret
  turn_realm                  = var.turn_realm
  relay_image                 = var.relay_image
  relay_instance_count        = var.relay_instance_count
  relay_machine_type          = var.relay_machine_type
  relay_zones                 = var.relay_zones
  turns_domain                = var.turns_domain
  acme_email                  = var.acme_email
  acme_eab_kid                = var.acme_eab_kid
  acme_eab_hmac_secret        = var.acme_eab_hmac_secret
  spot_advice                 = var.spot_advice
  trace_sample_rate           = var.trace_sample_rate
}
