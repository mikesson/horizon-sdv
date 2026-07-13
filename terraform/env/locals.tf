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

locals {
  # Parse repo URL to extract owner/name
  scm_repo_url_without_protocol = replace(var.scm_repo_url, "https://", "")
  scm_repo_parts                = split("/", local.scm_repo_url_without_protocol)
  scm_repo_owner                = var.scm_type == "github" ? (length(local.scm_repo_parts) > 1 ? local.scm_repo_parts[1] : "workspace-host") : ""
  scm_repo_name                 = length(local.scm_repo_parts) > 2 ? replace(local.scm_repo_parts[2], ".git", "") : "horizon-sdv"

  # Password policies per secret (adjust lengths/policy per need)
  secret_password_specs = {
    # s5 -> argocd-admin-password-b64
    s5 = { length = 25, min_lower = 2, min_upper = 2, min_numeric = 2, min_special = 2 }

    # s6 -> jenkins-admin-password-b64
    s6 = { length = 25, min_lower = 2, min_upper = 2, min_numeric = 2, min_special = 2 }

    # s9 -> gerrit-admin-password-b64
    s9 = { length = 25, min_lower = 2, min_upper = 2, min_numeric = 2, min_special = 2 }

    # s12 -> grafana-admin-password-b64 
    s12 = { length = 25, min_lower = 2, min_upper = 2, min_numeric = 2, min_special = 2 }

    # s14 -> postgres-admin-password-b64
    s14 = { length = 25, min_lower = 2, min_upper = 2, min_numeric = 2, min_special = 2 }

    # s17 -> mcp-gateway-registry-admin-password-b64
    s17 = { length = 25, min_lower = 2, min_upper = 2, min_numeric = 2, min_special = 2 }
  }

  # Identify which keys require auto-generation
  ids_to_generate = setsubtract(keys(local.secret_password_specs), nonsensitive(keys(var.manual_secrets)))

  # Check if user has set values manually
  resolved_secret_values = {
    for k, spec in local.secret_password_specs : k =>
    lookup(var.manual_secrets, k, null) != null ? var.manual_secrets[k] : random_password.pw[k].result
  }

  password_policy_error = <<EOT
Password must be at least 12 characters long and include:
- At least one uppercase letter [A-Z]
- At least one lowercase letter [a-z]
- At least one number [0-9]
- At least one symbol [!@#$%^&* etc.]
- No whitespace characters
EOT

  # --- SUB-ENVIRONMENT CONFIGURATION ---

  sub_env_manual_keys_safe = {
    for env, config in var.sdv_sub_env_configs :
    env => nonsensitive(keys(config.manual_secrets))
  }

  sub_env_password_keys = flatten([
    for env in keys(var.sdv_sub_env_configs) : [
      for secret_id in keys(local.secret_password_specs) :
      "${env}_${secret_id}"
      if !contains(local.sub_env_manual_keys_safe[env], secret_id)
    ]
  ])

  get_sub_env_password = {
    for env, config in var.sdv_sub_env_configs :
    env => {
      for secret_id in keys(local.secret_password_specs) :
      secret_id => lookup(
        config.manual_secrets,
        secret_id,
        lookup(random_password.sub_env_pw, "${env}_${secret_id}", { result = "" }).result
      )
    }
  }

  # --- SERVICE ACCOUNT TEMPLATES (sub-env SAs generated from these) ---
  sa_templates = {
    argocd = {
      name         = "argocd"
      prefix_style = "gke"
      gke_sas = [
        { ns = "argocd", sa = "argocd-sa" }
      ]
      roles = toset([
        "roles/secretmanager.secretAccessor",
        "roles/iam.serviceAccountTokenCreator",
      ])
    }
    jenkins = {
      name         = "jenkins"
      prefix_style = "gke"
      gke_sas = [
        { ns = "jenkins", sa = "jenkins-sa" },
        { ns = "jenkins", sa = "jenkins" }
      ]
      roles = toset([
        "roles/storage.objectUser",
        "roles/artifactregistry.writer",
        "roles/secretmanager.secretAccessor",
        "roles/iam.serviceAccountTokenCreator",
        "roles/container.admin",
        "roles/iap.tunnelResourceAccessor",
        "roles/iam.serviceAccountUser",
        "roles/compute.instanceAdmin.v1",
        "roles/workstations.admin",
        "roles/storage.bucketViewer",
        "roles/spanner.admin",
        "roles/logging.admin",
        "roles/editor",
        "roles/iam.serviceAccountAdmin",
        "roles/resourcemanager.projectIamAdmin"
      ])
    }
    keycloak = {
      name         = "keycloak"
      prefix_style = "gke"
      gke_sas = [
        { ns = "keycloak", sa = "keycloak-sa" }
      ]
      roles = toset([
        "roles/secretmanager.secretAccessor",
        "roles/iam.serviceAccountTokenCreator",
      ])
    }
    gerrit = {
      name         = "gerrit"
      prefix_style = "gke"
      gke_sas = [
        { ns = "gerrit", sa = "gerrit-sa" }
      ]
      roles = toset([
        "roles/secretmanager.secretAccessor",
        "roles/iam.serviceAccountTokenCreator",
      ])
    }
    monitoring = {
      name         = "monitoring"
      prefix_style = "plain"
      gke_sas = [
        { ns = "monitoring", sa = "monitoring-sa" }
      ]
      roles = toset([
        "roles/iam.workloadIdentityUser",
        "roles/monitoring.viewer"
      ])
    }
    monitoring_writer = {
      name         = "monitoring-writer"
      prefix_style = "plain"
      gke_sas = [
        { ns = "monitoring", sa = "monitoring-writer-sa" }
      ]
      roles = toset([
        "roles/monitoring.metricWriter",
        "roles/monitoring.viewer",
        "roles/iam.serviceAccountTokenCreator",
        "roles/iam.serviceAccountUser",
        "roles/iam.workloadIdentityUser"
      ])
    }
    tf_wl = {
      name         = "tf-wl"
      prefix_style = "gke"
      gke_sas = [
        { ns = "jenkins", sa = "terraform-workloads-sa" }
      ]
      roles = toset([
        "roles/storage.objectUser",
        "roles/artifactregistry.writer",
        "roles/secretmanager.secretAccessor",
        "roles/iam.serviceAccountTokenCreator",
        "roles/container.admin",
        "roles/iap.tunnelResourceAccessor",
        "roles/iam.serviceAccountUser",
        "roles/compute.instanceAdmin.v1",
        "roles/workstations.admin",
        "roles/storage.bucketViewer",
        "roles/spanner.admin",
        "roles/logging.admin",
        "roles/editor",
        "roles/iam.serviceAccountAdmin",
        "roles/resourcemanager.projectIamAdmin",
        "roles/file.editor"
      ])
    }
    external_dns = {
      name         = "external-dns"
      prefix_style = "plain"
      gke_sas = [
        { ns = "external-dns", sa = "external-dns-sa" }
      ]
      roles = toset([
        "roles/dns.admin"
      ])
    }
    mcp_gateway = {
      name         = "mcp-gateway"
      prefix_style = "gke"
      gke_sas = [
        { ns = "mcp-gateway-registry", sa = "mcp-gateway-registry-sa" }
      ]
      roles = toset([
        "roles/secretmanager.secretAccessor",
        "roles/iam.serviceAccountTokenCreator",
      ])
    }
    argo_workflows = {
      name         = "argo-workflows"
      prefix_style = "gke"
      gke_sas = [
        { ns = "argo-workflows", sa = "argo-workflows-server" },
        { ns = "workflows", sa = "workflow-executor" },
        # Horizon API signs artifact download URLs using this GSA.
        { ns = "horizon-api", sa = "horizon-api" }
      ]
      roles = toset([
        "roles/storage.objectUser",
        "roles/secretmanager.secretAccessor",
        "roles/artifactregistry.reader",
        "roles/artifactregistry.writer",
        "roles/iam.serviceAccountTokenCreator",
      ])
    }
    argo_workflows_elevated = {
      name         = "argo-workflows-elevated"
      prefix_style = "gke"
      gke_sas = [
        { ns = "workflows", sa = "workflow-executor-elevated" }
      ]
      roles = toset([
        "roles/storage.objectUser",
        "roles/artifactregistry.writer",
        "roles/secretmanager.secretAccessor",
        "roles/iam.serviceAccountTokenCreator",
        "roles/container.admin",
        "roles/iap.tunnelResourceAccessor",
        "roles/iam.serviceAccountUser",
        "roles/compute.instanceAdmin.v1",
        "roles/workstations.admin",
        "roles/storage.bucketViewer",
        "roles/spanner.admin",
        "roles/logging.admin",
        "roles/editor",
        "roles/iam.serviceAccountAdmin",
        "roles/resourcemanager.projectIamAdmin"
      ])
    }
  }

  # When no sub-envs are defined, avoid merge([]) which is invalid in Terraform
  sub_env_service_accounts = length(var.sdv_sub_env_configs) == 0 ? {} : merge([
    for env in keys(var.sdv_sub_env_configs) : {
      for tpl_key, tpl in local.sa_templates :
      "sa_${replace(env, "-", "_")}_${tpl_key}" => {
        account_id   = tpl.prefix_style == "gke" ? "gke-${env}-${tpl.name}-sa" : "${env}-${tpl.name}-sa"
        display_name = "${env} ${replace(title(tpl.name), "-", " ")} SA"
        description  = "${env}-${tpl.gke_sas[0].ns}/${tpl.gke_sas[0].sa} in GKE cluster uses this account through WI"

        gke_sas = [
          for gke_sa in tpl.gke_sas : {
            gke_ns = "${env}-${gke_sa.ns}"
            gke_sa = gke_sa.sa
          }
        ]

        roles = tpl.roles
      }
    }
  ]...)

  secret_templates = {
    keycloak_idp_client    = { secret_suffix = "keycloak-idp-client-secret", apply_value = false, ns = "keycloak", sa = "keycloak-sa", value_key = "keycloak_idp_client" }
    argocd_admin           = { secret_suffix = "argocd-admin-password-b64", apply_value = true, ns = "argocd", sa = "argocd-sa", value_key = "argocd_admin" }
    jenkins_admin          = { secret_suffix = "jenkins-admin-password-b64", apply_value = false, ns = "jenkins", sa = "jenkins-sa", value_key = "jenkins_admin" }
    keycloak_admin         = { secret_suffix = "keycloak-admin-password-b64", apply_value = true, ns = "keycloak", sa = "keycloak-sa", value_key = "keycloak_admin" }
    gerrit_admin           = { secret_suffix = "gerrit-admin-password-b64", apply_value = false, ns = "gerrit", sa = "gerrit-sa", value_key = "gerrit_admin" }
    gerrit_ssh             = { secret_suffix = "gerrit-admin-ssh-key-b64", apply_value = false, ns = "gerrit", sa = "gerrit-sa", value_key = "gerrit_ssh" }
    jenkins_cuttlefish_ssh = { secret_suffix = "jenkins-cuttlefish-ssh-key-b64", apply_value = false, ns = "jenkins", sa = "jenkins-sa", value_key = "jenkins_cuttlefish_ssh" }
    grafana_admin          = { secret_suffix = "grafana-admin-password-b64", apply_value = false, ns = "monitoring", sa = "monitoring-sa", value_key = "grafana_admin" }
    keycloak_horizon_admin = { secret_suffix = "keycloak-horizon-admin-password-b64", apply_value = true, ns = "jenkins", sa = "jenkins-sa", value_key = "keycloak_horizon_admin" }
    postgres_admin         = { secret_suffix = "postgres-admin-password-b64", apply_value = false, ns = "keycloak", sa = "keycloak-sa", value_key = "postgres_admin" }
    mcp_gateway_admin      = { secret_suffix = "mcp-gateway-registry-admin-password-b64", apply_value = true, ns = "mcp-gateway-registry", sa = "mcp-gateway-registry-sa", value_key = "mcp_gateway_admin" }
  }

  _sub_env_secret_values = {
    for env, config in var.sdv_sub_env_configs : env => {
      keycloak_idp_client    = "dummy"
      argocd_admin           = base64encode(bcrypt(local.get_sub_env_password[env]["s5"]))
      jenkins_admin          = base64encode(local.get_sub_env_password[env]["s6"])
      keycloak_admin         = base64encode(config.keycloak_admin_password)
      gerrit_admin           = base64encode(local.get_sub_env_password[env]["s9"])
      gerrit_ssh             = base64encode(module.gerrit_admin_key_subenv[env].private_key_openssh)
      jenkins_cuttlefish_ssh = base64encode(module.cuttlefish_key_subenv[env].private_key_openssh)
      grafana_admin          = base64encode(local.get_sub_env_password[env]["s12"])
      keycloak_horizon_admin = base64encode(config.keycloak_horizon_admin_password)
      postgres_admin         = base64encode(local.get_sub_env_password[env]["s14"])
      mcp_gateway_admin      = base64encode(local.get_sub_env_password[env]["s17"])
    }
  }

  sub_env_secrets = length(var.sdv_sub_env_configs) == 0 ? {} : merge([
    for env in keys(var.sdv_sub_env_configs) : {
      for tpl_key, tpl in local.secret_templates :
      "s_${env}_${tpl_key}" => {
        secret_id   = "${env}-${tpl.secret_suffix}"
        value       = local._sub_env_secret_values[env][tpl.value_key]
        apply_value = tpl.apply_value
        gke_access = [
          {
            ns = "${env}-${tpl.ns}"
            sa = tpl.sa
          }
        ]
      }
    }
  ]...)

  sub_env_git_secrets = length(var.sdv_sub_env_configs) == 0 ? {} : merge([
    for env in keys(var.sdv_sub_env_configs) :
    var.scm_auth_method == "app" ? {
      "s_${env}_git_app_id" = {
        secret_id   = "${env}-github-app-id-b64"
        value       = base64encode(var.sdv_github_app_id)
        apply_value = true
        gke_access = [
          { ns = "${env}-argocd", sa = "argocd-sa" },
          { ns = "${env}-jenkins", sa = "jenkins-sa" },
          { ns = "${env}-workflows", sa = "workflow-executor" }
        ]
      }
      "s_${env}_github_app_install" = {
        secret_id   = "${env}-github-app-installation-id-b64"
        value       = base64encode(var.sdv_github_app_install_id)
        apply_value = true
        gke_access = [
          { ns = "${env}-argocd", sa = "argocd-sa" },
          { ns = "${env}-jenkins", sa = "jenkins-sa" },
          { ns = "${env}-workflows", sa = "workflow-executor" }
        ]
      }
      "s_${env}_github_app_key" = {
        secret_id   = "${env}-github-app-private-key-b64"
        value       = base64encode(var.sdv_github_app_private_key)
        apply_value = true
        gke_access = [
          { ns = "${env}-argocd", sa = "argocd-sa" },
          { ns = "${env}-jenkins", sa = "jenkins-sa" },
          { ns = "${env}-workflows", sa = "workflow-executor" }
        ]
      }
      "s_${env}_github_app_pkcs8" = {
        secret_id   = "${env}-github-app-private-key-pkcs8-b64"
        value       = base64encode(data.external.pkcs8_converter.result.result)
        apply_value = true
        gke_access = [
          { ns = "${env}-jenkins", sa = "jenkins-sa" },
          { ns = "${env}-workflows", sa = "workflow-executor" }
        ]
      }
      } : {
      "s_${env}_git_pat" = {
        secret_id   = "${env}-scm-password-b64"
        value       = base64encode(var.scm_password)
        apply_value = true
        gke_access = [
          { ns = "${env}-jenkins", sa = "jenkins-sa" },
          { ns = "${env}-argocd", sa = "argocd-sa" },
          { ns = "${env}-workflows", sa = "workflow-executor" }
        ]
      }
    }
  ]...)

  # --- SUB ENVIRONMENT CONFIG END ---

  sdv_gcp_common_secrets_map = {
    s4 = {
      secret_id   = "keycloak-idp-client-secret"
      value       = "dummy"
      apply_value = false
      gke_access = [
        {
          ns = "keycloak"
          sa = "keycloak-sa"
        }
      ]
    }
    s5 = {
      secret_id   = "argocd-admin-password-b64"
      value       = base64encode(bcrypt(local.resolved_secret_values["s5"]))
      apply_value = true
      gke_access = [
        {
          ns = "argocd"
          sa = "argocd-sa"
        },
      ]
    }
    s6 = {
      secret_id   = "jenkins-admin-password-b64"
      value       = base64encode(local.resolved_secret_values["s6"])
      apply_value = false
      gke_access = [
        {
          ns = "jenkins"
          sa = "jenkins-sa"
        },
      ]
    }
    s7 = {
      secret_id   = "keycloak-admin-password-b64"
      value       = base64encode(var.sdv_keycloak_admin_password)
      apply_value = true
      gke_access = [
        {
          ns = "keycloak"
          sa = "keycloak-sa"
        },
      ]
    }
    # GCP secret name:  gerrit-admin-initial-password
    # WI to GKE at ns/gerrit/sa/gerrit-sa.
    s9 = {
      secret_id   = "gerrit-admin-password-b64"
      value       = base64encode(local.resolved_secret_values["s9"])
      apply_value = false
      gke_access = [
        {
          ns = "gerrit"
          sa = "gerrit-sa"
        }
      ]
    }
    # GCP secret name:  gh-gerrit-admin-private-key
    # WI to GKE at ns/gerrit/sa/gerrit-sa.
    s10 = {
      secret_id   = "gerrit-admin-ssh-key-b64"
      value       = base64encode(module.gerrit_admin_key.private_key_openssh)
      apply_value = false
      gke_access = [
        {
          ns = "gerrit"
          sa = "gerrit-sa"
        }
      ]
    }
    # GCP secret name:  gh-cuttlefish-vm-ssh-private-key
    # WI to GKE at ns/jenkins/sa/jenkins-sa.
    s11 = {
      secret_id = "jenkins-cuttlefish-ssh-key-b64"
      # SSH compatibility to adhere to POSIX compliance (newline).
      value       = base64encode(format("%s\n", module.cuttlefish_key.private_key_openssh))
      apply_value = false
      gke_access = [
        {
          ns = "jenkins"
          sa = "jenkins-sa"
        }
      ]
    }
    s12 = {
      secret_id   = "grafana-admin-password-b64"
      value       = base64encode(local.resolved_secret_values["s12"])
      apply_value = false
      gke_access = [
        {
          ns = "monitoring"
          sa = "monitoring-sa"
        },
      ]
    }
    s13 = {
      secret_id   = "keycloak-horizon-admin-password-b64"
      value       = base64encode(var.sdv_keycloak_horizon_admin_password)
      apply_value = true
      gke_access = [
        {
          ns = "jenkins"
          sa = "jenkins-sa"
        }
      ]
    }
    s14 = {
      secret_id   = "postgres-admin-password-b64"
      value       = base64encode(local.resolved_secret_values["s14"])
      apply_value = false
      gke_access = [
        {
          ns = "keycloak"
          sa = "keycloak-sa"
        }
      ]
    }
    s17 = {
      secret_id   = "mcp-gateway-registry-admin-password-b64"
      value       = base64encode(local.resolved_secret_values["s17"])
      apply_value = true
      gke_access = [
        {
          ns = "mcp-gateway-registry"
          sa = "mcp-gateway-registry-sa"
        },
      ]
    }
  }
  sdv_gcp_github_app_secrets_map = {
    s1 = {
      secret_id   = "github-app-id-b64"
      value       = base64encode(var.sdv_github_app_id)
      apply_value = true
      gke_access = [
        {
          ns = "argocd"
          sa = "argocd-sa"
        },
        {
          ns = "jenkins"
          sa = "jenkins-sa"
        },
        {
          ns = "workflows"
          sa = "workflow-executor"
        }
      ]
    }
    s2 = {
      secret_id   = "github-app-installation-id-b64"
      value       = base64encode(var.sdv_github_app_install_id)
      apply_value = true
      gke_access = [
        {
          ns = "argocd"
          sa = "argocd-sa"
        },
        {
          ns = "jenkins"
          sa = "jenkins-sa"
        },
        {
          ns = "workflows"
          sa = "workflow-executor"
        }
      ]
    }
    s3 = {
      secret_id   = "github-app-private-key-b64"
      value       = base64encode(var.sdv_github_app_private_key)
      apply_value = true
      gke_access = [
        {
          ns = "argocd"
          sa = "argocd-sa"
        },
        {
          ns = "jenkins"
          sa = "jenkins-sa"
        }
      ]
    }
    s8 = {
      secret_id   = "github-app-private-key-pkcs8-b64"
      value       = var.scm_auth_method == "app" ? base64encode(data.external.pkcs8_converter.result.result) : ""
      apply_value = true
      gke_access = [
        {
          ns = "jenkins"
          sa = "jenkins"
        },
        {
          ns = "workflows"
          sa = "workflow-executor"
        }
      ]
    }
  }
  # Username/password secrets (for generic Git auth)
  sdv_gcp_userpass_secrets_map = {
    s18 = {
      secret_id   = "scm-username-b64"
      value       = base64encode(var.scm_username)
      apply_value = true
      gke_access = [
        {
          ns = "jenkins"
          sa = "jenkins-sa"
        },
        {
          ns = "argocd"
          sa = "argocd-sa"
        }
      ]
    }
    s19 = {
      secret_id   = "scm-password-b64"
      value       = base64encode(var.scm_password)
      apply_value = true
      gke_access = [
        {
          ns = "jenkins"
          sa = "jenkins-sa"
        },
        {
          ns = "argocd"
          sa = "argocd-sa"
        },
        {
          ns = "workflows"
          sa = "workflow-executor"
        }
      ]
    }
  }

  # Conditionally select SCM secrets based on auth method
  #sdv_gcp_scm_secrets_map = (
  #  var.scm_auth_method == "app" ? local.sdv_gcp_github_app_secrets_map :
  #  var.scm_auth_method == "userpass" ? local.sdv_gcp_userpass_secrets_map :
  #  {} # Empty map for "none" - no secrets needed for public repos
  #)

  # Merge all secrets
  #sdv_gcp_secrets_map = merge(
  #  local.sdv_gcp_common_secrets_map,
  #  local.sdv_gcp_scm_secrets_map
  #)

  # Merge all secrets
  #sdv_gcp_secrets_map = merge(
  #  local.sdv_gcp_common_secrets_map,
  #  local.sdv_gcp_scm_secrets_map,
  #  local.sub_env_secrets,
  #  local.sub_env_git_secrets
  #)
}
