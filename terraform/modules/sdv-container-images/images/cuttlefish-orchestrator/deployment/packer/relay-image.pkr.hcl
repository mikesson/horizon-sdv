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

/**
 * Packer configuration for the TURN relay image.
 *
 * The relay is an internet-facing VM running a single-maintainer dependency,
 * so turn-rs is built from a pinned source revision inside the image instead
 * of being fetched at boot. What runs in production is what was reviewed, and
 * a moved tag upstream cannot change it. The authorization agent is compiled
 * from this repository in the same build.
 *
 * Neither binary is given a credential. The relay authenticates clients by
 * asking the orchestrator, and the agent proves the VM's identity with a token
 * the metadata server mints, so nothing secret is baked in or delivered.
 *
 * The builder is reached over IAP TCP forwarding, because the deployment VPC
 * permits SSH only from inside itself. That needs one firewall rule, which is
 * not carried in Terraform since it exists solely for image builds:
 *
 *   gcloud compute firewall-rules create allow-iap-ssh-image-build \
 *     --project=<project> --network=cuttlefish-network --direction=INGRESS \
 *     --source-ranges=35.235.240.0/20 --allow=tcp:22 \
 *     --target-tags=cco-image-build
 *
 * Delete it when the build is done. The builder keeps an external address for
 * egress only (it fetches Rust and Go), and nothing may reach it.
 */

packer {
  required_plugins {
    googlecompute = {
      version = ">= 1.0.0"
      source  = "github.com/hashicorp/googlecompute"
    }
  }
}

variable "project_id" {
  type        = string
  description = "GCP project ID where the image will be created"
}

variable "zone" {
  type        = string
  description = "GCP zone for building the image"
}

variable "image_family" {
  type        = string
  default     = "cco-relay"
  description = "Image family name for versioning"
}

variable "turnrs_version" {
  type        = string
  default     = "v4.1.4"
  description = "turn-rs release tag to build. Pinned; bump only with the hook conformance tests green."
}

variable "cco_revision" {
  type        = string
  default     = "unknown"
  description = "Commit the agent is built from; recorded on the image. Pass $(git rev-parse HEAD)."
}

variable "network" {
  type        = string
  default     = "cuttlefish-network"
  description = "VPC to build in. The project has no default network."
}

variable "subnetwork" {
  type        = string
  default     = "cuttlefish-network-subnet"
  description = "Subnetwork to build in; must be in the build zone's region."
}

variable "turnrs_repo" {
  type        = string
  default     = "https://github.com/mycrl/turn-rs.git"
  description = "turn-rs source repository"
}

source "googlecompute" "cco-relay" {
  project_id          = var.project_id
  source_image_family = "debian-12"
  zone                = var.zone
  # Compiling Rust and Go is the only reason this needs any size; the relay
  # itself is NIC-bound and runs on far less.
  machine_type = "e2-standard-4"

  image_name        = "cco-relay-{{timestamp}}"
  image_family      = var.image_family
  image_description = "CCO TURN relay: turn-rs plus the orchestrator authorization agent"

  disk_size = 20
  disk_type = "pd-balanced"

  network    = var.network
  subnetwork = var.subnetwork
  tags       = ["cco-image-build"]

  # SSH arrives through IAP; the VPC opens 22 to nothing else.
  use_iap      = true
  ssh_username = "packer"
}

build {
  name    = "cco-relay"
  sources = ["source.googlecompute.cco-relay"]

  provisioner "shell" {
    script          = "${path.root}/scripts/relay/01-install-turnrs.sh"
    execute_command = "{{.Vars}} sudo -E bash '{{.Path}}'"
    environment_vars = [
      "TURNRS_VERSION=${var.turnrs_version}",
      "TURNRS_REPO=${var.turnrs_repo}",
    ]
  }

  # The agent is built from this checkout instead of being fetched, so the image
  # and the orchestrator it talks to come from one revision. Only what the agent
  # imports is uploaded. The repository also vendors upstream trees that would
  # make a whole-tree copy enormous and would not be compiled anyway.
  provisioner "shell" {
    inline = ["mkdir -p /tmp/cco/cmd /tmp/cco/internal/platform"]
  }

  provisioner "file" {
    source      = "${path.root}/../../go.mod"
    destination = "/tmp/cco/go.mod"
  }

  provisioner "file" {
    source      = "${path.root}/../../go.sum"
    destination = "/tmp/cco/go.sum"
  }

  provisioner "file" {
    source      = "${path.root}/../../cmd/relayhook"
    destination = "/tmp/cco/cmd"
  }

  provisioner "file" {
    source      = "${path.root}/../../internal/platform/relay"
    destination = "/tmp/cco/internal/platform"
  }

  provisioner "shell" {
    script           = "${path.root}/scripts/relay/02-install-agent.sh"
    execute_command  = "{{.Vars}} sudo -E bash '{{.Path}}'"
    environment_vars = ["CCO_REVISION=${var.cco_revision}"]
  }

  provisioner "shell" {
    script          = "${path.root}/scripts/relay/04-setup-systemd.sh"
    execute_command = "{{.Vars}} sudo -E bash '{{.Path}}'"
  }

  provisioner "shell" {
    script          = "${path.root}/scripts/relay/05-verify.sh"
    execute_command = "{{.Vars}} sudo -E bash '{{.Path}}'"
  }
}
