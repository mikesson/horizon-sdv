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
#   Installs Antigravity CLI (agy) system-wide for Cloud Workstation images.
#   Intended for image build (run as root); no interactive OAuth at build time.
#
# Usage:
#   ANTIGRAVITY_CLI_VERSION=1.1.0 \
#   ANTIGRAVITY_CLI_TARBALL_URL=https://github.com/.../agy_cli_linux_x64.tar.gz \
#   ANTIGRAVITY_CLI_SHA256=<hex> \
#   install-antigravity-cli.sh
#
# Output:
#   /usr/local/bin/agy
#
# NOTE: When changing CLI version, update VERSION + TARBALL_URL + SHA256 together.

set -euo pipefail

ANTIGRAVITY_CLI_VERSION="${ANTIGRAVITY_CLI_VERSION:-${1:-}}"
ANTIGRAVITY_CLI_TARBALL_URL="${ANTIGRAVITY_CLI_TARBALL_URL:-}"
ANTIGRAVITY_CLI_SHA256="${ANTIGRAVITY_CLI_SHA256:-}"
TARGET_BIN="/usr/local/bin/agy"

log() { echo "[install-antigravity-cli] $*"; }
die() { echo "[install-antigravity-cli] ERROR: $*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required"

[[ -n "${ANTIGRAVITY_CLI_VERSION}" && "${ANTIGRAVITY_CLI_VERSION}" != "latest" ]] \
  || die "Set ANTIGRAVITY_CLI_VERSION to a concrete release (e.g. 1.1.0)"
[[ -n "${ANTIGRAVITY_CLI_TARBALL_URL}" ]] || die "Set ANTIGRAVITY_CLI_TARBALL_URL"
[[ -n "${ANTIGRAVITY_CLI_SHA256}" ]] || die "Set ANTIGRAVITY_CLI_SHA256"

# Accept uppercase hex and optional "sha256:" prefix, passed from Jenkins parameters
ANTIGRAVITY_CLI_SHA256="$(echo "${ANTIGRAVITY_CLI_SHA256}" | sed 's/^sha256://I' | tr 'A-F' 'a-f')"

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

log "Downloading Antigravity CLI ${ANTIGRAVITY_CLI_VERSION} from ${ANTIGRAVITY_CLI_TARBALL_URL}"
curl -fsSL "${ANTIGRAVITY_CLI_TARBALL_URL}" -o "${tmpdir}/agy.tar.gz"

actual="$(sha256sum "${tmpdir}/agy.tar.gz" | awk '{print $1}')"
[[ "${actual}" == "${ANTIGRAVITY_CLI_SHA256}" ]] \
  || die "SHA256 mismatch: expected ${ANTIGRAVITY_CLI_SHA256}, got ${actual}"

mkdir -p "${tmpdir}/extract"
tar -xzf "${tmpdir}/agy.tar.gz" -C "${tmpdir}/extract"

# Resolve the CLI binary resiliently: upstream may rename/renest it inside the tarball.
agy_src=""
for candidate in \
  "${tmpdir}/extract/antigravity" \
  "${tmpdir}/extract/agy" \
  "$(find "${tmpdir}/extract" -type f \( -iname antigravity -o -iname agy \) 2>/dev/null | head -n1)"; do
  [[ -n "${candidate}" && -f "${candidate}" ]] || continue
  chmod +x "${candidate}" 2>/dev/null || true
  [[ -x "${candidate}" ]] || continue
  agy_src="${candidate}"
  break
done
[[ -n "${agy_src}" ]] || die "Antigravity CLI binary not found in tarball"

install -d "$(dirname "${TARGET_BIN}")"
install -m 0755 "${agy_src}" "${TARGET_BIN}"
log "Installed agy → ${TARGET_BIN}"

version_out="$("${TARGET_BIN}" --version 2>&1)" || die "agy --version failed"
log "${version_out}"
echo "${version_out}" | grep -Fqx "${ANTIGRAVITY_CLI_VERSION}" \
  || die "Expected version containing '${ANTIGRAVITY_CLI_VERSION}', got: ${version_out}"

log "Done."