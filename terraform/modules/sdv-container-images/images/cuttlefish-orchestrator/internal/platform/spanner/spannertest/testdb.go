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
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	database "cloud.google.com/go/spanner/admin/database/apiv1"
	"cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	instance "cloud.google.com/go/spanner/admin/instance/apiv1"
	"cloud.google.com/go/spanner/admin/instance/apiv1/instancepb"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

const (
	// TestProject is the project ID used for emulator tests.
	TestProject = "test-project"

	// TestInstance is the instance ID used for emulator tests.
	TestInstance = "test-instance"
)

var (
	// globalMu protects globalClient and globalSetup.
	globalMu    sync.Mutex
	globalSetup bool

	// testDBCounter generates unique database names.
	testDBCounter int
	counterMu     sync.Mutex
)

// EmulatorHost returns the Spanner emulator host from environment, or empty string if not set.
func EmulatorHost() string {
	return os.Getenv("SPANNER_EMULATOR_HOST")
}

// SkipIfNoEmulator returns true if the emulator is not available.
// Use this in TestMain to skip integration tests when no emulator is running.
func SkipIfNoEmulator() bool {
	return EmulatorHost() == ""
}

// SkipWithoutEmulator skips the test if no emulator is available. With
// CCO_REQUIRE_EMULATOR set (CI), a missing emulator FAILS the test instead:
// the emulator suite is the only executed verification of the persistence
// layer's invariants (settle CAS, atomic quota, idempotency, leases), and a
// silent skip would report green while verifying none of it.
func SkipWithoutEmulator(t *testing.T) {
	t.Helper()
	if !SkipIfNoEmulator() {
		return
	}
	if os.Getenv("CCO_REQUIRE_EMULATOR") != "" {
		t.Fatal("SPANNER_EMULATOR_HOST not set but CCO_REQUIRE_EMULATOR is: the emulator suite must run, not skip")
	}
	t.Skip("Skipping: SPANNER_EMULATOR_HOST not set. Start emulator with: gcloud emulators spanner start")
}

// emulatorOpts returns gRPC options for connecting to the emulator.
func emulatorOpts() []option.ClientOption {
	host := EmulatorHost()
	return []option.ClientOption{
		option.WithEndpoint(host),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		option.WithoutAuthentication(),
	}
}

// ensureInstance creates the test instance in the emulator if it doesn't exist.
func ensureInstance(ctx context.Context) error {
	globalMu.Lock()
	defer globalMu.Unlock()

	if globalSetup {
		return nil
	}

	client, err := instance.NewInstanceAdminClient(ctx, emulatorOpts()...)
	if err != nil {
		return fmt.Errorf("failed to create instance admin client: %w", err)
	}
	defer client.Close()

	instancePath := fmt.Sprintf("projects/%s/instances/%s", TestProject, TestInstance)

	// Check if instance exists.
	_, err = client.GetInstance(ctx, &instancepb.GetInstanceRequest{Name: instancePath})
	if err == nil {
		globalSetup = true
		return nil
	}

	if status.Code(err) != codes.NotFound {
		return fmt.Errorf("failed to check instance: %w", err)
	}

	// Create instance.
	op, err := client.CreateInstance(ctx, &instancepb.CreateInstanceRequest{
		Parent:     "projects/" + TestProject,
		InstanceId: TestInstance,
		Instance: &instancepb.Instance{
			Name:        instancePath,
			Config:      fmt.Sprintf("projects/%s/instanceConfigs/emulator-config", TestProject),
			DisplayName: "Test Instance",
			NodeCount:   1,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create instance: %w", err)
	}

	if _, err := op.Wait(ctx); err != nil {
		return fmt.Errorf("failed to wait for instance creation: %w", err)
	}

	globalSetup = true
	return nil
}

// generateDBName creates a unique database name for the test.
func generateDBName() string {
	counterMu.Lock()
	defer counterMu.Unlock()
	testDBCounter++
	return fmt.Sprintf("testdb%d_%d", time.Now().UnixNano(), testDBCounter)
}

// TestDB represents a test database with cleanup capabilities.
type TestDB struct {
	Name     string
	Client   *spannerplatform.Client
	dbClient *database.DatabaseAdminClient
}

// NewTestDB creates a new test database with the full schema.
// Returns a TestDB with cleanup function. Call Close() when done.
func NewTestDB(ctx context.Context) (*TestDB, error) {
	if SkipIfNoEmulator() {
		return nil, errors.New("SPANNER_EMULATOR_HOST not set")
	}

	if err := ensureInstance(ctx); err != nil {
		return nil, err
	}

	dbName := generateDBName()
	dbClient, err := database.NewDatabaseAdminClient(ctx, emulatorOpts()...)
	if err != nil {
		return nil, fmt.Errorf("failed to create database admin client: %w", err)
	}

	ddl, err := SchemaDDL()
	if err != nil {
		dbClient.Close() //nolint:gosec // Best-effort cleanup on error path
		return nil, fmt.Errorf("loading schema DDL: %w", err)
	}

	// Create database with schema.
	instancePath := fmt.Sprintf("projects/%s/instances/%s", TestProject, TestInstance)
	op, err := dbClient.CreateDatabase(ctx, &databasepb.CreateDatabaseRequest{
		Parent:          instancePath,
		CreateStatement: fmt.Sprintf("CREATE DATABASE `%s`", dbName),
		ExtraStatements: ddl,
	})
	if err != nil {
		dbClient.Close() //nolint:gosec // Best-effort cleanup on error path
		return nil, fmt.Errorf("failed to create database: %w", err)
	}

	if _, err := op.Wait(ctx); err != nil {
		dbClient.Close() //nolint:gosec // Best-effort cleanup on error path
		return nil, fmt.Errorf("failed to wait for database creation: %w", err)
	}

	// Create Spanner client.
	cfg := spannerplatform.Config{
		ProjectID:  TestProject,
		InstanceID: TestInstance,
		DatabaseID: dbName,
		Emulator:   EmulatorHost(),
	}

	client, err := spannerplatform.NewClient(ctx, cfg)
	if err != nil {
		dbClient.Close() //nolint:gosec // Best-effort cleanup on error path
		return nil, fmt.Errorf("failed to create spanner client: %w", err)
	}

	return &TestDB{
		Name:     dbName,
		Client:   client,
		dbClient: dbClient,
	}, nil
}

// Close drops the test database and closes all clients.
func (db *TestDB) Close() error {
	if db.Client != nil {
		db.Client.Close()
	}

	if db.dbClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		dbPath := fmt.Sprintf("projects/%s/instances/%s/databases/%s",
			TestProject, TestInstance, db.Name)
		_ = db.dbClient.DropDatabase(ctx, &databasepb.DropDatabaseRequest{Database: dbPath})
		db.dbClient.Close() //nolint:gosec // Best-effort cleanup in Close()
	}

	return nil
}

// MustNewTestDB creates a new test database and fails the test if it fails.
func MustNewTestDB(t *testing.T) *TestDB {
	t.Helper()
	SkipWithoutEmulator(t)

	ctx := context.Background() //nolint:usetesting // t.Context() is canceled just before t.Cleanup runs, and this ctx backs the Spanner client the test's own cleanups still call through.
	db, err := NewTestDB(ctx)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Logf("Warning: failed to close test database: %v", err)
		}
	})

	return db
}
