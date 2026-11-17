#!/bin/sh
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

set -eu

raw_upstream="${API_UPSTREAM-cuttlefish-orchestrator-api:8080}"

if [ -z "$raw_upstream" ]; then
  echo "API_UPSTREAM must not be empty" >&2
  exit 1
fi

stripped_ws=$(printf '%s' "$raw_upstream" | LC_ALL=C tr -d '\r\n\t ')
if [ "$stripped_ws" != "$raw_upstream" ]; then
  echo "API_UPSTREAM must not contain whitespace or line breaks" >&2
  exit 1
fi

upstream="${raw_upstream#http://}"
upstream="${upstream#https://}"
upstream="${upstream%%/*}"

if [ -z "$upstream" ]; then
  echo "API_UPSTREAM must not be empty" >&2
  exit 1
fi

if ! printf '%s' "$upstream" | LC_ALL=C grep -Eq '^[]A-Za-z0-9._:[-]+$'; then
  echo "API_UPSTREAM contains invalid characters" >&2
  exit 1
fi

mkdir -p /tmp/client_temp /tmp/proxy_temp /tmp/fastcgi_temp /tmp/uwsgi_temp /tmp/scgi_temp
sed "s|__API_UPSTREAM__|${upstream}|g" /etc/nginx/nginx.conf.template > /tmp/nginx.conf

exec nginx -e stderr -c /tmp/nginx.conf -g "daemon off;"
