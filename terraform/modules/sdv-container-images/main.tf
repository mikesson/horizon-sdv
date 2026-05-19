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
  # Effective Docker build context per image (default: folder under images/).
  docker_context = {
    for name, img in var.images : name => coalesce(try(img.context_path, null), "${path.module}/images/${img.directory}/${name}")
  }

  # When context_path is set, skip hashing paths that match local npm/build artifacts (like .dockerignore).
  docker_context_files = {
    for name, img in var.images : name => sort([
      for f in fileset(local.docker_context[name], "**") : f
      if try(img.context_path, null) == null ? true : !(
        startswith(f, "node_modules/") || startswith(f, "dist/") || startswith(f, ".git/")
      )
    ])
  }
}

resource "docker_image" "sdv-container-images" {
  for_each = var.images

  name = "${var.gcp_region}-docker.pkg.dev/${var.gcp_project_id}/${var.gcp_registry_id}/${each.key}:${each.value.version}"
  build {
    no_cache = true

    context    = local.docker_context[each.key]
    tag        = ["${var.gcp_region}-docker.pkg.dev/${var.gcp_project_id}/${var.gcp_registry_id}/${each.key}:${each.value.version}"]
    build_args = each.value.build_args

    # Default "Dockerfile" in context; override with absolute path when context is external.
    dockerfile = coalesce(try(each.value.dockerfile_path, null), "Dockerfile")
    platform   = try(each.value.platform, null)
  }

  triggers = {
    dir_sha1 = sha1(join("", [
      for f in local.docker_context_files[each.key] :
      filesha1("${local.docker_context[each.key]}/${f}")
    ]))

    build_args_sha = sha1(jsonencode(each.value.build_args))

    dockerfile_sha = try(each.value.dockerfile_path, null) != null ? filesha1(each.value.dockerfile_path) : ""

    platform_sha = try(each.value.platform, null) != null ? each.value.platform : ""
  }
}

# Push container images to Google Artifact Registry
resource "docker_registry_image" "sdv-container-images" {
  for_each = docker_image.sdv-container-images

  name          = each.value.name
  keep_remotely = true

  # Push container image to Google Artifact Registry when changes are detected.
  triggers = {
    image_id = each.value.id
  }
}
