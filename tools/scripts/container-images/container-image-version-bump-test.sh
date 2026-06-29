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
# Probe flagged OS packages in container images from the security-upgrade worked
# example (14 images). Run at any time for any registry tag; save output with -o
# and diff snapshots yourself when comparing before/after upgrades.
#
# Companion guide: docs/guides/container_image_security_upgrade_guide.md (Section #4)
#
# Prerequisites: kubectl, gcloud (Connect Gateway cluster credentials).
#
# Usage:
#   ./tools/scripts/container-images/container-image-version-bump-test.sh probe --tag 1.0.1
#   ./tools/scripts/container-images/container-image-version-bump-test.sh probe --tag 1.0.1 -o probe_1.0.1.txt
#   ./tools/scripts/container-images/container-image-version-bump-test.sh argocd --tag 1.0.1
#   ./tools/scripts/container-images/container-image-version-bump-test.sh spot-check --tag 1.0.1
#   ./tools/scripts/container-images/container-image-version-bump-test.sh cleanup

set -euo pipefail

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------
NS_PROBE="cve-probe"

# All 14 flagged images (edit per your CVE batch — see security upgrade guide).
ALPINE_IMAGES="landingpage-app keycloak-post keycloak-post-gerrit keycloak-post-jenkins keycloak-post-argocd keycloak-post-grafana keycloak-post-headlamp keycloak-post-mcp-gateway-registry keycloak-post-mtk-connect grafana-post"
DEBIAN_IMAGES="gerrit-post gerrit-mcp-server-app mtk-connect-post mtk-connect-post-key"

APROBE='apk list -I 2>/dev/null | grep -Eio "^(openssl|libssl3|libcrypto3|curl|vim|openssh-client|openssh-keygen|expat|libexpat|libpng|nginx)-[^ ]*" | sort -u; echo --bin--; openssl version 2>/dev/null'
DPROBE='dpkg -l 2>/dev/null | grep -Ei "openssl|libssl|python3" | tr -s " "; echo --bin--; openssl version 2>/dev/null; python3 --version 2>&1'

# ---------------------------------------------------------------------------
# Defaults (override via flags or environment)
# ---------------------------------------------------------------------------
PROJECT="${PROJECT:-}"
REGION="${REGION:-}"
CTX="${CTX:-}"
TAG="${TAG:-}"
BASE="${BASE:-}"
OUTPUT_FILE="${OUTPUT_FILE:-}"
WITH_ARGOCD=false
GERRIT_NS="${GERRIT_NS:-gerrit}"
HORIZON_NS="${HORIZON_NS:-horizon}"
MTK_CONNECT_NS="${MTK_CONNECT_NS:-mtk-connect}"
MTK_CONNECT_CRONJOB="${MTK_CONNECT_CRONJOB:-mtk-connect-api-key-config}"
SKIP_CRONJOB=false
COMMAND=""

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
usage() {
  local self
  self="$(basename "$0")"
  cat <<EOF
Usage: ${self} <command> [options]

Commands:
  probe        Probe all 14 flagged images at a tag (packages in registry image)
  argocd       Argo CD sync/health; optional workload list for --tag
  spot-check   Live pod / CronJob checks for workloads at --tag
  cleanup      Delete the ephemeral probe namespace (${NS_PROBE})

Options (or environment variables):
  --project <id>         GCP project (PROJECT) — required for probe
  --region <region>      Artifact Registry region (REGION) — required for probe
  --context <ctx>        kubectl context (CTX)
  --tag <tag>            Image tag (TAG) — required for probe and spot-check
  --base <path>          Registry base (BASE); default: \${REGION}-docker.pkg.dev/\${PROJECT}/horizon-sdv
  -o, --output <file>    Save probe output to file
  --with-argocd          Include Argo CD desired image list (probe only)
  --gerrit-ns <ns>       gerrit-mcp-server namespace (default: gerrit)
  --horizon-ns <ns>      landingpage namespace (default: horizon)
  --mtk-connect-ns <ns>  mtk-connect namespace (default: mtk-connect)
  --mtk-cronjob <name>   CronJob for spot-check (default: mtk-connect-api-key-config)
  --skip-cronjob         Skip forced CronJob in spot-check
  -h, --help             Show this help

Example:
  export PROJECT=my-project REGION=us-central1
  gcloud container fleet memberships get-credentials sdv-cluster --project="\$PROJECT"
  export CTX="connectgateway_\${PROJECT}_\${REGION}_sdv-cluster"

  ${self} probe --tag 1.0.0 --with-argocd -o before_1.0.0.txt
  ${self} probe --tag 1.0.1 -o after_1.0.1.txt
  diff -u before_1.0.0.txt after_1.0.1.txt
  ${self} argocd --tag 1.0.1
  ${self} spot-check --tag 1.0.1
  ${self} cleanup
EOF
}

die() {
  echo "ERROR: $*" >&2
  exit 1
}

require_nonempty() {
  local name="$1" value="$2"
  [[ -n "$value" ]] || die "${name} is required (pass --${name,,} or set ${name^^})."
}

use_kube_context() {
  if [[ -n "$CTX" ]]; then
    kubectl config use-context "$CTX" >/dev/null
  fi
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project) PROJECT="${2:-}"; shift 2 ;;
      --region) REGION="${2:-}"; shift 2 ;;
      --context) CTX="${2:-}"; shift 2 ;;
      --tag) TAG="${2:-}"; shift 2 ;;
      --base) BASE="${2:-}"; shift 2 ;;
      --output|-o) OUTPUT_FILE="${2:-}"; shift 2 ;;
      --with-argocd) WITH_ARGOCD=true; shift ;;
      --gerrit-ns) GERRIT_NS="${2:-}"; shift 2 ;;
      --horizon-ns) HORIZON_NS="${2:-}"; shift 2 ;;
      --mtk-connect-ns) MTK_CONNECT_NS="${2:-}"; shift 2 ;;
      --mtk-cronjob) MTK_CONNECT_CRONJOB="${2:-}"; shift 2 ;;
      --skip-cronjob) SKIP_CRONJOB=true; shift ;;
      -h|--help) usage; exit 0 ;;
      *) die "Unknown option: $1 (try --help)" ;;
    esac
  done
}

resolve_registry_base() {
  require_nonempty PROJECT "$PROJECT"
  require_nonempty REGION "$REGION"
  [[ -n "$BASE" ]] || BASE="${REGION}-docker.pkg.dev/${PROJECT}/horizon-sdv"
}

# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------
print_argocd_images() {
  echo "=== Argo CD image references (desired state) ==="
  kubectl -n argocd get application horizon-sdv \
    -o jsonpath='{.spec.source.helm.values}' \
    | grep -Eo '[a-z0-9.-]+-docker\.pkg\.dev/[^ ]+' | sort -u || true
  echo
}

probe_tag() {
  local tag="$1"
  local outfile="${2:-}"

  kubectl delete ns "$NS_PROBE" --ignore-not-found --wait=true >/dev/null 2>&1
  kubectl create ns "$NS_PROBE" >/dev/null 2>&1
  kubectl label ns "$NS_PROBE" pod-security.kubernetes.io/enforce=privileged --overwrite >/dev/null 2>&1

  launch() {
    kubectl -n "$NS_PROBE" run "probe-$1" \
      --image="${BASE}/$1:${tag}" \
      --image-pull-policy=Always \
      --restart=Never \
      --command -- sh -c "$2" >/dev/null 2>&1
  }

  for img in $ALPINE_IMAGES; do launch "$img" "$APROBE"; done
  for img in $DEBIAN_IMAGES; do launch "$img" "$DPROBE"; done

  local n left
  for n in $(seq 1 60); do
    left=$(kubectl -n "$NS_PROBE" get pods --no-headers 2>/dev/null \
      | awk '$3 != "Completed" && $3 != "Error" { c++ } END { print c + 0 }')
    [[ "${left:-0}" -eq 0 ]] && break
    sleep 5
  done

  local failed
  failed=$(kubectl -n "$NS_PROBE" get pods --no-headers 2>/dev/null \
    | grep -E 'ImagePullBackOff|ErrImagePull|CrashLoopBackOff' || true)
  if [[ -n "$failed" ]]; then
    echo "WARNING: probe pods failed to run:" >&2
    echo "$failed" | sed 's/^/    /' >&2
  fi

  {
    for img in $ALPINE_IMAGES $DEBIAN_IMAGES; do
      echo "===== ${img} (${tag}) ====="
      if kubectl -n "$NS_PROBE" get pod "probe-${img}" >/dev/null 2>&1; then
        kubectl -n "$NS_PROBE" logs "probe-${img}" 2>&1 | sed 's/^/    /'
      else
        echo "    (pod probe-${img} not found)"
      fi
    done
  } | if [[ -n "$outfile" ]]; then tee "$outfile"; else cat; fi

  kubectl delete ns "$NS_PROBE" --wait=false >/dev/null 2>&1
}

cmd_probe() {
  resolve_registry_base
  require_nonempty TAG "$TAG"
  use_kube_context

  if [[ "$WITH_ARGOCD" == true ]]; then
    print_argocd_images
  fi

  echo "=== Probing 14 flagged images at TAG=${TAG} ==="
  probe_tag "$TAG" "$OUTPUT_FILE"
  if [[ -n "$OUTPUT_FILE" ]]; then
    echo
    echo "Saved probe output to: ${OUTPUT_FILE}"
  fi
}

cmd_argocd() {
  use_kube_context

  echo "=== Argo CD parent application ==="
  kubectl -n argocd get application horizon-sdv \
    -o jsonpath='sync={.status.sync.status} health={.status.health.status} rev={.status.sync.revision}{"\n"}'
  kubectl -n argocd get application horizon-sdv \
    -o jsonpath='{range .status.conditions[*]}{.type}: {.message}{"\n"}{end}' || true
  echo

  if [[ -z "$TAG" ]]; then
    echo "(Pass --tag to list workloads referencing a specific image tag)"
    return 0
  fi

  echo "=== Workloads referencing TAG=${TAG} ==="
  for kind in deployment cronjob job; do
    kubectl get "$kind" -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"  "}{range .spec.template.spec.containers[*]}{.image}{" "}{end}{range .spec.jobTemplate.spec.template.spec.containers[*]}{.image}{" "}{end}{"\n"}{end}' 2>/dev/null || true
  done | grep -E ":${TAG}([^[:alnum:].-]|$)" || echo "(no matches — check sync status and deploy_version)"
}

cmd_spot_check() {
  require_nonempty TAG "$TAG"
  use_kube_context

  echo "=== Live Deployment: landingpage (${HORIZON_NS}) ==="
  local pod
  pod=$(kubectl -n "$HORIZON_NS" get pods -l app.kubernetes.io/name=landingpage \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  if [[ -z "$pod" ]]; then
    echo "    (no landingpage pod in ${HORIZON_NS})"
  else
    kubectl -n "$HORIZON_NS" get pod "$pod" -o jsonpath='image={.spec.containers[0].image}{"\n"}'
    kubectl -n "$HORIZON_NS" exec "$pod" -- sh -c \
      'apk list -I 2>/dev/null | grep -Eio "^(curl|libssl3|libcrypto3|libexpat|libpng|nginx)-[^ ]*" | sort -u; echo --bin--; nginx -v 2>&1' \
      2>&1 | sed 's/^/    /'
  fi
  echo

  echo "=== Live Deployment: gerrit-mcp-server (${GERRIT_NS}) ==="
  pod=$(kubectl -n "$GERRIT_NS" get pods -l app.kubernetes.io/name=gerrit-mcp-server \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  if [[ -z "$pod" ]]; then
    echo "    (no gerrit-mcp-server pod in ${GERRIT_NS})"
  else
    kubectl -n "$GERRIT_NS" get pod "$pod" -o jsonpath='image={.spec.containers[0].image}{"\n"}'
    kubectl -n "$GERRIT_NS" exec "$pod" -- sh -c \
      'openssl version; dpkg -l 2>/dev/null | grep -Ei "libssl|libcurl" | tr -s " "' 2>&1 | sed 's/^/    /'
  fi
  echo

  if [[ "$SKIP_CRONJOB" == true ]]; then
    echo "=== CronJob spot-check skipped (--skip-cronjob) ==="
    return 0
  fi

  echo "=== Forced CronJob: ${MTK_CONNECT_CRONJOB} (${MTK_CONNECT_NS}) ==="
  local job_name="verify-${TAG//./-}"
  if ! kubectl -n "$MTK_CONNECT_NS" get cronjob "$MTK_CONNECT_CRONJOB" >/dev/null 2>&1; then
    echo "    (CronJob ${MTK_CONNECT_CRONJOB} not found)"
    return 0
  fi

  kubectl -n "$MTK_CONNECT_NS" delete job "$job_name" --ignore-not-found >/dev/null 2>&1
  kubectl -n "$MTK_CONNECT_NS" create job --from="cronjob/${MTK_CONNECT_CRONJOB}" "$job_name"
  kubectl -n "$MTK_CONNECT_NS" get job "$job_name" \
    -o jsonpath='image={.spec.template.spec.containers[0].image}{"\n"}'
  kubectl -n "$MTK_CONNECT_NS" wait --for=condition=complete "job/${job_name}" --timeout=180s
  kubectl -n "$MTK_CONNECT_NS" logs "job/${job_name}" 2>&1 | tail -20 | sed 's/^/    /'
  kubectl -n "$MTK_CONNECT_NS" delete job "$job_name" --ignore-not-found >/dev/null 2>&1
}

cmd_cleanup() {
  use_kube_context
  kubectl delete ns "$NS_PROBE" --ignore-not-found >/dev/null 2>&1
  echo "Removed probe namespace ${NS_PROBE} (if present)."
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
main() {
  [[ $# -ge 1 ]] || { usage; exit 1; }

  case "${1:-}" in
    -h|--help) usage; exit 0 ;;
  esac

  COMMAND="$1"
  shift
  parse_args "$@"

  case "$COMMAND" in
    probe) cmd_probe ;;
    argocd) cmd_argocd ;;
    spot-check) cmd_spot_check ;;
    cleanup) cmd_cleanup ;;
    *) die "Unknown command: ${COMMAND} (try --help)" ;;
  esac
}

main "$@"
