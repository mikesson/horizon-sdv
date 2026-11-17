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
	"net"
	"net/http"
	"slices"
	"strings"
)

const ipKey ctxKey = "client_ip"

// ClientInfo creates middleware that resolves the real client IP and stores
// it in the request context for IPFromContext.
func ClientInfo() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			ctx = context.WithValue(ctx, ipKey, extractClientIP(r))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractClientIP(r *http.Request) string {
	// Cloud Run's Google front end strips its own hop and appends the IP of
	// the connection it accepted as the FINAL X-Forwarded-For entry, so the
	// rightmost entry is the real client. Everything to its left arrived in
	// the client-supplied header and is attacker-controlled; taking the
	// leftmost entry would let clients spoof the logged/rate-limited IP.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		entries := strings.Split(xff, ",")
		for _, v := range slices.Backward(entries) {
			if ip := strings.TrimSpace(v); ip != "" {
				return ip
			}
		}
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// IPFromContext returns the client IP set by the ClientInfo middleware.
func IPFromContext(ctx context.Context) (string, bool) {
	ip, ok := ctx.Value(ipKey).(string)
	return ip, ok
}

// MustIPFromContext retrieves the client IP address from the context.
// Returns empty string if not present.
func MustIPFromContext(ctx context.Context) string {
	ip, _ := IPFromContext(ctx)
	return ip
}
