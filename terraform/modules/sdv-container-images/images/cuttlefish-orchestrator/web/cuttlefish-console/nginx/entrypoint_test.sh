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

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ENTRYPOINT="${SCRIPT_DIR}/entrypoint.sh"
TEMPLATE="${SCRIPT_DIR}/nginx.conf.template"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

mkdir -p "${TMP_DIR}/bin"
cat > "${TMP_DIR}/bin/nginx" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod +x "${TMP_DIR}/bin/nginx"

assert_fail() {
  local name="$1"
  shift
  local out_conf="${TMP_DIR}/${name}.conf"
  if env "$@" PATH="${TMP_DIR}/bin:${PATH}" NGINX_CONF_TEMPLATE="$TEMPLATE" NGINX_CONF_OUT="$out_conf" sh "$ENTRYPOINT" >/dev/null 2>&1; then
    echo "FAIL (${name}): expected non-zero exit" >&2
    exit 1
  fi
}

assert_render() {
  local name="$1"
  local want_port="$2"
  local want_probe="$3"
  shift 3
  local out_conf="${TMP_DIR}/${name}.conf"
  env "$@" PATH="${TMP_DIR}/bin:${PATH}" NGINX_CONF_TEMPLATE="$TEMPLATE" NGINX_CONF_OUT="$out_conf" sh "$ENTRYPOINT"
  grep -q "listen ${want_port};" "$out_conf" || { echo "FAIL (${name}): missing listen ${want_port};"; exit 1; }
  grep -q "listen ${want_probe};" "$out_conf" || { echo "FAIL (${name}): missing probe listen ${want_probe};"; exit 1; }
  if grep -q "__PORT__\|__PROBE_PORT__\|__API_UPSTREAM__" "$out_conf"; then
    echo "FAIL (${name}): unsubstituted template placeholder left in output" >&2
    exit 1
  fi
}

echo "Running nginx entrypoint.sh tests..."
assert_render "defaults" "8080" "8081"
assert_render "custom_port" "9090" "9091" PORT=9090 CCO_CONSOLE_PROBE_PORT=9091 API_UPSTREAM=cco-api:8080
assert_fail "port_zero" PORT=0
assert_fail "port_too_high" PORT=70000
assert_fail "port_non_numeric" PORT=abc
assert_fail "port_collides_with_probe" PORT=8081 CCO_CONSOLE_PROBE_PORT=8081
assert_fail "empty_probe_port" CCO_CONSOLE_PROBE_PORT=
echo "PASS: all nginx entrypoint tests succeeded"
