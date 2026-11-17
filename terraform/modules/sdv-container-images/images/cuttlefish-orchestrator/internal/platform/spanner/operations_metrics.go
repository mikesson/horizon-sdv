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
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"cloud.google.com/go/spanner"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// opMetrics counts the operation events that are otherwise invisible: a race the
// terminal CAS prevented, and a verb refused because one was already running.
// Both are decisions this layer makes silently, and the last time this codebase
// had an in-flight-operation failure mode it could not see, the fix was a
// counter.
type opMetrics struct {
	terminalRejected metric.Int64Counter
	admissionRefused metric.Int64Counter
}

// operationMetrics lazily builds the shared counters on first use, which in
// production is after main has installed the meter provider. Instruments are
// no-ops without a provider, so tests need no setup.
var operationMetrics = sync.OnceValue(func() *opMetrics {
	meter := otel.Meter(telemetry.ScopeName("operations"))
	m := &opMetrics{}
	m.terminalRejected, _ = meter.Int64Counter("cco.operations.terminal_write_rejected",
		metric.WithDescription("Terminal operation writes refused by the CAS because another delivery already settled the operation. Non-zero means at-least-once redelivery is happening and the CAS is doing its job."))
	m.admissionRefused, _ = meter.Int64Counter("cco.operations.admission_refused",
		metric.WithDescription("Verbs refused because an exclusive operation was already live on the resource. A high count means users are told to try later often enough that queueing deserves revisiting."))
	return m
})

func (m *opMetrics) terminalWriteRejected(ctx context.Context) {
	m.terminalRejected.Add(ctx, 1)
}

func (m *opMetrics) refused(ctx context.Context, verb operation.Verb) {
	m.admissionRefused.Add(ctx, 1, metric.WithAttributes(attribute.String("verb", string(verb))))
}

// OpenOperationsProvider exposes the in-flight working set for the gauges below.
type OpenOperationsProvider interface {
	OpenStats(ctx context.Context) (depth int64, oldest time.Duration, err error)
}

var _ OpenOperationsProvider = (*OperationsRepository)(nil)

// OpenStats returns the in-flight operation count and the age of the oldest,
// both from idx_operations_open, a null-filtered index over live rows in age
// order, so depth is a count and oldest age is the first row.
func (r *OperationsRepository) OpenStats(ctx context.Context) (int64, time.Duration, error) {
	stmt := spanner.Statement{SQL: `@{spanner_emulator.disable_query_null_filtered_index_check=true}
	      SELECT COUNT(*) AS n, MIN(open_at) AS oldest
	      FROM operations@{FORCE_INDEX=idx_operations_open}
	      WHERE open_at IS NOT NULL`}
	iter := r.client.StaleSingle().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if err != nil {
		return 0, 0, fmt.Errorf("reading open operation stats: %w", err)
	}
	var (
		n      int64
		oldest spanner.NullTime
	)
	if err := row.Columns(&n, &oldest); err != nil {
		return 0, 0, fmt.Errorf("scanning open operation stats: %w", err)
	}
	var age time.Duration
	if oldest.Valid {
		age = timeNow().Sub(oldest.Time)
	}
	return n, age, nil
}

// RegisterOperationMetrics registers observable gauges for the in-flight
// operation working set, the direct analog of queue depth and the signal
// that says whether this whole arc is healthy at all. The callback runs one
// low-priority query per collection. Call once at startup.
func RegisterOperationMetrics(provider OpenOperationsProvider, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	meter := otel.Meter(telemetry.ScopeName("operations"))
	open, _ := meter.Int64ObservableGauge("cco.operations.open",
		metric.WithDescription("Operations in flight."))
	oldestAge, _ := meter.Float64ObservableGauge("cco.operations.oldest_open_age",
		metric.WithUnit("s"),
		metric.WithDescription("Age of the oldest in-flight operation; rising past a verb's runtime means one is stalled."))

	_, err := meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		depth, age, statsErr := provider.OpenStats(ctx)
		if statsErr != nil {
			// Skip this cycle rather than returning the error: a callback error
			// makes the reader drop the ENTIRE collection, so one failed query
			// would blank every metric family.
			logger.Warn("open operation stats unavailable; skipping gauges", slog.Any("error", statsErr))
			return nil
		}
		o.ObserveInt64(open, depth)
		o.ObserveFloat64(oldestAge, age.Seconds())
		return nil
	}, open, oldestAge)
	if err != nil {
		return fmt.Errorf("registering operation metrics: %w", err)
	}
	return nil
}
