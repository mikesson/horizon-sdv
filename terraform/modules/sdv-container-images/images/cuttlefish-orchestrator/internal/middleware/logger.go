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

package middleware

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// LoggerDefault returns a middleware that logs requests using the default slog logger.
func LoggerDefault() Middleware {
	return Logger(slog.Default())
}

// Logger creates middleware that logs HTTP requests.
func Logger(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Wrap response writer to capture status code
			ww := &responseWriter{ResponseWriter: w, code: http.StatusOK}

			next.ServeHTTP(ww, r)

			duration := time.Since(start)

			// Log attributes
			attrs := []any{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.code),
				slog.Duration("duration", duration),
				slog.String("ip", MustIPFromContext(r.Context())),
				slog.Int("bytes", ww.bytesWritten),
			}
			if id, ok := RequestIDFromContext(r.Context()); ok {
				attrs = append(attrs, slog.String("request_id", id))
			}

			// Log with the request context so the trace/span IDs from the
			// edge server span (set by otelhttp) reach the Cloud Logging
			// handler and correlate the access log with its trace.
			ctx := r.Context()
			switch {
			case ww.code >= 500:
				logger.ErrorContext(ctx, "HTTP request failed", attrs...)
			case ww.code >= 400:
				logger.WarnContext(ctx, "HTTP client error", attrs...)
			default:
				logger.InfoContext(ctx, "HTTP request", attrs...)
			}
		})
	}
}

// responseWriter wraps http.ResponseWriter to capture status code and bytes written
type responseWriter struct {
	http.ResponseWriter
	code         int
	bytesWritten int
}

// WriteHeader records the status for the access log before delegating. A
// handler that never calls it leaves the 200 the wrapper was constructed with,
// which is the status net/http then sends.
func (w *responseWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += n
	return n, err
}

// Hijack implements http.Hijacker to support WebSocket upgrades.
// This is required because the responseWriter wrapper must preserve
// the Hijacker interface for WebSocket connections to work.
func (w *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// Flush implements http.Flusher to support streaming responses.
func (w *responseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
