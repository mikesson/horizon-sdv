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

data "google_project" "project" {
  count = var.project_number == null ? 1 : 0
}

locals {
  project_number = var.project_number != null ? var.project_number : data.google_project.project[0].number
}

resource "google_service_account_key" "sa_key" {
  service_account_id = var.service_account_id
  private_key_type   = "TYPE_GOOGLE_CREDENTIALS_FILE"
}

resource "google_secret_manager_secret" "sa_key_secret" {
  secret_id = var.secret_id

  replication {
    user_managed {
      replicas {
        location = var.location
      }
    }
  }
}

resource "google_secret_manager_secret_version" "sa_key_secret_version" {
  secret      = google_secret_manager_secret.sa_key_secret.id
  secret_data = base64decode(google_service_account_key.sa_key.private_key)
}

resource "google_secret_manager_secret_iam_member" "member" {
  for_each  = { for idx, access in var.gke_access : idx => access }
  project   = google_secret_manager_secret.sa_key_secret.project
  secret_id = google_secret_manager_secret.sa_key_secret.secret_id
  role      = "roles/secretmanager.secretAccessor"
  ## ns/jenkins/jenkins-sa.
  member = "principal://iam.googleapis.com/projects/${local.project_number}/locations/global/workloadIdentityPools/${var.project_id}.svc.id.goog/subject/ns/${each.value.ns}/sa/${each.value.sa}"
}
