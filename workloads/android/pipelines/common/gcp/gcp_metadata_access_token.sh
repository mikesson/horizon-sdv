#!/usr/bin/env bash
#
# Copyright (c) 2026 Accenture, All Rights Reserved.
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
#   Mint a cloud-platform OAuth access token from the GCE metadata server (GKE /
#   workload identity). Same pattern as cf_create_instance_template.sh — avoids
#   gcloud ADC picking a different principal than the pod service account.

# Usage: token="$(gcp_metadata_access_token)" || exit 1

# ---------------------------------------------------------------------------
# gcp_metadata_access_token — metadata server → cloud-platform bearer token
# ---------------------------------------------------------------------------

gcp_metadata_access_token() {
  local raw=""
  local base
  for base in \
    "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token" \
    "http://169.254.169.254/computeMetadata/v1/instance/service-accounts/default/token"; do
    raw="$(curl -fsS --connect-timeout 2 --max-time 8 \
      -H "Metadata-Flavor: Google" \
      "${base}?scopes=https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fcloud-platform" 2>/dev/null)" && break
    raw=""
  done
  [ -n "${raw}" ] || return 1
  if ! command -v python3 >/dev/null 2>&1; then
    return 1
  fi
  printf '%s' "${raw}" | python3 -c 'import sys, json; print(json.load(sys.stdin)["access_token"])' 2>/dev/null || return 1
}
