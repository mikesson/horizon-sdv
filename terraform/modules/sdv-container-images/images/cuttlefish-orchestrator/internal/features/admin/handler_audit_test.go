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

package adminfeature

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
)

type fakeAuditReader struct {
	filter  *audit.ListFilter
	entries []audit.Entry
}

func (f *fakeAuditReader) Query(ctx context.Context, filter audit.ListFilter) ([]audit.Entry, error) {
	f.filter = &filter
	return f.entries, nil
}

func (f *fakeAuditReader) Count(ctx context.Context, filter audit.ListFilter) (int64, error) {
	return int64(len(f.entries)), nil
}

func newAuditTestRouter(reader *fakeAuditReader) chi.Router {
	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("Test API", "1.0.0"))
	h := &AdminHandler{auditReader: reader}
	h.Register(api)
	return r
}

const testResourceID = "dev-1"

func wantStrPtr(t *testing.T, name string, got *string, want string) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s = %v, want %q", name, got, want)
	}
}

func wantTimePtr(t *testing.T, name string, got *time.Time, want time.Time) {
	t.Helper()
	if got == nil || !got.Equal(want) {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

// TestListAuditDefaults pins that a bare request (every filter omitted,
// including the format:"date-time" time params) passes Huma validation and
// reaches the repository with an empty filter and the default page size.
func TestListAuditDefaults(t *testing.T) {
	t.Parallel()
	resourceID := testResourceID
	reader := &fakeAuditReader{entries: []audit.Entry{{
		ID:           "e1",
		Timestamp:    time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC),
		ActorEmail:   "op@example.com",
		Action:       audit.ActionDeviceCreate,
		ResourceType: audit.ResourceDevice,
		ResourceID:   &resourceID,
	}}}
	router := newAuditTestRouter(reader)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v2/admin/audit", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if reader.filter == nil {
		t.Fatal("Query was not called")
	}
	f := reader.filter
	if *f != (audit.ListFilter{Limit: 50, Offset: 0}) {
		t.Errorf("filter = %+v, want empty with limit 50", f)
	}

	var body struct {
		Items []AuditEntryItem `json:"items"`
		Total int64            `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Items) != 1 || body.Total != 1 {
		t.Fatalf("items/total = %d/%d, want 1/1", len(body.Items), body.Total)
	}
	if body.Items[0].ActorEmail != "op@example.com" || body.Items[0].ResourceID != testResourceID {
		t.Errorf("item = %+v", body.Items[0])
	}
}

func TestListAuditFilters(t *testing.T) {
	t.Parallel()
	reader := &fakeAuditReader{}
	router := newAuditTestRouter(reader)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/v2/admin/audit?actor_email=op@example.com&action=device.create&resource_type=device"+
			"&resource_id="+testResourceID+"&kind=system_config"+
			"&start_time=2026-07-01T00:00:00Z&end_time=2026-07-02T00:00:00Z&limit=10&offset=20",
		nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	f := reader.filter
	if f == nil {
		t.Fatal("Query was not called")
	}
	wantStrPtr(t, "ActorEmail", f.ActorEmail, "op@example.com")
	wantStrPtr(t, "Action", (*string)(f.Action), "device.create")
	wantStrPtr(t, "ResourceType", (*string)(f.ResourceType), "device")
	wantStrPtr(t, "ResourceID", f.ResourceID, testResourceID)
	wantStrPtr(t, "Kind", f.Kind, "system_config")
	wantTimePtr(t, "StartTime", f.StartTime, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	wantTimePtr(t, "EndTime", f.EndTime, time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC))
	if f.Limit != 10 || f.Offset != 20 {
		t.Errorf("limit/offset = %d/%d, want 10/20", f.Limit, f.Offset)
	}
}

// TestListAuditRejectsBadTime pins that an unparseable start_time is a 4xx
// It may be caught by Huma's format validation or by the handler's parse.
// Either way the caller gets a client error and the repository is never
// queried.
func TestListAuditRejectsBadTime(t *testing.T) {
	t.Parallel()
	reader := &fakeAuditReader{}
	router := newAuditTestRouter(reader)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/v2/admin/audit?start_time=not-a-time", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("status = %d, want 4xx (body: %s)", rec.Code, rec.Body.String())
	}
	if reader.filter != nil {
		t.Error("repository was queried despite the invalid time")
	}
}
