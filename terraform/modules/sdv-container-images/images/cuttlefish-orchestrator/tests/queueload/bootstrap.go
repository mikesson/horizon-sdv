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

package main

import (
	"context"
	"fmt"

	gspanner "cloud.google.com/go/spanner"
	database "cloud.google.com/go/spanner/admin/database/apiv1"
	"cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	instance "cloud.google.com/go/spanner/admin/instance/apiv1"
	"cloud.google.com/go/spanner/admin/instance/apiv1/instancepb"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// countersTable is harness-owned scratch state, kept out of
// complete_schema.sql on purpose. The counter-shaped enqueue reads and
// increments one of its rows in the enqueue transaction, mimicking the
// production device-create commit (device row + host counter + task).
const (
	countersTable = "loadtest_counters"
	countersDDL   = "CREATE TABLE loadtest_counters (counter_id INT64 NOT NULL, n INT64 NOT NULL) PRIMARY KEY (counter_id)"
)

func databasePath(cfg Cfg) string {
	return fmt.Sprintf("projects/%s/instances/%s/databases/%s", cfg.Project, cfg.Instance, cfg.Database)
}

// bootstrap makes the target database exist and match the authoritative
// schema plus the harness scratch table. On the emulator it also creates the
// instance; on real Spanner a missing instance is the operator's call
// (creating billed infrastructure is out of scope for a load harness).
func bootstrap(ctx context.Context, cfg Cfg) error {
	var opts []option.ClientOption
	if cfg.Emulator != "" {
		opts = []option.ClientOption{
			option.WithEndpoint(cfg.Emulator),
			option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
			option.WithoutAuthentication(),
		}
		if err := ensureEmulatorInstance(ctx, cfg, opts); err != nil {
			return err
		}
	}

	admin, err := database.NewDatabaseAdminClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("creating database admin client: %w", err)
	}
	defer admin.Close()

	_, err = admin.GetDatabase(ctx, &databasepb.GetDatabaseRequest{Name: databasePath(cfg)})
	switch {
	case err == nil:
		return ensureCountersTable(ctx, cfg, admin)
	case status.Code(err) != codes.NotFound:
		return fmt.Errorf("checking database: %w", err)
	}

	ddl, err := spannertest.SchemaDDL()
	if err != nil {
		return fmt.Errorf("loading schema DDL: %w", err)
	}
	op, err := admin.CreateDatabase(ctx, &databasepb.CreateDatabaseRequest{
		Parent:          fmt.Sprintf("projects/%s/instances/%s", cfg.Project, cfg.Instance),
		CreateStatement: fmt.Sprintf("CREATE DATABASE `%s`", cfg.Database),
		ExtraStatements: append(ddl, countersDDL),
	})
	if err != nil {
		return fmt.Errorf("creating database %s: %w", cfg.Database, err)
	}
	if _, err := op.Wait(ctx); err != nil {
		return fmt.Errorf("waiting for database creation: %w", err)
	}
	return nil
}

func ensureEmulatorInstance(ctx context.Context, cfg Cfg, opts []option.ClientOption) error {
	admin, err := instance.NewInstanceAdminClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("creating instance admin client: %w", err)
	}
	defer admin.Close()

	path := fmt.Sprintf("projects/%s/instances/%s", cfg.Project, cfg.Instance)
	if _, err := admin.GetInstance(ctx, &instancepb.GetInstanceRequest{Name: path}); err == nil {
		return nil
	} else if status.Code(err) != codes.NotFound {
		return fmt.Errorf("checking emulator instance: %w", err)
	}
	op, err := admin.CreateInstance(ctx, &instancepb.CreateInstanceRequest{
		Parent:     "projects/" + cfg.Project,
		InstanceId: cfg.Instance,
		Instance: &instancepb.Instance{
			Name:        path,
			Config:      fmt.Sprintf("projects/%s/instanceConfigs/emulator-config", cfg.Project),
			DisplayName: "queueload",
			NodeCount:   1,
		},
	})
	if err != nil {
		return fmt.Errorf("creating emulator instance: %w", err)
	}
	if _, err := op.Wait(ctx); err != nil {
		return fmt.Errorf("waiting for emulator instance: %w", err)
	}
	return nil
}

// ensureCountersTable adds the scratch table to a pre-existing database.
func ensureCountersTable(ctx context.Context, cfg Cfg, admin *database.DatabaseAdminClient) error {
	op, err := admin.UpdateDatabaseDdl(ctx, &databasepb.UpdateDatabaseDdlRequest{
		Database:   databasePath(cfg),
		Statements: []string{countersDDL},
	})
	if err != nil {
		if status.Code(err) == codes.FailedPrecondition || status.Code(err) == codes.AlreadyExists {
			return nil // table already there
		}
		return fmt.Errorf("adding %s: %w", countersTable, err)
	}
	if err := op.Wait(ctx); err != nil {
		if status.Code(err) == codes.FailedPrecondition || status.Code(err) == codes.AlreadyExists {
			return nil
		}
		return fmt.Errorf("waiting for %s DDL: %w", countersTable, err)
	}
	return nil
}

// reset wipes queue state from a previous run and reseeds the counter rows, so
// every run starts from a clean, comparable baseline.
func reset(ctx context.Context, raw *gspanner.Client, cfg Cfg) error {
	for _, stmt := range []string{
		"DELETE FROM tasks WHERE TRUE",
		"DELETE FROM leader_leases WHERE TRUE",
		"DELETE FROM loadtest_counters WHERE TRUE",
	} {
		if _, err := raw.PartitionedUpdate(ctx, gspanner.Statement{SQL: stmt}); err != nil {
			return fmt.Errorf("reset %q: %w", stmt, err)
		}
	}
	muts := make([]*gspanner.Mutation, 0, cfg.HotRows)
	for i := range cfg.HotRows {
		muts = append(muts, gspanner.Insert(countersTable,
			[]string{"counter_id", "n"}, []any{int64(i), int64(0)}))
	}
	if _, err := raw.Apply(ctx, muts); err != nil {
		return fmt.Errorf("seeding counters: %w", err)
	}
	return nil
}
