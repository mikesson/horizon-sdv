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

	"github.com/google/uuid"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// The kind predicate runs JSON_VALUE over the metadata column, the one
// filter a SQL-builder assertion cannot vouch for, so it is proven against
// the emulator: only the entry stamped kind=system_config may come back.
func TestAuditQueryFiltersByMetadataKind(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := cspanner.NewAuditRepository(db.Client)

	entry := func(kind string) audit.Entry {
		e := audit.New("admin.config", "system").WithActor("op@example.com").Build()
		e.ID = uuid.New().String()
		if kind != "" {
			e.Metadata = map[string]any{"kind": kind, "value": "x"}
		}
		return e
	}
	settings := entry("system_config")
	if err := repo.WriteBatch(ctx, []audit.Entry{settings, entry("machine_type"), entry("")}); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	kind := "system_config"
	got, err := repo.Query(ctx, audit.ListFilter{Kind: &kind, Limit: 10})
	if err != nil {
		t.Fatalf("Query(kind): %v", err)
	}
	if len(got) != 1 || got[0].ID != settings.ID {
		t.Fatalf("Query(kind) = %d entries, want exactly the system_config one", len(got))
	}

	count, err := repo.Count(ctx, audit.ListFilter{Kind: &kind})
	if err != nil {
		t.Fatalf("Count(kind): %v", err)
	}
	if count != 1 {
		t.Errorf("Count(kind) = %d, want 1 (totals must agree with the rows)", count)
	}
}
