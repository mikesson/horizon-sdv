#!/usr/bin/env bash
#
# Copyright (c) 2026 Accenture, All Rights Reserved.
#
# Shared helpers for scripts that run on the ephemeral Cuttlefish VM.
#
# Used by:
#   cvd_argo_guest_startup.sh — downloaded from GCS before anything else runs
#   cvd_argo_remote_entry.sh  — sourced from the unpacked pipeline tarball
#
# Why a separate file:
#   GCE metadata startup often has no HOME and no login shell. These helpers fix
#   that and route guest logs to serial port 2 so the Argo pod can read app output
#   without kernel noise on port 1.

# -----------------------------------------------------------------------------
# _trace
# -----------------------------------------------------------------------------
# Log a line to stderr (keeps guest stdout clean for scripts that parse it).

function _trace() {
  printf '%s\n' "$1" >&2
}

# -----------------------------------------------------------------------------
# _cvd_argo_ensure_home
# -----------------------------------------------------------------------------
# Set HOME from passwd when missing (GCE metadata startup has no login shell).

function _cvd_argo_ensure_home() {
  if [[ -n "${HOME:-}" ]]; then
    return 0
  fi
  local passwd_home
  passwd_home="$(getent passwd "$(id -un)" 2>/dev/null | cut -d: -f6 || true)"
  export HOME="${passwd_home:-/root}"
  _trace "[cvd-argo] HOME unset; using ${HOME}"
}

# -----------------------------------------------------------------------------
# _cvd_argo_setup_stdio_redirect
# -----------------------------------------------------------------------------
# Tee stdout/stderr to a log file, optional serial (port 2 = /dev/ttyS1), and journald.
# On Ubuntu arm64, tee to /dev/ttyS1 often returns EIO; serial is attempted in a side
# branch (tee >(tee serial …)) so systemd-cat and the log file always receive app output.

function _cvd_argo_setup_stdio_redirect() {
  local serial_dev="${1:?}"
  local log_file="${2:?}"
  local syslog_id="${3:-cvd-argo-guest}"

  if [[ ! -c "${serial_dev}" ]]; then
    _trace "[cvd-argo-guest] WARN: ${serial_dev} missing; logs only in ${log_file}"
    if command -v systemd-cat >/dev/null 2>&1; then
      exec > >(tee -a "${log_file}" | systemd-cat -t "${syslog_id}" -p info) 2>&1
    else
      exec > >(tee -a "${log_file}") 2>&1
    fi
    return 0
  fi

  if command -v systemd-cat >/dev/null 2>&1; then
    # Serial write runs in a side branch so EIO on /dev/ttyS1 cannot break journald/cloud.
    exec > >(tee -a "${log_file}" >(tee "${serial_dev}" 2>/dev/null >/dev/null) | systemd-cat -t "${syslog_id}" -p info) 2>&1
  else
    exec > >(tee -a "${log_file}" >(tee "${serial_dev}" 2>/dev/null >/dev/null)) 2>&1
  fi
}
