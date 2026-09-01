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

# Ensure kvm and render groups exist in the GNOME base image so child images
# (e.g. Android Studio) can grant workstation users access to /dev/kvm and
# GPU render nodes. Without these groups, boot hooks that call usermod -aG
# silently skip and the Android emulator fails with permission denied.
# groupadd -f is idempotent if a package already created the group.

ensure_groups() {
  echo "Ensuring kvm and render groups exist..."
  groupadd -f kvm
  groupadd -f render

  if ! getent group kvm >/dev/null; then
    echo "Error: failed to create or locate group kvm" >&2
    return 1
  fi
  if ! getent group render >/dev/null; then
    echo "Error: failed to create or locate group render" >&2
    return 1
  fi

  echo "Groups present: kvm($(getent group kvm | cut -d: -f3)) render($(getent group render | cut -d: -f3))"
}

main() {
  ensure_groups
}

main "$@"
