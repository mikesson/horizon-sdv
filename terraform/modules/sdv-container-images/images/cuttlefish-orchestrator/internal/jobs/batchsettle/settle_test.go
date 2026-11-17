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

package batchsettle

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/batch"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

func TestJudgeAtomicBatch(t *testing.T) {
	t.Parallel()
	now := time.Now()
	fresh := &batch.Batch{RequestedCount: 4, CreatedAt: now.Add(-time.Minute)}
	stale := &batch.Batch{RequestedCount: 4, CreatedAt: now.Add(-atomicShortfallGrace - time.Minute)}
	timed := &batch.Batch{RequestedCount: 4, ReadyTimeout: 10 * time.Minute, CreatedAt: now.Add(-11 * time.Minute)}

	tests := []struct {
		name       string
		b          *batch.Batch
		m          memberCounts
		wantReady  bool
		wantReason bool
	}{
		{"full fill latches ready", fresh, memberCounts{live: 4, running: 4}, true, false},
		{"errored member fails", fresh, memberCounts{live: 4, running: 2, errored: 1}, false, true},
		{"converging waits", fresh, memberCounts{live: 4, running: 2}, false, false},
		{"timeout unfilled fails", timed, memberCounts{live: 4, running: 3}, false, true},
		{"shortfall inside grace waits", fresh, memberCounts{live: 2, running: 2}, false, false},
		{"shortfall past grace fails", stale, memberCounts{live: 2, running: 2}, false, true},
		// running == requested wins even on a batch past every deadline: the
		// judge sees the fill before it sees the clock.
		{"late full fill still latches", timed, memberCounts{live: 4, running: 4}, true, false},
		// Membership beyond the admitted target (pre-fingerprint rows only)
		// must settle ready, not orbit the sweep or trip a timeout while
		// every member runs.
		{"overfill still latches", timed, memberCounts{live: 6, running: 6}, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ready, reason := judgeAtomicBatch(tc.b, tc.m, now)
			if ready != tc.wantReady {
				t.Errorf("ready = %v, want %v", ready, tc.wantReady)
			}
			if (reason != "") != tc.wantReason {
				t.Errorf("reason = %q, want reason: %v", reason, tc.wantReason)
			}
		})
	}
}

// The fakes below are guarded and carry the guards their real counterparts
// commit under, because the loop runs on every instance: two of them sweeping
// one world is the case that has to converge, and a fake without the CAS or the
// already-deleted check would let it pass either way.

// fakeBatchStore is an in-memory Store with the real settle-CAS semantics
// (win once, never re-judge).
type fakeBatchStore struct {
	mu             sync.Mutex
	batches        map[string]*batch.Batch
	failedWithLive []string
	emptyIDs       []string
	deleted        []string
}

func newFakeBatchStore(batches ...*batch.Batch) *fakeBatchStore {
	m := map[string]*batch.Batch{}
	for _, b := range batches {
		m[b.ID] = b
	}
	return &fakeBatchStore{batches: m}
}

// Listings hand out copies, the way a Spanner read does: the caller judges a
// snapshot while the store keeps the row two instances race to settle.
func (f *fakeBatchStore) ListUnsettledAtomic(_ context.Context) ([]*batch.Batch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*batch.Batch
	for _, id := range slices.Sorted(maps.Keys(f.batches)) {
		b := f.batches[id]
		if b.FillPolicy == batch.FillAtomic && !b.Settled() {
			snapshot := *b
			out = append(out, &snapshot)
		}
	}
	return out, nil
}

func (f *fakeBatchStore) MarkReady(_ context.Context, id string, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.batches[id]
	if b.Settled() {
		return false, nil
	}
	b.ReadyAt = &at
	return true, nil
}

func (f *fakeBatchStore) MarkFailed(_ context.Context, id string, at time.Time, reason string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.batches[id]
	if b.Settled() {
		return false, nil
	}
	b.FailedAt = &at
	b.FailureReason = reason
	return true, nil
}

func (f *fakeBatchStore) ListFailedWithLiveMembers(_ context.Context, _ int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.failedWithLive), nil
}

func (f *fakeBatchStore) ListEmptyBefore(_ context.Context, _ time.Time, _ int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.emptyIDs), nil
}

func (f *fakeBatchStore) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	delete(f.batches, id)
	return nil
}

func (f *fakeBatchStore) collected() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deleted)
}

func (f *fakeBatchStore) present(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.batches[id]
	return ok
}

// fakeDevices serves members by batch id and records BatchDelete teardowns. A
// member already in teardown is skipped, mirroring deviceDeleteMutation's
// already-deleted check, the guard that makes a repeated teardown a no-op.
type fakeDevices struct {
	mu         sync.Mutex
	members    map[string][]*device.Device
	tornDownID []string
}

func (m *fakeDevices) List(_ context.Context, filter device.ListFilter) ([]*device.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if filter.BatchID == nil {
		return nil, nil
	}
	stored := m.members[*filter.BatchID]
	out := make([]*device.Device, 0, len(stored))
	for _, d := range stored {
		snapshot := *d
		out = append(out, &snapshot)
	}
	return out, nil
}

// BatchDelete transitions the STORED row, not the caller's snapshot: the second
// instance to arrive with a stale copy must still find the row already gone.
func (m *fakeDevices) BatchDelete(_ context.Context, devices []*device.Device) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var deleted int
	for _, d := range devices {
		stored := m.row(d.ID)
		if stored == nil || stored.Status == device.StatusDeleted {
			continue
		}
		m.tornDownID = append(m.tornDownID, stored.ID)
		stored.Status = device.StatusDeleted
		deleted++
	}
	return deleted, nil
}

// row finds a stored member by id; the caller holds mu.
func (m *fakeDevices) row(id string) *device.Device {
	for _, members := range m.members {
		for _, d := range members {
			if d.ID == id {
				return d
			}
		}
	}
	return nil
}

func (m *fakeDevices) tornDown() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.tornDownID)
}

// fakeHosts records host deletions, no-oping a repeat the way
// errHostAlreadyDeleting does.
type fakeHosts struct {
	mu         sync.Mutex
	deletedIDs []string
}

func (m *fakeHosts) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if slices.Contains(m.deletedIDs, id) {
		return nil
	}
	m.deletedIDs = append(m.deletedIDs, id)
	return nil
}

func (m *fakeHosts) deleted() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.deletedIDs)
}

// fakeDrained serves the drained-batch-host listing.
type fakeDrained struct {
	empty []*host.Host
}

func (m *fakeDrained) ListEmptyBatchHosts(_ context.Context, _ time.Time, _ int) ([]*host.Host, error) {
	return m.empty, nil
}

func member(id, batchID string, status device.Status) *device.Device {
	return &device.Device{ID: id, BatchID: batchID, Status: status}
}

func newService(store *fakeBatchStore, devices *fakeDevices, hosts *fakeHosts, drained *fakeDrained, opts ...Option) *Service {
	return New(Config{Enabled: true, BatchSize: 10},
		Deps{Store: store, Devices: devices, Hosts: hosts, HostLister: drained},
		slog.Default(), opts...)
}

func TestSweepAtomicBatches(t *testing.T) {
	t.Parallel()
	filled := &batch.Batch{ID: "b-filled", RequestedCount: 2, FillPolicy: batch.FillAtomic, CreatedAt: time.Now()}
	doomed := &batch.Batch{ID: "b-doomed", RequestedCount: 2, FillPolicy: batch.FillAtomic, CreatedAt: time.Now()}
	waiting := &batch.Batch{ID: "b-waiting", RequestedCount: 2, FillPolicy: batch.FillAtomic, CreatedAt: time.Now()}
	store := newFakeBatchStore(filled, doomed, waiting)
	devices := &fakeDevices{members: map[string][]*device.Device{
		"b-filled": {member("d1", "b-filled", device.StatusRunning), member("d2", "b-filled", device.StatusRunning)},
		"b-doomed": {member("d3", "b-doomed", device.StatusRunning), member("d4", "b-doomed", device.StatusError)},
		// One member torn down mid-fill: excluded from live, still converging
		// on the other, so the batch waits for grace/timeout, not an instant
		// verdict.
		"b-waiting": {member("d5", "b-waiting", device.StatusCreating), member("d6", "b-waiting", device.StatusDeleting)},
	}}
	svc := newService(store, devices, &fakeHosts{}, &fakeDrained{})

	settled, failed := svc.sweepAtomicBatches(t.Context(), false)
	if failed != 0 {
		t.Fatalf("sweep failures = %d, want 0", failed)
	}
	if settled != 2 {
		t.Errorf("settled = %d, want 2 (filled + doomed)", settled)
	}
	if filled.ReadyAt == nil {
		t.Error("filled batch not latched ready")
	}
	if doomed.FailedAt == nil || doomed.FailureReason == "" {
		t.Errorf("doomed batch not tripped: %+v", doomed)
	}
	if waiting.Settled() {
		t.Errorf("waiting batch judged early: %+v", waiting)
	}
	if want := []string{"d3", "d4"}; !slices.Equal(devices.tornDownID, want) {
		t.Errorf("teardown = %v, want %v (only the failed batch's members)", devices.tornDownID, want)
	}

	// A second sweep re-judges nothing: the settle CAS latched.
	devices.tornDownID = nil
	if settled, _ := svc.sweepAtomicBatches(t.Context(), false); settled != 0 {
		t.Errorf("second sweep settled %d, want 0", settled)
	}
	if len(devices.tornDownID) != 0 {
		t.Errorf("second sweep tore down %v, want none", devices.tornDownID)
	}
}

// A tripped batch whose winner died before finishing the member deletes is
// picked up by the resume arm: the settle CAS never re-judges it, so this
// level check is what keeps its members from burning capacity until their
// own TTLs.
func TestResumeFailedTeardowns(t *testing.T) {
	t.Parallel()
	store := newFakeBatchStore()
	store.failedWithLive = []string{"b-orphaned"}
	devices := &fakeDevices{members: map[string][]*device.Device{
		"b-orphaned": {
			member("d1", "b-orphaned", device.StatusRunning),
			member("d2", "b-orphaned", device.StatusCreating),
		},
	}}
	svc := newService(store, devices, &fakeHosts{}, &fakeDrained{})

	resumed, failed := svc.resumeFailedTeardowns(t.Context(), false)
	if resumed != 1 || failed != 0 {
		t.Fatalf("resume = (%d, %d), want (1, 0)", resumed, failed)
	}
	if want := []string{"d1", "d2"}; !slices.Equal(devices.tornDownID, want) {
		t.Errorf("teardown = %v, want %v", devices.tornDownID, want)
	}

	// Dry run only reports.
	store.failedWithLive = []string{"b-orphaned-2"}
	devices.members["b-orphaned-2"] = []*device.Device{member("d3", "b-orphaned-2", device.StatusRunning)}
	dry := newService(store, devices, &fakeHosts{}, &fakeDrained{})
	if resumed, _ := dry.resumeFailedTeardowns(t.Context(), true); resumed != 0 {
		t.Errorf("dry run resumed %d, want 0", resumed)
	}
	if slices.Contains(devices.tornDownID, "d3") {
		t.Error("dry run must not tear members down")
	}
}

func TestCollectEmptyBatches(t *testing.T) {
	t.Parallel()
	store := newFakeBatchStore()
	store.emptyIDs = []string{"b-old-1", "b-old-2"}
	svc := newService(store, &fakeDevices{}, &fakeHosts{}, &fakeDrained{})

	collected, failed := svc.collectEmptyBatches(t.Context(), false)
	if collected != 2 || failed != 0 {
		t.Errorf("collect = (%d, %d), want (2, 0)", collected, failed)
	}
	if want := []string{"b-old-1", "b-old-2"}; !slices.Equal(store.deleted, want) {
		t.Errorf("deleted = %v, want %v", store.deleted, want)
	}
}

func TestReapDrainedHosts(t *testing.T) {
	t.Parallel()
	hosts := &fakeHosts{}
	drained := &fakeDrained{empty: []*host.Host{
		{ID: "h-drained-1", BatchID: "b1"},
		{ID: "h-drained-2", BatchID: "b1"},
	}}
	svc := newService(newFakeBatchStore(), &fakeDevices{}, hosts, drained)

	reaped, failed := svc.reapDrainedHosts(t.Context(), false)
	if reaped != 2 || failed != 0 {
		t.Errorf("reap = (%d, %d), want (2, 0)", reaped, failed)
	}
	if want := []string{"h-drained-1", "h-drained-2"}; !slices.Equal(hosts.deletedIDs, want) {
		t.Errorf("deleted hosts = %v, want %v", hosts.deletedIDs, want)
	}
}

// The loop's lifecycle: one immediate cycle at Start, on every instance with
// nothing to gate it, and Stop joins.
func TestServiceLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeBatchStore()
		store.emptyIDs = []string{"b-old"}
		svc := newService(store, &fakeDevices{}, &fakeHosts{}, &fakeDrained{})
		svc.Start(t.Context())
		synctest.Wait()
		if want := []string{"b-old"}; !slices.Equal(store.collected(), want) {
			t.Errorf("immediate cycle deleted %v, want %v", store.collected(), want)
		}
		svc.Stop()
	})
}

// TestConcurrentInstancesSettleOnce is what replaces the leader gate. Two
// instances judge one world at once and every arm lands once: the settle CAS
// picks a single winner per batch, the winner alone tears the members down, a
// drained host is deprovisioned once, and a collected batch row ends up gone.
//
// It goes red if failAtomicBatch stops honoring the CAS result: both instances
// then count and own the same trip, and the tripped total doubles.
func TestConcurrentInstancesSettleOnce(t *testing.T) {
	t.Parallel()
	const batches = 6
	store := newFakeBatchStore()
	devices := &fakeDevices{members: map[string][]*device.Device{}}
	wantTornDown := make([]string, 0, 2*batches)
	for i := range batches {
		id := fmt.Sprintf("b-doomed-%02d", i)
		store.batches[id] = &batch.Batch{
			ID: id, RequestedCount: 2, FillPolicy: batch.FillAtomic, CreatedAt: time.Now(),
		}
		dev := fmt.Sprintf("d-%02d", i)
		devices.members[id] = []*device.Device{
			member(dev+"-a", id, device.StatusRunning),
			member(dev+"-b", id, device.StatusError),
		}
		wantTornDown = append(wantTornDown, dev+"-a", dev+"-b")

		empty := fmt.Sprintf("b-empty-%02d", i)
		store.batches[empty] = &batch.Batch{ID: empty, FillPolicy: batch.FillBestEffort, CreatedAt: time.Now()}
		store.emptyIDs = append(store.emptyIDs, empty)
	}
	hosts := &fakeHosts{}
	drained := &fakeDrained{empty: []*host.Host{
		{ID: "h-drained-1", BatchID: "b1"}, {ID: "h-drained-2", BatchID: "b1"},
	}}

	instances := make([]*Service, 2)
	for i := range instances {
		instances[i] = newService(store, devices, hosts, drained)
	}
	var wg sync.WaitGroup
	for _, svc := range instances {
		wg.Go(func() { _ = svc.settle(t.Context()) })
	}
	wg.Wait()

	tornDown := devices.tornDown()
	slices.Sort(tornDown)
	slices.Sort(wantTornDown)
	if !slices.Equal(tornDown, wantTornDown) {
		t.Errorf("teardown = %v, want each member exactly once: %v", tornDown, wantTornDown)
	}
	if want := []string{"h-drained-1", "h-drained-2"}; !slices.Equal(hosts.deleted(), want) {
		t.Errorf("drained hosts deprovisioned = %v, want %v", hosts.deleted(), want)
	}
	for i := range batches {
		if id := fmt.Sprintf("b-empty-%02d", i); store.present(id) {
			t.Errorf("empty batch %s survived collection", id)
		}
	}
	var tripped int64
	for _, svc := range instances {
		tripped += svc.totalTripped.Load()
		if got := svc.totalFailures.Load(); got != 0 {
			t.Errorf("an instance reported %d failures; losing a settle race is not a failure", got)
		}
	}
	if tripped != batches {
		t.Errorf("%d batches counted as tripped across both instances, want %d", tripped, batches)
	}
}
