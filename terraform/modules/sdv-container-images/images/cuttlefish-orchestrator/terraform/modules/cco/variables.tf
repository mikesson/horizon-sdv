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
# Deployment topology (three independent settings)
# =============================================================================
#
# ingress:        CCO reachability (Cloud Run ingress).
# auth:           front-door mechanism.
# device_ingress: where WebRTC device media meets the internet.
#
# Constraints (enforced by preconditions in main.tf):
#   auth = "network"           => ingress = "private"
#   device_ingress = "public"  => ingress = "public"
#   device_ingress = "private" => client_cidrs must be non-empty

variable "ingress" {
  type        = string
  nullable    = false
  default     = "public"
  description = <<-EOT
    CCO reachability (Cloud Run ingress):
    - "public": reachable from the internet (INGRESS_TRAFFIC_ALL).
    - "private": internal ingress only (INGRESS_TRAFFIC_INTERNAL_ONLY).
    Built-in Cloud Run IAP works with either value.
  EOT

  validation {
    condition     = contains(["public", "private"], var.ingress)
    error_message = "ingress must be 'public' or 'private'."
  }
}

variable "auth" {
  type        = string
  nullable    = false
  default     = "iap"
  description = <<-EOT
    Front-door authentication mechanism:
    - "iap": built-in Cloud Run IAP (works with either ingress value). The
      iap_members list is the entire access model.
    - "network": no authentication, so every caller is attributed to one
      synthetic principal. Requires ingress = "private", and VPC reachability
      (firewalls, VPN membership) is the entire access model. The cell for
      legacy clients that carry no credential (OmniLab ATS), mirroring
      upstream Cloud Orchestrator's username-only deployment. Also provisions
      an internal Application Load Balancer fronting the service; point such
      clients at internal_lb_url, never at the run.app URL (Google's front
      end rejects their HTTP/1.1 requests with 411).
  EOT

  validation {
    condition     = contains(["iap", "network"], var.auth)
    error_message = "auth must be 'iap' or 'network'."
  }
}

variable "device_ingress" {
  type        = string
  nullable    = false
  default     = "public"
  description = <<-EOT
    Where WebRTC device media meets the internet:
    - "public": host VMs get external IPs; media flows host -> client directly
      (TURN optional). Requires ingress = "public".
    - "private": host VMs stay internal-only; clients reach media over a VPN or
      private network (TURN unnecessary). Requires client_cidrs.
    - "relay": host VMs stay internal-only; media is relayed through a TURN
      server. TURN is required, checked at app startup; CCO does not ship a
      TURN deployment, so supply your own via turn_servers/turn_shared_secret.
  EOT

  validation {
    condition     = contains(["public", "private", "relay"], var.device_ingress)
    error_message = "device_ingress must be 'public', 'private', or 'relay'."
  }
}

variable "client_cidrs" {
  type        = list(string)
  nullable    = false
  default     = []
  description = <<-EOT
    CIDR ranges permitted to reach device WebRTC media directly:
    - device_ingress = "private": REQUIRED (non-empty). The VPN or private
      ranges that clients connect from.
    - device_ingress = "public": optional tightening of the host firewall;
      empty means 0.0.0.0/0 (open to the internet).
    - device_ingress = "relay": unused (media is relayed through TURN).
  EOT
}

variable "iap_members" {
  type        = set(string)
  nullable    = false
  default     = []
  description = <<-EOT
    Principals granted roles/iap.httpsResourceAccessor on the Cloud Run
    service (auth = "iap"). This list IS the entire access model:
    anyone on it has full access to everything; anyone not on it cannot
    reach the app. There are no roles.

    Examples: user:alice@example.com, group:team@example.com,
    serviceAccount:ci@PROJECT.iam.gserviceaccount.com
  EOT
}

variable "cli_users" {
  type        = set(string)
  nullable    = false
  default     = []
  description = <<-EOT
    Principals (user:…, group:…) who use cfctl through a managed CLI
    signing identity. Non-empty creates a "<deployment>-cli" service
    account, puts it on the deployment's IAP access list, and grants each
    listed principal roles/iam.serviceAccountTokenCreator on it (the one
    grant impersonation needs) plus roles/iap.tunnelResourceAccessor for
    `cfctl host ssh`. Revoke a user by removing them here: their
    tokenCreator grant goes away while the SA stays for everyone else.
    Empty (the default) creates nothing; hand-managed identities keep
    working unchanged.

    Examples: user:alice@example.com, group:android-team@example.com
  EOT
}

variable "deployers" {
  type        = set(string)
  nullable    = false
  default     = []
  description = <<-EOT
    Principals (user:…, group:…, serviceAccount:…) who build images with
    scripts/build-image.sh. Each gets roles/cloudbuild.builds.editor on the
    project, roles/iam.serviceAccountUser on the builder service account
    the builds run as, and object admin plus legacy bucket reader on the
    build staging bucket.

    Examples: user:alice@example.com, group:release-team@example.com
  EOT
}

variable "iap_oauth_client_id" {
  type        = string
  nullable    = false
  default     = ""
  description = <<-EOT
    Custom IAP OAuth 2.0 client ID for USER / browser-interactive programmatic
    access in auth = "iap". NOT needed for service accounts: an SA (cfctl
    / CI) authenticates with a self-signed JWT (aud = service URL) that works
    with the Google-managed client, as long as the SA is on iap_members. Leave
    this empty in that case.

    The Google-managed IAP client cannot be used for the user OAuth flow, and
    the IAP OAuth Admin API (google_iap_client) was permanently shut down in
    March 2026, so when you do need one, it must be created ONCE BY HAND:

      Cloud console > APIs & Services > Credentials > Create OAuth client ID >
      Web application, then add the authorized redirect URI
      https://iap.googleapis.com/v1/oauth/clientIds/CLIENT_ID:handleRedirect

    Paste the resulting client ID here. Terraform then allowlists it for
    programmatic IAP access via google_iap_settings.programmatic_clients, and
    cfctl uses it as its OIDC token audience (terraform output cfctl_audience).

    The browser SPA flow keeps using the Google-managed client either way.
  EOT
}

# =============================================================================
# Required Variables
# =============================================================================

variable "project_id" {
  type        = string
  description = "GCP project ID"
}

variable "deployment" {
  type        = string
  nullable    = false
  default     = "cco"
  description = <<-EOT
    Short name of this install, unique within the project. It is passed to the
    orchestrator as CCO_DEPLOYMENT and stamped on every host VM and boot disk
    as the cco-deployment label, so two installs in one project never treat
    each other's hosts as orphans. It also names the host and CLI service
    accounts ("<deployment>-host", "<deployment>-cli") and the artifacts
    bucket ("<project_id>-<deployment>-artifacts"). A second install in the
    same project sets a different value, plus its own network_name,
    firewall_rule_names, artifact_repository_id, service_name and
    spanner_instance_name.
  EOT

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,9}$", var.deployment))
    error_message = "deployment must start with a lowercase letter and hold at most 10 lowercase letters, digits or hyphens."
  }
}

# =============================================================================
# Regional Configuration
# =============================================================================

variable "region" {
  type        = string
  nullable    = false
  description = "Primary region for Cloud Run and resources"
}

# =============================================================================
# Cloud Run Configuration
# =============================================================================

variable "service_name" {
  type        = string
  nullable    = false
  default     = "cuttlefish-orchestrator"
  description = "Name of the Cloud Run service"
}

variable "image" {
  type        = string
  nullable    = false
  default     = ""
  description = "Container image URL, in this deployment's Artifact Registry repository. Empty keeps the image the service runs now. A first apply, before any service exists, passes the bootstrap placeholder us-docker.pkg.dev/cloudrun/container/hello."
}

variable "artifact_repository_id" {
  type        = string
  nullable    = false
  default     = "cuttlefish-orchestrator"
  description = "Artifact Registry repository that holds the orchestrator images"
}

variable "cloud_run_cpu" {
  type        = number
  nullable    = false
  default     = 4
  description = "Number of CPUs for Cloud Run instances"
}

variable "cloud_run_memory" {
  type        = string
  nullable    = false
  default     = "8Gi"
  description = "Memory for Cloud Run instances"
}

variable "cloud_run_min_instances" {
  type        = number
  nullable    = false
  default     = 0
  description = "Minimum number of Cloud Run instances"
}

variable "cloud_run_max_instances" {
  type        = number
  nullable    = false
  default     = 40
  description = <<-EOT
    Maximum number of Cloud Run instances. It is half of the concurrency
    ceiling for asynchronous work: each instance runs its own task-worker pool,
    a CVD create holds one of those workers for the whole remote launch, and so
    the fleet's sustained create rate is instances x workers / hold. Forty
    against the pool's 64 workers and a ~100 s hold carries the sustained
    design point with headroom, and stays well inside Cloud Run's own default
    ceiling of 100.
  EOT
}

variable "cloud_run_concurrency" {
  type        = number
  nullable    = false
  default     = 1000
  description = "Maximum concurrent requests per instance"
}

# =============================================================================
# Networking
# =============================================================================

variable "network_name" {
  type        = string
  nullable    = false
  default     = "cuttlefish-network"
  description = "VPC network name"
}

variable "subnet_name" {
  type        = string
  nullable    = false
  default     = "cuttlefish-subnet"
  description = "Subnet name (used when create_network = false)"
}

variable "subnet_cidr" {
  type        = string
  nullable    = false
  default     = "10.0.0.0/16"
  description = "CIDR range for the subnet"
}

variable "proxy_subnet_cidr" {
  type        = string
  nullable    = false
  default     = "10.129.0.0/23"
  description = <<-EOT
    CIDR for the proxy-only subnet the internal load balancer's Envoy proxies
    occupy (created in network mode only). Must not overlap subnet_cidr.
  EOT
}

variable "create_network" {
  type        = bool
  nullable    = false
  default     = true
  description = "Whether to create a new VPC network or use existing"
}

variable "firewall_rule_names" {
  type = object({
    host    = optional(string, "allow-cuttlefish-host")
    iap_ssh = optional(string, "allow-iap-ssh")
    webrtc  = optional(string, "allow-cuttlefish-webrtc")
  })
  nullable    = false
  default     = {}
  description = "Names of the firewall rules created with the network. Firewall rule names are unique per project, so a second install in the same project sets its own."
}

# =============================================================================
# Cloud Spanner
# =============================================================================

variable "create_spanner" {
  type        = bool
  nullable    = false
  default     = true
  description = "Whether to create Cloud Spanner instance"
}

variable "spanner_instance_name" {
  type        = string
  nullable    = false
  default     = "cuttlefish-orchestrator"
  description = "Cloud Spanner instance name"
}

variable "spanner_database_name" {
  type        = string
  nullable    = false
  default     = "orchestrator"
  description = "Cloud Spanner database name"
}

variable "spanner_deletion_protection" {
  type        = bool
  nullable    = false
  default     = true
  description = "Terraform's deletion guard on the Spanner database. The provider reads it from state, so a teardown applies false first, then runs destroy."
}

variable "spanner_processing_units" {
  type        = number
  nullable    = false
  default     = 100
  description = "Cloud Spanner processing units (minimum 100)"
}

variable "seed_database" {
  type        = bool
  nullable    = false
  default     = true
  description = <<-EOT
    Apply complete_schema.sql to seed the database with tables and initial data.

    This runs scripts/provision/spanner-seed.sh which:
    1. Creates all tables and indexes (DDL)
    2. Inserts seed data: global quota, machine_types, host_image_configs (DML)

    The script is idempotent, so existing tables and data are skipped. It runs
    when the database is created and not on later schema edits. Applying a
    schema change to a live database is an explicit DDL step, and a re-run
    would restore seed rows an operator had deleted.
    Set to false if managing schema separately or using existing database.
  EOT
}

# =============================================================================
# Labels
# =============================================================================

variable "labels" {
  type     = map(string)
  nullable = false
  default = {
    app       = "cuttlefish-orchestrator"
    component = "orchestrator"
  }
  description = "Labels to apply to resources"
}

variable "host_service_account" {
  type        = string
  nullable    = false
  default     = ""
  description = "Service account email for host VMs (grant it GCS read on the build-artifact bucket). Empty means VMs launch without a service account. Setting it changes how cvd fetches on NEW hosts, so pair it with a host image whose cvd handles ambient credentials and URL builds."
}

variable "host_image_project" {
  type        = string
  nullable    = false
  default     = ""
  description = "GCP project the host image families (such as cco-host) are read from. Empty means project_id. A different project gets the orchestrator service account roles/compute.imageUser there, which works only where the operator applying Terraform holds IAM in that project. A host image config row's source_project overrides it for one machine family."
}

variable "create_host_service_account" {
  type        = bool
  nullable    = false
  default     = true
  description = "Create the <deployment>-host service account and grant it read on the artifacts bucket. Inert until host_service_account references it."
}

variable "create_artifacts_bucket" {
  type        = bool
  nullable    = false
  default     = true
  description = "Create the <project_id>-<deployment>-artifacts bucket for device artifacts (bugreports, screen recordings) and grant the orchestrator object read/write on it."
}

variable "artifacts_bucket" {
  type        = string
  nullable    = false
  default     = ""
  description = "Existing bucket for device artifacts when create_artifacts_bucket = false (the operator grants the orchestrator SA objectAdmin on it). Empty with create_artifacts_bucket = false leaves artifact capture unconfigured until an admin sets buckets in the console."
}

variable "artifacts_retention_days" {
  type        = number
  nullable    = false
  default     = 30
  description = "Days device artifacts are retained before the bucket lifecycle rule deletes them."
}

variable "staging_retention_days" {
  type        = number
  nullable    = false
  default     = 180
  description = "Days staged build images (staging/ prefix, pushed local builds) are retained. Longer than artifacts, because a staged image backs registered builds and pinned devices."
}

variable "turn_servers" {
  type        = list(string)
  nullable    = false
  default     = []
  description = "TURN server URIs advertised to WebRTC clients (e.g. turn:IP:3478, turns:IP:5349). Set after deploying your TURN server; used when device_ingress = \"relay\"."
}

variable "turn_shared_secret" {
  type        = string
  nullable    = false
  default     = ""
  sensitive   = true
  description = "TURN REST API shared secret used to mint ephemeral credentials, mounted into the app as a Secret Manager volume. Required when device_ingress = \"relay\" or relay_image is set: the service mounts the secret's latest version, so an empty value is refused at plan time."
}

variable "turn_realm" {
  type        = string
  nullable    = false
  default     = "cco"
  description = <<-EOT
    TURN realm. It is part of the long-term key, so the orchestrator and the
    relay derive from the same value or every credential is rejected. Both
    sides are set from here.
  EOT
}

variable "relay_image" {
  type        = string
  nullable    = false
  default     = ""
  description = <<-EOT
    Relay image built by deployment/packer/relay-image.pkr.hcl. Required to
    stand up a relay in relay mode; empty means the operator runs their own
    TURN server and supplies turn_servers instead.
  EOT
}

variable "relay_instance_count" {
  type        = number
  nullable    = false
  default     = 2
  description = "Number of relay instances when the relay is managed here."
}

variable "relay_machine_type" {
  type        = string
  nullable    = false
  default     = "e2-medium"
  description = "Machine type for relay instances. Relay throughput is NIC-bound, not CPU-bound."
}

variable "relay_zones" {
  type        = list(string)
  nullable    = false
  default     = []
  description = "Zones for relay instances. Empty uses the region's -a and -b zones."
}

variable "turns_domain" {
  type        = string
  nullable    = false
  default     = ""
  description = <<-EOT
    DNS name clients reach TURNS at, pointed at a relay address. Without it the
    relay serves plain TURN only, which fails for exactly the clients relay mode
    exists to serve, namely enterprise networks that permit nothing but 443.
  EOT
}

variable "acme_email" {
  type        = string
  nullable    = false
  default     = ""
  description = "Contact address for the ACME account issuing the TURNS certificate."
}

variable "acme_eab_kid" {
  type        = string
  nullable    = false
  default     = ""
  description = "External account binding key ID. Google Trust Services requires one."
}

variable "acme_eab_hmac_secret" {
  type        = string
  nullable    = false
  default     = ""
  description = <<-EOT
    Secret Manager secret ID holding the external account binding HMAC key.
    The ID, not the key. The relay account is granted access to this one secret
    and nothing else.
  EOT
}

variable "spot_advice" {
  type        = bool
  nullable    = false
  default     = false
  description = "Enable the Spot capacity-advice sync (beta GCE advice.capacity API, probed per enabled zone × machine type). Spot placement consumes the advice as an uptime filter and obtainability ranking term; off, it degrades to quota/health signals."
}

variable "trace_sample_rate" {
  type        = number
  nullable    = false
  default     = 0.1
  description = "OTLP trace sampling rate (0.0-1.0). ParentBased: only root spans pay this rate. Default 0.1 bounds trace volume/cost in production; raise toward 1.0 for debugging."

  validation {
    condition     = var.trace_sample_rate >= 0 && var.trace_sample_rate <= 1
    error_message = "trace_sample_rate must be between 0.0 and 1.0."
  }
}
