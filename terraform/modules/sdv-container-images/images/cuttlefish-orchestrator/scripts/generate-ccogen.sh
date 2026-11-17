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
# Regenerates cfctl's models from the backend's OpenAPI document:
# specgen -dump emits the 3.0 spec from the in-process router, oapi-codegen
# turns it into cmd/cfctl/internal/cco/ccogen/ccogen.gen.go, and the license
# header is prepended so the committed file carries it like every other
# source file. The Go generated-code convention allows comments above the
# "Code generated" line, so tooling still treats the file as generated.
#
#   scripts/generate-ccogen.sh           regenerate in place
#   scripts/generate-ccogen.sh --check   regenerate to a temp file and diff
#                                        against the committed one; non-zero
#                                        on drift (the ci.sh gen job)

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# Pinned like GOVULNCHECK and DEADCODE in ci.sh: `go run pkg@version` resolves
# against the tool's own go.sum, so this is reproducible without giving the
# orchestrator module a generator dependency.
readonly OAPI_CODEGEN="github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0"
readonly CONFIG="cmd/cfctl/internal/cco/ccogen/oapi-codegen.yaml"
readonly TARGET="cmd/cfctl/internal/cco/ccogen/ccogen.gen.go"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

go run ./tests/specgen -dump > "$WORK/openapi-3.0.json" 2> /dev/null

go run "$OAPI_CODEGEN" -config "$CONFIG" -o "$WORK/models.gen.go" \
  "$WORK/openapi-3.0.json"

{
  sed -n '2,14p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}/\/\/ /;s/ $//'
  echo
  cat "$WORK/models.gen.go"
} > "$WORK/final.gen.go"

if [ "${1:-}" = "--check" ]; then
  if ! diff -u "$TARGET" "$WORK/final.gen.go" > "$WORK/drift.diff"; then
    echo "$TARGET is stale relative to the OpenAPI document." >&2
    echo "Regenerate with scripts/generate-ccogen.sh. Drift:" >&2
    head -40 "$WORK/drift.diff" >&2
    exit 1
  fi
  echo "$TARGET matches the spec"
else
  mv "$WORK/final.gen.go" "$TARGET"
  echo "wrote $TARGET"
fi
