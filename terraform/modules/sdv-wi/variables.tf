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

variable "project_id" {
  description = "Define the project id"
  type        = string
}

variable "wi_service_accounts" {
  description = "A map of service accounts and their configurations"
  type = map(object({
    account_id   = string
    display_name = string
    description  = string
    gke_sas = list(object({
      gke_ns = string
      gke_sa = string
    }))
    # Project-level IAM roles (google_project_iam_member).
    roles = set(string)
    # Roles granted on this service account only (google_service_account_iam_member, member = self).
    # Prefer this for roles/iam.serviceAccountTokenCreator when only self signBlob/getAccessToken is needed.
    sa_roles = optional(set(string), [])
    # Project IAM with a CEL condition (e.g. GCS admin limited to buckets named {project}-*).
    conditional_roles = optional(list(object({
      role        = string
      title       = string
      description = optional(string, "")
      expression  = string
    })), [])
  }))
}
