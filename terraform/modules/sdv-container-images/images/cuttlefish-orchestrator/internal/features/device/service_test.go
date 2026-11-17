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

package devicefeature

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/batch"
	"cloud-cuttlefish-orchestrator/internal/domain/build"
	deviceDomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/device/devicetest"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/features/device/placement"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// fakeEnqueuer satisfies TaskEnqueuer for tests: it records enqueued tasks and
// returns a throwaway mutation (the mock repo ignores extra mutations).
type fakeEnqueuer struct {
	enqueued []*task.Task
	wakes    int
}

// Wake is what the producer calls once the transaction carrying the mutation
// committed, so the count separates "a mutation was built" from "the commit
// that carried it succeeded".
func (f *fakeEnqueuer) Wake() { f.wakes++ }

func (f *fakeEnqueuer) EnqueueMutation(_ context.Context, t *task.Task) *spanner.Mutation {
	f.enqueued = append(f.enqueued, t)
	return spanner.Delete("tasks", spanner.Key{t.ID})
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, t *task.Task) error {
	f.enqueued = append(f.enqueued, t)
	return nil
}

// atomicDeviceRepo extends the shared in-memory fake with the feature's
// AtomicDeviceRepository operations. When linked to a host fake it mirrors the
// real transactional contract: create validates host state and capacity and
// increments the counter; delete decrements it idempotently. Extra mutations
// are ignored (the fake has no transaction to fold them into).
type atomicDeviceRepo struct {
	*devicetest.FakeRepo
	hostRepo  *hosttest.FakeRepo
	snapshots map[string]*deviceDomain.Snapshot

	// MarkDeletedErr fails MarkDeletedWithHostDecrement when non-nil, which is
	// the timed-out-commit shape the single-commit delete must survive intact.
	MarkDeletedErr error
}

func newDeviceRepoFake() *atomicDeviceRepo {
	return &atomicDeviceRepo{
		FakeRepo:  devicetest.NewFakeRepo(),
		snapshots: make(map[string]*deviceDomain.Snapshot),
	}
}

func newDeviceRepoFakeWithHosts(hostRepo *hosttest.FakeRepo) *atomicDeviceRepo {
	return &atomicDeviceRepo{
		FakeRepo: devicetest.NewFakeRepo(), hostRepo: hostRepo,
		snapshots: make(map[string]*deviceDomain.Snapshot),
	}
}

// SnapshotInsert records the row immediately, because the fake has no admission
// transaction to fold the mutation into, and returns a nil mutation, which the
// fake admitter ignores.
func (m *atomicDeviceRepo) SnapshotInsert(s *deviceDomain.Snapshot) (*spanner.Mutation, error) {
	cp := *s
	m.snapshots[s.ID] = &cp
	return nil, nil
}

func (m *atomicDeviceRepo) GetSnapshot(_ context.Context, deviceID, snapshotID string) (*deviceDomain.Snapshot, error) {
	s, ok := m.snapshots[snapshotID]
	if !ok || s.DeviceID != deviceID {
		return nil, &deviceDomain.SnapshotNotFoundError{ID: snapshotID}
	}
	cp := *s
	return &cp, nil
}

func (m *atomicDeviceRepo) ListSnapshotsByDevice(_ context.Context, deviceID string) ([]*deviceDomain.Snapshot, error) {
	var out []*deviceDomain.Snapshot
	for _, s := range m.snapshots {
		if s.DeviceID == deviceID {
			cp := *s
			out = append(out, &cp)
		}
	}
	slices.SortFunc(out, func(a, b *deviceDomain.Snapshot) int {
		return b.CreatedAt.Compare(a.CreatedAt) // newest first, like the query
	})
	return out, nil
}

func (m *atomicDeviceRepo) CountLiveSnapshotsByDevice(_ context.Context, deviceID string) (int64, error) {
	var n int64
	for _, s := range m.snapshots {
		if s.DeviceID == deviceID && s.State != deviceDomain.SnapshotStateLost {
			n++
		}
	}
	return n, nil
}

func (m *atomicDeviceRepo) MarkSnapshotReady(_ context.Context, _, snapshotID string) error {
	s, ok := m.snapshots[snapshotID]
	if !ok {
		return &deviceDomain.SnapshotNotFoundError{ID: snapshotID}
	}
	s.State = deviceDomain.SnapshotStateReady
	s.UpdatedAt = time.Now()
	return nil
}

func (m *atomicDeviceRepo) DeleteSnapshotRow(_ context.Context, _, snapshotID string) error {
	delete(m.snapshots, snapshotID)
	return nil
}

// Mutate mirrors the real repository contract: fn runs against the stored
// device and the result is persisted; fn errors abort the "transaction".
func (m *atomicDeviceRepo) Mutate(_ context.Context, id string, fn func(*deviceDomain.Device) error) (*deviceDomain.Device, error) {
	if m.UpdateErr != nil {
		return nil, m.UpdateErr
	}
	d, ok := m.Devices[id]
	if !ok {
		return nil, deviceDomain.ErrNotFound
	}
	working := *d
	if err := fn(&working); err != nil {
		return nil, fmt.Errorf("mutating device %s: %w", id, err)
	}
	working.UpdatedAt = time.Now()
	m.Devices[id] = &working
	return &working, nil
}

// hostAcceptsDevices mirrors the real repository's placement-status set.
func hostAcceptsDevices(status host.Status) bool {
	switch status {
	case host.StatusPending, host.StatusProvisioning, host.StatusStarting, host.StatusRunning:
		return true
	default:
		return false
	}
}

func (m *atomicDeviceRepo) CreateWithHostIncrement(_ context.Context, d *deviceDomain.Device, _ ...*spanner.Mutation) error {
	if m.CreateErr != nil {
		return m.CreateErr
	}
	if m.hostRepo != nil {
		h, ok := m.hostRepo.Hosts[d.HostID]
		if !ok {
			return errors.New("host not found")
		}
		if !hostAcceptsDevices(h.Status) {
			return fmt.Errorf("host %s cannot accept devices (status: %s)", h.ID, h.Status)
		}
		if h.CurrentDeviceCount >= h.MaxDevices {
			return host.ErrCapacityExceeded
		}
		h.CurrentDeviceCount++
	}
	m.Devices[d.ID] = d
	return nil
}

// PickHostForPlacement mirrors the real query: capacity + accepting status +
// most-loaded-first, with a deterministic ID tiebreak. gpuOnly approximates
// the catalog join with an attached-GPU check (the fake has no machine-type
// catalog), and prefs.CPUOnly its fence; the preference ordering is exercised
// by the emulator tests, not mirrored here.
func (m *atomicDeviceRepo) PickHostForPlacement(_ context.Context, gpuOnly bool, excluded []string, prefs placement.Prefs) (string, bool, error) {
	if m.hostRepo == nil {
		return "", false, nil
	}
	var best *host.Host
	for _, h := range m.hostRepo.Hosts {
		if !placeable(h, gpuOnly, prefs.CPUOnly) {
			continue
		}
		if slices.Contains(excluded, h.ID) {
			continue
		}
		if best == nil ||
			h.CurrentDeviceCount > best.CurrentDeviceCount ||
			(h.CurrentDeviceCount == best.CurrentDeviceCount && h.ID < best.ID) {
			best = h
		}
	}
	if best == nil {
		return "", false, nil
	}
	return best.ID, false, nil
}

func placeable(h *host.Host, gpuOnly, cpuOnly bool) bool {
	return hostAcceptsDevices(h.Status) && !h.IsDegraded() && !h.Exclusive &&
		h.CurrentDeviceCount < h.MaxDevices &&
		(!gpuOnly || h.GPUCount > 0) && (!cpuOnly || h.GPUCount == 0)
}

// ListRecentBuildIDs mirrors the real distinct-recent-builds query over the
// fake's stored devices.
func (m *atomicDeviceRepo) ListRecentBuildIDs(_ context.Context, since time.Time) ([]string, error) {
	seen := make(map[string]bool)
	var ids []string
	for _, d := range m.Devices {
		if d.BuildID != "" && !d.CreatedAt.Before(since) && !seen[d.BuildID] {
			seen[d.BuildID] = true
			ids = append(ids, d.BuildID)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// ListPlaceableCapacity mirrors the real fleet-capacity query: placeable
// hosts with their free slots, most-loaded first, deterministic ID tiebreak.
func (m *atomicDeviceRepo) ListPlaceableCapacity(_ context.Context, gpuOnly bool, prefs placement.Prefs) ([]batch.HostSlot, error) {
	if m.hostRepo == nil {
		return nil, nil
	}
	var hosts []*host.Host
	for _, h := range m.hostRepo.Hosts {
		if placeable(h, gpuOnly, prefs.CPUOnly) {
			hosts = append(hosts, h)
		}
	}
	slices.SortFunc(hosts, func(a, b *host.Host) int {
		if a.CurrentDeviceCount != b.CurrentDeviceCount {
			return int(b.CurrentDeviceCount - a.CurrentDeviceCount)
		}
		return strings.Compare(a.ID, b.ID)
	})
	slots := make([]batch.HostSlot, 0, len(hosts))
	for _, h := range hosts {
		slots = append(slots, batch.HostSlot{HostID: h.ID, Free: int(h.MaxDevices - h.CurrentDeviceCount)})
	}
	return slots, nil
}

func (m *atomicDeviceRepo) MarkDeletedWithHostDecrement(_ context.Context, hostID, deviceID string, _ ...*spanner.Mutation) (bool, error) {
	if m.MarkDeletedErr != nil {
		return false, m.MarkDeletedErr
	}
	d, ok := m.Devices[deviceID]
	if !ok || d.Status == deviceDomain.StatusDeleted {
		return false, nil // gone or already deleted: idempotent no-op
	}
	d.Status = deviceDomain.StatusDeleted
	m.StatusUpdates[deviceID] = deviceDomain.StatusDeleted
	if m.hostRepo != nil {
		if h, ok := m.hostRepo.Hosts[hostID]; ok && h.CurrentDeviceCount > 0 {
			h.CurrentDeviceCount--
		}
	}
	return true, nil
}

func (m *atomicDeviceRepo) BatchMarkDeletedWithHostDecrement(ctx context.Context, devices []*deviceDomain.Device, _ map[string]*spanner.Mutation) ([]*deviceDomain.Device, error) {
	var transitioned []*deviceDomain.Device
	for _, d := range devices {
		if ok, _ := m.MarkDeletedWithHostDecrement(ctx, d.HostID, d.ID); ok {
			transitioned = append(transitioned, d)
		}
	}
	return transitioned, nil
}

// ctxAwareDeviceRepo refuses UpdateStatus on an already-canceled context,
// modeling a real Spanner write that fails on a dead context. It backs the test
// that markCreateFailed detaches its terminal write from the exhausted
// attempt's (often already-canceled) context.
type mockBuildRepo struct {
	builds map[string]*build.Build
	getErr error
}

func newMockBuildRepo() *mockBuildRepo {
	return &mockBuildRepo{
		builds: make(map[string]*build.Build),
	}
}

func (m *mockBuildRepo) Get(ctx context.Context, id string) (*build.Build, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	b, ok := m.builds[id]
	if !ok {
		// The real repository returns the sentinel; callers branch on it
		// (unregistered build IDs are legal raw @ab references).
		return nil, build.ErrNotFound
	}
	return b, nil
}

func (m *mockBuildRepo) List(ctx context.Context, filter build.ListFilter) ([]*build.Build, error) {
	var result []*build.Build
	for _, b := range m.builds {
		if filter.IsDefault != nil && b.IsDefault != *filter.IsDefault {
			continue
		}
		if filter.Branch != nil && b.Branch != *filter.Branch {
			continue
		}
		if filter.Target != nil && b.Target != *filter.Target {
			continue
		}
		if filter.Source != nil && b.Source != *filter.Source {
			continue
		}
		result = append(result, b)
	}
	return result, nil
}

func (m *mockBuildRepo) Create(ctx context.Context, b *build.Build) error {
	m.builds[b.ID] = b
	return nil
}

func (m *mockBuildRepo) Update(ctx context.Context, b *build.Build) error {
	m.builds[b.ID] = b
	return nil
}

func (m *mockBuildRepo) Delete(ctx context.Context, id string) error {
	delete(m.builds, id)
	return nil
}

func (m *mockBuildRepo) GetDefault(ctx context.Context) (*build.Build, error) {
	for _, b := range m.builds {
		if b.IsDefault {
			return b, nil
		}
	}
	return nil, errors.New("no default build")
}

func (m *mockBuildRepo) GetByBuildID(ctx context.Context, buildID string) (*build.Build, error) {
	for _, b := range m.builds {
		if b.BuildID == buildID {
			return b, nil
		}
	}
	return nil, errors.New("build not found")
}

func (m *mockBuildRepo) Count(ctx context.Context, filter build.ListFilter) (int64, error) {
	return int64(len(m.builds)), nil
}

// Tests

func TestDeviceService_Get(t *testing.T) {
	t.Parallel()
	deviceRepo := newDeviceRepoFake()
	hostRepo := hosttest.NewFakeRepo()
	buildRepo := newMockBuildRepo()
	hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())
	logger := slog.Default()

	// Add a test device
	testDevice := &deviceDomain.Device{
		ID:     "test-device-id",
		HostID: "test-host-id",
		Name:   "test-device",
		Status: deviceDomain.StatusRunning,
	}
	deviceRepo.Devices[testDevice.ID] = testDevice

	svc, _ := NewDeviceService(deviceRepo, hostRepo, buildRepo, hoFactory, logger, WithTaskEnqueuer(&fakeEnqueuer{}))

	t.Run("Success", func(t *testing.T) {
		d, err := svc.Get(t.Context(), "test-device-id")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.ID != testDevice.ID {
			t.Errorf("got device ID %s, want %s", d.ID, testDevice.ID)
		}
		if d.Name != testDevice.Name {
			t.Errorf("got device name %s, want %s", d.Name, testDevice.Name)
		}
	})

	t.Run("NotFound", func(t *testing.T) {
		_, err := svc.Get(t.Context(), "nonexistent")
		if err == nil {
			t.Error("expected error for nonexistent device")
		}
	})
}

func TestDeviceService_List(t *testing.T) {
	t.Parallel()
	deviceRepo := newDeviceRepoFake()
	hostRepo := hosttest.NewFakeRepo()
	buildRepo := newMockBuildRepo()
	hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())
	logger := slog.Default()

	// Add test devices
	deviceRepo.Devices["device-1"] = &deviceDomain.Device{
		ID:     "device-1",
		HostID: "host-1",
		Name:   "device-one",
		Status: deviceDomain.StatusRunning,
	}
	deviceRepo.Devices["device-2"] = &deviceDomain.Device{
		ID:     "device-2",
		HostID: "host-1",
		Name:   "device-two",
		Status: deviceDomain.StatusStopped,
	}

	svc, _ := NewDeviceService(deviceRepo, hostRepo, buildRepo, hoFactory, logger, WithTaskEnqueuer(&fakeEnqueuer{}))

	t.Run("ListAll", func(t *testing.T) {
		devices, err := svc.List(t.Context(), deviceDomain.ListFilter{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(devices) != 2 {
			t.Errorf("got %d devices, want 2", len(devices))
		}
	})
}

func TestDeviceService_Create_HostNotFound(t *testing.T) {
	t.Parallel()
	deviceRepo := newDeviceRepoFake()
	hostRepo := hosttest.NewFakeRepo()
	buildRepo := newMockBuildRepo()
	hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())
	logger := slog.Default()

	svc, _ := NewDeviceService(deviceRepo, hostRepo, buildRepo, hoFactory, logger, WithTaskEnqueuer(&fakeEnqueuer{}))

	req := deviceDomain.CreateRequest{
		HostID: "nonexistent-host",
		Name:   "test-device",
	}

	_, err := svc.Create(t.Context(), req, "test-user")
	if err == nil {
		t.Error("expected error for nonexistent host")
	}
}

func TestDeviceService_Create_HostNotRunning(t *testing.T) {
	t.Parallel()
	hostRepo := hosttest.NewFakeRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	buildRepo := newMockBuildRepo()
	hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())
	logger := slog.Default()

	// Add a stopped host
	hostRepo.Hosts["test-host"] = &host.Host{
		ID:         "test-host",
		Status:     host.StatusStopped,
		MaxDevices: 4,
	}

	svc, _ := NewDeviceService(deviceRepo, hostRepo, buildRepo, hoFactory, logger, WithTaskEnqueuer(&fakeEnqueuer{}))

	req := deviceDomain.CreateRequest{
		HostID: "test-host",
		Name:   "test-device",
	}

	_, err := svc.Create(t.Context(), req, "test-user")
	if err == nil {
		t.Error("expected error for stopped host")
	}
}

func TestDeviceService_Create_HostNoCapacity(t *testing.T) {
	t.Parallel()
	hostRepo := hosttest.NewFakeRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	buildRepo := newMockBuildRepo()
	hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())
	logger := slog.Default()

	// Add a running host at capacity
	hostRepo.Hosts["test-host"] = &host.Host{
		ID:                 "test-host",
		Status:             host.StatusRunning,
		MaxDevices:         2,
		CurrentDeviceCount: 2,
	}

	svc, _ := NewDeviceService(deviceRepo, hostRepo, buildRepo, hoFactory, logger, WithTaskEnqueuer(&fakeEnqueuer{}))

	req := deviceDomain.CreateRequest{
		HostID: "test-host",
		Name:   "test-device",
	}

	_, err := svc.Create(t.Context(), req, "test-user")
	if err == nil {
		t.Error("expected error for host at capacity")
	}
}

func TestDeviceService_Create_Success(t *testing.T) {
	t.Parallel()
	hostRepo := hosttest.NewFakeRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	buildRepo := newMockBuildRepo()
	hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())
	logger := slog.Default()

	// Add a running host with capacity
	hostRepo.Hosts["test-host"] = &host.Host{
		ID:                   "test-host",
		Status:               host.StatusRunning,
		MaxDevices:           4,
		CurrentDeviceCount:   1,
		IPAddress:            "10.0.0.1",
		HostOrchestratorPort: 2080,
	}

	svc, _ := NewDeviceService(deviceRepo, hostRepo, buildRepo, hoFactory, logger, WithTaskEnqueuer(&fakeEnqueuer{}))

	req := deviceDomain.CreateRequest{
		HostID: "test-host",
		Name:   "test-device",
	}

	d, err := svc.Create(t.Context(), req, "test-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if d.ID == "" {
		t.Error("expected device ID to be set")
	}
	if d.HostID != "test-host" {
		t.Errorf("got host ID %s, want test-host", d.HostID)
	}
	if d.Name != "test-device" {
		t.Errorf("got name %s, want test-device", d.Name)
	}
	if d.Status != deviceDomain.StatusPending {
		t.Errorf("got status %s, want %s", d.Status, deviceDomain.StatusPending)
	}

	// Verify device was stored
	if _, ok := deviceRepo.Devices[d.ID]; !ok {
		t.Error("device not stored in repository")
	}
}

// The wake follows the COMMIT, not the mutation. A create whose transaction
// failed still built an enqueue mutation, and waking on that would send the
// pool looking for work that was never written. That is cheap when wrong, but
// the call is only correct after the commit reports success.
func TestDeviceService_Create_WakesOnlyOnACommittedEnqueue(t *testing.T) {
	t.Parallel()
	hostRepo := hosttest.NewFakeRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	hostRepo.Hosts["test-host"] = &host.Host{
		ID: "test-host", Status: host.StatusRunning, MaxDevices: 4,
		IPAddress: "10.0.0.1", HostOrchestratorPort: 2080,
	}
	enq := &fakeEnqueuer{}
	svc, _ := NewDeviceService(deviceRepo, hostRepo, newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(), WithTaskEnqueuer(enq))

	req := deviceDomain.CreateRequest{HostID: "test-host", Name: "committed"}
	if _, err := svc.Create(t.Context(), req, "u"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if enq.wakes != 1 {
		t.Errorf("wakes after a committed create = %d, want 1", enq.wakes)
	}

	deviceRepo.CreateErr = errors.New("commit rejected")
	if _, err := svc.Create(t.Context(), deviceDomain.CreateRequest{HostID: "test-host", Name: "lost"}, "u"); err == nil {
		t.Fatal("Create: want the commit failure")
	}
	if len(enq.enqueued) != 2 {
		t.Fatalf("mutations built = %d, want 2 (the failed create built one too)", len(enq.enqueued))
	}
	if enq.wakes != 1 {
		t.Errorf("wakes = %d, want still 1: a failed commit enqueued nothing", enq.wakes)
	}
}

func TestDeviceService_Create_LifecycleTTL(t *testing.T) {
	t.Parallel()
	hostRepo := hosttest.NewFakeRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	buildRepo := newMockBuildRepo()
	hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())

	hostRepo.Hosts["test-host"] = &host.Host{
		ID:                   "test-host",
		Status:               host.StatusRunning,
		MaxDevices:           4,
		IPAddress:            "10.0.0.1",
		HostOrchestratorPort: 2080,
	}
	svc, _ := NewDeviceService(deviceRepo, hostRepo, buildRepo, hoFactory, slog.Default(), WithTaskEnqueuer(&fakeEnqueuer{}))

	d, err := svc.Create(t.Context(), deviceDomain.CreateRequest{
		HostID:   "test-host",
		Name:     "ttl-device",
		TTLHours: 8,
	}, "test-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !d.Lifecycle.IsEphemeral() {
		t.Errorf("got lifecycle mode %s, want ephemeral", d.Lifecycle.Mode)
	}
	if d.Lifecycle.TTLSeconds != 8*3600 {
		t.Errorf("got ttl_seconds %d, want %d", d.Lifecycle.TTLSeconds, 8*3600)
	}
	if d.Lifecycle.ExpiresAt == nil {
		t.Fatal("expected expires_at to be set")
	}
	want := time.Now().Add(8 * time.Hour)
	if diff := d.Lifecycle.ExpiresAt.Sub(want); diff < -time.Minute || diff > time.Minute {
		t.Errorf("expires_at %v not within a minute of %v", d.Lifecycle.ExpiresAt, want)
	}
}

func TestDeviceService_Create_Pinned(t *testing.T) {
	t.Parallel()
	hostRepo := hosttest.NewFakeRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	buildRepo := newMockBuildRepo()
	hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())

	hostRepo.Hosts["test-host"] = &host.Host{
		ID:                   "test-host",
		Status:               host.StatusRunning,
		MaxDevices:           4,
		IPAddress:            "10.0.0.1",
		HostOrchestratorPort: 2080,
	}
	svc, _ := NewDeviceService(deviceRepo, hostRepo, buildRepo, hoFactory, slog.Default(), WithTaskEnqueuer(&fakeEnqueuer{}))

	d, err := svc.Create(t.Context(), deviceDomain.CreateRequest{
		HostID:             "test-host",
		Name:               "pinned-device",
		Pinned:             true,
		PinnedReason:       deviceDomain.PinReasonOther,
		PinnedReasonDetail: "long soak test",
		Tags:               []string{"soak", "team-a"},
	}, "pinner@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !d.Lifecycle.IsPinned() {
		t.Fatalf("got lifecycle mode %s, want pinned", d.Lifecycle.Mode)
	}
	if d.Lifecycle.ExpiresAt != nil {
		t.Error("pinned device must not have expires_at")
	}
	if d.Lifecycle.PinnedBy == nil || *d.Lifecycle.PinnedBy != "pinner@example.com" {
		t.Errorf("got pinned_by %v, want creator email", d.Lifecycle.PinnedBy)
	}
	if d.Lifecycle.PinnedReason == nil || *d.Lifecycle.PinnedReason != deviceDomain.PinReasonOther {
		t.Errorf("got pinned_reason %v, want other", d.Lifecycle.PinnedReason)
	}
	// TTLSeconds keeps the default so a later unpin has a base TTL.
	if d.Lifecycle.TTLSeconds != deviceDomain.DefaultDeviceTTLSeconds {
		t.Errorf("got ttl_seconds %d, want default %d", d.Lifecycle.TTLSeconds, deviceDomain.DefaultDeviceTTLSeconds)
	}
	if len(d.Tags) != 2 || d.Tags[0] != "soak" {
		t.Errorf("got tags %v, want [soak team-a]", d.Tags)
	}
}

func TestDeviceService_Delete_Success(t *testing.T) {
	t.Parallel()
	deviceRepo := newDeviceRepoFake()
	hostRepo := hosttest.NewFakeRepo()
	buildRepo := newMockBuildRepo()
	hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())
	logger := slog.Default()

	// Add a host
	hostRepo.Hosts["test-host"] = &host.Host{
		ID:                 "test-host",
		Status:             host.StatusStopped,
		CurrentDeviceCount: 1,
	}

	// Add a device
	deviceRepo.Devices["test-device"] = &deviceDomain.Device{
		ID:     "test-device",
		HostID: "test-host",
		Name:   "test-device",
		Status: deviceDomain.StatusError,
	}

	svc, _ := NewDeviceService(deviceRepo, hostRepo, buildRepo, hoFactory, logger, WithTaskEnqueuer(&fakeEnqueuer{}))

	err := svc.Delete(t.Context(), "test-device")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify status was updated to deleted
	d := deviceRepo.Devices["test-device"]
	if d.Status != deviceDomain.StatusDeleted {
		t.Errorf("got status %s, want %s", d.Status, deviceDomain.StatusDeleted)
	}
}

// statusWriteRecorder records every UpdateStatus call. Delete must never make
// one: the informational `deleting` pre-write was the only way a device could
// strand (intent landed, transactional mark timed out, row sat `deleting`
// with no task), so its absence is an invariant. Reintroducing the pre-write
// turns this red.
type statusWriteRecorder struct {
	*atomicDeviceRepo
	statusWrites []deviceDomain.Status
}

func (r *statusWriteRecorder) UpdateStatus(ctx context.Context, id string, status deviceDomain.Status, msg string) error {
	r.statusWrites = append(r.statusWrites, status)
	return r.atomicDeviceRepo.UpdateStatus(ctx, id, status, msg)
}

func TestDeviceService_Delete_SingleCommit(t *testing.T) {
	t.Parallel()
	repo := &statusWriteRecorder{atomicDeviceRepo: newDeviceRepoFake()}
	repo.Devices["dev-1"] = &deviceDomain.Device{
		ID: "dev-1", HostID: "h", Name: "dev-1", Status: deviceDomain.StatusRunning,
	}
	svc, _ := NewDeviceService(repo, hosttest.NewFakeRepo(), newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(), WithTaskEnqueuer(&fakeEnqueuer{}))

	if err := svc.Delete(t.Context(), "dev-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := repo.Devices["dev-1"].Status; got != deviceDomain.StatusDeleted {
		t.Errorf("status = %s, want deleted", got)
	}
	if len(repo.statusWrites) != 0 {
		t.Errorf("Delete made %d UpdateStatus writes (%v); the delete is one commit, no intent write",
			len(repo.statusWrites), repo.statusWrites)
	}
}

// A delete whose transactional mark fails must leave the row exactly as it
// was. The strand shape (a `deleting` row with no task) must be unreachable,
// and the caller retries the idempotent verb instead.
func TestDeviceService_Delete_FailedCommitLeavesRowUntouched(t *testing.T) {
	t.Parallel()
	repo := newDeviceRepoFake()
	repo.MarkDeletedErr = errors.New("deadline exceeded, transaction outcome unknown")
	repo.Devices["dev-1"] = &deviceDomain.Device{
		ID: "dev-1", HostID: "h", Name: "dev-1", Status: deviceDomain.StatusRunning,
	}
	svc, _ := NewDeviceService(repo, hosttest.NewFakeRepo(), newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(), WithTaskEnqueuer(&fakeEnqueuer{}))

	if err := svc.Delete(t.Context(), "dev-1"); err == nil {
		t.Fatal("Delete must surface the failed commit")
	}
	if got := repo.Devices["dev-1"].Status; got != deviceDomain.StatusRunning {
		t.Errorf("status after failed delete = %s, want the untouched running", got)
	}

	// And the retry a real client makes completes the delete.
	repo.MarkDeletedErr = nil
	if err := svc.Delete(t.Context(), "dev-1"); err != nil {
		t.Fatalf("retried Delete: %v", err)
	}
	if got := repo.Devices["dev-1"].Status; got != deviceDomain.StatusDeleted {
		t.Errorf("status after retry = %s, want deleted", got)
	}
}

func TestDeviceService_Delete_NotFound(t *testing.T) {
	t.Parallel()
	deviceRepo := newDeviceRepoFake()
	hostRepo := hosttest.NewFakeRepo()
	buildRepo := newMockBuildRepo()
	hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())
	logger := slog.Default()

	svc, _ := NewDeviceService(deviceRepo, hostRepo, buildRepo, hoFactory, logger, WithTaskEnqueuer(&fakeEnqueuer{}))

	err := svc.Delete(t.Context(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent device")
	}
}

func TestDeviceService_Start_WrongStatus(t *testing.T) {
	t.Parallel()
	deviceRepo := newDeviceRepoFake()
	hostRepo := hosttest.NewFakeRepo()
	buildRepo := newMockBuildRepo()
	hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())
	logger := slog.Default()

	// Add a running device (cannot start)
	deviceRepo.Devices["test-device"] = &deviceDomain.Device{
		ID:     "test-device",
		HostID: "test-host",
		Status: deviceDomain.StatusRunning,
	}

	svc, _ := NewDeviceService(deviceRepo, hostRepo, buildRepo, hoFactory, logger, WithTaskEnqueuer(&fakeEnqueuer{}))

	err := svc.Start(t.Context(), "test-device")
	if err == nil {
		t.Error("expected error for starting running device")
	}
}

func TestDeviceService_Stop_WrongStatus(t *testing.T) {
	t.Parallel()
	deviceRepo := newDeviceRepoFake()
	hostRepo := hosttest.NewFakeRepo()
	buildRepo := newMockBuildRepo()
	hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())
	logger := slog.Default()

	// Add a stopped device (cannot stop)
	deviceRepo.Devices["test-device"] = &deviceDomain.Device{
		ID:     "test-device",
		HostID: "test-host",
		Status: deviceDomain.StatusStopped,
	}

	svc, _ := NewDeviceService(deviceRepo, hostRepo, buildRepo, hoFactory, logger, WithTaskEnqueuer(&fakeEnqueuer{}))

	err := svc.Stop(t.Context(), "test-device")
	if err == nil {
		t.Error("expected error for stopping stopped device")
	}
}

func TestParseCVDID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		cvdID, wantGroup, wantName string
	}{
		{"cvd-abc123/my-device", "cvd-abc123", "my-device"},
		{"group/name/with/slashes", "group", "name/with/slashes"},
		{"bare-name", "", "bare-name"},
		{"", "", ""},
	}
	for _, tt := range tests {
		group, name := deviceDomain.ParseCVDID(tt.cvdID)
		if group != tt.wantGroup || name != tt.wantName {
			t.Errorf("deviceDomain.ParseCVDID(%q) = (%q, %q), want (%q, %q)",
				tt.cvdID, group, name, tt.wantGroup, tt.wantName)
		}
	}
}
