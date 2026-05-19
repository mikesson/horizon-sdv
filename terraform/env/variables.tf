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

variable "force_update_secret_ids" {
  description = "List of secret keys (e.g. ['s6']) to force regeneration for (only applies to auto-generated secrets)."
  type        = list(string)
  default     = []
}

variable "manual_secrets" {
  description = "Map of secret keys (e.g. s6, s12) to manual password values."
  type        = map(string)
  default     = {}
  sensitive   = true

  validation {
    condition = alltrue([
      for k, v in var.manual_secrets : (
        length(v) >= 12 &&            # At least 12 characters
        v != "Change_Me_123" &&       # Not default value ("Change_Me_123")
        can(regex("[A-Z]", v)) &&     # At least one Uppercase
        can(regex("[0-9]", v)) &&     # At least one Number
        can(regex("[^a-zA-Z0-9]", v)) # At least one Symbol (anything not letter/num)
      )
    ])
    error_message = "Invalid Password detected. All manual secrets must:\n1. Must be at least 12 characters long\n2. Not be 'Change_Me_123'\n3. Contain at least 1 Uppercase letter\n4. Contain at least 1 Number\n5. Contain at least 1 Symbol"
  }
}

# SCM Configuration
variable "scm_type" {
  description = "SCM type: 'github' (GitHub-specific features) or 'git' (generic Git server)"
  type        = string
  default     = "github"
  validation {
    condition     = contains(["github", "git"], var.scm_type)
    error_message = "SCM type must be either 'github' or 'git'."
  }
}

variable "scm_auth_method" {
  description = "Auth method: 'app' (GitHub App only), 'userpass' (username/password or token), or 'none' (public repos)"
  type        = string
  validation {
    condition     = contains(["app", "userpass", "none"], var.scm_auth_method)
    error_message = "Auth method must be 'app' (GitHub App), 'userpass' (username/password), or 'none' (public repository)."
  }
}

variable "scm_repo_url" {
  description = "Full repository URL (e.g., https://github.com/owner/repo or https://git.example.com/project/repo)"
  type        = string
}

variable "scm_repo_branch" {
  description = "Repository branch or ref"
  type        = string
}

variable "scm_username" {
  description = "SCM username (use 'git' for GitHub PAT, actual username for other Git servers)"
  type        = string
  default     = "git"
}

variable "scm_password" {
  description = "SCM password or token (GitHub PAT, Gerrit HTTP password, GitLab token, etc.)"
  type        = string
  default     = ""
  sensitive   = true
}

variable "sdv_github_app_id" {
  description = "GitHub App ID (only for GitHub App auth)"
  type        = string
  default     = ""
}

variable "sdv_github_app_install_id" {
  description = "GitHub App Installation ID (only for GitHub App auth)"
  type        = string
  default     = ""
}

variable "sdv_github_app_private_key" {
  description = "The secret GH_APP_KEY value"
  type        = string
  default     = ""
  sensitive   = true
}

variable "sdv_keycloak_admin_password" {
  description = "The secret KEYCLOAK_INITIAL_PASSWORD value"
  type        = string
  default     = "Change_Me_123"

  validation {
    condition = (
      var.sdv_keycloak_admin_password != "Change_Me_123" &&
      length(var.sdv_keycloak_admin_password) >= 12 &&
      length(regexall("[a-z]", var.sdv_keycloak_admin_password)) > 0 &&
      length(regexall("[A-Z]", var.sdv_keycloak_admin_password)) > 0 &&
      length(regexall("[0-9]", var.sdv_keycloak_admin_password)) > 0 &&
      length(regexall("[^a-zA-Z0-9]", var.sdv_keycloak_admin_password)) > 0
    )
    error_message = local.password_policy_error
  }
}

variable "sdv_keycloak_horizon_admin_password" {
  description = "The secret KEYCLOAK_HORIZON_ADMIN_PASSWORD value"
  type        = string
  default     = "Change_Me_123"

  validation {
    condition = (
      var.sdv_keycloak_horizon_admin_password != "Change_Me_123" &&
      length(var.sdv_keycloak_horizon_admin_password) >= 12 &&
      length(regexall("[a-z]", var.sdv_keycloak_horizon_admin_password)) > 0 &&
      length(regexall("[A-Z]", var.sdv_keycloak_horizon_admin_password)) > 0 &&
      length(regexall("[0-9]", var.sdv_keycloak_horizon_admin_password)) > 0 &&
      length(regexall("[^a-zA-Z0-9]", var.sdv_keycloak_horizon_admin_password)) > 0
    )
    error_message = local.password_policy_error
  }
}

variable "sdv_env_name" {
  description = "Environment name (used as the sub-domain for the platform)"
  type        = string
}

variable "sdv_root_domain" {
  description = "Horizon domain name"
  type        = string
}

variable "sdv_gcp_project_id" {
  description = "GCP project id"
  type        = string
}

variable "sdv_gcp_compute_sa_email" {
  description = "GCP computer SA"
  type        = string
}

variable "sdv_gcp_region" {
  description = "GCP cloud region"
  type        = string
}

variable "sdv_gcp_zone" {
  description = "GCP cloud zone"
  type        = string
}

variable "sdv_gcp_backend_bucket" {
  description = "GCP cloud bucket name that stores tfstate file"
  type        = string
}

variable "enable_arm64" {
  type = bool
}

# --- SUB-ENVIRONMENT CONFIGURATION ---

variable "sdv_sub_env_configs" {
  description = "Configuration for each sub-environment including required passwords"
  type = map(object({
    keycloak_admin_password         = string
    keycloak_horizon_admin_password = string
    manual_secrets                  = optional(map(string), {})
    branch                          = optional(string, null)
  }))
  default   = {}
  sensitive = true

  validation {
    condition = alltrue([
      for env in keys(var.sdv_sub_env_configs) :
      can(regex("^[a-z0-9]([a-z0-9-]{0,2}[a-z0-9])?$", env))
    ])
    error_message = "Sub-environment names must be lowercase alphanumeric with hyphens, 1-4 characters."
  }

  validation {
    condition = alltrue([
      for env, config in var.sdv_sub_env_configs :
      config.keycloak_admin_password != "Change_Me_123" &&
      length(config.keycloak_admin_password) >= 12 &&
      can(regex("[A-Z]", config.keycloak_admin_password)) &&
      can(regex("[a-z]", config.keycloak_admin_password)) &&
      can(regex("[0-9]", config.keycloak_admin_password)) &&
      can(regex("[^a-zA-Z0-9]", config.keycloak_admin_password))
    ])
    error_message = "Each sub-env keycloak_admin_password must not be 'Change_Me_123' and must be at least 12 chars with uppercase, lowercase, numbers, and special characters."
  }

  validation {
    condition = alltrue([
      for env, config in var.sdv_sub_env_configs :
      config.keycloak_horizon_admin_password != "Change_Me_123" &&
      length(config.keycloak_horizon_admin_password) >= 12 &&
      can(regex("[A-Z]", config.keycloak_horizon_admin_password)) &&
      can(regex("[a-z]", config.keycloak_horizon_admin_password)) &&
      can(regex("[0-9]", config.keycloak_horizon_admin_password)) &&
      can(regex("[^a-zA-Z0-9]", config.keycloak_horizon_admin_password))
    ])
    error_message = "Each sub-env keycloak_horizon_admin_password must not be 'Change_Me_123' and must be at least 12 chars with uppercase, lowercase, numbers, and special characters."
  }

  validation {
    condition = alltrue([
      for env, config in var.sdv_sub_env_configs :
      alltrue([
        for k, v in config.manual_secrets :
        v != "Change_Me_123" &&
        length(v) >= 12 &&
        can(regex("[A-Z]", v)) &&
        can(regex("[a-z]", v)) &&
        can(regex("[0-9]", v)) &&
        can(regex("[^a-zA-Z0-9]", v))
      ])
    ])
    error_message = "Sub-env manual_secrets values must not be 'Change_Me_123' and must meet the password policy (12+ chars, uppercase, lowercase, number, symbol)."
  }
}
variable "sdv_abfs_build_node_pool_version" {
  description = "Kubernetes version for the ABFS build node pool (e.g. 1.32.7-gke.1079000). Pins the node pool to this GKE version."
  type        = string
}

variable "sdv_cluster_version" {
  description = "GKE cluster control plane version (e.g. 1.33.5-gke.2172001). Replaces release_channel so ABFS node pool can set auto_upgrade = false for CASFS kernel. Set to current master version when migrating."
  type        = string
}

variable "sdv_enable_network_policies" {
  description = "Enable network policies for all workloads. When disabled, all network policies will be removed. Default is enabled."
  type        = bool
  default     = true
}
variable "sdv_dns_dnssec_enabled" {
  description = "Enable DNSSEC for Cloud DNS zone. Requires domain ownership verification. Enabled by default."
  type        = bool
  default     = true
}

variable "sdv_dns_use_static_a_records" {
  description = "Use static A records in parent zone instead of zone delegation. When true: no Cloud DNS zone, LB cert auth, DNSSEC off. Add A records (domain and mcp.domain) to parent zone manually; LB IP from GCP Console."
  type        = bool
  default     = false
}

variable "sdv_enable_kms_encryption" {
  description = "Enable KMS encryption for GKE secrets. Note: KMS keyrings cannot be deleted once created in GCP. Set to false to avoid KMS entirely."
  type        = bool
  default     = false
}
