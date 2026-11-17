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

package router

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// secret is the internal detail an unclassified failure carries. Every
// fragment of it is checked separately: a body that echoed only the dial
// target would still be a leak.
const secret = "spanner: dial tcp 10.0.0.5:9010: connection refused"

var secretFragments = []string{"spanner", "10.0.0.5:9010", "connection refused"}

// TestServerErrorsAreRedacted pins the 5xx wire contract: an internal error
// reaches the log and never the client, whichever of the three paths produced
// it. Delete the web.RedactServerErrors call in NewHumaAPIWithPrefix and the
// unclassified and cause-attached arms both go red.
func TestServerErrorsAreRedacted(t *testing.T) {
	logs := captureLogs(t)

	r := chi.NewRouter()
	api := NewHumaAPI(r, DefaultHumaConfig())

	// The unclassified arm: a handler routing an infrastructure error through
	// MapError, which by contract returns it unmapped for Huma to render.
	register(api, "unclassified", "/v2/unclassified", func() error {
		return web.MapError(fmt.Errorf("loading device: %w", fmt.Errorf("%s", secret)))
	})
	// The cause-attached arm: how the admin handlers build their 500s.
	register(api, "cause-attached", "/v2/cause-attached", func() error {
		return huma.Error500InternalServerError("failed to read queue statistics", fmt.Errorf("%s", secret))
	})

	for _, path := range []string{"/v2/unclassified", "/v2/cause-attached"} {
		t.Run(path, func(t *testing.T) {
			body, status := get(t, r, path)
			if status != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", status)
			}
			for _, fragment := range secretFragments {
				if strings.Contains(body, fragment) {
					t.Errorf("response leaked %q\nbody: %s", fragment, body)
				}
			}
		})
	}

	// The cause must have gone somewhere: withholding it from the client is
	// only correct because the log now carries it.
	logged := logs.String()
	for _, fragment := range secretFragments {
		if !strings.Contains(logged, fragment) {
			t.Errorf("log lost %q; the cause is now recorded nowhere\nlog: %s", fragment, logged)
		}
	}
	if got := strings.Count(logged, "internal error withheld from response"); got != 2 {
		t.Errorf("withheld log lines = %d, want 2 (one per arm, never double-logged)", got)
	}
}

// TestRedactionSparesClientErrorsAndClassifications guards the other
// direction: redaction that also swallowed validation details or a mapped
// family's own message would pass the test above while breaking the API.
func TestRedactionSparesClientErrorsAndClassifications(t *testing.T) {
	captureLogs(t)

	r := chi.NewRouter()
	api := NewHumaAPI(r, DefaultHumaConfig())

	register(api, "client-error", "/v2/client-error", func() error {
		return huma.Error400BadRequest("invalid request", &huma.ErrorDetail{
			Location: "body.name", Message: "expected string, got number",
		})
	})
	// A mapped 5xx family. Its detail is an intended classification and leaks
	// nothing.
	register(api, "classified", "/v2/classified", func() error {
		return web.MapError(&host.CapacityExceededError{HostID: "h-1", CurrentCount: 4, MaxCapacity: 4})
	})

	body, status := get(t, r, "/v2/client-error")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	for _, want := range []string{"body.name", "expected string, got number"} {
		if !strings.Contains(body, want) {
			t.Errorf("400 lost its validation detail %q\nbody: %s", want, body)
		}
	}

	body, status = get(t, r, "/v2/classified")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
	if !strings.Contains(body, "host h-1 has 4/4 devices") {
		t.Errorf("503 lost its classification message\nbody: %s", body)
	}
}

// register mounts a GET operation whose handler always fails with err().
func register(api huma.API, id, path string, err func() error) {
	huma.Register(api, huma.Operation{
		OperationID: id,
		Method:      http.MethodGet,
		Path:        path,
	}, func(context.Context, *struct{}) (*struct{}, error) {
		return nil, err()
	})
}

func get(t *testing.T, r chi.Router, path string) (string, int) {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Body.String(), rec.Code
}

// captureLogs redirects the default logger for the test's duration. That is
// what the redaction layer writes to, exactly as it does in the composition
// root.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buf
}
