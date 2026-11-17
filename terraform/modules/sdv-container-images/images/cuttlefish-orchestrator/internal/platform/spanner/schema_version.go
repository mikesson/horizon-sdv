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

package spanner

import (
	"context"
	"fmt"
	"strconv"

	"cloud.google.com/go/spanner"
)

// SchemaVersion is the schema this binary reads and writes. It must equal the
// version in the header of migrations/spanner/complete_schema.sql, and every
// bump adds migrations/spanner/upgrades/v<N>.sql, which raises the
// SchemaVersionKey row to match.
const SchemaVersion = 87

// SchemaVersionKey is the system_config row recording which schema version
// the database holds. The seed writes it, and each schema change raises it.
const SchemaVersionKey = "schema.version"

// SchemaVersion reads the schema version the database records. A missing row
// keeps the Spanner NotFound code, the same code as a database that does not
// exist yet, so a caller can wait out both alike.
func (r *SystemConfigRepository) SchemaVersion(ctx context.Context) (int, error) {
	row, err := r.client.Single().ReadRow(ctx, "system_config", spanner.Key{SchemaVersionKey}, []string{colConfigValue})
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", SchemaVersionKey, err)
	}
	var value string
	if err := row.Columns(&value); err != nil {
		return 0, fmt.Errorf("scanning %s: %w", SchemaVersionKey, err)
	}
	version, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s holds %q, which is not a version number: %w", SchemaVersionKey, value, err)
	}
	return version, nil
}
