#!/usr/bin/env bash
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
# Build the authorization agent from this repository's checkout, so the image
# and the orchestrator it talks to come from one revision.

set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
GO_VERSION=1.26.9

curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tar.gz
rm -rf /usr/local/go
tar -C /usr/local -xzf /tmp/go.tar.gz
export PATH=/usr/local/go/bin:$PATH

cd /tmp/cco
# Passed in by Packer: there is no checkout here to ask, only the few packages
# the agent imports.
echo "${CCO_REVISION:-unknown}" > /etc/cco-relayhook-revision

CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -ldflags="-s -w" -o /usr/local/bin/relayhook ./cmd/relayhook
chmod 0755 /usr/local/bin/relayhook

useradd --system --no-create-home --shell /usr/sbin/nologin relayhook

# The source and the toolchain have no business surviving into a running relay.
rm -rf /tmp/cco /tmp/go.tar.gz /usr/local/go /root/go /root/.cache/go-build
