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
	"fmt"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// SeedHost creates a test host and returns its ID.
func (db *TestDB) SeedHost(ctx context.Context, id, name, zone string) error {
	now := time.Now()
	_, err := db.Client.Apply(ctx, []*spanner.Mutation{
		spanner.InsertMap("hosts", map[string]any{
			"host_id":                    id,
			"name":                       name,
			"zone":                       zone,
			"status":                     "running",
			"gpu_count":                  int64(1),
			"host_orchestrator_port":     int64(2080),
			"max_devices":                int64(4),
			"current_device_count":       int64(0),
			spannerplatform.ColCreatedAt: now,
			spannerplatform.ColUpdatedAt: now,
		}),
	})
	if err != nil {
		return fmt.Errorf("seeding host: %w", err)
	}
	return nil
}

// SeedQuota inserts the platform/global quota row with the given limits.
func (db *TestDB) SeedQuota(ctx context.Context, maxHosts, usedHosts int64) error {
	now := time.Now()
	_, err := db.Client.Apply(ctx, []*spanner.Mutation{
		spanner.InsertMap("quotas", map[string]any{
			"scope":                      "platform",
			"scope_id":                   "global",
			"max_hosts":                  maxHosts,
			"used_hosts":                 usedHosts,
			spannerplatform.ColCreatedAt: now,
			spannerplatform.ColUpdatedAt: now,
		}),
	})
	if err != nil {
		return fmt.Errorf("seeding quota: %w", err)
	}
	return nil
}

// ReadQuota reads the platform/global quota row (the one SeedQuota creates),
// so tests outside the quota feature can assert on counter and watermark
// state without constructing the quota repository.
func (db *TestDB) ReadQuota(ctx context.Context) (*quota.Quota, error) {
	row, err := db.Client.Single().ReadRow(ctx, "quotas",
		spanner.Key{string(quota.ScopePlatform), quota.GlobalScopeID},
		[]string{"scope", "scope_id", "max_hosts", "used_hosts", "high_water_used_hosts", "high_water_at", spannerplatform.ColCreatedAt, spannerplatform.ColUpdatedAt},
	)
	if err != nil {
		return nil, fmt.Errorf("reading quota: %w", err)
	}
	var qRow models.QuotaRow
	if err := row.ToStruct(&qRow); err != nil {
		return nil, fmt.Errorf("scanning quota: %w", err)
	}
	return qRow.ToDomain(), nil
}
