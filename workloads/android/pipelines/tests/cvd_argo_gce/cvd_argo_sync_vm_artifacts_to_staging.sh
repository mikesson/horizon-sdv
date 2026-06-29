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
# Description:
#   Upload VM artifact bundle to GEMINI_TEST_RESULTS_STAGING_URI via storage.sh
#   (same entrypoint as gemini_storage.sh). Stages test-results/ for GCS layout parity.
#
#   Callers: cvd_argo_gce_ephemeral.sh (Gemini off), prepare-gemini MTK skip,
#   DAG sync-vm-staging-if-no-ai-review.

set -euo pipefail

# -----------------------------------------------------------------------------
# Main (no functions — linear upload via storage.sh)
# -----------------------------------------------------------------------------
# Wrap cvd-argo-out as test-results/ and upload to GEMINI_TEST_RESULTS_STAGING_URI.

: "${WORKSPACE:?WORKSPACE must be set (repo root)}"
: "${CLOUD_PROJECT:?CLOUD_PROJECT must be set}"
: "${GEMINI_TEST_RESULTS_STAGING_URI:?GEMINI_TEST_RESULTS_STAGING_URI must be set}"
: "${CVD_ARGO_LOCAL_ARTIFACT_ROOT:=/tmp/cvd-argo-artifacts}"

export CLOUDSDK_CORE_PROJECT="${CLOUD_PROJECT}"
export GOOGLE_CLOUD_PROJECT="${CLOUD_PROJECT}"

root="${CVD_ARGO_LOCAL_ARTIFACT_ROOT}"
src=""
if [[ -d "${root}/cvd-argo-out" ]]; then
  src="${root}/cvd-argo-out"
else
  src="${root}"
fi
if [[ ! -d "${src}" ]]; then
  echo "[cvd-argo-sync] WARN: no artifact tree at ${src}; skip staging upload" >&2
  exit 0
fi

_staging_wrap="$(mktemp -d /tmp/cvd-argo-storage-wrap.XXXXXX)"
trap 'rm -rf "${_staging_wrap}"' EXIT

mkdir -p "${_staging_wrap}/test-results"
cp -a "${src}/." "${_staging_wrap}/test-results/"
# Omit .meta (driver MTK stage flags under CVD_ARGO_LOCAL_ARTIFACT_ROOT/.meta); not for publication with test artifacts.
rm -rf "${_staging_wrap}/test-results/.meta"
find "${_staging_wrap}/test-results" -name 'cvd-argo.marker' -delete 2>/dev/null || true

export STORAGE_BUCKET_DESTINATION="${GEMINI_TEST_RESULTS_STAGING_URI%/}/"
_rel="${STORAGE_BUCKET_DESTINATION#gs://}"
export STORAGE_CLOUD_URL="${STORAGE_CLOUD_URL:-https://console.cloud.google.com/storage/browser/${_rel}}"
export ARTIFACT_STORAGE_SOLUTION=GCS_BUCKET
export ARTIFACT_LIST="${_staging_wrap}/test-results"
export ARTIFACT_SUMMARY="${_staging_wrap}/artifact-summary.txt"
export POST_CLEANUP_STRING=""

echo "[cvd-argo-sync] storing VM artifacts via storage.sh -> ${STORAGE_BUCKET_DESTINATION} (test-results/ layout)"
"${WORKSPACE}/workloads/common/storage/storage.sh"
