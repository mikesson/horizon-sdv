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

package reaper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/device/devicetest"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
)

// reaperDeviceRepo embeds the shared device fake and adds the reaper's narrow
// ListExpired view: every stored device counts as expired. ListErr on the
// embedded fake also fails ListExpired.
type reaperDeviceRepo struct {
	*devicetest.FakeRepo
	// CountErr fails the sizing read, which must leave the batch floor standing
	// rather than fail the cycle.
	CountErr error
}

func newReaperDeviceRepo(devices ...*device.Device) *reaperDeviceRepo {
	return &reaperDeviceRepo{FakeRepo: devicetest.NewFakeRepo(devices...)}
}

// CountUndeleted is the population the reaper sizes its device sweeps against;
// every stored device counts, matching ListExpired above.
func (m *reaperDeviceRepo) CountUndeleted(_ context.Context) (int64, error) {
	if m.CountErr != nil {
		return 0, m.CountErr
	}
	return int64(len(m.Devices)), nil
}

func (m *reaperDeviceRepo) ListExpired(_ context.Context, limit int) ([]*device.Device, error) {
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	out := make([]*device.Device, 0, min(limit, len(m.Devices)))
	for _, id := range slices.Sorted(maps.Keys(m.Devices)) {
		if len(out) == limit {
			break
		}
		out = append(out, m.Devices[id])
	}
	return out, nil
}

func (m *reaperDeviceRepo) ListLapsedPins(_ context.Context, limit int) ([]*device.Device, error) {
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	out := make([]*device.Device, 0, limit)
	for _, id := range slices.Sorted(maps.Keys(m.Devices)) {
		d := m.Devices[id]
		if !d.Lifecycle.IsPinned() || d.Lifecycle.PinnedUntil == nil || d.Lifecycle.PinnedUntil.After(time.Now()) {
			continue
		}
		if len(out) == limit {
			break
		}
		out = append(out, d)
	}
	return out, nil
}

// reaperHostRepo is the host counterpart of reaperDeviceRepo.
type reaperHostRepo struct {
	*hosttest.FakeRepo
	CountErr error
}

func newReaperHostRepo(hosts ...*host.Host) *reaperHostRepo {
	return &reaperHostRepo{FakeRepo: hosttest.NewFakeRepo(hosts...)}
}

func (m *reaperHostRepo) CountUndeleted(_ context.Context) (int64, error) {
	if m.CountErr != nil {
		return 0, m.CountErr
	}
	return int64(len(m.Hosts)), nil
}

func (m *reaperHostRepo) ListExpired(_ context.Context, limit int) ([]*host.Host, error) {
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	out := make([]*host.Host, 0, min(limit, len(m.Hosts)))
	for _, id := range slices.Sorted(maps.Keys(m.Hosts)) {
		if len(out) == limit {
			break
		}
		out = append(out, m.Hosts[id])
	}
	return out, nil
}

// mockDeviceService records what the reaper did. Guarded, because a sweep now
// acts on its items concurrently, and an unguarded recorder here would report
// a data race rather than a reaper bug.
type mockDeviceService struct {
	mu          sync.Mutex
	deleteErr   error
	deletedIDs  []string
	unpinErr    error
	unpinnedIDs []string
	// delay holds each act, so a test can tell a bounded fan-out from a serial
	// loop by how long the sweep takes.
	delay time.Duration
}

func (m *mockDeviceService) Delete(_ context.Context, id string) error {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deletedIDs = append(m.deletedIDs, id)
	return nil
}

func (m *mockDeviceService) UnpinLapsed(_ context.Context, id, _ string) (*device.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unpinErr != nil {
		return nil, m.unpinErr
	}
	m.unpinnedIDs = append(m.unpinnedIDs, id)
	return nil, nil
}

func (m *mockDeviceService) deleted() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.deletedIDs)
}

func (m *mockDeviceService) unpinned() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.unpinnedIDs)
}

// mockHostService records host deletions (the only HostReaper call), guarded
// for the reason mockDeviceService is.
type mockHostService struct {
	mu         sync.Mutex
	deleteErr  error
	deletedIDs []string
}

func (m *mockHostService) DeleteExpired(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deletedIDs = append(m.deletedIDs, id)
	return nil
}

func (m *mockHostService) deleted() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.deletedIDs)
}

// convergentState is one world two reaper instances sweep at once, carrying the
// guards the real services commit under. Without them a double-run proves
// nothing. A device delete that already transitioned no-ops (the already-deleted
// check in deviceDeleteMutation), an unpin of an unpinned device returns
// ErrInvalidState (DeviceService.UnpinLapsed's check, inside the transaction that
// writes), and an already-deleting host no-ops (errHostAlreadyDeleting). It
// counts calls beside transitions so a test can tell "the guard held" from "only
// one instance ever got there".
type convergentState struct {
	mu                sync.Mutex
	pinned            map[string]bool
	unpinCalls        map[string]int
	unpinWins         map[string]int
	deviceCalls       map[string]int
	deviceTransitions map[string]int
	hostCalls         map[string]int
	hostTransitions   map[string]int
}

func newConvergentState(devices []*device.Device) *convergentState {
	s := &convergentState{
		pinned:            map[string]bool{},
		unpinCalls:        map[string]int{},
		unpinWins:         map[string]int{},
		deviceCalls:       map[string]int{},
		deviceTransitions: map[string]int{},
		hostCalls:         map[string]int{},
		hostTransitions:   map[string]int{},
	}
	for _, d := range devices {
		if d.Lifecycle.IsPinned() {
			s.pinned[d.ID] = true
		}
	}
	return s
}

// actionCount separates how often an action was attempted on one row from how
// often it actually changed anything. A concurrent sweep has to be judged on
// the two numbers together.
type actionCount struct{ calls, transitions int }

func (s *convergentState) deviceDeletes(id string) actionCount {
	s.mu.Lock()
	defer s.mu.Unlock()
	return actionCount{s.deviceCalls[id], s.deviceTransitions[id]}
}

func (s *convergentState) hostDeletes(id string) actionCount {
	s.mu.Lock()
	defer s.mu.Unlock()
	return actionCount{s.hostCalls[id], s.hostTransitions[id]}
}

func (s *convergentState) unpins(id string) actionCount {
	s.mu.Lock()
	defer s.mu.Unlock()
	return actionCount{s.unpinCalls[id], s.unpinWins[id]}
}

func (s *convergentState) totalUnpins() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int
	for _, n := range s.unpinWins {
		total += n
	}
	return total
}

// assertActedOnce is the per-row verdict of a concurrent sweep: every instance
// reached the row (without which the run proves nothing) and exactly one
// transition came of it.
func assertActedOnce(t *testing.T, what string, instances int, got actionCount) {
	t.Helper()
	if got.calls != instances {
		t.Errorf("%s saw %d calls, want one per instance (%d)", what, got.calls, instances)
	}
	if got.transitions != 1 {
		t.Errorf("%s transitioned %d times, want 1", what, got.transitions)
	}
}

// convergentDevices is the DeviceReaper half of convergentState.
type convergentDevices struct{ state *convergentState }

func (c convergentDevices) Delete(_ context.Context, id string) error {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.deviceCalls[id]++
	if c.state.deviceTransitions[id] == 0 {
		c.state.deviceTransitions[id]++
	}
	return nil
}

func (c convergentDevices) UnpinLapsed(_ context.Context, id, _ string) (*device.Device, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.unpinCalls[id]++
	if !c.state.pinned[id] {
		return nil, fmt.Errorf("device not pinned: %w", device.ErrInvalidState)
	}
	c.state.pinned[id] = false
	c.state.unpinWins[id]++
	return nil, nil
}

// convergentHosts is the HostReaper half of convergentState.
type convergentHosts struct{ state *convergentState }

func (c convergentHosts) DeleteExpired(_ context.Context, id string) error {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.hostCalls[id]++
	if c.state.hostTransitions[id] == 0 {
		c.state.hostTransitions[id]++
	}
	return nil
}

// Tests

func TestService_StartStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deviceRepo := newReaperDeviceRepo(&device.Device{ID: "device-1", Status: device.StatusRunning})
		hostRepo := newReaperHostRepo()
		deviceSvc := &mockDeviceService{}
		hostSvc := &mockHostService{}
		logger := slog.Default()

		config := Config{
			Enabled:   true,
			Interval:  100 * time.Millisecond,
			BatchSize: 10,
		}

		svc := New(config, Deps{DeviceService: deviceSvc, HostService: hostSvc, DeviceRepo: deviceRepo, HostRepo: hostRepo}, logger)

		ctx := t.Context()
		svc.Start(ctx)
		// Start again should be no-op: the double Start must not double cycles.
		svc.Start(ctx)

		time.Sleep(10 * time.Millisecond)
		synctest.Wait()
		if got := len(deviceSvc.deleted()); got != 1 {
			t.Errorf("deleted after immediate cycle = %d, want 1 (Start ran one cycle, idempotently)", got)
		}

		// Stop joins the loop; a second Stop is a no-op.
		svc.Stop()
		svc.Stop()
	})
}

func TestService_DisabledDoesNotRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deviceRepo := newReaperDeviceRepo()
		hostRepo := newReaperHostRepo()
		deviceSvc := &mockDeviceService{}
		hostSvc := &mockHostService{}
		logger := slog.Default()

		config := Config{
			Enabled:   false,
			Interval:  100 * time.Millisecond,
			BatchSize: 10,
		}

		svc := New(config, Deps{DeviceService: deviceSvc, HostService: hostSvc, DeviceRepo: deviceRepo, HostRepo: hostRepo}, logger)

		ctx := t.Context()
		svc.Start(ctx)

		// Wait for potential reap cycles - time advances automatically
		time.Sleep(200 * time.Millisecond)

		// Nothing should have been deleted since it's disabled
		if len(deviceSvc.deleted()) != 0 {
			t.Errorf("expected 0 deleted devices, got %d", len(deviceSvc.deleted()))
		}

		svc.Stop()
	})
}

func TestService_ReapsExpiredDevices(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deviceRepo := newReaperDeviceRepo(
			&device.Device{ID: "device-1", Status: device.StatusRunning},
			&device.Device{ID: "device-2", Status: device.StatusRunning},
		)
		hostRepo := newReaperHostRepo()
		deviceSvc := &mockDeviceService{}
		hostSvc := &mockHostService{}
		logger := slog.Default()

		config := Config{
			Enabled:   true,
			Interval:  1 * time.Second,
			BatchSize: 10,
			DryRun:    false,
		}

		svc := New(config, Deps{DeviceService: deviceSvc, HostService: hostSvc, DeviceRepo: deviceRepo, HostRepo: hostRepo}, logger)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		svc.Start(ctx)

		// Wait for initial reap cycle - synctest advances time automatically
		time.Sleep(50 * time.Millisecond)

		svc.Stop()

		// Should have deleted the expired devices
		if len(deviceSvc.deleted()) < 2 {
			t.Errorf("expected at least 2 deleted devices, got %d", len(deviceSvc.deleted()))
		}
	})
}

// TestService_ConcurrentInstancesActOnce is what replaces the leader gate. Two
// instances sweep one world at once, both act on every row (the call counts say
// so, which is what keeps this from passing vacuously), and each row still
// transitions once with nothing reported as a failure.
//
// What it can catch is on the reaper's side of the boundary: drop the
// ErrInvalidState tolerate from unpinLapsedPins and the loser of every unpin
// race is counted a failure. The transitions themselves are the services'
// guards, modeled here and verified against Spanner in the repository suites.
func TestService_ConcurrentInstancesActOnce(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-time.Hour)
	const population = 8
	devices := make([]*device.Device, 0, population)
	pinnedIDs := make([]string, 0, population/2)
	for i := range population {
		d := &device.Device{ID: fmt.Sprintf("device-%02d", i), Status: device.StatusRunning}
		if i%2 == 0 {
			d.Lifecycle.Pin("user@example.com", device.PinReasonOvernightTest, "", &past)
			pinnedIDs = append(pinnedIDs, d.ID)
		}
		devices = append(devices, d)
	}
	hosts := []*host.Host{
		{ID: "host-1", Status: host.StatusRunning},
		{ID: "host-2", Status: host.StatusRunning},
		{ID: "host-3", Status: host.StatusRunning},
	}
	state := newConvergentState(devices)

	instances := make([]*Service, 2)
	for i := range instances {
		instances[i] = New(Config{Enabled: true, BatchSize: 10},
			Deps{
				DeviceService: convergentDevices{state}, HostService: convergentHosts{state},
				DeviceRepo: newReaperDeviceRepo(devices...), HostRepo: newReaperHostRepo(hosts...),
			},
			slog.Default())
	}

	var wg sync.WaitGroup
	for _, svc := range instances {
		wg.Go(func() { _ = svc.reap(t.Context()) })
	}
	wg.Wait()

	for _, d := range devices {
		assertActedOnce(t, "delete of device "+d.ID, len(instances), state.deviceDeletes(d.ID))
	}
	for _, id := range pinnedIDs {
		assertActedOnce(t, "unpin of device "+id, len(instances), state.unpins(id))
	}
	for _, h := range hosts {
		assertActedOnce(t, "delete of host "+h.ID, len(instances), state.hostDeletes(h.ID))
	}
	var failures int64
	for _, svc := range instances {
		failures += svc.totalFailures.Load()
	}
	if failures != 0 {
		t.Errorf("two instances sweeping at once reported %d failures; losing a race is not a failure", failures)
	}
	if got := state.totalUnpins(); got != len(pinnedIDs) {
		t.Errorf("%d pins lapsed across both instances, want %d", got, len(pinnedIDs))
	}
}

func TestService_ReapsExpiredHosts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deviceRepo := newReaperDeviceRepo()
		hostRepo := newReaperHostRepo(
			&host.Host{ID: "host-1", Status: host.StatusRunning},
			&host.Host{ID: "host-2", Status: host.StatusRunning},
		)
		deviceSvc := &mockDeviceService{}
		hostSvc := &mockHostService{}
		logger := slog.Default()

		config := Config{
			Enabled:   true,
			Interval:  1 * time.Second,
			BatchSize: 10,
			DryRun:    false,
		}

		svc := New(config, Deps{DeviceService: deviceSvc, HostService: hostSvc, DeviceRepo: deviceRepo, HostRepo: hostRepo}, logger)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		svc.Start(ctx)

		// Wait for reaper to run - synctest handles time advancement
		time.Sleep(100 * time.Millisecond)

		svc.Stop()

		// Should have deleted the expired hosts
		if len(hostSvc.deleted()) < 2 {
			t.Errorf("expected at least 2 deleted hosts, got %d", len(hostSvc.deleted()))
		}
	})
}

func TestService_DryRunDoesNotDelete(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deviceRepo := newReaperDeviceRepo(&device.Device{ID: "device-1", Status: device.StatusRunning})
		hostRepo := newReaperHostRepo(&host.Host{ID: "host-1", Status: host.StatusRunning})
		deviceSvc := &mockDeviceService{}
		hostSvc := &mockHostService{}
		logger := slog.Default()

		config := Config{
			Enabled:   true,
			Interval:  50 * time.Millisecond,
			BatchSize: 10,
			DryRun:    true, // Dry run enabled
		}

		svc := New(config, Deps{DeviceService: deviceSvc, HostService: hostSvc, DeviceRepo: deviceRepo, HostRepo: hostRepo}, logger)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		svc.Start(ctx)

		// Wait for reaper to run
		time.Sleep(100 * time.Millisecond)

		svc.Stop()

		// Nothing should have been deleted in dry run mode
		if len(deviceSvc.deleted()) != 0 {
			t.Errorf("expected 0 deleted devices in dry run, got %d", len(deviceSvc.deleted()))
		}
		if len(hostSvc.deleted()) != 0 {
			t.Errorf("expected 0 deleted hosts in dry run, got %d", len(hostSvc.deleted()))
		}
	})
}

func TestService_BatchSize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Create more devices than batch size
		devices := make([]*device.Device, 100)
		for i := range devices {
			devices[i] = &device.Device{ID: "device-" + string(rune('a'+i))}
		}

		deviceRepo := newReaperDeviceRepo(devices...)
		hostRepo := newReaperHostRepo()
		deviceSvc := &mockDeviceService{}
		hostSvc := &mockHostService{}
		logger := slog.Default()

		config := Config{
			Enabled:   true,
			Interval:  1 * time.Second, // Long interval to run only once
			BatchSize: 10,              // Only process 10 at a time
			DryRun:    false,
		}

		svc := New(config, Deps{DeviceService: deviceSvc, HostService: hostSvc, DeviceRepo: deviceRepo, HostRepo: hostRepo}, logger)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		svc.Start(ctx)

		// Wait for initial reap cycle
		time.Sleep(50 * time.Millisecond)

		svc.Stop()

		// Should have deleted exactly batch size devices (only one cycle ran)
		if len(deviceSvc.deleted()) != 10 {
			t.Errorf("expected 10 deleted devices (batch size), got %d", len(deviceSvc.deleted()))
		}
	})
}

// TestService_BatchScalesWithPopulation is the proportional bound: past a few
// thousand rows the configured batch stops being the limit and a share of the
// population takes over, so the sweep keeps up with the expiry rate the same
// population implies.
//
// It fails if the listing bound is ever wired back to Config.BatchSize alone,
// because then only ten of the sixteen hundred would go.
func TestService_BatchScalesWithPopulation(t *testing.T) {
	t.Parallel()
	devices := make([]*device.Device, 3200)
	for i := range devices {
		devices[i] = &device.Device{ID: fmt.Sprintf("device-%04d", i)}
	}
	deviceSvc := &mockDeviceService{}
	svc := New(Config{Enabled: true, BatchSize: 10},
		Deps{
			DeviceService: deviceSvc, HostService: &mockHostService{},
			DeviceRepo: newReaperDeviceRepo(devices...), HostRepo: newReaperHostRepo(),
		},
		slog.Default())

	_ = svc.reap(t.Context())

	if got := len(deviceSvc.deleted()); got != 3200/reapFleetShare {
		t.Errorf("one cycle reaped %d of 3200 devices, want %d (a %dth of the population, past the floor of 10)",
			got, 3200/reapFleetShare, reapFleetShare)
	}
}

// TestService_SizingFailureKeepsTheFloor pins the fail-safe direction: sizing
// only ever WIDENS the bound, so a cycle that could not read the population
// still reaps at the rate it reaped before the sizing existed.
func TestService_SizingFailureKeepsTheFloor(t *testing.T) {
	t.Parallel()
	devices := make([]*device.Device, 3200)
	for i := range devices {
		devices[i] = &device.Device{ID: fmt.Sprintf("device-%04d", i)}
	}
	repo := newReaperDeviceRepo(devices...)
	repo.CountErr = errors.New("count failed")
	deviceSvc := &mockDeviceService{}
	svc := New(Config{Enabled: true, BatchSize: 10},
		Deps{
			DeviceService: deviceSvc, HostService: &mockHostService{},
			DeviceRepo: repo, HostRepo: newReaperHostRepo(),
		},
		slog.Default())

	_ = svc.reap(t.Context())

	if got := len(deviceSvc.deleted()); got != 10 {
		t.Errorf("a failed sizing read reaped %d, want the floor of 10", got)
	}
}

// TestService_SweepActsConcurrently measures the fan-out the only way that can
// fail: by how long the sweep takes. Sixteen items each holding a second, under
// a bound of eight, is two waves, where a serial loop would be sixteen.
//
// It fails if the group's limit is dropped to one, or if the group is replaced
// by a serial loop again.
func TestService_SweepActsConcurrently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		items := reapConcurrency * 2
		devices := make([]*device.Device, items)
		for i := range devices {
			devices[i] = &device.Device{ID: fmt.Sprintf("device-%02d", i)}
		}
		deviceSvc := &mockDeviceService{delay: time.Second}
		svc := New(Config{Enabled: true, BatchSize: items},
			Deps{
				DeviceService: deviceSvc, HostService: &mockHostService{},
				DeviceRepo: newReaperDeviceRepo(devices...), HostRepo: newReaperHostRepo(),
			},
			slog.Default())

		start := time.Now()
		_ = svc.reap(t.Context())
		elapsed := time.Since(start)

		if got := len(deviceSvc.deleted()); got != items {
			t.Fatalf("reaped %d of %d devices", got, items)
		}
		if want := 2 * time.Second; elapsed != want {
			t.Errorf("sweeping %d items at a bound of %d took %v, want %v",
				items, reapConcurrency, elapsed, want)
		}
	})
}

// TestService_DryRunSurvivesTheFanOut holds the dry-run contract across the
// change that made the acting loop concurrent: nothing is called, nothing is
// counted, and no goroutine is spawned to call it.
func TestService_DryRunSurvivesTheFanOut(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-time.Hour)
	lapsed := &device.Device{ID: "lapsed", Status: device.StatusRunning}
	lapsed.Lifecycle.Pin("user@example.com", device.PinReasonOvernightTest, "", &past)
	deviceSvc := &mockDeviceService{}
	hostSvc := &mockHostService{}
	svc := New(Config{Enabled: true, BatchSize: 10, DryRun: true},
		Deps{
			DeviceService: deviceSvc, HostService: hostSvc,
			DeviceRepo: newReaperDeviceRepo(lapsed, &device.Device{ID: "expired"}),
			HostRepo:   newReaperHostRepo(&host.Host{ID: "host-1"}),
		},
		slog.Default())

	if err := svc.reap(t.Context()); err != nil {
		t.Errorf("a dry-run cycle reported %v, want no failures", err)
	}
	if got := deviceSvc.unpinned(); len(got) != 0 {
		t.Errorf("dry run unpinned %v", got)
	}
	if got := deviceSvc.deleted(); len(got) != 0 {
		t.Errorf("dry run deleted devices %v", got)
	}
	if got := hostSvc.deleted(); len(got) != 0 {
		t.Errorf("dry run deleted hosts %v", got)
	}
	if got := svc.totalPinsLapsed.Load() + svc.totalDevicesReaped.Load() + svc.totalHostsReaped.Load(); got != 0 {
		t.Errorf("dry run moved the counters by %d", got)
	}
}

func TestService_HandleDeleteErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deviceRepo := newReaperDeviceRepo(
			&device.Device{ID: "device-1"},
			&device.Device{ID: "device-2"},
		)
		hostRepo := newReaperHostRepo()
		deviceSvc := &mockDeviceService{
			deleteErr: errors.New("delete failed"),
		}
		hostSvc := &mockHostService{}
		logger := slog.Default()

		config := Config{
			Enabled:   true,
			Interval:  50 * time.Millisecond,
			BatchSize: 10,
			DryRun:    false,
		}

		svc := New(config, Deps{DeviceService: deviceSvc, HostService: hostSvc, DeviceRepo: deviceRepo, HostRepo: hostRepo}, logger)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		svc.Start(ctx)

		// Wait for reaper to run - it should handle errors gracefully
		time.Sleep(100 * time.Millisecond)

		svc.Stop()

		// Counter should not have increased due to errors
		if got := svc.totalDevicesReaped.Load(); got != 0 {
			t.Errorf("expected 0 reaped (due to errors), got %d", got)
		}
	})
}

// A pin whose pinned_until has passed must be unpinned (via the normal Unpin
// path, actor system:reaper); active pins and unpinned devices are untouched.
func TestService_UnpinsLapsedPins(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	lapsed := &device.Device{ID: "lapsed", Status: device.StatusRunning}
	lapsed.Lifecycle.Pin("user@example.com", device.PinReasonOvernightTest, "", &past)
	active := &device.Device{ID: "active", Status: device.StatusRunning}
	active.Lifecycle.Pin("user@example.com", device.PinReasonOvernightTest, "", &future)
	forever := &device.Device{ID: "forever", Status: device.StatusRunning}
	forever.Lifecycle.Pin("user@example.com", device.PinReasonOvernightTest, "", nil)

	deviceRepo := newReaperDeviceRepo(lapsed, active, forever)
	hostRepo := newReaperHostRepo()
	deviceSvc := &mockDeviceService{}
	svc := New(Config{Enabled: true, BatchSize: 10},
		Deps{DeviceService: deviceSvc, HostService: &mockHostService{}, DeviceRepo: deviceRepo, HostRepo: hostRepo},
		slog.Default())

	_ = svc.reap(t.Context())

	if got := deviceSvc.unpinned(); len(got) != 1 || got[0] != "lapsed" {
		t.Errorf("expected only the lapsed pin unpinned, got %v", got)
	}
	if got := svc.totalPinsLapsed.Load(); got != 1 {
		t.Errorf("totalPinsLapsed = %d, want 1", got)
	}
}

// An unpin failure is counted but must not abort the cycle.
func TestService_UnpinFailureCounted(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-time.Hour)
	lapsed := &device.Device{ID: "lapsed", Status: device.StatusRunning}
	lapsed.Lifecycle.Pin("user@example.com", device.PinReasonOvernightTest, "", &past)

	deviceRepo := newReaperDeviceRepo(lapsed)
	deviceSvc := &mockDeviceService{unpinErr: errors.New("boom")}
	svc := New(Config{Enabled: true, BatchSize: 10},
		Deps{DeviceService: deviceSvc, HostService: &mockHostService{}, DeviceRepo: deviceRepo, HostRepo: newReaperHostRepo()},
		slog.Default())

	_ = svc.reap(t.Context())

	if got := svc.totalFailures.Load(); got != 1 {
		t.Errorf("totalFailures = %d, want 1", got)
	}
	if got := svc.totalPinsLapsed.Load(); got != 0 {
		t.Errorf("totalPinsLapsed = %d, want 0", got)
	}
}

func TestService_CountsDeleteFailures(t *testing.T) {
	t.Parallel()
	deviceRepo := newReaperDeviceRepo(&device.Device{ID: "device-1"}, &device.Device{ID: "device-2"})
	hostRepo := newReaperHostRepo(&host.Host{ID: "host-1"})
	deviceSvc := &mockDeviceService{deleteErr: errors.New("delete failed")}
	hostSvc := &mockHostService{deleteErr: errors.New("delete failed")}

	config := Config{Enabled: true, Interval: time.Second, BatchSize: 10}
	svc := New(config, Deps{DeviceService: deviceSvc, HostService: hostSvc, DeviceRepo: deviceRepo, HostRepo: hostRepo}, slog.Default())

	reaped, failed := svc.reapExpiredDevices(t.Context(), false, 10)
	if reaped != 0 || failed != 2 {
		t.Errorf("reapExpiredDevices = (reaped=%d, failed=%d), want (0, 2)", reaped, failed)
	}
	reaped, failed = svc.reapExpiredHosts(t.Context(), false, 10)
	if reaped != 0 || failed != 1 {
		t.Errorf("reapExpiredHosts = (reaped=%d, failed=%d), want (0, 1)", reaped, failed)
	}
}

// A host the delete transaction finds no longer expired (extended or protected
// after the listing) is a concurrent win: neither reaped nor a failure. Drop
// the sweep's tolerate and this counts a failure on every racing extend.
func TestService_HostNoLongerExpiredIsTolerated(t *testing.T) {
	t.Parallel()
	hostRepo := newReaperHostRepo(&host.Host{ID: "host-1"})
	hostSvc := &mockHostService{deleteErr: &host.InvalidStateError{Message: "host host-1 is no longer expired"}}
	svc := New(Config{Enabled: true, BatchSize: 10},
		Deps{DeviceService: &mockDeviceService{}, HostService: hostSvc, DeviceRepo: newReaperDeviceRepo(), HostRepo: hostRepo},
		slog.Default())

	reaped, failed := svc.reapExpiredHosts(t.Context(), false, 10)
	if reaped != 0 || failed != 0 {
		t.Errorf("reapExpiredHosts = (reaped=%d, failed=%d), want (0, 0)", reaped, failed)
	}
}

// A lapsed pin re-pinned before the unpin commits is refused by UnpinLapsed
// with ErrInvalidState, and the sweep takes that as a concurrent win.
func TestService_RepinnedBeforeUnpinIsTolerated(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-time.Hour)
	lapsed := &device.Device{ID: "lapsed", Status: device.StatusRunning}
	lapsed.Lifecycle.Pin("user@example.com", device.PinReasonOvernightTest, "", &past)
	deviceSvc := &mockDeviceService{unpinErr: fmt.Errorf("device pin has not lapsed: %w", device.ErrInvalidState)}
	svc := New(Config{Enabled: true, BatchSize: 10},
		Deps{DeviceService: deviceSvc, HostService: &mockHostService{}, DeviceRepo: newReaperDeviceRepo(lapsed), HostRepo: newReaperHostRepo()},
		slog.Default())

	unpinned, failed := svc.unpinLapsedPins(t.Context(), false, 10)
	if unpinned != 0 || failed != 0 {
		t.Errorf("unpinLapsedPins = (unpinned=%d, failed=%d), want (0, 0)", unpinned, failed)
	}
}

func TestService_ZeroIntervalDefaults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := New(
			Config{Enabled: true, BatchSize: 10}, // Interval left at zero
			Deps{
				DeviceService: &mockDeviceService{},
				HostService:   &mockHostService{},
				DeviceRepo:    newReaperDeviceRepo(),
				HostRepo:      newReaperHostRepo(),
			},
			slog.Default(),
		)

		if svc.config.Interval != time.Minute {
			t.Errorf("Interval = %v, want %v", svc.config.Interval, time.Minute)
		}

		// Without the default this panics in time.NewTicker.
		svc.Start(t.Context())
		svc.Stop()
	})
}

func TestService_HandleListErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deviceRepo := newReaperDeviceRepo()
		deviceRepo.ListErr = errors.New("list failed")
		hostRepo := newReaperHostRepo()
		hostRepo.ListErr = errors.New("list failed")
		deviceSvc := &mockDeviceService{}
		hostSvc := &mockHostService{}
		logger := slog.Default()

		config := Config{
			Enabled:   true,
			Interval:  50 * time.Millisecond,
			BatchSize: 10,
			DryRun:    false,
		}

		svc := New(config, Deps{DeviceService: deviceSvc, HostService: hostSvc, DeviceRepo: deviceRepo, HostRepo: hostRepo}, logger)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		// Should not panic even with list errors
		svc.Start(ctx)

		time.Sleep(100 * time.Millisecond)

		svc.Stop()

		// Should handle errors gracefully
		if len(deviceSvc.deleted()) != 0 {
			t.Errorf("expected 0 deleted devices, got %d", len(deviceSvc.deleted()))
		}
	})
}
