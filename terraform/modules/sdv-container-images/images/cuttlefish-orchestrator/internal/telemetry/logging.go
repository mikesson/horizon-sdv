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
	"io"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// TracedHandler wraps an slog.Handler to add trace correlation fields.
// When running on Cloud Run, Cloud Logging automatically correlates logs
// with traces when the logging.googleapis.com/trace field is present.
type TracedHandler struct {
	handler   slog.Handler
	projectID string
}

// NewTracedHandler creates a new handler that adds trace context to log records.
// The projectID is required for constructing the full trace resource name.
func NewTracedHandler(handler slog.Handler, projectID string) *TracedHandler {
	return &TracedHandler{
		handler:   handler,
		projectID: projectID,
	}
}

// Enabled implements slog.Handler.
func (h *TracedHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.handler.Enabled(ctx, level)
}

// Handle implements slog.Handler. It extracts trace context from the context
// and adds Google Cloud Logging trace correlation fields.
func (h *TracedHandler) Handle(ctx context.Context, record slog.Record) error {
	// Extract span context from the context
	spanCtx := trace.SpanContextFromContext(ctx)

	if spanCtx.IsValid() {
		// Add trace correlation fields for Google Cloud Logging
		// See: https://cloud.google.com/logging/docs/structured-logging#special-payload-fields
		traceID := spanCtx.TraceID().String()
		spanID := spanCtx.SpanID().String()

		// Format: projects/PROJECT_ID/traces/TRACE_ID
		traceResource := "projects/" + h.projectID + "/traces/" + traceID

		// Add as attributes to the record
		record.AddAttrs(
			slog.String("logging.googleapis.com/trace", traceResource),
			slog.String("logging.googleapis.com/spanId", spanID),
			slog.Bool("logging.googleapis.com/trace_sampled", spanCtx.IsSampled()),
		)
	}

	return h.handler.Handle(ctx, record)
}

// WithAttrs implements slog.Handler.
func (h *TracedHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TracedHandler{
		handler:   h.handler.WithAttrs(attrs),
		projectID: h.projectID,
	}
}

// WithGroup implements slog.Handler.
func (h *TracedHandler) WithGroup(name string) slog.Handler {
	return &TracedHandler{
		handler:   h.handler.WithGroup(name),
		projectID: h.projectID,
	}
}

// NewCloudLoggingHandler creates a JSON handler optimized for Cloud Logging
// with trace correlation. This handler:
// - Uses JSON format (required for structured logging in Cloud Run)
// - Maps slog levels to Cloud Logging severity
// - Adds trace correlation fields when a span context is present
func NewCloudLoggingHandler(w io.Writer, projectID string, opts *slog.HandlerOptions) slog.Handler {
	if opts == nil {
		opts = &slog.HandlerOptions{}
	}

	// Cloud Logging expects specific field names for severity
	// slog's JSONHandler already uses "level" which Cloud Logging understands
	jsonHandler := slog.NewJSONHandler(w, opts)

	return NewTracedHandler(jsonHandler, projectID)
}

// GuardDegradedKey marks a log record as a safety check that was SKIPPED, with
// the caller proceeding without its protection. Named as a field rather than
// left to the message text so the condition is queryable: the live smoke fails
// on any record carrying it, and that assertion keeps working for guards written
// after it.
const GuardDegradedKey = "guard_degraded"

// GuardDegraded marks a record as a guard that fell back to its unprotected
// path.
//
// Use it ONLY where the failure makes the system MORE permissive than it would
// otherwise be: a check that could not run, so the caller went ahead anyway.
// That is the case nothing else can see: the work succeeds, every status is
// green, and the only difference is that a protection silently was not applied.
//
// Do NOT use it where failure is conservative (an action declined, a host left
// fenced, a destructive step deferred). Those are guards working, and marking
// them would drown the signal in exactly the cases that need no attention. Nor
// on a plain retry: work that will be reattempted has not lost a protection.
//
// The distinction is worth making at each call site instead of by reading the
// message later. A reconciling guard shipped refused by the database and fell
// back on every cycle for a whole release, while the full live smoke passed,
// because "guard absent" and "guard working" produced identical output.
func GuardDegraded() slog.Attr {
	return slog.Bool(GuardDegradedKey, true)
}
