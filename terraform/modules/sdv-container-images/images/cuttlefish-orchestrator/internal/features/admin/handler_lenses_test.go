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
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

type fakeDeviceLens struct {
	total, errored int64
}

func (f *fakeDeviceLens) List(ctx context.Context, filter device.ListFilter) ([]*device.Device, error) {
	return nil, nil
}

func (f *fakeDeviceLens) ListPinnedActive(ctx context.Context, limit int) ([]*device.Device, error) {
	return nil, nil
}

func (f *fakeDeviceLens) CountOutcomesSince(ctx context.Context, since time.Time) (int64, int64, error) {
	return f.total, f.errored, nil
}

type fakeHostLens struct {
	total, errored int64
}

func (f *fakeHostLens) List(ctx context.Context, filter host.ListFilter) ([]*host.Host, error) {
	return nil, nil
}

func (f *fakeHostLens) ListExpiringWithin(ctx context.Context, window time.Duration, limit int) ([]*host.Host, error) {
	return nil, nil
}

func (f *fakeHostLens) CountOutcomesSince(ctx context.Context, since time.Time) (int64, int64, error) {
	return f.total, f.errored, nil
}

// fakeCreateCounter counts audit events per action, honoring the Action filter.
type fakeCreateCounter struct {
	counts map[audit.Action]int64
}

func (f *fakeCreateCounter) Query(ctx context.Context, filter audit.ListFilter) ([]audit.Entry, error) {
	return nil, nil
}

func (f *fakeCreateCounter) Count(ctx context.Context, filter audit.ListFilter) (int64, error) {
	if filter.Action == nil {
		return 0, nil
	}
	return f.counts[*filter.Action], nil
}

func provisioningStats(t *testing.T, h *AdminHandler) (devices, hosts ProvisioningOutcome) {
	t.Helper()
	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("Test API", "1.0.0"))
	h.Register(api)
	req := httptest.NewRequest(http.MethodGet, "/v2/admin/provisioning-stats", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Devices ProvisioningOutcome `json:"devices"`
		Hosts   ProvisioningOutcome `json:"hosts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return body.Devices, body.Hosts
}

// Teardown cascades delete resource rows, so created counts come from the
// audit log; errored stays a rows-in-error-now number.
func TestProvisioningStatsCreatesFromAudit(t *testing.T) {
	t.Parallel()
	h := &AdminHandler{
		deviceLens: &fakeDeviceLens{total: 0, errored: 0},
		hostLens:   &fakeHostLens{total: 1, errored: 1},
		auditReader: &fakeCreateCounter{counts: map[audit.Action]int64{
			audit.ActionDeviceCreate: 25,
			audit.ActionHostCreate:   23,
		}},
	}
	devices, hosts := provisioningStats(t, h)
	if devices.Created != 25 || devices.Errored != 0 {
		t.Errorf("devices = %+v, want 25 created (audit) / 0 errored", devices)
	}
	if hosts.Created != 23 || hosts.Errored != 1 {
		t.Errorf("hosts = %+v, want 23 created (audit) / 1 errored (rows)", hosts)
	}
}

// Surviving rows are the floor: pre-audit history and audit-less deployments
// must not report fewer creates than rows that visibly exist.
func TestProvisioningStatsRowFloor(t *testing.T) {
	t.Parallel()
	h := &AdminHandler{
		deviceLens:  &fakeDeviceLens{total: 4, errored: 2},
		hostLens:    &fakeHostLens{total: 2, errored: 0},
		auditReader: &fakeCreateCounter{counts: map[audit.Action]int64{}},
	}
	devices, hosts := provisioningStats(t, h)
	if devices.Created != 4 || devices.Errored != 2 {
		t.Errorf("devices = %+v, want rows floor 4/2", devices)
	}
	if hosts.Created != 2 {
		t.Errorf("hosts = %+v, want rows floor 2", hosts)
	}
}

func TestProvisioningStatsWithoutAuditReader(t *testing.T) {
	t.Parallel()
	h := &AdminHandler{
		deviceLens: &fakeDeviceLens{total: 3, errored: 1},
		hostLens:   &fakeHostLens{},
	}
	devices, _ := provisioningStats(t, h)
	if devices.Created != 3 || devices.Errored != 1 {
		t.Errorf("devices = %+v, want row counts with nil audit reader", devices)
	}
}
