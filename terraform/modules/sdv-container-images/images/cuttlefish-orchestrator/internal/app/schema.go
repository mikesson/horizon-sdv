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

package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

// The startup schema check. On a fresh install the service can start before
// the database exists, before the seed has written the schema, and before
// the service account's database grant takes effect, which can lag the grant
// by minutes. All three read as NotFound or PermissionDenied, so the check
// waits them out for schemaWait. Cloud Run's startup probe in Terraform is
// sized to outlast it. Variables so the emulator test can shorten them.
var (
	schemaWait = 3 * time.Minute
	schemaPoll = 5 * time.Second
)

// checkSchema refuses to start against a database that is not seeded, or
// whose schema is older than this binary's. A newer schema is accepted, because
// an upgrade raises the version before the deploy that needs it, so the
// running binary always sees a schema at least as new as its own.
func checkSchema(ctx context.Context, client *spanner.Client, logger *slog.Logger) error {
	repo := spanner.NewSystemConfigRepository(client)
	waitCtx, cancel := context.WithTimeout(ctx, schemaWait)
	defer cancel()
	for {
		version, err := repo.SchemaVersion(waitCtx)
		if err == nil {
			if version < spanner.SchemaVersion {
				return fmt.Errorf("database schema is v%d and this binary needs v%d: apply the newer files in migrations/spanner/upgrades/ first", version, spanner.SchemaVersion)
			}
			logger.Info("Database schema checked",
				slog.Int("schema_version", version), slog.Int("binary_schema_version", spanner.SchemaVersion))
			return nil
		}
		code := status.Code(err)
		if code != codes.NotFound && code != codes.PermissionDenied && waitCtx.Err() == nil {
			return fmt.Errorf("database schema: %w", err)
		}
		logger.Warn("Database schema not readable yet; retrying", slog.Any("error", err))
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return fmt.Errorf("database schema: %w", context.Cause(ctx))
			}
			return fmt.Errorf("database schema: no readable %s row after %s; seed the database from complete_schema.sql and grant the service account access: %w",
				spanner.SchemaVersionKey, schemaWait, err)
		case <-time.After(schemaPoll):
		}
	}
}
