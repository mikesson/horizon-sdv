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
# Smoke-test Horizon API: catalog → submit sample workflow → running/status/logs → abort path →
# more runs → history with archived log links (gs://) when the cluster exposes them.
#
# Prerequisites: curl, jq, python3 (strongly recommended for true live GET /v1/workflows/{name}/log streaming),
#   a valid Bearer token (Keycloak).
#
# Usage — authentication (pick one):
#
# A) CI / automation: confidential client horizon-api-ci (token refreshed before each HTTP call / log stream):
#   export KEYCLOAK_CLIENT_SECRET='…'
#   export HORIZON_API_BASE_URL="https://YOUR_DOMAIN/horizon-api"
#   ./tools/scripts/horizon-api/horizon-api-e2e-smoke.sh
#
# B) Human: public client horizon-api (device flow — browser login, no client secret); export token once:
#   export HORIZON_API_BASE_URL="https://YOUR_DOMAIN/horizon-api"
#   export HORIZON_ACCESS_TOKEN="$(./tools/scripts/horizon-api/horizon-api-get-token.sh --device)"
#   ./tools/scripts/horizon-api/horizon-api-e2e-smoke.sh
#   Long runs may need a fresh token (reuse --device or Swagger Authorize).
#
# C) Static JWT from anywhere (may expire on long runs / log streams):
#   export HORIZON_ACCESS_TOKEN="…"
#
# Optional:
#   MODULE=sample
#   TEMPLATE=sample-smoke-test
#   LOG_SAMPLE_SECS=90       # max time per GET /v1/workflows/{name}/log stream (seconds)
#   LOG_MAX_LINES=0          # stop after N ndjson lines (0 = no cap, only time)
#   LOG_WAIT_POD_SECS=0      # default 0: open workflow log stream immediately (best for live follow; increase if debugging)
#   LOG_FOLLOW_MODE=auto     # auto: follow=false if workflow already Succeeded/Failed/Error (Argo often sends no lines with follow=true on finished WFs); true|false to force
#   LOG_USE_PYTHON_STREAM=1  # 1 = stream logs via python3+subprocess (default if python3 exists); 0 = bash+curl only
#   WAIT_TERMINAL_SECS=600   # max wait for workflow to finish (history test)
#   RUNNING_POLL_LIMIT=500   # GET /v1/workflows/running?limit=… when discovering new runs (max 500; see script header comment)
#   WAIT_NEW_RUNNING_ATTEMPTS=120  # polls (× WAIT_NEW_RUNNING_SLEEP_SECS) after submit until a new name appears in running
#   WAIT_NEW_RUNNING_SLEEP_SECS=2
#   WAIT_AFTER_HIST_SUBMIT_SECS=2  # sleep after POST submit in step 8 (Argo Events / CR creation delay)
#   GLOB_PREFIX_LEN=20             # step 9c nameGlob prefix length (narrower pattern when cluster has many webhook-sm* terminals)
#   HISTORY_GLOB_TEST_LIMIT=500    # step 9c filtered history limit (must be high enough to reach WF_A in scan order)
#   SKIP_ABORT_TEST=true
#   SKIP_LONG_HISTORY=true   # skip extra runs + history archive inspection
#   REQUIRE_ARCHIVED_GCS=true  # fail if GET /v1/workflows/{name} has no gs:// links after terminal (needs gcs-artifact-bucket + template log artifacts)
#
# Why RUNNING_POLL_LIMIT matters: the API lists up to `limit` Workflow CRs from Kubernetes, then keeps only
# non-terminal phases. If the first page is mostly finished workflows, the new run may not be in the response
# at all when limit=50, and wait_for_new_running will time out ("no workflow for run …").

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GET_TOKEN_SH="${GET_TOKEN_SH:-${SCRIPT_DIR}/horizon-api-get-token.sh}"

MODULE="${MODULE:-sample}"
TEMPLATE="${TEMPLATE:-sample-smoke-test}"
HORIZON_API_BASE_URL="${HORIZON_API_BASE_URL:-}"
HORIZON_ACCESS_TOKEN="${HORIZON_ACCESS_TOKEN:-}"
KEYCLOAK_CLIENT_SECRET="${KEYCLOAK_CLIENT_SECRET:-}"
LOG_SAMPLE_SECS="${LOG_SAMPLE_SECS:-90}"
LOG_MAX_LINES="${LOG_MAX_LINES:-0}"
LOG_WAIT_POD_SECS="${LOG_WAIT_POD_SECS:-0}"
LOG_FOLLOW_MODE="${LOG_FOLLOW_MODE:-auto}"
WAIT_TERMINAL_SECS="${WAIT_TERMINAL_SECS:-600}"
RUNNING_POLL_LIMIT="${RUNNING_POLL_LIMIT:-500}"
WAIT_NEW_RUNNING_ATTEMPTS="${WAIT_NEW_RUNNING_ATTEMPTS:-120}"
WAIT_NEW_RUNNING_SLEEP_SECS="${WAIT_NEW_RUNNING_SLEEP_SECS:-2}"
WAIT_AFTER_HIST_SUBMIT_SECS="${WAIT_AFTER_HIST_SUBMIT_SECS:-2}"
SKIP_ABORT_TEST="${SKIP_ABORT_TEST:-false}"
SKIP_LONG_HISTORY="${SKIP_LONG_HISTORY:-false}"
REQUIRE_ARCHIVED_GCS="${REQUIRE_ARCHIVED_GCS:-false}"

die() { echo "error: $*" >&2; exit 1; }

# Print archivedLogs JSON and a flat list of gs:// URIs from GET /v1/workflows/{name}.
show_archived_gcs_for_workflow() {
  local wf="$1"
  local json
  json="$(api_get "/v1/workflows/${wf}")"
  echo "$json" | jq '{name, phase, workflowTemplate, archivedLogs}'
  echo "— gs:// URLs (from archivedLogs) —"
  echo "$json" | jq -r '
    .archivedLogs
    | if . == null then
        "  (no archivedLogs yet — workflow must be terminal; ensure horizon-api has --gcs-artifact-bucket when Argo omits bucket on artifacts; template should expose log-style GCS outputs e.g. main-logs)"
      else
        ((.combined // {}).gcsUri // empty | select(length > 0) | "  combined: \(.)"),
        (.steps // [])[] | select((.gcsUri // "") | length > 0) | "  step (\(.displayName // .templateName // "?")): \(.gcsUri)"
      end
    '
}

# Count gs:// lines extractable from archivedLogs object.
count_gs_urls_in_archived_payload() {
  local json="$1"
  echo "$json" | jq -r '
    .archivedLogs
    | if . == null then empty
      else
        ((.combined // {}).gcsUri // empty),
        (.steps // [])[] | .gcsUri // empty
      end
    | select(test("^gs://"))
  ' | wc -l | tr -d ' '
}

[[ -n "$HORIZON_API_BASE_URL" ]] || die "set HORIZON_API_BASE_URL (no trailing slash), e.g. https://env.example.com/horizon-api"
if [[ -n "${KEYCLOAK_CLIENT_SECRET}" ]]; then
  [[ -f "$GET_TOKEN_SH" ]] || die "missing get-token script: $GET_TOKEN_SH"
elif [[ -z "${HORIZON_ACCESS_TOKEN}" ]]; then
  die "set KEYCLOAK_CLIENT_SECRET (CI; token refresh), or HORIZON_ACCESS_TOKEN (human: run ${GET_TOKEN_SH} --device, or paste JWT from Swagger)"
fi

command -v curl >/dev/null || die "curl required"
command -v jq >/dev/null || die "jq required"
command -v python3 >/dev/null || die "python3 required (for line-buffered live workflow logs; install or use a host with python3)"

BASE="${HORIZON_API_BASE_URL%/}"

# When KEYCLOAK_CLIENT_SECRET is set, fetch a new access_token before each HTTP call / log stream (short-lived JWTs).
refresh_bearer_if_configured() {
  if [[ -n "${KEYCLOAK_CLIENT_SECRET}" ]]; then
    HORIZON_ACCESS_TOKEN="$(bash "$GET_TOKEN_SH")" || die "could not refresh access token (check KEYCLOAK_* and $GET_TOKEN_SH)"
  fi
}

# GET: refresh token, then GET; on 401 refresh once and retry.
api_get() {
  local path="$1"
  local attempt tmp code
  for attempt in 1 2; do
    refresh_bearer_if_configured
    tmp="$(mktemp)"
    code="$(curl -sS -o "$tmp" -w "%{http_code}" \
      -H "Authorization: Bearer ${HORIZON_ACCESS_TOKEN}" \
      -H "Accept: application/json" \
      "${BASE}${path}")" || true
    if [[ "$code" == "401" && "$attempt" == "1" && -n "${KEYCLOAK_CLIENT_SECRET}" ]]; then
      rm -f "$tmp"
      continue
    fi
    if [[ "$code" != "2"* ]]; then
      echo "error: GET ${path} HTTP ${code} $(head -c 400 "$tmp" | tr '\n' ' ')" >&2
      rm -f "$tmp"
      return 1
    fi
    cat "$tmp"
    rm -f "$tmp"
    return 0
  done
  return 1
}

# GET with HTTP code as last line of output (newline + code), for non-2xx assertions.
api_get_raw() {
  local path="$1"
  local attempt raw
  for attempt in 1 2; do
    refresh_bearer_if_configured
    raw="$(curl -sS -w "\n%{http_code}" \
      -H "Authorization: Bearer ${HORIZON_ACCESS_TOKEN}" \
      -H "Accept: application/json" \
      "${BASE}${path}")" || true
    local code
    code="$(printf '%s' "$raw" | tail -n 1)"
    if [[ "$code" == "401" && "$attempt" == "1" && -n "${KEYCLOAK_CLIENT_SECRET}" ]]; then
      continue
    fi
    printf '%s' "$raw"
    return 0
  done
}

# POST JSON: refresh, post, 401 retry once.
api_post() {
  local path="$1"
  local body="$2"
  local attempt tmp code
  for attempt in 1 2; do
    refresh_bearer_if_configured
    tmp="$(mktemp)"
    code="$(curl -sS -o "$tmp" -w "%{http_code}" -X POST \
      -H "Authorization: Bearer ${HORIZON_ACCESS_TOKEN}" \
      -H "Accept: application/json" \
      -H "Content-Type: application/json" \
      -d "${body}" \
      "${BASE}${path}")" || true
    if [[ "$code" == "401" && "$attempt" == "1" && -n "${KEYCLOAK_CLIENT_SECRET}" ]]; then
      rm -f "$tmp"
      continue
    fi
    if [[ "$code" != "2"* ]]; then
      echo "error: POST ${path} HTTP ${code} $(head -c 400 "$tmp" | tr '\n' ' ')" >&2
      rm -f "$tmp"
      return 1
    fi
    cat "$tmp"
    rm -f "$tmp"
    return 0
  done
  return 1
}

# POST with empty JSON body (abort); returns body + trailing line with http code (same as prior script).
api_post_raw() {
  local path="$1"
  local attempt raw
  for attempt in 1 2; do
    refresh_bearer_if_configured
    raw="$(curl -sS -w "\n%{http_code}" -X POST \
      -H "Authorization: Bearer ${HORIZON_ACCESS_TOKEN}" \
      -H "Accept: application/json" \
      -H "Content-Type: application/json" \
      -d '{}' \
      "${BASE}${path}")" || true
    local code
    code="$(printf '%s' "$raw" | tail -n 1)"
    if [[ "$code" == "401" && "$attempt" == "1" && -n "${KEYCLOAK_CLIENT_SECRET}" ]]; then
      continue
    fi
    printf '%s' "$raw"
    return 0
  done
}

running_names_json() {
  api_get "/v1/workflows/running?limit=${RUNNING_POLL_LIMIT}" | jq '[(.items // [])[] | .name]'
}

# Return names in $running that were not in $before_json (jq array of strings).
pick_new_workflow_name() {
  local before_json="$1"
  api_get "/v1/workflows/running?limit=${RUNNING_POLL_LIMIT}" | jq -r --argjson before "$before_json" '
    [(.items // [])[] | .name] as $now |
    $before as $b |
    $now | map(select(. as $n | $b | index($n) | not)) | .[0] // empty
  '
}

wait_for_new_running() {
  local before_json="$1"
  local attempts="${2:-$WAIT_NEW_RUNNING_ATTEMPTS}"
  local sleep_s="${3:-$WAIT_NEW_RUNNING_SLEEP_SECS}"
  local wf=""
  for ((i = 0; i < attempts; i++)); do
    wf="$(pick_new_workflow_name "$before_json")"
    if [[ -n "$wf" ]]; then
      echo "$wf"
      return 0
    fi
    sleep "$sleep_s"
  done
  return 1
}

wait_terminal_phase() {
  local wf="$1"
  local max_wait="${2:-$WAIT_TERMINAL_SECS}"
  local elapsed=0
  while (( elapsed < max_wait )); do
    local phase
    phase="$(api_get "/v1/workflows/${wf}" | jq -r '.phase // empty')"
    case "$phase" in
      Succeeded|Failed|Error|Aborted) echo "$phase"; return 0 ;;
    esac
    sleep 5
    elapsed=$((elapsed + 5))
  done
  die "timeout waiting for terminal phase on ${wf}"
}

# Poll workflow detail until at least one node has a podName (helps Argo log stream produce lines).
wait_for_workflow_pod_hint() {
  local wf="$1"
  local max_wait="${2:-$LOG_WAIT_POD_SECS}"
  local elapsed=0
  if (( max_wait <= 0 )); then
    echo "LOG_WAIT_POD_SECS=${max_wait:-0} — opening log stream immediately (live follow; horizon-api uses per-pod streams when workflow is non-terminal)."
    return 0
  fi
  echo "Waiting up to ${max_wait}s for pod-backed nodes on ${wf} …"
  while (( elapsed < max_wait )); do
    if api_get "/v1/workflows/${wf}" | jq -e '([(.nodes // [])[] | select(.podName != null and .podName != "")] | length) > 0' >/dev/null 2>&1; then
      echo "Pod node(s) present; starting log stream."
      return 0
    fi
    sleep 2
    elapsed=$((elapsed + 2))
  done
  echo "No pod nodes yet; attempting log stream anyway."
}

# Stream GET /v1/workflows/{wf}/log to stdout (NDJSON lines as they arrive).
# Bash `curl | while read` often buffers; `stdbuf` does not reliably affect static curl binaries.
# Python reads curl stdout with bufsize=0 and flushes every line so you see live logs.
stream_ndjson_logs() {
  local wf_name="$1"
  local max_secs="${2:-$LOG_SAMPLE_SECS}"
  local banner="${3:-}"
  local max_lines="${4:-${LOG_MAX_LINES:-0}}"
  max_lines="${max_lines:-0}"

  local follow_q="true"
  case "${LOG_FOLLOW_MODE}" in
    true|1|yes) follow_q="true" ;;
    false|0|no) follow_q="false" ;;
    auto|"")
      local ph
      ph="$(api_get "/v1/workflows/${wf_name}" | jq -r '.phase // empty')"
      case "$ph" in
        Succeeded|Failed|Error|Aborted)
          follow_q="false"
          echo "log stream: workflow phase=${ph} → follow=false (snapshot; Argo often returns no body with follow=true after completion)"
          ;;
        *)
          follow_q="true"
          echo "log stream: workflow phase=${ph:-'(pending)'} → follow=true (live)"
          ;;
      esac
      ;;
    *)
      die "LOG_FOLLOW_MODE must be auto, true, or false (got: ${LOG_FOLLOW_MODE})"
      ;;
  esac

  local url="${BASE}/v1/workflows/${wf_name}/log?follow=${follow_q}&container=main"
  echo ""
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo " LIVE  GET /v1/workflows/${wf_name}/log  ${banner}"
  echo "   ${url}"
  echo "   max-time=${max_secs}s  follow=${follow_q}  container=main  (line-buffered via python3+curl)"
  echo "   (NDJSON may include {\"heartbeat\":true} every ~30s while waiting on Argo — that means the connection is open.)"
  if (( max_lines > 0 )); then
    echo "   line cap=${max_lines} (then stop reading)"
  fi
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

  # Fresh token immediately before a long-lived stream (wait_for_pod may have consumed most of JWT TTL).
  refresh_bearer_if_configured

  local rc=0
  set +e
  if [[ "${LOG_USE_PYTHON_STREAM:-1}" != "0" ]]; then
    _E2E_LOG_URL="${url}" \
    _E2E_LOG_MAX_SECS="${max_secs}" \
    _E2E_LOG_MAX_LINES="${max_lines}" \
    _E2E_LOG_TOKEN="${HORIZON_ACCESS_TOKEN}" \
      python3 - <<'PY'
import os, subprocess, sys

url = os.environ["_E2E_LOG_URL"]
max_secs = os.environ["_E2E_LOG_MAX_SECS"]
max_lines = int(os.environ.get("_E2E_LOG_MAX_LINES") or "0")
token = os.environ["_E2E_LOG_TOKEN"]

cmd = [
    "curl",
    "-sS",
    "-N",
    "--no-buffer",
    "--http1.1",
    "--max-time",
    max_secs,
    "-H",
    "Authorization: Bearer " + token,
    "-H",
    "Accept: application/x-ndjson",
    # Avoid gzip on the stream so intermediaries less often buffer full chunks before decode.
    "-H",
    "Accept-Encoding: identity",
    url,
]
# stderr inherited so curl errors print live; stdout read line-by-line with flush
p = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=None, bufsize=0)
if p.stdout is None:
    sys.exit(1)
n = 0
try:
    while True:
        line = p.stdout.readline()
        if not line:
            break
        sys.stdout.buffer.write(line)
        sys.stdout.buffer.flush()
        n += 1
        if max_lines > 0 and n >= max_lines:
            p.terminate()
            break
finally:
    p.stdout.close()
rc = p.wait()
sys.exit(rc if isinstance(rc, int) else 1)
PY
    rc=$?
    unset _E2E_LOG_URL _E2E_LOG_MAX_SECS _E2E_LOG_MAX_LINES _E2E_LOG_TOKEN
  else
    # Fallback: no python path (should not hit: python3 is required above)
    curl -sS -N --http1.1 --max-time "${max_secs}" \
      -H "Authorization: Bearer ${HORIZON_ACCESS_TOKEN}" \
      -H "Accept: application/x-ndjson" \
      "${url}" || rc=$?
  fi
  set -e

  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo " END  GET /v1/workflows/${wf_name}/log  (stream helper exit: ${rc})"
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo ""
}

echo "== 1) GET /v1/catalog"
echo "   Test: Catalog lists modules and workflow templates the API advertises."
echo "   Expect: HTTP 200; response includes entry for MODULE=${MODULE} and TEMPLATE=${TEMPLATE} (else script exits)."
CATALOG="$(api_get "/v1/catalog")"
echo "$CATALOG" | jq '{count: (.entries|length), modules: [.entries[].module]|unique}'
echo "$CATALOG" | jq -e --arg m "$MODULE" --arg t "$TEMPLATE" \
  '.entries[] | select(.module==$m and .templateName==$t)' >/dev/null \
  || die "catalog missing module=${MODULE} template=${TEMPLATE} (enable module in Module Manager / sync sample-module)"

echo "== 2) GET /v1/workflows/running (retention echo)"
echo "   Test: Running-workflow list shape and retention hints from the API."
echo "   Expect: HTTP 200; JSON has items[] and retention (TTL / explanation echo)."
api_get "/v1/workflows/running" | jq '{item_count: ((.items // [])|length), retention}'

echo "== 3) Submit ${MODULE}/${TEMPLATE} with parameters"
echo "   Test: POST submit creates a new workflow instance (Argo CR) in the cluster."
echo "   Expect: HTTP 2xx from submit; a new name appears under GET /v1/workflows/running within the poll window."
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
NOTE="e2e-smoke-${STAMP}"
SUBMIT_BODY="$(jq -n \
  --arg env "e2e" \
  --arg bid "build-${STAMP}" \
  --arg note "$NOTE" \
  '{parameters: {sampleEnv: $env, sampleBuildId: $bid, sampleNote: $note}}')"
BEFORE_JSON="$(running_names_json)"
echo "Submit body: $SUBMIT_BODY"
api_post "/v1/modules/${MODULE}/workflowTemplates/${TEMPLATE}/submit" "$SUBMIT_BODY" | jq .
echo "Waiting for new workflow in /v1/workflows/running …"
WF="$(wait_for_new_running "$BEFORE_JSON")" || die "no new running workflow (check Argo Events webhook + Sensor)"
echo "Picked workflow name: ${WF}"

echo "== 4) GET /v1/workflows/{name}/log — live NDJSON (opens early; build+deploy horizon-api with dynamic per-pod follow)"
echo "   Test: Combined log stream for the submitted workflow (NDJSON; optional heartbeats while waiting)."
echo "   Expect: Stream opens (python3+curl); no hard failure — lines may be sparse depending on phase and Argo."
wait_for_workflow_pod_hint "${WF}"
stream_ndjson_logs "${WF}" "${LOG_SAMPLE_SECS}" "(abort path — before abort)"

echo "== 5) GET /v1/workflows/{name} (snapshot after log sample)"
echo "   Test: Workflow detail DTO after the log sample (phase, nodes, timestamps)."
echo "   Expect: HTTP 200; JSON includes name, phase, workflowTemplate, and nodes count."
api_get "/v1/workflows/${WF}" | jq '{name, phase, workflowTemplate, startedAt, finishedAt, node_count: (.nodes|length)}'

echo "== 5b) Wait for terminal phase (archivedLogs with gs:// appears after workflow finishes)"
echo "   Test: Workflow reaches a terminal API phase (Succeeded / Failed / Error / Aborted)."
echo "   Expect: Phase becomes terminal within WAIT_TERMINAL_SECS, or script was already terminal."
CUR_PHASE="$(api_get "/v1/workflows/${WF}" | jq -r '.phase // empty')"
if [[ "$CUR_PHASE" != "Succeeded" && "$CUR_PHASE" != "Failed" && "$CUR_PHASE" != "Error" && "$CUR_PHASE" != "Aborted" ]]; then
  PH="$(wait_terminal_phase "$WF")"
  echo "Terminal phase: ${PH}"
else
  echo "Already terminal: ${CUR_PHASE}"
fi

echo "== 5c) GET /v1/workflows/{name} — archivedLogs + gs:// list"
echo "   Test: Detail payload includes archivedLogs when the cluster/template exposes GCS log artifacts."
echo "   Expect: HTTP 200; print archivedLogs structure; if REQUIRE_ARCHIVED_GCS=true at least one gs:// URI or script dies."
show_archived_gcs_for_workflow "${WF}"
if [[ "$REQUIRE_ARCHIVED_GCS" == "true" ]]; then
  DET_JSON="$(api_get "/v1/workflows/${WF}")"
  n="$(count_gs_urls_in_archived_payload "$DET_JSON")"
  if [[ "${n:-0}" -lt 1 ]]; then
    die "REQUIRE_ARCHIVED_GCS=true but no gs:// URLs in archivedLogs for ${WF} (set gitops horizon-api gcsArtifactBucket; confirm template writes log artifacts to GCS)"
  fi
  echo "REQUIRE_ARCHIVED_GCS: OK (${n} gs:// URI(s))"
fi

if [[ "$SKIP_ABORT_TEST" == "true" ]]; then
  echo "== 6) SKIP_ABORT_TEST=true — waiting for workflow to finish instead of abort"
  echo "   Test: Abort path skipped; workflow may still be running until it finishes naturally."
  echo "   Expect: wait_terminal_phase returns a terminal phase (same timeout semantics as elsewhere)."
  PH="$(wait_terminal_phase "$WF")"
  echo "Terminal phase: ${PH}"
else
  echo "== 6) POST /v1/workflows/{name}/abort"
  echo "   Test: Graceful shutdown request for a non-terminal workflow (horizon-api patches spec.shutdown)."
  echo "   Expect: HTTP 202 Accepted; JSON {\"status\":\"aborting\"} (then brief sleep before second call)."
  raw="$(api_post_raw "/v1/workflows/${WF}/abort")"
  code="$(printf '%s' "$raw" | tail -n 1)"
  body="$(printf '%s' "$raw" | sed '$d')"
  echo "HTTP ${code}"
  echo "$body" | jq . 2>/dev/null || echo "$body"
  sleep 5
  echo "== 7) Second abort (expect 409 if already terminal)"
  echo "   Test: Idempotence — second abort when workflow is already terminal."
  echo "   Expect: HTTP 409 Conflict (or still success if cluster lags); code printed for inspection."
  raw="$(api_post_raw "/v1/workflows/${WF}/abort")"
  code2="$(printf '%s' "$raw" | tail -n 1)"
  echo "HTTP ${code2}"
fi

if [[ "$SKIP_LONG_HISTORY" == "true" ]]; then
  echo "== 8–10) SKIP_LONG_HISTORY=true — done"
  echo "   Test: Long-running history block (extra submits, filtered history, gs:// sweep) skipped."
  echo "   Expect: Clean exit 0 after steps 1–7 (or 1–6 when SKIP_ABORT_TEST)."
  exit 0
fi

# Populated in run_and_wait (step 8) for history filter assertions.
E2E_HIST_WORKFLOWS=()

echo "== 8) Submit two more workflows; wait until terminal (for history + archivedLogs)"
echo "   Test: Two independent submit→run→terminal sequences (names recorded for step 9 filters)."
echo "   Expect: Two new workflows reach terminal phase; E2E_HIST_WORKFLOWS has two names."
run_and_wait() {
  local suffix="$1"
  local before
  before="$(running_names_json)"
  local body
  body="$(jq -n \
    --arg env "e2e" \
    --arg bid "hist-${suffix}" \
    --arg note "e2e-history-${suffix}" \
    '{parameters: {sampleEnv: $env, sampleBuildId: $bid, sampleNote: $note}}')"
  api_post "/v1/modules/${MODULE}/workflowTemplates/${TEMPLATE}/submit" "$body" >/dev/null
  if [[ "${WAIT_AFTER_HIST_SUBMIT_SECS}" != "0" ]]; then
    sleep "${WAIT_AFTER_HIST_SUBMIT_SECS}"
  fi
  local wname
  wname="$(wait_for_new_running "$before")" || die "no workflow for run ${suffix} (check RUNNING_POLL_LIMIT, Argo Events, cluster workflows namespace; see script header)"
  E2E_HIST_WORKFLOWS+=("$wname")
  echo "Started ${wname}."
  wait_for_workflow_pod_hint "${wname}"
  stream_ndjson_logs "${wname}" "${LOG_SAMPLE_SECS}" "(history run ${suffix} — while still running)"
  echo "  ${wname}: waiting for terminal…"
  local ph
  ph="$(wait_terminal_phase "$wname")"
  echo "  ${wname} → ${ph}"
}

run_and_wait "a-$(date +%s)"
run_and_wait "b-$(date +%s)"

echo "== 9) GET /v1/workflows/history (pagination + retention)"
echo "   Test: Unfiltered history list uses Kubernetes-style limit + optional continue token."
echo "   Expect: HTTP 200; items are terminal rows; retention echoed; continue may be non-empty if more pages exist."
api_get "/v1/workflows/history?limit=15" | jq '{retention, continue, names: [(.items // [])[].name], phases: [(.items // [])[].phase]}'

echo "== 9b) Filtered history includes truncated + scanned; comma-separated phase"
echo "   Test: Filtered history scan (phase query); response must include scan metadata."
echo "   Expect: truncated (bool) and scanned (number); items length ≤ limit; jq assertions pass."
api_get "/v1/workflows/history?limit=8&phase=succeeded,failed,error,aborted" \
  | jq -e '
      (.truncated | type) == "boolean"
      and ((.scanned | type) == "number")
      and ((.items // []) | length) <= 8
    ' >/dev/null \
  || die "filtered history must expose truncated, scanned, and respect limit"
api_get "/v1/workflows/history?limit=8&phase=succeeded" \
  | jq '{truncated, scanned, count: ((.items // []) | length), sample: [(.items // [])[:3][].name]}'

WF_A="${E2E_HIST_WORKFLOWS[0]:-}"
WF_B="${E2E_HIST_WORKFLOWS[1]:-}"
[[ -n "$WF_A" && -n "$WF_B" ]] || die "expected two workflow names from step 8 for filter checks"

echo "== 9c) History filters — nameRegex (exact) and nameGlob (prefix*)"
echo "   Test: nameRegex=^wf$ per known workflow; nameGlob=prefix* must still return WF_A."
echo "   Expect: Each exact regex returns its workflow. Glob uses a long prefix + high limit so a broad pattern (e.g. webhook-sm*) cannot fill the result cap before the scan reaches WF_A."
history_exact_regex_q() {
  local name="$1"
  local esc
  esc="$(printf '%s' "$name" | sed 's/[.^$*+?()[\]{}|]/\\&/g')"
  jq -nr --arg r "^${esc}\$" '$r|@uri'
}
for w in "$WF_A" "$WF_B"; do
  RX_Q="$(history_exact_regex_q "$w")"
  api_get "/v1/workflows/history?limit=10&nameRegex=${RX_Q}" \
    | jq -e --arg n "$w" '[(.items // [])[].name] | index($n) != null' >/dev/null \
    || die "nameRegex exact match should return workflow ${w}"
done

# Filtered history stops after `limit` matches; a short prefix + busy cluster can return 20 other
# terminal workflows matching e.g. webhook-sm* before WF_A appears in list order — use a longer prefix
# and API max limit for this assertion.
GLOB_PREFIX_LEN="${GLOB_PREFIX_LEN:-20}"
HISTORY_GLOB_TEST_LIMIT="${HISTORY_GLOB_TEST_LIMIT:-500}"
if [[ ${#WF_A} -gt "$GLOB_PREFIX_LEN" ]]; then
  GLOB_PREFIX="${WF_A:0:GLOB_PREFIX_LEN}"
else
  # Keep at least one character for '*' so we still exercise glob (not exact equality only).
  GLOB_PREFIX="${WF_A:0:$((${#WF_A} > 1 ? ${#WF_A} - 1 : 1))}"
fi
GLOB_Q="$(jq -nr --arg g "${GLOB_PREFIX}*" '$g|@uri')"
api_get "/v1/workflows/history?limit=${HISTORY_GLOB_TEST_LIMIT}&nameGlob=${GLOB_Q}" \
  | jq -e --arg n "$WF_A" '[(.items // [])[].name] | index($n) != null' >/dev/null \
  || die "nameGlob ${GLOB_PREFIX}* should include ${WF_A} (increase HISTORY_GLOB_TEST_LIMIT or GLOB_PREFIX_LEN if cluster had many matches before this workflow in scan order)"

echo "== 9d) History filters — startedAt / finishedAt bounds"
echo "   Test: startedAfter / startedBefore / finishedAfter / finishedBefore (RFC3339) with nameRegex pin."
echo "   Expect: Boundary times still match WF_A; impossible future bounds yield zero items for that name."
STARTED="$(api_get "/v1/workflows/${WF_A}" | jq -r '.startedAt // empty')"
FINISHED="$(api_get "/v1/workflows/${WF_A}" | jq -r '.finishedAt // empty')"
[[ -n "$STARTED" && -n "$FINISHED" ]] || die "terminal workflow ${WF_A} should have startedAt and finishedAt"
SA="$(jq -nr --arg s "$STARTED" '$s|@uri')"
SB="$(jq -nr --arg s "$FINISHED" '$s|@uri')"
RX_A_Q="$(history_exact_regex_q "$WF_A")"
api_get "/v1/workflows/history?limit=5&nameRegex=${RX_A_Q}&startedAfter=${SA}" \
  | jq -e --arg n "$WF_A" '[(.items // [])[].name] | index($n) != null' >/dev/null \
  || die "startedAfter=workflow startedAt should still match (>=)"
api_get "/v1/workflows/history?limit=5&nameRegex=${RX_A_Q}&finishedBefore=${SB}" \
  | jq -e --arg n "$WF_A" '[(.items // [])[].name] | index($n) != null' >/dev/null \
  || die "finishedBefore=workflow finishedAt should still match (<=)"
api_get "/v1/workflows/history?limit=5&nameRegex=${RX_A_Q}&startedAfter=2099-12-31T23:59:59Z" \
  | jq -e '((.items // []) | length) == 0' >/dev/null \
  || die "startedAfter in the future should yield no rows for this workflow"
api_get "/v1/workflows/history?limit=5&nameRegex=${RX_A_Q}&finishedAfter=2099-12-31T23:59:59Z" \
  | jq -e '((.items // []) | length) == 0' >/dev/null \
  || die "finishedAfter in the future should yield no rows for this workflow"

echo "== 9e) History filters — validation errors (expect HTTP 400)"
echo "   Test: Bad regex, inverted started/finished windows, continue+filter combination."
echo "   Expect: HTTP 400 for invalid nameRegex and inconsistent time bounds; 400 when continue paired with phase if continue token exists."
raw_bad_rx="$(api_get_raw '/v1/workflows/history?limit=3&nameRegex=(unclosed')"
code_bad_rx="$(printf '%s' "$raw_bad_rx" | tail -n 1)"
[[ "$code_bad_rx" == "400" ]] || die "invalid nameRegex should return 400 (got ${code_bad_rx})"

raw_range="$(api_get_raw '/v1/workflows/history?limit=3&startedAfter=2099-01-01T00:00:00Z&startedBefore=2000-01-01T00:00:00Z')"
code_range="$(printf '%s' "$raw_range" | tail -n 1)"
[[ "$code_range" == "400" ]] || die "startedAfter > startedBefore should return 400 (got ${code_range})"

raw_frange="$(api_get_raw '/v1/workflows/history?limit=3&finishedAfter=2099-01-01T00:00:00Z&finishedBefore=2000-01-01T00:00:00Z')"
code_frange="$(printf '%s' "$raw_frange" | tail -n 1)"
[[ "$code_frange" == "400" ]] || die "finishedAfter > finishedBefore should return 400 (got ${code_frange})"

CONT="$(api_get "/v1/workflows/history?limit=1" | jq -r '.continue // empty')"
if [[ -n "$CONT" ]]; then
  CONT_Q="$(jq -nr --arg c "$CONT" '$c|@uri')"
  raw_cont="$(api_get_raw "/v1/workflows/history?limit=5&phase=succeeded&continue=${CONT_Q}")"
  code_cont="$(printf '%s' "$raw_cont" | tail -n 1)"
  [[ "$code_cont" == "400" ]] || die "continue + filter should return 400 (got ${code_cont})"
else
  echo "  (skip continue+filter: empty continue from history?limit=1 — not enough pages)"
fi

echo "== 10) Recent history — archivedLogs summary + flat gs:// lines"
echo "   Test: Unfiltered history page shows archivedLogs and gs:// URIs when templates write log artifacts."
echo "   Expect: HTTP 200; jq prints per-item archive summary and a flat list of gs:// lines (may be empty)."
HIST_JSON="$(api_get "/v1/workflows/history?limit=20")"
echo "$HIST_JSON" | jq '.items[:8] | map({
  name,
  phase,
  workflowTemplate,
  combined: (.archivedLogs.combined.gcsUri // null),
  stepLogUris: [(.archivedLogs.steps // [])[] | select((.gcsUri // "") | test("^gs://")) | {template: .templateName, display: .displayName, gcsUri: .gcsUri}]
})'
echo "— All gs:// in this history page —"
echo "$HIST_JSON" | jq -r '
  (.items // [])[] | . as $i | $i.archivedLogs
  | if . == null then empty
    else
      ((.combined // {}).gcsUri // empty | "\($i.name) combined: \(.)"),
      (.steps // [])[] | (.gcsUri // empty) | select(test("^gs://")) | "\($i.name) step: \(.)"
    end
'

echo "Done."
