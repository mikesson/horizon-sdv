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
#
# Workload Identity Federation for CI/CD Authentication
#
# This module creates a Workload Identity Pool and Providers for keyless
# authentication from GitHub Actions, GitLab CI/CD, or other OIDC providers.
#
# Resources created:
# - Workload Identity Pool
# - OIDC Providers (GitHub, GitLab)
# - Service Account for CI/CD
# - IAM bindings for Workload Identity Federation

terraform {
  required_version = ">= 1.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 4.0"
    }
  }
}

# -----------------------------------------------------------------------------
# Enable Required APIs
# -----------------------------------------------------------------------------

resource "google_project_service" "iam_credentials" {
  project = var.project_id
  service = "iamcredentials.googleapis.com"

  disable_on_destroy = false
}

resource "google_project_service" "iam" {
  project = var.project_id
  service = "iam.googleapis.com"

  disable_on_destroy = false
}

resource "google_project_service" "cloudresourcemanager" {
  project = var.project_id
  service = "cloudresourcemanager.googleapis.com"

  disable_on_destroy = false
}

resource "google_project_service" "sts" {
  project = var.project_id
  service = "sts.googleapis.com"

  disable_on_destroy = false
}

# -----------------------------------------------------------------------------
# Workload Identity Pool
# -----------------------------------------------------------------------------

resource "google_iam_workload_identity_pool" "cco" {
  project                   = var.project_id
  workload_identity_pool_id = var.pool_id
  display_name              = var.pool_display_name
  description               = "Workload Identity Pool for Cloud Cuttlefish Orchestrator CI/CD"
  disabled                  = false

  depends_on = [
    google_project_service.iam_credentials,
    google_project_service.iam,
    google_project_service.sts,
  ]
}

# -----------------------------------------------------------------------------
# GitHub Actions OIDC Provider
# -----------------------------------------------------------------------------

resource "google_iam_workload_identity_pool_provider" "github" {
  count = var.enable_github_provider ? 1 : 0

  project                            = var.project_id
  workload_identity_pool_id          = google_iam_workload_identity_pool.cco.workload_identity_pool_id
  workload_identity_pool_provider_id = "github"
  display_name                       = "GitHub Actions"
  description                        = "OIDC provider for GitHub Actions"

  # OIDC configuration for GitHub Actions
  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }

  # Attribute mapping from GitHub OIDC token to Google attributes
  # See: https://docs.github.com/en/actions/deployment/security-hardening-your-deployments/about-security-hardening-with-openid-connect
  attribute_mapping = {
    "google.subject"             = "assertion.sub"
    "attribute.actor"            = "assertion.actor"
    "attribute.repository"       = "assertion.repository"
    "attribute.repository_owner" = "assertion.repository_owner"
    "attribute.ref"              = "assertion.ref"
    "attribute.ref_type"         = "assertion.ref_type"
    "attribute.workflow"         = "assertion.workflow"
  }

  # Attribute condition to restrict which GitHub repos can authenticate
  # Only allows repositories from the specified organization/user
  attribute_condition = var.github_attribute_condition
}

# -----------------------------------------------------------------------------
# GitLab CI/CD OIDC Provider
# -----------------------------------------------------------------------------

resource "google_iam_workload_identity_pool_provider" "gitlab" {
  count = var.enable_gitlab_provider ? 1 : 0

  project                            = var.project_id
  workload_identity_pool_id          = google_iam_workload_identity_pool.cco.workload_identity_pool_id
  workload_identity_pool_provider_id = "gitlab"
  display_name                       = "GitLab CI/CD"
  description                        = "OIDC provider for GitLab CI/CD pipelines"

  # OIDC configuration for GitLab
  # See: https://docs.gitlab.com/ee/ci/cloud_services/google_cloud/
  oidc {
    issuer_uri = var.gitlab_issuer_uri
  }

  # Attribute mapping from GitLab OIDC token to Google attributes
  # See: https://docs.gitlab.com/ee/ci/cloud_services/index.html#configure-a-conditional-role-with-oidc-claims
  attribute_mapping = {
    "google.subject"           = "assertion.sub"
    "attribute.project_path"   = "assertion.project_path"
    "attribute.namespace_path" = "assertion.namespace_path"
    "attribute.ref"            = "assertion.ref"
    "attribute.ref_type"       = "assertion.ref_type"
    "attribute.pipeline_id"    = "assertion.pipeline_id"
    "attribute.job_id"         = "assertion.job_id"
    "attribute.environment"    = "assertion.environment"
  }

  # Attribute condition to restrict which GitLab projects can authenticate
  attribute_condition = var.gitlab_attribute_condition
}

# Note: GitLab WIF uses the same pool-level binding as GitHub.
# The attribute_condition on the provider restricts which GitLab projects can authenticate.

# -----------------------------------------------------------------------------
# Service Account for CI/CD
# -----------------------------------------------------------------------------

resource "google_service_account" "cco_cicd" {
  count = var.create_service_account ? 1 : 0

  project      = var.project_id
  account_id   = var.service_account_id
  display_name = "CCO CI/CD Service Account"
  description  = "Service account for CI/CD pipelines to access CCO via Workload Identity Federation"
}

# Allow the Workload Identity Pool to impersonate the service account
resource "google_service_account_iam_member" "workload_identity_user" {
  count = var.create_service_account && var.enable_github_provider ? 1 : 0

  service_account_id = google_service_account.cco_cicd[0].name
  role               = "roles/iam.workloadIdentityUser"

  # Allow any identity from the pool that matches the attribute condition
  member = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.cco.name}/*"
}

# Grant the service account access to the IAP-protected Cloud Run service.
# The IAP access list is the entire access model - this puts the CI/CD
# service account on it.
resource "google_iap_web_cloud_run_service_iam_member" "cco_cicd_iap" {
  count = var.create_service_account && var.cloud_run_service_name != "" ? 1 : 0

  project                = var.project_id
  location               = var.cloud_run_location
  cloud_run_service_name = var.cloud_run_service_name
  role                   = "roles/iap.httpsResourceAccessor"
  member                 = "serviceAccount:${google_service_account.cco_cicd[0].email}"
}

# -----------------------------------------------------------------------------
# IAM Bindings for Existing Service Accounts
# -----------------------------------------------------------------------------

# Allow Workload Identity Federation for existing service accounts
resource "google_service_account_iam_member" "external_sa_wif" {
  for_each = toset(var.existing_service_accounts)

  service_account_id = "projects/${var.project_id}/serviceAccounts/${each.value}"
  role               = "roles/iam.workloadIdentityUser"

  # Allow any identity from the pool that matches the attribute condition
  member = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.cco.name}/*"
}
