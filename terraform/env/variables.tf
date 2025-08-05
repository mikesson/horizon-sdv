# Copyright (c) 2024-2025 Accenture, All Rights Reserved.
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
# 
# Description:
# Configuration file containing variables for the main env configuration.


variable "sdv_gh_app_id" {
  description = "The var gh_app_id value"
  type        = string
}

variable "sdv_gh_installation_id" {
  description = "The var gh_installation_id value"
  type        = string
}

variable "sdv_gh_app_key" {
  description = "The secret GH_APP_KEY value"
  type        = string
}

variable "sdv_gh_app_key_pkcs8" {
  description = "The secret GH_APP_KEY converted to pkcs8 value"
  type        = string
}

variable "sdv_gh_argocd_initial_password_bcrypt" {
  description = "The secret ARGOCD_INITIAL_PASSWORD_BCRYPT value"
  type        = string
}

variable "sdv_gh_jenkins_initial_password" {
  description = "The secret JENKINS_INITIAL_PASSWORD value"
  type        = string
}

variable "sdv_gh_keycloak_initial_password" {
  description = "The secret KEYCLOAK_INITIAL_PASSWORD value"
  type        = string
}

variable "sdv_gh_gerrit_admin_initial_password" {
  description = "The secret Github GERRIT_ADMIN_INITIAL_PASSWORD value"
  type        = string
}

variable "sdv_gh_gerrit_admin_private_key" {
  description = "The secret Github GERRIT_ADMIN_PRIVATE_KEY value"
  type        = string
}

variable "sdv_gh_keycloak_horizon_admin_password" {
  description = "The secret Github KEYCLOAK_HORIZON_ADMIN_PASSWORD value"
  type        = string
}

variable "sdv_gh_cuttlefish_vm_ssh_private_key" {
  description = "The secret Github CUTTLEFISH_VM_SSH_PRIVATE_KEY value"
  type        = string
}

variable "sdv_gh_access_token" {
  description = "Github access token"
  type        = string
}

variable "sdv_gh_repo_name" {
  description = "Github repo name"
  type        = string
}

variable "sdv_gh_env_name" {
  description = "Github environment name"
  type        = string
}

variable "sdv_gh_domain_name" {
  description = "Horizon domain name"
  type        = string
}

variable "sdv_gcp_project_id" {
  description = "GCP project id"
  type        = string
}

variable "sdv_computer_sa" {
  description = "GCP computer SA"
  type        = string
}

variable "sdv_gcp_cloud_region" {
  description = "GCP cloud region"
  type        = string
}

variable "sdv_gcp_cloud_zone" {
  description = "GCP cloud zone"
  type        = string
}

variable "sdv_create_network" {
  description = "Set to true to create a new VPC and subnetwork, or false to use existing ones."
  type        = bool
  default     = true
}

variable "sdv_existing_network_name" {
  description = "The name of the existing VPC network to use. Required if sdv_create_network is false."
  type        = string
  default     = ""
}

variable "sdv_existing_subnetwork_name" {
  description = "The name of the existing subnetwork to use. Required if sdv_create_network is false."
  type        = string
  default     = ""
}
