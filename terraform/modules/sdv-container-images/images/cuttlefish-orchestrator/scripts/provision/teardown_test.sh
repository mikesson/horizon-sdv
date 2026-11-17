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
# Runs scripts/provision/teardown.sh against stub gcloud and sleep commands on
# PATH. Checks that the instance list is filtered on both host labels and the
# name prefix, that deletes go per zone, that a failed delete waits before the
# next list, that the script stops only after two empty lists 30 seconds apart, that the subnet wait runs only when asked and
# after the hosts are gone, and that every gcloud call names the project.
#
#   scripts/provision/teardown_test.sh

set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/teardown.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir "$work/bin"

readonly PROJECT=test-project
readonly REGION=test-region1
readonly DEPLOYMENT=second
readonly SUBNET=test-subnet

# Each call appends one line, "gcloud <args>" or "sleep <args>". The instance
# list answers two hosts in two zones for its first STUB_INSTANCE_LISTS calls
# ("always" never stops), and the address list answers one address for its
# first STUB_ADDRESS_LISTS calls. Deletes fail when STUB_DELETE_FAILS=1.
cat >"$work/bin/gcloud" <<'EOF'
#!/usr/bin/env bash
echo "gcloud $*" >>"$STUB_LOG"
count() {
  local n=0
  [ -e "$STUB_DIR/$1" ] && n="$(cat "$STUB_DIR/$1")"
  echo $((n + 1)) >"$STUB_DIR/$1"
  echo "$n"
}
case "$1 $2 $3" in
  "compute instances list")
    n="$(count instances)"
    if [ "$STUB_INSTANCE_LISTS" = always ] || [ "$n" -lt "$STUB_INSTANCE_LISTS" ]; then
      printf 'cf-a-1\tzone-a\ncf-b-1\tzone-b\ncf-a-2\tzone-a\n'
    fi
    ;;
  "compute instances delete") [ "${STUB_DELETE_FAILS:-0}" = 1 ] && exit 1 ;;
  "compute addresses list")
    n="$(count addresses)"
    [ "$n" -lt "$STUB_ADDRESS_LISTS" ] && echo serverless-ipv4-1
    ;;
  *) echo "stub gcloud: unexpected call: $*" >&2; exit 1 ;;
esac
exit 0
EOF

cat >"$work/bin/sleep" <<'EOF'
#!/usr/bin/env bash
echo "sleep $*" >>"$STUB_LOG"
EOF
chmod +x "$work/bin/gcloud" "$work/bin/sleep"

export PATH="$work/bin:$PATH"

failures=0
fail() {
  echo "FAIL [$case_name]: $*" >&2
  failures=$((failures + 1))
}

# Prepares a case. The remaining arguments are teardown.sh's extra flags.
start_case() {
  case_name="$1"
  export STUB_LOG="$work/$case_name.log" STUB_DIR="$work/$case_name.d"
  mkdir "$STUB_DIR"
  : >"$STUB_LOG"
}

run_script() {
  "$script" --project="$PROJECT" --region="$REGION" --deployment="$DEPLOYMENT" \
    --name-prefix=cf- --subnet="$SUBNET" "$@" >"$work/$case_name.out" 2>"$work/$case_name.err"
}

check_project_on_every_call() {
  local calls projects
  calls="$(grep -c '^gcloud ' "$STUB_LOG")"
  projects="$(grep -c "^gcloud .* --project=$PROJECT\( \|$\)" "$STUB_LOG")"
  [ "$calls" -eq "$projects" ] || fail "$calls gcloud calls but $projects carry --project=$PROJECT"
}

# --- hosts: one listing with hosts, then two empty ones, no subnet wait.
start_case hosts
export STUB_INSTANCE_LISTS=1 STUB_ADDRESS_LISTS=0
if ! run_script; then
  fail "script failed: $(cat "$work/$case_name.err")"
fi

filter="--filter=labels.managed-by=cloud-cuttlefish-orchestrator AND labels.cco-deployment=$DEPLOYMENT AND name:cf-*"
lists="$(grep -c '^gcloud compute instances list ' "$STUB_LOG" || true)"
[ "$lists" -eq 3 ] || fail "instances listed $lists times, want 3 (hosts, empty, empty)"
filtered="$(grep -cF -- "$filter" "$STUB_LOG" || true)"
[ "$filtered" -eq "$lists" ] || fail "$filtered of $lists instance lists carry $filter"

grep -qxF "gcloud compute instances delete --project=$PROJECT --zone=zone-a --quiet cf-a-1 cf-a-2" "$STUB_LOG" ||
  fail "no single delete of both zone-a hosts"
grep -qxF "gcloud compute instances delete --project=$PROJECT --zone=zone-b --quiet cf-b-1" "$STUB_LOG" ||
  fail "no delete of the zone-b host"
deletes="$(grep -c '^gcloud compute instances delete ' "$STUB_LOG" || true)"
[ "$deletes" -eq 2 ] || fail "$deletes delete calls, want 2 (one per zone)"

# The two empty lists must be separated by the 30-second settle, and the list
# after the deletes must not wait.
sequence="$(grep -oE '^(gcloud compute instances (list|delete)|sleep .*)' "$STUB_LOG" | tr '\n' '|')"
want='gcloud compute instances list|gcloud compute instances delete|gcloud compute instances delete|gcloud compute instances list|sleep 30|gcloud compute instances list|'
[ "$sequence" = "$want" ] || fail "call sequence is '$sequence', want '$want'"

! grep -q '^gcloud compute addresses ' "$STUB_LOG" || fail "addresses listed without --wait-for-subnet"
check_project_on_every_call

# --- delete_fails: a failed delete waits the settle before the next list.
start_case delete_fails
export STUB_INSTANCE_LISTS=1 STUB_ADDRESS_LISTS=0 STUB_DELETE_FAILS=1
if ! run_script; then
  fail "script failed: $(cat "$work/$case_name.err")"
fi
unset STUB_DELETE_FAILS
sequence="$(grep -oE '^(gcloud compute instances (list|delete)|sleep .*)' "$STUB_LOG" | tr '\n' '|')"
want='gcloud compute instances list|gcloud compute instances delete|gcloud compute instances delete|sleep 30|gcloud compute instances list|sleep 30|gcloud compute instances list|'
[ "$sequence" = "$want" ] || fail "call sequence is '$sequence', want '$want'"

# --- subnet: the address wait runs after the hosts are gone, polls until empty.
start_case subnet
export STUB_INSTANCE_LISTS=0 STUB_ADDRESS_LISTS=2
if ! run_script --wait-for-subnet; then
  fail "script failed: $(cat "$work/$case_name.err")"
fi

address_call="gcloud compute addresses list --project=$PROJECT --regions=$REGION --filter=purpose=SERVERLESS AND subnetwork~/subnetworks/$SUBNET\$ --format=value(name)"
polls="$(grep -cxF -- "$address_call" "$STUB_LOG" || true)"
[ "$polls" -eq 3 ] || fail "addresses listed $polls times with the subnet filter, want 3 (two held, one empty)"
last_instance_list="$(grep -n '^gcloud compute instances list ' "$STUB_LOG" | tail -1 | cut -d: -f1)"
first_address_list="$(grep -n '^gcloud compute addresses list ' "$STUB_LOG" | head -1 | cut -d: -f1)"
[ -n "$first_address_list" ] && [ "$first_address_list" -gt "$last_instance_list" ] ||
  fail "the address wait started before the hosts were gone"
grep -qx 'sleep 60' "$STUB_LOG" || fail "the address poll does not wait between lists"
check_project_on_every_call

# --- instance timeout: hosts that never go fail the script.
start_case instance_timeout
export STUB_INSTANCE_LISTS=always STUB_ADDRESS_LISTS=0
if TEARDOWN_INSTANCE_TIMEOUT=0 run_script --wait-for-subnet; then
  fail "script succeeded while hosts were still listed"
fi
! grep -q '^gcloud compute addresses ' "$STUB_LOG" || fail "the address wait ran after the host deletes failed"

# --- address timeout: an address that never goes fails the script.
start_case address_timeout
export STUB_INSTANCE_LISTS=0 STUB_ADDRESS_LISTS=1000
if TEARDOWN_ADDRESS_TIMEOUT=0 run_script --wait-for-subnet; then
  fail "script succeeded while the subnet still held an address"
fi

# --- usage: a missing deployment refuses before any gcloud call.
start_case usage
if "$script" --project="$PROJECT" --region="$REGION" --name-prefix=cf- --subnet="$SUBNET" 2>/dev/null; then
  fail "script ran without --deployment"
fi
[ ! -s "$STUB_LOG" ] || fail "gcloud was called without --deployment"

if [ "$failures" -ne 0 ]; then
  echo "teardown_test: $failures failure(s)" >&2
  exit 1
fi
echo "teardown_test: PASS"
