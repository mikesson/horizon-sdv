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

package cvd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/device/devicetest"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/tests/harness/fakeho"
)

// nopBuilds is a build reader for actuator tests whose devices carry no
// build ID (the artifact resolution short-circuits on empty).
type nopBuilds struct{}

func (nopBuilds) Get(context.Context, string) (*build.Build, error) { return nil, build.ErrNotFound }
func (nopBuilds) List(context.Context, build.ListFilter) ([]*build.Build, error) {
	return nil, nil
}
func (nopBuilds) Count(context.Context, build.ListFilter) (int64, error) { return 0, nil }

// newTestActuator builds an actuator over the shared fakes.
func newTestActuator(t *testing.T, devices DeviceStore, hosts HostStore) *Actuator {
	t.Helper()
	return NewActuator(devices, hosts, nopBuilds{},
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
}

func TestRunCreateCVDIdempotent(t *testing.T) {
	t.Parallel()
	hostRepo := hosttest.NewFakeRepo()
	deviceRepo := devicetest.NewFakeRepo()
	act := newTestActuator(t, deviceRepo, hostRepo)
	ctx := t.Context()

	// Device gone -> no-op (handler completes the task).
	deviceRepo.GetErr = device.ErrNotFound
	require.NoError(t, act.RunCreateCVD(ctx, "missing", nil))
	deviceRepo.GetErr = nil

	// Device already past creating -> no-op; status untouched, no host lookup.
	deviceRepo.Devices["done"] = &device.Device{
		ID: "done", HostID: "h", Status: device.StatusRunning,
	}
	require.NoError(t, act.RunCreateCVD(ctx, "done", nil))
	assert.Equal(t, device.StatusRunning, deviceRepo.Devices["done"].Status)
}

func TestCreateCVDHandlerDeadLettersOnExhaustion(t *testing.T) {
	t.Parallel()
	hostRepo := hosttest.NewFakeRepo() // no host registered -> host load fails fast
	deviceRepo := devicetest.NewFakeRepo()
	act := newTestActuator(t, deviceRepo, hostRepo)
	h := NewCreateHandler(act, nil)
	ctx := t.Context()

	// Exhausted attempt -> device marked error.
	deviceRepo.Devices["d"] = &device.Device{ID: "d", HostID: "h", Status: device.StatusPending}
	exhausted := &task.Task{ResourceID: "d", Attempts: task.DefaultMaxAttempts, MaxAttempts: task.DefaultMaxAttempts}
	require.Error(t, h.Handle(ctx, exhausted))
	assert.Equal(t, device.StatusError, deviceRepo.Devices["d"].Status)

	// Non-exhausted failure -> stays "creating" for a later retry.
	deviceRepo.Devices["d2"] = &device.Device{ID: "d2", HostID: "h", Status: device.StatusPending}
	retryable := &task.Task{ResourceID: "d2", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts}
	require.Error(t, h.Handle(ctx, retryable))
	assert.Equal(t, device.StatusCreating, deviceRepo.Devices["d2"].Status)
}

// A still-provisioning host is a wait: the handler postpones (no attempt
// burned) until hostWaitBudget elapses, then, with no provision-task checker
// wired, fails the device and completes the task (the host's own dead-letter
// carries the cause).
func TestCreateCVDHandlerWaitsForProvisioningHost(t *testing.T) {
	t.Parallel()
	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{ID: "h", Status: host.StatusProvisioning, MaxDevices: 4}
	deviceRepo := devicetest.NewFakeRepo()
	act := newTestActuator(t, deviceRepo, hostRepo)
	h := NewCreateHandler(act, nil)
	ctx := t.Context()

	// Within the budget -> reschedule, device keeps waiting in "creating".
	deviceRepo.Devices["d"] = &device.Device{ID: "d", HostID: "h", Status: device.StatusPending}
	fresh := &task.Task{ResourceID: "d", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts, CreatedAt: time.Now()}
	err := h.Handle(ctx, fresh)
	resched, ok := errors.AsType[*task.RescheduleError](err)
	require.True(t, ok, "provisioning host must postpone, got %v", err)
	assert.Equal(t, hostNotReadyRetry, resched.After)
	assert.Equal(t, device.StatusCreating, deviceRepo.Devices["d"].Status)

	// Budget exhausted -> device fails, task completes (nil).
	deviceRepo.Devices["d2"] = &device.Device{ID: "d2", HostID: "h", Status: device.StatusPending}
	stale := &task.Task{
		ResourceID: "d2", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts,
		CreatedAt: time.Now().Add(-hostWaitBudget - time.Minute),
	}
	require.NoError(t, h.Handle(ctx, stale))
	assert.Equal(t, device.StatusError, deviceRepo.Devices["d2"].Status)
}

// A host that will never serve the create (error/deleting/deleted) fails the
// device immediately instead of retrying to exhaustion.
func TestCreateCVDHandlerFailsFastOnHostGone(t *testing.T) {
	t.Parallel()
	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{ID: "h", Status: host.StatusError, MaxDevices: 4}
	deviceRepo := devicetest.NewFakeRepo()
	act := newTestActuator(t, deviceRepo, hostRepo)
	h := NewCreateHandler(act, nil)

	deviceRepo.Devices["d"] = &device.Device{ID: "d", HostID: "h", Status: device.StatusPending}
	tk := &task.Task{ResourceID: "d", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts, CreatedAt: time.Now()}
	require.NoError(t, h.Handle(t.Context(), tk), "host-gone completes the task; the device carries the failure")
	assert.Equal(t, device.StatusError, deviceRepo.Devices["d"].Status)
}

// fakeTaskChecker stubs ActiveTaskChecker for the cross-loop wait tests.
type fakeTaskChecker struct {
	active bool
	err    error
}

func (f *fakeTaskChecker) HasActive(context.Context, string, string) (bool, error) {
	return f.active, f.err
}

// Past hostWaitBudget the wait must follow the host's provision task:
// a live provision keeps the device waiting, because its retry envelope
// exceeds the base budget, while a dead one fails the device promptly, and
// hostProvisionWaitCap bounds everything.
func TestCreateCVDHandlerFollowsProvisionTask(t *testing.T) {
	t.Parallel()
	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{ID: "h", Status: host.StatusProvisioning, MaxDevices: 4}
	deviceRepo := devicetest.NewFakeRepo()
	act := newTestActuator(t, deviceRepo, hostRepo)
	ctx := t.Context()
	pastBudget := time.Now().Add(-hostWaitBudget - time.Minute)

	// Provision still in flight -> keep postponing past the base budget.
	deviceRepo.Devices["d"] = &device.Device{ID: "d", HostID: "h", Status: device.StatusPending}
	h := NewCreateHandler(act, &fakeTaskChecker{active: true})
	err := h.Handle(ctx, &task.Task{ResourceID: "d", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts, CreatedAt: pastBudget})
	_, ok := errors.AsType[*task.RescheduleError](err)
	require.True(t, ok, "live provision must keep the device waiting, got %v", err)
	assert.Equal(t, device.StatusCreating, deviceRepo.Devices["d"].Status)

	// Indeterminate check -> also keep waiting (the cap still bounds it).
	deviceRepo.Devices["d2"] = &device.Device{ID: "d2", HostID: "h", Status: device.StatusPending}
	h = NewCreateHandler(act, &fakeTaskChecker{err: errors.New("spanner blip")})
	err = h.Handle(ctx, &task.Task{ResourceID: "d2", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts, CreatedAt: pastBudget})
	_, ok = errors.AsType[*task.RescheduleError](err)
	require.True(t, ok, "indeterminate check must not fail the device, got %v", err)

	// No provision in flight -> nothing will make progress; fail now.
	deviceRepo.Devices["d3"] = &device.Device{ID: "d3", HostID: "h", Status: device.StatusPending}
	h = NewCreateHandler(act, &fakeTaskChecker{active: false})
	require.NoError(t, h.Handle(ctx, &task.Task{ResourceID: "d3", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts, CreatedAt: pastBudget}))
	assert.Equal(t, device.StatusError, deviceRepo.Devices["d3"].Status)

	// Past the absolute cap -> fail even with a live provision.
	deviceRepo.Devices["d4"] = &device.Device{ID: "d4", HostID: "h", Status: device.StatusPending}
	h = NewCreateHandler(act, &fakeTaskChecker{active: true})
	capped := &task.Task{
		ResourceID: "d4", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts,
		CreatedAt: time.Now().Add(-hostProvisionWaitCap - time.Minute),
	}
	require.NoError(t, h.Handle(ctx, capped))
	assert.Equal(t, device.StatusError, deviceRepo.Devices["d4"].Status)
}

func TestRunCleanupCVDNoOps(t *testing.T) {
	t.Parallel()
	hostRepo := hosttest.NewFakeRepo()
	deviceRepo := devicetest.NewFakeRepo()
	act := newTestActuator(t, deviceRepo, hostRepo)
	ctx := t.Context()

	// Device purged -> no-op (handler completes the task).
	deviceRepo.GetErr = device.ErrNotFound
	require.NoError(t, act.RunCleanupCVD(ctx, "gone"))
	deviceRepo.GetErr = nil

	// Host gone -> no-op (the CVD went with it).
	deviceRepo.Devices["d3"] = &device.Device{ID: "d3", HostID: "missing", Status: device.StatusDeleted, CVDID: "cvd/3"}
	require.NoError(t, act.RunCleanupCVD(ctx, "d3"))
}

// A not-serving host splits by VM continuity: statuses whose VM still exists
// (host.ExpectsVM) must make cleanup WAIT. A stopped or unreachable VM keeps
// its CVD state, and completing early is how a returning host leaks the group
// (the chaos harness caught exactly this via a preempted host's stale
// address). Statuses whose VM is absent, fresh on recovery, or being torn
// down complete: nothing of the group survives into any future VM.
func TestRunCleanupCVDHostNotServing(t *testing.T) {
	t.Parallel()
	waits := []host.Status{
		host.StatusStarting, host.StatusStopping, host.StatusStopped,
		host.StatusUnreachable,
		host.StatusRunning, // running with no IP yet: not serving, VM exists
	}
	completes := []host.Status{
		host.StatusPending, host.StatusProvisioning, host.StatusPreempted,
		host.StatusError, host.StatusDeleting, host.StatusDeleted,
	}

	newSvc := func(t *testing.T, status host.Status) *Actuator {
		t.Helper()
		hostRepo := hosttest.NewFakeRepo()
		hostRepo.Hosts["h"] = &host.Host{ID: "h", Status: status, MaxDevices: 4}
		deviceRepo := devicetest.NewFakeRepo()
		deviceRepo.Devices["d1"] = &device.Device{ID: "d1", HostID: "h", Status: device.StatusDeleted}
		return newTestActuator(t, deviceRepo, hostRepo)
	}

	for _, status := range waits {
		t.Run("waits/"+string(status), func(t *testing.T) {
			t.Parallel()
			err := newSvc(t, status).RunCleanupCVD(t.Context(), "d1")
			notServing, ok := errors.AsType[*hostNotServingError](err)
			require.True(t, ok, "VM-continuity status %s must make cleanup wait, got %v", status, err)
			assert.Equal(t, status, notServing.status)
		})
	}
	for _, status := range completes {
		t.Run("completes/"+string(status), func(t *testing.T) {
			t.Parallel()
			require.NoError(t, newSvc(t, status).RunCleanupCVD(t.Context(), "d1"),
				"status %s has no VM the group could survive on; cleanup completes", status)
		})
	}
}

// The cleanup task postpones while the deleted device's host is parked with
// its VM intact, and gives up (completes) once the wait cap passes. The
// census and host teardown own reclamation from there.
func TestCleanupCVDHandlerWaitsForParkedHost(t *testing.T) {
	t.Parallel()
	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{ID: "h", Status: host.StatusStopped, MaxDevices: 4}
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["d1"] = &device.Device{ID: "d1", HostID: "h", Status: device.StatusDeleted}
	h := NewCleanupHandler(newTestActuator(t, deviceRepo, hostRepo))

	fresh := &task.Task{ResourceID: "d1", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts, CreatedAt: time.Now()}
	err := h.Handle(t.Context(), fresh)
	resched, ok := errors.AsType[*task.RescheduleError](err)
	require.True(t, ok, "parked host must postpone the cleanup, got %v", err)
	assert.Equal(t, hostServingRetry, resched.After)

	capped := &task.Task{
		ResourceID: "d1", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts,
		CreatedAt: time.Now().Add(-hostServingWaitCap - time.Minute),
	}
	require.NoError(t, h.Handle(t.Context(), capped),
		"past the cap the task completes; census/teardown reclaim the group")
}

// The create task's torn-down-device sweep hits the same parked-host wait and
// must postpone instead of failing, because the device is already deleted and
// there is nothing to mark failed.
func TestCreateCVDHandlerSweepWaitsForParkedHost(t *testing.T) {
	t.Parallel()
	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{ID: "h", Status: host.StatusStopped, MaxDevices: 4}
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["d1"] = &device.Device{ID: "d1", HostID: "h", Status: device.StatusDeleting}
	h := NewCreateHandler(newTestActuator(t, deviceRepo, hostRepo), nil)

	fresh := &task.Task{ResourceID: "d1", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts, CreatedAt: time.Now()}
	err := h.Handle(t.Context(), fresh)
	resched, ok := errors.AsType[*task.RescheduleError](err)
	require.True(t, ok, "sweep against a parked host must postpone, got %v", err)
	assert.Equal(t, hostServingRetry, resched.After)
	assert.Equal(t, device.StatusDeleting, deviceRepo.Devices["d1"].Status,
		"a deleted device is never marked failed by the sweep wait")

	capped := &task.Task{
		ResourceID: "d1", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts,
		CreatedAt: time.Now().Add(-hostServingWaitCap - time.Minute),
	}
	require.NoError(t, h.Handle(t.Context(), capped))
}

// The keyed reconcile's teardown convergence postpones on a parked host the
// same way.
func TestReconcileTeardownWaitsForParkedHost(t *testing.T) {
	t.Parallel()
	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{ID: "h", Status: host.StatusUnreachable, MaxDevices: 4}
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["d1"] = &device.Device{ID: "d1", HostID: "h", Status: device.StatusDeleting}
	h := NewReconcileHandler(newTestActuator(t, deviceRepo, hostRepo), nil, nil, nil)

	fresh := &task.Task{ResourceID: "d1", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts, CreatedAt: time.Now()}
	err := h.Handle(t.Context(), fresh)
	resched, ok := errors.AsType[*task.RescheduleError](err)
	require.True(t, ok, "reconcile teardown against an unreachable host must postpone, got %v", err)
	assert.Equal(t, hostServingRetry, resched.After)
}

// RunCleanupCVD must reclaim a group orphaned by an interrupted create (no
// CVDID recorded) via its deterministic name, and treat an absent group as
// already clean instead of grinding HO's failed-delete 500 through retries.
func TestRunCleanupCVDDeterministicGroup(t *testing.T) {
	t.Parallel()
	ho, err := fakeho.Single()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ho.Close() })
	// The orphaned group from a dead create attempt: present on the host,
	// absent from the create ledger, no CVDID on the row.
	ho.SeedGroup(fakeho.SingleHostIP, "cvddeadbeef", "dev-1", "Stopped")

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{
		ID: "h", Status: host.StatusRunning, MaxDevices: 4,
		IPAddress: fakeho.SingleHostIP, HostOrchestratorPort: int64(ho.Port()),
	}
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["deadbeef-del"] = &device.Device{
		ID: "deadbeef-del", HostID: "h", Status: device.StatusDeleted, // no CVDID
	}
	act := newTestActuator(t, deviceRepo, hostRepo)

	require.NoError(t, act.RunCleanupCVD(t.Context(), "deadbeef-del"))
	_, deleted := ho.Ledger()
	assert.Equal(t, []string{"cvddeadbeef"}, deleted[fakeho.SingleHostIP],
		"orphaned group deleted by deterministic name")
	assert.Empty(t, ho.LiveGroups(), "nothing left on the host")

	// Second run: group gone from the host -> no delete attempted.
	require.NoError(t, act.RunCleanupCVD(t.Context(), "deadbeef-del"))
	_, deleted = ho.Ledger()
	assert.Len(t, deleted[fakeho.SingleHostIP], 1, "absent group is already clean; no second delete")
}

// A StartCVD failure on an adopted group must leave the device `creating` and
// return an error so the worker retries. Setting `error` mid-flight would make
// RunCreateCVD's already-handled guard complete the next attempt as a no-op and
// strand the device (live-fire regression, 2026-07-07).
func TestRunCreateCVDStartFailureStaysCreating(t *testing.T) {
	t.Parallel()
	var deletedGroup atomic.Bool
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cvds" && r.Method == http.MethodGet:
			w.Write([]byte(`{"cvds": [{"group": "cvddeadbeef", "name": "dev-1", "status": "Stopped"}]}`))
		case strings.HasSuffix(r.URL.Path, "/:start"):
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error": "Malformed JSON in request"}`))
		case r.URL.Path == "/cvds/cvddeadbeef" && r.Method == http.MethodDelete:
			deletedGroup.Store(true)
			w.Write([]byte(`{"name": "op-del"}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer ho.Close()
	hostURL, _ := url.Parse(ho.URL)
	port, _ := strconv.ParseInt(hostURL.Port(), 10, 64)

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{
		ID: "h", Status: host.StatusRunning, MaxDevices: 4,
		IPAddress: "127.0.0.1", HostOrchestratorPort: port,
	}
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["deadbeef-dev"] = &device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "dev-1", Status: device.StatusPending,
	}
	act := newTestActuator(t, deviceRepo, hostRepo)

	err := act.RunCreateCVD(t.Context(), "deadbeef-dev", nil)
	require.Error(t, err, "start failure must surface to the worker for retry")
	assert.Equal(t, device.StatusCreating, deviceRepo.Devices["deadbeef-dev"].Status,
		"device must stay creating so the retry's guard lets it through")
	assert.True(t, deletedGroup.Load(),
		"the unstartable group must be deleted so the retry creates it fresh")
}

// newInFlightGroupActuator builds an actuator whose fake HO lists the device's
// group in the given transient status and fails the test if anything actuates
// on it (:start or DELETE). That is the exact adopt-race sequence.
func newInFlightGroupActuator(t *testing.T, status string) (*Actuator, *devicetest.FakeRepo) {
	t.Helper()
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cvds" && r.Method == http.MethodGet:
			w.Write([]byte(`{"cvds": [{"group": "cvddeadbeef", "name": "dev-1", "status": "` + status + `"}]}`))
		case strings.HasSuffix(r.URL.Path, "/:start"):
			t.Error("StartCVD actuated on a group whose launch is still in flight")
			w.Write([]byte(`{}`))
		case r.Method == http.MethodDelete:
			t.Error("DeleteCVD actuated on a group whose launch is still in flight")
			w.Write([]byte(`{}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(ho.Close)
	hostURL, _ := url.Parse(ho.URL)
	port, _ := strconv.ParseInt(hostURL.Port(), 10, 64)

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{
		ID: "h", Status: host.StatusRunning, MaxDevices: 4,
		IPAddress: "127.0.0.1", HostOrchestratorPort: port,
	}
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["deadbeef-dev"] = &device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "dev-1", Status: device.StatusPending,
	}
	return newTestActuator(t, deviceRepo, hostRepo), deviceRepo
}

// An existing group in a transient state means a prior attempt's operation is
// still running server-side (an abandoned wait or crashed worker; the launch
// itself never stopped). Actuating on it races the live launcher; the create
// must wait for it to settle instead.
func TestRunCreateCVDPostponesOnInFlightLaunch(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"Starting", "Stopping"} {
		t.Run(status, func(t *testing.T) {
			act, deviceRepo := newInFlightGroupActuator(t, status)
			err := act.RunCreateCVD(t.Context(), "deadbeef-dev", nil)
			_, ok := errors.AsType[*cvdLaunchInFlightError](err)
			require.True(t, ok, "want cvdLaunchInFlightError, got %v", err)
			assert.Equal(t, device.StatusCreating, deviceRepo.Devices["deadbeef-dev"].Status,
				"device keeps waiting in creating")
		})
	}
}

// The handler postpones a launch-in-flight create (no attempt burned) until
// the group settles; total task age bounds the wait so a hung launcher
// fails the device instead of postponing forever.
func TestCreateCVDHandlerWaitsForInFlightLaunch(t *testing.T) {
	t.Parallel()
	act, deviceRepo := newInFlightGroupActuator(t, "Starting")
	h := NewCreateHandler(act, nil)
	ctx := t.Context()

	fresh := &task.Task{ResourceID: "deadbeef-dev", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts, CreatedAt: time.Now()}
	err := h.Handle(ctx, fresh)
	resched, ok := errors.AsType[*task.RescheduleError](err)
	require.True(t, ok, "in-flight launch must postpone, got %v", err)
	assert.Equal(t, launchInFlightRetry, resched.After)
	assert.Equal(t, device.StatusCreating, deviceRepo.Devices["deadbeef-dev"].Status)

	// Past the cap -> fail the device, complete the task.
	deviceRepo.Devices["deadbeef-dev"].Status = device.StatusPending
	capped := &task.Task{
		ResourceID: "deadbeef-dev", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts,
		CreatedAt: time.Now().Add(-launchWaitCap - time.Minute),
	}
	require.NoError(t, h.Handle(ctx, capped))
	assert.Equal(t, device.StatusError, deviceRepo.Devices["deadbeef-dev"].Status)
}

// A device deleted mid-create leaves its launch running server-side; the
// cleanup task must wait for the group to settle rather than `cvd rm` a live
// launch (the robustness harness caught exactly this leak via the cleanup
// path). Past the settle cap the delete proceeds best-effort.
func TestCleanupCVDHandlerWaitsForInFlightLaunch(t *testing.T) {
	t.Parallel()
	act, deviceRepo := newInFlightGroupActuator(t, "Starting")
	deviceRepo.Devices["deadbeef-dev"].Status = device.StatusDeleted
	h := NewCleanupHandler(act)
	ctx := t.Context()

	fresh := &task.Task{ResourceID: "deadbeef-dev", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts, CreatedAt: time.Now()}
	err := h.Handle(ctx, fresh)
	resched, ok := errors.AsType[*task.RescheduleError](err)
	require.True(t, ok, "in-flight launch must postpone the cleanup, got %v", err)
	assert.Equal(t, launchInFlightRetry, resched.After)
}

func TestCleanupCVDForcesDeletePastSettleCap(t *testing.T) {
	t.Parallel()
	var deleted atomic.Bool
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cvds" && r.Method == http.MethodGet:
			w.Write([]byte(`{"cvds": [{"group": "cvddeadbeef", "name": "dev-1", "status": "Starting"}]}`))
		case r.Method == http.MethodDelete:
			deleted.Store(true)
			w.Write([]byte(`{"name": "op-del"}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer ho.Close()
	hostURL, _ := url.Parse(ho.URL)
	port, _ := strconv.ParseInt(hostURL.Port(), 10, 64)

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{
		ID: "h", Status: host.StatusRunning, MaxDevices: 4,
		IPAddress: "127.0.0.1", HostOrchestratorPort: port,
	}
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["deadbeef-dev"] = &device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "dev-1", Status: device.StatusDeleted,
	}
	act := newTestActuator(t, deviceRepo, hostRepo)
	h := NewCleanupHandler(act)

	capped := &task.Task{
		ResourceID: "deadbeef-dev", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts,
		CreatedAt: time.Now().Add(-launchWaitCap - time.Minute),
	}
	require.NoError(t, h.Handle(t.Context(), capped))
	assert.True(t, deleted.Load(), "past the cap a wedged launcher's group is reclaimed best-effort")
}

// fakeCompleter records the delete journeys the reconcile handler finishes.
type fakeCompleter struct {
	completed []string
}

func (f *fakeCompleter) MarkDeletedWithHostDecrement(_ context.Context, _, deviceID string, _ ...*spanner.Mutation) (bool, error) {
	f.completed = append(f.completed, deviceID)
	return true, nil
}

// A device stuck in 'deleting' owes the rest of its delete commit: Delete's
// best-effort status write and its tombstone transaction are separate, and a
// crash between them leaves deleting + no cleanup task + the counter still
// holding the device (the robustness chaos harness caught the stranded row).
// Once the CVD converges clean, the reconcile finishes the journey; an
// already-tombstoned device is left alone.
func TestReconcileTeardownFinishesInterruptedDelete(t *testing.T) {
	t.Parallel()
	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{ID: "h", Status: host.StatusRunning, MaxDevices: 4}
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["d-stuck"] = &device.Device{ID: "d-stuck", HostID: "h", Status: device.StatusDeleting}
	deviceRepo.Devices["d-done"] = &device.Device{ID: "d-done", HostID: "h", Status: device.StatusDeleted}
	completer := &fakeCompleter{}
	// Host not running the HO in this fake -> not serving would postpone; use
	// a host whose row expects no VM so cleanup no-ops as already-clean.
	hostRepo.Hosts["h"].Status = host.StatusDeleting
	h := NewReconcileHandler(newTestActuator(t, deviceRepo, hostRepo), nil, completer, nil)

	fresh := &task.Task{ResourceID: "d-stuck", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts, CreatedAt: time.Now()}
	require.NoError(t, h.Handle(t.Context(), fresh))
	assert.Equal(t, []string{"d-stuck"}, completer.completed, "interrupted delete finished")

	done := &task.Task{ResourceID: "d-done", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts, CreatedAt: time.Now()}
	require.NoError(t, h.Handle(t.Context(), done))
	assert.Equal(t, []string{"d-stuck"}, completer.completed, "tombstoned device left alone")
}

// The keyed reconcile's teardown branch applies the same launch-in-flight
// bound as the cleanup handler: postpone while the task is young, force the
// best-effort delete past the settle cap. Without the cap the reconcile
// postpones forever. Postpone refunds the attempt, so the task never
// dead-letters and the device stays deleting invisibly.
func TestReconcileTeardownWaitsForInFlightLaunch(t *testing.T) {
	t.Parallel()
	act, deviceRepo := newInFlightGroupActuator(t, "Starting")
	deviceRepo.Devices["deadbeef-dev"].Status = device.StatusDeleting
	h := NewReconcileHandler(act, nil, nil, nil)

	fresh := &task.Task{ResourceID: "deadbeef-dev", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts, CreatedAt: time.Now()}
	err := h.Handle(t.Context(), fresh)
	resched, ok := errors.AsType[*task.RescheduleError](err)
	require.True(t, ok, "in-flight launch must postpone the reconcile, got %v", err)
	assert.Equal(t, launchInFlightRetry, resched.After)
}

func TestReconcileTeardownForcesDeletePastSettleCap(t *testing.T) {
	t.Parallel()
	var deleted atomic.Bool
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cvds" && r.Method == http.MethodGet:
			w.Write([]byte(`{"cvds": [{"group": "cvddeadbeef", "name": "dev-1", "status": "Starting"}]}`))
		case r.Method == http.MethodDelete:
			deleted.Store(true)
			w.Write([]byte(`{"name": "op-del"}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer ho.Close()
	hostURL, _ := url.Parse(ho.URL)
	port, _ := strconv.ParseInt(hostURL.Port(), 10, 64)

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{
		ID: "h", Status: host.StatusRunning, MaxDevices: 4,
		IPAddress: "127.0.0.1", HostOrchestratorPort: port,
	}
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["deadbeef-dev"] = &device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "dev-1", Status: device.StatusDeleting,
	}
	h := NewReconcileHandler(newTestActuator(t, deviceRepo, hostRepo), nil, nil, nil)

	capped := &task.Task{
		ResourceID: "deadbeef-dev", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts,
		CreatedAt: time.Now().Add(-launchWaitCap - time.Minute),
	}
	require.NoError(t, h.Handle(t.Context(), capped))
	assert.True(t, deleted.Load(), "past the cap the reconcile reclaims the wedged launcher's group")
}

// A quiescent Running group from a prior attempt is adopted as success. The
// launch completed after the previous attempt was abandoned, so there is no
// second launch and no delete.
func TestRunCreateCVDAdoptsRunningGroup(t *testing.T) {
	t.Parallel()
	act, deviceRepo := newInFlightGroupActuator(t, "Running")
	require.NoError(t, act.RunCreateCVD(t.Context(), "deadbeef-dev", nil))
	d := deviceRepo.Devices["deadbeef-dev"]
	assert.Equal(t, device.StatusRunning, d.Status)
	assert.Equal(t, "cvddeadbeef/dev-1", d.CVDID)
}

// fakeJournal stubs task.OperationJournal for the resume tests.
type fakeJournal struct {
	mu      sync.Mutex
	ops     map[string]string // step -> operation
	cleared bool
}

func newFakeJournal() *fakeJournal { return &fakeJournal{ops: map[string]string{}} }

func (f *fakeJournal) RecordedOperation(_ context.Context, _ *task.Task, step string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ops[step], nil
}

func (f *fakeJournal) RecordOperation(_ context.Context, _ *task.Task, step, op string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops[step] = op
	return nil
}

func (f *fakeJournal) ClearOperations(context.Context, *task.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = map[string]string{}
	f.cleared = true
	return nil
}

// journalActuator builds an actuator against the given HO handler with a
// journal wired, plus a pending device on a running host.
func journalActuator(t *testing.T, j *fakeJournal, handler http.HandlerFunc) (*Actuator, *devicetest.FakeRepo) {
	t.Helper()
	ho := httptest.NewServer(handler)
	t.Cleanup(ho.Close)
	hostURL, _ := url.Parse(ho.URL)
	port, _ := strconv.ParseInt(hostURL.Port(), 10, 64)

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{
		ID: "h", Status: host.StatusRunning, MaxDevices: 4,
		IPAddress: "127.0.0.1", HostOrchestratorPort: port,
	}
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["deadbeef-dev"] = &device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "dev-1", Status: device.StatusPending,
	}
	act := NewActuator(deviceRepo, hostRepo, nopBuilds{},
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithOperationJournal(j))
	return act, deviceRepo
}

// A journaled load operation is RESUMED on redelivery, with no second POST
// /cvds and no adoption by observation.
func TestRunCreateCVDResumesJournaledOperation(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	require.NoError(t, j.RecordOperation(t.Context(), &task.Task{ID: "t1"}, stepCVDLoad, "op-prior"))

	var posted atomic.Bool
	act, deviceRepo := journalActuator(t, j, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/operations/op-prior/:wait":
			w.Write([]byte(`{"cvds": [{"group": "cvddeadbeef", "name": "dev-1", "status": "Running"}]}`))
		case r.URL.Path == "/cvds" && r.Method == http.MethodPost:
			posted.Store(true)
			w.Write([]byte(`{"name": "op-new"}`))
		default:
			w.Write([]byte(`{"cvds": []}`))
		}
	})

	require.NoError(t, act.RunCreateCVD(t.Context(), "deadbeef-dev", &task.Task{ID: "t1"}))
	d := deviceRepo.Devices["deadbeef-dev"]
	assert.Equal(t, device.StatusRunning, d.Status)
	assert.Equal(t, "cvddeadbeef/dev-1", d.CVDID)
	assert.False(t, posted.Load(), "resume must not start a second load")
}

// A resumed operation that settled as failed clears the journey and falls
// through to a fresh create in the same attempt, and never re-reads the same
// stored failure to exhaustion.
func TestRunCreateCVDRestartsAfterTerminalResumedOperation(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	require.NoError(t, j.RecordOperation(t.Context(), &task.Task{ID: "t1"}, stepCVDLoad, "op-prior"))

	act, deviceRepo := journalActuator(t, j, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/operations/op-prior/:wait":
			// The operation's own stored failure (terminal 500).
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error": "cvd load failed"}`))
		case r.URL.Path == "/cvds" && r.Method == http.MethodGet:
			w.Write([]byte(`{"cvds": []}`)) // nothing left to adopt
		case r.URL.Path == "/cvds" && r.Method == http.MethodPost:
			w.Write([]byte(`{"name": "op-fresh"}`))
		case r.URL.Path == "/operations/op-fresh/:wait":
			w.Write([]byte(`{"cvds": [{"group": "cvddeadbeef", "name": "dev-1", "status": "Running"}]}`))
		default:
			w.Write([]byte(`{}`))
		}
	})

	require.NoError(t, act.RunCreateCVD(t.Context(), "deadbeef-dev", &task.Task{ID: "t1"}))
	assert.Equal(t, device.StatusRunning, deviceRepo.Devices["deadbeef-dev"].Status)
	assert.True(t, j.cleared, "terminal resumed operation must clear the journey")
	j.mu.Lock()
	defer j.mu.Unlock()
	assert.Equal(t, "op-fresh", j.ops[stepCVDLoad], "the fresh load is journaled in turn")
}

// A fresh create journals the operation it starts, before waiting on it.
func TestRunCreateCVDJournalsFreshOperation(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	act, deviceRepo := journalActuator(t, j, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cvds" && r.Method == http.MethodGet:
			w.Write([]byte(`{"cvds": []}`))
		case r.URL.Path == "/cvds" && r.Method == http.MethodPost:
			w.Write([]byte(`{"name": "op-fresh"}`))
		case r.URL.Path == "/operations/op-fresh/:wait":
			w.Write([]byte(`{"cvds": [{"group": "cvddeadbeef", "name": "dev-1", "status": "Running"}]}`))
		default:
			w.Write([]byte(`{}`))
		}
	})

	require.NoError(t, act.RunCreateCVD(t.Context(), "deadbeef-dev", &task.Task{ID: "t1"}))
	assert.Equal(t, device.StatusRunning, deviceRepo.Devices["deadbeef-dev"].Status)
	j.mu.Lock()
	defer j.mu.Unlock()
	assert.Equal(t, "op-fresh", j.ops[stepCVDLoad])
}

// device.reconcile converges teardown (probe-first cleanup) and postpones on
// an in-flight launch like the cleanup handler; steady/missing devices no-op.
func TestReconcileDeviceHandler(t *testing.T) {
	t.Parallel()
	act, deviceRepo := newInFlightGroupActuator(t, "Starting")
	deviceRepo.Devices["deadbeef-dev"].Status = device.StatusDeleted
	h := NewReconcileHandler(act, nil, nil, nil)
	ctx := t.Context()

	// Teardown convergence against an in-flight launch -> postpone. CreatedAt
	// matters: the launch-in-flight wait is bounded by total task age.
	err := h.Handle(ctx, &task.Task{ResourceID: "deadbeef-dev", CreatedAt: time.Now()})
	_, ok := errors.AsType[*task.RescheduleError](err)
	require.True(t, ok, "in-flight launch must postpone the reconcile, got %v", err)

	// Steady device and missing device -> no-op.
	deviceRepo.Devices["deadbeef-dev"].Status = device.StatusRunning
	require.NoError(t, h.Handle(ctx, &task.Task{ResourceID: "deadbeef-dev"}))
	require.NoError(t, h.Handle(ctx, &task.Task{ResourceID: "missing"}))
}

func TestBuildEnvConfigLeavesFetchAlone(t *testing.T) {
	t.Parallel()
	d := &device.Device{ID: "deadbeef", Name: "dev-1"}

	simple := buildEnvConfig(d, "cvddeadbeef", map[string]any{}, "", map[string]any{}, map[string]any{})
	assert.NotContains(t, simple, "fetch",
		"the fetcher's compiled-in v4 default is the only surface its catchall API key is valid for; overriding it broke all @ab fetches")

	d.EnvConfig = map[string]any{
		"instances": []any{map[string]any{}},
		"fetch":     map[string]any{"api_base_url": "https://example.com/custom"},
	}
	advanced := buildEnvConfig(d, "cvddeadbeef", map[string]any{}, "", map[string]any{}, map[string]any{})
	assert.Equal(t, map[string]any{"api_base_url": "https://example.com/custom"}, advanced["fetch"],
		"a user-provided fetch section passes through untouched")
}

type ctxAwareDeviceRepo struct {
	*devicetest.FakeRepo
}

func (m *ctxAwareDeviceRepo) UpdateStatus(ctx context.Context, id string, status device.Status, _ string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("refusing write on dead context: %w", err)
	}
	m.StatusUpdates[id] = status
	return nil
}

func TestMarkCreateFailedDetachesFromDeadContext(t *testing.T) {
	t.Parallel()
	repo := &ctxAwareDeviceRepo{devicetest.NewFakeRepo()}
	repo.Devices["dev-1"] = &device.Device{ID: "dev-1", Status: device.StatusCreating}
	act := &Actuator{devices: repo, logger: slog.Default()}

	// The exhausted attempt failed because its own context died (HandleTimeout
	// or pool shutdown). The terminal write must still land, or the device is
	// stranded in "creating" forever.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	act.markCreateFailed(ctx, "dev-1", errors.New("boom"))

	if got := repo.StatusUpdates["dev-1"]; got != device.StatusError {
		t.Errorf("status = %q, want error: the terminal write must land despite the dead context", got)
	}
}

// A still-`starting` host whose Host Orchestrator already answers must not
// park the create behind the device controller's next cycle: the create-side
// probe adopts it. The row is promoted through MarkHOReady and the create
// runs now. A silent HO keeps the wait, and the handler re-checks on the
// short starting-phase delay rather than the provisioning one.
func TestRunCreateCVDProbesStartingHost(t *testing.T) {
	t.Parallel()

	startingHost := func(port int64) *host.Host {
		return &host.Host{
			ID: "h", Status: host.StatusStarting, MaxDevices: 4,
			IPAddress: "127.0.0.1", HostOrchestratorPort: port,
			VMStatus: "RUNNING", // the promotion re-derives from the stored observation
		}
	}

	t.Run("HO answers: promoted and created in one delivery", func(t *testing.T) {
		t.Parallel()
		ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/cvds" && r.Method == http.MethodGet:
				w.Write([]byte(`{"cvds": []}`)) // the probe, then the adopt guard
			case r.URL.Path == "/cvds" && r.Method == http.MethodPost:
				w.Write([]byte(`{"name": "op-create"}`))
			case strings.HasSuffix(r.URL.Path, "/:wait"):
				w.Write([]byte(`{"cvds":[{"group":"cvd-deadbeef","name":"dev-1","status":"Running"}]}`))
			default:
				w.Write([]byte(`{}`))
			}
		}))
		defer ho.Close()
		hostURL, _ := url.Parse(ho.URL)
		port, _ := strconv.ParseInt(hostURL.Port(), 10, 64)

		hostRepo := hosttest.NewFakeRepo()
		hostRepo.Hosts["h"] = startingHost(port)
		deviceRepo := devicetest.NewFakeRepo()
		deviceRepo.Devices["deadbeef-dev"] = &device.Device{
			ID: "deadbeef-dev", HostID: "h", Name: "dev-1", Status: device.StatusPending,
		}
		act := newTestActuator(t, deviceRepo, hostRepo)

		require.NoError(t, act.RunCreateCVD(t.Context(), "deadbeef-dev", nil))
		assert.Equal(t, host.StatusRunning, hostRepo.Hosts["h"].Status,
			"the probe must promote the answering host instead of waiting for the controller cycle")
		assert.Equal(t, device.StatusRunning, deviceRepo.Devices["deadbeef-dev"].Status,
			"the create must run in the same delivery, not postpone")
	})

	t.Run("HO silent: waits on the short starting-phase delay", func(t *testing.T) {
		t.Parallel()
		dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		dead.Close() // the port now refuses connections, like a booting VM
		hostURL, _ := url.Parse(dead.URL)
		port, _ := strconv.ParseInt(hostURL.Port(), 10, 64)

		hostRepo := hosttest.NewFakeRepo()
		hostRepo.Hosts["h"] = startingHost(port)
		deviceRepo := devicetest.NewFakeRepo()
		deviceRepo.Devices["deadbeef-dev"] = &device.Device{
			ID: "deadbeef-dev", HostID: "h", Name: "dev-1", Status: device.StatusPending,
		}
		act := newTestActuator(t, deviceRepo, hostRepo)
		h := NewCreateHandler(act, nil)

		tk := &task.Task{
			ResourceID: "deadbeef-dev", Attempts: 1,
			MaxAttempts: task.DefaultMaxAttempts, CreatedAt: time.Now(),
		}
		err := h.Handle(t.Context(), tk)
		resched, ok := errors.AsType[*task.RescheduleError](err)
		require.True(t, ok, "a silent HO must keep the not-ready wait, got %v", err)
		assert.Equal(t, hostStartingRetry, resched.After,
			"a starting host re-checks on the short delay, not the provisioning one")
		assert.Equal(t, host.StatusStarting, hostRepo.Hosts["h"].Status,
			"a silent HO must not be promoted")
	})
}

// The load-wait sampler is the only visibility into the multi-minute HO
// create wait: each change of the group's listed status must land exactly
// once as a span event on the ambient attempt span (absent → Preparing →
// Running here), deduplicated between changes.
func TestAwaitCVDCreateSamplesLoadTransitions(t *testing.T) {
	t.Parallel()
	var samples atomic.Int64
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cvds" && r.Method == http.MethodGet:
			switch n := samples.Add(1); {
			case n <= 2:
				w.Write([]byte(`{"cvds": []}`))
			case n <= 4:
				w.Write([]byte(`{"cvds":[{"group":"cvd-deadbeef","name":"dev-1","status":"Preparing"}]}`))
			default:
				w.Write([]byte(`{"cvds":[{"group":"cvd-deadbeef","name":"dev-1","status":"Running"}]}`))
			}
		case strings.HasSuffix(r.URL.Path, "/:wait"):
			// Hold the wait until the sampler has walked all three states.
			// The hold is bounded, so a broken (never-sampling) build fails
			// fast instead of deadlocking the test.
			deadline := time.Now().Add(2 * time.Second)
			for samples.Load() < 6 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			w.Write([]byte(`{"cvds":[{"group":"cvd-deadbeef","name":"dev-1","status":"Running"}]}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer ho.Close()

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	ctx, span := tp.Tracer("test").Start(t.Context(), "attempt")

	d := &device.Device{ID: "deadbeef-dev", HostID: "h", Name: "dev-1", Status: device.StatusCreating}
	act := newTestActuator(t, devicetest.NewFakeRepo(d), hosttest.NewFakeRepo())
	act.loadSample = 2 * time.Millisecond
	client := hoclient.NewFactory(hoclient.DefaultClientConfig()).ClientForHost(ho.URL)

	resp, stand, err := act.awaitCVDCreate(ctx, client, "op-1", d, "cvd-deadbeef")
	span.End()
	require.NoError(t, err)
	require.Equal(t, standDownNone, stand)
	require.Len(t, resp.CVDs, 1)

	var statuses []string
	for _, s := range rec.Ended() {
		if s.Name() != "attempt" {
			continue
		}
		for _, e := range s.Events() {
			if e.Name != "cvd.load.observed" {
				continue
			}
			for _, kv := range e.Attributes {
				if string(kv.Key) == "cvd.status" {
					statuses = append(statuses, kv.Value.AsString())
				}
			}
		}
	}
	assert.Equal(t, []string{"absent", "Preparing", "Running"}, statuses,
		"each observed transition exactly once, in order, deduplicated between changes")
}
