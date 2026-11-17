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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// schemaOnce caches the parsed schema; the file is identical for every test DB.
var schemaOnce = sync.OnceValues(loadSchemaDDL)

// SchemaDDL returns the DDL statements parsed from
// migrations/spanner/complete_schema.sql, the single authoritative schema,
// so the test schema can never drift from the deployed one. Exported for
// tooling (tests/queueload) that bootstraps scratch databases from the same
// source. Two emulator adaptations (both harmless on real Spanner, where
// callers always provide explicit IDs):
//
//   - Seed-data INSERT statements are dropped: CreateDatabase accepts only
//     DDL, and tests seed exactly what they assert on.
//   - DEFAULT (GENERATE_UUID()) is stripped (it only appears on primary
//     keys). The bundled emulator misbehaves when a table's key column has a
//     default: UPDATE mutations then re-apply column DEFAULTs to every
//     unspecified column (e.g. a hosts counter update silently resets status
//     to 'pending'). Real Spanner does not do this. Tests always provide IDs.
//
// Everything else (non-key column defaults, partial indexes, row deletion
// policies, interleaving) is emulator-supported and kept verbatim.
func SchemaDDL() ([]string, error) {
	return schemaOnce()
}

func loadSchemaDDL() ([]string, error) {
	path, err := schemaPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading schema: %w", err)
	}
	var ddl []string
	for _, stmt := range splitSQLStatements(string(raw)) {
		if strings.HasPrefix(strings.ToUpper(stmt), "INSERT") {
			continue // seed DML; not DDL
		}
		stmt = strings.ReplaceAll(stmt, " DEFAULT (GENERATE_UUID())", "")
		ddl = append(ddl, stmt)
	}
	if len(ddl) == 0 {
		return nil, fmt.Errorf("no DDL statements parsed from %s", path)
	}
	return ddl, nil
}

// schemaPath locates migrations/spanner/complete_schema.sql by walking up from
// this source file's directory to the module root (go.mod).
func schemaPath() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("cannot determine spannertest source location")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "migrations", "spanner", "complete_schema.sql"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found above %s", file)
		}
		dir = parent
	}
}

// splitSQLStatements splits SQL text into statements on semicolons, stripping
// "--" line comments; both are recognized only outside single-quoted string
// literals (a quote is escaped by doubling it).
func splitSQLStatements(src string) []string {
	var stmts []string
	var b strings.Builder
	inString := false
	// An index loop rather than `for i := range src`, because the body advances
	// i itself to consume a doubled '' escape and to skip a -- comment to the
	// end of the line. A range loop ignores those writes and rescans the
	// characters.
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case inString:
			b.WriteByte(c)
			if c == '\'' {
				if i+1 < len(src) && src[i+1] == '\'' {
					b.WriteByte(src[i+1])
					i++
				} else {
					inString = false
				}
			}
		case c == '\'':
			inString = true
			b.WriteByte(c)
		case c == '-' && i+1 < len(src) && src[i+1] == '-':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
		case c == ';':
			if s := strings.TrimSpace(b.String()); s != "" {
				stmts = append(stmts, s)
			}
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	if s := strings.TrimSpace(b.String()); s != "" {
		stmts = append(stmts, s)
	}
	return stmts
}
