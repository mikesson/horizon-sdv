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
	"slices"
	"time"

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

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

func databasePath(cfg Cfg) string {
	return fmt.Sprintf("projects/%s/instances/%s/databases/%s", cfg.Project, cfg.Instance, cfg.Database)
}

// bootstrap makes the target database exist with the authoritative schema.
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
		if cfg.Emulator == "" {
			return nil
		}
		// An emulator database left by an earlier run carries that run's
		// schema; a table added since would fail the run minutes in. reset()
		// wipes all rows anyway, so recreating from the authoritative schema
		// loses nothing and keeps "bootstrap == current schema" true.
		if err := admin.DropDatabase(ctx, &databasepb.DropDatabaseRequest{Database: databasePath(cfg)}); err != nil {
			return fmt.Errorf("dropping stale emulator database: %w", err)
		}
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
		ExtraStatements: ddl,
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
			DisplayName: "robustness",
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

// Seed-row column keys, named once for the seeding block below.
const (
	colCreatedAt   = "created_at"
	colUpdatedAt   = "updated_at"
	colIsEnabled   = "is_enabled"
	colConfigKey   = "config_key"
	colConfigValue = "config_value"
)

// reset wipes lifecycle state from a previous run and seeds what the create
// paths require: the quota row (host create), the default machine type the
// declarative resolver shapes auto-provisioned hosts from, and the enabled
// zone set placement ranks over.
//
// What Compute Engine offers is not seeded and cannot be: it lives in an
// in-process snapshot the harness never builds, so every availability cell
// reads as unknown, which under the fail-open rule removes no zone, leaving
// both enabled zones as candidates. That is the state a cold instance serves
// in, so the harness exercises it rather than papering over it.
func reset(ctx context.Context, raw *gspanner.Client, cfg Cfg) error {
	for _, stmt := range []string{
		"DELETE FROM devices WHERE TRUE",
		"DELETE FROM hosts WHERE TRUE",
		"DELETE FROM tasks WHERE TRUE",
		"DELETE FROM leader_leases WHERE TRUE",
		"DELETE FROM idempotency_keys WHERE TRUE",
		"DELETE FROM audit_logs WHERE TRUE",
		"DELETE FROM quotas WHERE TRUE",
		"DELETE FROM builds WHERE TRUE", // declarative creates auto-register tracking builds
		"DELETE FROM zone_health WHERE TRUE",
		// Not hygiene: a leaked done = false EXCLUSIVE operation permanently
		// blocks admission for its resource key through the UNIQUE index, so the
		// next run's verbs on that device would be refused with no live work
		// behind the refusal. Every leaked row also stays in the orphan sweep's
		// scan via open_at.
		"DELETE FROM operations WHERE TRUE",
	} {
		if _, err := raw.PartitionedUpdate(ctx, gspanner.Statement{SQL: stmt}); err != nil {
			return fmt.Errorf("reset %q: %w", stmt, err)
		}
	}
	now := time.Now()
	muts := []*gspanner.Mutation{
		gspanner.InsertMap("quotas", map[string]any{
			"scope":      "platform",
			"scope_id":   "global",
			"max_hosts":  cfg.MaxHosts,
			"used_hosts": int64(0),
			colCreatedAt: now,
			colUpdatedAt: now,
		}),
		// The family the harness provisions from. Its shapes come from the
		// harness discovery in instance.go rather than from here; the catalog
		// holds no shape rows at all any more.
		gspanner.InsertOrUpdateMap("machine_families", map[string]any{
			"family":       harnessMachineFamily,
			"name":         "N1 General Purpose",
			"category":     "general_purpose",
			"architecture": "x86",
			"gpu_mode":     "none",
			colIsEnabled:   true,
			"sort_order":   int64(0),
			colCreatedAt:   now,
			colUpdatedAt:   now,
		}),
		// The default election, which is the one per-shape fact the family row
		// cannot carry.
		gspanner.InsertOrUpdateMap("machine_type_overrides", map[string]any{
			"machine_type": harnessMachineType,
			"is_default":   true,
			colCreatedAt:   now,
			colUpdatedAt:   now,
		}),
		enabledZonesMutation(now),
	}
	if _, err := raw.Apply(ctx, slices.Concat(muts, fleetPolicyMutations(cfg, now))); err != nil {
		return fmt.Errorf("seeding quota and catalog: %w", err)
	}
	return nil
}

// enabledZonesMutation seeds the operator's zone set, which is the placement
// space and the one half of the zone catalog that is durable.
func enabledZonesMutation(now time.Time) *gspanner.Mutation {
	encoded, err := gcecatalog.EnabledZonesJSON([]string{harnessZone, harnessZoneB})
	if err != nil {
		// Encoding a two-element string slice cannot fail; a panic here would
		// be a compile-time impossibility reached at runtime.
		panic(err)
	}
	return gspanner.InsertOrUpdateMap("system_config", map[string]any{
		colConfigKey:   models.ConfigKeyZonesEnabled,
		colConfigValue: encoded,
		colCreatedAt:   now,
		colUpdatedAt:   now,
	})
}

// fleetPolicyMutations seeds the autoscaler policy for whichever scenario asked
// for one, or nothing at all. An absent policy reads as off, which is the fleet
// keeper doing nothing.
//
// All three keys, because each one decides whether a different half of a
// scenario can happen. The standing intent alone provisions nothing: the mode
// is the switch and its absent default is off. The mode is also what decides
// whether the reclamation checkers can fail at all. Under provision_only no
// lease is ever lowered, so a shorten bound asserted against that seeding is
// decoration, which is why both reclaiming scenarios seed `on`.
//
// And demand off, which is what makes the target EQUAL the seeded floor rather
// than merely start there. The target is max-wins across standing intent,
// schedules and demand, and this workload misses placement constantly, so with
// the signal live the target wanders: the census bounds here are all stated
// against the floor, so a wandering target makes the overshoot bound unsound in
// one direction and the drop-to-zero arm unreachable in the other. Zero is the
// horizon's documented "signal off" and its own lower bound, not a value
// smuggled past the validator, and what the signal itself computes is covered
// where seeding it is cheap: the audit-demand and target-composition tests.
//
// The dedicated-host scenario needs the mode and nothing else: a fenced host is
// not inventory, so no target describes it and the collapse answers to none.
// Asked for on its own it therefore seeds a standing intent of ZERO. That is
// the configuration a deployment with the pool off actually runs, and the one
// where the collapse is the only thing the keeper still does.
func fleetPolicyMutations(cfg Cfg, now time.Time) []*gspanner.Mutation {
	if cfg.PoolFloor <= 0 && cfg.FleetFloor <= 0 && !cfg.GroupCollapse {
		return nil
	}
	mode := hostconfig.ModeProvisionOnly
	// The fleet-floor scenario needs `on` for the same reason the reclaim one
	// does: its whole claim is about where reclamation STOPS, and under a mode
	// that may not lower a lease there is no reclamation to stop.
	if cfg.PoolReclaim || cfg.GroupCollapse || cfg.FleetFloor > 0 {
		mode = hostconfig.ModeOn
	}
	settings := [][2]string{
		{models.ConfigKeyFleetPoolBounds, poolBoundsValue(cfg.PoolFloor, cfg.FleetFloor)},
		{models.ConfigKeyFleetAutoscaleMode, string(mode)},
		{models.ConfigKeyFleetDemandHorizonSec, "0"},
	}
	muts := make([]*gspanner.Mutation, 0, len(settings))
	for _, s := range settings {
		muts = append(muts, gspanner.InsertOrUpdateMap("system_config", map[string]any{
			colConfigKey:   s[0],
			colConfigValue: s[1],
			colCreatedAt:   now,
			colUpdatedAt:   now,
		}))
	}
	return muts
}

// poolBoundsValue renders the default pool's two lower bounds the way the
// settings service stores them: one JSON object keyed by pool, which is the only
// shape the keeper reads.
//
// Through the domain encoder and against the domain's key constants, never a
// literal of either. A harness that spells a stored shape itself goes on seeding
// something after the shape moves, and a scenario whose configuration is
// ignored does not fail. It passes, having exercised nothing.
func poolBoundsValue(standby, floor int) string {
	encoded, err := hostconfig.PoolBoundsJSON(map[string]hostconfig.PoolBounds{
		hostconfig.DefaultTier: {Standby: int64(standby), Floor: int64(floor)},
	})
	if err != nil {
		// Encoding two integers under one name cannot fail; a panic here would
		// be a compile-time impossibility reached at runtime.
		panic(err)
	}
	return encoded
}
