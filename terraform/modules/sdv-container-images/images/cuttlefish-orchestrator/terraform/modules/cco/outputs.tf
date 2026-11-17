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

# =============================================================================
# Service
# =============================================================================

output "service_url" {
  value       = google_cloud_run_v2_service.orchestrator.uri
  description = "URL of the orchestrator (run.app). Behind IAP when auth = iap; internal-only when ingress = private."
}

output "cloud_run_service_name" {
  value       = google_cloud_run_v2_service.orchestrator.name
  description = "Name of the Cloud Run service"
}

output "ingress" {
  value       = var.ingress
  description = "CCO reachability: public (INGRESS_TRAFFIC_ALL) or private (internal ingress only)"
}

output "auth" {
  value       = var.auth
  description = "Front-door mechanism: iap (built-in Cloud Run IAP)"
}

output "device_ingress" {
  value       = var.device_ingress
  description = "Where WebRTC device media meets the internet: public, private, or relay"
}

output "iap_audience" {
  value       = local.use_iap ? local.iap_audience : ""
  description = <<-EOT
    The audience the BACKEND expects on the IAP-injected assertion header
    (iap mode). This is NOT the value cfctl/CI configure. Programmatic clients
    mint an ID token whose aud is the custom IAP OAuth client ID (see
    cfctl_audience), which IAP checks at the edge. Do not pass this to
    `cfctl config set-audience`.
  EOT
}

output "cfctl_audience" {
  value       = var.iap_oauth_client_id
  description = <<-EOT
    The OIDC-token audience for `cfctl config set-audience`: the custom IAP
    OAuth client ID (var.iap_oauth_client_id), EMPTY by default. A service
    account does not need it. Leave the audience unset and cfctl
    authenticates with a self-signed JWT (aud = service URL), which works
    with the Google-managed client. Set iap_oauth_client_id only for user /
    browser-interactive programmatic access. See
    docs/concepts/authentication.md.
  EOT
}

output "service_account_email" {
  value       = google_service_account.orchestrator.email
  description = "Service account used by the orchestrator"
}

output "internal_lb_url" {
  value       = local.internal_lb_enabled ? "http://${google_compute_forwarding_rule.internal_lb[0].ip_address}" : ""
  description = <<-EOT
    The service URL for credential-less legacy clients in network mode.
    OmniLab ATS's --orchestration_service_url points here. Their HTTP/1.1
    requests never survive Google's front end on the run.app URL (empty POSTs
    without Content-Length are rejected with 411 before the service sees
    them); this internal load balancer accepts them. Empty in iap mode.
  EOT
}

# =============================================================================
# Database
# =============================================================================

output "spanner_instance" {
  value       = var.create_spanner ? google_spanner_instance.main[0].name : ""
  description = "Cloud Spanner instance name"
}

output "spanner_database" {
  value       = var.create_spanner ? google_spanner_database.main[0].name : ""
  description = "Cloud Spanner database name"
}

# =============================================================================
# Networking
# =============================================================================

output "network_name" {
  value       = var.create_network ? google_compute_network.network[0].name : var.network_name
  description = "VPC network name"
}

output "network_id" {
  value       = var.create_network ? google_compute_network.network[0].id : ""
  description = "VPC network ID"
}

output "subnet_name" {
  value       = var.create_network ? google_compute_subnetwork.subnet[0].name : var.subnet_name
  description = "Subnet name"
}

output "subnetwork_id" {
  value       = var.create_network ? google_compute_subnetwork.subnet[0].id : ""
  description = "Subnetwork ID"
}

# =============================================================================
# Configuration
# =============================================================================

output "artifact_registry" {
  value       = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.repo.repository_id}"
  description = "Artifact Registry repository for container images"
}

output "build" {
  value = {
    project         = var.project_id
    region          = var.region
    repository      = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.repo.repository_id}"
    service_account = google_service_account.builder.id
    staging_dir     = "gs://${google_storage_bucket.build_staging.name}/source"
  }
  description = "What scripts/build-image.sh reads to run a build"
}

# =============================================================================
# Next Steps
# =============================================================================

output "next_steps" {
  value = <<-EOT

    Deployment complete. Next steps:

    1. Build and push the container image (prints its image@digest):
       scripts/build-image.sh

    2. Deploy that digest, and pin it as image in terraform.tfvars so a plain
       apply keeps it:
       terraform apply -var image=<digest>

    3. Grant access by adding principals to iap_members in terraform.tfvars
       and re-applying. The IAP access list is the entire access model.
       Anyone on it has full access; anyone not on it cannot reach the app.

    4. Open the console:
       ${google_cloud_run_v2_service.orchestrator.uri}
    %{if var.auth == "network"~}

    Credential-less legacy clients (OmniLab ATS) must use the internal load
    balancer, not the run.app URL:
       ${local.internal_lb_enabled ? "http://${google_compute_forwarding_rule.internal_lb[0].ip_address}" : ""}
    %{endif~}
    %{if length(var.cli_users) > 0~}

    Each CLI user (cli_users) sets up cfctl with:
       cfctl config set-server ${google_cloud_run_v2_service.orchestrator.uri}
       cfctl config set-impersonate ${google_service_account.cli[0].email}
       cfctl auth whoami
    %{endif~}
  EOT

  description = "Follow-up steps after terraform apply"
}

output "cli_service_account" {
  value       = length(var.cli_users) > 0 ? google_service_account.cli[0].email : ""
  description = "The managed cfctl signing identity (empty unless cli_users is set); users run `cfctl config set-impersonate` with it"
}
