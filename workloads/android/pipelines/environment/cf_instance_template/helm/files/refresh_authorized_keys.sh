#!/usr/bin/env bash

# Copyright (c) 2026 Accenture, All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#        http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

METADATA_HEADER="Metadata-Flavor: Google"
METADATA_URL="http://metadata.google.internal/computeMetadata/v1/instance/attributes"

metadata_get() {
  local key="$1"
  curl -fsS -H "${METADATA_HEADER}" "${METADATA_URL}/${key}" || true
}

default_user="$(metadata_get "jenkins-user")"
metadata_pubkey="$(metadata_get "jenkins-authorized-key")"

[ -z "${default_user}" ] && default_user="jenkins"

resolved_pubkey="${metadata_pubkey}"

if [ -z "${resolved_pubkey}" ]; then
  echo "No SSH public key available in instance metadata; skipping update."
  exit 0
fi

ssh_dir="/home/${default_user}/.ssh"
auth_keys="${ssh_dir}/authorized_keys"
tmp_file="$(mktemp)"

printf '%s\n' "${resolved_pubkey}" > "${tmp_file}"
install -d -m 700 -o "${default_user}" -g "${default_user}" "${ssh_dir}"
install -m 600 -o "${default_user}" -g "${default_user}" "${tmp_file}" "${auth_keys}"
rm -f "${tmp_file}"

