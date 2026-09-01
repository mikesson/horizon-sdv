#!/bin/bash

# Copyright 2025-2026 Google LLC
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

# This script handles GNOME-specific user and session setup tasks.

set -euo pipefail

# Max seconds to wait for gnome-shell to recognize extensions
EXTENSION_READY_TIMEOUT=15

# Source common utilities
# shellcheck source=/dev/null
source /google/scripts/common.sh

# Waits for a GNOME extension to become available in the shell.
wait_for_extension() {
  local ext_id="${1}"
  local start_time
  start_time=$(date +%s)

  log "Waiting for extension ${ext_id} to become available..."
  while true; do
    if runuser -u "${WORKSTATION_USER}" -- bash -c "
      export XDG_RUNTIME_DIR=/run/user/${WORKSTATION_UID}
      export DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/${WORKSTATION_UID}/bus
      gnome-extensions list" | grep -q "${ext_id}"; then
      log "Extension ${ext_id} is now available."
      return 0
    fi

    local current_time
    current_time=$(date +%s)
    if (( current_time - start_time > EXTENSION_READY_TIMEOUT )); then
      log "Warning: Timeout waiting for extension ${ext_id}."
      return 1
    fi
    sleep 0.5
  done
}

# Returns 0 when the RDP TLS cert/key pair is present and parseable as PEM.
rdp_certs_valid() {
  local grd_cert_dir="${1}"
  local crt="${grd_cert_dir}/rdp-tls.crt"
  local key="${grd_cert_dir}/rdp-tls.key"

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

# Generates RDP TLS certificates if missing or invalid (persistent homes can
# retain corrupt winpr output from a previous boot).
setup_rdp_certs() {
  log "Checking RDP TLS certificates..."
  local grd_cert_dir="/home/${WORKSTATION_USER}/.local/share/gnome-remote-desktop"
  if ! command -v winpr-makecert3 >/dev/null 2>&1 && ! command -v winpr-makecert >/dev/null 2>&1; then
    log "warning: winpr-makecert not found; cannot generate RDP TLS certificates"
    return 1
  fi

  if rdp_certs_valid "${grd_cert_dir}"; then
    log "RDP TLS certificates already valid."
    return 0
  fi

  # Regeneration needs root (runuser/chown). When called from the user session
  # (start_gnome_session.sh), fail loudly and rely on the root startup hook.
  if (( EUID != 0 )); then
    log "error: RDP TLS certificates missing/invalid but running as UID ${EUID}; cannot regenerate."
    log "error: expected /etc/workstation-startup.d/005_ensure_rdp_tls_certs.sh to provision them at boot."
    return 1
  fi

  log "Generating new RDP TLS certificate..."
  mkdir -p "${grd_cert_dir}"
  chown -R "${WORKSTATION_UID}:${WORKSTATION_UID}" "/home/${WORKSTATION_USER}/.local"
  rm -f "${grd_cert_dir}/rdp-tls.crt" "${grd_cert_dir}/rdp-tls.key" \
    "${grd_cert_dir}/rdp-tls.crt.tmp" "${grd_cert_dir}/rdp-tls.key.tmp"

  local make_cert
  make_cert=$(command -v winpr-makecert3 || command -v winpr3-makecert || command -v winpr-makecert)
  runuser -u "${WORKSTATION_USER}" -- "${make_cert}" -silent -rdp -path "${grd_cert_dir}" rdp-tls

  # Strip trailing null bytes that break some PEM parsers
  for f in "${grd_cert_dir}"/rdp-tls.*; do
    if [[ -f "$f" ]]; then
      tr -d '\0' < "$f" > "$f.tmp" && mv "$f.tmp" "$f"
    fi
  done

  chown -R "${WORKSTATION_UID}:${WORKSTATION_UID}" "${grd_cert_dir}"

  if ! rdp_certs_valid "${grd_cert_dir}"; then
    log "error: failed to produce a valid RDP TLS certificate under ${grd_cert_dir}"
    return 1
  fi
}

# Initializes the GNOME keyring with a blank password for Guacamole/RDP sessions
# (PAM never receives the RDP password and therefore cannot unlock the login
# keyring). Prefer unlocking the existing keyring. If that fails, stop the
# daemon before recreating the store so it cannot retain a stale collection in
# memory.
setup_keyring() {
  if ! command -v gnome-keyring-daemon >/dev/null 2>&1; then
    log "gnome-keyring-daemon not installed; skipping keyring setup."
    return 0
  fi

  log "Initializing gnome-keyring..."
  local keyring_dir="/home/${WORKSTATION_USER}/.local/share/keyrings"
  mkdir -p "${keyring_dir}"
  if (( EUID == 0 )); then
    chown -R "${WORKSTATION_UID}:${WORKSTATION_UID}" "/home/${WORKSTATION_USER}/.local"
  fi

  run_in_user_session() {
    local command="${1}"
    local -a session_env=(
      "HOME=/home/${WORKSTATION_USER}"
      "XDG_RUNTIME_DIR=/run/user/${WORKSTATION_UID}"
      "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/${WORKSTATION_UID}/bus"
      "GNOME_KEYRING_CONTROL=/run/user/${WORKSTATION_UID}/keyring"
    )

    if (( EUID == 0 )); then
      runuser -u "${WORKSTATION_USER}" -- env "${session_env[@]}" bash -c "${command}"
    else
      env "${session_env[@]}" bash -c "${command}"
    fi
  }

  # Querying the Locked property is non-interactive. Do not use secret-tool as
  # a probe: attempting to store a secret in a locked collection opens the
  # authentication dialog that this setup is intended to prevent.
  keyring_unlocked() {
    run_in_user_session "
      busctl --user get-property org.freedesktop.secrets \
        /org/freedesktop/secrets/collection/login \
        org.freedesktop.Secret.Collection Locked 2>/dev/null | grep -q 'false'
    "
  }

  unlock_keyring() {
    run_in_user_session "
      printf '\n' | gnome-keyring-daemon --unlock
    "
  }

  stop_keyring_daemon() {
    run_in_user_session "
      systemctl --user stop gnome-keyring-daemon.service gnome-keyring-daemon.socket 2>/dev/null || true
      pkill -f '^/usr/bin/gnome-keyring-daemon' 2>/dev/null || true
    "
  }

  start_keyring_daemon() {
    run_in_user_session "
      systemctl --user start gnome-keyring-daemon.socket 2>/dev/null || true
      systemctl --user start gnome-keyring-daemon.service 2>/dev/null || \
        gnome-keyring-daemon --start --components=secrets >/dev/null
    "
  }

  reset_keyring_store() {
    log "Resetting keyring store under ${keyring_dir} (unlock failed or store unusable)..."
    stop_keyring_daemon
    rm -rf "${keyring_dir}"
    mkdir -p "${keyring_dir}"
    if (( EUID == 0 )); then
      chown -R "${WORKSTATION_UID}:${WORKSTATION_UID}" "/home/${WORKSTATION_USER}/.local"
    fi
    start_keyring_daemon
  }

  local unlock_rc=0
  unlock_keyring || unlock_rc=$?
  if (( unlock_rc != 0 )); then
    log "warning: gnome-keyring-daemon --unlock exited ${unlock_rc}"
  fi

  if keyring_unlocked; then
    log "GNOME keyring is unlocked."
    if (( EUID == 0 )); then
      chown -R "${WORKSTATION_UID}:${WORKSTATION_UID}" "${keyring_dir}"
    fi
    return 0
  fi

  log "error: keyring unlock verification failed; recreating blank-password login keyring."
  reset_keyring_store

  unlock_rc=0
  unlock_keyring || unlock_rc=$?
  if (( unlock_rc != 0 )); then
    log "error: gnome-keyring-daemon --unlock after reset exited ${unlock_rc}"
  fi

  if keyring_unlocked; then
    log "GNOME keyring recreated and unlocked."
  else
    log "error: GNOME keyring remains unusable after reset; Electron apps may fail OAuth token storage."
  fi

  if (( EUID == 0 )); then
    chown -R "${WORKSTATION_UID}:${WORKSTATION_UID}" "${keyring_dir}"
  fi
}

# Sets Google Chrome as the default browser.
setup_browser() {
  if [[ ! -f /home/${WORKSTATION_USER}/.config/mimeapps.list ]]; then
    log "Setting google-chrome as default browser..."
    mkdir -p "/home/${WORKSTATION_USER}/.config"
    chown -R "${WORKSTATION_UID}:${WORKSTATION_UID}" "/home/${WORKSTATION_USER}/.config"
    # xdg-settings can exit non-zero in headless sessions; do not fail gnome-setup.
    runuser -u "${WORKSTATION_USER}" -- bash -c "
      export HOME=/home/${WORKSTATION_USER}
      xdg-mime default google-chrome.desktop text/html
      xdg-mime default google-chrome.desktop x-scheme-handler/http
      xdg-mime default google-chrome.desktop x-scheme-handler/https
      xdg-mime default google-chrome.desktop x-scheme-handler/about
      xdg-settings set default-web-browser google-chrome.desktop || true
    " || true
  fi
}

# Configures GNOME Shell settings.
setup_gnome_settings() {
  log "Configuring GNOME settings..."

  local ext="just-perfection-desktop@just-perfection"
  wait_for_extension "${ext}" || true

  runuser -u "${WORKSTATION_USER}" -- bash -s "${ext}" "${WORKSTATION_UID}" <<'EOF'
    ext_id="${1}"
    uid="${2}"

    export XDG_RUNTIME_DIR="/run/user/${uid}"
    export DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/${uid}/bus"

    gsettings set org.gnome.shell disable-user-extensions false

    if gnome-extensions list | grep -q "${ext_id}"; then
      echo "Enabling ${ext_id}..."
      gnome-extensions enable "${ext_id}" || true
    fi

    # Standard Workstation UI Tweak
    gsettings set org.gnome.shell.extensions.just-perfection screen-sharing-indicator false
    gsettings set org.gnome.shell.extensions.just-perfection screen-recording-indicator false
    gsettings set org.gnome.shell.extensions.just-perfection startup-status 0
    gsettings set org.gnome.shell.extensions.just-perfection support-notifier-showed-version 999

    gsettings set org.gnome.SessionManager auto-save-session true
    gsettings set org.gnome.desktop.screensaver lock-enabled false
    gsettings set org.gnome.desktop.screensaver idle-activation-enabled false
    gsettings set org.gnome.desktop.session idle-delay 0
    gsettings set org.gnome.desktop.lockdown disable-lock-screen false
EOF
}

main() {
  if [[ -f /run/gnome-setup-done ]]; then
    log "GNOME setup already done, skipping."
    exit 0
  fi

  log_event SERVICE_STARTING "Initializing GNOME workstation environment" SERVICE=gnome-setup

  # Clean up legacy configs
  rm -f "/home/${WORKSTATION_USER}/.config/systemd/user/org.gnome.Shell@wayland.service.d/override.conf"
  rm -f "/home/${WORKSTATION_USER}/.local/share/applications/org.gnome.Shell.desktop"

  setup_rdp_certs
  setup_keyring
  setup_browser
  setup_gnome_settings

  log_event SERVICE_READY "GNOME environment initialized" SERVICE=gnome-setup
  touch /run/gnome-setup-done
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
