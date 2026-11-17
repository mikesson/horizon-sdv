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
	"log/slog"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// shortenSchemaWait keeps the NotFound retry from holding a test for the
// production three minutes.
func shortenSchemaWait(t *testing.T) {
	t.Helper()
	wait, poll := schemaWait, schemaPoll
	schemaWait, schemaPoll = 2*time.Second, 100*time.Millisecond
	t.Cleanup(func() { schemaWait, schemaPoll = wait, poll })
}

// A database created from the DDL alone has no schema.version row, which is
// what a revision sees when it starts before the seed has run. Run must
// refuse it, so the startup probe fails the revision and the apply with it.
// Without the check Run serves, and the test stops it with SIGTERM.
func TestRunRefusesUnseededDatabase(t *testing.T) {
	db := spannertest.MustNewTestDB(t)
	shortenSchemaWait(t)
	t.Setenv("CCO_DEVICE_INGRESS", "public")
	t.Setenv("CCO_PROJECT_ID", spannertest.TestProject)
	t.Setenv("CCO_HOST_IMAGE_PROJECT", spannertest.TestProject)
	t.Setenv("CCO_DEPLOYMENT", "cco")
	t.Setenv("CCO_SPANNER_PROJECT", spannertest.TestProject)
	t.Setenv("CCO_SPANNER_INSTANCE", spannertest.TestInstance)
	t.Setenv("CCO_SPANNER_DATABASE", db.Name)

	done := make(chan error, 1)
	go func() { done <- Run() }()

	var err error
	select {
	case err = <-done:
	case <-time.After(time.Minute):
		// Run is inside its own signal.NotifyContext here, so the signal stops
		// it rather than the test binary.
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		<-done
		t.Fatal("Run served against a database with no schema.version row")
	}
	if err == nil {
		t.Fatal("Run returned nil against a database with no schema.version row")
	}
	if !strings.Contains(err.Error(), spanner.SchemaVersionKey) {
		t.Errorf("Run error = %q, want it to name %s", err, spanner.SchemaVersionKey)
	}
}

func TestCheckSchemaComparesVersions(t *testing.T) {
	db := spannertest.MustNewTestDB(t)
	shortenSchemaWait(t)
	ctx := t.Context()
	repo := spanner.NewSystemConfigRepository(db.Client)
	logger := slog.New(slog.DiscardHandler)

	set := func(version int) {
		t.Helper()
		if err := repo.Set(ctx, spanner.SchemaVersionKey, strconv.Itoa(version), "test"); err != nil {
			t.Fatalf("seeding %s: %v", spanner.SchemaVersionKey, err)
		}
	}

	set(spanner.SchemaVersion - 1)
	err := checkSchema(ctx, db.Client, logger)
	if err == nil {
		t.Fatal("checkSchema accepted a schema older than the binary")
	}
	if want := "v" + strconv.Itoa(spanner.SchemaVersion-1); !strings.Contains(err.Error(), want) {
		t.Errorf("checkSchema error = %q, want it to name %s", err, want)
	}

	for _, version := range []int{spanner.SchemaVersion, spanner.SchemaVersion + 1} {
		set(version)
		if err := checkSchema(ctx, db.Client, logger); err != nil {
			t.Errorf("checkSchema at v%d = %v, want nil", version, err)
		}
	}
}
