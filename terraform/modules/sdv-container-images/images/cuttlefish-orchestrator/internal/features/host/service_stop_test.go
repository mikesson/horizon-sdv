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

package hostfeature

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/spanner"
	"google.golang.org/api/option"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// stopGCE serves the stop-settle path: the stop call, the operation wait and
// the instance read. It records the requestId of every stop call.
type stopGCE struct {
	mu         sync.Mutex
	waitJSON   string // the wait RPC's answer; DONE without error by default
	waitStatus int    // non-zero answers the wait RPC with this HTTP error
	instStatus string // the VM status the instance read reports
	requestIDs []string
	calls      []string
}

func (f *stopGCE) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop"):
			f.requestIDs = append(f.requestIDs, r.URL.Query().Get("requestId"))
			fmt.Fprint(w, `{"name":"op-stop","status":"RUNNING"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/wait"):
			if f.waitStatus != 0 {
				http.Error(w, fmt.Sprintf(`{"error":{"code":%d,"message":"wait failed"}}`, f.waitStatus), f.waitStatus)
				return
			}
			if f.waitJSON != "" {
				fmt.Fprint(w, f.waitJSON)
				return
			}
			fmt.Fprint(w, `{"name":"op-stop","status":"DONE"}`)
		case r.Method == http.MethodGet:
			fmt.Fprintf(w, `{"name":"cf-web-abc","status":%q,"networkInterfaces":[{"networkIP":"10.0.0.7"}]}`, f.instStatus)
		default:
			http.Error(w, `{"error":{"code":400,"message":"unexpected"}}`, http.StatusBadRequest)
		}
	})
}

func (f *stopGCE) stopRequestIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requestIDs...)
}

func (f *stopGCE) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *stopGCE) clients(t *testing.T) (*compute.InstancesClient, *compute.ZoneOperationsClient) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	instances, err := compute.NewInstancesRESTClient(t.Context(), option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("instances client: %v", err)
	}
	t.Cleanup(func() { _ = instances.Close() })
	operations, err := compute.NewZoneOperationsRESTClient(t.Context(), option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("operations client: %v", err)
	}
	t.Cleanup(func() { _ = operations.Close() })
	return instances, operations
}

func newStopService(t *testing.T, repo AtomicHostRepository, fake *stopGCE) (*HostService, *fakeEnqueuer) {
	t.Helper()
	instances, operations := fake.clients(t)
	enq := &fakeEnqueuer{}
	svc := NewHostService(HostServiceConfig{ProjectID: "test-project", HostOrchestratorPort: 2080},
		instances, operations, repo, slog.New(slog.DiscardHandler), WithTaskEnqueuer(enq))
	return svc, enq
}

// stoppingHost is a host a user stop has just marked stopping. HOReadyAt is
// set, as on any host that reached running.
func stoppingHost() *host.Host {
	ready := time.Now().Add(-time.Hour)
	return &host.Host{
		ID: testHostID, Name: "web", Zone: "z1", MaxDevices: 4,
		Status: host.StatusStopping, DesiredState: host.DesiredStopped,
		GCEInstanceName: "cf-web-abc", VMStatus: host.GCEVMStateRunning, HOReadyAt: &ready,
	}
}

func claimedSettleTask() *task.Task {
	tk := task.New(TaskTypeStopSettleHost, testHostID)
	tk.ID = "stop-settle-task-1"
	tk.Attempts = 1
	return tk
}

// Stop must commit the settle task with its transition, and the stop it sends
// GCE must carry the requestId the task will re-issue. A mismatch would leave
// the task waiting on a second, unrelated stop operation.
func TestStopEnqueuesTheSettleTaskItsRequestIDNames(t *testing.T) {
	t.Parallel()
	fake := &stopGCE{instStatus: host.GCEVMStateTerminated}
	running := stoppingHost()
	running.Status = host.StatusRunning
	running.DesiredState = host.DesiredRunning
	repo := newAtomicHostRepo(running)
	svc, enq := newStopService(t, repo, fake)

	if err := svc.Stop(t.Context(), testHostID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(enq.enqueued) != 1 || enq.enqueued[0].Type != TaskTypeStopSettleHost {
		t.Fatalf("enqueued = %+v, want one %s task", enq.enqueued, TaskTypeStopSettleHost)
	}
	if repo.committedExtras != 1 {
		t.Errorf("committed extras = %d, want the settle task in the stop commit", repo.committedExtras)
	}
	if enq.wakes != 1 {
		t.Errorf("wakes = %d, want 1 after the commit", enq.wakes)
	}
	want := stopRequestID(enq.enqueued[0].ID)
	if ids := fake.stopRequestIDs(); len(ids) != 1 || ids[0] != want {
		t.Errorf("stop requestIds = %v, want [%s] derived from the settle task", ids, want)
	}
	if got := repo.Hosts[testHostID]; got.Status != host.StatusStopping || got.DesiredState != host.DesiredStopped {
		t.Errorf("host = %s/%s, want stopping/stopped", got.Status, got.DesiredState)
	}
}

// A completed stop settles the host to stopped through the CAS observation
// write, from stopping, with the VM's own observation.
func TestStopSettleSettlesACompletedStop(t *testing.T) {
	t.Parallel()
	fake := &stopGCE{instStatus: host.GCEVMStateTerminated}
	repo := newAtomicHostRepo(stoppingHost())
	svc, _ := newStopService(t, repo, fake)
	tk := claimedSettleTask()

	if err := NewStopSettleHostHandler(svc).Handle(t.Context(), tk); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if ids := fake.stopRequestIDs(); len(ids) != 1 || ids[0] != stopRequestID(tk.ID) {
		t.Errorf("re-issued stop requestIds = %v, want the task's fixed id", ids)
	}
	if len(repo.gceUpdates) != 1 {
		t.Fatalf("observation writes = %+v, want one", repo.gceUpdates)
	}
	u := repo.gceUpdates[0]
	if u.PrevStatus != host.StatusStopping || u.Status != host.StatusStopped || u.VMStatus != host.GCEVMStateTerminated {
		t.Errorf("update = %s -> %s (vm %s), want stopping -> stopped (vm TERMINATED)", u.PrevStatus, u.Status, u.VMStatus)
	}
	if got := repo.Hosts[testHostID].Status; got != host.StatusStopped {
		t.Errorf("host status = %s, want stopped", got)
	}
}

// A failed stop operation returns the host to desired running and out of
// stopping. Before this, the host stayed in stopping for good.
func TestStopSettleReturnsAFailedStopToRunning(t *testing.T) {
	t.Parallel()
	fake := &stopGCE{
		instStatus: host.GCEVMStateRunning,
		waitJSON: `{"name":"op-stop","status":"DONE","httpErrorStatusCode":400,"httpErrorMessage":"BAD REQUEST",` +
			`"error":{"errors":[{"code":"RESOURCE_NOT_READY","message":"not ready"}]}}`,
	}
	repo := newAtomicHostRepo(stoppingHost())
	svc, _ := newStopService(t, repo, fake)

	if err := NewStopSettleHostHandler(svc).Handle(t.Context(), claimedSettleTask()); err != nil {
		t.Fatalf("Handle: %v (a failed stop completes the task)", err)
	}
	got := repo.Hosts[testHostID]
	if got.DesiredState != host.DesiredRunning {
		t.Errorf("desired_state = %s, want running", got.DesiredState)
	}
	if got.Status != host.StatusRunning {
		t.Errorf("status = %s, want running (stopping -> running)", got.Status)
	}
	if len(repo.gceUpdates) != 0 {
		t.Errorf("a failed stop wrote a stopped observation: %+v", repo.gceUpdates)
	}
}

// A failed stop whose restore write fails must be retried. Completing the task
// there would leave the host in stopping with nothing left to move it.
func TestStopSettleRetriesAFailedRestoreWrite(t *testing.T) {
	t.Parallel()
	fake := &stopGCE{
		instStatus: host.GCEVMStateRunning,
		waitJSON: `{"name":"op-stop","status":"DONE","httpErrorStatusCode":400,"httpErrorMessage":"BAD REQUEST",` +
			`"error":{"errors":[{"code":"RESOURCE_NOT_READY","message":"not ready"}]}}`,
	}
	repo := newAtomicHostRepo(stoppingHost())
	repo.mutateErr = errors.New("spanner unavailable")
	svc, _ := newStopService(t, repo, fake)
	handler := NewStopSettleHostHandler(svc)
	tk := claimedSettleTask()

	if err := handler.Handle(t.Context(), tk); err == nil {
		t.Fatal("Handle returned nil with the restore write unrecorded")
	}
	if got := repo.Hosts[testHostID]; got.Status != host.StatusStopping || got.DesiredState != host.DesiredStopped {
		t.Fatalf("host = %s/%s after the failed write, want stopping/stopped", got.Status, got.DesiredState)
	}

	repo.mutateErr = nil
	if err := handler.Handle(t.Context(), tk); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := repo.Hosts[testHostID]; got.Status != host.StatusRunning || got.DesiredState != host.DesiredRunning {
		t.Errorf("host = %s/%s after the retry, want running/running", got.Status, got.DesiredState)
	}
}

// A wait that cannot complete is retried; only the exhausted attempt gives the
// stop up and returns the host to desired running.
func TestStopSettleGivesUpOnlyWhenExhausted(t *testing.T) {
	t.Parallel()
	fake := &stopGCE{instStatus: host.GCEVMStateRunning, waitStatus: http.StatusBadRequest}
	repo := newAtomicHostRepo(stoppingHost())
	svc, _ := newStopService(t, repo, fake)
	handler := NewStopSettleHostHandler(svc)

	tk := claimedSettleTask()
	if err := handler.Handle(t.Context(), tk); err == nil {
		t.Fatal("a failed wait must be retried")
	}
	if got := repo.Hosts[testHostID]; got.Status != host.StatusStopping || got.DesiredState != host.DesiredStopped {
		t.Fatalf("host = %s/%s after a retryable failure, want stopping/stopped", got.Status, got.DesiredState)
	}

	tk.Attempts = tk.MaxAttempts
	if err := handler.Handle(t.Context(), tk); err == nil {
		t.Fatal("the exhausted attempt still reports its failure")
	}
	if got := repo.Hosts[testHostID]; got.DesiredState != host.DesiredRunning {
		t.Errorf("desired_state = %s after exhaustion, want running", got.DesiredState)
	}
}

// A host that already left stopping (the reconciler adopted the stop, or a
// delete took over) is not this task's to settle, and GCE is not called.
func TestStopSettleIsANoOpOnceSuperseded(t *testing.T) {
	t.Parallel()
	for _, mutate := range []func(*host.Host){
		func(h *host.Host) { h.Status = host.StatusStopped },
		func(h *host.Host) { h.Status = host.StatusDeleting; h.DesiredState = host.DesiredAbsent },
	} {
		fake := &stopGCE{instStatus: host.GCEVMStateTerminated}
		h := stoppingHost()
		mutate(h)
		repo := newAtomicHostRepo(h)
		svc, _ := newStopService(t, repo, fake)

		if err := svc.RunStopSettle(t.Context(), claimedSettleTask()); err != nil {
			t.Fatalf("RunStopSettle(%s): %v", h.Status, err)
		}
		if n := fake.count(http.MethodPost); n != 0 {
			t.Errorf("host %s: %d GCE calls, want none", h.Status, n)
		}
		if repo.Hosts[testHostID].Status != h.Status {
			t.Errorf("host %s rewritten to %s", h.Status, repo.Hosts[testHostID].Status)
		}
	}
}

// A VM still in stop motion after its operation is DONE is re-read later,
// without burning an attempt.
func TestStopSettleWaitsOutStopMotion(t *testing.T) {
	t.Parallel()
	fake := &stopGCE{instStatus: host.GCEVMStateStopping}
	repo := newAtomicHostRepo(stoppingHost())
	svc, _ := newStopService(t, repo, fake)
	tk := claimedSettleTask()
	tk.CreatedAt = time.Now()

	err := NewStopSettleHostHandler(svc).Handle(t.Context(), tk)
	if _, ok := errors.AsType[*task.RescheduleError](err); !ok {
		t.Fatalf("Handle = %v, want a reschedule", err)
	}
	if got := repo.Hosts[testHostID].Status; got != host.StatusStopping {
		t.Errorf("status = %s, want stopping until the VM reads parked", got)
	}
}

// hostRowStatus reads a host's stored status.
func hostRowStatus(t *testing.T, db *spannertest.TestDB, id string) string {
	t.Helper()
	row, err := db.Client.Single().ReadRow(t.Context(), "hosts", spanner.Key{id}, []string{"status"})
	if err != nil {
		t.Fatalf("reading host %s: %v", id, err)
	}
	var status string
	if err := row.Columns(&status); err != nil {
		t.Fatalf("scanning host %s: %v", id, err)
	}
	return status
}

// seedStopDevices inserts one device row per status on the host.
func seedStopDevices(t *testing.T, db *spannertest.TestDB, hostID string, statuses map[string]string) {
	t.Helper()
	now := time.Now()
	muts := make([]*spanner.Mutation, 0, len(statuses))
	for id, status := range statuses {
		muts = append(muts, spanner.InsertMap("devices", map[string]any{
			"host_id": hostID, "device_id": id, "name": id,
			"status": status, "created_at": now, "updated_at": now,
		}))
	}
	if _, err := db.Client.Apply(t.Context(), muts); err != nil {
		t.Fatalf("seeding devices: %v", err)
	}
}

// deviceRow reads a device's status and whether stopped_at is set.
func deviceRow(t *testing.T, db *spannertest.TestDB, hostID, deviceID string) (string, bool) {
	t.Helper()
	row, err := db.Client.Single().ReadRow(t.Context(), "devices",
		spanner.Key{hostID, deviceID}, []string{"status", "stopped_at"})
	if err != nil {
		t.Fatalf("reading device %s: %v", deviceID, err)
	}
	var status string
	var stoppedAt spanner.NullTime
	if err := row.Columns(&status, &stoppedAt); err != nil {
		t.Fatalf("scanning device %s: %v", deviceID, err)
	}
	return status, stoppedAt.Valid
}

// The reconciler's adoption of a stopped host settles the host's running
// devices in the same commit and leaves a deleting one alone. The return value
// counts host rows only, which is what SyncZone's stale-miss arithmetic reads.
// A CAS miss on the host must leave every device untouched, and an update that
// does not enter stopped must not touch devices.
func TestBatchUpdateGCEStateSettlesDevicesOfAStoppedHost(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := NewHostRepository(db.Client)

	for _, id := range []string{"settle-host", "missed-host", "steady-host"} {
		if err := repo.Create(ctx, &host.Host{
			ID: id, Name: id, Zone: "us-central1-a", Status: host.StatusRunning, MaxDevices: 4,
		}); err != nil {
			t.Fatalf("Create(%s): %v", id, err)
		}
	}
	for _, id := range []string{"settle-host", "missed-host"} {
		seedStopDevices(t, db, id, map[string]string{
			"dev-run-a": "running", "dev-run-b": "running", "dev-deleting": "deleting",
		})
	}
	seedStopDevices(t, db, "steady-host", map[string]string{"dev-run-a": "running"})

	applied, err := repo.BatchUpdateGCEState(ctx, []host.GCEStateUpdate{
		{ID: "settle-host", PrevStatus: host.StatusRunning, Status: host.StatusStopped, VMStatus: host.GCEVMStateTerminated},
		// Planned from a stale snapshot that read starting; the row reads running.
		{ID: "missed-host", PrevStatus: host.StatusStarting, Status: host.StatusStopped, VMStatus: host.GCEVMStateTerminated},
		{ID: "steady-host", PrevStatus: host.StatusRunning, Status: host.StatusRunning, VMStatus: host.GCEVMStateRunning, IP: "34.1.1.1"},
	})
	if err != nil {
		t.Fatalf("BatchUpdateGCEState: %v", err)
	}
	if applied != 2 {
		t.Errorf("applied = %d, want 2 host rows (device rows are not counted)", applied)
	}

	for _, id := range []string{"dev-run-a", "dev-run-b"} {
		status, stopped := deviceRow(t, db, "settle-host", id)
		if status != "stopped" || !stopped {
			t.Errorf("settle-host %s = %s (stopped_at set %v), want stopped with stopped_at", id, status, stopped)
		}
	}
	if status, _ := deviceRow(t, db, "settle-host", "dev-deleting"); status != "deleting" {
		t.Errorf("settle-host dev-deleting = %s, want deleting untouched", status)
	}

	if got := hostRowStatus(t, db, "missed-host"); got != "running" {
		t.Errorf("missed-host status = %s, want running (CAS miss)", got)
	}
	for id, want := range map[string]string{"dev-run-a": "running", "dev-run-b": "running", "dev-deleting": "deleting"} {
		if status, _ := deviceRow(t, db, "missed-host", id); status != want {
			t.Errorf("missed-host %s = %s, want %s (a host CAS miss must not settle devices)", id, status, want)
		}
	}

	if status, _ := deviceRow(t, db, "steady-host", "dev-run-a"); status != "running" {
		t.Errorf("steady-host dev-run-a = %s, want running (no stop observed)", status)
	}
}

// A host stopped and started again before the device controller's next cycle
// must start with its devices already stopped. The controller never sees the
// host stopped in that window, so the adoption commit is the only place the
// settle can happen. Host stopping -> starting is illegal, so every start goes
// through an adopted stopped.
func TestHostStartedInsideOneCycleFindsItsDevicesStopped(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := NewHostRepository(db.Client)

	if err := repo.Create(ctx, &host.Host{
		ID: testHostID, Name: "web", Zone: "z1", Status: host.StatusRunning,
		DesiredState: host.DesiredRunning, GCEInstanceName: "cf-web-abc", MaxDevices: 4,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedStopDevices(t, db, testHostID, map[string]string{"dev-a": "running", "dev-b": "stopping"})

	if _, err := repo.BatchUpdateGCEState(ctx, []host.GCEStateUpdate{{
		ID: testHostID, PrevStatus: host.StatusRunning, Status: host.StatusStopped, VMStatus: host.GCEVMStateTerminated,
	}}); err != nil {
		t.Fatalf("adopting the stop: %v", err)
	}
	svc := NewHostService(HostServiceConfig{ProjectID: "test-project", HostOrchestratorPort: 2080},
		newFakeGCEClient(t, &fakeGCE{}), nil, repo, slog.New(slog.DiscardHandler))
	if err := svc.Start(ctx, testHostID); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if got := hostRowStatus(t, db, testHostID); got != "starting" {
		t.Fatalf("host status = %s, want starting", got)
	}
	for _, id := range []string{"dev-a", "dev-b"} {
		if status, _ := deviceRow(t, db, testHostID, id); status != "stopped" {
			t.Errorf("%s = %s on a restarted host, want stopped", id, status)
		}
	}
}

// The stop-settle task commits the same settle against the real repository:
// host stopping -> stopped and its running devices stopped, in one commit.
func TestStopSettleSettlesDevicesInTheSameCommit(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := NewHostRepository(db.Client)

	if err := repo.Create(ctx, stoppingHost()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedStopDevices(t, db, testHostID, map[string]string{"dev-a": "running", "dev-gone": "deleting"})
	svc, _ := newStopService(t, repo, &stopGCE{instStatus: host.GCEVMStateTerminated})

	if err := svc.RunStopSettle(ctx, claimedSettleTask()); err != nil {
		t.Fatalf("RunStopSettle: %v", err)
	}
	if got := hostRowStatus(t, db, testHostID); got != "stopped" {
		t.Errorf("host status = %s, want stopped", got)
	}
	if status, stopped := deviceRow(t, db, testHostID, "dev-a"); status != "stopped" || !stopped {
		t.Errorf("dev-a = %s (stopped_at set %v), want stopped with stopped_at", status, stopped)
	}
	if status, _ := deviceRow(t, db, testHostID, "dev-gone"); status != "deleting" {
		t.Errorf("dev-gone = %s, want deleting untouched", status)
	}
}

// The adoption write skips the device settle for a Spot host that stopped
// while desired running: that is a preemption the ops scan has not classified
// yet, and preemption recovery re-creates its devices from running. A Spot
// host the user stopped settles like any other.
func TestBatchUpdateGCEStateLeavesAnUnrequestedSpotStopToPreemption(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := NewHostRepository(db.Client)

	for id, desired := range map[string]string{"spot-preempted": host.DesiredRunning, "spot-user-stop": host.DesiredStopped} {
		if err := repo.Create(ctx, &host.Host{
			ID: id, Name: id, Zone: "us-central1-a", Status: host.StatusRunning, MaxDevices: 4,
			DesiredState: desired, ProvisioningModel: host.ProvisioningModelSpot,
		}); err != nil {
			t.Fatalf("Create(%s): %v", id, err)
		}
		seedStopDevices(t, db, id, map[string]string{"dev-a": "running"})
	}

	applied, err := repo.BatchUpdateGCEState(ctx, []host.GCEStateUpdate{
		{ID: "spot-preempted", PrevStatus: host.StatusRunning, Status: host.StatusStopped, VMStatus: host.GCEVMStateTerminated},
		{ID: "spot-user-stop", PrevStatus: host.StatusRunning, Status: host.StatusStopped, VMStatus: host.GCEVMStateTerminated},
	})
	if err != nil {
		t.Fatalf("BatchUpdateGCEState: %v", err)
	}
	if applied != 2 {
		t.Errorf("applied = %d, want 2", applied)
	}
	if status, _ := deviceRow(t, db, "spot-preempted", "dev-a"); status != "running" {
		t.Errorf("spot-preempted dev-a = %s, want running (left to preemption recovery)", status)
	}
	if status, _ := deviceRow(t, db, "spot-user-stop", "dev-a"); status != "stopped" {
		t.Errorf("spot-user-stop dev-a = %s, want stopped", status)
	}
}

// A stop recorded by the manual host sync settles the devices in its commit,
// the same as the reconciler's adoption.
func TestSyncOfAStoppedHostSettlesItsDevices(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := NewHostRepository(db.Client)

	if err := repo.Create(ctx, stoppingHost()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedStopDevices(t, db, testHostID, map[string]string{"dev-a": "running", "dev-gone": "deleting"})
	svc := NewHostService(HostServiceConfig{ProjectID: "test-project", HostOrchestratorPort: 2080},
		newFakeGCEClient(t, &fakeGCE{instanceJSON: `{"name":"cf-web-abc","status":"TERMINATED"}`}),
		nil, repo, slog.New(slog.DiscardHandler))

	if err := svc.Sync(ctx, testHostID); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := hostRowStatus(t, db, testHostID); got != "stopped" {
		t.Fatalf("host status = %s, want stopped", got)
	}
	if status, stopped := deviceRow(t, db, testHostID, "dev-a"); status != "stopped" || !stopped {
		t.Errorf("dev-a = %s (stopped_at set %v), want stopped with stopped_at", status, stopped)
	}
	if status, _ := deviceRow(t, db, testHostID, "dev-gone"); status != "deleting" {
		t.Errorf("dev-gone = %s, want deleting untouched", status)
	}
}
