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
# Every gate, run on this machine. There is no hosted CI. Nothing under
# .github/ has ever executed, so this script is the gate.
#
# Exit status is the verdict, and the last line says it in words. Advisory
# jobs report and never fail the run; everything else is a hard gate.

set -uo pipefail

# Resolved before the cd, because each job re-execs this script under `timeout`
# (see run_job) and needs an absolute path to itself.
SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
readonly SELF

cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1
readonly ROOT="$PWD"

# Every Go job runs on the toolchain go.mod names. The workstation's Go is
# upgraded outside the repo, and the pinned golangci-lint cannot load packages
# built by a newer one.
GOTOOLCHAIN="go$(awk '/^go /{print $2}' go.mod)"
export GOTOOLCHAIN
readonly WORK="${TMPDIR:-/tmp}/cco-ci.$$"
mkdir -p "$WORK"

# Per-job output, kept after the run so a failure can be read without scrolling
# the whole stream. Stable names rather than per-pid, so they overwrite instead
# of accumulating; the summary prints the path of anything that failed.
readonly LOG_DIR="${TMPDIR:-/tmp}/cco-ci-logs"
mkdir -p "$LOG_DIR"

readonly LOCKFILE="${TMPDIR:-/tmp}/cco-ci.lock"

# ---------------------------------------------------------------------------
# Cleanup. Installed here, before argument parsing, because --list and a bad
# argument exit early and would otherwise leave $WORK behind.
#
# Established by experiment. bash DOES run an EXIT trap when the script is
# interrupted, but a backgrounded child SURVIVES it. So a job that starts a
# server must register it here, and stopping it must escalate. A plain TERM
# that the process ignores leaves it holding a port while the run that started
# it is gone.
# ---------------------------------------------------------------------------
declare -a CLEANUP_PIDS=()
cleaned=0
CURRENT_CHILD=""

stop_pid() {
  local pid="$1"
  kill -TERM "$pid" 2> /dev/null || return 0
  for _ in $(seq 1 20); do
    kill -0 "$pid" 2> /dev/null || return 0
    sleep 0.25
  done
  kill -KILL "$pid" 2> /dev/null || true
}

cleanup() {
  [ "$cleaned" -eq 1 ] && return 0
  cleaned=1
  # The job first: it owns the emulator work and the console build, and under
  # `timeout` it is a process-group leader, so stopping it takes its own
  # children with it.
  [ -n "$CURRENT_CHILD" ] && stop_pid "$CURRENT_CHILD"
  if [ ${#CLEANUP_PIDS[@]} -gt 0 ]; then
    local pid
    for pid in "${CLEANUP_PIDS[@]}"; do stop_pid "$pid"; done
  fi
  rm -rf "$WORK"
}

trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM
trap cleanup EXIT

readonly EMULATOR_CONTAINER="cco-spanner-emu"
readonly EMULATOR_HOST="localhost:9010"
readonly GOVULNCHECK="golang.org/x/vuln/cmd/govulncheck@v1.6.0"
readonly DEADCODE="golang.org/x/tools/cmd/deadcode@v0.48.0"

# Error-branch coverage floor, set just under the measured 35.7% so it ratchets
# rather than blocks. Raise it after a run that clears it by a margin.
readonly ERROR_BRANCH_FLOOR=35.0

# Generous enough that no healthy job approaches it (test ~4min, e2e ~7min):
# it turns a hung emulator or a stalled browser into a reported failure. It
# does not police how long work takes. --timeout 0 disables it.
readonly DEFAULT_JOB_TIMEOUT_MINS=20

# Jobs in the order they run. Cheap and likely-to-fail first, so a broken tree
# is reported in seconds rather than after the full suite. This list is also
# the default set, so there is no second copy to drift from it.
#
# e2e is absent on purpose. It needs browsers a reboot's overlay reset leaves
# behind, and a gate that fails for want of a bootstrap teaches people to
# ignore it. Ask for it by name; the preflight says when it cannot run.
#
# The harnesses are opt-in for their own reasons. queueload and robustness are
# opt-in because minutes of emulator torture belong before queue- or lifecycle-
# touching changes rather than in every run, livesmoke because it provisions
# real billable VMs. They live here so `--list` is the complete verification
# map; each wrapper runs the documented smoke, and the deeper scenarios
# (chaos, stockout, keyed, soaks) stay direct `go run` runs per the harness's
# doc.go.
readonly ALL_JOBS=(shellcheck bleed terraform build vet gen lint test leak vuln contract webbuild webtest weblint deadcss knip deps cards deadcode)
readonly OPT_IN_JOBS=(e2e queueload robustness livesmoke)
readonly ADVISORY_JOBS=(deadcode)
readonly NET_JOBS=(vuln deps)

# Jobs that own something machine-global: the emulator container, the console
# build output, Playwright's report and traces, and :4200. Two runs doing these
# at once interleave, so a run that wants any of them takes the lock first.
# Everything else (lint, vet, shellcheck…) reads the tree and is free to run
# alongside. Spanner test databases are NOT in this category. They are named
# per call and dropped by name, so concurrent suites do not collide there.
readonly EXCLUSIVE_JOBS=(test contract webbuild webtest e2e queueload robustness livesmoke)

# livesmoke legitimately spans a device create, the verb ladder, and a
# teardown that must not be killed mid-flight, and a 20-minute cap would TERM
# it while VMs still bill. Applied only when --timeout was not given explicitly.
declare -A JOB_TIMEOUT_OVERRIDE_MINS=([livesmoke]=45)

readonly CONSOLE="web/cuttlefish-console"
readonly E2E_BOOTSTRAP="$CONSOLE/scripts/e2e-bootstrap.sh"

# ---------------------------------------------------------------------------
# What each job shells out to.
#
# This is a Cloud Workstation: /home is a persistent disk and the rest of the
# filesystem is an overlay that every reboot resets. So the repo, node_modules
# and the browsers under ~/.cache survive, while apt-installed tools and
# Chromium's shared libraries do not. Checking the whole requested set up front
# turns "five minutes in, one missing tool at a time" into one report before
# any work starts, which is the difference between a gate you trust after a
# reboot and one you learn to distrust.
# ---------------------------------------------------------------------------
declare -A JOB_TOOLS=(
  [shellcheck]="git shellcheck"
  [bleed]="git"
  [terraform]="git terraform"
  [build]="go"
  [vet]="go"
  [gen]="go"
  [lint]="go"
  [test]="go docker curl"
  [leak]="go jq"
  [vuln]="go"
  [contract]="go npm curl"
  [webbuild]="npm"
  [webtest]="npm"
  [weblint]="npm"
  [deadcss]="node"
  [knip]="npx"
  [deps]="npm"
  [cards]="npx"
  [deadcode]="go jq"
  [e2e]="npm npx"
  [queueload]="go docker curl"
  [robustness]="go docker curl"
  [livesmoke]="go gcloud"
)

# Only the tools this workstation installs from apt. go, docker, terraform and
# node came from elsewhere, and inventing an apt command for them would send
# the reader somewhere that cannot work.
declare -A APT_PACKAGE=(
  [shellcheck]="shellcheck"
  [jq]="jq"
  [flock]="util-linux"
  [curl]="curl"
)

if [ -t 1 ]; then
  C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YEL=$'\033[33m'; C_DIM=$'\033[2m'; C_OFF=$'\033[0m'
else
  C_RED=""; C_GRN=""; C_YEL=""; C_DIM=""; C_OFF=""
fi

no_net=0
wait_for_lock=0
fix_only=0
job_timeout_mins="$DEFAULT_JOB_TIMEOUT_MINS"
timeout_explicit=0
declare -a requested=()

usage() {
  cat <<'EOF'
Every gate, run on this machine. Exit status is the verdict, and the last
line says it in words.

  scripts/ci.sh                 every gate except e2e
  scripts/ci.sh test lint       just those
  scripts/ci.sh --no-net        skip the jobs that need the network
  scripts/ci.sh e2e             opt-in; browsers restored by --fix-env after a reboot
  scripts/ci.sh queueload robustness
                                opt-in harness smokes (emulator torture)
  CCO_LIVESMOKE_PROJECT=<id> CCO_LIVESMOKE_REGION=<region> scripts/ci.sh livesmoke
                                opt-in; provisions REAL billable VMs there
  scripts/ci.sh --wait          queue behind another run instead of refusing
  scripts/ci.sh --timeout MINS  per-job limit (0 disables; default 20)
  scripts/ci.sh --fix-env       reinstall what a reboot took, then exit
  scripts/ci.sh --list          what the jobs are
EOF
  echo
  echo "Jobs:      ${ALL_JOBS[*]}"
  echo "Opt-in:    ${OPT_IN_JOBS[*]}"
  echo "Advisory:  ${ADVISORY_JOBS[*]} (report, never fail the run)"
  echo "Exclusive: ${EXCLUSIVE_JOBS[*]} (take the machine lock)"
}

known_job() {
  # shellcheck disable=SC2076
  [[ " ${ALL_JOBS[*]} ${OPT_IN_JOBS[*]} " =~ " $1 " ]]
}

while [ $# -gt 0 ]; do
  case "$1" in
    --no-net) no_net=1 ;;
    --wait) wait_for_lock=1 ;;
    --fix-env) fix_only=1 ;;
    --timeout)
      shift
      [ $# -gt 0 ] || { echo "--timeout needs a value in minutes" >&2; exit 2; }
      [[ "$1" =~ ^[0-9]+$ ]] || { echo "--timeout takes whole minutes, got: $1" >&2; exit 2; }
      job_timeout_mins="$1"
      timeout_explicit=1 ;;
    --list|-l|--help|-h) usage; exit 0 ;;
    -*) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
    *)
      if known_job "$1"; then requested+=("$1")
      else echo "unknown job: $1" >&2; usage >&2; exit 2; fi ;;
  esac
  shift
done
[ ${#requested[@]} -eq 0 ] && requested=("${ALL_JOBS[@]}")

# The same job named twice would run twice; keep first-mention order.
declare -a deduped=()
for job in "${requested[@]}"; do
  # shellcheck disable=SC2076
  [[ " ${deduped[*]} " =~ " $job " ]] || deduped+=("$job")
done
requested=("${deduped[@]}")

is_advisory()  { [[ " ${ADVISORY_JOBS[*]} " =~ ( |^)"$1"( |$) ]]; }
needs_net()    { [[ " ${NET_JOBS[*]} " =~ ( |^)"$1"( |$) ]]; }
is_exclusive() { [[ " ${EXCLUSIVE_JOBS[*]} " =~ ( |^)"$1"( |$) ]]; }

declare -A RESULT=()
declare -A SECONDS_TAKEN=()

# ---------------------------------------------------------------------------
# Spanner emulator
#
# The persistence layer's invariants (settle CAS, atomic quota, idempotency,
# leases) are verified ONLY by emulator-backed tests, which skip silently
# without SPANNER_EMULATOR_HOST. CCO_REQUIRE_EMULATOR turns those skips into
# failures, so a stopped container cannot read as a green run.
# ---------------------------------------------------------------------------
# The emulator has no health endpoint and 404s on /, so the probe accepts any
# HTTP answer on :9020 (no curl -f: a 404 from a serving emulator would read
# as down), and then requires a TCP accept on the gRPC port, because :9020 is
# the REST frontend while the tests dial gRPC, and one can serve with the
# other hung. Probing through docker exec is not an option, because the image
# carries no shell utilities, so every exec fails whether or not the emulator
# is up.
emulator_up() {
  curl -s -m 2 -o /dev/null "http://${EMULATOR_HOST%%:*}:9020/" 2> /dev/null &&
    port_in_use "${EMULATOR_HOST##*:}"
}

ensure_emulator() {
  if emulator_up; then return 0; fi
  if ! command -v docker > /dev/null; then
    echo "docker is not installed; the emulator-backed suite cannot run" >&2
    return 1
  fi
  if ! docker ps --format '{{.Names}}' | grep -qx "$EMULATOR_CONTAINER"; then
    if docker ps -a --format '{{.Names}}' | grep -qx "$EMULATOR_CONTAINER"; then
      echo "${C_DIM}starting $EMULATOR_CONTAINER (a reboot stops it)${C_OFF}"
      docker start "$EMULATOR_CONTAINER" > /dev/null 2>&1 || true
    else
      echo "${C_DIM}creating $EMULATOR_CONTAINER${C_OFF}"
      # A create that loses a race with another process (or with docker itself
      # still tearing an old container down) is not fatal. Fall through to the
      # readiness loop, which is what decides.
      docker run -d --name "$EMULATOR_CONTAINER" -p 9010:9010 -p 9020:9020 \
        gcr.io/cloud-spanner-emulator/emulator > /dev/null 2>&1 ||
        docker start "$EMULATOR_CONTAINER" > /dev/null 2>&1 || true
    fi
  fi
  for _ in $(seq 1 30); do
    emulator_up && return 0
    sleep 1
  done
  echo "emulator did not come up; try: docker restart $EMULATOR_CONTAINER" >&2
  return 1
}

# Bash's own /dev/tcp instead of ss or lsof. That is one less tool to require,
# and the question is only "will a connect succeed".
port_in_use() {
  (exec 3<> "/dev/tcp/127.0.0.1/$1") 2> /dev/null
}

tool_fix_hint() {
  if [ -n "${APT_PACKAGE[$1]:-}" ]; then
    echo "sudo apt-get install -y ${APT_PACKAGE[$1]}"
  else
    echo "not an apt package here; restore it the way it was first installed"
  fi
}

# Why the browser cannot run, or nothing. Prints the reason and returns 0 when
# there IS a problem, so callers read as `if playwright_problem; then`.
#
# `playwright install --dry-run` reports where the pinned browsers BELONG. It
# exits 0 and prints the same lines whether or not they are there, so it can
# answer "which build" and never "is it installed". A guard built on its exit
# status, which is what used to stand here, could not fail. These two
# file-system questions can, and they are the two that differ after a reboot.
# The binaries live on /home and survive; their shared libraries do not.
playwright_problem() {
  local -a want=()
  mapfile -t want < <( (cd "$ROOT/$CONSOLE" && npx playwright install --dry-run 2> /dev/null) |
    awk '/Install location:/ && $3 ~ /chromium/ {print $3}')
  if [ ${#want[@]} -eq 0 ]; then
    echo "playwright could not report its browser locations"
    return 0
  fi

  local dir exe
  for dir in "${want[@]}"; do
    if [ ! -d "$dir" ]; then
      echo "browsers missing; run $E2E_BOOTSTRAP"
      return 0
    fi
    # The executable's directory is named per build (chrome-linux, then
    # chrome-linux64), so ask the filesystem instead of assuming this release's
    # layout. An assumed path silently probes whichever old build still
    # matches it.
    exe=$(find "$dir" -maxdepth 2 -type f \( -name chrome -o -name headless_shell \) -print -quit 2> /dev/null)
    [ -n "$exe" ] || continue
    if ldd "$exe" 2> /dev/null | grep -q 'not found'; then
      echo "chromium's shared libraries are unresolved; run $E2E_BOOTSTRAP"
      return 0
    fi
  done
  return 1
}

# A run killed mid-test leaves its turn-rs container behind, and those tests use
# host networking, so the next run's container cannot bind, dies, and the
# suite fails somewhere that looks unrelated. Warn instead of removing, because
# this is someone's machine and the fix is one command.
warn_stray_containers() {
  local strays
  command -v docker > /dev/null || return 0
  strays=$(docker ps --format '{{.Names}}' 2> /dev/null | grep '^cco-relay-it-')
  [ -n "$strays" ] || return 0
  echo "${C_YEL}leftover relay-test container(s) from an interrupted run:${C_OFF}" >&2
  local name
  while IFS= read -r name; do echo "  $name" >&2; done <<< "$strays"
  echo "  they hold the ports the relay tests need; remove with:" >&2
  echo "  docker rm -f $(echo "$strays" | tr '\n' ' ')" >&2
  echo >&2
}

# Everything the requested jobs need, checked before any of them runs.
preflight() {
  local job tool line
  local -A seen=()
  local -a problems=()
  # Whether anything missing is something a reboot takes. The port below is
  # not, and explaining it with the wrong cause is worse than not explaining it.
  local reboot_related=0

  for job in "${requested[@]}"; do
    for tool in ${JOB_TOOLS[$job]:-}; do
      [ -n "${seen[$tool]:-}" ] && continue
      seen[$tool]=1
      command -v "$tool" > /dev/null && continue
      problems+=("$(printf '%-14s %s' "$tool" "$(tool_fix_hint "$tool")")")
      reboot_related=1
    done
  done

  if wants_exclusive && ! command -v flock > /dev/null; then
    problems+=("$(printf '%-14s %s' flock "$(tool_fix_hint flock)")")
    reboot_related=1
  fi

  if [[ " ${requested[*]} " =~ ( |^)lint( |$) ]] && [ ! -x bin/golangci-lint ]; then
    problems+=("$(printf '%-14s %s' golangci-lint 'git-excluded and per-tree; install it into bin/')")
  fi

  # gen replays the /v1 parity corpus against the upstream checkout (the
  # legacyparity module cannot even build without it) and test sets
  # CCO_REQUIRE_UPSTREAM=1. Without the git-excluded clone both die minutes in
  # with a build error that never names the real cause; this refusal does.
  if [[ " ${requested[*]} " =~ ( |^)(gen|test)( |$) ]] &&
    [ ! -d UPSTREAM/cloud-android-orchestration ]; then
    problems+=("$(printf '%-14s %s' UPSTREAM/ 'clone github.com/google/cloud-android-orchestration into UPSTREAM/; gen and test verify /v1 parity against it')")
  fi

  # Asked here rather than inside job_e2e so `scripts/ci.sh e2e` answers in a
  # second instead of after the console jobs ahead of it.
  if [[ " ${requested[*]} " =~ ( |^)e2e( |$) ]] && [ -d "$ROOT/$CONSOLE/node_modules" ]; then
    local why
    if why=$(playwright_problem); then
      problems+=("$(printf '%-14s %s' playwright "$why")")
      reboot_related=1
    fi
    # The gate starts its own dev server so the suite tests THIS tree, which
    # means an occupied port is refused instead of reused. Saying so here beats
    # Playwright's own error, which reads as a suite failure and not as an
    # occupied port. E2E_BASE_URL is the supported way to target what is
    # already there, and turns the webServer off entirely.
    if [ -z "${E2E_BASE_URL:-}" ] && port_in_use 4200; then
      problems+=("$(printf '%-14s %s' :4200 'already served; stop it, or set E2E_BASE_URL to test that instead')")
    fi
  fi

  # There is no default project on purpose. livesmoke provisions real VMs, and
  # a mistyped or inherited project would bill somebody else's account.
  if [[ " ${requested[*]} " =~ ( |^)livesmoke( |$) ]] && [ -z "${CCO_LIVESMOKE_PROJECT:-}" ]; then
    problems+=("$(printf '%-14s %s' livesmoke 'set CCO_LIVESMOKE_PROJECT=<project>; this run creates real, billable VMs there')")
  fi
  if [[ " ${requested[*]} " =~ ( |^)livesmoke( |$) ]] && [ -z "${CCO_LIVESMOKE_REGION:-}" ]; then
    problems+=("$(printf '%-14s %s' livesmoke 'set CCO_LIVESMOKE_REGION=<region>; the region the Cloud Run service runs in')")
  fi

  warn_stray_containers

  [ ${#problems[@]} -eq 0 ] && return 0

  echo "${C_RED}the requested jobs cannot run here yet:${C_OFF}" >&2
  for line in "${problems[@]}"; do echo "  $line" >&2; done
  echo >&2
  [ "$reboot_related" -eq 0 ] && return 1
  echo "This workstation keeps /home across reboots and resets the rest, so apt" >&2
  echo "packages and Chromium's OS libraries go missing on every boot while the" >&2
  echo "repo, node_modules and the downloaded browsers survive." >&2
  echo >&2
  echo "Restore the lot with:  scripts/ci.sh --fix-env" >&2
  return 1
}

# The repair side of the same knowledge. Explicitly invoked, so unlike a job it
# may change the machine. It restores the environment, never the tree.
fix_env() {
  local tool pkgs=()
  for tool in "${!APT_PACKAGE[@]}"; do
    command -v "$tool" > /dev/null || pkgs+=("${APT_PACKAGE[$tool]}")
  done

  if [ ${#pkgs[@]} -gt 0 ]; then
    echo "installing: ${pkgs[*]}"
    sudo apt-get update || echo "apt-get update reported errors; continuing" >&2
    sudo apt-get install -y "${pkgs[@]}" || return 1
  else
    echo "apt tools present"
  fi

  # The console half has owned this since before ci.sh did, and its
  # script is idempotent, so defer to it instead of restating it here.
  if [ ! -d "$ROOT/$CONSOLE/node_modules" ] || playwright_problem > /dev/null; then
    echo "restoring the console e2e environment"
    "$ROOT/$E2E_BOOTSTRAP" || return 1
  else
    echo "playwright browsers and libraries present"
  fi

  echo "${C_GRN}environment restored${C_OFF}"
}

# ---------------------------------------------------------------------------
# Jobs. Each writes its own output and returns non-zero to fail.
# ---------------------------------------------------------------------------

# Every tracked shell script, wherever it lives: scripts/ and its bootstrap
# subdirectory, the console's e2e bootstrap, the packer provisioners. The old
# 'scripts/*.sh' pathspec silently left everything outside the root scripts/
# directory unlinted, including the one script the preflight itself depends on.
# A missing shellcheck fails the job instead of passing silently, because a
# gate that checks nothing is worse than no gate.
#
# SC2317 and SC2329 are the only suppressions, and they are one finding.
# Every job below is called indirectly as "job_$job", which shellcheck reads
# as unreachable code (SC2317) and, since 0.11, as functions never invoked
# (SC2329).
job_shellcheck() {
  local files=()
  mapfile -t files < <(git ls-files '*.sh')
  if [ ${#files[@]} -eq 0 ]; then
    echo "no tracked scripts matched; this job would check nothing" >&2
    return 1
  fi
  shellcheck -e SC2317,SC2329 "${files[@]}" || return 1
  echo "${#files[@]} scripts clean"
}

# Names from the development deployment, and instructions that no longer work,
# in tracked files. CCO installs into any organization, project and region, so
# a tracked file that names ours is a default or an example someone copies.
# Tests and recorded fixtures keep real zone names; everything else uses a
# placeholder or us-central1. The names are not tracked, since the list would
# publish them: each clone keeps one extended regex in .git/info/bleed-names.
readonly BLEED_NAMES_EXCLUDE=(
  ':!*_test.go' ':!*.spec.ts'                                         # unit test fixtures
  ':!web/cuttlefish-console/e2e/'                                     # browser test fixtures
  ':!**/testdata/**'                                                  # recorded fixtures
  ':!tools/legacyparity/corpus.go'                                    # input to the recorded /v1 parity fixtures
  ':!internal/features/gcecatalog/geo.go'                             # region catalog
  ':!web/cuttlefish-console/src/app/shared/models/gcp-regions.ts'     # region catalog
  ':!web/cuttlefish-console/src/app/shared/pipes/region-flag.pipe.ts' # region catalog
)
readonly BLEED_STALE='gcloud run services update|make deploy|auth = "bearer"|conf\.toml|gcloud builds submit'
readonly BLEED_STALE_EXCLUDE=(
  ':!scripts/ci.sh'          # holds the patterns
  ':!scripts/build-image.sh' # the one place that submits a build
  ':!scripts/build-image_test.sh' # asserts that it does
)
job_bleed() {
  local failed=0 names_file names
  names_file="$(git rev-parse --git-common-dir)/info/bleed-names"
  if [ -s "$names_file" ]; then
    names="$(<"$names_file")"
    if git grep -nE "$names" -- . "${BLEED_NAMES_EXCLUDE[@]}"; then
      echo "development deployment names above; use a placeholder or us-central1" >&2
      failed=1
    fi
  else
    echo "no $names_file, so no deployment names are checked"
  fi
  if git grep -nE "$BLEED_STALE" -- . "${BLEED_STALE_EXCLUDE[@]}"; then
    echo "stale instructions above; the next steps are terraform output next_steps" >&2
    failed=1
  fi
  [ "$failed" -eq 0 ] && echo "no deployment-specific names or stale instructions"
  return "$failed"
}

# Tracked terraform only. terraform/terraform.tfvars holds this workstation's
# deployment values, is untracked, and is not fmt-clean, so a `fmt -recursive`
# over the directory would fail on it forever.
#
# validate runs in every directory that holds tracked terraform, and not only
# in the root. modules/wif is referenced from no root module, so a root-only
# validate never reads it. It needs the providers `terraform init` downloads,
# and the job does not init on your behalf, because init writes into the tree
# it is checking.
job_terraform() {
  local files=()
  mapfile -t files < <(git ls-files 'terraform/*.tf' 'terraform/*.tfvars')
  if [ ${#files[@]} -eq 0 ]; then
    echo "no tracked terraform matched; this job would check nothing" >&2
    return 1
  fi

  local failed=0
  terraform fmt -check -diff "${files[@]}" || failed=1

  local dirs=() dir
  mapfile -t dirs < <(printf '%s\n' "${files[@]}" | xargs -n1 dirname | sort -u)
  for dir in "${dirs[@]}"; do
    terraform -chdir="$dir" validate || failed=1
  done
  if [ "$failed" -eq 0 ]; then
    echo "${#files[@]} files fmt-clean, ${#dirs[@]} configurations valid"
  fi
  return "$failed"
}

job_build() { go build ./...; }

job_vet() { go vet ./...; }

# cfctl's generated models against the OpenAPI document the router actually
# registers. The generated file is committed, because there is no hosted CI and
# generated-in-CI would mean generated nowhere. This job regenerates to a
# temp file and diffs, so a backend schema change without a regenerate cannot
# land green.
job_gen() {
  scripts/generate-ccogen.sh --check || return 1
  # The /v1 parity recordings are the same committed-artifact contract. The
  # recorder replays the corpus against the upstream checkout and diffs, so
  # an upstream bump or corpus edit without a regenerate cannot land green.
  go -C tools/legacyparity run . -check
}

# The tree is clean, so any finding at all fails the job. There is no baseline
# and no ratchet. Those existed to tolerate a backlog that no longer exists.
job_lint() {
  if [ ! -x bin/golangci-lint ]; then
    echo "bin/golangci-lint is missing; it is git-excluded and per-tree" >&2
    echo "install it there (a detached worktree needs its own copy)" >&2
    return 1
  fi
  # `run` never validates the config. A settings block for an option a linter
  # does not have is silently ignored, and only `config verify` says so.
  bin/golangci-lint config verify || return 1
  bin/golangci-lint run || return 1

  # tools/ are separate modules on purpose, so a run from the repo root never
  # reaches them. They share the root .golangci.yml, which the linter finds by
  # walking up from the module directory.
  local failed=0 mod
  for mod in tools/*/go.mod; do
    echo "${C_DIM}${mod%/go.mod}${C_OFF}"
    (cd "${mod%/go.mod}" && "$ROOT/bin/golangci-lint" run) || failed=1
  done
  return "$failed"
}

# -shuffle=on surfaces test-order dependence; -count=1 keeps emulator-backed
# results uncached; -coverpkg=./... because the default attributes coverage
# only to the package under test, which in a vertical-slice layout reports
# where a test lives instead of what it exercises.
#
# CCO_REQUIRE_DOCKER does for the container-backed relay tests what
# CCO_REQUIRE_EMULATOR does for the Spanner ones. It turns a skip into a
# failure, so a machine without docker cannot read as a green run.
# CCO_REQUIRE_UPSTREAM does the same for the tests that read the UPSTREAM/
# checkout (the coapi drift check). No checkout means no verification, and the
# run has to say so.
#
# The error-branch floor is the number worth gating on. A suite can carry a
# respectable total while leaving almost every `if err != nil` arm unexecuted.
job_test() {
  ensure_emulator || return 1
  SPANNER_EMULATOR_HOST="$EMULATOR_HOST" CCO_REQUIRE_EMULATOR=1 CCO_REQUIRE_DOCKER=1 \
    CCO_REQUIRE_UPSTREAM=1 \
    go test -race -shuffle=on -count=1 -coverpkg=./... \
      -coverprofile="$WORK/cover.out" -covermode=atomic ./... || return 1
  echo
  go tool cover -func="$WORK/cover.out" | tail -1
  go run ./tests/coverreport -fail-under-error "$ERROR_BRANCH_FLOOR" \
    "$WORK/cover.out" > "$WORK/coverage.txt"
  local status=$?
  head -2 "$WORK/coverage.txt"
  return "$status"
}

# The assertions live behind a goexperiment build tag, so a normal `go test`
# never compiles them. Three silent ways this could verify nothing, all closed
# below: a -run regex matching no test exits 0; a toolchain without the
# experiment compiles the tagged files away just as silently; and a package can
# assert on leaks while its detector sits inert, since an empty profile and a
# working one look identical.
job_leak() {
  GOEXPERIMENT=goroutineleakprofile go test -json -count=1 -shuffle=on -run GoroutineLeak \
    ./internal/telemetry/... ./internal/jobs/... > "$WORK/leak.json"
  local status=$?
  jq -r 'select(.Action=="output") | .Output' "$WORK/leak.json" \
    | grep -E '^(--- |ok |FAIL)' || true
  [ "$status" -eq 0 ] || return 1

  jq -r 'select(.Action=="pass" and .Test != null) | "\(.Package)\t\(.Test)"' "$WORK/leak.json" \
    | sort -u > "$WORK/leak.passed"
  cut -f1 "$WORK/leak.passed" | sort -u > "$WORK/leak.pkgs"
  awk -F'\t' '$2 == "TestGoroutineLeakDetectorIsLive" {print $1}' "$WORK/leak.passed" \
    | sort -u > "$WORK/leak.controlled"

  local n
  n=$(wc -l < "$WORK/leak.pkgs")
  if [ "$n" -lt 4 ]; then
    echo "only $n package(s) ran leak assertions, expected at least 4" >&2
    return 1
  fi
  if ! diff -q "$WORK/leak.pkgs" "$WORK/leak.controlled" > /dev/null; then
    echo "package(s) asserting on leaks with no live-detector control:" >&2
    comm -23 "$WORK/leak.pkgs" "$WORK/leak.controlled" >&2
    return 1
  fi
  echo "$n packages asserted on goroutine leaks, each with a live detector"
}

# gosec (in lint) finds insecure code patterns and structurally cannot find a
# vulnerable dependency. Symbol-level by default, so a CVE in a module we
# require but never call does not fail the build.
job_vuln() { go run "$GOVULNCHECK" ./...; }

# Zod schemas against the OpenAPI document the router actually registers.
# specgen boots the real router, so this also catches a Huma registration
# panic -- which `go test` does not: only 4 of 12 feature packages exercise
# route registration.
#
# The server takes an ephemeral port and reports it, and the drift check is
# pointed at whatever it got. That is what makes this job independent of
# anything else on the machine: there is no fixed port for a leftover server to
# hold, and none for a leftover to answer a readiness probe on with a spec
# built from older code.
#
# Built, not `go run`: go run execs the server as a CHILD, so killing the
# wrapper would leave the server running.
job_contract() {
  console_ready || return 1
  go build -o "$WORK/specgen" ./tests/specgen || return 1
  "$WORK/specgen" -addr localhost:0 > "$WORK/specgen.log" 2>&1 &
  local pid=$!
  CLEANUP_PIDS+=("$pid")

  # The log line is the contract; tests/specgen/main.go says so on its side.
  local url=""
  for _ in $(seq 1 60); do
    url=$(sed -n 's#.*serving OpenAPI spec at \(http://[^/]*\)/.*#\1#p' "$WORK/specgen.log")
    [ -n "$url" ] && break
    kill -0 "$pid" 2> /dev/null || break
    sleep 1
  done
  if [ -z "$url" ]; then
    echo "specgen never reported a listen address" >&2
    cat "$WORK/specgen.log" >&2
    return 1
  fi

  local up=0
  for _ in $(seq 1 30); do
    if curl -sf "$url/openapi.json" -o /dev/null; then up=1; break; fi
    kill -0 "$pid" 2> /dev/null || break
    sleep 1
  done
  if [ "$up" -ne 1 ]; then
    echo "specgen reported $url but never answered there" >&2
    cat "$WORK/specgen.log" >&2
    return 1
  fi

  (cd "$ROOT/$CONSOLE" && npm run check:schema-drift -- "--api-url=$url")
  local status=$?
  stop_pid "$pid"
  return "$status"
}

# ---------------------------------------------------------------------------
# Console. Every job needs dependencies installed; none of them installs on
# your behalf, because `npm ci` deletes node_modules and a gate should not
# rewrite the tree it is checking.
# ---------------------------------------------------------------------------
console_ready() {
  if [ ! -d "$ROOT/$CONSOLE/node_modules" ]; then
    echo "console dependencies missing; run: (cd $CONSOLE && npm ci)" >&2
    return 1
  fi
}

job_webbuild() { console_ready && (cd "$ROOT/$CONSOLE" && npm run build); }

# Coverage floors live in angular.json as a ratchet set just below measured
# reality; the builder fails the run when one is unmet, so this needs no
# threshold of its own.
job_webtest() { console_ready && (cd "$ROOT/$CONSOLE" && CI=true npm test -- --watch=false --coverage); }

# Both linters, exit codes OR'd; npm run lint does that itself.
job_weblint() { console_ready && (cd "$ROOT/$CONSOLE" && npm run lint); }

# Cross-file dead code. Exits non-zero on any finding, and is clean today, so
# it gates. The production run (exports only tests reach) stays a report,
# because some of those exports are white-box test hooks on purpose.
job_knip() { console_ready && (cd "$ROOT/$CONSOLE" && npx knip --no-progress); }

# Advisories against the shipped dependency tree only. There is no severity
# threshold, on purpose. That tree is clean, so any advisory at all is news,
# and raising the bar with --audit-level would answer the report instead of
# the finding. The dev tree carries six that this job must not see, because the
# @angular/cli chain's only offered fix is a downgrade to CLI 21.0.4, which is
# semver-major and breaks the Angular 22 pin.
job_deps() { console_ready && (cd "$ROOT/$CONSOLE" && npm run audit:deps); }

# A settings card is an immediate-switch card or a batched-Save card, never
# both. This is scanned in the source, not asserted in the DOM, because
# settings-card admits one variant per card, so a rendered assertion could
# only ever pass.
job_cards() { console_ready && (cd "$ROOT/$CONSOLE" && npx tsx scripts/gates/check-settings-cards.ts); }

# Dead CSS: custom properties nothing reads, `var()` of properties nothing
# declares, token names Material no longer reads, deleted palette values, and
# class selectors no sibling markup mentions.
#
# It gates because the first four are closed questions over text this repo owns
# both sides of. The selector half cannot see a class name assembled at runtime,
# so it is ratcheted against `.deadcss-allow` instead. New ones fail, recorded
# ones do not. Nothing here can see whether a `--mat-*` override still reaches
# Material; only a rendered pixel can, which is what the e2e accent gate is for.
job_deadcss() {
  console_ready || return 1
  (cd "$ROOT/$CONSOLE" && node scripts/gates/dead-css.mjs)
}

# Opt-in. The browsers and their OS libraries are the preflight's business.
# It refuses the run before this is reached, ahead of the other jobs. On
# failure the report and traces stay in the console directory and are not
# uploaded anywhere.
#
# CCO_E2E_OWN_SERVER stops Playwright reusing whatever already holds :4200.
# Under the gate that server may be another worktree's, and a suite that passes
# against someone else's tree is worse than one that refuses to start. CI=true
# would do it too, and would also switch on retries and the github reporter,
# which this repo does not want locally.
job_e2e() {
  console_ready || return 1
  (cd "$ROOT/$CONSOLE" && CCO_E2E_OWN_SERVER=1 npm run e2e) || {
    echo "report: $CONSOLE/playwright-report/  traces: $CONSOLE/test-results/" >&2
    return 1
  }
}

# The task-queue smoke test, the documented emulator steady scenario
# (tests/queueload/doc.go). Conservation and drain are the gates; wall-clock
# throughput on the emulator is meaningless and never judged.
job_queueload() {
  ensure_emulator || return 1
  SPANNER_EMULATOR_HOST="$EMULATOR_HOST" go run ./tests/queueload \
    -scenario steady -rate 20 -duration 30s -instances 2
}

# The lifecycle-invariant baseline. The real stack over fakes at the GCE/HO
# edges, seeded workload, no nemesis. Chaos, stockout and the other nemeses are
# asked for by flag; tests/robustness/doc.go lists them.
#
# The standing-capacity scenario runs with reclamation on, so the keeper both
# holds a target and gives it back inside one run. Without -pool-reclaim the
# seeded mode is provision_only, and every assertion about a lowered lease
# passes without reaching the code that lowers one.
#
# -group-collapse is the third reclamation direction (hosts fenced for a group
# that has ended, which no target describes and which nothing else takes back),
# and it runs SEPARATELY on purpose. Composed into the run above it starved the
# fleet keeper. The group arm provisions a host per create, and a run that ends
# with keeper-hosts=0 has no standing inventory to drain, renew or give back, so
# both of that scenario's arms report a fleet that was never built. Two runs cost
# a minute; one run that silently stops exercising half of what it claims costs
# more than that.
#
# -fleet-floor is the fleet's OTHER lower bound and runs separately for a reason
# of its own. It seeds the free-host buffer at zero, which is exactly the
# configuration the first run drops INTO halfway through. One run cannot hold a
# buffer of two and a buffer of zero, and a composed run would seed one of them
# over the other with nothing reporting which won.
job_robustness() {
  ensure_emulator || return 1
  SPANNER_EMULATOR_HOST="$EMULATOR_HOST" go run ./tests/robustness \
    -instances=2 -duration=45s -seed=42 -pool-floor=2 -pool-reclaim || return 1
  SPANNER_EMULATOR_HOST="$EMULATOR_HOST" go run ./tests/robustness \
    -instances=2 -duration=45s -seed=42 -group-collapse || return 1
  SPANNER_EMULATOR_HOST="$EMULATOR_HOST" go run ./tests/robustness \
    -instances=2 -duration=45s -seed=42 -fleet-floor=2
}

# A real device on a real deployment, driving a cfctl built from this tree.
# The preflight already refused the run unless CCO_LIVESMOKE_PROJECT and
# CCO_LIVESMOKE_REGION are set; -sa and the other flags are for direct
# invocation only (tests/livesmoke/doc.go).
job_livesmoke() {
  go run ./tests/livesmoke -project="$CCO_LIVESMOKE_PROJECT" -region="$CCO_LIVESMOKE_REGION"
}

# Advisory. golangci-lint's `unused` assumes every exported identifier is used
# (staticcheck dropped whole-program mode in 2020.2), so two tools cover what
# it structurally cannot see.
job_deadcode() {
  local failed=0
  go mod tidy -diff || { echo "go.mod is not tidy"; failed=1; }

  # Roots are the shipped binaries only. Running over ./... would make the
  # harness mains under tests/ roots too, keeping production code reachable
  # that nothing deployed can call.
  go run "$DEADCODE" -json ./cmd/... > "$WORK/deadcode.json" || return 1
  jq -r '.[] | (.Path | split("/") | last) as $p
         | .Funcs[] | "\($p).\(.Name)\t\(.Position.File):\(.Position.Line)"' \
    "$WORK/deadcode.json" | sort > "$WORK/deadcode.tsv"
  grep -v '^#' .deadcode-allow | sed '/^[[:space:]]*$/d' | sort > "$WORK/deadcode.allow"
  awk -F'\t' 'NR==FNR {allow[$0]; next} !($1 in allow)' \
    "$WORK/deadcode.allow" "$WORK/deadcode.tsv" > "$WORK/deadcode.new"
  if [ -s "$WORK/deadcode.new" ]; then
    echo "unreachable from any shipped binary:"
    cat "$WORK/deadcode.new"
    failed=1
  fi

  go -C tools/unexportcheck test ./... -count=1 > /dev/null || failed=1
  go -C tools/unexportcheck build -o "$WORK/unexportcheck" . || return 1
  "$WORK/unexportcheck" -allow .unexportcheck-allow ./... || failed=1

  return "$failed"
}

# ---------------------------------------------------------------------------
# One job, in a child of this script. The parent runs each job this way so
# `timeout` can bound it. timeout signals the whole process group, which a
# watchdog around a shell function could not do, since go test and npm spawn
# their own children and would outlive it.
# ---------------------------------------------------------------------------
if [ -n "${CCO_CI_ONE_JOB:-}" ]; then
  if ! known_job "$CCO_CI_ONE_JOB"; then
    echo "unknown job: $CCO_CI_ONE_JOB" >&2
    exit 2
  fi
  # The job tees its own log instead of the parent piping it. A pipeline is
  # not interruptible from the parent, and streaming it back through a `tail`
  # would race the last lines of a failure against the kill that ends it.
  # Inherited stdout keeps the output live either way.
  "job_$CCO_CI_ONE_JOB" 2>&1 | tee "$LOG_DIR/$CCO_CI_ONE_JOB.log"
  exit "${PIPESTATUS[0]}"
fi

# ---------------------------------------------------------------------------
# The machine lock. Only taken when the run wants a job that owns something
# global, so `scripts/ci.sh lint` never waits on a full run.
# ---------------------------------------------------------------------------
wants_exclusive() {
  local job
  for job in "${requested[@]}"; do
    is_exclusive "$job" && return 0
  done
  return 1
}

if [ "$fix_only" -eq 1 ]; then
  fix_env
  exit $?
fi

preflight || exit 2

if wants_exclusive; then
  touch "$LOCKFILE"
  # <> instead of >, because opening for write would truncate the holder's
  # identity before we could read it back.
  exec 9<> "$LOCKFILE"
  if ! flock -n 9; then
    holder="$(cat "$LOCKFILE" 2> /dev/null)"
    if [ "$wait_for_lock" -eq 1 ]; then
      echo "${C_DIM}waiting for the run holding the gate (${holder:-unknown})${C_OFF}"
      flock 9 || { echo "could not take the lock" >&2; exit 1; }
    else
      echo "another run holds the gate: ${holder:-unknown}" >&2
      echo "these jobs own the emulator, the console build output and :4200," >&2
      echo "so two runs would interleave rather than both be right." >&2
      echo "wait for it, pass --wait to queue, or run the read-only jobs" >&2
      echo "instead (e.g. scripts/ci.sh lint vet build)." >&2
      exit 2
    fi
  fi
  printf 'pid %s on %s since %s\n' "$$" "$(hostname)" "$(date '+%Y-%m-%d %H:%M:%S')" > "$LOCKFILE"
fi

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------

# What was checked. The tree moves under this script, because other sessions
# edit it, so a log that does not say which commit and how dirty it was cannot
# answer "whose failure is this?" after the fact.
head_sha="$(git rev-parse --short HEAD 2> /dev/null || echo unknown)"
dirty_count="$(git status --porcelain 2> /dev/null | wc -l)"
printf '%s\n' "${C_DIM}HEAD $head_sha · $dirty_count file(s) modified · pid $$ on $(hostname) · $(date '+%H:%M:%S')${C_OFF}"
printf '%s\n\n' "${C_DIM}logs: $LOG_DIR${C_OFF}"

# timeout's own exit code for a command it killed.
readonly TIMEOUT_STATUS=124

# The job runs in the background and this waits on it, instead of running it
# in the foreground. bash defers a trap until the foreground command finishes,
# so an interactive Ctrl-C would work (the terminal signals the whole group)
# while a `kill -INT` from a script would not stop the run at all. `wait` is
# interruptible, so both do.
#
# 9>&- closes the lock fd for the job. Descendants inherit an open fd, and a
# `go test` that outlives this script would otherwise keep the machine lock
# held after the run that took it is gone. This has been observed.
run_job() {
  local job="$1"
  local mins="$job_timeout_mins"
  if [ "$timeout_explicit" -eq 0 ] && [ -n "${JOB_TIMEOUT_OVERRIDE_MINS[$job]:-}" ]; then
    mins="${JOB_TIMEOUT_OVERRIDE_MINS[$job]}"
  fi
  if [ "$mins" -gt 0 ] && command -v timeout > /dev/null; then
    CCO_CI_ONE_JOB="$job" timeout --kill-after=30s "${mins}m" "$SELF" 9>&- &
  else
    CCO_CI_ONE_JOB="$job" "$SELF" 9>&- &
  fi
  CURRENT_CHILD=$!
  wait "$CURRENT_CHILD"
  local status=$?
  CURRENT_CHILD=""
  return "$status"
}

overall=0
declare -a failed_jobs=()
for job in "${requested[@]}"; do
  if [ "$no_net" -eq 1 ] && needs_net "$job"; then
    RESULT[$job]="SKIP"
    continue
  fi
  printf '%s\n' "${C_DIM}── $job ──────────────────────────────────────────${C_OFF}"
  start=$(date +%s)
  run_job "$job"
  status=$?
  if [ "$status" -eq 0 ]; then
    RESULT[$job]="PASS"
  elif is_advisory "$job"; then
    # Advisory never fails the run, but a timeout is a different message from
    # a finding, so say which one it was.
    if [ "$status" -eq "$TIMEOUT_STATUS" ]; then
      RESULT[$job]="ADVISORY_TIME"
    else
      RESULT[$job]="ADVISORY"
    fi
  elif [ "$status" -eq "$TIMEOUT_STATUS" ]; then
    RESULT[$job]="TIMEOUT"
    failed_jobs+=("$job")
    overall=1
  else
    RESULT[$job]="FAIL"
    failed_jobs+=("$job")
    overall=1
  fi
  SECONDS_TAKEN[$job]=$(($(date +%s) - start))
  echo
done

echo "${C_DIM}══ summary ═══════════════════════════════════════${C_OFF}"
for job in "${requested[@]}"; do
  r="${RESULT[$job]:-SKIP}"
  t="${SECONDS_TAKEN[$job]:-0}"
  case "$r" in
    PASS)     printf '%s  %-10s %ss\n' "${C_GRN}PASS${C_OFF}" "$job" "$t" ;;
    FAIL)     printf '%s  %-10s %ss %s\n' "${C_RED}FAIL${C_OFF}" "$job" "$t" "${C_DIM}$LOG_DIR/$job.log${C_OFF}" ;;
    TIMEOUT)  printf '%s  %-10s %ss %s\n' "${C_RED}TIME${C_OFF}" "$job" "$t" "${C_DIM}exceeded ${job_timeout_mins}m · $LOG_DIR/$job.log${C_OFF}" ;;
    ADVISORY) printf '%s  %-10s %ss %s\n' "${C_YEL}WARN${C_OFF}" "$job" "$t" "${C_DIM}(advisory)${C_OFF}" ;;
    ADVISORY_TIME) printf '%s  %-10s %ss %s\n' "${C_YEL}WARN${C_OFF}" "$job" "$t" "${C_DIM}(advisory, exceeded ${job_timeout_mins}m · $LOG_DIR/$job.log)${C_OFF}" ;;
    SKIP)     printf '%s  %-10s\n' "${C_DIM}SKIP${C_OFF}" "$job" ;;
  esac
done

# One line, last, saying the verdict in words. Every other signal here can be
# misread. A summary block has to be reasoned over, and an exit status is lost
# the moment this runs inside a compound command whose last element succeeds.
echo
if [ "$overall" -eq 0 ]; then
  printf '%sRESULT: PASS%s, %d job(s)\n' "$C_GRN" "$C_OFF" "${#requested[@]}"
else
  printf '%sRESULT: FAIL%s, %d of %d job(s) failed: %s\n' \
    "$C_RED" "$C_OFF" "${#failed_jobs[@]}" "${#requested[@]}" "${failed_jobs[*]}"
fi

exit $overall
