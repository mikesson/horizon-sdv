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
# Build horizon-api-app (Docker), push to Artifact Registry, restart the Deployment.
#
# Prerequisites: docker, gcloud (authenticated), kubectl (context set), jq optional for version parse.
#
# Usage:
#   export HORIZON_API_NAMESPACE="sbx-horizon-api"   # Kubernetes namespace (required)
#   ./tools/scripts/horizon-api/horizon-api-build-push-restart.sh
#
# Optional:
#   DEPLOYMENT_NAME=horizon-api
#   CONTAINER_NAME=horizon-api
#   SKIP_BUILD=true          # only restart (set FULL_IMAGE_URI)
#   SKIP_PUSH=true           # passed to build.sh
#   NO_CACHE=true            # passed to build.sh
#   FULL_IMAGE_URI=...       # override image for kubectl set image (default: computed from tfvars + locals)
#
# Terraform-derived defaults (same as tools/scripts/container-images/build.sh):
#   terraform/env/terraform.tfvars  → project, region
#   terraform/env/main.tf           → registry id
#   terraform/modules/base/locals.tf → horizon-api-app build_version

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
BUILD_SH="${REPO_ROOT}/tools/scripts/container-images/build.sh"
LOCALS_FILE="${REPO_ROOT}/terraform/modules/base/locals.tf"
TFVARS_FILE="${REPO_ROOT}/terraform/env/terraform.tfvars"
ENV_MAIN_FILE="${REPO_ROOT}/terraform/env/main.tf"

DEPLOYMENT_NAME="${DEPLOYMENT_NAME:-horizon-api}"
CONTAINER_NAME="${CONTAINER_NAME:-horizon-api}"
SKIP_BUILD="${SKIP_BUILD:-false}"

die() { echo "error: $*" >&2; exit 1; }

[[ -n "${HORIZON_API_NAMESPACE:-}" ]] || die "set HORIZON_API_NAMESPACE (e.g. sbx-horizon-api)"

load_tfvars() {
  if [[ -f "$TFVARS_FILE" ]]; then
    GCP_PROJECT="${GCP_PROJECT:-$(awk -F'"' '/sdv_gcp_project_id[[:space:]]*=/ {print $2; exit}' "$TFVARS_FILE")}"
    GCP_REGION="${GCP_REGION:-$(awk -F'"' '/sdv_gcp_region[[:space:]]*=/ {print $2; exit}' "$TFVARS_FILE")}"
  fi
  if [[ -f "$ENV_MAIN_FILE" ]]; then
    REGISTRY_ID="${REGISTRY_ID:-$(awk -F'"' '/sdv_artifact_registry_repository_id[[:space:]]*=/ {print $2; exit}' "$ENV_MAIN_FILE")}"
  fi
  if [[ -z "${GCP_PROJECT:-}" ]] && command -v gcloud >/dev/null 2>&1; then
    GCP_PROJECT="$(gcloud config get-value project 2>/dev/null || true)"
  fi
  if [[ -z "${GCP_REGION:-}" ]] && command -v gcloud >/dev/null 2>&1; then
    GCP_REGION="$(gcloud config get-value compute/region 2>/dev/null || true)"
  fi
  [[ -n "${GCP_PROJECT:-}" ]] || die "set GCP_PROJECT or sdv_gcp_project_id in ${TFVARS_FILE} (file missing?)"
  [[ -n "${GCP_REGION:-}" ]] || die "set GCP_REGION or sdv_gcp_region in ${TFVARS_FILE} (file missing?)"
  [[ -n "${REGISTRY_ID:-}" ]] || die "set REGISTRY_ID or sdv_artifact_registry_repository_id in ${ENV_MAIN_FILE}"
}

horizon_api_version() {
  if [[ ! -f "$LOCALS_FILE" ]]; then
    die "missing ${LOCALS_FILE}"
  fi
  python3 - "$LOCALS_FILE" <<'PY'
import re, sys
text = open(sys.argv[1], encoding="utf-8").read()
# First block for "horizon-api-app" = { ... build_version = "x" ... }
m = re.search(
    r'"horizon-api-app"\s*=\s*\{[^}]*?build_version\s*=\s*"([^"]+)"',
    text,
    re.DOTALL,
)
print(m.group(1) if m else "1.0.0")
PY
}

load_tfvars
VERSION="$(horizon_api_version)"
REGISTRY_HOST="${GCP_REGION}-docker.pkg.dev"
DEFAULT_IMAGE="${REGISTRY_HOST}/${GCP_PROJECT}/${REGISTRY_ID}/horizon-api-app:${VERSION}"
FULL_IMAGE_URI="${FULL_IMAGE_URI:-$DEFAULT_IMAGE}"

if [[ "$SKIP_BUILD" != "true" ]]; then
  [[ -x "$BUILD_SH" || -f "$BUILD_SH" ]] || die "missing build script: $BUILD_SH"
  build_args=(-i horizon-api-app -p "$GCP_PROJECT" -r "$GCP_REGION" --registry "$REGISTRY_ID")
  [[ "${SKIP_PUSH:-false}" == "true" ]] && build_args+=(--skip-push)
  [[ "${NO_CACHE:-false}" == "true" ]] && build_args+=(-n)
  bash "$BUILD_SH" "${build_args[@]}"
else
  echo "SKIP_BUILD=true — skipping docker build/push"
fi

echo "Restarting Deployment/${DEPLOYMENT_NAME} in namespace ${HORIZON_API_NAMESPACE} with image:"
echo "  ${FULL_IMAGE_URI}"
kubectl set image "deployment/${DEPLOYMENT_NAME}" "${CONTAINER_NAME}=${FULL_IMAGE_URI}" -n "${HORIZON_API_NAMESPACE}"
kubectl rollout status "deployment/${DEPLOYMENT_NAME}" -n "${HORIZON_API_NAMESPACE}" --timeout=300s
echo "Rollout complete."
