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

// Error paths for the admin slice. Every collaborator is an interface, so these
// need injection rather than infrastructure: fail one dependency at a time and
// assert what the handler does with it.
//
// The tests separate the endpoints that MUST degrade from the endpoints that
// MUST fail. Stats and Reclaimable are
// dashboards assembled from independent reads, and they log-and-continue: a
// failing read must leave its own section empty while the others still fill,
// which "returned 200" alone cannot show. ProvisioningStats, ControlPlane and
// Capacity report numbers an operator makes decisions from, so a partial read
// is a wrong answer and must surface as 500.
package adminfeature

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

// errBoom stands in for any repository failure. Assertions match on it with
// errors.Is where the handler is supposed to preserve the cause.
var errBoom = errors.New("boom")

// discardLogger keeps the degrade paths' warnings out of the test output; they
// are the expected behavior here.
func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// --- stubs, each failing exactly where the test points it ---

type stubHostCounter struct {
	counts map[host.Status]int64
	err    error
}

// A failing repository returns no counts, so the stubs must not hand back data
// alongside an error. That would hide a handler that tallies a failed read.
func (s *stubHostCounter) CountByStatus(context.Context) (map[host.Status]int64, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.counts, nil
}

type stubDeviceCounter struct {
	counts map[device.Status]int64
	err    error
}

func (s *stubDeviceCounter) CountByStatus(context.Context) (map[device.Status]int64, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.counts, nil
}

type stubQuotaService struct {
	q   *quota.Quota
	err error
}

func (s *stubQuotaService) SetLimit(context.Context, quota.Scope, string, int64) error { return nil }
func (s *stubQuotaService) ReconcileAll(context.Context) (int, error)                  { return 0, nil }
func (s *stubQuotaService) Get(context.Context, quota.Scope, string) (*quota.Quota, error) {
	return s.q, s.err
}

type stubTaskAdmin struct {
	listFailedErr  error
	listLiveErr    error
	countStateErr  error
	requeueErr     error
	deleteErr      error
	statsErr       error
	requeuedIDs    []string
	deletedIDs     []string
	statsCallCount int
}

func (s *stubTaskAdmin) ListFailed(context.Context, int, string) ([]*task.Task, error) {
	return nil, s.listFailedErr
}

func (s *stubTaskAdmin) ListLive(context.Context, int) ([]*task.Task, error) {
	return nil, s.listLiveErr
}

func (s *stubTaskAdmin) CountByTypeState(context.Context) ([]task.TypeStateCount, error) {
	return nil, s.countStateErr
}

func (s *stubTaskAdmin) RequeueFailed(_ context.Context, id string) error {
	if s.requeueErr != nil {
		return s.requeueErr
	}
	s.requeuedIDs = append(s.requeuedIDs, id)
	return nil
}

func (s *stubTaskAdmin) DeleteTask(_ context.Context, id string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deletedIDs = append(s.deletedIDs, id)
	return nil
}

func (s *stubTaskAdmin) Stats(context.Context) (task.QueueStats, error) {
	s.statsCallCount++
	return task.QueueStats{}, s.statsErr
}

type stubLeaseAdmin struct{ err error }

func (s *stubLeaseAdmin) ListLeases(context.Context) ([]spanner.LeaderLease, error) {
	return nil, s.err
}

// stubAuditReader fails Count per action so the ProvisioningStats and spot-churn
// paths, which call Count several times with different filters, can be pointed
// at one call at a time.
type stubAuditReader struct {
	queryErr    error
	countErr    error
	countErrFor map[audit.Action]error
	counts      map[audit.Action]int64
	entries     []audit.Entry
	queries     []audit.ListFilter
}

func (s *stubAuditReader) Query(_ context.Context, f audit.ListFilter) ([]audit.Entry, error) {
	s.queries = append(s.queries, f)
	return s.entries, s.queryErr
}

func (s *stubAuditReader) Count(_ context.Context, f audit.ListFilter) (int64, error) {
	if s.countErr != nil {
		return 0, s.countErr
	}
	if f.Action != nil {
		if err := s.countErrFor[*f.Action]; err != nil {
			return 0, err
		}
		return s.counts[*f.Action], nil
	}
	return 0, nil
}

type stubDeviceLens struct {
	listErr, pinnedErr, countErr error
	list, pinned                 []*device.Device
	total, errored               int64
}

func (s *stubDeviceLens) List(context.Context, device.ListFilter) ([]*device.Device, error) {
	return s.list, s.listErr
}

func (s *stubDeviceLens) ListPinnedActive(context.Context, int) ([]*device.Device, error) {
	return s.pinned, s.pinnedErr
}

func (s *stubDeviceLens) CountOutcomesSince(context.Context, time.Time) (int64, int64, error) {
	return s.total, s.errored, s.countErr
}

type stubHostLens struct {
	listErr, expiringErr, countErr error
	list, expiring                 []*host.Host
	total, errored                 int64
}

func (s *stubHostLens) List(context.Context, host.ListFilter) ([]*host.Host, error) {
	return s.list, s.listErr
}

func (s *stubHostLens) ListExpiringWithin(context.Context, time.Duration, int) ([]*host.Host, error) {
	return s.expiring, s.expiringErr
}

func (s *stubHostLens) CountOutcomesSince(context.Context, time.Time) (int64, int64, error) {
	return s.total, s.errored, s.countErr
}

type stubZoneCatalog struct {
	zonesErr error
	zones    []string
}

func (s *stubZoneCatalog) ListEnabledZones(context.Context) ([]string, error) {
	return s.zones, s.zonesErr
}

type stubZoneHealth struct{ err error }

func (s *stubZoneHealth) ListCells(context.Context) ([]*gcecatalog.ZoneHealth, error) {
	return nil, s.err
}

func (s *stubZoneHealth) ListCapacityCells(context.Context, []string) ([]*gcecatalog.ZoneHealth, error) {
	return nil, s.err
}

func (s *stubZoneHealth) ClearCell(context.Context, string, string) error {
	return s.err
}

type stubZoneFamily struct {
	counts []host.ZoneFamilyCount
	err    error
}

func (s *stubZoneFamily) CountByZoneFamily(context.Context) ([]host.ZoneFamilyCount, error) {
	return s.counts, s.err
}

type stubMachineCatalog struct{ err error }

func (s *stubMachineCatalog) List(context.Context, machinetype.ListFilter) ([]*machinetype.MachineType, error) {
	return nil, s.err
}

func (s *stubMachineCatalog) Get(context.Context, string) (*machinetype.MachineType, error) {
	return nil, s.err
}

type stubPlacementPolicy struct{ err error }

func (s *stubPlacementPolicy) GetQuotaMargin(context.Context) (int64, bool, error) {
	return 0, false, s.err
}

type stubSpotFleet struct{ err error }

func (s *stubSpotFleet) CountByModelStatus(context.Context) ([]host.ModelStatusCount, error) {
	return nil, s.err
}

type stubSpotAdvice struct{ err error }

func (s *stubSpotAdvice) ListCells(context.Context) ([]*gcecatalog.SpotAdvice, error) {
	return nil, s.err
}

// serve registers h and runs one request against it.
func serve(t *testing.T, h *AdminHandler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("Test API", "1.0.0"))
	h.Register(api)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// --- Stats: every read is optional, so a failure must degrade without a 500 ---

// TestStatsDegradesPerFailedRead pins the log-and-continue contract one read at
// a time. Asserting only the 200 would pass even if a failed host count wiped
// the device numbers too, so each case also names the section that must SURVIVE.
func TestStatsDegradesPerFailedRead(t *testing.T) {
	t.Parallel()

	hostCounts := map[host.Status]int64{host.StatusRunning: 3, host.StatusDeleted: 9}
	deviceCounts := map[device.Status]int64{device.StatusRunning: 5, device.StatusDeleted: 7}

	tests := []struct {
		name                     string
		hostErr, deviceErr, qErr error
		wantHostTotal            int64
		wantDeviceTotal          int64
		wantMaxQuota             int64
	}{
		{
			name: "all reads succeed", wantHostTotal: 3, wantDeviceTotal: 5, wantMaxQuota: 50,
		},
		{
			name: "host count fails", hostErr: errBoom,
			wantHostTotal: 0, wantDeviceTotal: 5, wantMaxQuota: 50,
		},
		{
			name: "device count fails", deviceErr: errBoom,
			wantHostTotal: 3, wantDeviceTotal: 0, wantMaxQuota: 50,
		},
		{
			name: "quota read fails", qErr: errBoom,
			wantHostTotal: 3, wantDeviceTotal: 5, wantMaxQuota: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &AdminHandler{
				hostCounter:   &stubHostCounter{counts: hostCounts, err: tc.hostErr},
				deviceCounter: &stubDeviceCounter{counts: deviceCounts, err: tc.deviceErr},
				quotaService:  &stubQuotaService{q: &quota.Quota{MaxHosts: 50}, err: tc.qErr},
				logger:        discardLogger(),
			}

			rec := serve(t, h, http.MethodGet, "/v2/admin/stats")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (a failed read must degrade, not fail): %s",
					rec.Code, rec.Body.String())
			}

			var body struct {
				Hosts struct {
					Total    int64 `json:"total"`
					MaxQuota int64 `json:"max_quota"`
				} `json:"hosts"`
				Devices struct {
					Total int64 `json:"total"`
				} `json:"devices"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if body.Hosts.Total != tc.wantHostTotal {
				t.Errorf("hosts.total = %d, want %d", body.Hosts.Total, tc.wantHostTotal)
			}
			if body.Devices.Total != tc.wantDeviceTotal {
				t.Errorf("devices.total = %d, want %d", body.Devices.Total, tc.wantDeviceTotal)
			}
			if body.Hosts.MaxQuota != tc.wantMaxQuota {
				t.Errorf("hosts.max_quota = %d, want %d", body.Hosts.MaxQuota, tc.wantMaxQuota)
			}
		})
	}
}

// --- task administration: every read and write must surface ---

// TestTaskEndpointsWithoutTaskAdmin covers requireTaskAdmin on all four routes.
func TestTaskEndpointsWithoutTaskAdmin(t *testing.T) {
	t.Parallel()
	routes := []struct{ method, path string }{
		{http.MethodGet, "/v2/admin/tasks/failed"},
		{http.MethodGet, "/v2/admin/tasks/stats"},
		{http.MethodGet, "/v2/admin/control-plane"},
		{http.MethodPost, "/v2/admin/tasks/t1/retry"},
		{http.MethodDelete, "/v2/admin/tasks/t1"},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			t.Parallel()
			rec := serve(t, &AdminHandler{logger: discardLogger()}, rt.method, rt.path)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500 when taskAdmin is unconfigured", rec.Code)
			}
		})
	}
}

func TestTaskReadFailuresSurfaceAs500(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		admin  *stubTaskAdmin
		method string
		path   string
	}{
		{"list failed", &stubTaskAdmin{listFailedErr: errBoom}, http.MethodGet, "/v2/admin/tasks/failed"},
		{"queue stats", &stubTaskAdmin{statsErr: errBoom}, http.MethodGet, "/v2/admin/tasks/stats"},
		{"delete task", &stubTaskAdmin{deleteErr: errBoom}, http.MethodDelete, "/v2/admin/tasks/t1"},
		{"control plane stats", &stubTaskAdmin{statsErr: errBoom}, http.MethodGet, "/v2/admin/control-plane"},
		{"control plane type counts", &stubTaskAdmin{countStateErr: errBoom}, http.MethodGet, "/v2/admin/control-plane"},
		{"control plane live tasks", &stubTaskAdmin{listLiveErr: errBoom}, http.MethodGet, "/v2/admin/control-plane"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &AdminHandler{taskAdmin: tc.admin, logger: discardLogger()}
			rec := serve(t, h, tc.method, tc.path)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestRetryTaskMapsRequeueSentinels pins the three-way split in RetryTask: the
// two sentinels that mean "requeueing this would be wrong" are 409 and carry
// their explanation, while an unrecognized failure is a 400. Asserting only
// "not 200" would let all three collapse onto one status.
func TestRetryTaskMapsRequeueSentinels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"leads nowhere", task.ErrRequeueLeadsNowhere, http.StatusConflict},
		{"unsafe to replay", task.ErrRequeueUnsafe, http.StatusConflict},
		{"unrecognized failure", errBoom, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			admin := &stubTaskAdmin{requeueErr: tc.err}
			h := &AdminHandler{taskAdmin: admin, logger: discardLogger()}
			rec := serve(t, h, http.MethodPost, "/v2/admin/tasks/t1/retry")
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			// A rejected retry must not be recorded as one.
			if len(admin.requeuedIDs) != 0 {
				t.Errorf("requeued %v despite the failure", admin.requeuedIDs)
			}
		})
	}
}

// --- audit log ---

func TestListAuditWithoutReader(t *testing.T) {
	t.Parallel()
	rec := serve(t, &AdminHandler{logger: discardLogger()}, http.MethodGet, "/v2/admin/audit")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 when the audit reader is unconfigured", rec.Code)
	}
}

// TestListAuditRejectsUnparsableTimesAtTheEdge documents WHERE the rejection
// happens. Both bounds carry format:"date-time", so Huma rejects a malformed
// value during request validation and answers 422, so the handler never runs.
//
// This is asserted rather than assumed because the obvious test (expect the
// handler's own 400) passes for the wrong reason: any non-2xx would satisfy it
// while the branch under test stayed unexecuted.
func TestListAuditRejectsUnparsableTimesAtTheEdge(t *testing.T) {
	t.Parallel()
	for _, param := range []string{"start_time", "end_time"} {
		t.Run(param, func(t *testing.T) {
			t.Parallel()
			reader := &stubAuditReader{}
			h := &AdminHandler{auditReader: reader, logger: discardLogger()}
			rec := serve(t, h, http.MethodGet, "/v2/admin/audit?"+param+"=not-a-timestamp")
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422 from Huma's format validation: %s", rec.Code, rec.Body.String())
			}
			if len(reader.queries) != 0 {
				t.Errorf("rejected input still reached the audit reader: %+v", reader.queries)
			}
		})
	}
}

// TestAuditFilterFromInputRejectsUnparsableTimes covers the handler's own parse
// guards, which the edge validation above makes unreachable over HTTP. They are
// the second line of defense: dropping format:"date-time" from either tag would
// hand an unparsed bound straight through, and these are what still catch it.
func TestAuditFilterFromInputRejectsUnparsableTimes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input *ListAuditInput
	}{
		{"start_time", &ListAuditInput{StartTime: "not-a-timestamp"}},
		{"end_time", &ListAuditInput{EndTime: "not-a-timestamp"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			filter, err := auditFilterFromInput(tc.input)
			if err == nil {
				t.Fatal("expected an error for an unparsable bound")
			}
			// The bound must be left unset rather than defaulted to the zero
			// time, which would silently widen the query instead of failing it.
			if filter.StartTime != nil || filter.EndTime != nil {
				t.Errorf("filter carries a time bound despite the parse failure: %+v", filter)
			}
		})
	}
}

// TestListAuditPropagatesFilterErrors calls the handler directly, below Huma's
// format validation, because that validation is what makes this branch
// unreachable over HTTP. It asserts the reader is never consulted: returning
// the filter error is only correct if the malformed query did not also run.
func TestListAuditPropagatesFilterErrors(t *testing.T) {
	t.Parallel()
	reader := &stubAuditReader{}
	h := &AdminHandler{auditReader: reader, logger: discardLogger()}

	out, err := h.ListAudit(t.Context(), &ListAuditInput{StartTime: "not-a-timestamp"})
	if err == nil {
		t.Fatal("expected an error for an unparsable start_time")
	}
	if out != nil {
		t.Errorf("returned output alongside the error: %+v", out)
	}
	if len(reader.queries) != 0 {
		t.Errorf("queried the audit log despite the bad filter: %+v", reader.queries)
	}
}

// TestAuditFilterFromInputAcceptsValidTimes is the counterfactual: without it,
// a parser that rejected everything would satisfy the test above.
func TestAuditFilterFromInputAcceptsValidTimes(t *testing.T) {
	t.Parallel()
	start := "2026-07-01T00:00:00Z"
	filter, err := auditFilterFromInput(&ListAuditInput{StartTime: start, EndTime: "2026-07-02T00:00:00Z"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filter.StartTime == nil || filter.EndTime == nil {
		t.Fatalf("bounds not set: %+v", filter)
	}
	if got := filter.StartTime.Format(time.RFC3339); got != start {
		t.Errorf("start_time = %s, want %s", got, start)
	}
}

func TestListAuditReaderFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		reader *stubAuditReader
	}{
		{"query fails", &stubAuditReader{queryErr: errBoom}},
		{"count fails", &stubAuditReader{countErr: errBoom}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &AdminHandler{auditReader: tc.reader, logger: discardLogger()}
			rec := serve(t, h, http.MethodGet, "/v2/admin/audit")
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// --- capacity lenses ---

// TestReclaimableDegradesPerFailedBucket pins the same log-and-continue contract
// as Stats: the four buckets are independent, so one failing read must empty its
// own bucket and leave the other three populated.
func TestReclaimableDegradesPerFailedBucket(t *testing.T) {
	t.Parallel()

	populated := func() (*stubDeviceLens, *stubHostLens) {
		return &stubDeviceLens{
				pinned: []*device.Device{{ID: "d-pinned"}},
				list:   []*device.Device{{ID: "d-stopped"}},
			}, &stubHostLens{
				list:     []*host.Host{{ID: "h-stopped"}},
				expiring: []*host.Host{{ID: "h-expiring"}},
			}
	}

	tests := []struct {
		name                                              string
		breakLens                                         func(*stubDeviceLens, *stubHostLens)
		wantPinned, wantStoppedDev, wantStoppedH, wantExp int
	}{
		{"all buckets read", func(*stubDeviceLens, *stubHostLens) {}, 1, 1, 1, 1},
		{"pinned devices fail", func(d *stubDeviceLens, _ *stubHostLens) { d.pinnedErr = errBoom; d.pinned = nil }, 0, 1, 1, 1},
		{"stopped devices fail", func(d *stubDeviceLens, _ *stubHostLens) { d.listErr = errBoom; d.list = nil }, 1, 0, 1, 1},
		{"stopped hosts fail", func(_ *stubDeviceLens, h *stubHostLens) { h.listErr = errBoom; h.list = nil }, 1, 1, 0, 1},
		{"expiring hosts fail", func(_ *stubDeviceLens, h *stubHostLens) { h.expiringErr = errBoom; h.expiring = nil }, 1, 1, 1, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dl, hl := populated()
			tc.breakLens(dl, hl)
			h := &AdminHandler{deviceLens: dl, hostLens: hl, logger: discardLogger()}

			rec := serve(t, h, http.MethodGet, "/v2/admin/reclaimable")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			var body struct {
				Total          int64             `json:"total"`
				PinnedDevices  []json.RawMessage `json:"pinned_devices"`
				StoppedDevices []json.RawMessage `json:"stopped_devices"`
				StoppedHosts   []json.RawMessage `json:"stopped_hosts"`
				ExpiringHosts  []json.RawMessage `json:"expiring_hosts"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := []int{len(body.PinnedDevices), len(body.StoppedDevices), len(body.StoppedHosts), len(body.ExpiringHosts)}
			want := []int{tc.wantPinned, tc.wantStoppedDev, tc.wantStoppedH, tc.wantExp}
			for i, name := range []string{"pinned_devices", "stopped_devices", "stopped_hosts", "expiring_hosts"} {
				if got[i] != want[i] {
					t.Errorf("%s = %d entries, want %d", name, got[i], want[i])
				}
			}
			// The KPI the page states comes from here, so a bucket that failed to
			// read has to lower it rather than leave a count nothing served.
			wantTotal := int64(tc.wantPinned + tc.wantStoppedDev + tc.wantStoppedH + tc.wantExp)
			if body.Total != wantTotal {
				t.Errorf("total = %d, want %d, the rows actually served", body.Total, wantTotal)
			}
		})
	}
}

func TestLensEndpointsWithoutLenses(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/v2/admin/reclaimable", "/v2/admin/provisioning-stats"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			rec := serve(t, &AdminHandler{logger: discardLogger()}, http.MethodGet, path)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500 when the lenses are unconfigured", rec.Code)
			}
		})
	}
}

// TestProvisioningStatsFailsClosed is the counterpart to the degrade tests: these
// numbers drive decisions, so a partial read must be an error rather than a
// silently smaller count.
func TestProvisioningStatsFailsClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		device *stubDeviceLens
		host   *stubHostLens
		reader *stubAuditReader
	}{
		{
			name:   "device outcome count fails",
			device: &stubDeviceLens{countErr: errBoom}, host: &stubHostLens{},
		},
		{
			name:   "host outcome count fails",
			device: &stubDeviceLens{}, host: &stubHostLens{countErr: errBoom},
		},
		{
			name:   "device create audit count fails",
			device: &stubDeviceLens{}, host: &stubHostLens{},
			reader: &stubAuditReader{countErrFor: map[audit.Action]error{audit.ActionDeviceCreate: errBoom}},
		},
		{
			name:   "host create audit count fails",
			device: &stubDeviceLens{}, host: &stubHostLens{},
			reader: &stubAuditReader{countErrFor: map[audit.Action]error{audit.ActionHostCreate: errBoom}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &AdminHandler{
				deviceLens: tc.device, hostLens: tc.host,
				auditReader: tc.reader, logger: discardLogger(),
			}
			rec := serve(t, h, http.MethodGet, "/v2/admin/provisioning-stats")
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestCountAuditedCreatesWrapsWithAction pins the wrapping, which is the only
// thing that tells the two identical-looking Count failures apart in a log.
func TestCountAuditedCreatesWrapsWithAction(t *testing.T) {
	t.Parallel()
	h := &AdminHandler{
		auditReader: &stubAuditReader{countErr: errBoom},
		logger:      discardLogger(),
	}
	_, err := h.countAuditedCreates(t.Context(), audit.ActionHostCreate, time.Now())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("cause not preserved: %v", err)
	}
	if want := fmt.Sprintf("counting %s audit events", audit.ActionHostCreate); !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not name the action (want it to contain %q)", err, want)
	}
}

// TestCountAuditedCreatesWithoutReaderReportsZero pins the deployment-without-
// audit-logging path: no reader is not an error, it is a zero the caller floors
// against surviving rows.
func TestCountAuditedCreatesWithoutReaderReportsZero(t *testing.T) {
	t.Parallel()
	h := &AdminHandler{logger: discardLogger()}
	n, err := h.countAuditedCreates(t.Context(), audit.ActionHostCreate, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
}

// --- control plane: the machinery's own reads ---

func TestControlPlaneLeaseFailureSurfacesAs500(t *testing.T) {
	t.Parallel()
	h := &AdminHandler{
		taskAdmin:  &stubTaskAdmin{},
		leaseAdmin: &stubLeaseAdmin{err: errBoom},
		logger:     discardLogger(),
	}
	rec := serve(t, h, http.MethodGet, "/v2/admin/control-plane")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
}

// --- capacity: every source it composes ---

// The capacity snapshot has no partial answer to give. A page whose numbers are
// all server-derived cannot render a board with one source silently missing, so
// any failed read is the page's error state.
func TestCapacityPlaneFailuresSurfaceAs500(t *testing.T) {
	t.Parallel()

	zone := "test-region-a"

	tests := []struct {
		name   string
		mutate func(*AdminHandler)
	}{
		{"zone listing fails", func(h *AdminHandler) {
			h.zoneCatalog = &stubZoneCatalog{zonesErr: errBoom}
			h.zoneFamily = &stubZoneFamily{}
		}},
		{"zone family census fails", func(h *AdminHandler) {
			h.zoneCatalog = &stubZoneCatalog{zones: []string{zone}}
			h.zoneFamily = &stubZoneFamily{err: errBoom}
		}},
		{"zone health cells fail", func(h *AdminHandler) {
			h.zoneCatalog = &stubZoneCatalog{zones: []string{zone}}
			h.zoneFamily = &stubZoneFamily{}
			h.zoneHealth = &stubZoneHealth{err: errBoom}
		}},
		{"the machine-type catalog fails", func(h *AdminHandler) {
			h.zoneCatalog = &stubZoneCatalog{zones: []string{zone}}
			h.zoneFamily = &stubZoneFamily{}
			h.machineTypes = &stubMachineCatalog{err: errBoom}
		}},
		{"the reserve margin read fails", func(h *AdminHandler) {
			h.placementPolicy = &stubPlacementPolicy{err: errBoom}
		}},
		{"spot fleet census fails", func(h *AdminHandler) {
			h.spotFleet = &stubSpotFleet{err: errBoom}
		}},
		{"spot advice cells fail", func(h *AdminHandler) {
			h.spotAdvice = &stubSpotAdvice{err: errBoom}
		}},
		{"preemption count fails", func(h *AdminHandler) {
			h.auditReader = &stubAuditReader{
				countErrFor: map[audit.Action]error{audit.ActionHostPreempted: errBoom},
			}
		}},
		{"recovery count fails", func(h *AdminHandler) {
			h.auditReader = &stubAuditReader{
				countErrFor: map[audit.Action]error{audit.ActionHostPreemptionRecovered: errBoom},
			}
		}},
		{"recovery sample read fails", func(h *AdminHandler) {
			h.auditReader = &stubAuditReader{
				counts:   map[audit.Action]int64{audit.ActionHostPreemptionRecovered: 2},
				queryErr: errBoom,
			}
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &AdminHandler{taskAdmin: &stubTaskAdmin{}, logger: discardLogger()}
			tc.mutate(h)
			rec := serve(t, h, http.MethodGet, "/v2/admin/capacity")
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500: %s", rec.Code, rec.Body.String())
			}
		})
	}
}
