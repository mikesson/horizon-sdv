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
# Deletes what a deployment created outside Terraform state. Terraform runs it
# on destroy, after the Cloud Run service is gone and before the network goes.
#
#   teardown.sh --project=P --region=R --deployment=D --name-prefix=cf- \
#     --subnet=S [--wait-for-subnet]
#
# 1. Deletes every host VM carrying this deployment's labels and name prefix,
#    then lists again until two lists 30 seconds apart come back empty. The
#    relist catches inserts GCE accepted just before Cloud Run went.
# 2. With --wait-for-subnet, waits until Cloud Run's serverless address has
#    left the subnet. Google releases it 1 to 2 hours after the service is
#    deleted, it cannot be deleted by hand, and it blocks deleting the subnet.
#
# Exits non-zero on any failure or when a wait runs out. Running destroy again
# resumes.

set -euo pipefail

# Must match the managed-by label the orchestrator stamps on every host VM
# (managedByLabels in internal/app/features.go).
readonly MANAGED_BY=cloud-cuttlefish-orchestrator

readonly SETTLE_SECONDS=30
readonly ADDRESS_POLL_SECONDS=60
INSTANCE_TIMEOUT="${TEARDOWN_INSTANCE_TIMEOUT:-900}"
ADDRESS_TIMEOUT="${TEARDOWN_ADDRESS_TIMEOUT:-9000}"

project=""
region=""
deployment=""
name_prefix=""
subnet=""
wait_for_subnet=false

usage() {
  echo "usage: $0 --project=P --region=R --deployment=D --name-prefix=PREFIX --subnet=S [--wait-for-subnet]" >&2
  exit 2
}

for arg in "$@"; do
  case "$arg" in
    --project=*) project="${arg#*=}" ;;
    --region=*) region="${arg#*=}" ;;
    --deployment=*) deployment="${arg#*=}" ;;
    --name-prefix=*) name_prefix="${arg#*=}" ;;
    --subnet=*) subnet="${arg#*=}" ;;
    --wait-for-subnet) wait_for_subnet=true ;;
    *) echo "unknown argument: $arg" >&2; usage ;;
  esac
done
if [ -z "$project" ] || [ -z "$region" ] || [ -z "$deployment" ] ||
  [ -z "$name_prefix" ] || [ -z "$subnet" ]; then
  usage
fi

# One "name zone" line per instance.
list_instances() {
  gcloud compute instances list --project="$project" \
    --filter="labels.managed-by=$MANAGED_BY AND labels.cco-deployment=$deployment AND name:$name_prefix*" \
    --format='value(name,zone.basename())'
}

# A failed delete is reported and left to the next list, which still sees the
# instance if it was not deleted. Returns 1 when any zone's delete failed.
delete_instances() {
  local listing="$1" zone zones names failed=0
  mapfile -t zones < <(awk '{print $2}' <<<"$listing" | sort -u)
  for zone in "${zones[@]}"; do
    mapfile -t names < <(awk -v z="$zone" '$2 == z {print $1}' <<<"$listing")
    echo "deleting ${#names[@]} instance(s) in $zone: ${names[*]}"
    if ! gcloud compute instances delete --project="$project" --zone="$zone" --quiet "${names[@]}"; then
      echo "delete in $zone failed; listing again in ${SETTLE_SECONDS}s" >&2
      failed=1
    fi
  done
  return "$failed"
}

delete_hosts() {
  local start=$SECONDS empty=0 listing pause
  echo "deleting host VMs of deployment $deployment in project $project"
  while true; do
    listing="$(list_instances)"
    pause=true
    if [ -z "$listing" ]; then
      empty=$((empty + 1))
      if [ "$empty" -ge 2 ]; then
        echo "no host VMs left"
        return 0
      fi
    else
      empty=0
      # After deletes that all succeeded, list again at once. After a failed
      # one, wait so a persistent error does not call gcloud in a tight loop.
      delete_instances "$listing" && pause=false
    fi
    if [ $((SECONDS - start)) -ge "$INSTANCE_TIMEOUT" ]; then
      echo "host VMs still listed after ${INSTANCE_TIMEOUT}s:" >&2
      list_instances >&2 || true
      return 1
    fi
    if [ "$pause" = true ]; then
      sleep "$SETTLE_SECONDS"
    fi
  done
}

wait_for_serverless_address() {
  local start=$SECONDS addresses count
  echo "waiting for Cloud Run's serverless address to leave subnet $subnet (Google takes 1 to 2 hours)"
  while true; do
    addresses="$(gcloud compute addresses list --project="$project" --regions="$region" \
      --filter="purpose=SERVERLESS AND subnetwork~/subnetworks/$subnet\$" \
      --format='value(name)')"
    if [ -z "$addresses" ]; then
      echo "subnet $subnet holds no serverless address"
      return 0
    fi
    count="$(wc -l <<<"$addresses")"
    echo "subnet $subnet still holds $count serverless address(es) after $(((SECONDS - start) / 60)) min"
    if [ $((SECONDS - start)) -ge "$ADDRESS_TIMEOUT" ]; then
      echo "serverless address still in subnet $subnet after ${ADDRESS_TIMEOUT}s; run terraform destroy again later" >&2
      return 1
    fi
    sleep "$ADDRESS_POLL_SECONDS"
  done
}

delete_hosts
if [ "$wait_for_subnet" = true ]; then
  wait_for_serverless_address
fi
