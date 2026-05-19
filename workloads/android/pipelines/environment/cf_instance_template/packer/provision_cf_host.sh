#!/usr/bin/env bash

# Copyright (c) 2025-2026 Accenture, All Rights Reserved.
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

if [ ! -d /tmp/cf ]; then
    echo "ERROR: /tmp/cf scripts directory not found"
    exit 1
fi

chmod +x /tmp/cf/*.sh

# Run the existing host initialization flow to keep package behavior aligned.
cd /tmp/cf
./cf_host_initialise.sh

if [ -n "${SSH_PUBLIC_KEY_B64:-}" ]; then
    DEFAULT_USER=${DEFAULT_USER:-jenkins}
    mkdir -p "/home/${DEFAULT_USER}/.ssh"
    echo "${SSH_PUBLIC_KEY_B64}" | base64 -d > "/home/${DEFAULT_USER}/.ssh/authorized_keys"
    chmod 700 "/home/${DEFAULT_USER}/.ssh"
    chmod 600 "/home/${DEFAULT_USER}/.ssh/authorized_keys"
    chown -R "${DEFAULT_USER}:${DEFAULT_USER}" "/home/${DEFAULT_USER}/.ssh"
fi

sync
