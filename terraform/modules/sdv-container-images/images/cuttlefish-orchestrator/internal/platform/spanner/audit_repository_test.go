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
	"strings"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
)

func TestBuildAuditFilter(t *testing.T) {
	t.Parallel()
	actorEmail := "user@example.com"
	action := audit.Action("create")
	resourceType := audit.ResourceType("device")
	resourceID := "dev-123"
	kind := "system_config"
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name           string
		filter         audit.ListFilter
		wantPredicates []string
		wantParams     map[string]any
	}{
		{
			name:           "empty filter",
			filter:         audit.ListFilter{},
			wantPredicates: nil,
			wantParams:     map[string]any{},
		},
		{
			name: "all predicates",
			filter: audit.ListFilter{
				ActorEmail:   &actorEmail,
				Action:       &action,
				ResourceType: &resourceType,
				ResourceID:   &resourceID,
				Kind:         &kind,
				StartTime:    &start,
				EndTime:      &end,
			},
			wantPredicates: []string{
				`actor_email = @actor_email`,
				`action = @action`,
				`resource_type = @resource_type`,
				`resource_id = @resource_id`,
				`JSON_VALUE(metadata, '$.kind') = @kind`,
				`timestamp >= @start_time`,
				`timestamp <= @end_time`,
			},
			wantParams: map[string]any{
				"actor_email":   actorEmail,
				"action":        string(action),
				"resource_type": string(resourceType),
				"resource_id":   resourceID,
				"kind":          kind,
				"start_time":    start,
				"end_time":      end,
			},
		},
		{
			// The pagination-total bug: Count used to drop the resource_id
			// predicate, disagreeing with Query's rows.
			name: "resource id only",
			filter: audit.ListFilter{
				ResourceID: &resourceID,
			},
			wantPredicates: []string{`resource_id = @resource_id`},
			wantParams:     map[string]any{"resource_id": resourceID},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			whereClause, params := buildAuditFilter(tt.filter)

			if !strings.HasPrefix(whereClause, ` WHERE 1=1`) {
				t.Errorf("whereClause = %q, want ` WHERE 1=1` prefix", whereClause)
			}
			for _, p := range tt.wantPredicates {
				if !strings.Contains(whereClause, p) {
					t.Errorf("whereClause = %q, missing predicate %q", whereClause, p)
				}
			}
			if got, want := strings.Count(whereClause, " AND "), len(tt.wantPredicates); got != want {
				t.Errorf("whereClause = %q has %d predicates, want %d", whereClause, got, want)
			}

			if len(params) != len(tt.wantParams) {
				t.Errorf("params = %v, want %v", params, tt.wantParams)
			}
			for k, want := range tt.wantParams {
				if got, ok := params[k]; !ok || got != want {
					t.Errorf("params[%q] = %v (present=%t), want %v", k, got, ok, want)
				}
			}
		})
	}
}
