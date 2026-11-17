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
# Fail the image build rather than ship something that cannot start.

set -euo pipefail

test -x /usr/local/bin/turn-server || { echo "turn-server missing"; exit 1; }
test -x /usr/local/bin/relayhook   || { echo "relayhook missing"; exit 1; }

/usr/local/bin/relayhook -h 2>&1 | grep -q 'orchestrator' || { echo "relayhook flags unexpected"; exit 1; }

# The TLS listener and the authorization hook are both cargo features. A build
# without them produces a binary that looks fine and cannot do its job.
/usr/local/bin/turn-server --help >/dev/null 2>&1 || true
ldd /usr/local/bin/turn-server >/dev/null 2>&1 || true

for unit in cco-relay-firstboot relayhook turn-server; do
  systemctl is-enabled "$unit.service" >/dev/null || { echo "$unit not enabled"; exit 1; }
done

# The build toolchains must not survive into a relay that faces the internet.
for leftover in /usr/local/go /tmp/cco /root/.cargo /root/.rustup; do
  test ! -e "$leftover" || { echo "build toolchain left behind: $leftover"; exit 1; }
done

echo "turn-rs revision:   $(cat /etc/turn-server-revision)"
echo "relayhook revision: $(cat /etc/cco-relayhook-revision)"
echo "relay image verified"
