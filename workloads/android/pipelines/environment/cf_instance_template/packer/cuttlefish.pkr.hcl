// Copyright (c) 2025-2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

packer {
  required_plugins {
    googlecompute = {
      source  = "github.com/hashicorp/googlecompute"
      version = ">= 1.1.6"
    }
  }
}

variable "project_id" {
  type = string
}

variable "zone" {
  type = string
}

variable "region" {
  type = string
}

variable "network" {
  type = string
}

variable "subnetwork" {
  type = string
}

variable "source_image_project_id" {
  type = string
}

variable "source_image" {
  type = string
}

variable "machine_type" {
  type = string
}

variable "disk_size_gb" {
  type = number
}

variable "disk_type" {
  type = string
}

variable "image_name" {
  type = string
}

variable "image_description" {
  type = string
}

variable "ssh_username" {
  type = string
}

variable "default_user" {
  type = string
}

variable "cf_script_path" {
  type = string
}

variable "android_cuttlefish_revision" {
  type = string
}

variable "cuttlefish_url" {
  type = string
}

variable "cuttlefish_post_command" {
  type = string
}

variable "repo_username" {
  type = string
  default = ""
}

variable "repo_password" {
  type = string
  default   = ""
  sensitive = true
}

variable "java_version" {
  type = string
}

variable "nodejs_version" {
  type = string
}

variable "curl_update_command" {
  type = string
}

variable "os_version" {
  type = string
}

variable "cts_android_16_url" {
  type = string
  default = ""
}

variable "cts_android_15_url" {
  type = string
  default = ""
}

variable "cts_android_14_url" {
  type = string
  default = ""
}

variable "ssh_public_key_b64" {
  type      = string
  sensitive = true
}

source "googlecompute" "cuttlefish" {
  project_id              = var.project_id
  source_image_project_id = [var.source_image_project_id]
  source_image            = var.source_image
  zone                    = var.zone
  machine_type            = var.machine_type
  # Required for bare-metal machine families (for example c4a-*-metal).
  on_host_maintenance     = "TERMINATE"
  disk_size               = var.disk_size_gb
  disk_type               = var.disk_type
  network                 = var.network
  subnetwork              = var.subnetwork
  omit_external_ip        = true
  use_internal_ip         = true
  ssh_username            = var.ssh_username
  image_name              = var.image_name
  image_description       = var.image_description
  image_storage_locations = [var.region]
  metadata = {
    enable-oslogin = "true"
  }
}

build {
  sources = ["source.googlecompute.cuttlefish"]

  provisioner "file" {
    source      = var.cf_script_path
    destination = "/tmp/cf"
  }

  provisioner "shell" {
    execute_command = "chmod +x {{ .Path }}; {{ .Vars }} sudo -E bash '{{ .Path }}'"
    environment_vars = [
      "CUTTLEFISH_REVISION=${var.android_cuttlefish_revision}",
      "CUTTLEFISH_URL=${var.cuttlefish_url}",
      "CUTTLEFISH_POST_COMMAND=${var.cuttlefish_post_command}",
      "REPO_USERNAME=${var.repo_username}",
      "REPO_PASSWORD=${var.repo_password}",
      "JAVA_VERSION=${var.java_version}",
      "NODEJS_VERSION=${var.nodejs_version}",
      "CURL_UPDATE_COMMAND=${var.curl_update_command}",
      "DEFAULT_USER=${var.default_user}",
      "OS_VERSION=${var.os_version}",
      "CTS_ANDROID_16_URL=${var.cts_android_16_url}",
      "CTS_ANDROID_15_URL=${var.cts_android_15_url}",
      "CTS_ANDROID_14_URL=${var.cts_android_14_url}",
      "SSH_PUBLIC_KEY_B64=${var.ssh_public_key_b64}"
    ]
    script = "${path.root}/provision_cf_host.sh"
  }
}
