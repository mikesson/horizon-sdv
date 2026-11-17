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

# Cloud Cuttlefish Orchestrator - Single-Tenant Cloud Run Deployment
#
# The orchestrator runs as a single Cloud Run service, shaped by three
# independent settings:
# - ingress ("public"/"private"): Cloud Run reachability. Built-in IAP works
#   with either value.
# - auth ("iap"/"network"): front-door mechanism. "iap" uses built-in Cloud
#   Run IAP (iap_members is the entire access model); "network" authenticates
#   nothing (requires ingress = "private", and VPC reachability is the entire
#   access model).
# - device_ingress ("public"/"private"/"relay"): where WebRTC device media
#   meets the internet.
#
# There is no Load Balancer, no GCIP/Identity Platform, and no blocking
# function.

# The caller configures the providers. This module declares only their
# version constraints (versions.tf), so it can be called with count, for_each
# or depends_on.

# =============================================================================
# Data Sources
# =============================================================================

data "google_project" "project" {}

locals {
  project_number = data.google_project.project.number

  # Terraform manages the container image so image + env deploy atomically in one
  # revision. Deploy = `scripts/build-image.sh` then `terraform apply -var
  # image=<digest>`. An empty image keeps the image the service runs now, so a
  # plain apply never swaps it out. A first apply has no service and no image
  # in the repository yet, so it passes bootstrap_image explicitly.
  image = var.image != "" ? var.image : data.google_cloud_run_v2_service.running[0].template[0].containers[0].image

  bootstrap_image = "us-docker.pkg.dev/cloudrun/container/hello"

  # Built from variables, never from the repository resource: on a fresh apply
  # its attributes are unknown, and a precondition reading them is deferred to
  # apply instead of failing the plan.
  image_repository_id = var.artifact_repository_id
  image_prefix        = "${var.region}-docker.pkg.dev/${var.project_id}/${local.image_repository_id}/"

  # The project host image families resolve in (CCO_HOST_IMAGE_PROJECT).
  # Defaults to the deployment's own project.
  host_image_project = coalesce(var.host_image_project, var.project_id)

  use_iap = var.auth == "iap"

  # Network mode fronts the service with an internal Application Load Balancer
  # so credential-less legacy clients can reach it at all. See the Internal
  # Application Load Balancer section for why run.app cannot serve them.
  internal_lb_enabled = var.auth == "network"

  # The IAP JWT audience for a direct Cloud Run service.
  iap_audience = "/projects/${local.project_number}/locations/${var.region}/services/${var.service_name}"

  # A relay is stood up wherever an image is supplied. Relay mode requires one,
  # but TURN is orthogonal to where media meets the internet. A public
  # deployment gains nothing from direct host addressing when the client's own
  # network blocks UDP, and today such a client has no path at all. Running a
  # relay beside public media is therefore a supported configuration, not a
  # fourth mode.
  relay_enabled = var.relay_image != ""

  # The shared secret is what the orchestrator derives relay keys from, so it
  # is managed wherever there is a relay to authorize for.
  turn_secret_managed = var.device_ingress == "relay" || var.turn_shared_secret != "" || var.relay_image != ""

  # Required APIs. cloudtrace + monitoring are the storage backends behind the
  # OTLP ingestion endpoint (telemetry.googleapis.com): if either is disabled,
  # the Telemetry API silently discards that signal's data. They must stay
  # enabled. iam, iamcredentials, storage and logging are on in most projects,
  # but a project from an organization's project factory may have them off.
  apis = toset([
    "artifactregistry.googleapis.com",
    "cloudbuild.googleapis.com",
    "cloudtrace.googleapis.com",
    "compute.googleapis.com",
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "iap.googleapis.com",
    "logging.googleapis.com",
    "monitoring.googleapis.com",
    "run.googleapis.com",
    "secretmanager.googleapis.com",
    "spanner.googleapis.com",
    "storage.googleapis.com",
    "telemetry.googleapis.com",
  ])
}

# The image the service runs now, read only when no image is passed. With no
# service either (a first apply without -var image), the read fails the plan.
data "google_cloud_run_v2_service" "running" {
  count    = var.image == "" ? 1 : 0
  name     = var.service_name
  location = var.region
}

# =============================================================================
# Enable Required APIs
# =============================================================================

resource "google_project_service" "apis" {
  for_each           = local.apis
  service            = each.key
  disable_on_destroy = false
}

# =============================================================================
# Service Account
# =============================================================================

resource "google_service_account" "orchestrator" {
  account_id   = var.service_name
  display_name = "Cloud Cuttlefish Orchestrator Service Account"
  depends_on   = [google_project_service.apis]
}

# Compute Admin for creating host VMs
resource "google_project_iam_member" "compute_admin" {
  project = var.project_id
  role    = "roles/compute.admin"
  member  = google_service_account.orchestrator.member
}

# Service Account User for attaching SAs to VMs
resource "google_project_iam_member" "sa_user" {
  project = var.project_id
  role    = "roles/iam.serviceAccountUser"
  member  = google_service_account.orchestrator.member
}

# Reading and booting host images that live in another project. Inside the
# deployment's own project compute.admin above already covers it.
resource "google_project_iam_member" "host_image_user" {
  count   = local.host_image_project != var.project_id ? 1 : 0
  project = local.host_image_project
  role    = "roles/compute.imageUser"
  member  = google_service_account.orchestrator.member
}

# Export OpenTelemetry traces and metrics via OTLP to telemetry.googleapis.com
# (the native ingestion endpoint). Per the Telemetry API docs, writes need
# roles/telemetry.tracesWriter for traces and roles/monitoring.metricWriter
# for metrics; without them the OTLP exporters log PermissionDenied per flush.
resource "google_project_iam_member" "telemetry_traces_writer" {
  project = var.project_id
  role    = "roles/telemetry.tracesWriter"
  member  = google_service_account.orchestrator.member
}

resource "google_project_iam_member" "monitoring_metric_writer" {
  project = var.project_id
  role    = "roles/monitoring.metricWriter"
  member  = google_service_account.orchestrator.member
}

# Placement reads the project's own quota telemetry for the axes Compute
# Engine's region list cannot report: CPU quota per VM family, which every
# machine family newer than c3 draws from, and the hyperdisk capacity those
# families' boot disks fill. That needs monitoring.timeSeries.list, which
# metricWriter above does not carry. Without it the reads fail, the axes stay
# unknown, and placement passes them advisorily. It degrades instead of
# breaking, which is exactly why a missing grant would go unnoticed.
resource "google_project_iam_member" "monitoring_viewer" {
  project = var.project_id
  role    = "roles/monitoring.viewer"
  member  = google_service_account.orchestrator.member
}

# The runtime sets GOOGLE_CLOUD_QUOTA_PROJECT (for the Telemetry API), which
# ADC also attaches as x-goog-user-project on every Google API call. GCS then
# requires serviceusage.services.use on the quota project or rejects reads and
# writes with 403. This grant satisfies that; without it the storage feature
# (artifact capture, the GCS browser) cannot reach any bucket.
resource "google_project_iam_member" "service_usage_consumer" {
  project = var.project_id
  role    = "roles/serviceusage.serviceUsageConsumer"
  member  = google_service_account.orchestrator.member
}

# Host VMs authenticate to GCS with their own identity, never the
# orchestrator's. This SA is what a host's cvd uses to fetch staged build
# images (gs:// build sources). Creating it and granting read here has no
# effect on its own. Hosts only carry it once var.host_service_account names
# it, and that must wait for a host image whose cvd understands URL builds,
# because attaching any SA also flips cvd's Build API fetch path onto ambient
# credentials.
resource "google_service_account" "host" {
  count        = var.create_host_service_account ? 1 : 0
  account_id   = "${var.deployment}-host"
  display_name = "Cuttlefish host VMs"
}

resource "google_storage_bucket_iam_member" "host_staging_object_viewer" {
  count = var.create_host_service_account && var.create_artifacts_bucket ? 1 : 0

  bucket = google_storage_bucket.artifacts[0].name
  role   = "roles/storage.objectViewer"
  member = google_service_account.host[0].member
}

# The console's storage browser lists the project's buckets and reads objects
# across them (build artifacts, host images). Without buckets.list the
# GET /v2/storage/buckets endpoint 403s. bucketViewer carries exactly
# storage.buckets.{get,list}; objectViewer adds project-wide read-only object
# access so listed buckets can actually be browsed. Writes stay scoped to the
# artifacts bucket (objectAdmin below).
resource "google_project_iam_member" "storage_bucket_viewer" {
  project = var.project_id
  role    = "roles/storage.bucketViewer"
  member  = google_service_account.orchestrator.member
}

resource "google_project_iam_member" "storage_object_viewer" {
  project = var.project_id
  role    = "roles/storage.objectViewer"
  member  = google_service_account.orchestrator.member
}

# =============================================================================
# Device artifacts bucket (bugreports, screen recordings)
# =============================================================================
# The deployment default the artifacts.* admin settings fall back to. The
# lifecycle rule is the retention policy. Artifacts are triage material, not
# records. force_destroy so a teardown does not strand the deployment on
# leftover artifacts.

resource "google_storage_bucket" "artifacts" {
  count = var.create_artifacts_bucket ? 1 : 0

  name                        = "${var.project_id}-${var.deployment}-artifacts"
  location                    = var.region
  uniform_bucket_level_access = true
  force_destroy               = true

  # Captures (bugreports, recordings) live under artifacts/ and expire on
  # the short clock; pushed workstation images live under staging/ and get
  # their own retention, because a build backing a pinned device must not
  # vanish under it.
  lifecycle_rule {
    condition {
      age            = var.artifacts_retention_days
      matches_prefix = ["artifacts/"]
    }
    action {
      type = "Delete"
    }
  }

  lifecycle_rule {
    condition {
      age            = var.staging_retention_days
      matches_prefix = ["staging/"]
    }
    action {
      type = "Delete"
    }
  }

  depends_on = [google_project_service.apis]
}

# The orchestrator streams artifacts in (capture) and out (downloads, admin
# write-probe); objectAdmin covers create/read/delete, scoped to this bucket.
resource "google_storage_bucket_iam_member" "artifacts_object_admin" {
  count = var.create_artifacts_bucket ? 1 : 0

  bucket = google_storage_bucket.artifacts[0].name
  role   = "roles/storage.objectAdmin"
  member = google_service_account.orchestrator.member
}

# =============================================================================
# Artifact Registry
# =============================================================================

# Deploys pin a digest, and a pushed tag can never be moved onto another build.
resource "google_artifact_registry_repository" "repo" {
  location      = var.region
  repository_id = local.image_repository_id
  format        = "DOCKER"
  labels        = var.labels
  depends_on    = [google_project_service.apis]

  docker_config {
    immutable_tags = true
  }
}

# =============================================================================
# Image builds
# =============================================================================
# scripts/build-image.sh runs Cloud Build as this account, in var.region, with
# the source staged in the bucket below. Builds never run as the Compute Engine
# default service account, which an organization may have deleted or barred
# from grants. The three grants below are everything a build needs.

resource "google_service_account" "builder" {
  account_id   = "${var.deployment}-builder"
  display_name = "CCO image builder"
  depends_on   = [google_project_service.apis]
}

resource "google_artifact_registry_repository_iam_member" "builder_writer" {
  location   = google_artifact_registry_repository.repo.location
  repository = google_artifact_registry_repository.repo.name
  role       = "roles/artifactregistry.writer"
  member     = google_service_account.builder.member
}

# Log writing has no scope narrower than the project. A build that runs as a
# user-chosen account must log to Cloud Logging only, which cloudbuild.yaml
# sets.
resource "google_project_iam_member" "builder_log_writer" {
  project = var.project_id
  role    = "roles/logging.logWriter"
  member  = google_service_account.builder.member
}

# Regional, so a resource-location policy cannot block it. The staging bucket
# gcloud creates on its own is US multi-region.
resource "google_storage_bucket" "build_staging" {
  name                        = "${var.project_id}-${var.deployment}-build-staging"
  location                    = var.region
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = true
  labels                      = var.labels

  lifecycle_rule {
    condition {
      age = 7
    }
    action {
      type = "Delete"
    }
  }

  depends_on = [google_project_service.apis]
}

resource "google_storage_bucket_iam_member" "builder_staging_viewer" {
  bucket = google_storage_bucket.build_staging.name
  role   = "roles/storage.objectViewer"
  member = google_service_account.builder.member
}

# Deployers submit builds that run as the builder. gcloud reads the staging
# bucket's metadata before it uploads the source, so object admin alone is
# not enough and the legacy bucket reader role sits beside it.
resource "google_service_account_iam_member" "deployers_builder_user" {
  for_each           = var.deployers
  service_account_id = google_service_account.builder.name
  role               = "roles/iam.serviceAccountUser"
  member             = each.value
}

resource "google_project_iam_member" "deployers_build_editor" {
  for_each = var.deployers
  project  = var.project_id
  role     = "roles/cloudbuild.builds.editor"
  member   = each.value
}

resource "google_storage_bucket_iam_member" "deployers_staging_object_admin" {
  for_each = var.deployers
  bucket   = google_storage_bucket.build_staging.name
  role     = "roles/storage.objectAdmin"
  member   = each.value
}

resource "google_storage_bucket_iam_member" "deployers_staging_bucket_reader" {
  for_each = var.deployers
  bucket   = google_storage_bucket.build_staging.name
  role     = "roles/storage.legacyBucketReader"
  member   = each.value
}

# =============================================================================
# VPC Network
# =============================================================================

resource "google_compute_network" "network" {
  count                   = var.create_network ? 1 : 0
  name                    = var.network_name
  auto_create_subnetworks = false
  depends_on              = [google_project_service.apis]
}

resource "google_compute_subnetwork" "subnet" {
  count         = var.create_network ? 1 : 0
  name          = "${var.network_name}-subnet"
  ip_cidr_range = var.subnet_cidr
  region        = var.region
  network       = google_compute_network.network[0].id

  private_ip_google_access = true
}

# Firewall rules for Cuttlefish hosts
resource "google_compute_firewall" "cuttlefish" {
  count   = var.create_network ? 1 : 0
  name    = var.firewall_rule_names.host
  network = google_compute_network.network[0].name

  allow {
    protocol = "tcp"
    # 1080 = operator (WS signaling, proxied by the backend); 2080 = nginx →
    # Host Orchestrator; 6520 = adb. Nothing listens on 8443 on the cco-host
    # image (the operator's TLS port is 1443); it was removed 2026-07-08.
    #
    # 6520 is the FIRST instance only. A CVD group takes 6520, 6521, ... one per
    # Android VM, and this rule is not widened to the range. The supported way
    # to reach any instance is adb-over-WebSocket through the operator on 1080,
    # which the backend already proxies. That path works for every instance,
    # needs no VPC route, and is the only path that exists at all when
    # hosts have no public address. Widening to 6520-6527 would open eight
    # unauthenticated adb ports on every host to buy a second way to reach the
    # instances the first way already reaches. A per-instance adb_port on the
    # API is provenance, not a dialable target.
    ports = ["22", "1080", "2080", "6520"]
  }

  allow {
    protocol = "tcp"
    ports    = ["15550-15599"]
  }

  allow {
    protocol = "udp"
    ports    = ["15550-15599"]
  }

  source_ranges = ["10.0.0.0/8"]
  target_tags   = ["cuttlefish-host"]
}

# SSH reaches hosts only through IAP TCP forwarding. gcloud opens an HTTPS
# tunnel to Google's IAP frontend, which dials the VM's internal IP from this
# fixed Google range. Port 22 is never internet-reachable in any topology. This
# rule admits Google's tunnel service, and IAP authorizes each caller against
# roles/iap.tunnelResourceAccessor before forwarding a byte.
resource "google_compute_firewall" "iap_ssh" {
  count   = var.create_network ? 1 : 0
  name    = var.firewall_rule_names.iap_ssh
  network = google_compute_network.network[0].name

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }

  source_ranges = ["35.235.240.0/20"]
  target_tags   = ["cuttlefish-host"]
}

# Client → host WebRTC media when it meets the internet at the host.
# - device_ingress = "public": open the media ports to client_cidrs, or to
#   0.0.0.0/0 when client_cidrs is empty.
# - device_ingress = "private": open the media ports to client_cidrs only
#   (the VPN or private ranges clients connect from).
# - device_ingress = "relay": no client-facing rule. Media is relayed through
#   TURN, and the TURN module owns its own firewall rules.
resource "google_compute_firewall" "cuttlefish_webrtc" {
  count   = var.create_network && var.device_ingress != "relay" ? 1 : 0
  name    = var.firewall_rule_names.webrtc
  network = google_compute_network.network[0].name

  allow {
    protocol = "tcp"
    ports    = ["15550-15599"]
  }

  allow {
    protocol = "udp"
    ports    = ["15550-15599"]
  }

  source_ranges = var.device_ingress == "private" ? var.client_cidrs : (length(var.client_cidrs) > 0 ? var.client_cidrs : ["0.0.0.0/0"])
  target_tags   = ["cuttlefish-webrtc"]
}

# =============================================================================
# Internal Application Load Balancer (network mode)
# =============================================================================
# Network mode exists for legacy clients that carry no credential (OmniLab
# ATS). Their client library speaks HTTP/1.1 only and sends its empty POSTs
# (every operation `:wait`) without a Content-Length header. Google's front
# end, which terminates every run.app request, rejects those with 411 before
# the service sees them; no server-side code can change that. This Envoy-based
# load balancer accepts them (a bodyless request without Content-Length is
# zero-length per RFC 7230), so such clients must be pointed at
# internal_lb_url, never at the run.app URL. Proven live 2026-08-05 with a
# full OmniLab ATS test run.

# Envoy proxies for every internal ALB in this network+region live in this
# subnet. GCE allows one ACTIVE proxy-only subnet per network+region.
resource "google_compute_subnetwork" "proxy_only" {
  count         = local.internal_lb_enabled ? 1 : 0
  name          = "${var.network_name}-proxy-subnet"
  ip_cidr_range = var.proxy_subnet_cidr
  region        = var.region
  network       = var.create_network ? google_compute_network.network[0].id : var.network_name
  purpose       = "REGIONAL_MANAGED_PROXY"
  role          = "ACTIVE"
}

resource "google_compute_region_network_endpoint_group" "orchestrator" {
  count                 = local.internal_lb_enabled ? 1 : 0
  name                  = "${var.service_name}-neg"
  region                = var.region
  network_endpoint_type = "SERVERLESS"

  cloud_run {
    service = google_cloud_run_v2_service.orchestrator.name
  }
}

resource "google_compute_region_backend_service" "internal_lb" {
  count                 = local.internal_lb_enabled ? 1 : 0
  name                  = "${var.service_name}-internal-lb"
  region                = var.region
  load_balancing_scheme = "INTERNAL_MANAGED"
  protocol              = "HTTPS"

  # timeout_sec must stay unset: the API rejects an explicit value for
  # serverless backends. Long holds (`:wait`, the ADB WebSocket) are governed
  # by Cloud Run's own request ceiling; Envoy may still cut a multi-minute
  # hold with a 504, which the legacy wait contract's reissue absorbs.
  backend {
    group           = google_compute_region_network_endpoint_group.orchestrator[0].id
    balancing_mode  = "UTILIZATION"
    capacity_scaler = 1.0
  }
}

resource "google_compute_region_url_map" "internal_lb" {
  count           = local.internal_lb_enabled ? 1 : 0
  name            = "${var.service_name}-internal-lb"
  region          = var.region
  default_service = google_compute_region_backend_service.internal_lb[0].id
}

# Plain HTTP on 80. The path from client to Envoy never leaves the VPC, which
# in network mode is the entire access model anyway.
resource "google_compute_region_target_http_proxy" "internal_lb" {
  count   = local.internal_lb_enabled ? 1 : 0
  name    = "${var.service_name}-internal-lb"
  region  = var.region
  url_map = google_compute_region_url_map.internal_lb[0].id
}

resource "google_compute_forwarding_rule" "internal_lb" {
  count                 = local.internal_lb_enabled ? 1 : 0
  name                  = "${var.service_name}-internal-lb"
  region                = var.region
  load_balancing_scheme = "INTERNAL_MANAGED"
  network               = var.create_network ? google_compute_network.network[0].id : var.network_name
  subnetwork            = var.create_network ? google_compute_subnetwork.subnet[0].id : var.subnet_name
  ip_protocol           = "TCP"
  port_range            = "80"
  target                = google_compute_region_target_http_proxy.internal_lb[0].id

  # The rule itself never references the proxy-only subnet, but Envoy cannot
  # program without one, and creation fails if it does not exist yet.
  depends_on = [google_compute_subnetwork.proxy_only]
}

# =============================================================================
# Cloud Spanner
# =============================================================================

resource "google_spanner_instance" "main" {
  count            = var.create_spanner ? 1 : 0
  name             = var.spanner_instance_name
  config           = "regional-${var.region}"
  display_name     = "Cuttlefish Orchestrator"
  processing_units = var.spanner_processing_units
  labels           = var.labels

  # Google's automatic daily backups block deleting the instance, and CCO
  # makes none of its own. force_destroy removes them on destroy, which can
  # only reach the instance after the database guard below is lifted.
  default_backup_schedule_type = "AUTOMATIC"
  force_destroy                = true

  depends_on = [google_project_service.apis]
}

resource "google_spanner_database" "main" {
  count               = var.create_spanner ? 1 : 0
  instance            = google_spanner_instance.main[0].name
  name                = var.spanner_database_name
  deletion_protection = var.spanner_deletion_protection
}

resource "google_spanner_database_iam_member" "orchestrator" {
  count    = var.create_spanner ? 1 : 0
  instance = google_spanner_instance.main[0].name
  database = google_spanner_database.main[0].name
  role     = "roles/spanner.databaseUser"
  member   = google_service_account.orchestrator.member
}

# =============================================================================
# Secret Manager
# =============================================================================
#
# There is no config-file secret. The app is configured entirely by CCO_* env
# vars (below) plus per-secret Secret Manager volumes (TURN).

# TURN shared secret (relay mode only). The app reads it from a mounted Secret
# Manager volume, so this secret stays out of the app config (env) and rotates
# without a redeploy. It must match the TURN server's own REST API secret.
# Supplied via var.turn_shared_secret, which a precondition on the service
# requires wherever the secret is mounted.
resource "google_secret_manager_secret" "turn_shared_secret" {
  count     = local.turn_secret_managed ? 1 : 0
  secret_id = "${var.service_name}-turn-shared-secret"
  replication {
    auto {}
  }
  labels     = var.labels
  depends_on = [google_project_service.apis]
}

resource "google_secret_manager_secret_version" "turn_shared_secret" {
  count       = local.turn_secret_managed && var.turn_shared_secret != "" ? 1 : 0
  secret      = google_secret_manager_secret.turn_shared_secret[0].id
  secret_data = var.turn_shared_secret
}

resource "google_secret_manager_secret_iam_member" "turn_shared_secret" {
  count     = local.turn_secret_managed ? 1 : 0
  secret_id = google_secret_manager_secret.turn_shared_secret[0].secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = google_service_account.orchestrator.member
}

# =============================================================================
# Cloud Run Service
# =============================================================================

resource "google_cloud_run_v2_service" "orchestrator" {
  name     = var.service_name
  location = var.region

  # ingress is independent of auth. Built-in IAP works with either value.
  # "public" = reachable from the internet; "private" = internal ingress only.
  ingress     = var.ingress == "public" ? "INGRESS_TRAFFIC_ALL" : "INGRESS_TRAFFIC_INTERNAL_ONLY"
  iap_enabled = local.use_iap

  # network mode disables Cloud Run's invoker IAM check entirely. That is the
  # platform's recommended shape for serving callers without Google
  # credentials, and it avoids the allUsers run.invoker grant that
  # domain-restricted-sharing org policies block. It makes the service public
  # at the platform level, which is why the precondition below forces private
  # ingress. The VPC boundary is then the entire access model.
  invoker_iam_disabled = var.auth == "network"

  lifecycle {
    precondition {
      condition     = var.auth != "network" || var.ingress == "private"
      error_message = "auth = \"network\" requires ingress = \"private\" (network mode has no credential gate; VPC reachability is the entire access model)."
    }

    precondition {
      condition     = var.device_ingress != "public" || var.ingress == "public"
      error_message = "device_ingress = \"public\" requires ingress = \"public\" (host media meets the internet, so CCO must too)."
    }

    precondition {
      condition     = var.device_ingress != "private" || length(var.client_cidrs) > 0
      error_message = "device_ingress = \"private\" requires a non-empty client_cidrs (the VPN / private ranges clients connect from)."
    }

    # The service mounts the latest version of the TURN secret, and without
    # turn_shared_secret no version exists, so the revision could not start.
    precondition {
      condition     = !local.turn_secret_managed || var.turn_shared_secret != ""
      error_message = "turn_shared_secret is required when device_ingress = \"relay\" or relay_image is set. The service mounts the secret's latest version, and with none the revision cannot start."
    }

    # An image from another project's registry needs a pull grant there,
    # which ties this deployment to that project.
    precondition {
      condition     = startswith(local.image, local.image_prefix) || local.image == local.bootstrap_image
      error_message = "image must be in this deployment's repository (${local.image_prefix}...), or be the bootstrap placeholder ${local.bootstrap_image} on a first apply."
    }
  }

  template {
    service_account = google_service_account.orchestrator.email

    # The platform closes a request at this bound, and a WebSocket is one long
    # request. At the 300s default every device stream, signaling and the ADB
    # bridge alike, is torn down after five minutes of use. An hour is the
    # maximum Cloud Run allows, and it is only an outer limit. Ordinary API
    # requests carry their own, much shorter, per-request deadlines from the
    # app, and streaming routes are the ones exempted from those, so raising
    # this bound lengthens the streams and nothing else.
    timeout = "3600s"

    scaling {
      min_instance_count = var.cloud_run_min_instances
      max_instance_count = var.cloud_run_max_instances
    }

    containers {
      image = local.image

      resources {
        limits = {
          cpu    = var.cloud_run_cpu
          memory = var.cloud_run_memory
        }
        cpu_idle          = false
        startup_cpu_boost = true
      }

      # The binary waits up to three minutes for its database schema and
      # grants before it listens. Cloud Run's default startup window is four
      # minutes, which leaves too little for a cold Spanner client on top, so
      # the probe allows five.
      startup_probe {
        tcp_socket {}
        period_seconds    = 10
        timeout_seconds   = 5
        failure_threshold = 30
      }

      # OTLP telemetry export to telemetry.googleapis.com. The quota project is
      # billed for Telemetry API usage; ADC reads it from this env (we send no
      # x-goog-user-project header, which would double-count). The sampler arg
      # bounds root-span trace volume (ParentBased); raise toward 1.0 for debug.
      env {
        name  = "GOOGLE_CLOUD_QUOTA_PROJECT"
        value = var.project_id
      }
      env {
        name  = "OTEL_TRACES_SAMPLER_ARG"
        value = tostring(var.trace_sample_rate)
      }

      # Deployment configuration. Every deployment-determined value ships as a
      # CCO_* env var, so Terraform is the single source of truth (there is no
      # config file). Operational tuning (timeouts, job intervals) stays in code
      # defaults; secrets are mounted Secret Manager volumes.
      env {
        name  = "CCO_DEVICE_INGRESS"
        value = var.device_ingress
      }
      env {
        name  = "CCO_DEPLOYMENT"
        value = var.deployment
      }
      env {
        name  = "CCO_AUTH_MODE"
        value = var.auth
      }
      env {
        name  = "CCO_IAP_AUDIENCE"
        value = local.use_iap ? local.iap_audience : ""
      }
      env {
        name  = "CCO_PROJECT_ID"
        value = var.project_id
      }
      env {
        name  = "CCO_SPANNER_PROJECT"
        value = var.project_id
      }
      env {
        name  = "CCO_SPANNER_INSTANCE"
        value = var.spanner_instance_name
      }
      env {
        name  = "CCO_SPANNER_DATABASE"
        value = var.spanner_database_name
      }
      env {
        name  = "CCO_NETWORK"
        value = var.create_network ? google_compute_network.network[0].name : var.network_name
      }
      env {
        name  = "CCO_SUBNETWORK"
        value = var.create_network ? google_compute_subnetwork.subnet[0].name : var.subnet_name
      }
      env {
        name  = "CCO_HOST_SERVICE_ACCOUNT"
        value = var.host_service_account
      }
      env {
        name  = "CCO_HOST_IMAGE_PROJECT"
        value = local.host_image_project
      }
      env {
        name  = "CCO_ARTIFACTS_BUCKET"
        value = var.create_artifacts_bucket ? google_storage_bucket.artifacts[0].name : var.artifacts_bucket
      }
      env {
        name  = "CCO_HOST_ORCHESTRATOR_PORT"
        value = "2080"
      }
      env {
        name  = "CCO_ALLOW_SELF_SIGNED_HOST_SSL_CERT"
        value = "true"
      }
      env {
        name = "CCO_TURN_SERVERS"
        # A relay stood up by this configuration advertises itself; anything
        # else the operator runs is appended.
        value = join(",", concat(
          local.relay_enabled ? module.relay[0].turn_servers : [],
          var.turn_servers,
        ))
      }
      env {
        name = "CCO_DEVICE_TURN_SERVERS"
        # The same relays by their internal addresses. A relay-mode host has no
        # external address and the VPC has no NAT, so this is the only way the
        # device leg reaches one. Only a managed relay has two addresses to
        # tell apart. An operator's own TURN server is advertised to both legs
        # through turn_servers above.
        value = join(",", local.relay_enabled ? module.relay[0].device_turn_servers : [])
      }
      # Part of the long-term key, so the orchestrator and the relay must agree
      # exactly; one variable feeds both. Set only where there is a TURN
      # deployment to agree with, since a realm without a relay configures
      # nothing.
      dynamic "env" {
        for_each = local.turn_secret_managed ? [1] : []
        content {
          name  = "CCO_TURN_REALM"
          value = var.turn_realm
        }
      }
      env {
        name  = "CCO_SPOT_ADVICE"
        value = var.spot_advice ? "true" : "false"
      }
      # Secrets are exposed as mounted Secret Manager VOLUMES, not env vars: a
      # volume read fetches the latest version, so rotating a secret is picked
      # up by a fresh instance without a redeploy (env-injected secrets resolve
      # once at startup). The app reads the file path named by each *_FILE var.
      dynamic "env" {
        for_each = local.turn_secret_managed ? [1] : []
        content {
          name  = "CCO_TURN_SHARED_SECRET_FILE"
          value = "/secrets/turn/shared-secret"
        }
      }

      dynamic "volume_mounts" {
        for_each = local.turn_secret_managed ? [1] : []
        content {
          name       = "turn-secret"
          mount_path = "/secrets/turn"
        }
      }
    }

    # Secret volumes fetch the latest version on read (rotation-friendly).
    dynamic "volumes" {
      for_each = local.turn_secret_managed ? [1] : []
      content {
        name = "turn-secret"
        secret {
          secret       = google_secret_manager_secret.turn_shared_secret[0].secret_id
          default_mode = 292 # 0444
          items {
            version = "latest"
            path    = "shared-secret"
          }
        }
      }
    }

    vpc_access {
      network_interfaces {
        network    = var.create_network ? google_compute_network.network[0].name : var.network_name
        subnetwork = var.create_network ? google_compute_subnetwork.subnet[0].name : var.subnet_name
      }
      egress = "PRIVATE_RANGES_ONLY"
    }

    max_instance_request_concurrency = var.cloud_run_concurrency
  }

  deletion_protection = false

  labels = var.labels

  # The env vars name the database by variable, so without these Terraform sees
  # no link to it and a first revision can start before the database, its
  # grant, its schema or the TURN secret's first version exists.
  depends_on = [
    google_project_service.apis,
    google_secret_manager_secret_iam_member.turn_shared_secret,
    google_secret_manager_secret_version.turn_shared_secret,
    google_spanner_database_iam_member.orchestrator,
    null_resource.database_seed,
    terraform_data.teardown,
  ]
}

# =============================================================================
# Teardown
# =============================================================================
#
# Host VMs are created by the orchestrator and are not in Terraform state.
# This resource deletes them on destroy. Cloud Run depends on it and it
# depends on the subnet, so destroy removes Cloud Run first (no new inserts),
# then this deployment's hosts, then the network.
#
# No triggers_replace: a changed input updates in place, so the provisioner
# runs only on destroy or an explicit -replace. A destroy-time provisioner can
# read only self, so every value it needs is in input.
resource "terraform_data" "teardown" {
  input = {
    script      = "${path.module}/../../../scripts/provision/teardown.sh"
    project     = var.project_id
    region      = var.region
    deployment  = var.deployment
    name_prefix = "cf-"
    subnet      = var.create_network ? google_compute_subnetwork.subnet[0].name : var.subnet_name
    # Cloud Run's serverless address is waited out only in a subnet this
    # module created, since only then does destroy go on to delete it.
    wait_for_subnet = var.create_network
  }

  provisioner "local-exec" {
    when       = destroy
    on_failure = fail
    command    = "${self.input.script} --project='${self.input.project}' --region='${self.input.region}' --deployment='${self.input.deployment}' --name-prefix='${self.input.name_prefix}' --subnet='${self.input.subnet}'${self.input.wait_for_subnet ? " --wait-for-subnet" : ""}"
  }

  depends_on = [google_project_service.apis]
}

# =============================================================================
# Access Control
# =============================================================================
#
# There is intentionally NO allUsers run.invoker binding: with built-in IAP
# the IAP service agent is the only principal that may invoke the service,
# otherwise callers could bypass IAP via the run.app URL.

# The IAP service agent must exist before it can be granted run.invoker.
resource "google_project_service_identity" "iap" {
  count    = local.use_iap ? 1 : 0
  provider = google-beta
  service  = "iap.googleapis.com"

  depends_on = [google_project_service.apis]
}

# Built-in IAP fronts the service; only the IAP service agent may invoke it.
resource "google_cloud_run_v2_service_iam_member" "iap_invoker" {
  count    = local.use_iap ? 1 : 0
  name     = google_cloud_run_v2_service.orchestrator.name
  location = google_cloud_run_v2_service.orchestrator.location
  role     = "roles/run.invoker"
  member   = google_project_service_identity.iap[0].member
}

# =============================================================================
# TURN relay
# =============================================================================
# Stood up only in relay mode. The module owns the relay's identity, firewall
# and certificate; this module supplies the network and the orchestrator URL
# the relay authorizes against.

module "relay" {
  count  = local.relay_enabled ? 1 : 0
  source = "../relay"

  project_id = var.project_id
  region     = var.region
  zones      = length(var.relay_zones) > 0 ? var.relay_zones : ["${var.region}-a", "${var.region}-b"]

  network    = var.create_network ? google_compute_network.network[0].id : var.network_name
  subnetwork = var.create_network ? google_compute_subnetwork.subnet[0].id : var.subnet_name

  orchestrator_url = google_cloud_run_v2_service.orchestrator.uri
  realm            = var.turn_realm

  image          = var.relay_image
  instance_count = var.relay_instance_count
  machine_type   = var.relay_machine_type

  turns_domain         = var.turns_domain
  acme_email           = var.acme_email
  acme_eab_kid         = var.acme_eab_kid
  acme_eab_hmac_secret = var.acme_eab_hmac_secret

  host_media_ports = "15550-15599"
  labels           = { managed-by = "cloud-cuttlefish-orchestrator" }
}

# The managed CLI signing identity. cfctl users impersonate this SA, which is
# what sits on the access list, while per-user access is the tokenCreator
# grant below. Created only when cli_users names anyone.
resource "google_service_account" "cli" {
  count        = length(var.cli_users) > 0 ? 1 : 0
  account_id   = "${var.deployment}-cli"
  display_name = "CCO CLI (cfctl) signing identity"
  depends_on   = [google_project_service.apis]
}

# The one grant impersonation needs, per user, on the SA itself.
resource "google_service_account_iam_member" "cli_token_creators" {
  for_each           = var.cli_users
  service_account_id = google_service_account.cli[0].name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = each.value
}

# `cfctl host ssh` tunnels through IAP TCP forwarding, dialed as the user
# (gcloud), not the impersonated SA, so the tunnel grant goes to the users.
# The condition limits it to SSH, and these principals cannot tunnel to any
# other port. SSH login itself (OS Login or metadata keys) and compute read
# access stay operator decisions.
resource "google_project_iam_member" "cli_users_iap_tunnel" {
  for_each = var.cli_users
  project  = var.project_id
  role     = "roles/iap.tunnelResourceAccessor"
  member   = each.value

  condition {
    title       = "cfctl-host-ssh-only"
    description = "IAP tunnels to port 22 only"
    expression  = "destination.port == 22"
  }
}

locals {
  cli_sa_member = length(var.cli_users) > 0 ? toset(["serviceAccount:${google_service_account.cli[0].email}"]) : toset([])
}

# The IAP access list is the entire access model. Everyone on it has full
# access to everything; no roles, no admin/developer split.
resource "google_iap_web_cloud_run_service_iam_member" "members" {
  # The relay's agent is a caller like any other. It authorizes credentials
  # through the same front door, with no separate path and no extra mechanism.
  # The managed CLI SA joins the same way.
  for_each = local.use_iap ? setunion(
    var.iap_members,
    local.cli_sa_member,
    local.relay_enabled ? toset(["serviceAccount:${module.relay[0].service_account_email}"]) : toset([]),
  ) : toset([])
  project                = var.project_id
  location               = google_cloud_run_v2_service.orchestrator.location
  cloud_run_service_name = google_cloud_run_v2_service.orchestrator.name
  role                   = "roles/iap.httpsResourceAccessor"
  member                 = each.value
}

# USER / browser-interactive programmatic access in iap mode. Service accounts
# don't need this. An SA on iap_members authenticates with a self-signed JWT
# (aud = service URL) that works with the Google-managed client. The managed
# client only blocks the USER OAuth flow, so a custom OAuth client (created by
# hand, since the IAP OAuth Admin API was shut down in March 2026, see
# var.iap_oauth_client_id) is allowlisted here for that case. cfctl then mints
# OIDC tokens with aud = that client ID, which IAP accepts at the edge. Created
# only when a client ID is supplied; SA-only and browser-only modes need none.
resource "google_iap_settings" "orchestrator" {
  count = local.use_iap && var.iap_oauth_client_id != "" ? 1 : 0
  name  = "projects/${local.project_number}/iap_web/cloud_run-${var.region}/services/${var.service_name}"

  access_settings {
    oauth_settings {
      programmatic_clients = [var.iap_oauth_client_id]
    }
  }

  depends_on = [
    google_iap_web_cloud_run_service_iam_member.members,
    google_cloud_run_v2_service_iam_member.iap_invoker,
  ]
}

# =============================================================================
# Database Seeding
# =============================================================================
# Applies migrations/spanner/complete_schema.sql:
# 1. DDL: CREATE TABLE, CREATE INDEX statements
# 2. DML: INSERT statements for seed data (system config, global quota,
#    machine families, the default machine type, host image configs)

resource "null_resource" "database_seed" {
  count = var.create_spanner && var.seed_database ? 1 : 0

  triggers = {
    # Bootstrap only: this re-runs when the database is recreated, never when
    # the schema file changes. A seed run against a live database would
    # re-execute the seed INSERTs and restore seeded rows an operator deleted.
    # A live database is upgraded with migrations/spanner/upgrades/.
    database_id = google_spanner_database.main[0].id
  }

  provisioner "local-exec" {
    command = "${path.module}/../../../scripts/provision/spanner-seed.sh ${var.project_id} ${var.spanner_instance_name} ${var.spanner_database_name}"
  }

  depends_on = [
    google_spanner_database.main,
    google_project_service.apis,
  ]
}
