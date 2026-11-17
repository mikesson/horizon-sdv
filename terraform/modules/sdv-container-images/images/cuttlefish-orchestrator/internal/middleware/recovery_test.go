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
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoveryLogsPanicAndWrites500(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	h := Recovery(slog.New(slog.NewJSONHandler(&buf, nil)))(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("boom")
		}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(buf.String(), "Panic recovered") || !strings.Contains(buf.String(), "boom") {
		t.Errorf("log = %q, want a recovered-panic entry mentioning the panic value", buf.String())
	}
}

func TestRecoveryReRaisesErrAbortHandler(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	h := Recovery(slog.New(slog.NewJSONHandler(&buf, nil)))(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		}))

	rec := httptest.NewRecorder()
	defer func() {
		r := recover()
		err, ok := r.(error)
		if !ok || !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("recovered %v, want http.ErrAbortHandler re-raised for net/http to suppress", r)
		}
		if buf.Len() != 0 {
			t.Errorf("log = %q, want nothing (a client disconnect is not a panic)", buf.String())
		}
		if rec.Body.Len() != 0 {
			t.Errorf("body = %q, want no superfluous 500 written", rec.Body.String())
		}
	}()

	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	t.Fatal("ServeHTTP returned; want ErrAbortHandler to propagate")
}
