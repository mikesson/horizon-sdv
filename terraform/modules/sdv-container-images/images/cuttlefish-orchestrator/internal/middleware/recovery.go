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
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recovery creates middleware that recovers from panics and logs the error.
func Recovery(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					// net/http panics with ErrAbortHandler to abort a reply
					// (e.g. the client disconnected mid-stream). Re-raise it
					// so the server suppresses it as designed instead of
					// logging a fake panic and writing a superfluous 500.
					if abortErr, ok := err.(error); ok && errors.Is(abortErr, http.ErrAbortHandler) {
						panic(err)
					}
					stack := debug.Stack()
					logger.Error("Panic recovered",
						slog.Any("error", err),
						slog.String("stack", string(stack)),
					)

					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}
