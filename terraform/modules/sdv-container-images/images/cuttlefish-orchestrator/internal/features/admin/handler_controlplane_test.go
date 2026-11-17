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
	"slices"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

type fakeTaskAdmin struct {
	stats task.QueueStats
	live  []*task.Task
	byTS  []task.TypeStateCount
}

func (f *fakeTaskAdmin) ListFailed(ctx context.Context, limit int, resourceID string) ([]*task.Task, error) {
	return nil, nil
}

func (f *fakeTaskAdmin) ListLive(ctx context.Context, limit int) ([]*task.Task, error) {
	return f.live, nil
}

func (f *fakeTaskAdmin) CountByTypeState(ctx context.Context) ([]task.TypeStateCount, error) {
	return f.byTS, nil
}

func (f *fakeTaskAdmin) RequeueFailed(ctx context.Context, taskID string) error { return nil }
func (f *fakeTaskAdmin) DeleteTask(ctx context.Context, taskID string) error    { return nil }

func (f *fakeTaskAdmin) Stats(ctx context.Context) (task.QueueStats, error) {
	return f.stats, nil
}

type fakeLeaseAdmin struct {
	leases []spanner.LeaderLease
}

func (f *fakeLeaseAdmin) ListLeases(ctx context.Context) ([]spanner.LeaderLease, error) {
	return f.leases, nil
}

func TestControlPlaneSnapshot(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	leased := &task.Task{
		ID: "t1", Type: "host.provision", State: task.StateLeased,
		ResourceID: "host-1", Attempts: 1, MaxAttempts: 10,
		AvailableAt: now.Add(-time.Minute), LeaseExpiry: now.Add(30 * time.Minute),
		LeaseHolder: "inst-a", UpdatedAt: now,
	}
	pending := &task.Task{
		ID: "t2", Type: "device.reconcile", State: task.StatePending,
		ResourceID: "dev-1", Attempts: 2, MaxAttempts: 10, Priority: task.PriorityResync,
		AvailableAt: now.Add(45 * time.Second), LastError: "still tearing down",
		UpdatedAt: now,
	}
	taskAdmin := &fakeTaskAdmin{
		stats: task.QueueStats{Pending: 1, Leased: 1, Failed: 3, OldestPendingAge: 42 * time.Second},
		live:  []*task.Task{leased, pending},
		byTS: []task.TypeStateCount{
			{Type: "device.reconcile", State: task.StatePending, Count: 1},
			{Type: "host.provision", State: task.StateLeased, Count: 1},
		},
	}
	leaseAdmin := &fakeLeaseAdmin{leases: []spanner.LeaderLease{
		{
			Name: "hostreconciler", Holder: "inst-a", AcquiredAt: now.Add(-time.Hour),
			Expiry: now.Add(10 * time.Second), RenewedAt: now.Add(-3 * time.Second),
		},
		{Name: "reaper", RenewedAt: now.Add(-time.Minute)},
	}}

	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("Test API", "1.0.0"))
	h := &AdminHandler{taskAdmin: taskAdmin, leaseAdmin: leaseAdmin, workersPerInstance: 8}
	h.Register(api)

	req := httptest.NewRequest(http.MethodGet, "/v2/admin/control-plane", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Now        time.Time            `json:"now"`
		Queue      QueueDepth           `json:"queue"`
		TypeCounts []TypeStateCountItem `json:"type_counts"`
		LiveTasks  []LiveTaskItem       `json:"live_tasks"`
		Leaders    []LeaderLeaseItem    `json:"leaders"`
		Workers    int                  `json:"workers_per_instance"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Now.IsZero() {
		t.Error("now must be set")
	}
	if body.Queue.Pending != 1 || body.Queue.Leased != 1 || body.Queue.Failed != 3 {
		t.Errorf("queue = %+v", body.Queue)
	}
	if body.Queue.OldestPendingAgeSeconds != 42 {
		t.Errorf("oldest age = %d, want 42", body.Queue.OldestPendingAgeSeconds)
	}
	if body.Workers != 8 {
		t.Errorf("workers_per_instance = %d, want 8", body.Workers)
	}
	if len(body.TypeCounts) != 2 || body.TypeCounts[0].Type != "device.reconcile" {
		t.Errorf("type_counts = %+v", body.TypeCounts)
	}
	assertLiveTasks(t, body.LiveTasks)
	assertLeaders(t, body.Leaders)
}

func assertLiveTasks(t *testing.T, items []LiveTaskItem) {
	t.Helper()
	if len(items) != 2 {
		t.Fatalf("live_tasks = %+v", items)
	}
	gotLeased := items[0]
	if gotLeased.State != "leased" || gotLeased.LeaseHolder != "inst-a" || gotLeased.LeaseExpiry.IsZero() {
		t.Errorf("leased item = %+v", gotLeased)
	}
	gotPending := items[1]
	if gotPending.Priority != task.PriorityResync || gotPending.LastError != "still tearing down" {
		t.Errorf("pending item = %+v", gotPending)
	}
	if !gotPending.LeaseExpiry.IsZero() {
		t.Errorf("pending lease_expiry must be omitted/zero, got %v", gotPending.LeaseExpiry)
	}
}

func assertLeaders(t *testing.T, items []LeaderLeaseItem) {
	t.Helper()
	if len(items) != 2 {
		t.Fatalf("leaders = %+v", items)
	}
	if items[0].Name != "hostreconciler" || items[0].Holder != "inst-a" {
		t.Errorf("leader[0] = %+v", items[0])
	}
	if items[1].Holder != "" || !items[1].Expiry.IsZero() {
		t.Errorf("unheld leader must have empty holder and zero expiry: %+v", items[1])
	}
}

// The snapshot endpoint requires a configured task admin but tolerates a
// missing lease admin (leaders just come back empty).
func TestControlPlaneWithoutLeaseAdmin(t *testing.T) {
	t.Parallel()
	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("Test API", "1.0.0"))
	h := &AdminHandler{taskAdmin: &fakeTaskAdmin{}}
	h.Register(api)

	req := httptest.NewRequest(http.MethodGet, "/v2/admin/control-plane", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Leaders []LeaderLeaseItem `json:"leaders"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Leaders == nil || len(body.Leaders) != 0 {
		t.Errorf("leaders = %#v, want empty non-nil", body.Leaders)
	}
}

type fakeZoneCatalog struct {
	enabled  []string
	statuses map[string]gcecatalog.ZoneStatus
	quotas   map[string][]*gcecatalog.RegionalQuota
}

func (f *fakeZoneCatalog) ListEnabledZones(ctx context.Context) ([]string, error) {
	return f.enabled, nil
}

func (f *fakeZoneCatalog) Zones() map[string]gcecatalog.ZoneStatus {
	return f.statuses
}

func (f *fakeZoneCatalog) RegionalQuotas(region string) []*gcecatalog.RegionalQuota {
	return f.quotas[region]
}

type fakeZoneHealth struct {
	cells    []*gcecatalog.ZoneHealth
	cleared  []string
	clearErr error
	// capacityReads records one entry per ListCapacityCells call, holding the
	// families it was asked about. The ladder's whole statement budget is that
	// there is exactly one of them.
	capacityReads [][]string
}

func (f *fakeZoneHealth) ListCells(ctx context.Context) ([]*gcecatalog.ZoneHealth, error) {
	return f.cells, nil
}

func (f *fakeZoneHealth) ListCapacityCells(_ context.Context, machineFamilies []string) ([]*gcecatalog.ZoneHealth, error) {
	f.capacityReads = append(f.capacityReads, machineFamilies)
	capacity := make([]*gcecatalog.ZoneHealth, 0, len(f.cells))
	for _, cell := range f.cells {
		if cell.BindingMetric == gcecatalog.ZoneMetricCapacity &&
			slices.Contains(machineFamilies, cell.MachineFamily) {
			capacity = append(capacity, cell)
		}
	}
	return capacity, nil
}

func (f *fakeZoneHealth) ClearCell(_ context.Context, zone, machineFamily string) error {
	if f.clearErr != nil {
		return f.clearErr
	}
	f.cleared = append(f.cleared, zone+"/"+machineFamily)
	return nil
}

type fakeSpotFleet struct {
	counts []host.ModelStatusCount
}

func (f *fakeSpotFleet) CountByModelStatus(context.Context) ([]host.ModelStatusCount, error) {
	return f.counts, nil
}

type fakeSpotAdvice struct {
	cells []*gcecatalog.SpotAdvice
}

func (f *fakeSpotAdvice) ListCells(context.Context) ([]*gcecatalog.SpotAdvice, error) {
	return f.cells, nil
}

type fakeSpotAuditReader struct {
	counts  map[audit.Action]int64
	entries []audit.Entry
}

func (f *fakeSpotAuditReader) Count(_ context.Context, filter audit.ListFilter) (int64, error) {
	if filter.Action == nil {
		return 0, nil
	}
	return f.counts[*filter.Action], nil
}

func (f *fakeSpotAuditReader) Query(context.Context, audit.ListFilter) ([]audit.Entry, error) {
	return f.entries, nil
}

type fakeFleetPolicy struct {
	policy  hostconfig.FleetPolicy
	density int64
}

func (f *fakeFleetPolicy) GetFleetPolicy(context.Context) (hostconfig.FleetPolicy, error) {
	return f.policy, nil
}

func (f *fakeFleetPolicy) GetHostDensity(context.Context) (hostconfig.HostDensity, error) {
	return hostconfig.HostDensity{
		Effective:   f.density,
		MachineType: "n1-standard-8",
	}, nil
}

type fakeStandbyCounter struct{ standby, draining int64 }

func (f *fakeStandbyCounter) StandbyInventory(context.Context, time.Duration) (host.FleetInventory, error) {
	return host.FleetInventory{
		host.PoolDefault: {Standby: f.standby, Draining: f.draining},
	}, nil
}

// fakeDemandReader records the horizon each of its two reads was given. The card
// prints the miss split and the degradation ratio side by side, so they have to
// cover the same window. Otherwise it invites an operator to compare two
// populations that do not overlap, and nothing else would show that.
type fakeDemandReader struct {
	window      hostconfig.DemandWindow
	degradation hostconfig.GPUDegradation
	since       []time.Time
}

func (f *fakeDemandReader) Demand(_ context.Context, since time.Time) (hostconfig.DemandWindow, error) {
	f.since = append(f.since, since)
	return f.window, nil
}

func (f *fakeDemandReader) GPUDegradation(_ context.Context, since time.Time) (hostconfig.GPUDegradation, error) {
	f.since = append(f.since, since)
	return f.degradation, nil
}

// The autoscaler card: a target composed from the same inputs the keeper reads,
// every miss attributed, and the capacity the target stands for derived from the
// density a host is actually packed to rather than the policy's own number.
func TestControlPlaneFleetPanel(t *testing.T) {
	t.Parallel()
	now := time.Now()
	demand := &fakeDemandReader{
		window: hostconfig.DemandWindow{Misses: []hostconfig.Miss{
			{At: now.Add(-3 * time.Minute), GPURequired: new(false), FitsDefaultHost: new(true)},
			{At: now.Add(-2 * time.Minute), GPURequired: new(true), FitsDefaultHost: new(true)},
			{At: now.Add(-time.Minute), GPURequired: new(false), FitsDefaultHost: new(false)},
		}},
		degradation: hostconfig.GPUDegradation{Degraded: 4, Wanted: 10},
	}
	policy := hostconfig.DefaultFleetPolicy()
	policy.Tiers = map[string]hostconfig.FleetTier{hostconfig.DefaultTier: {Standby: 2}}

	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("Test API", "1.0.0"))
	h := &AdminHandler{
		taskAdmin:   &fakeTaskAdmin{},
		fleetPolicy: &fakeFleetPolicy{policy: policy, density: 3},
		standby:     &fakeStandbyCounter{standby: 1, draining: 1},
		demand:      demand,
	}
	h.Register(api)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v2/admin/control-plane", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Fleet FleetPanelItem `json:"fleet"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertFleetDemandSplit(t, body.Fleet)
	if body.Fleet.GPUDegradation != (hostconfig.GPUDegradation{Degraded: 4, Wanted: 10}) {
		t.Errorf("gpu_degradation = %+v, want 4 of 10", body.Fleet.GPUDegradation)
	}
	// The target is the standing intent (2), not the one servable miss, and the
	// slots are that target at the density a host is packed to.
	if body.Fleet.Status.Target != 2 || body.Fleet.DeviceSlots != 6 {
		t.Errorf("target/device_slots = %d/%d, want 2/6",
			body.Fleet.Status.Target, body.Fleet.DeviceSlots)
	}
	if len(demand.since) != 2 || !demand.since[0].Equal(demand.since[1]) {
		t.Errorf("the two demand reads covered %v, want one shared horizon", demand.since)
	}
}

func assertFleetDemandSplit(t *testing.T, panel FleetPanelItem) {
	t.Helper()
	want := hostconfig.DemandSplit{
		Servable: 1, Unservable: 2, GPURequired: 1, NeedsLargerHost: 1,
	}
	if panel.Status.Demand != want {
		t.Errorf("demand split = %+v, want %+v", panel.Status.Demand, want)
	}
	// The peak counts the servable miss alone; the split accounts for all three.
	// A card showing only the peak is the state this step exists to end.
	if panel.Status.Misses != 1 {
		t.Errorf("misses = %d, want 1, since only the servable miss composes a target", panel.Status.Misses)
	}
}
