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

# -----------------------------------------------------------------------------
# Workload Identity Pool Outputs
# -----------------------------------------------------------------------------

output "workload_identity_pool_name" {
  description = "The full resource name of the Workload Identity Pool"
  value       = google_iam_workload_identity_pool.cco.name
}

output "workload_identity_pool_id" {
  description = "The ID of the Workload Identity Pool"
  value       = google_iam_workload_identity_pool.cco.workload_identity_pool_id
}

output "github_provider_name" {
  description = "The full resource name of the GitHub OIDC provider"
  value       = var.enable_github_provider ? google_iam_workload_identity_pool_provider.github[0].name : null
}

output "gitlab_provider_name" {
  description = "The full resource name of the GitLab OIDC provider"
  value       = var.enable_gitlab_provider ? google_iam_workload_identity_pool_provider.gitlab[0].name : null
}

# -----------------------------------------------------------------------------
# Service Account Outputs
# -----------------------------------------------------------------------------

output "service_account_email" {
  description = "The email of the created service account"
  value       = var.create_service_account ? google_service_account.cco_cicd[0].email : null
}

output "service_account_name" {
  description = "The full resource name of the created service account"
  value       = var.create_service_account ? google_service_account.cco_cicd[0].name : null
}

# -----------------------------------------------------------------------------
# CI/CD Configuration Outputs
# -----------------------------------------------------------------------------

output "github_workload_identity_provider" {
  description = "The workload identity provider resource name for use in GitHub Actions"
  value       = var.enable_github_provider ? google_iam_workload_identity_pool_provider.github[0].name : null
}

output "gitlab_workload_identity_provider" {
  description = "The workload identity provider resource name for use in GitLab CI/CD"
  value       = var.enable_gitlab_provider ? google_iam_workload_identity_pool_provider.gitlab[0].name : null
}

locals {
  github_snippet = <<-EOT
    # Add this to your GitHub Actions workflow:
    #
    # permissions:
    #   id-token: write  # Required for requesting OIDC token
    #   contents: read   # Required for actions/checkout
    #
    # steps:
    #   - uses: actions/checkout@v4
    #
    #   - id: auth
    #     uses: google-github-actions/auth@v2
    #     with:
    #       workload_identity_provider: '${try(google_iam_workload_identity_pool_provider.github[0].name, "<workload-identity-provider>")}'
    #       service_account: '${try(google_service_account.cco_cicd[0].email, "<service-account-email>")}'
    #
    #   - name: Configure cfctl
    #     run: |
    #       cfctl config set-server <service-url>
    #
    #   - name: Use cfctl
    #     run: |
    #       cfctl auth whoami
    #       cfctl device list
  EOT

  gitlab_snippet = <<-EOT
    # Add this to your .gitlab-ci.yml:
    #
    # deploy:
    #   image: google/cloud-sdk:slim
    #   id_tokens:
    #     GITLAB_OIDC_TOKEN:
    #       aud: https://iam.googleapis.com/${try(google_iam_workload_identity_pool_provider.gitlab[0].name, "<workload-identity-provider>")}
    #   script:
    #     - |
    #       gcloud iam workload-identity-pools create-cred-config \
    #         ${try(google_iam_workload_identity_pool_provider.gitlab[0].name, "<workload-identity-provider>")} \
    #         --service-account=${try(google_service_account.cco_cicd[0].email, "<service-account-email>")} \
    #         --output-file=credentials.json \
    #         --credential-source-file=$CI_JOB_JWT_V2
    #     - export GOOGLE_APPLICATION_CREDENTIALS=credentials.json
    #     - cfctl config set-server <service-url>
    #     - cfctl device list
  EOT
}

output "github_actions_workflow_snippet" {
  description = "Example GitHub Actions workflow snippet for using this WIF configuration"
  value       = var.enable_github_provider && var.create_service_account ? local.github_snippet : null
}

output "gitlab_ci_workflow_snippet" {
  description = "Example GitLab CI/CD job snippet for using this WIF configuration"
  value       = var.enable_gitlab_provider && var.create_service_account ? local.gitlab_snippet : null
}

output "cloud_run_service_name" {
  description = "The IAP-protected Cloud Run service the CI/CD account can access"
  value       = var.cloud_run_service_name
}
