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

// Package telemetry provides OpenTelemetry instrumentation for the orchestrator.
// Traces and metrics are exported via OTLP to Google Cloud's native ingestion
// endpoint (telemetry.googleapis.com); logs stay on stdout and are correlated by
// trace ID in Cloud Logging (see logging.go). Failure to reach the endpoint
// degrades observability only. It never blocks or crashes the service.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"go.opentelemetry.io/contrib/detectors/gcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/oauth"
)

const (
	// ServiceName is the canonical service name for telemetry.
	ServiceName = "cuttlefish-orchestrator"

	// InstrumentationName is the root name for all instrumentation scopes.
	// Every Tracer/Meter scope derives from this so Cloud Trace/Monitoring
	// filtering stays coherent (see ScopeName).
	InstrumentationName = "cloud-cuttlefish-orchestrator"

	// InstrumentationVersion is the version of the instrumentation.
	InstrumentationVersion = "1.0.0"

	// DefaultEndpoint is Google Cloud's native OTLP ingestion endpoint. It
	// speaks OTLP over gRPC with TLS and routes data by the resource's
	// gcp.project_id attribute, which it REQUIRES: payloads without it are
	// rejected with InvalidArgument. The GCP resource detector does NOT set it
	// (it sets cloud.account.id), so Init adds it explicitly.
	DefaultEndpoint = "https://telemetry.googleapis.com"

	// otlpAuthScope is the OAuth scope used by the ADC token source backing
	// the OTLP exporters.
	otlpAuthScope = "https://www.googleapis.com/auth/cloud-platform"
)

// ScopeName returns a child instrumentation-scope name rooted at
// InstrumentationName, e.g. ScopeName("taskqueue") -> the canonical scope for
// the task queue. Use this instead of ad-hoc scope strings.
func ScopeName(component string) string {
	return InstrumentationName + "/" + component
}

// RecordError marks span as failed with err. It both records the exception
// event AND sets the span status to Error: Cloud Trace's error-rate views and
// "has error" filter key off span status, not the event, so a bare
// span.RecordError leaves a failed operation rendering as a green span.
func RecordError(span trace.Span, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// Config holds configuration for telemetry initialization.
type Config struct {
	// ProjectID is the GCP project ID for exporting telemetry.
	ProjectID string

	// ServiceVersion is the version of the service (for resource attributes).
	ServiceVersion string

	// Environment is the deployment environment (e.g., "production", "staging").
	Environment string

	// Endpoint is the OTLP ingestion endpoint. Defaults to DefaultEndpoint.
	Endpoint string

	// SampleRate is the trace sampling rate (0.0-1.0). Default is 1.0 (all
	// traces); override via OTEL_TRACES_SAMPLER_ARG. ParentBased sampling means
	// only root spans pay this rate. For high-volume production, set 0.1 or lower.
	SampleRate float64

	// MetricInterval is how often metrics are exported. Default is 60 seconds.
	MetricInterval time.Duration

	// EnableTracing enables trace export. Default is true.
	EnableTracing bool

	// EnableMetrics enables metrics export. Default is true.
	EnableMetrics bool
}

// DefaultConfig returns a default configuration suitable for Cloud Run.
func DefaultConfig(projectID string) Config {
	env := os.Getenv("ENV")
	if env == "" {
		env = "production"
	}

	version := os.Getenv("SERVICE_VERSION")
	if version == "" {
		version = "unknown"
	}

	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}

	return Config{
		ProjectID:      projectID,
		ServiceVersion: version,
		Environment:    env,
		Endpoint:       endpoint,
		SampleRate:     sampleRateFromEnv(),
		MetricInterval: 60 * time.Second,
		EnableTracing:  true,
		EnableMetrics:  true,
	}
}

// sampleRateFromEnv reads OTEL_TRACES_SAMPLER_ARG (0.0-1.0), defaulting to 1.0
// when unset or invalid.
func sampleRateFromEnv() float64 {
	v := os.Getenv("OTEL_TRACES_SAMPLER_ARG")
	if v == "" {
		return 1.0
	}
	rate, err := strconv.ParseFloat(v, 64)
	if err != nil || rate < 0 || rate > 1 {
		slog.Warn("telemetry: invalid OTEL_TRACES_SAMPLER_ARG, defaulting to 1.0",
			slog.String("value", v))
		return 1.0
	}
	return rate
}

// Telemetry holds the initialized OpenTelemetry providers.
type Telemetry struct {
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider

	// Tracer is the main tracer for creating spans.
	Tracer trace.Tracer

	// Meter is the main meter for creating metrics.
	Meter metric.Meter
}

// Init initializes OpenTelemetry with OTLP export to telemetry.googleapis.com.
// Tracing and metrics initialize independently: a failure in either (or in the
// ADC credentials) disables that signal but never blocks startup. Returns a
// Telemetry instance and a shutdown function.
func Init(ctx context.Context, cfg Config) (*Telemetry, func(context.Context) error, error) {
	if cfg.ProjectID == "" {
		return nil, nil, errors.New("project ID is required for telemetry")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}

	// Build resource with GCP detector and service attributes. The detector
	// populates cloud.*/faas.* so the endpoint can derive the monitored
	// resource, but gcp.project_id must be set by hand: the Telemetry API
	// routes by it and rejects the whole payload when it is absent.
	res, err := resource.New(ctx,
		resource.WithDetectors(gcp.NewDetector()),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			attribute.String("gcp.project_id", cfg.ProjectID),
			semconv.ServiceName(ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
			semconv.DeploymentEnvironmentName(cfg.Environment),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create resource: %w", err)
	}

	tel := &Telemetry{}
	var shutdownFuncs []func(context.Context) error

	// Per-RPC ADC credentials shared by both OTLP exporters. The quota project
	// is taken from GOOGLE_CLOUD_QUOTA_PROJECT by ADC; we send no
	// x-goog-user-project header, because it double-counts on the Telemetry API.
	creds, credErr := oauth.NewApplicationDefault(ctx, otlpAuthScope)
	switch {
	case credErr != nil:
		slog.Warn("telemetry: no application default credentials; export disabled",
			slog.Any("error", credErr))
	default:
		if cfg.EnableTracing {
			if tp, terr := initTracing(ctx, cfg, res, creds); terr != nil {
				slog.Warn("telemetry: tracing disabled", slog.Any("error", terr))
			} else {
				tel.tracerProvider = tp
				otel.SetTracerProvider(tp)
				shutdownFuncs = append(shutdownFuncs, tp.Shutdown)
			}
		}
		if cfg.EnableMetrics {
			if mp, merr := initMetrics(ctx, cfg, res, creds); merr != nil {
				slog.Warn("telemetry: metrics disabled", slog.Any("error", merr))
			} else {
				tel.meterProvider = mp
				otel.SetMeterProvider(mp)
				shutdownFuncs = append(shutdownFuncs, mp.Shutdown)
			}
		}
	}

	// W3C Trace Context + Baggage for distributed tracing.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Convenience tracer and meter resolve to the global providers (no-op if
	// export was disabled above), so callers never need nil checks.
	tel.Tracer = otel.Tracer(InstrumentationName, trace.WithInstrumentationVersion(InstrumentationVersion))
	tel.Meter = otel.Meter(InstrumentationName, metric.WithInstrumentationVersion(InstrumentationVersion))

	shutdown := func(ctx context.Context) error {
		var errs []error
		for _, fn := range shutdownFuncs {
			if err := fn(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("telemetry shutdown errors: %v", errs)
		}
		return nil
	}

	slog.Info("OpenTelemetry initialized",
		slog.String("endpoint", cfg.Endpoint),
		slog.Bool("tracing", tel.tracerProvider != nil),
		slog.Bool("metrics", tel.meterProvider != nil),
		slog.Float64("sample_rate", cfg.SampleRate),
	)

	return tel, shutdown, nil
}

// initTracing sets up the trace provider with an OTLP/gRPC exporter.
func initTracing(ctx context.Context, cfg Config, res *resource.Resource, creds credentials.PerRPCCredentials) (*sdktrace.TracerProvider, error) {
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpointURL(cfg.Endpoint),
		otlptracegrpc.WithDialOption(grpc.WithPerRPCCredentials(creds)),
	)
	if err != nil {
		return nil, fmt.Errorf("creating OTLP trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(samplerFor(cfg.SampleRate)),
	)
	return tp, nil
}

// initMetrics sets up the meter provider with an OTLP/gRPC exporter. Delta
// temporality matches what Cloud Monitoring's OTLP ingestion expects for sums
// and histograms.
func initMetrics(ctx context.Context, cfg Config, res *resource.Resource, creds credentials.PerRPCCredentials) (*sdkmetric.MeterProvider, error) {
	exporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpointURL(cfg.Endpoint),
		otlpmetricgrpc.WithDialOption(grpc.WithPerRPCCredentials(creds)),
		otlpmetricgrpc.WithTemporalitySelector(cloudMonitoringTemporality),
	)
	if err != nil {
		return nil, fmt.Errorf("creating OTLP metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(exporter,
				sdkmetric.WithInterval(cfg.MetricInterval),
			),
		),
	)
	return mp, nil
}

// samplerFor builds the sampler: child spans follow their parent's decision,
// root spans are sampled at the configured rate, except consumer spans,
// which are always sampled (see consumerAlwaysSampler). Rate 0 disables
// tracing entirely, consumers included.
func samplerFor(rate float64) sdktrace.Sampler {
	switch {
	case rate >= 1.0:
		return sdktrace.AlwaysSample()
	case rate <= 0:
		return sdktrace.NeverSample()
	default:
		return consumerAlwaysSampler{base: sdktrace.ParentBased(sdktrace.TraceIDRatioBased(rate))}
	}
}

// consumerAlwaysSampler samples every consumer span regardless of the ratio.
// Consumer spans here are exactly the durable task attempts (GCE provisioning,
// Host Orchestrator work), which are low-volume, failure-prone, and the traces
// most worth keeping, while the ratio governs the high-volume HTTP and client
// tiers. Their children follow parent-based sampling as usual.
type consumerAlwaysSampler struct{ base sdktrace.Sampler }

// ShouldSample implements sdktrace.Sampler.
func (s consumerAlwaysSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	if p.Kind == trace.SpanKindConsumer {
		return sdktrace.AlwaysSample().ShouldSample(p)
	}
	return s.base.ShouldSample(p)
}

// Description implements sdktrace.Sampler.
func (s consumerAlwaysSampler) Description() string {
	return "ConsumerAlways(" + s.base.Description() + ")"
}

// cloudMonitoringTemporality selects delta temporality for sums and histograms
// (Cloud Monitoring's OTLP convention); up-down counters and gauges stay
// cumulative, since the delta of a level has no meaning.
func cloudMonitoringTemporality(kind sdkmetric.InstrumentKind) metricdata.Temporality {
	switch kind {
	case sdkmetric.InstrumentKindCounter,
		sdkmetric.InstrumentKindHistogram,
		sdkmetric.InstrumentKindObservableCounter:
		return metricdata.DeltaTemporality
	default:
		return metricdata.CumulativeTemporality
	}
}

// TracerProvider returns the trace provider, or nil if tracing is disabled.
func (t *Telemetry) TracerProvider() *sdktrace.TracerProvider {
	return t.tracerProvider
}

// MeterProvider returns the meter provider, or nil if metrics are disabled.
func (t *Telemetry) MeterProvider() *sdkmetric.MeterProvider {
	return t.meterProvider
}
