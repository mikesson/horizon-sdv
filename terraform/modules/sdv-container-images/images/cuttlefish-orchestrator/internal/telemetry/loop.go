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

package telemetry

import (
	"context"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// LoopMetrics instruments one background reconcile loop with the two signals
// its own health is judged by: a per-cycle duration histogram and a
// last-success-age gauge. The age grows without bound while the loop is
// stuck, crashed, or blocked on a dependency, and action counters alone cannot
// distinguish "quiet" from "dead".
type LoopMetrics struct {
	duration    metric.Float64Histogram
	attrs       []attribute.KeyValue
	lastSuccess atomic.Int64 // unix nanos of the last successful cycle; 0 = never
}

// NewLoopMetrics registers cco.<component>.cycle_duration and
// cco.<component>.last_success_age. Instruments are no-ops without a meter
// provider, so this is always safe (e.g. in tests). Optional attrs distinguish
// sibling loops sharing one component (e.g. the catalog sync's per-source loops,
// or one elector per lease) without multiplying metric names.
func NewLoopMetrics(component string, attrs ...attribute.KeyValue) *LoopMetrics {
	lm := &LoopMetrics{attrs: attrs}
	meter := otel.Meter(ScopeName(component))
	lm.duration, _ = meter.Float64Histogram("cco."+component+".cycle_duration",
		metric.WithUnit("s"), metric.WithDescription("Reconcile cycle wall time."))
	age, _ := meter.Float64ObservableGauge("cco."+component+".last_success_age",
		metric.WithUnit("s"),
		metric.WithDescription("Seconds since the last fully successful cycle; grows while the loop is stuck."))
	_, _ = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		if ns := lm.lastSuccess.Load(); ns > 0 {
			o.ObserveFloat64(age, time.Since(time.Unix(0, ns)).Seconds(),
				metric.WithAttributes(lm.attrs...))
		}
		return nil
	}, age)
	return lm
}

// RecordCycle records one cycle's duration and outcome. Only a fully
// successful cycle resets the staleness age.
func (l *LoopMetrics) RecordCycle(ctx context.Context, start time.Time, success bool) {
	attrs := make([]attribute.KeyValue, 0, len(l.attrs)+1)
	attrs = append(attrs, attribute.Bool("success", success))
	attrs = append(attrs, l.attrs...)
	l.duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
	if success {
		l.lastSuccess.Store(time.Now().UnixNano())
	}
}

// CycleBudget returns the per-cycle timeout for a loop that ticks at
// interval: finish before the next tick, but never start with an
// already-expired context when the interval is very short. A non-positive
// interval returns 0 (no timeout).
func CycleBudget(interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	return max(interval-time.Second, interval/2)
}
