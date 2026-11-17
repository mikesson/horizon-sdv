// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package spannertest

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

func TestSplitSQLStatements(t *testing.T) {
	t.Parallel()
	src := `-- leading comment
CREATE TABLE a (
    id STRING(36) NOT NULL, -- trailing comment
    v STRING(100) DEFAULT ('semi;colon -- not a comment'),
) PRIMARY KEY (id);

INSERT INTO a (id, v) VALUES ('x', 'it''s a value');
`
	stmts := splitSQLStatements(src)
	if len(stmts) != 2 {
		t.Fatalf("statements = %d, want 2: %q", len(stmts), stmts)
	}
	if !strings.Contains(stmts[0], "'semi;colon -- not a comment'") {
		t.Errorf("string literal mangled: %q", stmts[0])
	}
	if strings.Contains(stmts[0], "trailing comment") {
		t.Errorf("comment not stripped: %q", stmts[0])
	}
	if !strings.Contains(stmts[1], "'it''s a value'") {
		t.Errorf("escaped quote mangled: %q", stmts[1])
	}
}

// TestSchemaDDLFromAuthoritativeFile pins the parse of the real schema file:
// DDL only (seed INSERTs dropped) and the tables every repository test relies on.
func TestSchemaDDLFromAuthoritativeFile(t *testing.T) {
	t.Parallel()
	ddl, err := SchemaDDL()
	if err != nil {
		t.Fatalf("schemaDDL: %v", err)
	}
	joined := strings.Join(ddl, "\n")
	for _, stmt := range ddl {
		if !strings.HasPrefix(strings.ToUpper(stmt), "CREATE") {
			t.Errorf("non-DDL statement leaked through: %.60q", stmt)
		}
	}
	if strings.Contains(joined, "GENERATE_UUID") {
		t.Error("GENERATE_UUID defaults must be stripped (emulator re-applies defaults on update when the key has one)")
	}
	for _, table := range []string{"hosts", "devices", "builds", "quotas", "tasks", "leader_leases", "idempotency_keys"} {
		if !strings.Contains(joined, "CREATE TABLE "+table+" ") {
			t.Errorf("schema missing table %s", table)
		}
	}
}

// The binary's schema version, the schema file's header and the seeded
// schema.version row must agree. A bump to one alone either refuses every
// fresh install or lets a binary start against a schema it cannot read.
func TestSchemaVersionMatchesSchemaFile(t *testing.T) {
	t.Parallel()
	path, err := schemaPath()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strconv.Itoa(spannerplatform.SchemaVersion)
	if header := "Complete Schema v" + want + " "; !strings.Contains(string(raw), header) {
		t.Errorf("complete_schema.sql header does not read %q", header)
	}
	seed := fmt.Sprintf("VALUES ('%s', '%s',", spannerplatform.SchemaVersionKey, want)
	found := false
	for _, stmt := range splitSQLStatements(string(raw)) {
		if strings.HasPrefix(stmt, "INSERT INTO system_config") && strings.Contains(stmt, seed) {
			found = true
		}
	}
	if !found {
		t.Errorf("complete_schema.sql seeds no system_config row %s", seed)
	}
}
