#!/bin/bash

# Copyright 2026 Google LLC
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

# The Dockerfile dpkg-diverts /usr/bin/google-chrome-stable ->
# /usr/bin/google-chrome-stable.real so a GPU-safe wrapper can sit on PATH.
# This hook creates that wrapper, restores the diverted path as a symlink, and
# points the .desktop entry at the wrapper.

DESKTOP_FILE="/usr/share/applications/google-chrome.desktop"
WRAPPER_BIN="/usr/local/bin/google-chrome-stable"
DIVERTED_BIN="/usr/bin/google-chrome-stable.real"
OPT_BIN="/opt/google/chrome/google-chrome"
CHROME_SYMLINK="/usr/bin/google-chrome-stable"

install_chrome_wrapper() {
  local real_bin=""
  if [[ -x "${DIVERTED_BIN}" ]]; then
    real_bin="${DIVERTED_BIN}"
  elif [[ -x "${OPT_BIN}" ]]; then
    real_bin="${OPT_BIN}"
  else
    echo "Error: Chrome binary not found at ${DIVERTED_BIN} or ${OPT_BIN}" >&2
    return 1
  fi

  echo "Installing Chrome wrapper at ${WRAPPER_BIN} -> ${real_bin}"
  install -d "$(dirname "${WRAPPER_BIN}")"
  cat > "${WRAPPER_BIN}" <<EOF
#!/usr/bin/env bash
set -euo pipefail

# Software-rendered Guacamole/RDP sessions (no discrete GPU): match Horizon
# Antigravity / legacy Android Studio Chrome workarounds.
export QT_X11_NO_MITSHM=1
export _X11_NO_MITSHM=1
export _MITSHM=0

FLAGS=(
  --disable-gpu
  --disable-dev-shm-usage
  --use-gl=swiftshader
)

exec "${real_bin}" "\${FLAGS[@]}" "\$@"
EOF
  chmod 0755 "${WRAPPER_BIN}"

  # Restore PATH / update-alternatives / x-www-browser chain after divert.
  ln -sfn "${WRAPPER_BIN}" "${CHROME_SYMLINK}"

  if [[ ! -x "${WRAPPER_BIN}" ]]; then
    echo "Error: Chrome wrapper is not executable: ${WRAPPER_BIN}" >&2
    return 1
  fi
  if [[ ! -x "${CHROME_SYMLINK}" ]]; then
    echo "Error: Chrome symlink is not executable: ${CHROME_SYMLINK}" >&2
    return 1
  fi
}

patch_chrome_desktop() {
  if [[ ! -f "${DESKTOP_FILE}" ]]; then
    echo "Warning: ${DESKTOP_FILE} not found. Skipping desktop patch."
    return 0
  fi

  echo "Patching ${DESKTOP_FILE} to use the Chrome wrapper..."
  sed -i 's|^Exec=/usr/bin/google-chrome-stable|Exec=/usr/local/bin/google-chrome-stable|g' "${DESKTOP_FILE}"

  # Hide the entry if the wrapper disappears (GNOME respects TryExec).
  if grep -q '^TryExec=' "${DESKTOP_FILE}"; then
    sed -i "s|^TryExec=.*|TryExec=${WRAPPER_BIN}|" "${DESKTOP_FILE}"
  else
    sed -i "/^\[Desktop Entry\]/a TryExec=${WRAPPER_BIN}" "${DESKTOP_FILE}"
  fi

  # Register as a high-priority favorite (but do not autostart).
  # Priority 20 ensures it appears before IDEs (30).
  # shellcheck source=/dev/null
  source /google/scripts/build/desktop_integration.sh
  desktop_register_app "${DESKTOP_FILE}" 20 false true

  if command -v update-desktop-database >/dev/null 2>&1; then
    update-desktop-database /usr/share/applications
  fi

  echo "Successfully patched Google Chrome desktop entry."
}

main() {
  install_chrome_wrapper
  patch_chrome_desktop
}

main "$@"
