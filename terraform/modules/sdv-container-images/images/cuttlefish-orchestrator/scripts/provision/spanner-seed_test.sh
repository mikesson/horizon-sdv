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

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SEED_SCRIPT="${SCRIPT_DIR}/spanner-seed.sh"

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

cat > "${TMP_ROOT}/schema.sql" <<'EOF'
CREATE TABLE machine_families (
  id STRING(64) NOT NULL
) PRIMARY KEY (id);

INSERT INTO machine_families (id) VALUES ('n2-standard-8');
EOF

make_mock_gcloud() {
  local mode="$1"
  local bin_dir="${TMP_ROOT}/bin_${mode}"
  mkdir -p "$bin_dir"
  cat > "${bin_dir}/gcloud" <<EOF
#!/usr/bin/env bash
set -euo pipefail
case "\$*" in
  *"databases describe "*)
    exit 0
    ;;
  *"databases ddl update "*)
    if [[ "${mode}" == "ddl_fail" ]]; then
      echo "ERROR: DDL syntax error near machine_families" >&2
      exit 1
    fi
    exit 0
    ;;
  *"databases execute-sql "*)
    if [[ "${mode}" == "dml_fail" ]]; then
      echo "ERROR: PERMISSION_DENIED: caller lacks spanner.databases.write" >&2
      exit 1
    fi
    if [[ "${mode}" == "already_exists" ]]; then
      echo "ERROR: (gcloud.spanner.databases.execute-sql) ALREADY_EXISTS: Row already exists" >&2
      exit 1
    fi
    exit 0
    ;;
  *)
    echo "unexpected mock gcloud call: \$*" >&2
    exit 2
    ;;
esac
EOF
  chmod +x "${bin_dir}/gcloud"
  printf '%s' "$bin_dir"
}

run_case() {
  local mode="$1"
  local expect_rc="$2"
  local bin_dir
  bin_dir="$(make_mock_gcloud "$mode")"
  local out_file="${TMP_ROOT}/${mode}.out"

  set +e
  PATH="${bin_dir}:${PATH}" bash "$SEED_SCRIPT" test-proj test-inst test-db "${TMP_ROOT}/schema.sql" >"$out_file" 2>&1
  local rc=$?
  set -e

  if [[ "$expect_rc" == "zero" && "$rc" -ne 0 ]]; then
    echo "FAIL (${mode}): expected exit 0, got ${rc}" >&2
    cat "$out_file" >&2
    exit 1
  fi
  if [[ "$expect_rc" == "nonzero" && "$rc" -eq 0 ]]; then
    echo "FAIL (${mode}): expected non-zero exit, got 0" >&2
    cat "$out_file" >&2
    exit 1
  fi
  printf '%s' "$out_file"
}

echo "Running spanner-seed.sh tests..."
run_case "ddl_fail" "nonzero" >/dev/null
dml_out="$(run_case "dml_fail" "nonzero")"
grep -q "PERMISSION_DENIED" "$dml_out" || { echo "FAIL (dml_fail): missing DML error message"; exit 1; }

exists_out="$(run_case "already_exists" "zero")"
grep -q "Row already exists; skipping" "$exists_out" || { echo "FAIL (already_exists): missing skip log"; exit 1; }

ok_out="$(run_case "clean_ok" "zero")"
grep -q "FROM machine_families" "$ok_out" || { echo "FAIL (clean_ok): verify hint should reference machine_families"; exit 1; }
if grep -q "machine_types" "$ok_out"; then
  echo "FAIL (clean_ok): stale table name machine_types still present" >&2
  exit 1
fi

echo "PASS: all spanner-seed.sh tests succeeded"
