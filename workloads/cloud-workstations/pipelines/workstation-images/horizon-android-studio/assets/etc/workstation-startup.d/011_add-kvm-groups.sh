#!/bin/bash

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

# Grant the workstation user access to nested virtualization (/dev/kvm) and the
# GPU render node so the Android emulator can boot. /dev/kvm is owned root:kvm
# with mode 0660, so a user outside the kvm group gets "permission denied".
# This runs at boot (as root) before the user session starts, so the membership
# is in effect for the session that launches Android Studio / the emulator.
#
# Safe to run twice per boot (Horizon start_systemd.sh run_hooks and Google's
# workstation-startup.service both execute /etc/workstation-startup.d).

set -euo pipefail

TARGET_USER="${WORKSTATION_USER:-user}"

log() { echo "[011_add-kvm-groups] $*"; }
warn() { echo "[011_add-kvm-groups] WARNING: $*" >&2; }
error() { echo "[011_add-kvm-groups] ERROR: $*" >&2; }

require_group() {
  local group="${1}"
  if ! getent group "${group}" >/dev/null; then
    error "required group '${group}' is missing. The GNOME base should create it via build-hooks.d/07_ensure_kvm_render_groups.sh."
    return 1
  fi
}

user_in_group() {
  local user="${1}"
  local group="${2}"
  id -nG "${user}" 2>/dev/null | tr ' ' '\n' | grep -qx "${group}"
}

ensure_membership() {
  local user="${1}"
  local group="${2}"

  require_group "${group}"

  if user_in_group "${user}" "${group}"; then
    log "User ${user} already in group ${group}."
    return 0
  fi

  log "Adding ${user} to group ${group}..."
  usermod -aG "${group}" "${user}"

  if ! user_in_group "${user}" "${group}"; then
    error "usermod did not add ${user} to ${group} (id -nG: $(id -nG "${user}"))"
    return 1
  fi
}

verify_kvm_device() {
  if [[ ! -e /dev/kvm ]]; then
    warn "/dev/kvm is not present. Nested virtualization is not exposed on this host/config; the Android emulator will lack KVM acceleration."
    return 0
  fi

  local kvm_gid
  local expected_gid
  kvm_gid="$(stat -c '%g' /dev/kvm)"
  expected_gid="$(getent group kvm | cut -d: -f3)"

  local kvm_group_name
  kvm_group_name="$(stat -c '%G' /dev/kvm)"

  if [[ "${kvm_gid}" != "${expected_gid}" ]]; then
    warn "/dev/kvm is group-owned by ${kvm_group_name} (gid ${kvm_gid}), expected kvm (gid ${expected_gid}). Emulator access may still fail."
    return 0
  fi

  log "/dev/kvm present and group-owned by kvm (gid ${kvm_gid})."
}

main() {
  if (( EUID != 0 )); then
    error "must run as root (EUID=${EUID})"
    exit 1
  fi

  if ! id "${TARGET_USER}" >/dev/null 2>&1; then
    error "workstation user '${TARGET_USER}' does not exist"
    exit 1
  fi

  log "Configuring kvm/render group membership for ${TARGET_USER}..."

  ensure_membership "${TARGET_USER}" kvm
  ensure_membership "${TARGET_USER}" render
  verify_kvm_device

  log "Supplemental groups for ${TARGET_USER}: $(id -nG "${TARGET_USER}")"
}

main "$@"
