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
#   Remove all OS Login SSH public keys for the *current* metadata-token identity
#   (same identity used for Packer OS Login import). Calls gcp_compute_rest.py
#   prune-os-login-ssh-keys. Best-effort: warnings only, never fails the caller.
#
#   Jenkins GCE cloud agents manage keys per agent lifecycle; CF publish must prune
#   explicitly before each importSshPublicKey (CVD/CTS Path B does not use OS Login).
#
# Usage:
#   # shellcheck source=gcp_prune_os_login_ssh_keys.sh
#   source .../gcp_prune_os_login_ssh_keys.sh
#   gcp_prune_os_login_ssh_keys_for_caller

_gcp_prune_os_login_lib_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ---------------------------------------------------------------------------
# gcp_prune_os_login_ssh_keys_for_caller — delegates to gcp_compute_rest.py
# ---------------------------------------------------------------------------

# Remove all SSH keys from the OS Login profile for the current metadata identity.
# https://cloud.google.com/compute/docs/troubleshooting/troubleshoot-os-login#invalid_argument
gcp_prune_os_login_ssh_keys_for_caller() {
  local token removed rest_py
  rest_py="${GCP_COMPUTE_REST_PY:-${_gcp_prune_os_login_lib_dir}/gcp_compute_rest.py}"

  echo "[gcp-os-login] pruning SSH keys for current identity (frees profile before OS Login import)" >&2

  if ! command -v gcp_metadata_access_token >/dev/null 2>&1; then
    # shellcheck source=gcp_metadata_access_token.sh
    # shellcheck disable=SC1091
    source "${_gcp_prune_os_login_lib_dir}/gcp_metadata_access_token.sh"
  fi

  if ! token="$(gcp_metadata_access_token 2>/dev/null)" || [[ -z "${token}" ]]; then
    echo "[gcp-os-login] WARNING: metadata token unavailable; skipped OS Login SSH key prune" >&2
    return 0
  fi
  if [[ ! -f "${rest_py}" ]] || ! command -v python3 >/dev/null 2>&1; then
    echo "[gcp-os-login] WARNING: missing ${rest_py} or python3; cannot prune OS Login keys" >&2
    return 0
  fi

  removed="$(CF_COMPUTE_REST_TOKEN="${token}" python3 "${rest_py}" prune-os-login-ssh-keys | tail -n 1)" || removed="0"
  if ! [[ "${removed}" =~ ^[0-9]+$ ]]; then
    removed="0"
  fi
  echo "[gcp-os-login] prune finished (removed ${removed} key(s))" >&2
}
