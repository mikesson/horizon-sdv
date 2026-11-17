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

// ZoneHealth is one cell of the placement plane's reactive memory: classified
// capacity failures for a (zone, machine family) pair, with an exponential
// cooldown that steers ranking away from the cell until it passes. Written by
// the provision failure path; read by zone ranking; cleared by the next
// successful provision in the cell.
type ZoneHealth struct {
	// Zone is the GCE zone the failure occurred in.
	Zone string `json:"zone"`

	// MachineFamily is the machine type prefix (e.g. "n1") the failing spec
	// used, so a cooldown never blocks specs the failure says nothing about.
	MachineFamily string `json:"machine_family"`

	// BindingMetric names the quota/capacity axis that failed (a ZoneHealth
	// metric constant). Quota metrics are regional: at read time they cool
	// every zone in the cell's region for the family, and only for specs that
	// actually demand the metric. A GPU-quota cooldown must not block a
	// CPU-only spec's placement, and a cooldown on one disk pool says nothing
	// about a shape that boots from the other. (Instance quota counts as
	// demanded by every spec: each host is one instance slot.)
	BindingMetric string `json:"binding_metric"`

	// ConsecutiveFailures counts failures since the cell was last cleared.
	ConsecutiveFailures int64 `json:"consecutive_failures"`

	LastFailureAt *time.Time `json:"last_failure_at,omitempty"`
	LastReason    string     `json:"last_reason,omitempty"`

	// CooldownUntil is when the cell stops repelling placement.
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`

	UpdatedAt time.Time `json:"updated_at"`
}

// InCooldown reports whether the cell still repels placement at now.
func (zh *ZoneHealth) InCooldown(now time.Time) bool {
	return zh.CooldownUntil != nil && now.Before(*zh.CooldownUntil)
}

// Cooldown pacing for zone_health cells: the first failure cools its cell for
// cooldownBase, each further consecutive failure doubles it, capped at
// cooldownCap so a chronically constrained zone re-enters ranking within the
// hour and gets re-probed rather than banned.
const (
	cooldownBase = 5 * time.Minute
	cooldownCap  = time.Hour
)

// NextCooldownUntil returns when a cell stops repelling placement after a
// failure that follows prevFailures consecutive ones: base × 2^prevFailures,
// capped. The escalation schedule is placement policy and lives here with the
// cell it governs; the store persists the computed value.
func NextCooldownUntil(prevFailures int64, now time.Time) time.Time {
	cooldown := cooldownBase
	for range prevFailures {
		cooldown *= 2
		if cooldown >= cooldownCap {
			return now.Add(cooldownCap)
		}
	}
	return now.Add(min(cooldown, cooldownCap))
}

// Binding-metric vocabulary for ZoneHealth cells. The zonal metrics cool only
// their own cell; the regional (quota) metrics cool every zone in the region,
// because GCE quota is accounted per region.
const (
	// ZoneMetricCapacity: zonal stockout (zoneResourcePoolExhausted).
	ZoneMetricCapacity = "capacity"
	// ZoneMetricAvailability: the zone/type/GPU combination is not offered
	// (zone down or not found, machine type or accelerator absent).
	ZoneMetricAvailability = "availability"

	// ZoneMetricPreemption: a zone-wide Spot reclaim storm (the host reconciler's recovery
	// breaker tripping). Zonal, but Spot-scoped at read time: it repels only
	// Spot specs, because a reclaim storm says nothing about on-demand
	// capacity in the zone.
	ZoneMetricPreemption = "preemption"

	// One CPU axis covers every CPU pool, including the per-family ones: the
	// cell is already keyed by machine family, so a name carrying the family a
	// second time would only let the two disagree.
	ZoneMetricQuotaCPU = "quota_cpu"
	ZoneMetricQuotaGPU = "quota_gpu"
	ZoneMetricQuotaIP  = "quota_ip"
	// The two disk axes are separate pools an operator raises separately
	// (pd-* boot disks fill SSD_TOTAL_GB, hyperdisk-balanced ones
	// HDB_TOTAL_GB), so naming a hyperdisk exhaustion "SSD" would send them
	// after the wrong increase.
	ZoneMetricQuotaSSD       = "quota_ssd"
	ZoneMetricQuotaHyperdisk = "quota_hyperdisk"
	ZoneMetricQuotaInstance  = "quota_instances"
)

// IsRegionalMetric reports whether a binding metric cools the whole region
// (quota classes) rather than just its own zone.
func IsRegionalMetric(metric string) bool {
	switch metric {
	case ZoneMetricQuotaCPU, ZoneMetricQuotaGPU, ZoneMetricQuotaIP,
		ZoneMetricQuotaSSD, ZoneMetricQuotaHyperdisk, ZoneMetricQuotaInstance:
		return true
	}
	return false
}

// ZoneHealthStore persists the reactive placement memory. The Spanner
// ZoneHealthRepository satisfies it.
type ZoneHealthStore interface {
	// RecordFailure counts one classified capacity failure against the
	// (zone, machineFamily) cell, replacing its binding metric and reason and
	// extending its cooldown exponentially. It returns the updated cell.
	RecordFailure(ctx context.Context, zone, machineFamily, bindingMetric, reason string) (*ZoneHealth, error)

	// ClearCell removes the (zone, machineFamily) cell. A provision succeeded
	// there, so the memory no longer reflects reality. A missing cell is a
	// no-op.
	ClearCell(ctx context.Context, zone, machineFamily string) error

	// ListCells returns every cell. Ranking evaluates cooldown filtering,
	// regional fan-out, and score decay for lapsed-but-recent failures over
	// this set; it is small by construction (only recently-failing cells
	// exist, and quiet ones age out under the table's deletion policy).
	ListCells(ctx context.Context) ([]*ZoneHealth, error)

	// ListCapacityCells returns the stockout observations recorded for these
	// machine families, in ONE statement whatever the family count.
	//
	// Narrower than ListCells on both axes because it answers a different
	// question. ListCells asks which cells should steer ranking; this asks
	// which shapes there is no point probing. A shape ladder asks that of every
	// rung at once, and a read per rung is the shape this exists to make
	// unrepresentable.
	ListCapacityCells(ctx context.Context, machineFamilies []string) ([]*ZoneHealth, error)
}

// FreshCapacityCells are the stockout observations that still stand at now: a
// capacity refusal whose own cooldown has not lapsed.
//
// The single definition of a standing stockout. The memory decays on the
// schedule the placement plane already applies and nothing holds a second one,
// so a surface dating these observations and a walk skipping a shape because of
// them can never disagree about which ones count.
func FreshCapacityCells(cells []*ZoneHealth, now time.Time) []*ZoneHealth {
	fresh := make([]*ZoneHealth, 0, len(cells))
	for _, cell := range cells {
		if cell.BindingMetric == ZoneMetricCapacity && cell.InCooldown(now) {
			fresh = append(fresh, cell)
		}
	}
	return fresh
}

// StockedOutZones folds capacity cells into the answer a provisioning walk
// asks of them: per machine family, the zones a fresh stockout observation
// still stands in.
//
// A stockout that has decayed leaves the map, and the shape it belonged to is
// preferred again, which is what stops a transient stockout from demoting a
// fleet for a day.
func StockedOutZones(cells []*ZoneHealth, now time.Time) map[string]map[string]bool {
	stocked := make(map[string]map[string]bool, len(cells))
	for _, cell := range FreshCapacityCells(cells, now) {
		zones := stocked[cell.MachineFamily]
		if zones == nil {
			zones = make(map[string]bool, 4)
			stocked[cell.MachineFamily] = zones
		}
		zones[cell.Zone] = true
	}
	return stocked
}
