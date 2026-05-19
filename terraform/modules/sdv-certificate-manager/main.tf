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

data "google_project" "project" {}

locals {
  # Certificate map hostname matching is entry-based, not SAN-based.
  # Wildcard map entries (and wildcard cert SANs) require DNS authorization;
  # load_balancer auth does not support wildcard domains.
  certificate_map_host_entries = merge(
    {
      for k, d in var.domains : "${k}-apex" => {
        name       = "${var.name}-entry-${k}"
        domain_key = k
        hostname   = d
      }
    },
    var.certificate_authorization_type == "dns" ? {
      for k, d in var.domains : "${k}-wildcard" => {
        name       = "${var.name}-entry-${k}-wildcard"
        domain_key = k
        hostname   = "*.${d}"
      }
    } : {},
    var.certificate_authorization_type == "load_balancer" ? {
      for k, d in var.domains : "${k}-mcp" => {
        name       = "${var.name}-entry-${k}-mcp"
        domain_key = k
        hostname   = "mcp.${d}"
      }
    } : {}
  )
}

# DNS authorization (CNAME) only for certificate_authorization_type = "dns".
# "load_balancer" uses managed domains without DNS authorizations (static A records flow).
resource "google_certificate_manager_dns_authorization" "instance" {
  for_each = var.certificate_authorization_type == "dns" ? var.domains : {}

  name   = each.key == "main" ? "${var.name}-dns-auth" : "${var.name}-dns-auth-${each.key}"
  domain = each.value
}

# TAA-1571: State migration block for 3.0.0 -> 3.1.0 upgrade.
# Remove after all environments have been upgraded.
moved {
  from = google_certificate_manager_dns_authorization.instance
  to   = google_certificate_manager_dns_authorization.instance["main"]
}

resource "google_certificate_manager_certificate" "horizon_sdv_cert" {
  for_each = var.domains
  project  = data.google_project.project.project_id

  name  = "${var.name}-${each.key}"
  scope = "DEFAULT"

  managed {
    domains = var.certificate_authorization_type == "load_balancer" ? [
      each.value,
      "mcp.${each.value}",
      ] : [
      google_certificate_manager_dns_authorization.instance[each.key].domain,
      "*.${google_certificate_manager_dns_authorization.instance[each.key].domain}",
    ]
    dns_authorizations = var.certificate_authorization_type == "dns" ? [
      google_certificate_manager_dns_authorization.instance[each.key].id,
    ] : []
  }
}

resource "google_certificate_manager_certificate_map" "horizon_sdv_map" {
  project     = data.google_project.project.project_id
  name        = "horizon-sdv-map"
  description = "Certificate Manager Map for Horizon SDV"
}

resource "google_certificate_manager_certificate_map_entry" "horizon_sdv_map_entry" {
  for_each = local.certificate_map_host_entries

  name         = each.value.name
  map          = google_certificate_manager_certificate_map.horizon_sdv_map.name
  certificates = [google_certificate_manager_certificate.horizon_sdv_cert[each.value.domain_key].id]
  hostname     = each.value.hostname
}
