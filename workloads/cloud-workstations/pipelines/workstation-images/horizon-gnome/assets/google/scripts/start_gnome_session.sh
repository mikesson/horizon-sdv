#!/bin/bash

# Copyright 2025-2026 Google LLC
# Modifications Copyright (c) 2026 Accenture, All Rights Reserved.
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

set -euo pipefail

# Source common utilities
# shellcheck source=/dev/null
source /google/scripts/common.sh

# Sets up the session environment variables.
setup_environment() {
  local target_user="${1}"
  local target_uid
  target_uid=$(id -u "${target_user}")

  # Ensure the runtime directory is set
  export XDG_RUNTIME_DIR="/run/user/${target_uid}"

  # Ensure D-Bus session bus is available
  if [[ -z "${DBUS_SESSION_BUS_ADDRESS:-}" ]]; then
    export DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/${target_uid}/bus"
  fi

  # GNOME Headless Wayland defaults
  export WAYLAND_DISPLAY=wayland-0
  export XDG_SESSION_TYPE=wayland
  export XDG_CURRENT_DESKTOP=ubuntu:GNOME
  export GNOME_SHELL_SESSION_MODE=ubuntu

  # Add Wayland support for common toolkits
  export GDK_BACKEND=wayland
  export QT_QPA_PLATFORM=wayland
  export CLUTTER_BACKEND=wayland
  export SDL_VIDEODRIVER=wayland

  # Conditionally force software rendering if no GPU is detected
  if [[ "${WORKSTATION_GPU_ENABLED}" == "false" ]]; then
    log "No GPU detected. Forcing software rendering (LLVMpipe)."
    export LIBGL_ALWAYS_SOFTWARE=1
    export GALLIUM_DRIVER=llvmpipe
    export MESA_LOADER_DRIVER_OVERRIDE=swrast
  fi
}

# Ensures the dconf database directory exists before any grdctl GSettings write.
# grdctl can exit 0 while logging "failed to commit changes to dconf" when this
# path is missing, leaving RDP Status: disabled with no listener on 3389.
ensure_dconf_dir() {
  local target_user="${1}"
  local dconf_dir="/home/${target_user}/.config/dconf"
  mkdir -p "${dconf_dir}"
  chmod 700 "${dconf_dir}" 2>/dev/null || true
  log "Ensured dconf directory exists at ${dconf_dir}."
}

# Returns 0 when headless RDP is enabled with TLS paths and credentials.
# Do not trust individual grdctl exit codes: dconf commit failures still exit 0.
# Must use `grdctl --headless status` (status is top-level). Plain `grdctl status`
# reads the separate Desktop Sharing config, which this script never writes.
rdp_settings_applied() {
  local st
  st=$(grdctl --headless status 2>/dev/null) || return 1
  grep -Eq 'Status:[[:space:]]*enabled' <<<"${st}" || return 1
  grep -Eq 'View-only:[[:space:]]*no' <<<"${st}" || return 1
  grep -Eq 'TLS key:[[:space:]]*\S' <<<"${st}" || return 1
  grep -Eq 'TLS certificate:[[:space:]]*\S' <<<"${st}" || return 1
  # Credentials print as "(hidden)" when set and "(empty)" when not.
  grep -Eq 'Username:[[:space:]]*\(empty\)' <<<"${st}" && return 1
  return 0
}

# Logs the headless GRD configuration for post-mortem analysis.
dump_rdp_status() {
  log "grdctl --headless status:"
  grdctl --headless status 2>&1 | while IFS= read -r line; do log "  ${line}"; done || true
}

# Returns 0 when something is accepting TCP on the RDP port.
rdp_port_listening() {
  local port="${RDP_PORT:-3389}"
  if command -v ss >/dev/null 2>&1; then
    ss -ltn "sport = :${port}" 2>/dev/null | grep -q LISTEN && return 0
  fi
  timeout 1 bash -c "echo >/dev/tcp/127.0.0.1/${port}" 2>/dev/null
}

# Starts the headless GRD daemon and waits for its D-Bus interface.
# Sets GRD_PID. Returns non-zero if the daemon never becomes usable.
start_grd_daemon() {
  local target_user="${1}"

  log "Starting GNOME Remote Desktop daemon..."
  # Clean up any stale PIDs to avoid bus name conflicts
  pkill -9 -u "${target_user}" gnome-remote-de || true
  sleep 1

  /usr/libexec/gnome-remote-desktop-daemon --headless &
  GRD_PID=$!

  local timeout=60
  local waited=0
  until gdbus introspect --session --dest org.gnome.RemoteDesktop.Headless --object-path /org/gnome/RemoteDesktop >/dev/null 2>&1; do
    if ! kill -0 "${GRD_PID}" 2>/dev/null; then
      log "error: GNOME Remote Desktop daemon exited during startup."
      return 1
    fi
    if (( waited >= timeout )); then
      log "error: GNOME Remote Desktop daemon D-Bus interface not ready after ${timeout}s."
      return 1
    fi
    log "Waiting for GNOME Remote Desktop daemon..."
    sleep 2
    waited=$((waited + 2))
  done
  return 0
}

# Waits for the RDP listener to bind. "Status: enabled" without a bound port is
# still an outage from Guacamole's point of view, so this is the real signal.
wait_for_rdp_port() {
  local timeout=30
  local waited=0
  until rdp_port_listening; do
    if (( waited >= timeout )); then
      log "error: RDP port ${RDP_PORT:-3389} not listening after ${timeout}s."
      return 1
    fi
    log "Waiting for RDP port ${RDP_PORT:-3389} to listen..."
    sleep 2
    waited=$((waited + 2))
  done
  return 0
}

apply_rdp_settings() {
  local target_user="${1}"
  local grd_cert_dir="${2}"
  local password="${3}"
  local err

  # Capture stderr so silent dconf commit failures are visible in the journal.
  err=$(grdctl --headless rdp disable 2>&1) || true
  if [[ -n "${err}" ]]; then
    if grep -qi 'failed to commit changes to dconf' <<<"${err}"; then
      log "warning: grdctl rdp disable reported dconf commit failure: $(tr '\n' ' ' <<<"${err}")"
    elif ! grep -qiE '^(Init TPM|using GKeyFile)' <<<"${err}"; then
      log "warning: grdctl rdp disable: $(tr '\n' ' ' <<<"${err}")"
    fi
  fi

  grdctl --headless rdp set-tls-key "${grd_cert_dir}/rdp-tls.key" \
    || log "warning: failed to set RDP TLS key"
  grdctl --headless rdp set-tls-cert "${grd_cert_dir}/rdp-tls.crt" \
    || log "warning: failed to set RDP TLS cert"
  grdctl --headless rdp set-credentials "${target_user}" "${password}" \
    || log "error: failed to set RDP credentials"
  grdctl --headless rdp set-port "${RDP_PORT:-3389}" \
    || log "warning: failed to set RDP port"
  grdctl --headless rdp enable \
    || log "error: failed to enable RDP"
  grdctl --headless rdp disable-view-only \
    || log "warning: failed to disable view-only mode"
}

# Configures RDP using credentials from the ephemeral environment.
# Retries until grdctl reflects the intended settings, then returns.
# Returns non-zero on failure; the caller must not treat that as fatal, since
# starting the daemon anyway is strictly better than leaving no listener at all.
configure_rdp() {
  local target_user="${1}"

  # Wait for config rendering to complete (ephemeral.env creation)
  local timeout=60
  local count=0
  until [[ -f "${EPHEMERAL_ENV_PATH}" ]] || (( count >= timeout )); do
    log "Waiting for ephemeral credentials file..."
    sleep 2
    (( count += 2 ))
  done

  if [[ ! -f "${EPHEMERAL_ENV_PATH}" ]]; then
    log "error: ${EPHEMERAL_ENV_PATH} not found after timeout. RDP configuration will fail."
    return 1
  fi

  # shellcheck disable=SC1091
  source "${EPHEMERAL_ENV_PATH}"
  if [[ -z "${EPHEMERAL_PASS:-}" ]]; then
    log "error: EPHEMERAL_PASS not found in ephemeral.env."
    return 1
  fi

  log "Configuring RDP credentials for ${target_user}..."
  local grd_cert_dir="/home/${target_user}/.local/share/gnome-remote-desktop"

  ensure_dconf_dir "${target_user}"

  # Certs must exist and be valid BEFORE grdctl enable / daemon start.
  # Provisioning is owned by the root startup hook
  # /etc/workstation-startup.d/005_ensure_rdp_tls_certs.sh (runs before
  # systemd). This call only verifies; if certs are still missing/invalid
  # setup_rdp_certs returns 1 (it cannot regenerate as a non-root user).
  # shellcheck source=/dev/null
  source /google/scripts/gnome_setup.sh
  setup_rdp_certs || log "warning: setup_rdp_certs returned non-zero"

  local max_attempts=12
  local attempt=1
  local sleep_duration=2
  until (( attempt > max_attempts )); do
    log "Applying RDP settings (attempt ${attempt}/${max_attempts})..."
    apply_rdp_settings "${target_user}" "${grd_cert_dir}" "${EPHEMERAL_PASS}"
    if rdp_settings_applied; then
      log "RDP settings verified (Status: enabled, TLS + credentials present)."
      return 0
    fi
    log "warning: RDP settings not yet applied (headless status incomplete); retrying in ${sleep_duration}s..."
    sleep "${sleep_duration}"
    attempt=$((attempt + 1))
  done

  log "error: failed to apply RDP settings after ${max_attempts} attempts."
  dump_rdp_status
  return 1
}

main() {
  local target_user="${1:-$WORKSTATION_USER}"

  log_event SERVICE_STARTING "Starting headless GNOME session" SERVICE=gnome-session
  setup_environment "${target_user}"

  # RDP authentication does not pass a password through PAM, so initialize the
  # login keyring before GNOME starts autostart applications. Running this after
  # gnome-session allows Chrome, Antigravity, and IDEs to race against a locked
  # keyring and display an authentication prompt.
  # shellcheck source=/dev/null
  source /google/scripts/gnome_setup.sh
  setup_keyring || log "error: GNOME keyring initialization failed before session startup"

  # Thoroughly clean up any stale session/D-Bus state for this user
  pkill -9 -u "${target_user}" gnome-session || true
  pkill -9 -u "${target_user}" gnome-shell || true
  pkill -9 -u "${target_user}" gnome-remote-de || true
  # Do NOT kill the user's D-Bus bus itself if possible, but clear the session bus if needed
  # pkill -9 -u "${target_user}" dbus-daemon || true

  /usr/libexec/gnome-session-binary --session=ubuntu &
  local session_pid=$!

  # Wait for shell readiness via D-Bus. Only a dead session process is fatal;
  # timing out and continuing keeps SSH/desktop available for recovery.
  local shell_timeout=180
  local shell_waited=0
  until gdbus call --session --dest org.gnome.Shell --object-path /org/gnome/Shell --method org.gnome.Shell.Eval "Main.sessionMode" >/dev/null 2>&1; do
    if ! kill -0 "${session_pid}" 2>/dev/null; then
      log "GNOME Session failed to start."
      exit 1
    fi
    if (( shell_waited >= shell_timeout )); then
      log "error: GNOME Shell not ready after ${shell_timeout}s; continuing anyway."
      break
    fi
    log "Waiting for GNOME Shell..."
    sleep 2
    shell_waited=$((shell_waited + 2))
  done
  log "GNOME Shell wait finished after ${shell_waited}s."

  # Configure RDP after Shell is ready (D-Bus available). Recover in place by
  # re-applying settings and bouncing only the GRD daemon; exiting would restart
  # the whole session via Restart=on-failure and loop on deterministic failures.
  GRD_PID=""
  local cycle=1
  local max_cycles=3
  local rdp_ready="false"
  while (( cycle <= max_cycles )); do
    configure_rdp "${target_user}" \
      || log "error: RDP configuration incomplete (cycle ${cycle}/${max_cycles}); starting daemon regardless."

    if start_grd_daemon "${target_user}" && wait_for_rdp_port; then
      rdp_ready="true"
      break
    fi

    log "warning: RDP not serving after cycle ${cycle}/${max_cycles}."
    dump_rdp_status
    cycle=$((cycle + 1))
  done

  if [[ "${rdp_ready}" == "true" ]]; then
    log "RDP is listening on port ${RDP_PORT:-3389}."
    log_event SERVICE_READY "GNOME session is active" SERVICE=gnome-session
  else
    log "error: RDP could not be brought up after ${max_cycles} cycles."
    log "error: session is left running so SSH and the desktop stay available."
    log_event SERVICE_DEGRADED "GNOME session active without RDP" SERVICE=gnome-session
  fi

  # Wait for the session to exit. Exiting early would make systemd restart it.
  wait "${session_pid}"
  [[ -n "${GRD_PID}" ]] && kill "${GRD_PID}" 2>/dev/null || true
}

main "$@"
