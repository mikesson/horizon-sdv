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
	"context"
	"net/http"

	"github.com/google/uuid"
)

const requestIDKey ctxKey = "request_id"

// maxRequestIDLength bounds inbound X-Request-ID values so a client cannot
// inflate every access-log line with an arbitrarily long header.
const maxRequestIDLength = 128

// RequestID creates middleware that ensures every request has a Request-ID header.
// Inbound values are echoed only when they are printable and bounded in length;
// anything else is replaced with a freshly generated UUID.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-ID")
			if !validRequestID(id) {
				id = uuid.New().String()
			}

			// Set header for response
			w.Header().Set("X-Request-ID", id)

			// Add to context
			ctx := context.WithValue(r.Context(), requestIDKey, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// validRequestID reports whether an inbound request ID is safe to echo and log:
// non-empty, at most maxRequestIDLength bytes, printable ASCII without spaces.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLength {
		return false
	}
	for i := range len(id) {
		if id[i] <= ' ' || id[i] > '~' {
			return false
		}
	}
	return true
}

// RequestIDFromContext returns the request ID set by the RequestID middleware.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey).(string)
	return id, ok
}
