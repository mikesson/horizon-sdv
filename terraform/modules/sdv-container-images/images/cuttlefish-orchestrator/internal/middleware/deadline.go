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
	"net/http"
	"time"
)

// Deadlines is the connection deadline pair one request runs under. A zero
// field disables that deadline.
type Deadlines struct {
	Read  time.Duration
	Write time.Duration
}

// RequestDeadlines sets per-request read/write deadlines on the underlying
// connection via http.ResponseController, replacing server-wide
// ReadTimeout/WriteTimeout, which cannot distinguish a JSON API call from the
// legacy `:wait` long-poll (blocks up to 55s by contract), a storage stream, or
// a device verb that waits out a Host Orchestrator operation. The caller
// classifies each request; hijacked connections (WebSockets) have their
// deadlines cleared by net/http on hijack regardless.
//
// A WRITE deadline shorter than the handler's own runtime is a fault. It
// breaks the response mid-flight, which the platform reports as a malformed
// response or connection error, and the work the handler was doing completes
// unseen. Classify slow endpoints accordingly.
//
// Deadline errors (e.g. an exotic ResponseWriter without a controller) are
// ignored, because the deadline is a protection the handler does not rely on.
func RequestDeadlines(classify func(*http.Request) Deadlines) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d := classify(r)
			if d.Read > 0 || d.Write > 0 {
				rc := http.NewResponseController(w)
				now := time.Now()
				if d.Read > 0 {
					_ = rc.SetReadDeadline(now.Add(d.Read))
				}
				if d.Write > 0 {
					_ = rc.SetWriteDeadline(now.Add(d.Write))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
