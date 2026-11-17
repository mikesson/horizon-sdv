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
# Runs scripts/build-image.sh against stub gcloud, terraform and sleep
# commands on PATH. Checks that the build runs regionally, as the builder
# account, staged in the deployment's bucket, that every gcloud call names
# the project, and that stdout carries nothing but image@digest.
#
#   scripts/build-image_test.sh

set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/build-image.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir "$work/bin"

readonly PROJECT=test-project
readonly REGION=test-region1
readonly REPOSITORY=test-region1-docker.pkg.dev/test-project/cuttlefish-orchestrator
readonly SA=projects/test-project/serviceAccounts/cco-builder@test-project.iam.gserviceaccount.com
readonly STAGING=gs://test-project-cco-build-staging/source
readonly DIGEST=sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef

# Each gcloud call appends its arguments, one per line, then a blank line.
# The first status poll answers WORKING, so the polling loop runs twice.
cat >"$work/bin/gcloud" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$@" >>"$STUB_LOG"
echo >>"$STUB_LOG"
case "$1 $2" in
  "builds submit")
    echo "Uploading tarball..." >&2
    echo "stub-build-id"
    ;;
  "builds describe")
    case "$*" in
      *results.images*) echo "$STUB_DIGEST" ;;
      *status*)
        if [ -e "$STUB_STATE" ]; then
          printf 'SUCCESS\thttps://logs.example/stub\n'
        else
          touch "$STUB_STATE"
          printf 'WORKING\thttps://logs.example/stub\n'
        fi
        ;;
    esac
    ;;
  *) echo "stub gcloud: unexpected call: $*" >&2; exit 1 ;;
esac
EOF

cat >"$work/bin/terraform" <<EOF
#!/usr/bin/env bash
[ "\$*" = "-chdir=$work/tf output -json build" ] || { echo "stub terraform: unexpected call: \$*" >&2; exit 1; }
cat <<JSON
{"project":"$PROJECT","region":"$REGION","repository":"$REPOSITORY","service_account":"$SA","staging_dir":"$STAGING"}
JSON
EOF

printf '#!/usr/bin/env bash\nexit 0\n' >"$work/bin/sleep"
chmod +x "$work/bin/gcloud" "$work/bin/terraform" "$work/bin/sleep"

export PATH="$work/bin:$PATH"
export STUB_DIGEST="$DIGEST"

failures=0
fail() {
  echo "FAIL [$case_name]: $*" >&2
  failures=$((failures + 1))
}

# The arguments of the one gcloud call whose first two words are $1 $2.
call_args() {
  awk -v want="$1 $2" '
    NF == 0 { if (n >= 2 && (a[1] " " a[2]) == want) { for (i = 1; i <= n; i++) print a[i] } n = 0; next }
    { a[++n] = $0 }
  ' "$STUB_LOG"
}

has_arg() {
  grep -qxF -- "$2" <<<"$1"
}

run_case() {
  case_name="$1"
  shift
  export STUB_LOG="$work/$case_name.log" STUB_STATE="$work/$case_name.state"
  : >"$STUB_LOG"

  local out
  if ! out="$("$script" "$@" --tag t1 2>"$work/$case_name.err")"; then
    fail "script failed: $(cat "$work/$case_name.err")"
    return
  fi

  [ "$out" = "$REPOSITORY/cuttlefish-orchestrator@$DIGEST" ] ||
    fail "stdout is '$out', want only the image@digest"

  local submit
  submit="$(call_args builds submit)"
  [ -n "$submit" ] || { fail "gcloud builds submit was never called"; return; }
  for want in \
    "--project=$PROJECT" \
    "--region=$REGION" \
    "--service-account=$SA" \
    "--gcs-source-staging-dir=$STAGING" \
    "--async" \
    "--substitutions=_IMAGE=$REPOSITORY/cuttlefish-orchestrator,_TAG=t1"; do
    has_arg "$submit" "$want" || fail "builds submit lacks $want"
  done

  local describes
  describes="$(grep -cxF describe "$STUB_LOG")"
  [ "$describes" -eq 3 ] || fail "gcloud builds describe ran $describes times, want 3 (two polls and the digest)"

  local calls projects
  calls="$(grep -c '^builds$' "$STUB_LOG")"
  projects="$(grep -cxF -- "--project=$PROJECT" "$STUB_LOG")"
  [ "$calls" -eq "$projects" ] || fail "$calls gcloud calls but $projects carry --project"
  ! grep -qx config "$STUB_LOG" || fail "the script ran a gcloud config command"
}

run_case flags \
  --project "$PROJECT" --region "$REGION" --repository "$REPOSITORY" \
  --service-account "$SA" --staging-dir "$STAGING"

if command -v jq >/dev/null; then
  run_case terraform_output --terraform-dir "$work/tf"
else
  echo "SKIP [terraform_output]: jq is not installed" >&2
fi

if [ "$failures" -ne 0 ]; then
  echo "build-image_test: $failures failure(s)" >&2
  exit 1
fi
echo "build-image_test: PASS"
