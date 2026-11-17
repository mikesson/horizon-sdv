#!/usr/bin/env bash
#
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
#
# Restore everything the Playwright e2e suite needs after a Cloud Workstation
# reboot. On this workstation /home is a persistent disk but the rest of the
# filesystem (apt packages, /etc) is an ephemeral overlay reset on every boot.
#
#   Persists  (no action): node_modules, ~/.cache/ms-playwright, the repo.
#   Ephemeral (restored here): Chromium's OS libraries + the yarn apt key fix
#                              (its signing key reverts to a stale one on boot,
#                              which otherwise breaks apt-get update).
#
# Idempotent, safe to run anytime. After a normal stop/start it only reinstalls
# the OS libs; after a full workstation *recreate* (which also wipes /home) it
# additionally restores node_modules and the browser binaries.
#
# The interface is `scripts/ci.sh --fix-env` at the repo root; this script is
# its console-side implementation and stays runnable directly:
# Usage:  ./scripts/e2e-bootstrap.sh      (then: npm run e2e)

set -euo pipefail

CONSOLE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$CONSOLE_DIR"

log() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

if ! command -v sudo >/dev/null 2>&1; then
  echo "error: sudo is required to install system packages." >&2
  exit 1
fi

# 1. Node deps: persist on /home, only restored after a full recreate.
if [ -d node_modules ]; then
  log "node_modules present, skipping install"
else
  log "node_modules missing, running npm ci"
  npm ci
fi

# 2. Playwright browser binaries: persist on /home, idempotent download
#    (prints "is already installed" when present).
log "Ensuring Chromium browser binary"
npx playwright install chromium

# 3. apt health. yarn's signing key reverts to a stale one on reboot, breaking
#    apt-get update (and thus `playwright install-deps`, which runs it).
if [ -f /etc/apt/sources.list.d/yarn.list ]; then
  log "Refreshing yarn apt signing key"
  sudo mkdir -p /etc/apt/keyrings
  sudo curl -fsSL https://dl.yarnpkg.com/debian/pubkey.gpg -o /etc/apt/keyrings/yarn.asc
fi

log "Updating apt package lists"
sudo apt-get update || log "apt-get update reported errors, continuing to install-deps"

# 4. Chromium's OS libraries: ephemeral overlay, gone on every reboot.
log "Installing Chromium system dependencies"
sudo env "PATH=$PATH" npx playwright install-deps chromium

log "Ready. Run the suite with:  npm run e2e"
