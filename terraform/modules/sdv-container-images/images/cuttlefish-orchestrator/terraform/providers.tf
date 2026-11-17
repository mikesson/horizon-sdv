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

# The root configuration owns the provider settings. An organization that
# calls modules/cco from its own configuration supplies its own instead, for
# example with impersonation.

terraform {
  required_version = ">= 1.5"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 7.36"
    }
    google-beta = {
      source  = "hashicorp/google-beta"
      version = ">= 7.36"
    }
  }

  # State is local by default. To keep it in a GCS bucket instead, create the
  # bucket with object versioning on, uncomment this block, and run
  # `terraform init -migrate-state`.
  #
  # backend "gcs" {
  #   bucket = "PROJECT_ID-tfstate"
  #   prefix = "cco"
  # }
}

provider "google" {
  project               = var.project_id
  user_project_override = true
  billing_project       = var.project_id
}

provider "google-beta" {
  project               = var.project_id
  user_project_override = true
  billing_project       = var.project_id
}
