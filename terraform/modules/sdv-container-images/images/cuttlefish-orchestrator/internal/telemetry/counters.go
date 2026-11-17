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

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ObservedCounter mirrors one atomic value as an observable instrument: a
// monotonic total by default, or a level when Gauge is set. Entries sharing a
// Name share one instrument and are distinguished by Attrs (e.g. one counter
// broken down by class).
type ObservedCounter struct {
	Name        string
	Description string
	Value       *atomic.Int64
	Gauge       bool
	Attrs       []attribute.KeyValue
}

// RegisterCounters registers a job's atomic counters as observable instruments
// under a single callback on the component's meter. This is the shared form of
// the per-job atomic-to-observable blocks. The first entry for a Name decides
// the instrument's kind and description. Instruments are no-ops without a meter
// provider, so this is always safe (e.g. in tests).
func RegisterCounters(component string, counters []ObservedCounter) {
	meter := otel.Meter(ScopeName(component))
	instruments := make(map[string]metric.Int64Observable, len(counters))
	observables := make([]metric.Observable, 0, len(counters))
	for _, c := range counters {
		if _, ok := instruments[c.Name]; ok {
			continue
		}
		var (
			inst metric.Int64Observable
			err  error
		)
		if c.Gauge {
			inst, err = meter.Int64ObservableGauge(c.Name, metric.WithDescription(c.Description))
		} else {
			inst, err = meter.Int64ObservableCounter(c.Name, metric.WithDescription(c.Description))
		}
		_ = err // no-op instrument on failure, matching the hand-rolled blocks
		instruments[c.Name] = inst
		observables = append(observables, inst)
	}
	_, _ = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		for _, c := range counters {
			o.ObserveInt64(instruments[c.Name], c.Value.Load(), metric.WithAttributes(c.Attrs...))
		}
		return nil
	}, observables...)
}
