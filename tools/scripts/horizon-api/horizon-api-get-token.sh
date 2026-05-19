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
#
# Description:
# Print a Keycloak access token for Horizon API (Swagger "Authorize" / curl Bearer).
#
# Two modes:
#
# 1) CI / automation — confidential client `horizon-api-ci` + client_credentials
#    export KEYCLOAK_CLIENT_SECRET='…'
#    ./tools/scripts/horizon-api/horizon-api-get-token.sh
#
# 2) Humans — public client `horizon-api` + OAuth 2.0 device authorization (no secret;
#    open the verification URL in a browser and approve with your user account)
#    ./tools/scripts/horizon-api/horizon-api-get-token.sh --device
#    # or: KEYCLOAK_AUTH_MODE=device ./tools/scripts/horizon-api/horizon-api-get-token.sh
#
# Optional env:
#   KEYCLOAK_BASE          default https://sbx.horizon-sdv.com/auth
#   KEYCLOAK_REALM         default horizon
#   KEYCLOAK_CLIENT_ID     default horizon-api-ci (credentials) or horizon-api (device)
#   KEYCLOAK_AUTH_MODE     client_credentials | device (overrides auto if set)
#   KEYCLOAK_CLIENT_SECRET required for client_credentials (default mode when set and not --device)
#   KEYCLOAK_DEVICE_SCOPE  default "openid" (space-separated scopes for device flow)
set -euo pipefail

KEYCLOAK_BASE="${KEYCLOAK_BASE:-https://sbx.horizon-sdv.com/auth}"
KEYCLOAK_REALM="${KEYCLOAK_REALM:-horizon}"
KEYCLOAK_DEVICE_SCOPE="${KEYCLOAK_DEVICE_SCOPE:-openid}"
KEYCLOAK_AUTH_MODE="${KEYCLOAK_AUTH_MODE:-}"

AUTH_MODE_CLI=""
for arg in "$@"; do
  case "$arg" in
    --device | -d) AUTH_MODE_CLI="device" ;;
    --client-credentials | --ci) AUTH_MODE_CLI="client_credentials" ;;
    -h | --help)
      echo "Usage: $0 [--device|-d] [--client-credentials|--ci]"
      echo "  Default: client_credentials if KEYCLOAK_CLIENT_SECRET is set, else device (public client horizon-api)."
      echo "  Env: KEYCLOAK_AUTH_MODE=client_credentials|device, KEYCLOAK_CLIENT_ID, KEYCLOAK_BASE, KEYCLOAK_REALM"
      exit 0
      ;;
  esac
done

if [[ -n "${AUTH_MODE_CLI}" ]]; then
  AUTH_MODE="${AUTH_MODE_CLI}"
elif [[ -n "${KEYCLOAK_AUTH_MODE}" ]]; then
  AUTH_MODE="${KEYCLOAK_AUTH_MODE}"
elif [[ -n "${KEYCLOAK_CLIENT_SECRET:-}" ]]; then
  AUTH_MODE="client_credentials"
else
  AUTH_MODE="device"
fi

if [[ "$AUTH_MODE" == "client_credentials" ]]; then
  KEYCLOAK_CLIENT_ID="${KEYCLOAK_CLIENT_ID:-horizon-api-ci}"
  KEYCLOAK_CLIENT_SECRET="${KEYCLOAK_CLIENT_SECRET:-}"
  if [[ -z "${KEYCLOAK_CLIENT_SECRET}" ]]; then
    echo "error: client_credentials requires KEYCLOAK_CLIENT_SECRET (Keycloak → Clients → ${KEYCLOAK_CLIENT_ID} → Credentials)." >&2
    echo "       For human login without a secret, run: $0 --device" >&2
    exit 1
  fi
else
  KEYCLOAK_CLIENT_ID="${KEYCLOAK_CLIENT_ID:-horizon-api}"
fi

TOKEN_URL="${KEYCLOAK_BASE%/}/realms/${KEYCLOAK_REALM}/protocol/openid-connect/token"
AUTH_BASE="${KEYCLOAK_BASE%/}/realms/${KEYCLOAK_REALM}/protocol/openid-connect"

# Public client horizon-api sets pkce.code.challenge.method=S256 in Keycloak — device authorization
# must include PKCE (RFC 7636) on /auth/device and send code_verifier on the token request.
pkce_verifier_and_challenge() {
  if command -v openssl >/dev/null 2>&1; then
    local v
    v="$(openssl rand -base64 96 | tr -d '\n' | tr '+/' '-_' | tr -d '=')"
    v="${v:0:96}"
    [[ ${#v} -ge 43 ]] || {
      echo "error: could not build PKCE verifier (openssl)." >&2
      return 1
    }
    PKCE_VERIFIER="$v"
    PKCE_CHALLENGE="$(printf '%s' "${PKCE_VERIFIER}" | openssl dgst -binary -sha256 | openssl base64 | tr -d '\n' | tr '+/' '-_' | tr -d '=')"
    return 0
  fi
  if command -v python3 >/dev/null 2>&1; then
    local _line1 _line2
    IFS=$'\n' read -r _line1 _line2 < <(python3 - <<'PY'
import base64, hashlib, secrets
v = base64.urlsafe_b64encode(secrets.token_bytes(32)).decode("ascii").rstrip("=")
c = base64.urlsafe_b64encode(hashlib.sha256(v.encode("ascii")).digest()).decode("ascii").rstrip("=")
print(v)
print(c)
PY
)
    PKCE_VERIFIER="${_line1}"
    PKCE_CHALLENGE="${_line2}"
    return 0
  fi
  echo "error: need openssl or python3 for PKCE (device flow with S256 client setting)." >&2
  return 1
}

device_flow() {
  pkce_verifier_and_challenge || exit 1
  local dev_resp device_code interval expires_in verification_uri user_code verification_uri_complete
  dev_resp="$(curl -sS -X POST "${AUTH_BASE}/auth/device" \
    -H 'Content-Type: application/x-www-form-urlencoded' \
    --data-urlencode "client_id=${KEYCLOAK_CLIENT_ID}" \
    --data-urlencode "scope=${KEYCLOAK_DEVICE_SCOPE}" \
    --data-urlencode 'code_challenge_method=S256' \
    --data-urlencode "code_challenge=${PKCE_CHALLENGE}")"

  if command -v jq >/dev/null 2>&1; then
    device_code="$(printf '%s' "${dev_resp}" | jq -r '.device_code // empty')"
    verification_uri="$(printf '%s' "${dev_resp}" | jq -r '.verification_uri // empty')"
    user_code="$(printf '%s' "${dev_resp}" | jq -r '.user_code // empty')"
    verification_uri_complete="$(printf '%s' "${dev_resp}" | jq -r '.verification_uri_complete // empty')"
    interval="$(printf '%s' "${dev_resp}" | jq -r '.interval // 5')"
    expires_in="$(printf '%s' "${dev_resp}" | jq -r '.expires_in // 600')"
    err="$(printf '%s' "${dev_resp}" | jq -r '.error_description // .error // empty')"
  else
    echo "error: jq required for device flow (install jq)." >&2
    exit 1
  fi

  if [[ -z "${device_code}" ]]; then
    echo "error: device authorization failed: ${err:-${dev_resp}}" >&2
    exit 1
  fi

  echo "" >&2
  echo "Open this URL in a browser (sign in with your Horizon user):" >&2
  if [[ -n "${verification_uri_complete}" ]]; then
    echo "  ${verification_uri_complete}" >&2
  else
    echo "  ${verification_uri}" >&2
    echo "  Enter code: ${user_code}" >&2
  fi
  echo "" >&2
  echo "Waiting for authorization (expires in ${expires_in}s)…" >&2

  local end=$((SECONDS + expires_in))
  while (( SECONDS < end )); do
    sleep "${interval}"
    local tok_resp
    tok_resp="$(curl -sS -X POST "${TOKEN_URL}" \
      -H 'Content-Type: application/x-www-form-urlencoded' \
      --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:device_code' \
      --data-urlencode "client_id=${KEYCLOAK_CLIENT_ID}" \
      --data-urlencode "device_code=${device_code}" \
      --data-urlencode "code_verifier=${PKCE_VERIFIER}")"

    local terr ttoken
    terr="$(printf '%s' "${tok_resp}" | jq -r '.error // empty')"
    ttoken="$(printf '%s' "${tok_resp}" | jq -r '.access_token // empty')"

    case "${terr}" in
      authorization_pending) continue ;;
      slow_down)
        interval=$((interval + 5))
        continue
        ;;
      access_denied | expired_token)
        echo "error: device authorization ${terr}: $(printf '%s' "${tok_resp}" | jq -r '.error_description // empty')" >&2
        exit 1
        ;;
      "")
        if [[ -n "${ttoken}" ]]; then
          printf '%s\n' "${ttoken}"
          return 0
        fi
        continue
        ;;
    esac

    if [[ -n "${terr}" && "${terr}" != "authorization_pending" && "${terr}" != "slow_down" ]]; then
      local msg
      msg="$(printf '%s' "${tok_resp}" | jq -r '.error_description // .error')"
      echo "error: token poll failed: ${msg}" >&2
      exit 1
    fi
  done
  echo "error: device authorization timed out (re-run $0 --device)." >&2
  exit 1
}

client_credentials_flow() {
  local resp token err
  resp="$(curl -sS -X POST "${TOKEN_URL}" \
    -d 'grant_type=client_credentials' \
    -d "client_id=${KEYCLOAK_CLIENT_ID}" \
    -d "client_secret=${KEYCLOAK_CLIENT_SECRET}")"

  if command -v jq >/dev/null 2>&1; then
    token="$(printf '%s' "${resp}" | jq -r '.access_token // empty')"
    err="$(printf '%s' "${resp}" | jq -r '(.error_description // .error) // empty')"
  else
    token="$(printf '%s' "${resp}" | python3 -c 'import sys, json; d=json.load(sys.stdin); print(d.get("access_token") or "")')"
    err="$(printf '%s' "${resp}" | python3 -c 'import sys, json; d=json.load(sys.stdin); print(d.get("error_description") or d.get("error") or "")')"
  fi

  if [[ -z "${token}" ]]; then
    echo "error: no access_token in response: ${err:-${resp}}" >&2
    exit 1
  fi
  printf '%s\n' "${token}"
}

case "${AUTH_MODE}" in
  device) device_flow ;;
  client_credentials) client_credentials_flow ;;
  *)
    echo "error: unknown KEYCLOAK_AUTH_MODE / mode: ${AUTH_MODE} (use client_credentials or device)" >&2
    exit 1
    ;;
esac
