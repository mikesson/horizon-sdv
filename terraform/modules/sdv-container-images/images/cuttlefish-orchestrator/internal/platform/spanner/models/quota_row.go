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

package models

import (
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/quota"
)

// QuotaRow represents the Spanner table schema for quotas.
type QuotaRow struct {
	Scope              string            `spanner:"scope"`
	ScopeID            string            `spanner:"scope_id"`
	MaxHosts           int64             `spanner:"max_hosts"`
	UsedHosts          int64             `spanner:"used_hosts"`
	HighWaterUsedHosts spanner.NullInt64 `spanner:"high_water_used_hosts"`
	HighWaterAt        spanner.NullTime  `spanner:"high_water_at"`
	CreatedAt          time.Time         `spanner:"created_at"`
	UpdatedAt          time.Time         `spanner:"updated_at"`
}

// ToDomain converts a QuotaRow to a domain Quota.
func (r *QuotaRow) ToDomain() *quota.Quota {
	q := &quota.Quota{
		Scope:              quota.Scope(r.Scope),
		ScopeID:            r.ScopeID,
		MaxHosts:           r.MaxHosts,
		UsedHosts:          r.UsedHosts,
		HighWaterUsedHosts: r.HighWaterUsedHosts.Int64,
		CreatedAt:          r.CreatedAt,
		UpdatedAt:          r.UpdatedAt,
	}
	if r.HighWaterAt.Valid {
		q.HighWaterAt = &r.HighWaterAt.Time
	}
	return q
}
