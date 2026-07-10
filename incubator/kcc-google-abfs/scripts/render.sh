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

# Render the parameterized ABFS KCC manifests + Helm chart values for one instance.
#
# Every project-specific value is kept as a REPLACE_<NAME> token. This script
# substitutes each token with the matching KEY from an instance env file, writing
# a deployable copy under rendered/<instance>/ (the source tree stays templated).
#
# Usage:
#   scripts/render.sh instances/example.env [output_dir]
#
# Then:
#   kubectl apply -k rendered/example/infra
#   helm install abfs rendered/example/chart/abfs -n abfs \
#     -f rendered/example/chart/abfs/values.yaml   # then --set licensed=true (docs/04)
# The license is delivered via the abfs-data node pool's metadata, not Helm (docs/02 §1b).
#
# Tokens with no value in the env file are left as-is (e.g. infra/cicd/ tokens
# when you only render the core data plane) and are reported at the end.
set -euo pipefail

ENV_FILE="${1:?usage: scripts/render.sh instances/<name>.env [output_dir]}"
[ -f "$ENV_FILE" ] || { echo "no such env file: $ENV_FILE" >&2; exit 1; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${2:-$ROOT/rendered/$(basename "$ENV_FILE" .env)}"

# Load KEY=VALUE pairs (comments / blanks ignored) into the environment.
set -a; # shellcheck disable=SC1090
source "$ENV_FILE"; set +a

rm -rf "$OUT"; mkdir -p "$OUT"
cp -r "$ROOT/infra" "$OUT/infra"
cp -r "$ROOT/chart" "$OUT/chart"

# Substitute REPLACE_<NAME> -> ${NAME} for every NAME set in the env file.
mapfile -t FILES < <(find "$OUT" -type f \( -name '*.yaml' -o -name '*.yml' \))
for f in "${FILES[@]}"; do
  for tok in $(grep -ohE 'REPLACE_[A-Z0-9_]+' "$f" 2>/dev/null | sort -u); do
    var="${tok#REPLACE_}"
    val="${!var-}"
    [ -n "$val" ] && sed -i "s|${tok}|${val}|g" "$f"
  done
done

# Create-vs-reference toggle (mirrors Terraform's create-or-data SA pattern).
# When CREATE_RUNTIME_SAS=false the data plane runs as a pre-existing, already-licensed
# SA (RUNTIME_SA), so drop the SA-creation manifest and its kustomization entry — KCC
# must not try to create/acquire the existing SA. The 12-* role grants still target it
# via an external reference.
if [ "${CREATE_RUNTIME_SAS:-true}" = "false" ]; then
  rm -f "$OUT/infra/10-iam-service-accounts.yaml"
  sed -i '/10-iam-service-accounts\.yaml/d' "$OUT/infra/kustomization.yaml"
  echo "CREATE_RUNTIME_SAS=false: dropped infra/10-iam-service-accounts.yaml (referencing pre-existing SAs)."
fi

# Spanner schema toggle (mirrors Terraform's abfs_spanner_database_create_tables, default
# false). The ABFS server (image 0.1.14) does NOT create the schema at runtime, so the
# SpannerDatabase carries it in spec.ddl. When CREATE_TABLES != true, blank that ddl —
# apply the schema out-of-band instead (see docs/04 §5).
if [ "${CREATE_TABLES:-false}" != "true" ]; then
  awk '
    /# ABFS-DDL-BEGIN/ { print "  ddl: []"; skip=1; next }
    /# ABFS-DDL-END/   { skip=0; next }
    skip { next }
    { print }
  ' "$OUT/infra/20-spanner.yaml" > "$OUT/infra/20-spanner.yaml.tmp" && mv "$OUT/infra/20-spanner.yaml.tmp" "$OUT/infra/20-spanner.yaml"
  echo "CREATE_TABLES != true: SpannerDatabase ddl set to [] (apply the schema out-of-band; see docs/04 §5)."
fi

echo "Rendered $ENV_FILE -> $OUT"
LEFT="$(grep -rhoE 'REPLACE_[A-Z0-9_]+' "$OUT" 2>/dev/null | sort -u || true)"
if [ -n "$LEFT" ]; then
  echo "Unfilled tokens (no value in $ENV_FILE) left in place:"
  echo "$LEFT" | sed 's/^/  /'
fi
