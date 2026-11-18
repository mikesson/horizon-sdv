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

variable "service_account_id" {
  description = "Define the service account ID"
  type        = string
}

variable "location" {
  description = "Define the secret replication location"
  type        = string
}

variable "secret_id" {
  description = "Name of the secret"
  type        = string
}

variable "gke_access" {
  description = "Define the GKE SAs the access of the secret"
  type = list(object({
    ns = string
    sa = string
  }))
}

variable "project_number" {
  description = "Optional GCP project number (avoids deferred data.google_project lookup when module uses depends_on)"
  type        = string
  default     = null
}
