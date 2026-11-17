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
# TURN relay instances
#
# The relay carries device media for deployments whose hosts have no public
# address. Every client is authenticated through the orchestrator rather than
# against a secret of its own, so these VMs hold no credential material.
# =============================================================================

locals {
  network_tag = "cco-relay"

  # One instance per name, spread round-robin across zones. Each advertises its
  # own address: a TURN allocation is per-instance state, and no stateless L4
  # load balancer can preserve it across two peers with different source IPs.
  instances = {
    for i in range(var.instance_count) : "cco-relay-${i}" => {
      zone = var.zones[i % length(var.zones)]
    }
  }

  turns_enabled = var.turns_domain != ""

  # The relay's config, rendered here instead of on the VM. The guards read
  # local.guard_config instead of this. See there for why.
  #
  # The TURNS interface advertises the certificate's name when there is one.
  # A client validates against the name it dialled, and an address would not
  # match. Without a name it advertises the address, which serves the placeholder
  # and works for clients that do not need TLS.
  configs = {
    for name, cfg in local.instances : name => templatefile("${path.module}/templates/turn-server.toml.tftpl", {
      realm            = var.realm
      external_address = google_compute_address.relay[name].address
      turns_external   = local.turns_enabled ? var.turns_domain : google_compute_address.relay[name].address
      hook_endpoint    = "http://127.0.0.1:3001"
    })
  }

  # What the guards below inspect: the same template, rendered with the
  # addresses stubbed.
  #
  # local.configs cannot be used for this. Its addresses are unknown until the
  # reservations exist, so on a fresh relay every guard reading it evaluates to
  # unknown and Terraform DEFERS it to apply. The plan then passes silently,
  # which is the one outcome a plan-time guard exists to prevent. (Observed: a
  # template carrying `[api] listen = "0.0.0.0:3000"` planned clean.)
  #
  # Stubbing costs nothing real. A GCP-assigned address cannot contain a
  # newline or a quote, so it cannot introduce or hide a section. Every input
  # an operator controls (the realm, the TURNS domain, the template itself) is
  # the real one.
  guard_config = templatefile("${path.module}/templates/turn-server.toml.tftpl", {
    realm            = var.realm
    external_address = "0.0.0.0"
    turns_external   = local.turns_enabled ? var.turns_domain : "0.0.0.0"
    hook_endpoint    = "http://127.0.0.1:3001"
  })

  # The [api] section's body, or empty when it is absent. When it is absent,
  # turn-rs takes its own 127.0.0.1:3000 default (src/config.rs:212-225).
  # Non-greedy to the next section header, so [api.ssl] is not swept in.
  api_section = try(regex("(?is)\\[api\\].*?(?:\\n\\[|$)", local.guard_config), "")

  # Every address the control API binds. Searching the whole config for
  # `listen` instead would match [[server.interfaces]], which is meant to bind
  # 0.0.0.0. A guard that fires on the configuration it is meant to permit is
  # one nobody keeps.
  api_listens = [
    for m in regexall("(?im)^[[:space:]]*listen[[:space:]]*=[[:space:]]*[\"']([^\"']+)[\"']", local.api_section) : m[0]
  ]

  # Consumed by the certificate unit. Empty when no name is configured, which
  # leaves the placeholder in place and the relay serving plain TURN.
  acme_env = local.turns_enabled ? join("\n", [
    "ACME_DOMAIN=${var.turns_domain}",
    "ACME_EMAIL=${var.acme_email}",
    "ACME_DIRECTORY=${var.acme_directory}",
    "ACME_PROFILE=${var.acme_profile}",
    "ACME_EAB_KID=${var.acme_eab_kid}",
    # The key itself is fetched at boot from Secret Manager, never written here.
    "ACME_EAB_HMAC_SECRET=${var.acme_eab_hmac_secret}",
    "ACME_PROJECT=${var.project_id}",
  ]) : ""
}

# The relay's identity, granted nothing at all.
#
# turn-rs calls no Google API, and the agent beside it only has to prove who it
# is, which the metadata server does without any role. A stolen metadata token
# from an internet-facing VM therefore yields no access to this project, which
# is what makes exposing the relay acceptable at all.
resource "google_service_account" "relay" {
  project      = var.project_id
  account_id   = "cco-relay"
  display_name = "CCO TURN relay"
  description  = "Runs the TURN relay and its authorization agent. Intentionally holds no IAM roles."
}

# The agent authenticates to the orchestrator by signing a JWT as this account.
# That needs token-creator on the account itself, a binding on the service
# account resource and not a project role. It confers nothing beyond acting as
# an identity that is already on the access list.
#
# A Google-signed identity token from the metadata server would need no grant at
# all, and does not work. Built-in Cloud Run IAP rejects it at every audience
# (checked against the live deployment). IAP wants a self-signed JWT.
resource "google_service_account_iam_member" "relay_self_signer" {
  service_account_id = google_service_account.relay.name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = google_service_account.relay.member
}

# The other grant, and only when a certificate has to be obtained from an issuer
# that requires external account binding. Scoped to secret accessor on a single
# secret, and not a project role.
resource "google_secret_manager_secret_iam_member" "acme_eab" {
  count = var.acme_eab_hmac_secret == "" ? 0 : 1

  project   = var.project_id
  secret_id = var.acme_eab_hmac_secret
  role      = "roles/secretmanager.secretAccessor"
  member    = google_service_account.relay.member
}

resource "google_compute_address" "relay" {
  for_each = local.instances

  project      = var.project_id
  region       = var.region
  name         = each.key
  address_type = "EXTERNAL"
  description  = "Stable address for ${each.key}, advertised to clients as its TURN server."
}

# The device leg's address, reserved instead of assigned.
#
# Two reasons it is a resource of its own. A device inside the VPC reaches the
# relay only here. There is no NAT, and the external address is unroutable from
# a host with no address of its own. The orchestrator also has to be able to
# name it. Reading the IP off the instance would make the Cloud Run service
# depend on a VM that depends on the service's own URL, which is a cycle.
# Reserving the address breaks the cycle, and it keeps the address stable across
# a relay replacement, so a credential minted with these URIs outlives the
# instance that served it.
resource "google_compute_address" "relay_internal" {
  for_each = local.instances

  project      = var.project_id
  region       = var.region
  name         = "${each.key}-internal"
  address_type = "INTERNAL"
  subnetwork   = var.subnetwork
  description  = "Stable internal address for ${each.key}, advertised to devices as its TURN server."
}

resource "google_compute_instance" "relay" {
  for_each = local.instances

  project      = var.project_id
  name         = each.key
  zone         = each.value.zone
  machine_type = var.machine_type
  tags         = [local.network_tag]
  labels       = var.labels

  boot_disk {
    initialize_params {
      image = var.image
      size  = 20
      type  = "pd-balanced"
    }
  }

  network_interface {
    network    = var.network
    subnetwork = var.subnetwork
    network_ip = google_compute_address.relay_internal[each.key].address

    access_config {
      nat_ip = google_compute_address.relay[each.key].address
    }
  }

  service_account {
    email = google_service_account.relay.email
    # The agent presents an identity token, which needs no scope beyond the
    # default. Nothing on this VM calls a Google API.
    scopes = ["https://www.googleapis.com/auth/cloud-platform"]
  }

  metadata = {
    cco-relay-config     = local.configs[each.key]
    cco-relay-acme-env   = local.acme_env
    cco-orchestrator-url = var.orchestrator_url
    # OS Login keeps SSH access on IAM rather than on keys in metadata.
    enable-oslogin = "TRUE"
  }

  shielded_instance_config {
    enable_secure_boot          = true
    enable_vtpm                 = true
    enable_integrity_monitoring = true
  }

  allow_stopping_for_update = true

  lifecycle {
    # turn-rs resolves credentials in the order static credentials, then
    # static-auth-secret, then the hook, returning early at each. A secret
    # present anywhere in this config would mean the hook is never called, with
    # no warning and no log line, and credentials would stop expiring. That is
    # the one misconfiguration that silently removes the property relay mode
    # exists for, so it fails the plan instead of the deployment.
    # Anchored to the start of a line so it matches an actual TOML key. An
    # unanchored search also matches the comment in the template explaining why
    # the key is absent, which is a guard that fires on its own documentation.
    precondition {
      condition     = !can(regex("(?im)^[[:space:]]*(static-auth-secret|static_credentials)", local.guard_config))
      error_message = "The relay config must not carry a static auth secret: it bypasses the orchestrator, and credentials would never expire."
    }

    # turn-rs's control API is UNAUTHENTICATED (its [api.ssl] block is a server
    # certificate, not a client identity), and it serves DestroySession beside
    # GetInfo and GetSession (sdk/protos/server.proto). It is also not optional.
    # One Cargo feature gates both the authentication hook and this server, so
    # every relay runs it whether or not anything calls it.
    #
    # The only thing keeping it off the network is turn-rs's loopback default.
    # One config line would undo that, and the result would look exactly like a
    # working deployment while anything in the VPC could enumerate and tear down
    # relayed media, including a Cuttlefish host running arbitrary Android
    # workloads. The plan refuses it so nobody has to discover it in production.
    precondition {
      condition = alltrue([
        for addr in local.api_listens :
        startswith(addr, "127.0.0.1:") || startswith(addr, "localhost:") || startswith(addr, "[::1]:")
      ])
      error_message = "The relay's control API must stay on loopback: it is unauthenticated and can destroy sessions, so binding it to the network would let anything in the VPC tear down relayed media."
    }

    # Otherwise the failure surfaces as a certificate that never arrives, on a
    # VM, hours later.
    precondition {
      condition     = !local.turns_enabled || var.acme_email != ""
      error_message = "acme_email is required when turns_domain is set: the issuer will not open an account without a contact address."
    }
  }
}
