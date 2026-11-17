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

package gcecatalogfeature

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// placementMetrics instruments zone ranking. Instruments are no-ops without a
// meter provider, so construction is always safe (tests, harnesses).
type placementMetrics struct {
	quotaRejections   metric.Int64Counter
	spotObtainability metric.Float64Histogram
	spotEmptyRanks    metric.Int64Counter
}

// newPlacementMetrics registers the ranking instruments; with a zone-health
// store it also exposes the active-cooldown count as a gauge (the callback
// runs on the metrics export cycle, one small read per collection).
func newPlacementMetrics(zoneHealth gcecatalog.ZoneHealthStore) *placementMetrics {
	meter := otel.Meter(telemetry.ScopeName("placement"))
	m := &placementMetrics{}
	m.quotaRejections, _ = meter.Int64Counter("cco.placement.quota_rejections",
		metric.WithDescription("Candidate zones filtered out of ranking by regional quota headroom."))
	m.spotObtainability, _ = meter.Float64Histogram("cco.placement.spot_obtainability",
		metric.WithDescription("Advice obtainability of the winning candidate of each Spot ranking. This is the series to correlate the advice signal with outcomes."))
	m.spotEmptyRanks, _ = meter.Int64Counter("cco.placement.spot_rank_empty",
		metric.WithDescription("Spot rankings that produced no viable zone; each one is a standard-capacity fallback (auto) or an outright failure (explicit spot) at the caller."))
	if zoneHealth != nil {
		cooldowns, _ := meter.Int64ObservableGauge("cco.zone_health.cooldowns_active",
			metric.WithDescription("zone_health cells currently inside their cooldown."))
		_, _ = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
			cells, err := zoneHealth.ListCells(ctx)
			if err != nil {
				return nil //nolint:nilerr // metrics are advisory; skip the observation
			}
			now := time.Now()
			var active int64
			for _, c := range cells {
				if c.InCooldown(now) {
					active++
				}
			}
			o.ObserveInt64(cooldowns, active)
			return nil
		}, cooldowns)
	}
	return m
}

// quotaRejected counts one zone filtered out by quota headroom.
func (m *placementMetrics) quotaRejected(ctx context.Context, zone string) {
	if m == nil || m.quotaRejections == nil {
		return
	}
	m.quotaRejections.Add(ctx, 1, metric.WithAttributes(attribute.String(attrZone, zone)))
}

// spotRanked records the winning candidate's advice obtainability for a Spot
// ranking (only when it had fresh advice).
func (m *placementMetrics) spotRanked(ctx context.Context, survivors []*rankedZone, ranked []gcecatalog.ZoneCandidate) {
	if m == nil || m.spotObtainability == nil || len(ranked) == 0 {
		return
	}
	for _, c := range survivors {
		if c.zone.Zone == ranked[0].Zone && c.obtainKnown {
			m.spotObtainability.Record(ctx, c.obtainRaw,
				metric.WithAttributes(attribute.String(attrZone, c.zone.Zone)))
			return
		}
	}
}

// spotRankEmpty counts one Spot ranking that yielded no viable zone.
func (m *placementMetrics) spotRankEmpty(ctx context.Context) {
	if m == nil || m.spotEmptyRanks == nil {
		return
	}
	m.spotEmptyRanks.Add(ctx, 1)
}
