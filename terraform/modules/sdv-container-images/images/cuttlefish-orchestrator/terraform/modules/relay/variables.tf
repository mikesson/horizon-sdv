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

# This module has no TURN shared-secret variable. The relay authenticates
# through the orchestrator, which holds the secret and returns derived keys, so
# a secret on these VMs would be both unnecessary and a way to bypass expiry
# enforcement. See README.md.

variable "project_id" {
  type        = string
  description = "GCP project hosting the relay instances."
}

variable "region" {
  type        = string
  description = "Region for the relay subnetwork and instances."
}

variable "zones" {
  type        = list(string)
  description = "Zones to spread relay instances across, round-robin."

  validation {
    condition     = length(var.zones) > 0
    error_message = "At least one zone is required."
  }
}

variable "network" {
  type        = string
  description = "Self link or name of the VPC the relay attaches to."
}

variable "subnetwork" {
  type        = string
  description = "Self link or name of the subnetwork for relay instances."
}

variable "orchestrator_url" {
  type        = string
  description = "Base URL of the orchestrator the hook agent authorizes against."

  validation {
    condition     = startswith(var.orchestrator_url, "https://")
    error_message = "orchestrator_url must be https: the agent sends a bearer token."
  }
}

variable "realm" {
  type        = string
  default     = "cco"
  description = <<-EOT
    TURN realm. It is part of the long-term key, so this must equal the
    orchestrator's CCO_TURN_REALM or every credential is rejected. Both sides
    are set from one root-module variable.
  EOT
}

variable "instance_count" {
  type        = number
  default     = 2
  description = "Number of relay instances."

  validation {
    condition     = var.instance_count > 0
    error_message = "instance_count must be at least 1."
  }
}

variable "machine_type" {
  type        = string
  default     = "e2-medium"
  description = "Machine type for relay instances. Relay throughput is NIC-bound, not CPU-bound."
}

variable "image" {
  type        = string
  description = "Relay image built by deployment/packer/relay-image.pkr.hcl (self link or family reference)."
}

variable "host_network_tag" {
  type        = string
  default     = "cuttlefish-host"
  description = "Network tag on host VMs; relay egress to device media is scoped to it."
}

variable "host_media_ports" {
  type        = string
  default     = "15550-15599"
  description = "UDP port range host VMs serve WebRTC media on."
}

variable "client_cidrs" {
  type        = list(string)
  default     = ["0.0.0.0/0"]
  description = <<-EOT
    Who may reach the relay's TURN ports. The default is the internet, which is
    what relay mode is for. Clients arrive from anywhere and are admitted by
    credential, not by network position.
  EOT
}

variable "labels" {
  type        = map(string)
  default     = {}
  description = "Labels applied to relay instances."
}

variable "turns_domain" {
  type        = string
  default     = ""
  description = <<-EOT
    DNS name clients reach TURNS at, resolving to a relay instance's external
    address. Empty serves plain TURN only. `turns:` needs a certificate, and a
    certificate needs a name the client asked for. The no-name path (an address
    certificate) is untested here, above all whether browsers accept one, so it
    is not wired.
  EOT
}

variable "acme_email" {
  type        = string
  default     = ""
  description = "Contact address for the ACME account. Required when turns_domain is set."
}

variable "acme_directory" {
  type        = string
  default     = "https://dv.acme-v02.api.pki.goog/directory"
  description = <<-EOT
    ACME directory. Defaults to Google Trust Services, whose root is
    cross-signed to an old and universally present one. That matters if the
    device leg ever needs TLS, since native WebRTC validates against a root list
    compiled into the binary and not the system store.
  EOT
}

variable "acme_profile" {
  type        = string
  default     = ""
  description = "Optional ACME profile, for issuers that offer shorter-lived certificates."
}

variable "acme_eab_kid" {
  type        = string
  default     = ""
  description = <<-EOT
    External account binding key ID. Google Trust Services requires one; Let's
    Encrypt does not. This is an identifier, useless without the HMAC key, so it
    travels in metadata.
  EOT
}

variable "acme_eab_hmac_secret" {
  type        = string
  default     = ""
  description = <<-EOT
    Secret Manager secret ID holding the external account binding HMAC key.

    The secret ID, not the key. Rendering a credential into instance metadata
    is what made the previous relay module unsalvageable, and the same mistake
    is available here: metadata is readable by anything on the VM and by anyone
    with compute.instances.get. The module grants the relay account access to
    this one secret and nothing else.
  EOT
}
