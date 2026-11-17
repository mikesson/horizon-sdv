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
# Relay firewall
#
# Ingress is the TURN ports and nothing else. There is no relay port range to
# open. turn-rs binds one socket per interface and resolves a peer by port
# lookup inside an authenticated session, so the 49152-65535 opening every
# coturn deployment carries has no equivalent here.
#
# Egress is closed except device media. turn-rs cannot address a peer that is
# not already one of its own clients, so relaying into the VPC is not
# expressible. This VM faces the internet under a single-maintainer dependency,
# and a second layer costs nothing.
# =============================================================================

resource "google_compute_firewall" "turn_ingress" {
  project     = var.project_id
  name        = "allow-cco-relay-turn"
  network     = var.network
  description = "TURN control and media from clients."
  direction   = "INGRESS"
  priority    = 1000

  allow {
    protocol = "udp"
    ports    = ["3478"]
  }

  allow {
    protocol = "tcp"
    # 3478 is TURN over TCP for networks that block UDP. 443 carries TURNS for
    # networks that also block 3478, which is most restrictive corporate egress.
    ports = ["3478", "443"]
  }

  source_ranges = var.client_cidrs
  target_tags   = [local.network_tag]
}

# HTTP-01 is the only challenge available. TLS-ALPN-01 wants 443, which the
# relay is holding, and DNS-01 would need the zero-role service account to hold
# DNS credentials. The rule exists only where a certificate is issued.
resource "google_compute_firewall" "acme_challenge" {
  count = var.turns_domain == "" ? 0 : 1

  project     = var.project_id
  name        = "allow-cco-relay-acme"
  network     = var.network
  description = "ACME HTTP-01 challenge for the TURNS certificate."
  direction   = "INGRESS"
  priority    = 1000

  allow {
    protocol = "tcp"
    ports    = ["80"]
  }

  # The issuer validates from addresses it does not publish.
  source_ranges = ["0.0.0.0/0"]
  target_tags   = [local.network_tag]
}

resource "google_compute_firewall" "media_egress" {
  project     = var.project_id
  name        = "allow-cco-relay-to-hosts"
  network     = var.network
  description = "Device media from the relay to Cuttlefish hosts."
  direction   = "EGRESS"
  priority    = 1000

  allow {
    protocol = "udp"
    ports    = [var.host_media_ports]
  }

  target_tags        = [local.network_tag]
  destination_ranges = ["0.0.0.0/0"]
}

resource "google_compute_firewall" "orchestrator_egress" {
  project     = var.project_id
  name        = "allow-cco-relay-to-orchestrator"
  network     = var.network
  description = "The agent's authorization calls, and metadata-server token minting."
  direction   = "EGRESS"
  priority    = 1000

  allow {
    protocol = "tcp"
    ports    = ["443", "80"]
  }

  target_tags = [local.network_tag]
  # Cloud Run and the metadata server are not a fixed range. Egress is bounded
  # by protocol and port rather than destination; the deny below removes
  # everything else, including any path toward host control ports.
  destination_ranges = ["0.0.0.0/0"]
}

resource "google_compute_firewall" "deny_other_egress" {
  project     = var.project_id
  name        = "deny-cco-relay-egress"
  network     = var.network
  description = "Everything the relay does not need. Lower priority than the allows above."
  direction   = "EGRESS"
  priority    = 2000

  deny {
    protocol = "all"
  }

  target_tags        = [local.network_tag]
  destination_ranges = ["0.0.0.0/0"]
}
