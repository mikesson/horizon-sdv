#!/usr/bin/env bash

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

# Description:
#   Installs Antigravity 2.0 Agent (desktop app) for Android Studio and ASfP CW images.
#   - Binary under /opt/antigravity/ or /usr/share/antigravity/
#   - GPU-safe launcher at /usr/local/bin/antigravity
#   - .desktop entry for app menu (no autostart)

# NOTE:
# When changing Antigravity 2.0 Agent version, update VERSION + TARBALL_URL + SHA256 together.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ANTIGRAVITY_2_VERSION="${ANTIGRAVITY_2_VERSION:-${1:-}}"
ANTIGRAVITY_2_INSTALL_MODE="${ANTIGRAVITY_2_INSTALL_MODE:-tarball}"
INSTALL_ROOT="/opt/antigravity"
WRAPPER_BIN="/usr/local/bin/antigravity"
DESKTOP_FILE="/usr/share/applications/antigravity.desktop"
AUTOSTART_DIR="/etc/xdg/autostart"

log() { echo "[install-antigravity-agent] $*"; }
die() { echo "[install-antigravity-agent] ERROR: $*" >&2; exit 1; }

if [[ "${ANTIGRAVITY_2_INSTALL_MODE}" == "tarball" ]]; then
  [[ -n "${ANTIGRAVITY_2_VERSION}" && "${ANTIGRAVITY_2_VERSION}" != "latest" ]] \
    || die "Set ANTIGRAVITY_2_VERSION to a concrete release (e.g. 2.2.1)"
fi

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required"

# --- Install real binary ---
install_from_tarball() {
  local url="${ANTIGRAVITY_2_TARBALL_URL:-}"
  local expected="${ANTIGRAVITY_2_TARBALL_SHA256:-}"
  [[ -n "${url}" ]] || die "Set ANTIGRAVITY_2_TARBALL_URL for tarball install"
  [[ -n "${expected}" ]] || die "Set ANTIGRAVITY_2_TARBALL_SHA256 for tarball integrity check"

  # Accept uppercase hex and optional "sha256:" prefix, passed from Jenkins parameters
  expected="$(echo "${expected}" | sed 's/^sha256://I' | tr 'A-F' 'a-f')"

  local tmpdir
  tmpdir="$(mktemp -d)"
  trap 'rm -rf "${tmpdir}"' RETURN

  log "Downloading Antigravity 2.0 tarball from ${url}"
  curl -fsSL "${url}" -o "${tmpdir}/antigravity.tar.gz"

  local actual
  actual="$(sha256sum "${tmpdir}/antigravity.tar.gz" | awk '{print $1}')"
  [[ "${actual}" == "${expected}" ]] \
    || die "SHA256 mismatch: expected ${expected}, got ${actual}"

  mkdir -p "${INSTALL_ROOT}"
  tar -xzf "${tmpdir}/antigravity.tar.gz" -C "${INSTALL_ROOT}" --strip-components=1
}

install_from_tarball

# Resolve real binary (tarball → /opt/antigravity)
AGY_REAL_BIN=""
for candidate in \
  "${INSTALL_ROOT}/antigravity" \
  "$(find "${INSTALL_ROOT}" -type f -iname Antigravity 2>/dev/null | head -n1)"; do
  [[ -n "${candidate}" && -f "${candidate}" ]] || continue
  chmod +x "${candidate}" 2>/dev/null || true
  [[ -x "${candidate}" ]] || continue
  AGY_REAL_BIN="${candidate}"
  break
done
[[ -n "${AGY_REAL_BIN}" ]] || die "Antigravity binary not found after install"

# Reject Antigravity IDE if present
if command -v antigravity-ide >/dev/null 2>&1 || [[ -d /opt/antigravity-ide ]]; then
  die "Antigravity IDE detected; this image must install Antigravity 2.0 Agent only"
fi

log "Using Antigravity binary: ${AGY_REAL_BIN}"

# --- GPU-safe wrapper for TigerVNC / X11 (Phase 1 Horizon stack) ---
install -d "$(dirname "${WRAPPER_BIN}")"
cat > "${WRAPPER_BIN}" <<EOF
#!/usr/bin/env bash
set -euo pipefail

# Phase 1: noVNC + TigerVNC + X11 (not Wayland). Mirrors Chrome GPU workarounds in Horizon Dockerfiles.
export QT_X11_NO_MITSHM=1
export _X11_NO_MITSHM=1
export _MITSHM=0

FLAGS=(
  --disable-gpu
  --disable-gpu-compositing
  --disable-dev-shm-usage
  --use-gl=swiftshader
)

exec "${AGY_REAL_BIN}" "\${FLAGS[@]}" "\$@"
EOF
chmod 0755 "${WRAPPER_BIN}"

# Install the icon
ICON_SRC="${SCRIPT_DIR}/icon/antigravity.png"
if [[ -f "${ICON_SRC}" ]]; then
  install -d /usr/share/icons/hicolor/512x512/apps
  install -m 0644 "${ICON_SRC}" /usr/share/icons/hicolor/512x512/apps/antigravity.png
  install -d /usr/share/pixmaps
  install -m 0644 "${ICON_SRC}" /usr/share/pixmaps/antigravity.png
  command -v gtk-update-icon-cache >/dev/null 2>&1 \
    && gtk-update-icon-cache -f /usr/share/icons/hicolor 2>/dev/null || true
else
  log "Warning: ${ICON_SRC} not found; app will show a generic icon"
fi

# --- .desktop launcher (app menu only; Exec points at wrapper) ---
# MimeType registers antigravity:// so OAuth can redirect back to the Agent.
install -d "$(dirname "${DESKTOP_FILE}")"
if [[ -f "${SCRIPT_DIR}/assets/antigravity.desktop" ]]; then
  install -m 0644 "${SCRIPT_DIR}/assets/antigravity.desktop" "${DESKTOP_FILE}"
  sed -i "s|^Exec=.*|Exec=${WRAPPER_BIN} %U|" "${DESKTOP_FILE}"
  if ! grep -q 'x-scheme-handler/antigravity' "${DESKTOP_FILE}"; then
    if grep -q '^MimeType=' "${DESKTOP_FILE}"; then
      sed -i 's|^MimeType=.*|MimeType=x-scheme-handler/antigravity;|' "${DESKTOP_FILE}"
    else
      sed -i '/^\[Desktop Entry\]/a MimeType=x-scheme-handler/antigravity;' "${DESKTOP_FILE}"
    fi
  fi
else
  cat > "${DESKTOP_FILE}" <<EOF
[Desktop Entry]
Name=Antigravity 2.0
Comment=Antigravity 2.0 Agent
Exec=${WRAPPER_BIN} %U
Icon=antigravity
Terminal=false
Type=Application
Categories=Development;
StartupWMClass=Antigravity
MimeType=x-scheme-handler/antigravity;
EOF
  chmod 0644 "${DESKTOP_FILE}"
fi

if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database /usr/share/applications || true
fi

# Explicitly no autostart
rm -f "${AUTOSTART_DIR}/antigravity.desktop" 2>/dev/null || true

# --- Version check (best effort) ---
if "${AGY_REAL_BIN}" --version >/dev/null 2>&1; then
  ver="$("${AGY_REAL_BIN}" --version 2>&1 || true)"
  log "${ver}"
  if [[ -n "${ANTIGRAVITY_2_VERSION}" && "${ANTIGRAVITY_2_VERSION}" != "latest" ]]; then
    echo "${ver}" | grep -Fq "${ANTIGRAVITY_2_VERSION}" \
      || die "Expected version containing '${ANTIGRAVITY_2_VERSION}', got: ${ver}"
  fi
  # Reject obvious 1.x if version string exposes it
  if echo "${ver}" | grep -qE '^1\.'; then
    die "Detected Antigravity 1.x; require 2.0 Agent"
  fi
else
  log "Note: real binary does not support --version; skipping version verification"
fi

log "Installed Antigravity 2.0 Agent (wrapper: ${WRAPPER_BIN}, desktop: ${DESKTOP_FILE})"
log "Done."
