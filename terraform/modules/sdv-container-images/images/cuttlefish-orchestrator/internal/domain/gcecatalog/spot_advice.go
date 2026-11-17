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

package gcecatalog

import (
	"context"
	"time"
)

// Estimated-uptime tiers the advice.capacity API discretizes to, in seconds.
const (
	SpotUptimeTierMinute = 60
	SpotUptimeTier10Min  = 600
	SpotUptimeTierHour   = 3600
)

// SpotAdvice is one synced cell of Spot capacity advice for a
// (zone, machine type) pair: the obtainability score and estimated-uptime
// tier the GCE advice.capacity API reports for a single-VM Spot request.
// It is advisory by contract. Scores guarantee nothing, and ranking degrades
// to its quota/health signals when a cell is absent or stale.
type SpotAdvice struct {
	Zone        string `json:"zone"`
	MachineType string `json:"machine_type"`

	// Obtainability is the likelihood (0.0-1.0, higher better) that a
	// single-VM Spot create for this cell succeeds. GCE's documented bands:
	// 0.7+ high, 0.4-0.6 medium, 0.3 and below low.
	Obtainability float64 `json:"obtainability"`

	// UptimeTierSeconds is the estimated minimum run time before preemption,
	// discretized by the API to 60, 600, or 3600 seconds.
	UptimeTierSeconds int64 `json:"uptime_tier_s"`

	SyncedAt time.Time `json:"synced_at"`
}

// SpotAdviceStore persists synced Spot capacity advice. The Spanner
// SpotAdviceRepository satisfies it.
type SpotAdviceStore interface {
	// UpsertCells replaces the given cells (keyed by zone + machine type),
	// stamping their sync time.
	UpsertCells(ctx context.Context, cells []*SpotAdvice) error

	// ListCells returns every synced advice cell. Small by construction: the
	// probed set is enabled zones × enabled machine types, and quiet rows age
	// out under the table's 1-day deletion policy.
	ListCells(ctx context.Context) ([]*SpotAdvice, error)
}
