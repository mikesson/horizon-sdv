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

package taskworker

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// StatsProvider exposes queue depth for metrics; the Spanner TaskQueue satisfies it.
type StatsProvider interface {
	Stats(ctx context.Context) (task.QueueStats, error)
}

var _ StatsProvider = (*spanner.TaskQueue)(nil)

// RegisterQueueMetrics registers observable gauges for queue depth (backlog,
// dead-letters, oldest-pending age, per-shard hotspot) and a goroutine-count
// canary. The callback reads the queue at each collection with a low-priority
// query. Call once at startup. A nil logger falls back to slog.Default.
func RegisterQueueMetrics(provider StatsProvider, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	meter := otel.Meter(telemetry.ScopeName("taskqueue"))
	pending, _ := meter.Int64ObservableGauge("cco.tasks.pending",
		metric.WithDescription("Tasks awaiting a worker."))
	leased, _ := meter.Int64ObservableGauge("cco.tasks.leased",
		metric.WithDescription("Tasks currently held by a worker."))
	deadLettered, _ := meter.Int64ObservableGauge("cco.tasks.dlq_depth",
		metric.WithDescription("Dead-lettered (failed) tasks awaiting operator action."))
	oldestAge, _ := meter.Float64ObservableGauge("cco.tasks.oldest_pending_age",
		metric.WithUnit("s"), metric.WithDescription("Age of the oldest pending task; rising means workers can't keep up."))
	pendingByShard, _ := meter.Int64ObservableGauge("cco.tasks.pending_by_shard",
		metric.WithDescription("Pending tasks per shard; a hot shard signals a hotspot."))
	goroutines, _ := meter.Int64ObservableGauge("cco.runtime.goroutines",
		metric.WithDescription("Live goroutines; a steady climb signals a leak."))

	_, err := meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		o.ObserveInt64(goroutines, int64(runtime.NumGoroutine()))
		stats, statsErr := provider.Stats(ctx)
		if statsErr != nil {
			// Skip this cycle's queue gauges rather than returning the error:
			// a callback error makes the reader drop the ENTIRE collection,
			// so one failed stats query would blank every metric family.
			logger.Warn("queue stats unavailable; skipping queue gauges",
				slog.Any("error", statsErr))
			return nil
		}
		o.ObserveInt64(pending, stats.Pending)
		o.ObserveInt64(leased, stats.Leased)
		o.ObserveInt64(deadLettered, stats.Failed)
		o.ObserveFloat64(oldestAge, stats.OldestPendingAge.Seconds())
		for shard, n := range stats.PendingByShard {
			o.ObserveInt64(pendingByShard, n, metric.WithAttributes(attribute.Int("shard", shard)))
		}
		return nil
	}, pending, leased, deadLettered, oldestAge, pendingByShard, goroutines)
	if err != nil {
		return fmt.Errorf("registering queue metrics callback: %w", err)
	}
	return nil
}
