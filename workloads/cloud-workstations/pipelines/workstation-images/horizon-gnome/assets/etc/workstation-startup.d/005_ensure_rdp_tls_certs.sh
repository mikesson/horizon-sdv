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

# Ensure RDP TLS certs are valid before systemd starts GNOME/RDP.
# Persistent /home can retain corrupt winpr certs; gnome-setup runs too late
# (After=gnome-session), which leaves Guacamole failing with
# "wrong security type?" / remote desktop unreachable.
#
# Also pre-create ~/.config/dconf. grdctl persists RDP settings via dconf and
# returns exit 0 even when the commit fails with "No such file or directory"
# for ~/.config/dconf. That left RDP Status: disabled with no listener on 3389
# while the session unit stayed healthy (observed on cold Android Studio boots).

set -euo pipefail

# shellcheck source=/dev/null
source /google/scripts/common.sh

GRD_CERT_DIR="/home/${WORKSTATION_USER}/.local/share/gnome-remote-desktop"
DCONF_DIR="/home/${WORKSTATION_USER}/.config/dconf"

ensure_dconf_dir() {
  # Must exist before any grdctl GSettings write. Enforce mode 700 only on the
  # dconf directory; leave ~/.config mode alone on persistent /home.
  install -d -o "${WORKSTATION_UID}" -g "${WORKSTATION_UID}" \
    "/home/${WORKSTATION_USER}/.config"
  install -d -o "${WORKSTATION_UID}" -g "${WORKSTATION_UID}" -m 700 \
    "${DCONF_DIR}"
  log "Ensured dconf directory exists at ${DCONF_DIR}."
}

rdp_certs_valid() {
  local crt="${GRD_CERT_DIR}/rdp-tls.crt"
  local key="${GRD_CERT_DIR}/rdp-tls.key"
  [[ -s "${crt}" && -s "${key}" ]] || return 1
  if command -v openssl >/dev/null 2>&1; then
    openssl x509 -in "${crt}" -noout >/dev/null 2>&1 || return 1
    openssl rsa -in "${key}" -check -noout >/dev/null 2>&1 \
      || openssl pkey -in "${key}" -noout >/dev/null 2>&1 \
      || return 1
  else
    grep -q "BEGIN CERTIFICATE" "${crt}" || return 1
    grep -qE "BEGIN (RSA )?PRIVATE KEY|BEGIN PRIVATE KEY" "${key}" || return 1
  fi
  return 0
}

main() {
  ensure_dconf_dir

  local crt_size=0 key_size=0
  [[ -f "${GRD_CERT_DIR}/rdp-tls.crt" ]] && crt_size=$(stat -c%s "${GRD_CERT_DIR}/rdp-tls.crt" 2>/dev/null || echo 0)
  [[ -f "${GRD_CERT_DIR}/rdp-tls.key" ]] && key_size=$(stat -c%s "${GRD_CERT_DIR}/rdp-tls.key" 2>/dev/null || echo 0)

  if rdp_certs_valid; then
    log "RDP TLS certs already valid (crt=${crt_size} key=${key_size})."
    exit 0
  fi

  log "RDP TLS certs missing or invalid (crt=${crt_size} key=${key_size}); regenerating..."

  mkdir -p "${GRD_CERT_DIR}"
  chown -R "${WORKSTATION_UID}:${WORKSTATION_UID}" "/home/${WORKSTATION_USER}/.local" 2>/dev/null || true
  rm -f "${GRD_CERT_DIR}/rdp-tls.crt" "${GRD_CERT_DIR}/rdp-tls.key" \
    "${GRD_CERT_DIR}/rdp-tls.crt.tmp" "${GRD_CERT_DIR}/rdp-tls.key.tmp"

  local make_cert
  make_cert=$(command -v winpr-makecert3 || command -v winpr3-makecert || command -v winpr-makecert || true)
  if [[ -z "${make_cert}" ]]; then
    log "warning: winpr-makecert not found; cannot generate RDP TLS certificates."
    exit 0
  fi

  runuser -u "${WORKSTATION_USER}" -- "${make_cert}" -silent -rdp -path "${GRD_CERT_DIR}" rdp-tls
  for f in "${GRD_CERT_DIR}"/rdp-tls.*; do
    if [[ -f "${f}" ]]; then
      tr -d '\0' < "${f}" > "${f}.tmp" && mv "${f}.tmp" "${f}"
    fi
  done
  chown -R "${WORKSTATION_UID}:${WORKSTATION_UID}" "${GRD_CERT_DIR}"

  crt_size=0; key_size=0
  [[ -f "${GRD_CERT_DIR}/rdp-tls.crt" ]] && crt_size=$(stat -c%s "${GRD_CERT_DIR}/rdp-tls.crt" 2>/dev/null || echo 0)
  [[ -f "${GRD_CERT_DIR}/rdp-tls.key" ]] && key_size=$(stat -c%s "${GRD_CERT_DIR}/rdp-tls.key" 2>/dev/null || echo 0)
  if rdp_certs_valid; then
    log "RDP TLS certs regenerated successfully (crt=${crt_size} key=${key_size})."
  else
    log "error: RDP TLS cert regeneration still invalid (crt=${crt_size} key=${key_size})."
  fi
}

main "$@"
