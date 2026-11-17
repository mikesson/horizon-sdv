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
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// withManualReader swaps the global meter provider for one backed by a manual
// reader, restoring the previous provider on cleanup. It is not parallel-safe
// by construction, so callers must not use t.Parallel.
func withManualReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(prev) })
	return reader
}

// findMetric locates one metric by name across all scopes.
func findMetric(t *testing.T, rm *metricdata.ResourceMetrics, name string) metricdata.Metrics {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m
			}
		}
	}
	t.Fatalf("metric %q not found", name)
	return metricdata.Metrics{}
}

func TestRegisterCountersObservesCountersGaugesAndAttrs(t *testing.T) {
	reader := withManualReader(t)

	var total, level, classA, classB atomic.Int64
	total.Store(7)
	level.Store(3)
	classA.Store(1)
	classB.Store(2)
	RegisterCounters("countertest", []ObservedCounter{
		{Name: "cco.countertest.total", Description: "A total.", Value: &total},
		{Name: "cco.countertest.level", Description: "A level.", Value: &level, Gauge: true},
		{
			Name: "cco.countertest.classified", Description: "By class.", Value: &classA,
			Attrs: []attribute.KeyValue{attribute.String("class", "a")},
		},
		{
			Name: "cco.countertest.classified", Description: "By class.", Value: &classB,
			Attrs: []attribute.KeyValue{attribute.String("class", "b")},
		},
	})

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}

	sum, ok := findMetric(t, &rm, "cco.countertest.total").Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatal("total is not a Sum")
	}
	if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 7 {
		t.Errorf("total datapoints = %+v, want one point of 7", sum.DataPoints)
	}

	gauge, ok := findMetric(t, &rm, "cco.countertest.level").Data.(metricdata.Gauge[int64])
	if !ok {
		t.Fatal("level is not a Gauge")
	}
	if len(gauge.DataPoints) != 1 || gauge.DataPoints[0].Value != 3 {
		t.Errorf("level datapoints = %+v, want one point of 3", gauge.DataPoints)
	}

	classified, ok := findMetric(t, &rm, "cco.countertest.classified").Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatal("classified is not a Sum")
	}
	byClass := map[string]int64{}
	for _, dp := range classified.DataPoints {
		if v, found := dp.Attributes.Value("class"); found {
			byClass[v.AsString()] = dp.Value
		}
	}
	if byClass["a"] != 1 || byClass["b"] != 2 {
		t.Errorf("classified by class = %v, want a=1 b=2", byClass)
	}
}

func TestLoopMetricsAttrsDistinguishSiblingLoops(t *testing.T) {
	reader := withManualReader(t)

	a := NewLoopMetrics("looptest", attribute.String("loop", "a"))
	b := NewLoopMetrics("looptest", attribute.String("loop", "b"))
	a.RecordCycle(t.Context(), time.Now(), true)
	b.RecordCycle(t.Context(), time.Now(), false)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	hist, ok := findMetric(t, &rm, "cco.looptest.cycle_duration").Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatal("cycle_duration is not a Histogram")
	}
	seen := map[string]bool{}
	for _, dp := range hist.DataPoints {
		if v, found := dp.Attributes.Value("loop"); found {
			seen[v.AsString()] = true
		}
	}
	if !seen["a"] || !seen["b"] {
		t.Errorf("histogram series by loop attr = %v, want both a and b", seen)
	}

	// Only the successful loop resets its staleness age, and the observation
	// must carry the loop attr.
	var rm2 metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &rm2); err != nil {
		t.Fatal(err)
	}
	age, ok := findMetric(t, &rm2, "cco.looptest.last_success_age").Data.(metricdata.Gauge[float64])
	if !ok {
		t.Fatal("last_success_age is not a Gauge")
	}
	if len(age.DataPoints) != 1 {
		t.Fatalf("age datapoints = %d, want 1 (only the successful loop)", len(age.DataPoints))
	}
	if v, found := age.DataPoints[0].Attributes.Value("loop"); !found || v.AsString() != "a" {
		t.Errorf("age datapoint attrs = %v, want loop=a", age.DataPoints[0].Attributes)
	}
}
