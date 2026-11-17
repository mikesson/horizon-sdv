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

// The slice's first tests. Both endpoints read twice, the record(s) and then
// the live task progress, and each read has its own failure, so what needs
// pinning is that a progress failure is not silently reported as an empty
// progress. A page of operations with every progress blanked looks like a
// healthy fleet standing still.
package operationfeature

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

var errBoom = errors.New("boom")

type stubReader struct {
	rec         *operation.Record
	list        []*operation.Record
	progress    map[string]operation.Progress
	getErr      error
	listErr     error
	progressErr error

	progressCalls [][]string
}

func (s *stubReader) Get(context.Context, string) (*operation.Record, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.rec, nil
}

func (s *stubReader) List(context.Context, spanner.ListFilter) ([]*operation.Record, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.list, nil
}

func (s *stubReader) ProgressByTasks(_ context.Context, ids []string) (map[string]operation.Progress, error) {
	s.progressCalls = append(s.progressCalls, ids)
	if s.progressErr != nil {
		return nil, s.progressErr
	}
	return s.progress, nil
}

func serve(t *testing.T, ops Reader, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("Test API", "1.0.0"))
	NewHandler(ops).Register(api)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// running is an operation whose task is still live, so it is the only shape
// that reaches ProgressByTasks at all.
func running(id string) *operation.Record {
	return &operation.Record{ID: id, TaskID: "task-" + id, Verb: operation.VerbDeviceRestart}
}

func TestGetOperationSurfacesReadFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		ops        *stubReader
		wantStatus int
	}{
		{
			name:       "record read fails",
			ops:        &stubReader{getErr: errBoom},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "missing operation maps to 404",
			ops:        &stubReader{getErr: operation.ErrNotFound},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "progress read fails",
			ops:        &stubReader{rec: running("op-1"), progressErr: errBoom},
			wantStatus: http.StatusInternalServerError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := serve(t, tc.ops, "/v2/operations/op-1")
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestListOperationsSurfacesReadFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ops  *stubReader
	}{
		{"listing fails", &stubReader{listErr: errBoom}},
		{"progress read fails", &stubReader{list: []*operation.Record{running("op-1")}, progressErr: errBoom}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := serve(t, tc.ops, "/v2/operations")
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestProgressFailureIsNotReportedAsNoProgress is the assertion the status codes
// above cannot make. Swallowing the progress error would return 200 with every
// operation showing zero progress, which a client cannot tell apart from work
// that has not started. Asserting the 500 only proves the current code errors;
// this proves no body was produced to be misread.
func TestProgressFailureIsNotReportedAsNoProgress(t *testing.T) {
	t.Parallel()
	ops := &stubReader{list: []*operation.Record{running("op-1")}, progressErr: errBoom}

	out, err := NewHandler(ops).ListOperations(t.Context(), &ListOperationsInput{Limit: 20})
	if err == nil {
		t.Fatal("expected the progress failure to surface")
	}
	if out != nil {
		t.Errorf("returned a page alongside the error: %+v", out)
	}
	if len(ops.progressCalls) != 1 {
		t.Errorf("ProgressByTasks called %d times, want 1", len(ops.progressCalls))
	}
}

// TestProgressSkipsSettledOperations pins the batching rule: a settled record
// carries its own outcome and its task row is usually gone, so it must not widen
// the query. Without this, progressFor could ask for every task and the error
// tests above would not notice.
func TestProgressSkipsSettledOperations(t *testing.T) {
	t.Parallel()
	settled := &operation.Record{ID: "op-done", TaskID: "task-op-done", Done: true}
	noTask := &operation.Record{ID: "op-notask"}
	live := running("op-live")

	ops := &stubReader{
		list:     []*operation.Record{settled, noTask, live},
		progress: map[string]operation.Progress{"task-op-live": {}},
	}
	if _, err := NewHandler(ops).ListOperations(t.Context(), &ListOperationsInput{Limit: 20}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ops.progressCalls) != 1 {
		t.Fatalf("ProgressByTasks called %d times, want 1", len(ops.progressCalls))
	}
	if got := ops.progressCalls[0]; len(got) != 1 || got[0] != "task-op-live" {
		t.Errorf("asked for %v, want only the live task", got)
	}
}

// TestProgressNotQueriedWhenNothingIsLive covers the early return: a page of
// settled operations must not hit the task table at all.
func TestProgressNotQueriedWhenNothingIsLive(t *testing.T) {
	t.Parallel()
	ops := &stubReader{list: []*operation.Record{
		{ID: "op-1", TaskID: "task-1", Done: true},
		{ID: "op-2"},
	}}
	if _, err := NewHandler(ops).ListOperations(t.Context(), &ListOperationsInput{Limit: 20}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ops.progressCalls) != 0 {
		t.Errorf("queried task progress for a fully settled page: %v", ops.progressCalls)
	}
}

// TestDoneFilterResolvesTriState pins the tri-state query parameter that exists
// only because a *bool would panic Huma at registration (see the comment on
// ListOperationsInput.Done). Omitted must mean "either", not "not done".
func TestDoneFilterResolvesTriState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want *bool
	}{
		{"", nil},
		{"true", new(true)},
		{"false", new(false)},
	}
	for _, tc := range tests {
		got := (&ListOperationsInput{Done: tc.in}).DoneFilter()
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("Done=%q resolved to %v, want nil (either)", tc.in, *got)
		case tc.want != nil && got == nil:
			t.Errorf("Done=%q resolved to nil, want %v", tc.in, *tc.want)
		case tc.want != nil && got != nil && *got != *tc.want:
			t.Errorf("Done=%q resolved to %v, want %v", tc.in, *got, *tc.want)
		}
	}
}
