#!/usr/bin/env bash
#
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
# Horizon API workflow steps via REST (curl+jq), aligned with tools/horizon CLI behavior.
# Env: HORIZON_DOMAIN or HORIZON_API_BASE_URL; KEYCLOAK_BASE or HORIZON_DOMAIN for token;
#      KEYCLOAK_REALM (default horizon), KEYCLOAK_CLIENT_ID (default horizon-api-ci),
#      KEYCLOAK_CLIENT_SECRET or HORIZON_ACCESS_TOKEN;
#      WORKFLOW_MODULE (default sample), WORKFLOW_TEMPLATE, WORKFLOW_PARAMETERS_JSON.
# Optional tuning: RUNNING_POLL_LIMIT, WAIT_NEW_ATTEMPTS, WAIT_NEW_SLEEP, WAIT_TERMINAL_SECS,
# TERMINAL_POLL_INTERVAL, LOG_STREAM_MAX_SECS, LOG_WAIT_POD_SECS (defaults match tools/horizon defaultConfig).

set -euo pipefail

RUNNING_POLL_LIMIT="${RUNNING_POLL_LIMIT:-500}"
WAIT_NEW_ATTEMPTS="${WAIT_NEW_ATTEMPTS:-120}"
WAIT_NEW_SLEEP="${WAIT_NEW_SLEEP:-2}"
WAIT_TERMINAL_SECS="${WAIT_TERMINAL_SECS:-3600}"
TERMINAL_POLL_INTERVAL="${TERMINAL_POLL_INTERVAL:-5}"
LOG_STREAM_MAX_SECS="${LOG_STREAM_MAX_SECS:-7200}"
LOG_WAIT_POD_SECS="${LOG_WAIT_POD_SECS:-60}"
KEYCLOAK_REALM="${KEYCLOAK_REALM:-horizon}"
KEYCLOAK_CLIENT_ID="${KEYCLOAK_CLIENT_ID:-horizon-api-ci}"
WORKFLOW_MODULE="${WORKFLOW_MODULE:-sample}"

# Prefer line-buffered pipes when stdbuf exists (non-TTY Jenkins: smoother streaming).
linebuf() {
  if command -v stdbuf >/dev/null 2>&1; then
    stdbuf -oL -eL "$@"
  else
    "$@"
  fi
}

api_base() {
  local b
  if [[ -n "${HORIZON_API_BASE_URL:-}" ]]; then
    b="${HORIZON_API_BASE_URL%/}"
  elif [[ -n "${HORIZON_DOMAIN:-}" ]]; then
    b="https://${HORIZON_DOMAIN}/horizon-api"
  else
    echo "horizon-workflow-via-rest: set HORIZON_API_BASE_URL or HORIZON_DOMAIN" >&2
    exit 1
  fi
  printf '%s' "$b"
}

keycloak_base() {
  local k
  if [[ -n "${KEYCLOAK_BASE:-}" ]]; then
    k="${KEYCLOAK_BASE%/}"
  elif [[ -n "${HORIZON_DOMAIN:-}" ]]; then
    k="https://${HORIZON_DOMAIN}/auth"
  else
    echo "horizon-workflow-via-rest: set KEYCLOAK_BASE or HORIZON_DOMAIN for token" >&2
    exit 1
  fi
  printf '%s' "$k"
}

refresh_bearer() {
  if [[ -n "${HORIZON_ACCESS_TOKEN:-}" ]]; then
    BEARER="${HORIZON_ACCESS_TOKEN}"
    return 0
  fi
  local token_url resp tok err
  token_url="$(keycloak_base)/realms/${KEYCLOAK_REALM}/protocol/openid-connect/token"
  resp="$(curl -sS -X POST "${token_url}" \
    -H 'Content-Type: application/x-www-form-urlencoded' \
    --data-urlencode 'grant_type=client_credentials' \
    --data-urlencode "client_id=${KEYCLOAK_CLIENT_ID}" \
    --data-urlencode "client_secret=${KEYCLOAK_CLIENT_SECRET}")"
  tok="$(printf '%s' "${resp}" | jq -r '.access_token // empty')"
  err="$(printf '%s' "${resp}" | jq -r '(.error_description // .error) // empty')"
  if [[ -z "${tok}" ]]; then
    echo "horizon-workflow-via-rest: token error: ${err:-${resp}}" >&2
    exit 1
  fi
  BEARER="${tok}"
}

http_get() {
  local path="$1"
  local url base code body
  base="$(api_base)"
  url="${base}${path}"
  body="$(curl -sS -w '\n%{http_code}' -H "Authorization: Bearer ${BEARER}" -H 'Accept: application/json' "${url}")"
  code="$(printf '%s' "${body}" | tail -n1)"
  body="$(printf '%s' "${body}" | sed '$d')"
  if [[ "${code}" == "401" ]]; then
    refresh_bearer
    body="$(curl -sS -w '\n%{http_code}' -H "Authorization: Bearer ${BEARER}" -H 'Accept: application/json' "${url}")"
    code="$(printf '%s' "${body}" | tail -n1)"
    body="$(printf '%s' "${body}" | sed '$d')"
  fi
  if [[ "${code}" != "2"* ]]; then
    echo "horizon-workflow-via-rest: GET ${path} HTTP ${code} ${body}" >&2
    exit 1
  fi
  printf '%s' "${body}"
}

# Build merged parameters JSON (string values) from catalog + user JSON (matches CLI merge; pure jq).
# Use --arg (string) + fromjson, not --argjson: passing JSON via argv breaks on size/newlines/special chars.
build_submit_body() {
  local catalog_json="$1"
  local user_json="$2"
  local module="$3"
  local template="$4"
  local uj="${user_json}"
  [[ -z "${uj// }" ]] && uj="{}"
  jq -n -c \
    --arg cat "${catalog_json}" \
    --arg usr "${uj}" \
    --arg m "${module}" \
    --arg t "${template}" \
    '
    ($cat | try fromjson catch error("catalog response is not valid JSON")) as $catalog |
    ($usr | sub("^\uFEFF";"") | try fromjson catch error("WORKFLOW_PARAMETERS_JSON is not valid JSON")) as $user |
    def string_empty(v): v == null or (type == "string" and v == "");
    (
      ($catalog.entries // []) | map(select(.module == $m and .templateName == $t)) | .[0]
    ) as $entry
    | if $entry == null then error("catalog missing module=\($m) template=\($t)") else $entry end
    | . as $e
    | reduce ($e.parameters // [])[] as $p (
        {};
        . + {
          ($p.name): (
            if ($user | has($p.name) | not) then ($p.default // "")
            elif string_empty($user[$p.name]) then (if (($p.default // "") != "") then $p.default else "" end)
            else ($user[$p.name] | tostring)
            end
          )
        }
      )
    | {parameters: map_values(if . == null then "" else tostring end)}
    '
}

http_post_submit() {
  local path="$1"
  local json="$2"
  local base url code body
  base="$(api_base)"
  url="${base}${path}"
  body="$(curl -sS -w '\n%{http_code}' \
    -H "Authorization: Bearer ${BEARER}" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json' \
    -H 'X-Horizon-Submitted-From: rest-api' \
    -d "${json}" "${url}")"
  code="$(printf '%s' "${body}" | tail -n1)"
  body="$(printf '%s' "${body}" | sed '$d')"
  if [[ "${code}" == "401" ]]; then
    refresh_bearer
    body="$(curl -sS -w '\n%{http_code}' \
      -H "Authorization: Bearer ${BEARER}" \
      -H 'Content-Type: application/json' \
      -H 'Accept: application/json' \
      -H 'X-Horizon-Submitted-From: rest-api' \
      -d "${json}" "${url}")"
    code="$(printf '%s' "${body}" | tail -n1)"
    body="$(printf '%s' "${body}" | sed '$d')"
  fi
  if [[ "${code}" != "2"* ]]; then
    echo "horizon-workflow-via-rest: POST ${path} HTTP ${code} ${body}" >&2
    exit 1
  fi
  printf '%s' "${body}"
}

running_names_json() {
  local limit="$1"
  http_get "/v1/workflows/running?limit=${limit}"
}

name_in_list() {
  local name="$1"
  shift
  local n
  for n in "$@"; do
    [[ "${n}" == "${name}" ]] && return 0
  done
  return 1
}

workflow_parameters_json_payload() {
  # Prefer file from Jenkins (withEnv JSON is unreliable); else env (manual runs).
  local raw
  if [[ -n "${WORKFLOW_PARAMETERS_JSON_FILE:-}" && -r "${WORKFLOW_PARAMETERS_JSON_FILE}" ]]; then
    raw="$(cat "${WORKFLOW_PARAMETERS_JSON_FILE}")"
  else
    raw="${WORKFLOW_PARAMETERS_JSON:-{}}"
  fi
  printf '%s' "${raw}" | tr -d '\r'
}

cmd_submit() {
  refresh_bearer
  local catalog user_json body path mod_enc tpl_enc before_json after_json wf i
  catalog="$(http_get '/v1/catalog')"
  user_json="$(workflow_parameters_json_payload)"
  body="$(build_submit_body "${catalog}" "${user_json}" "${WORKFLOW_MODULE}" "${WORKFLOW_TEMPLATE}")"
  mod_enc="$(printf '%s' "${WORKFLOW_MODULE}" | jq -sRr @uri)"
  tpl_enc="$(printf '%s' "${WORKFLOW_TEMPLATE}" | jq -sRr @uri)"
  path="/v1/modules/${mod_enc}/workflowTemplates/${tpl_enc}/submit"

  before_json="$(running_names_json "${RUNNING_POLL_LIMIT}")"
  local -a before_a=()
  while IFS= read -r line; do
    [[ -n "${line}" ]] && before_a+=("${line}")
  done < <(printf '%s' "${before_json}" | jq -r '.items[]?.name // empty')

  http_post_submit "${path}" "${body}" >/dev/null

  wf=""
  for ((i = 0; i < WAIT_NEW_ATTEMPTS; i++)); do
    after_json="$(running_names_json "${RUNNING_POLL_LIMIT}")"
    while IFS= read -r n; do
      [[ -z "${n}" ]] && continue
      if ! name_in_list "${n}" "${before_a[@]}"; then
        wf="${n}"
        break
      fi
    done < <(printf '%s' "${after_json}" | jq -r '.items[]?.name // empty')
    [[ -n "${wf}" ]] && break
    sleep "${WAIT_NEW_SLEEP}"
  done
  if [[ -z "${wf}" ]]; then
    echo "horizon-workflow-via-rest: no new workflow in running list (check Argo Events)" >&2
    exit 1
  fi
  printf '%s' "${wf}"
}

workflow_phase() {
  local wf="$1"
  local enc
  enc="$(printf '%s' "${wf}" | jq -sRr @uri)"
  http_get "/v1/workflows/${enc}" | jq -r '.phase // empty'
}

has_pod_hint() {
  local wf="$1"
  local enc
  enc="$(printf '%s' "${wf}" | jq -sRr @uri)"
  http_get "/v1/workflows/${enc}" | jq -e '[.nodes[]? | .podName? // empty | select(length>0)] | length > 0' >/dev/null
}

wait_for_pod_hint_verbose() {
  local wf="$1"
  if [[ "${LOG_WAIT_POD_SECS}" -le 0 ]]; then
    echo "Logs: opening live stream for workflow ${wf} (pod wait disabled)." >&2
    return 0
  fi
  echo "Logs: waiting for workflow pods (workflow ${wf}, up to ${LOG_WAIT_POD_SECS}s) …" >&2
  local deadline=$((SECONDS + LOG_WAIT_POD_SECS))
  while (( SECONDS < deadline )); do
    if has_pod_hint "${wf}"; then
      echo "Logs: pods are visible; streaming output below." >&2
      return 0
    fi
    sleep 2
    echo "Logs: still waiting for pod assignment …" >&2
  done
  echo "Logs: opening stream anyway (pods may appear shortly)." >&2
}

terminal_phase() {
  case "$1" in
    Succeeded | Failed | Error | Aborted) return 0 ;;
    *) return 1 ;;
  esac
}

phase_to_exit() {
  case "$1" in
    Succeeded) return 0 ;;
    Aborted) return 2 ;;
    Failed | Error | "") return 1 ;;
    *) return 1 ;;
  esac
}

stream_logs() {
  local wf="$1"
  local enc ph follow url base
  enc="$(printf '%s' "${wf}" | jq -sRr @uri)"
  ph="$(workflow_phase "${wf}")"
  follow="true"
  if terminal_phase "${ph}"; then
    follow="false"
  fi
  base="$(api_base)"
  url="${base}/v1/workflows/${enc}/log?follow=${follow}&container=main"
  echo "━━ Horizon log stream ━━ ${url} (max-time=${LOG_STREAM_MAX_SECS}s follow=${follow} format=FORMATTED)" >&2
  # Do not capture jq in $(): POSIX strips trailing newlines and buffers output. Stream with jq --unbuffered.
  linebuf curl -sS -N --max-time "${LOG_STREAM_MAX_SECS}" \
    -H "Authorization: Bearer ${BEARER}" \
    -H 'Accept: application/x-ndjson' \
    -H 'Accept-Encoding: identity' \
    "${url}" | while IFS= read -r line || [[ -n "${line}" ]]; do
    [[ -z "${line// }" ]] && continue
    # shellcheck disable=SC2016
    # jq -r appends one newline after each emitted value; do not end strings with "\n" or lines double-space.
    jq -n --arg line "${line}" --unbuffered -r '
      def oneline(s): (s | tostring | gsub("\r"; "") | gsub("\n"; " "));
      ($line | try fromjson catch null) as $o |
      if $o == null then $line
      elif ($o | type) != "object" then $line
      elif $o.heartbeat then empty
      elif $o.result == "done" then
        (if ($o.workflowStatus != null) and ($o.workflowStatus != "") then $o.workflowStatus
         elif ($o.reason != null) and ($o.reason != "") then $o.reason else "done" end) as $s |
        (if ($o.detail != null) and ($o.detail != "") then $o.detail
         elif ($o.reason != null) then $o.reason else "-" end) as $m |
        "[\($s)] [-] [\(oneline($m))]"
      else
        (if ($o.ts != null) and ($o.ts != "") then $o.ts else "-" end) as $t |
        (if ($o.displayName != null) and ($o.displayName != "") then $o.displayName
         elif ($o.templateName != null) and ($o.templateName != "") then $o.templateName
         elif ($o.podName != null) and ($o.podName != "") then $o.podName else "log" end) as $st |
        (if $o.msg != null then $o.msg else "" end) as $msg |
        "[\($st)] [\($t)] [\(oneline($msg))]"
      end
    ' 2>/dev/null || printf '%s\n' "${line}"
  done
  printf '%s\n' '━━ log stream end ━━'
}

cmd_wait_logs() {
  local wf="$1"
  refresh_bearer
  local deadline=$((SECONDS + WAIT_TERMINAL_SECS))
  local ph="" log_pid

  (
    wait_for_pod_hint_verbose "${wf}"
    stream_logs "${wf}"
  ) &
  log_pid=$!

  ph=""
  while (( SECONDS < deadline )); do
    ph="$(workflow_phase "${wf}")"
    if terminal_phase "${ph}"; then
      break
    fi
    sleep "${TERMINAL_POLL_INTERVAL}"
  done

  kill "${log_pid}" 2>/dev/null || true
  wait "${log_pid}" 2>/dev/null || true

  if ! terminal_phase "${ph}"; then
    echo "horizon-workflow-via-rest: timeout waiting for terminal phase on ${wf}" >&2
    exit 1
  fi
  phase_to_exit "${ph}"
}

cmd_show() {
  local wf="$1"
  refresh_bearer
  local enc b
  enc="$(printf '%s' "${wf}" | jq -sRr @uri)"
  b="$(http_get "/v1/workflows/${enc}")"
  printf '%s\n' "${b}" | jq '
    {name, phase, workflowTemplate, submittedFrom, archivedLogs, outputArtifacts}
  '
  echo "━━ GCS / artifact URIs ━━"
  printf '%s\n' "${b}" | jq -r '
    .archivedLogs.combined.gcsUri? // empty | select(length>0) | "archivedLogs.combined: \(.)"
  '
  printf '%s\n' "${b}" | jq -r '
    (.archivedLogs.steps // [])[] | .gcsUri? // empty | select(length>0) | "archivedLogs.step: \(.)"
  '
  printf '%s\n' "${b}" | jq -r '
    (.outputArtifacts // [])[]? | "outputArtifact: \(.name) \(.gcsUri)"
  '
}

cmd_abort() {
  local wf="$1"
  refresh_bearer
  local enc base url code body
  enc="$(printf '%s' "${wf}" | jq -sRr @uri)"
  base="$(api_base)"
  url="${base}/v1/workflows/${enc}/abort"
  body="$(curl -sS -w '\n%{http_code}' -X POST -H "Authorization: Bearer ${BEARER}" -H 'Accept: application/json' "${url}")"
  code="$(printf '%s' "${body}" | tail -n1)"
  if [[ "${code}" == "401" ]]; then
    refresh_bearer
    body="$(curl -sS -w '\n%{http_code}' -X POST -H "Authorization: Bearer ${BEARER}" -H 'Accept: application/json' "${url}")"
    code="$(printf '%s' "${body}" | tail -n1)"
  fi
  if [[ "${code}" == "2"* ]] || [[ "${code}" == "204" ]]; then
    return 0
  fi
  echo "horizon-workflow-via-rest: abort HTTP ${code}" >&2
  return 1
}

case "${1:-}" in
  submit) cmd_submit ;;
  wait-logs) cmd_wait_logs "${2:-}" ;;
  show) cmd_show "${2:-}" ;;
  abort) cmd_abort "${2:-}" ;;
  *)
    echo "usage: $0 submit | wait-logs <workflowName> | show <workflowName> | abort <workflowName>" >&2
    exit 2
    ;;
esac
