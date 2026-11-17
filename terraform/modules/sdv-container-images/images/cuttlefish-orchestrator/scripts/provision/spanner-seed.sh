#!/bin/bash
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

# Apply Cloud Spanner schema from complete_schema.sql
#
# This script:
# 1. Extracts DDL statements (CREATE TABLE, CREATE INDEX) and applies them
# 2. Extracts DML statements (INSERT INTO) and applies them
#
# Usage: ./spanner-seed.sh <project_id> <instance_name> <database_name> [schema_file]

set -e

PROJECT_ID="${1:?Error: project_id required}"
INSTANCE="${2:?Error: instance_name required}"
DATABASE="${3:?Error: database_name required}"
SCHEMA_FILE="${4:-$(dirname "$0")/../../migrations/spanner/complete_schema.sql}"

# Check if schema file exists
if [[ ! -f "$SCHEMA_FILE" ]]; then
    echo "Error: Schema file not found: $SCHEMA_FILE"
    exit 1
fi

echo "=== Spanner Database Seeding ==="
echo "Project:  $PROJECT_ID"
echo "Instance: $INSTANCE"
echo "Database: $DATABASE"
echo "Schema:   $SCHEMA_FILE"
echo ""

# Check if database exists
if ! gcloud spanner databases describe "$DATABASE" \
    --project="$PROJECT_ID" \
    --instance="$INSTANCE" &>/dev/null; then
    echo "Error: Database '$DATABASE' does not exist in instance '$INSTANCE'"
    echo "Create it first with: gcloud spanner databases create $DATABASE --instance=$INSTANCE"
    exit 1
fi

# Create temp directory for the extracted DDL/DML.
TEMP_DIR=$(mktemp -d)
trap 'rm -rf "$TEMP_DIR"' EXIT

# Extract the DDL (everything except comments and INSERT..; blocks) into one
# file and apply it with --ddl-file. gcloud reads the semicolon-separated
# statements directly, so multi-line definitions and quoted DEFAULT/CHECK
# clauses survive verbatim. An earlier approach rebuilt each statement into a
# --ddl="..." string via xargs+eval, which mangled the quoting and silently
# left the database empty (the failure was hidden by `|| true`). CREATE ... IF
# NOT EXISTS keeps re-runs idempotent; any real DDL error now aborts the script
# (set -e) instead of being swallowed.
echo "=== Applying DDL statements ==="
awk '
    /^[[:space:]]*--/ { next }
    /^INSERT INTO/    { in_insert=1; next }
    in_insert         { if (/;[[:space:]]*$/) in_insert=0; next }
    { print }
' "$SCHEMA_FILE" \
  | sed -E 's/^CREATE TABLE /CREATE TABLE IF NOT EXISTS /;
            s/^CREATE ((UNIQUE |NULL_FILTERED )*)INDEX /CREATE \1INDEX IF NOT EXISTS /' \
  > "$TEMP_DIR/ddl.sql"

DDL_COUNT=$(grep -c ';[[:space:]]*$' "$TEMP_DIR/ddl.sql" || true)
echo "Found $DDL_COUNT DDL statements"

if [[ "$DDL_COUNT" -gt 0 ]]; then
    gcloud spanner databases ddl update "$DATABASE" \
        --project="$PROJECT_ID" \
        --instance="$INSTANCE" \
        --ddl-file="$TEMP_DIR/ddl.sql"
    echo "DDL applied successfully"
else
    echo "No DDL statements to apply"
fi

echo ""
echo "=== Extracting DML statements (seed data) ==="

# Extract INSERT statements (multi-line: from INSERT until the closing ";"),
# flattening each statement onto one line separated by a blank line.
awk '
    /^INSERT INTO/ { in_insert=1 }
    in_insert      { printf "%s ", $0 }
    /;[[:space:]]*$/ { if (in_insert) { print ""; in_insert=0 } }
' "$SCHEMA_FILE" > "$TEMP_DIR/dml.sql"

# Count DML statements
DML_COUNT=$(grep -c "INSERT INTO" "$TEMP_DIR/dml.sql" || echo "0")
echo "Found $DML_COUNT DML statements"

if [[ "$DML_COUNT" -gt 0 ]]; then
    echo "Applying seed data..."

    # Execute each INSERT statement
    while IFS= read -r stmt; do
        # Skip empty lines
        [[ -z "$stmt" ]] && continue
        # Strip the trailing semicolon
        stmt="${stmt%;*}"

        echo "  Inserting: $(echo "$stmt" | cut -c1-60)..."
        gcloud spanner databases execute-sql "$DATABASE" \
            --project="$PROJECT_ID" \
            --instance="$INSTANCE" \
            --sql="$stmt" 2>&1 | grep -v "already exists" || true
    done < "$TEMP_DIR/dml.sql"

    echo "Seed data applied successfully"
else
    echo "No seed data to apply"
fi

echo ""
echo "=== Database seeding complete ==="
echo "Verify with: gcloud spanner databases execute-sql $DATABASE --instance=$INSTANCE --sql=\"SELECT COUNT(*) FROM machine_types\""
