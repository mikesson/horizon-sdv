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

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
)

// ZoneHealthRow represents the Spanner table schema for zone_health cells.
type ZoneHealthRow struct {
	Zone                string             `spanner:"zone"`
	MachineFamily       string             `spanner:"machine_family"`
	BindingMetric       string             `spanner:"binding_metric"`
	ConsecutiveFailures int64              `spanner:"consecutive_failures"`
	LastFailureAt       spanner.NullTime   `spanner:"last_failure_at"`
	LastReason          spanner.NullString `spanner:"last_reason"`
	CooldownUntil       spanner.NullTime   `spanner:"cooldown_until"`
	UpdatedAt           time.Time          `spanner:"updated_at"`
}

// ToDomain converts a ZoneHealthRow to the domain cell.
func (r *ZoneHealthRow) ToDomain() *gcecatalog.ZoneHealth {
	return &gcecatalog.ZoneHealth{
		Zone:                r.Zone,
		MachineFamily:       r.MachineFamily,
		BindingMetric:       r.BindingMetric,
		ConsecutiveFailures: r.ConsecutiveFailures,
		LastFailureAt:       FromNullTimePtr(r.LastFailureAt),
		LastReason:          FromNullString(r.LastReason),
		CooldownUntil:       FromNullTimePtr(r.CooldownUntil),
		UpdatedAt:           r.UpdatedAt,
	}
}
