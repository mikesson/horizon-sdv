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

set -euo pipefail

# Masks host-oriented systemd units that are not needed in this headless
# container image (physical tty, host module loading, etc.). SSH is
# intentionally left unmasked: openssh-server is installed and Guacamole's
# user-mapping advertises an SSH connection (preflight's SUPPORTED_PROTOCOLS
# includes SSH), so masking it here would break an advertised feature.
# These masks are created here at build time rather than committed as
# symlinks in the repo, because Argo CD blocks out-of-bounds symlinks
# (any symlink resolving to a target outside the repo, e.g. -> /dev/null)
# at fetch time, which would break GitOps sync for this repository.

MASK_UNITS=(
  "getty@tty1.service"
  "ldconfig.service"
  "systemd-modules-load.service"
)

mask_systemd_units() {
  echo "Masking unneeded systemd units..."
  mkdir -p /etc/systemd/system
  for unit in "${MASK_UNITS[@]}"; do
    ln -sf /dev/null "/etc/systemd/system/${unit}"
  done
}

main() {
  mask_systemd_units
}

main "$@"
