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
	"errors"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// highTraceID is above any small TraceIDRatioBased threshold, so a ratio
// sampler deterministically drops it.
func highTraceID(t *testing.T) trace.TraceID {
	t.Helper()
	id, err := trace.TraceIDFromHex("ffffffffffffffffffffffffffffffff")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRecordErrorSetsErrorStatusAndEvent(t *testing.T) {
	t.Parallel()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	_, span := tp.Tracer("test").Start(t.Context(), "op")

	RecordError(span, errors.New("boom"))
	span.End()

	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(ended))
	}
	got := ended[0]
	// Status must be Error (Cloud Trace error views key off it). An exception
	// event on its own is not enough.
	if got.Status().Code != codes.Error {
		t.Errorf("status code = %v, want Error", got.Status().Code)
	}
	if got.Status().Description != "boom" {
		t.Errorf("status description = %q, want %q", got.Status().Description, "boom")
	}
	var hasException bool
	for _, e := range got.Events() {
		if e.Name == "exception" {
			hasException = true
		}
	}
	if !hasException {
		t.Error("expected an exception event to also be recorded")
	}
}

func TestSamplerAlwaysSamplesConsumers(t *testing.T) {
	t.Parallel()
	s := samplerFor(0.0001)

	consumer := sdktrace.SamplingParameters{
		ParentContext: t.Context(),
		TraceID:       highTraceID(t),
		Name:          "device.create_cvd",
		Kind:          trace.SpanKindConsumer,
	}
	if got := s.ShouldSample(consumer).Decision; got != sdktrace.RecordAndSample {
		t.Errorf("consumer span at low ratio: got %v, want RecordAndSample", got)
	}

	server := consumer
	server.Kind = trace.SpanKindServer
	if got := s.ShouldSample(server).Decision; got == sdktrace.RecordAndSample {
		t.Error("server span with a max trace ID at low ratio should not sample")
	}
}

func TestSamplerRateZeroDisablesConsumersToo(t *testing.T) {
	t.Parallel()
	s := samplerFor(0)
	p := sdktrace.SamplingParameters{
		ParentContext: t.Context(),
		TraceID:       highTraceID(t),
		Kind:          trace.SpanKindConsumer,
	}
	if got := s.ShouldSample(p).Decision; got == sdktrace.RecordAndSample {
		t.Error("rate 0 must disable tracing entirely, consumer spans included")
	}
}
