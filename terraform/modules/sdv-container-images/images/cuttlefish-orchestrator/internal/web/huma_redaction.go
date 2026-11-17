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

package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"github.com/danielgtaylor/huma/v2"
)

// RedactServerErrors installs the Huma error model that keeps internal error
// text out of 5xx response bodies and logs it instead.
//
// Huma's constructors render every error handed to their variadic cause into
// the problem document's errors[] array, and three paths reach that with an
// internal error: the operation dispatcher, which wraps whatever MapError did
// not classify; the admin handlers, which attach the repository error to their
// 500s; and Huma's own request-body failures. A Spanner or GCE chain, dial
// targets included, therefore reached authenticated callers, while the raw chi
// routes have served a fixed message for exactly this class since they were
// written (see RespondError). Two halves of one surface, opposite policies.
//
// Only the causes are dropped, and only at 5xx. The Detail survives: on a
// mapped family it is a decided classification (a capacity 503's own text, an
// upstream Host Orchestrator message) and not a leak. Below 500 nothing is
// touched, because that is where Huma's request-validation details live and
// they name the offending field.
//
// The dropped cause is logged, which is where it belonged: nothing else records
// it. The access log carries the status and never the reason, so before this
// the client was the only party told why its request failed.
//
// Installed by every construction of the API instead of once in the
// composition root, so that no surface, tests and spec generation included,
// can be built without it. That placement is also what makes writing these
// package-level vars safe: serving a request is always downstream of the
// construction that registered its route, so every read of the model
// happens-after the sync.Once that wrote it.
func RedactServerErrors() { redactServerErrors() }

var redactServerErrors = sync.OnceFunc(func() {
	newError, newErrorWithContext := huma.NewError, huma.NewErrorWithContext

	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		if withheld(context.Background(), status, msg, errs) {
			errs = nil
		}
		return newError(status, msg, errs...)
	}

	// Huma hands this one the request context, so its log line correlates with
	// the request's trace. It withholds the causes itself instead of leaving
	// them to the override above (which the default implementation calls) so
	// one failure can never be logged twice.
	huma.NewErrorWithContext = func(ctx huma.Context, status int, msg string, errs ...error) huma.StatusError {
		if withheld(ctx.Context(), status, msg, errs) {
			errs = nil
		}
		return newErrorWithContext(ctx, status, msg, errs...)
	}
})

// withheld logs the causes behind a server error and reports whether they must
// be kept off the wire.
func withheld(ctx context.Context, status int, detail string, errs []error) bool {
	if status < http.StatusInternalServerError {
		return false
	}
	cause := errors.Join(errs...)
	if cause == nil {
		return false
	}
	slog.Default().ErrorContext(ctx, "internal error withheld from response",
		slog.Int("status", status),
		slog.String("detail", detail),
		slog.Any("error", cause))
	return true
}
