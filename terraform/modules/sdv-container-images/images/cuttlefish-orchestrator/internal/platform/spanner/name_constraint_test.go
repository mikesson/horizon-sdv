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

package spanner_test

import (
	"testing"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// TestNameShapeConstraintsAreLive proves the v57 CHECK constraints reach a
// created database and reject writes.
//
// The create request's `pattern` tag is what normally enforces the name shape,
// but it is per-endpoint: a repository write, a backfill, or a second writer
// inherits nothing from it. These constraints are the last check, and a
// constraint nobody exercises is indistinguishable from one the DDL splitter
// dropped, because the whole schema still applies cleanly without it.
func TestNameShapeConstraintsAreLive(t *testing.T) {
	spannertest.SkipWithoutEmulator(t)

	ctx := t.Context()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()

	insertHost := func(name string) error {
		_, err := db.Client.Apply(ctx, []*spanner.Mutation{
			spanner.InsertOrUpdate("hosts",
				[]string{"host_id", "name", "zone", "status"},
				[]any{"h-1", name, "europe-west4-a", "running"}),
		})
		return err
	}

	for _, name := range []string{"Bad Name", "9leading-digit", "under_score"} {
		if err := insertHost(name); err == nil {
			t.Errorf("hosts.name %q was admitted; chk_hosts_name_shape is not enforcing", name)
		}
	}
	// The label a real caller sends, and the derived form pre-v57 rows carry.
	for _, name := range []string{"my-host", "host-x7k2q", "cf-legacy-a1b2c3d4"} {
		if err := insertHost(name); err != nil {
			t.Errorf("hosts.name %q rejected: %v", name, err)
		}
	}

	// devices is interleaved, so the parent row above must exist first.
	insertDevice := func(name string) error {
		_, err := db.Client.Apply(ctx, []*spanner.Mutation{
			spanner.InsertOrUpdate("devices",
				[]string{"host_id", "device_id", "name", "status"},
				[]any{"h-1", "d-1", name, "pending"}),
		})
		return err
	}

	if err := insertDevice("Bad Device"); err == nil {
		t.Error("devices.name \"Bad Device\" was admitted; chk_devices_name_shape is not enforcing")
	}
	if err := insertDevice("my-phone"); err != nil {
		t.Errorf("devices.name %q rejected: %v", "my-phone", err)
	}
}
