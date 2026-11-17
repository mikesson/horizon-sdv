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
# Required Variables
# -----------------------------------------------------------------------------

variable "project_id" {
  description = "The GCP project ID where resources will be created"
  type        = string
}

# -----------------------------------------------------------------------------
# Workload Identity Pool Configuration
# -----------------------------------------------------------------------------

variable "pool_id" {
  description = "The ID of the Workload Identity Pool (must be 4-32 chars, lowercase, digits, hyphens)"
  type        = string
  default     = "cco-cicd-pool"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{3,31}$", var.pool_id))
    error_message = "Pool ID must be 4-32 characters, start with a letter, and contain only lowercase letters, digits, and hyphens."
  }
}

variable "pool_display_name" {
  description = "Display name for the Workload Identity Pool"
  type        = string
  default     = "CCO CI/CD Pool"
}

# -----------------------------------------------------------------------------
# GitHub Provider Configuration
# -----------------------------------------------------------------------------

variable "enable_github_provider" {
  description = "Whether to create a GitHub Actions OIDC provider"
  type        = bool
  default     = true
}

variable "github_attribute_condition" {
  description = <<-EOT
    CEL expression to restrict which GitHub repositories can authenticate.

    Examples:
    - Single repo: assertion.repository == 'owner/repo'
    - Organization: assertion.repository_owner == 'my-org'
    - Multiple repos: assertion.repository in ['owner/repo1', 'owner/repo2']
    - Branch filter: assertion.repository_owner == 'my-org' && assertion.ref == 'refs/heads/main'
  EOT
  type        = string

  validation {
    condition     = length(var.github_attribute_condition) > 0
    error_message = "GitHub attribute condition must not be empty to prevent unauthorized access."
  }
}

# -----------------------------------------------------------------------------
# GitLab Provider Configuration
# -----------------------------------------------------------------------------

variable "enable_gitlab_provider" {
  description = "Whether to create a GitLab CI/CD OIDC provider"
  type        = bool
  default     = false
}

variable "gitlab_issuer_uri" {
  description = <<-EOT
    The OIDC issuer URI for GitLab.
    Use "https://gitlab.com" for GitLab.com, or your self-hosted GitLab URL.
  EOT
  type        = string
  default     = "https://gitlab.com"
}

variable "gitlab_attribute_condition" {
  description = <<-EOT
    CEL expression to restrict which GitLab projects can authenticate.

    Examples:
    - Single project: assertion.project_path == 'my-group/my-project'
    - Group: assertion.namespace_path == 'my-group'
    - Multiple projects: assertion.project_path in ['group/project1', 'group/project2']
    - Branch filter: assertion.namespace_path == 'my-group' && assertion.ref == 'main'
  EOT
  type        = string
  default     = ""
}

# -----------------------------------------------------------------------------
# Service Account Configuration
# -----------------------------------------------------------------------------

variable "create_service_account" {
  description = "Whether to create a new service account for CI/CD"
  type        = bool
  default     = true
}

variable "service_account_id" {
  description = "The ID of the service account to create (if create_service_account is true)"
  type        = string
  default     = "cco-cicd-sa"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{5,29}$", var.service_account_id))
    error_message = "Service account ID must be 6-30 characters, start with a letter, and contain only lowercase letters, digits, and hyphens."
  }
}

variable "existing_service_accounts" {
  description = "List of existing service account emails to grant Workload Identity access"
  type        = list(string)
  default     = []
}

# -----------------------------------------------------------------------------
# IAP Configuration
# -----------------------------------------------------------------------------

variable "cloud_run_service_name" {
  description = <<-EOT
    Name of the IAP-protected Cloud Run service to grant the CI/CD service
    account access to. Leave empty to skip the IAP access grant.
  EOT
  type        = string
  default     = ""
}

variable "cloud_run_location" {
  description = "Location of the Cloud Run service"
  type        = string
}
