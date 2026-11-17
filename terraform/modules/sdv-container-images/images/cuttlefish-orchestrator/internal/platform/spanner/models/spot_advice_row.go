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

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
)

// SpotAdviceRow represents the Spanner table schema for spot_advice cells.
type SpotAdviceRow struct {
	Zone          string    `spanner:"zone"`
	MachineType   string    `spanner:"machine_type"`
	Obtainability float64   `spanner:"obtainability"`
	UptimeTierS   int64     `spanner:"uptime_tier_s"`
	SyncedAt      time.Time `spanner:"synced_at"`
}

// ToDomain converts a SpotAdviceRow to the domain cell.
func (r *SpotAdviceRow) ToDomain() *gcecatalog.SpotAdvice {
	return &gcecatalog.SpotAdvice{
		Zone:              r.Zone,
		MachineType:       r.MachineType,
		Obtainability:     r.Obtainability,
		UptimeTierSeconds: r.UptimeTierS,
		SyncedAt:          r.SyncedAt,
	}
}
