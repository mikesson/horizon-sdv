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
# Builds the orchestrator image with Cloud Build and prints it as
# image@digest on stdout, ready for `terraform apply -var image=...`.
# Progress goes to stderr.
#
# The build runs in the deployment's region, as its builder service account,
# with the source staged in its own bucket. Those values come from
# `terraform output -json build`, or from flags when the configuration that
# applied this one wraps it as a module. A flag overrides the output.
#
#   scripts/build-image.sh
#   scripts/build-image.sh --project P --region R --repository REPO \
#       --service-account projects/P/serviceAccounts/EMAIL \
#       --staging-dir gs://BUCKET/source
#
#   --tag TAG            image tag; default UTC time plus the git commit
#   --terraform-dir DIR  where to run `terraform output`; default terraform/
#
# Tags in the repository are immutable, so a tag that already exists fails
# the push. The script never changes gcloud's configuration.

set -euo pipefail

readonly IMAGE_NAME=cuttlefish-orchestrator
readonly POLL_SECONDS=10

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

usage() {
  sed -n '16,34p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

die() {
  echo "build-image: $*" >&2
  exit 1
}

project="" region="" repository="" service_account="" staging_dir="" tag=""
tf_dir="$root/terraform"

while [ $# -gt 0 ]; do
  case "$1" in
    --project) project="${2:?--project needs a value}"; shift 2 ;;
    --region) region="${2:?--region needs a value}"; shift 2 ;;
    --repository) repository="${2:?--repository needs a value}"; shift 2 ;;
    --service-account) service_account="${2:?--service-account needs a value}"; shift 2 ;;
    --staging-dir) staging_dir="${2:?--staging-dir needs a value}"; shift 2 ;;
    --tag) tag="${2:?--tag needs a value}"; shift 2 ;;
    --terraform-dir) tf_dir="${2:?--terraform-dir needs a value}"; shift 2 ;;
    -h | --help) usage ;;
    *) echo "build-image: unknown argument: $1" >&2; usage ;;
  esac
done

command -v gcloud >/dev/null || die "gcloud is not installed"

if [ -z "$project" ] || [ -z "$region" ] || [ -z "$repository" ] ||
  [ -z "$service_account" ] || [ -z "$staging_dir" ]; then
  command -v terraform >/dev/null || die "terraform is not installed; pass every value as a flag instead"
  command -v jq >/dev/null || die "jq is not installed; pass every value as a flag instead"
  out="$(terraform -chdir="$tf_dir" output -json build)" ||
    die "no 'build' output in $tf_dir; apply the configuration first or pass every value as a flag"
  field() { jq -er --arg k "$1" '.[$k]' <<<"$out" || die "the 'build' output has no $1"; }
  [ -n "$project" ] || project="$(field project)"
  [ -n "$region" ] || region="$(field region)"
  [ -n "$repository" ] || repository="$(field repository)"
  [ -n "$service_account" ] || service_account="$(field service_account)"
  [ -n "$staging_dir" ] || staging_dir="$(field staging_dir)"
fi

case "$staging_dir" in
  gs://*) ;;
  *) die "--staging-dir must be a gs:// path, got '$staging_dir'" ;;
esac

if [ -z "$tag" ]; then
  tag="$(date -u +%Y%m%d-%H%M%S)"
  if commit="$(git -C "$root" rev-parse --short=8 HEAD 2>/dev/null)"; then
    tag="$tag-$commit"
  fi
fi

image="$repository/$IMAGE_NAME"

echo "build-image: building $image:$tag in $project ($region)" >&2
build_id="$(
  cd "$root" &&
    gcloud builds submit . \
      --project="$project" \
      --region="$region" \
      --service-account="$service_account" \
      --gcs-source-staging-dir="$staging_dir" \
      --config=cloudbuild.yaml \
      --substitutions="_IMAGE=$image,_TAG=$tag" \
      --async \
      --format='value(id)'
)"
[ -n "$build_id" ] || die "gcloud returned no build ID"

log_url=""
while :; do
  line="$(gcloud builds describe "$build_id" \
    --project="$project" --region="$region" \
    --format='value(status,logUrl)')"
  read -r status url <<<"$line"
  if [ -z "$log_url" ] && [ -n "${url:-}" ]; then
    log_url="$url"
    echo "build-image: build $build_id, logs at $log_url" >&2
  fi
  case "$status" in
    SUCCESS) break ;;
    STATUS_UNKNOWN | PENDING | QUEUED | WORKING)
      echo "build-image: $status" >&2
      sleep "$POLL_SECONDS"
      ;;
    *) die "build $build_id ended $status${log_url:+; logs at $log_url}" ;;
  esac
done

digest="$(gcloud builds describe "$build_id" \
  --project="$project" --region="$region" \
  --format='value(results.images[0].digest)')"
case "$digest" in
  sha256:*) ;;
  *) die "build $build_id succeeded but reported no image digest" ;;
esac

echo "$image@$digest"
