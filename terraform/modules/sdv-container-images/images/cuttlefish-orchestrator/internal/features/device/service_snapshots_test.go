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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/tests/harness/fakeho"
)

// fakeAdmitter accepts every verb and records what was admitted, so the
// admission tests exercise the service's own guards rather than Spanner's.
type fakeAdmitter struct {
	admitted []spannerplatform.AdmitRequest
}

func (f *fakeAdmitter) Admit(_ context.Context, req spannerplatform.AdmitRequest) (*operation.Record, error) {
	f.admitted = append(f.admitted, req)
	return &operation.Record{
		ID: fmt.Sprintf("op-%d", len(f.admitted)), Verb: req.Verb,
		ResourceType: operation.ResourceTypeDevice, ResourceID: req.ResourceID,
		TaskID: "t1", Request: req.Request, CreatedBy: req.CreatedBy,
	}, nil
}

// snapshotFixture is a running device on a live host, with a fake admitter,
// optionally backed by a fakeho server for the worker-side tests.
type snapshotFixture struct {
	svc  *DeviceService
	repo *atomicDeviceRepo
}

func newSnapshotFixture(t *testing.T, hoPort int) *snapshotFixture {
	t.Helper()

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h1"] = &host.Host{
		ID: "h1", Status: host.StatusRunning, MaxDevices: 4, Incarnation: 2,
		IPAddress: fakeho.SingleHostIP, HostOrchestratorPort: int64(hoPort),
	}
	repo := newDeviceRepoFake()
	repo.Devices["d1"] = &device.Device{
		ID: "d1", Name: "dev", HostID: "h1", Status: device.StatusRunning,
		// swiftshader is the only mode a take is admitted in, so every fixture
		// that means to reach past the GPU gate has to use it.
		CVDID: "cvdd1/dev", GPUMode: device.GPUModeSwift,
	}
	svc, err := NewDeviceService(repo, hostRepo, newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithTaskEnqueuer(&fakeEnqueuer{}), WithOperations(&fakeAdmitter{}))
	if err != nil {
		t.Fatalf("building service: %v", err)
	}
	return &snapshotFixture{svc: svc, repo: repo}
}

// newTakeFixture is newSnapshotFixture plus the host-side ground truth every
// admitted take now needs: a live host serving the group, and the launcher.log
// of the pinned swiftshader launch CCO's create produces. Admission reads that
// log rather than the device row, so a take fixture without a host refuses.
func newTakeFixture(t *testing.T) (*snapshotFixture, *fakeho.Server) {
	t.Helper()

	server, err := fakeho.New()
	if err != nil {
		t.Fatalf("starting fake HO: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	server.SeedGroup(fakeho.SingleHostIP, "cvdd1", "dev", "Running")
	server.SeedLogFile(fakeho.SingleHostIP, "cvdd1", "dev",
		device.LauncherLogFile, launcherLog(t, pinnedLaunchLog))
	return newSnapshotFixture(t, server.Port()), server
}

// seedSnapshot plants an inventory row directly.
func (f *snapshotFixture) seedSnapshot(id string, state device.SnapshotState, incarnation int64) {
	f.repo.snapshots[id] = &device.Snapshot{
		ID: id, DeviceID: "d1", HostID: "h1", CVDGroup: "cvdd1", CVDName: "dev",
		HostIncarnation: incarnation, State: state,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

// The GPU gate is the admission decision that keeps a caller from destroying
// the device. A take in any accelerated mode kills crosvm mid-capture and the
// launcher with it, so everything but swiftshader must refuse, with prose that
// names the fix.
//
// Auto is the case that matters. It reads as safe and was admitted for months
// on the belief that the host would fail politely; it does not, and the host
// resolves auto from a candidate list the guest image can supply, so no fact
// CCO holds makes it predictable.
func TestTakeSnapshotGatesOnGPUMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mode    device.GPUMode
		refused bool
	}{
		{device.GPUModeGfxstream, true},
		{device.GPUModeVirgl, true},
		{device.GPUModeSwift, false},
		{device.GPUModeAuto, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			t.Parallel()
			f, _ := newTakeFixture(t)
			f.repo.Devices["d1"].GPUMode = tt.mode

			_, err := f.svc.TakeSnapshot(t.Context(), "d1")
			if !tt.refused {
				if err != nil {
					t.Fatalf("TakeSnapshot(%s) = %v, want admitted", tt.mode, err)
				}
				return
			}
			var ve *device.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("TakeSnapshot(%s) = %v, want *ValidationError", tt.mode, err)
			}
			if !strings.Contains(ve.Message, "swiftshader") {
				t.Errorf("refusal %q does not tell the caller the fix", ve.Message)
			}
		})
	}
}

func TestTakeSnapshotRequiresRunning(t *testing.T) {
	t.Parallel()
	f := newSnapshotFixture(t, 1080)
	f.repo.Devices["d1"].Status = device.StatusStopped

	_, err := f.svc.TakeSnapshot(t.Context(), "d1")
	if !errors.Is(err, device.ErrInvalidState) {
		t.Fatalf("TakeSnapshot(stopped) = %v, want ErrInvalidState", err)
	}
}

// The cap counts occupancy (creating + ready) and not history. A snapshot lost
// with its host must free its slot or a preempted host permanently pins its
// devices at the limit.
func TestTakeSnapshotEnforcesTheCap(t *testing.T) {
	t.Parallel()
	f, _ := newTakeFixture(t)
	for i := range maxSnapshotsPerDevice {
		f.seedSnapshot(fmt.Sprintf("s%d", i), device.SnapshotStateReady, 2)
	}

	_, err := f.svc.TakeSnapshot(t.Context(), "d1")
	var le *device.SnapshotLimitError
	if !errors.As(err, &le) {
		t.Fatalf("TakeSnapshot at cap = %v, want *SnapshotLimitError", err)
	}
	if le.Limit != maxSnapshotsPerDevice {
		t.Errorf("limit in error = %d, want %d", le.Limit, maxSnapshotsPerDevice)
	}

	f.repo.snapshots["s0"].State = device.SnapshotStateLost
	if _, err := f.svc.TakeSnapshot(t.Context(), "d1"); err != nil {
		t.Fatalf("TakeSnapshot with a lost slot = %v, want admitted", err)
	}
}

// Admission must bind the operation to the row it inserted. The request
// payload carries the same id the row got, and the row starts creating on the
// device's current host incarnation.
func TestTakeSnapshotAdmissionBindsRowAndRequest(t *testing.T) {
	t.Parallel()
	f, _ := newTakeFixture(t)

	rec, err := f.svc.TakeSnapshot(t.Context(), "d1")
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}
	var req snapshotVerbRequest
	if err := json.Unmarshal(rec.Request, &req); err != nil || req.SnapshotID == "" {
		t.Fatalf("stored request %s carries no snapshot id", rec.Request)
	}
	snap, ok := f.repo.snapshots[req.SnapshotID]
	if !ok {
		t.Fatalf("no inventory row for admitted snapshot %s", req.SnapshotID)
	}
	if snap.State != device.SnapshotStateCreating {
		t.Errorf("row state = %q, want creating", snap.State)
	}
	if snap.HostIncarnation != 2 {
		t.Errorf("row incarnation = %d, want the host's current 2", snap.HostIncarnation)
	}
	if snap.CVDGroup != "cvdd1" || snap.CVDName != "dev" {
		t.Errorf("row addressing = %s/%s, want cvdd1/dev", snap.CVDGroup, snap.CVDName)
	}
}

// The gate the device row cannot supply. swiftshader was requested, the row
// still says so, and the launch actually running came back from a stop → start
// on cvd's defaults with virtio-fs on the bus. Admitting it destroys the
// device, so the refusal has to happen here, and no capture may reach the
// host, because the damage is done by the request itself.
func TestTakeSnapshotRefusesAnUnsnapshottableLaunch(t *testing.T) {
	t.Parallel()
	f, server := newTakeFixture(t)
	server.SeedLogFile(fakeho.SingleHostIP, "cvdd1", "dev",
		device.LauncherLogFile, launcherLog(t, defaultsLaunchLog))

	_, err := f.svc.TakeSnapshot(t.Context(), "d1")
	var ve *device.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("TakeSnapshot on a defaults relaunch = %v, want *ValidationError", err)
	}
	if !strings.Contains(ve.Message, "virtio-fs") {
		t.Errorf("refusal %q does not name what the launch carries", ve.Message)
	}
	if slices.Contains(server.ServedRoutes(), "POST /cvds/{group}/{name}/snapshots") {
		t.Error("a refused take still asked the host to capture")
	}
	if len(f.repo.snapshots) != 0 {
		t.Errorf("a refused take left %d inventory rows", len(f.repo.snapshots))
	}
}

// Fail closed, and say which read failed. A host that cannot produce the log is
// a host CCO knows nothing about, and "probably fine" is how the destroyed
// devices were admitted.
func TestTakeSnapshotRefusesWhenTheLaunchCannotBeRead(t *testing.T) {
	t.Parallel()
	server, err := fakeho.New()
	if err != nil {
		t.Fatalf("starting fake HO: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	// The host is up and the group is in its fleet; only the log is missing, so
	// the refusal is a real 404 on the read rather than an unreachable host.
	server.SeedGroup(fakeho.SingleHostIP, "cvdd1", "dev", "Running")
	f := newSnapshotFixture(t, server.Port())

	_, err = f.svc.TakeSnapshot(t.Context(), "d1")
	var ve *device.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("TakeSnapshot with no launcher.log = %v, want *ValidationError", err)
	}
	for _, want := range []string{"cannot confirm", device.LauncherLogFile} {
		if !strings.Contains(ve.Message, want) {
			t.Errorf("refusal %q does not mention %q", ve.Message, want)
		}
	}
	if !slices.Contains(server.ServedRoutes(), "GET /cvds/{group}/{name}/logs/{file}") {
		t.Error("admission never read the log it refuses for")
	}
	if slices.Contains(server.ServedRoutes(), "POST /cvds/{group}/{name}/snapshots") {
		t.Error("a take was captured on a launch that could not be read")
	}
}

// An env_config that contradicts the device's declared mode is refused on the
// stored text alone, before the host is dialed at all, which is what keeps a
// `cvd fleet` probe off the answer nothing needed it for.
func TestTakeSnapshotRefusesAContradictingEnvConfig(t *testing.T) {
	t.Parallel()
	f, server := newTakeFixture(t)
	f.repo.Devices["d1"].EnvConfig = map[string]any{"instances": []any{
		map[string]any{"vm": map[string]any{"crosvm": map[string]any{"enable_sandbox": true}}},
	}}

	_, err := f.svc.TakeSnapshot(t.Context(), "d1")
	var ve *device.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("TakeSnapshot with enable_sandbox true = %v, want *ValidationError", err)
	}
	if !strings.Contains(ve.Message, "enable_sandbox") {
		t.Errorf("refusal %q does not name the setting to remove", ve.Message)
	}
	if slices.Contains(server.ServedRoutes(), "GET /cvds/{group}/{name}/logs/{file}") {
		t.Error("the host was dialed for a take the stored config already refuses")
	}
}

func TestRestoreSnapshotAdmissionGuards(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		deviceStatus device.Status
		snapState    device.SnapshotState
		incarnation  int64
		wantErr      func(error) bool
	}{
		{
			"running device refused", device.StatusRunning, device.SnapshotStateReady, 2,
			func(err error) bool { return errors.Is(err, device.ErrInvalidState) },
		},
		{
			"lost snapshot refused", device.StatusStopped, device.SnapshotStateLost, 2,
			func(err error) bool { _, ok := errors.AsType[*device.SnapshotNotRestorableError](err); return ok },
		},
		{
			"stale incarnation refused", device.StatusStopped, device.SnapshotStateReady, 1,
			func(err error) bool { _, ok := errors.AsType[*device.SnapshotNotRestorableError](err); return ok },
		},
		{"stopped device with ready snapshot admitted", device.StatusStopped, device.SnapshotStateReady, 2, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSnapshotFixture(t, 1080)
			f.repo.Devices["d1"].Status = tt.deviceStatus
			f.seedSnapshot("snap-r", tt.snapState, tt.incarnation)

			_, err := f.svc.RestoreSnapshot(t.Context(), "d1", "snap-r")
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("RestoreSnapshot = %v, want admitted", err)
				}
				return
			}
			if !tt.wantErr(err) {
				t.Fatalf("RestoreSnapshot = %v, wrong refusal", err)
			}
		})
	}
}

func TestDeleteSnapshotUnknownIs404(t *testing.T) {
	t.Parallel()
	f := newSnapshotFixture(t, 1080)

	_, err := f.svc.DeleteSnapshot(t.Context(), "d1", "no-such")
	if _, ok := errors.AsType[*device.SnapshotNotFoundError](err); !ok {
		t.Fatalf("DeleteSnapshot(unknown) = %v, want *SnapshotNotFoundError", err)
	}
}

// snapshotWorker builds the queue handler for one snapshot verb over a
// fixture, plus the operation record its admission would have produced.
func snapshotWorker(t *testing.T, f *snapshotFixture, verb operation.Verb, snapshotID string) (*SnapshotVerbHandler, *fakeOperationStore) {
	t.Helper()

	req, err := json.Marshal(snapshotVerbRequest{SnapshotID: snapshotID})
	if err != nil {
		t.Fatalf("encoding request: %v", err)
	}
	rec := liveOperation(verb)
	rec.Request = req
	store := &fakeOperationStore{rec: rec}

	var def SnapshotVerbDefinition
	for _, d := range []SnapshotVerbDefinition{
		{operation.VerbDeviceSnapshot, "snapshot", "", (*DeviceService).runSnapshotTake},
		{operation.VerbDeviceSnapshotDelete, "snapshot delete", "", (*DeviceService).runSnapshotDelete},
		{operation.VerbDeviceSnapshotRestore, "snapshot restore", "", (*DeviceService).runSnapshotRestore},
	} {
		if d.Verb == verb {
			def = d
		}
	}
	h, err := NewSnapshotVerbHandler(def, f.svc, store, slog.Default())
	if err != nil {
		t.Fatalf("building handler: %v", err)
	}
	return h, store
}

func TestSnapshotTakeWorker_SuccessSettlesRowAndResponse(t *testing.T) {
	t.Parallel()
	server, err := fakeho.New()
	if err != nil {
		t.Fatalf("starting fake HO: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	server.SeedGroup(fakeho.SingleHostIP, "cvdd1", "dev", "Running")

	f := newSnapshotFixture(t, server.Port())
	f.seedSnapshot("snap-1", device.SnapshotStateCreating, 2)
	h, store := snapshotWorker(t, f, operation.VerbDeviceSnapshot, "snap-1")

	if err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 1, Attempts: 1}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	terminated, problem, response := store.snapshot()
	if terminated != 1 || problem != nil {
		t.Fatalf("terminated=%d problem=%v, want one success", terminated, problem)
	}
	var body map[string]any
	if err := json.Unmarshal(response, &body); err != nil {
		t.Fatalf("response %s: %v", response, err)
	}
	if body["snapshot_id"] != "snap-1" {
		t.Errorf("response snapshot_id = %v, want snap-1", body["snapshot_id"])
	}
	if _, ok := body["created_at"]; !ok {
		t.Error("response has no created_at")
	}
	if got := f.repo.snapshots["snap-1"].State; got != device.SnapshotStateReady {
		t.Errorf("row state = %q, want ready", got)
	}
	if !server.HasSnapshot(fakeho.SingleHostIP, "snap-1") {
		t.Error("host does not hold the snapshot the row says is ready")
	}
}

// The compensation that matters. A failed take can strand the CVD suspended,
// and the host offers no resume, so the handler must restart, drop the dead
// row, and record the failure.
func TestSnapshotTakeWorker_FailureCompensatesAndDropsRow(t *testing.T) {
	t.Parallel()
	server, err := fakeho.New()
	if err != nil {
		t.Fatalf("starting fake HO: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	server.SeedGroup(fakeho.SingleHostIP, "cvdd1", "dev", "Running")
	server.SetOperationFailure(fakeho.SingleHostIP, "cvd snapshot_take failed")

	f := newSnapshotFixture(t, server.Port())
	f.seedSnapshot("snap-1", device.SnapshotStateCreating, 2)
	h, store := snapshotWorker(t, f, operation.VerbDeviceSnapshot, "snap-1")

	if err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 1, Attempts: 1}); err == nil {
		t.Fatal("Handle reported success for a failed take")
	}
	if _, problem, _ := store.snapshot(); problem == nil {
		t.Error("no terminal problem recorded")
	}
	if _, ok := f.repo.snapshots["snap-1"]; ok {
		t.Error("dead creating row was not dropped")
	}
	// SetOperationFailure fails every operation on the host, so the
	// compensating restart fails too. What must survive is that it was
	// ATTEMPTED. The served-route record is the witness.
	if !slices.Contains(server.ServedRoutes(), "POST /cvds/{group}/{name}/:restart") {
		t.Error("no compensating restart was attempted after the failed take")
	}
	// With the restart refused there is no evidence anything serves the
	// device. The row must say error, not running over a dead launcher.
	if got := f.repo.Devices["d1"].Status; got != device.StatusError {
		t.Errorf("device status after failed take and failed restart = %s, want error", got)
	}
	if f.repo.Devices["d1"].ErrorMessage == "" {
		t.Error("error mark carries no message naming what happened")
	}
}

// The counterpart that bounds the mark. A failed take whose compensating
// restart SUCCEEDS proves the launcher alive, and the device must stay
// running. Goes red if the error mark is widened to every failed take.
func TestSnapshotTakeWorker_FailureWithLiveLauncherStaysRunning(t *testing.T) {
	t.Parallel()
	server, err := fakeho.New()
	if err != nil {
		t.Fatalf("starting fake HO: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	server.SeedGroup(fakeho.SingleHostIP, "cvdd1", "dev", "Running")
	server.SetSnapshotFailure(fakeho.SingleHostIP, "cvd snapshot_take failed")

	f := newSnapshotFixture(t, server.Port())
	f.seedSnapshot("snap-1", device.SnapshotStateCreating, 2)
	h, store := snapshotWorker(t, f, operation.VerbDeviceSnapshot, "snap-1")

	if err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 1, Attempts: 1}); err == nil {
		t.Fatal("Handle reported success for a failed take")
	}
	if _, problem, _ := store.snapshot(); problem == nil {
		t.Error("no terminal problem recorded")
	}
	if !slices.Contains(server.ServedRoutes(), "POST /cvds/{group}/{name}/:restart") {
		t.Error("no compensating restart was attempted after the failed take")
	}
	if got := f.repo.Devices["d1"].Status; got != device.StatusRunning {
		t.Errorf("device status after failed take with live launcher = %s, want running", got)
	}
}

func TestSnapshotRestoreWorker_StartsFromSnapshotAndMarksRunning(t *testing.T) {
	t.Parallel()
	server, err := fakeho.New()
	if err != nil {
		t.Fatalf("starting fake HO: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	server.SeedGroup(fakeho.SingleHostIP, "cvdd1", "dev", "Stopped")

	f := newSnapshotFixture(t, server.Port())
	f.repo.Devices["d1"].Status = device.StatusStopped
	f.seedSnapshot("snap-1", device.SnapshotStateReady, 2)
	h, store := snapshotWorker(t, f, operation.VerbDeviceSnapshotRestore, "snap-1")

	// The host does not hold the snapshot. Restore must fail with the 404
	// surfaced, and the device must stay stopped.
	if err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 1, Attempts: 1}); err == nil {
		t.Fatal("restore of a snapshot the host lost reported success")
	}
	if got := f.repo.Devices["d1"].Status; got != device.StatusStopped {
		t.Fatalf("device status after failed restore = %s, want stopped", got)
	}

	// Now the host holds it, so restore succeeds and the row transitions.
	store.rec.Done = false
	store.terminateErr = nil
	if _, err := f.svc.hoClientFactory.ClientForHost(
		fmt.Sprintf("http://%s:%d", fakeho.SingleHostIP, server.Port())).
		CreateSnapshot(t.Context(), "cvdd1", "dev", "snap-1"); err != nil {
		t.Fatalf("seeding host snapshot: %v", err)
	}
	if err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 1, Attempts: 1}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := f.repo.Devices["d1"].Status; got != device.StatusRunning {
		t.Errorf("device status after restore = %s, want running", got)
	}
	_, _, response := store.snapshot()
	var body map[string]any
	if err := json.Unmarshal(response, &body); err != nil {
		t.Fatalf("response %s: %v", response, err)
	}
	if _, ok := body["restored_at"]; !ok {
		t.Error("response has no restored_at")
	}
}

// The snapshot verbs race a delete exactly as the in-place ones do, and settle
// the same way. The operation records the refusal on the first attempt and the
// task is acked. A dead-letter here would report the system broken because a
// user deleted a device while its restore was queued.
//
// An error-status device is the contrast that keeps the arm accurate. It is not
// a superseding state, so the failure still reaches the queue.
func TestSnapshotRestoreWorker_SettlesARaceWithADelete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   device.Status
		rowGone  bool
		wantAck  bool
		wantHTTP int
	}{
		{name: "deleting", status: device.StatusDeleting, wantAck: true, wantHTTP: http.StatusNotFound},
		{name: "deleted row", rowGone: true, wantAck: true, wantHTTP: http.StatusNotFound},
		{name: "error heals back", status: device.StatusError, wantAck: false, wantHTTP: http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newSnapshotFixture(t, 1080)
			f.seedSnapshot("snap-1", device.SnapshotStateReady, 2)
			if tt.rowGone {
				delete(f.repo.Devices, "d1")
			} else {
				f.repo.Devices["d1"].Status = tt.status
			}
			h, store := snapshotWorker(t, f, operation.VerbDeviceSnapshotRestore, "snap-1")

			err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 1, Attempts: 1})
			if tt.wantAck && err != nil {
				t.Fatalf("a raced restore must finish its task: %v", err)
			}
			if !tt.wantAck && err == nil {
				t.Fatal("an error-status device can heal back to stopped; the failure belongs to the queue")
			}
			// Either way the operation settles, since restore has one
			// attempt, so the problem document separates the two outcomes.
			n, problem, _ := store.snapshot()
			if n != 1 || problem == nil {
				t.Fatalf("terminal writes = %d, problem = %+v, want one recorded failure", n, problem)
			}
			if problem.Status != tt.wantHTTP {
				t.Errorf("problem status = %d, want %d", problem.Status, tt.wantHTTP)
			}
		})
	}
}

func TestSnapshotDeleteWorker_RemovesHostSideAndRow(t *testing.T) {
	t.Parallel()
	server, err := fakeho.New()
	if err != nil {
		t.Fatalf("starting fake HO: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	server.SeedGroup(fakeho.SingleHostIP, "cvdd1", "dev", "Running")

	f := newSnapshotFixture(t, server.Port())
	f.seedSnapshot("snap-1", device.SnapshotStateReady, 2)
	if _, err := f.svc.hoClientFactory.ClientForHost(
		fmt.Sprintf("http://%s:%d", fakeho.SingleHostIP, server.Port())).
		CreateSnapshot(t.Context(), "cvdd1", "dev", "snap-1"); err != nil {
		t.Fatalf("seeding host snapshot: %v", err)
	}
	h, store := snapshotWorker(t, f, operation.VerbDeviceSnapshotDelete, "snap-1")

	if err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 3, Attempts: 1}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if server.HasSnapshot(fakeho.SingleHostIP, "snap-1") {
		t.Error("host still holds the snapshot")
	}
	if _, ok := f.repo.snapshots["snap-1"]; ok {
		t.Error("inventory row survived the delete")
	}
	if _, problem, _ := store.snapshot(); problem != nil {
		t.Errorf("unexpected terminal problem: %v", problem)
	}
}

// A stale incarnation means the bytes' disk no longer exists. The delete must
// remove only the row and never dial the host it would wrongly address.
func TestSnapshotDeleteWorker_StaleIncarnationSkipsHost(t *testing.T) {
	t.Parallel()
	server, err := fakeho.New()
	if err != nil {
		t.Fatalf("starting fake HO: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	f := newSnapshotFixture(t, server.Port())
	f.seedSnapshot("snap-1", device.SnapshotStateReady, 1) // host is on incarnation 2
	h, _ := snapshotWorker(t, f, operation.VerbDeviceSnapshotDelete, "snap-1")

	if err := h.Handle(t.Context(), &task.Task{ID: "t1", MaxAttempts: 3, Attempts: 1}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if _, ok := f.repo.snapshots["snap-1"]; ok {
		t.Error("inventory row survived the delete")
	}
	if slices.Contains(server.ServedRoutes(), "DELETE /snapshots/{id}") {
		t.Error("delete dialed the host for a disk that no longer exists")
	}
}
