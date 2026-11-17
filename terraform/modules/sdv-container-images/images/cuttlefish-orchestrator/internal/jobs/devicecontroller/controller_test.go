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

package devicecontroller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/device/devicetest"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// Tests

func TestService_StartStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deviceRepo := devicetest.NewFakeRepo()
		hostRepo := hosttest.NewFakeRepo()
		hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())
		logger := slog.Default()

		config := device.HealthCheckConfig{
			Interval:         100 * time.Millisecond,
			FailureThreshold: 3,
			ZombieThreshold:  10,
		}

		svc := New(config, deviceRepo, hostRepo, hoFactory, logger)

		ctx := t.Context()
		svc.Start(ctx)
		svc.Start(ctx) // idempotent no-op

		// Stop joins the loop; a second Stop is a no-op.
		svc.Stop()
		svc.Stop()
	})
}

func TestService_NoDevicesNeedingCheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deviceRepo := devicetest.NewFakeRepo()
		hostRepo := hosttest.NewFakeRepo()
		hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())
		logger := slog.Default()

		config := device.HealthCheckConfig{
			Interval:         50 * time.Millisecond,
			FailureThreshold: 3,
			ZombieThreshold:  10,
		}

		svc := New(config, deviceRepo, hostRepo, hoFactory, logger)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		svc.Start(ctx)

		// Wait for a cycle - synctest advances time automatically
		time.Sleep(100 * time.Millisecond)

		svc.Stop()

		// No health updates should have occurred
		if len(deviceRepo.HealthUpdates) != 0 {
			t.Errorf("expected 0 health updates, got %d", len(deviceRepo.HealthUpdates))
		}
	})
}

func TestService_ListError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deviceRepo := devicetest.NewFakeRepo()
		deviceRepo.ListErr = errors.New("database error")
		hostRepo := hosttest.NewFakeRepo()
		hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())
		logger := slog.Default()

		config := device.HealthCheckConfig{
			Interval:         50 * time.Millisecond,
			FailureThreshold: 3,
			ZombieThreshold:  10,
		}

		svc := New(config, deviceRepo, hostRepo, hoFactory, logger)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		// Should not panic even with errors
		svc.Start(ctx)

		time.Sleep(100 * time.Millisecond)

		svc.Stop()

		// Should handle errors gracefully
		if len(deviceRepo.HealthUpdates) != 0 {
			t.Errorf("expected 0 health updates, got %d", len(deviceRepo.HealthUpdates))
		}
	})
}

func TestService_HostNotFound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deviceRepo := devicetest.NewFakeRepo()
		hostRepo := hosttest.NewFakeRepo()
		hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())
		logger := slog.Default()

		// Add a device needing check with a non-existent host
		deviceRepo.NeedingCheck = []*device.Device{
			{
				ID:     "device-1",
				HostID: "nonexistent-host",
				CVDID:  "cvd/1",
				Status: device.StatusRunning,
			},
		}
		deviceRepo.Devices["device-1"] = deviceRepo.NeedingCheck[0]

		// Interval longer than the sleep below so exactly one check runs;
		// Interval <= 1s also exercises the check-timeout clamp.
		config := device.HealthCheckConfig{
			Interval:         1 * time.Second,
			FailureThreshold: 3,
			ZombieThreshold:  10,
		}

		svc := New(config, deviceRepo, hostRepo, hoFactory, logger)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		svc.Start(ctx)

		time.Sleep(100 * time.Millisecond)

		svc.Stop()

		// One failure is below FailureThreshold (3): the failure is recorded
		// but the device is not yet marked unhealthy.
		if deviceRepo.HealthUpdates["device-1"] != device.HealthStatusUnknown {
			t.Errorf("expected device health unknown below threshold, got %s", deviceRepo.HealthUpdates["device-1"])
		}
		if got := deviceRepo.Devices["device-1"].ConsecutiveFails; got != 1 {
			t.Errorf("expected 1 consecutive fail, got %d", got)
		}
	})
}

// Devices on a non-serving host must degrade. Skipping them
// left health_status at last-known-healthy while the VM might already be
// gone, so the console showed healthy devices on a dead host. The monitor now
// records failures so the unhealthy/zombie escalation runs.
func TestService_NonServingHostDegradesDevices(t *testing.T) {
	t.Parallel()
	deviceRepo := devicetest.NewFakeRepo()
	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["host-1"] = &host.Host{ID: "host-1", Status: host.StatusPreempted}
	d := &device.Device{
		ID: "device-1", HostID: "host-1", Status: device.StatusRunning,
		HealthStatus: device.HealthStatusHealthy, ConsecutiveFails: 2,
	}
	deviceRepo.Devices[d.ID] = d

	config := device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10}
	svc := New(config, deviceRepo, hostRepo, hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())

	svc.checkHostDevices(t.Context(), hostRepo.Hosts["host-1"], []*device.Device{d})

	if got := deviceRepo.HealthUpdates["device-1"]; got != device.HealthStatusUnhealthy {
		t.Errorf("health = %q, want unhealthy (threshold reached on a non-serving host)", got)
	}
	if d.ConsecutiveFails != 3 {
		t.Errorf("ConsecutiveFails = %d, want 3", d.ConsecutiveFails)
	}
	// Lifecycle settling is scoped to STOPPED hosts: a preempted host's
	// devices keep their status for the recovery flow to recreate.
	if got, ok := deviceRepo.StatusUpdates["device-1"]; ok {
		t.Errorf("device on a preempted host must not be lifecycle-written, got %q", got)
	}
}

// A settled STOPPED host implies its CVDs are not running: devices with a
// legal path to stopped settle their lifecycle (the live defect: a stopped
// host's devices claimed "running" indefinitely, health decayed to zombie
// but nothing ever wrote the lifecycle). Health is held, so it does not
// decay to failed. A mid-start device has no legal transition to stopped
// and keeps the accurate failure record.
func TestService_StoppedHostSettlesDevicesToStopped(t *testing.T) {
	t.Parallel()
	deviceRepo := devicetest.NewFakeRepo()
	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["host-1"] = &host.Host{ID: "host-1", Status: host.StatusStopped}
	running := &device.Device{
		ID: "dev-running", HostID: "host-1", Status: device.StatusRunning,
		HealthStatus: device.HealthStatusHealthy, ConsecutiveFails: 2,
		CVDStatus: device.CVDStateRunning,
	}
	starting := &device.Device{
		ID: "dev-starting", HostID: "host-1", Status: device.StatusStarting,
	}
	deviceRepo.Devices[running.ID] = running
	deviceRepo.Devices[starting.ID] = starting

	config := device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10}
	svc := New(config, deviceRepo, hostRepo, hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())

	svc.checkHostDevices(t.Context(), hostRepo.Hosts["host-1"], []*device.Device{running, starting})

	if got := deviceRepo.StatusUpdates["dev-running"]; got != device.StatusStopped {
		t.Errorf("running device on a stopped host: status write = %q, want stopped", got)
	}
	if got := deviceRepo.HealthUpdates["dev-running"]; got != device.HealthStatusHealthy {
		t.Errorf("settled device health = %q, want held at healthy (lifecycle owns the outcome)", got)
	}
	if running.ConsecutiveFails != 2 {
		t.Errorf("settled device ConsecutiveFails = %d, want held at 2", running.ConsecutiveFails)
	}

	if got, ok := deviceRepo.StatusUpdates["dev-starting"]; ok {
		t.Errorf("mid-start device must not be lifecycle-written (no legal transition), got %q", got)
	}
	if starting.ConsecutiveFails != 1 {
		t.Errorf("mid-start device ConsecutiveFails = %d, want 1 (accurate failure record)", starting.ConsecutiveFails)
	}
}

// fakeTaskChecker reports one resource as already owned by a live task and
// asserts the task type every check probes for.
type fakeTaskChecker struct {
	t        *testing.T
	wantType string
	ownedID  string
}

func (f *fakeTaskChecker) HasActive(_ context.Context, taskType, resourceID string) (bool, error) {
	if taskType != f.wantType {
		f.t.Errorf("checked %q, want %q", taskType, f.wantType)
	}
	return resourceID == f.ownedID, nil
}

// taskRecorder records filed tasks through either enqueue port.
type taskRecorder struct {
	ids   []string
	types []string
}

func (r *taskRecorder) record(t *task.Task) {
	r.ids = append(r.ids, t.ResourceID)
	r.types = append(r.types, t.Type)
}

func (r *taskRecorder) Enqueue(_ context.Context, t *task.Task) error {
	r.record(t)
	return nil
}

func (r *taskRecorder) EnqueueResync(_ context.Context, taskType, resourceID string, _ time.Duration) error {
	r.ids = append(r.ids, resourceID)
	r.types = append(r.types, taskType)
	return nil
}

// The stale-teardown suspector files device.reconcile suspicion for devices
// stuck 'deleting' past the grace with NO live cleanup task, and leaves
// owned or fresh teardowns alone. The handler once existed with no producer
// at all, so the suspicion is the half that must stay pinned.
//
// The stale row's age sits between the teardown threshold and the create-side
// staleTransitionalAfter on purpose: it pins the DECOUPLING. A `deleting` row
// with no live task is a lost task, not a wait, so re-coupling the thresholds
// would leave this row alone for ten minutes and turn the test red.
func TestService_SuspectsStaleTeardowns(t *testing.T) {
	t.Parallel()
	deviceRepo := devicetest.NewFakeRepo()
	stale := &device.Device{
		ID: "stale-del", HostID: "h", Status: device.StatusDeleting,
		UpdatedAt: time.Now().Add(-staleTeardownAfter - 30*time.Second),
	}
	owned := &device.Device{
		ID: "owned-del", HostID: "h", Status: device.StatusDeleting,
		UpdatedAt: time.Now().Add(-staleTeardownAfter - 30*time.Second),
	}
	fresh := &device.Device{
		ID: "fresh-del", HostID: "h", Status: device.StatusDeleting,
		UpdatedAt: time.Now(),
	}
	if staleTeardownAfter+30*time.Second >= staleTransitionalAfter {
		t.Fatal("fixture age no longer sits between the two thresholds; the decoupling assertion is vacuous")
	}
	deviceRepo.Devices[stale.ID] = stale
	deviceRepo.Devices[owned.ID] = owned
	deviceRepo.Devices[fresh.ID] = fresh

	rec := &taskRecorder{}
	config := device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10}
	svc := New(config, deviceRepo, hosttest.NewFakeRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithReconcileSuspector(ReconcileSuspector{
			Tasks:    &fakeTaskChecker{t: t, wantType: task.TypeDeviceCleanupCVD, ownedID: "owned-del"},
			Enqueuer: rec,
		}))

	svc.suspectStaleTeardowns(t.Context())

	if len(rec.ids) != 1 || rec.ids[0] != "stale-del" {
		t.Errorf("suspicion enqueued for %v, want [stale-del] only (owned and fresh teardowns are left alone)", rec.ids)
	}
	if len(rec.types) != 1 || rec.types[0] != task.TypeDeviceReconcile {
		t.Errorf("suspicion filed %v, want [%s]", rec.types, task.TypeDeviceReconcile)
	}
}

// The zombie suspector files device.reconcile for running+zombie devices
// (so the reconcile handler recreates the CVD), skipping healthy devices and
// those a live create task already owns, and only when ZombieRecreate is on.
func TestService_SuspectZombies(t *testing.T) {
	t.Parallel()
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["z"] = &device.Device{ID: "z", HostID: "h", Status: device.StatusRunning, HealthStatus: device.HealthStatusZombie}
	deviceRepo.Devices["ok"] = &device.Device{ID: "ok", HostID: "h", Status: device.StatusRunning, HealthStatus: device.HealthStatusHealthy}
	deviceRepo.Devices["owned"] = &device.Device{ID: "owned", HostID: "h", Status: device.StatusRunning, HealthStatus: device.HealthStatusZombie}

	rec := &taskRecorder{}
	suspector := ReconcileSuspector{
		Tasks:    &fakeTaskChecker{t: t, wantType: task.TypeDeviceCreateCVD, ownedID: "owned"},
		Enqueuer: rec,
	}
	cfg := device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10, ZombieRecreate: true}
	svc := New(cfg, deviceRepo, hosttest.NewFakeRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(), WithReconcileSuspector(suspector))

	svc.suspectZombies(t.Context())
	if len(rec.ids) != 1 || rec.ids[0] != "z" {
		t.Errorf("zombie suspicion enqueued for %v, want [z] only (healthy and owned are left alone)", rec.ids)
	}

	// With ZombieRecreate off, a zombie is detected but never acted on.
	rec.ids = nil
	cfg.ZombieRecreate = false
	svcOff := New(cfg, deviceRepo, hosttest.NewFakeRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(), WithReconcileSuspector(suspector))
	svcOff.suspectZombies(t.Context())
	if len(rec.ids) != 0 {
		t.Errorf("suspicion enqueued with ZombieRecreate off: %v", rec.ids)
	}
}

// The failed-capture detector files a capture only for error devices not yet
// captured (bugreport_captured_at NULL), skips those with a live capture task,
// and does nothing at all when the capture-on-error policy reads off.
func TestService_SuspectFailedCaptures(t *testing.T) {
	t.Parallel()
	captured := time.Now()
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["err"] = &device.Device{ID: "err", HostID: "h", Status: device.StatusError}
	deviceRepo.Devices["done"] = &device.Device{ID: "done", HostID: "h", Status: device.StatusError, BugreportCapturedAt: &captured}
	deviceRepo.Devices["owned"] = &device.Device{ID: "owned", HostID: "h", Status: device.StatusError}
	deviceRepo.Devices["running"] = &device.Device{ID: "running", HostID: "h", Status: device.StatusRunning}

	rec := &taskRecorder{}
	enabled := true
	cs := CaptureSuspector{
		Enabled:  func(_ context.Context) (bool, error) { return enabled, nil },
		Tasks:    &fakeTaskChecker{t: t, wantType: task.TypeDeviceCaptureBugreport, ownedID: "owned"},
		Enqueuer: rec,
	}
	svc := New(device.HealthCheckConfig{Interval: time.Minute}, deviceRepo, hosttest.NewFakeRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(), WithCaptureSuspector(cs))

	svc.suspectFailedCaptures(t.Context())
	if len(rec.ids) != 1 || rec.ids[0] != "err" {
		t.Errorf("capture detector enqueued %v, want [err] only (captured, owned, running are left alone)", rec.ids)
	}
	if len(rec.types) != 1 || rec.types[0] != task.TypeDeviceCaptureBugreport {
		t.Errorf("capture detector filed %v, want [%s]", rec.types, task.TypeDeviceCaptureBugreport)
	}

	// Policy off: the admin toggle takes effect without a redeploy.
	rec.ids = nil
	enabled = false
	svc.suspectFailedCaptures(t.Context())
	if len(rec.ids) != 0 {
		t.Errorf("capture enqueued with the policy off: %v", rec.ids)
	}

	// Unwired: no detector at all.
	rec.ids = nil
	svcOff := New(device.HealthCheckConfig{Interval: time.Minute}, deviceRepo, hosttest.NewFakeRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
	svcOff.suspectFailedCaptures(t.Context())
	if len(rec.ids) != 0 {
		t.Errorf("capture enqueued with no suspector wired: %v", rec.ids)
	}
}

// The degraded-host sweep queues poison remediation only for degraded hosts
// with no live devices (an empty poisoned host), leaves tenanted degraded hosts
// fenced for an operator, skips hosts that already have a live remediate task,
// and ignores healthy hosts entirely.
func TestService_SuspectDegradedHosts(t *testing.T) {
	t.Parallel()
	degrade := func(id string) *host.Host {
		h := &host.Host{ID: id, Zone: "z", Status: host.StatusRunning}
		for range host.HostPoisonThreshold {
			h.RecordCreateFailure("tap interface busy")
		}
		return h
	}
	empty := degrade("empty")     // only error rows → remediate
	tenanted := degrade("tenant") // a live device → leave fenced
	owned := degrade("owned")     // remediate task already live → skip
	healthy := &host.Host{ID: "ok", Zone: "z", Status: host.StatusRunning}
	hostRepo := hosttest.NewFakeRepo(empty, tenanted, owned, healthy)

	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["e-err"] = &device.Device{ID: "e-err", HostID: "empty", Status: device.StatusError}
	deviceRepo.Devices["o-err"] = &device.Device{ID: "o-err", HostID: "owned", Status: device.StatusError}
	deviceRepo.Devices["t-live"] = &device.Device{ID: "t-live", HostID: "tenant", Status: device.StatusRunning}

	rec := &taskRecorder{}
	rem := HostRemediator{
		Tasks:    &fakeTaskChecker{t: t, wantType: task.TypeHostRemediate, ownedID: "owned"},
		Enqueuer: rec,
	}
	svc := New(device.HealthCheckConfig{Interval: time.Minute}, deviceRepo, hostRepo,
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(), WithHostRemediator(rem))

	svc.suspectDegradedHosts(t.Context())
	if len(rec.ids) != 1 || rec.ids[0] != "empty" {
		t.Errorf("degraded sweep enqueued %v, want [empty] only", rec.ids)
	}

	// Unwired: no sweep at all.
	rec.ids = nil
	svcOff := New(device.HealthCheckConfig{Interval: time.Minute}, deviceRepo, hostRepo,
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
	svcOff.suspectDegradedHosts(t.Context())
	if len(rec.ids) != 0 {
		t.Errorf("sweep enqueued with no remediator wired: %v", rec.ids)
	}
}

// The census flags CVD groups no device row claims, after a first-seen
// grace, and sweeps them only when enabled. Claimed groups (by deterministic
// name or recorded CVDID) are never touched.
func TestService_CensusOrphanGroups(t *testing.T) {
	t.Parallel()
	var deleted atomic.Int32
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cvds" && r.Method == http.MethodGet:
			w.Write([]byte(`{"cvds": [
				{"group": "cvddeadbeef", "name": "dev-1", "status": "Running"},
				{"group": "cvdrogue001", "name": "rogue", "status": "Running"}
			]}`))
		case r.URL.Path == "/cvds/cvdrogue001" && r.Method == http.MethodDelete:
			deleted.Add(1)
			w.Write([]byte(`{"name": "op-del"}`))
		case strings.Contains(r.URL.Path, "/operations/"):
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected HO request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ho.Close()

	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["deadbeef-dev"] = &device.Device{
		ID: "deadbeef-dev", HostID: "h", Status: device.StatusRunning,
	}

	config := device.HealthCheckConfig{
		Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10,
		SweepOrphanCVDs: true,
	}
	svc := New(config, deviceRepo, hosttest.NewFakeRepo(), hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
	client := svc.hoClientFactory.ClientForHost(ho.URL)
	cvds, err := client.GetAllCVDStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// First sighting: recorded, within grace, no sweep yet.
	svc.censusHost(t.Context(), "h", client, cvds)
	if deleted.Load() != 0 {
		t.Fatal("orphan swept on first sighting; grace must hold it")
	}

	// Unclaimed past the grace window: swept.
	svc.censusMu.Lock()
	svc.censusFirstSeen["h/cvdrogue001"] = time.Now().Add(-censusGrace - time.Minute)
	svc.censusMu.Unlock()
	svc.censusHost(t.Context(), "h", client, cvds)
	if deleted.Load() != 1 {
		t.Errorf("orphan sweeps = %d, want exactly 1 (claimed group untouched)", deleted.Load())
	}
	if svc.cycleOrphans.Load() == 0 {
		t.Error("orphan count not recorded")
	}

	// Dry-run config: detected, never deleted.
	svc.config.SweepOrphanCVDs = false
	svc.censusMu.Lock()
	svc.censusFirstSeen["h/cvdrogue001"] = time.Now().Add(-censusGrace - time.Minute)
	svc.censusMu.Unlock()
	svc.censusHost(t.Context(), "h", client, cvds)
	if deleted.Load() != 1 {
		t.Errorf("dry-run must not delete; sweeps = %d", deleted.Load())
	}
}

// The warm plane's reserved warm group has no device row by design: the
// census must treat it as claimed, with no gauge, no log and no sweep,
// because its gauge is documented as any-nonzero-is-an-anomaly and the keeper
// owns the group's cleanup.
func TestService_CensusExemptsWarmGroup(t *testing.T) {
	t.Parallel()
	var deleted atomic.Int32
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cvds" && r.Method == http.MethodGet:
			fmt.Fprintf(w, `{"cvds": [{"group": %q, "name": "1", "status": "Starting"}]}`, device.WarmGroupName)
		case r.Method == http.MethodDelete:
			deleted.Add(1)
			w.Write([]byte(`{"name": "op-del"}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer ho.Close()

	config := device.HealthCheckConfig{
		Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10,
		SweepOrphanCVDs: true,
	}
	svc := New(config, devicetest.NewFakeRepo(), hosttest.NewFakeRepo(), hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
	client := svc.hoClientFactory.ClientForHost(ho.URL)
	cvds, err := client.GetAllCVDStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// Even aged far past the grace window, the reserved name never orphans.
	svc.censusMu.Lock()
	svc.censusFirstSeen["h/"+device.WarmGroupName] = time.Now().Add(-censusGrace - time.Hour)
	svc.censusMu.Unlock()
	svc.censusHost(t.Context(), "h", client, cvds)
	if deleted.Load() != 0 {
		t.Errorf("warm group swept %d times; the keeper owns its cleanup", deleted.Load())
	}
	if svc.cycleOrphans.Load() != 0 {
		t.Error("warm group counted on the orphan gauge; every warm cycle would read as an anomaly")
	}
}

// The census fence: a foreign-CVDs host (the legacy docker-shape create) is
// exempt by provenance: its CVDs arrive through the /v1 proxy with no device
// rows, so the gauge would page on healthy tests and the sweep would kill
// them. The exemption keys on provenance, NOT on Exclusive: an exclusive host
// CCO provisioned itself still gets its real orphans counted and swept.
func TestService_CensusFenceForeignCVDs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		foreignCVDs bool
		exclusive   bool
		wantSwept   int32
		wantGauged  bool
	}{
		{"foreign host exempt even with sweeping enabled", true, true, 0, false},
		{"ordinary host still swept", false, false, 1, true},
		{"exclusive host CCO provisioned still swept", false, true, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var deleted atomic.Int32
			ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/cvds" && r.Method == http.MethodGet:
					w.Write([]byte(`{"cvds": [{"group": "cvdrogue001", "name": "1", "status": "Running"}]}`))
				case r.URL.Path == "/cvds/cvdrogue001" && r.Method == http.MethodDelete:
					deleted.Add(1)
					w.Write([]byte(`{"name": "op-del"}`))
				default:
					w.Write([]byte(`{}`))
				}
			}))
			defer ho.Close()
			port := int64(ho.Listener.Addr().(*net.TCPAddr).Port)

			hostRepo := hosttest.NewFakeRepo()
			hostRepo.Hosts["h"] = &host.Host{
				ID: "h", Status: host.StatusRunning, InternalIP: "127.0.0.1", HostOrchestratorPort: port,
				ForeignCVDs: tc.foreignCVDs, Exclusive: tc.exclusive,
			}

			cfg := device.HealthCheckConfig{
				Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10,
				SweepOrphanCVDs: true,
			}
			svc := New(cfg, devicetest.NewFakeRepo(), hostRepo, hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
			// Age the group past the grace window so a sweep would fire on
			// this very cycle if the fence does not hold.
			svc.censusMu.Lock()
			svc.censusFirstSeen["h/cvdrogue001"] = time.Now().Add(-censusGrace - time.Minute)
			svc.censusMu.Unlock()

			svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], nil)

			if deleted.Load() != tc.wantSwept {
				t.Errorf("sweeps = %d, want %d", deleted.Load(), tc.wantSwept)
			}
			if gauged := svc.cycleOrphans.Load() != 0; gauged != tc.wantGauged {
				t.Errorf("orphan gauged = %v, want %v", gauged, tc.wantGauged)
			}
		})
	}
}

func TestService_FailureThreshold(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		priorFails  int
		priorStatus device.HealthStatus
		want        device.HealthStatus
	}{
		{"below threshold keeps current status", 0, device.HealthStatusHealthy, device.HealthStatusHealthy},
		{"below threshold defaults to unknown", 1, "", device.HealthStatusUnknown},
		{"at threshold flips unhealthy", 2, device.HealthStatusHealthy, device.HealthStatusUnhealthy},
		{"above threshold stays unhealthy", 5, device.HealthStatusUnhealthy, device.HealthStatusUnhealthy},
		{"at zombie threshold flips zombie", 9, device.HealthStatusUnhealthy, device.HealthStatusZombie},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deviceRepo := devicetest.NewFakeRepo()
			d := &device.Device{
				ID:               "device-1",
				HostID:           "host-1",
				Status:           device.StatusRunning,
				HealthStatus:     tt.priorStatus,
				ConsecutiveFails: tt.priorFails,
			}
			deviceRepo.Devices[d.ID] = d

			config := device.HealthCheckConfig{
				Interval:         time.Minute,
				FailureThreshold: 3,
				ZombieThreshold:  10,
			}
			svc := New(config, deviceRepo, hosttest.NewFakeRepo(), hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())

			svc.flushHealth(t.Context(), []device.HealthUpdate{svc.failureUpdate(d, "test failure", "")})

			if got := deviceRepo.HealthUpdates["device-1"]; got != tt.want {
				t.Errorf("health after failure = %q, want %q", got, tt.want)
			}
			if d.ConsecutiveFails != tt.priorFails+1 {
				t.Errorf("ConsecutiveFails = %d, want %d", d.ConsecutiveFails, tt.priorFails+1)
			}
		})
	}
}

func TestService_FailureThresholdDefault(t *testing.T) {
	t.Parallel()
	config := device.HealthCheckConfig{Interval: time.Minute, ZombieThreshold: 10}
	svc := New(config, devicetest.NewFakeRepo(), hosttest.NewFakeRepo(), hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
	if svc.config.FailureThreshold != 3 {
		t.Errorf("FailureThreshold = %d, want default 3", svc.config.FailureThreshold)
	}
}

func TestService_MarksUnhealthyAtThreshold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deviceRepo := devicetest.NewFakeRepo()
		hostRepo := hosttest.NewFakeRepo()
		hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())

		// Two prior failures: the next one reaches FailureThreshold (3).
		deviceRepo.NeedingCheck = []*device.Device{
			{
				ID:               "device-1",
				HostID:           "nonexistent-host",
				CVDID:            "cvd/1",
				Status:           device.StatusRunning,
				HealthStatus:     device.HealthStatusHealthy,
				ConsecutiveFails: 2,
			},
		}
		deviceRepo.Devices["device-1"] = deviceRepo.NeedingCheck[0]

		// Interval longer than the sleep below so exactly one check runs.
		config := device.HealthCheckConfig{
			Interval:         1 * time.Second,
			FailureThreshold: 3,
			ZombieThreshold:  10,
		}

		svc := New(config, deviceRepo, hostRepo, hoFactory, slog.Default())

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		svc.Start(ctx)

		time.Sleep(100 * time.Millisecond)

		svc.Stop()

		if deviceRepo.HealthUpdates["device-1"] != device.HealthStatusUnhealthy {
			t.Errorf("expected device unhealthy at threshold, got %s", deviceRepo.HealthUpdates["device-1"])
		}
	})
}

// The cycle visits running, starting and stopped devices, with stopped
// visited only so a CVD running under it can be noticed, and a host that
// refuses connections degrades the live ones while leaving the stopped one at
// rest.
func TestService_VisitsLiveDevicesAndHoldsTheOnesAtRest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deviceRepo := devicetest.NewFakeRepo()
		hostRepo := hosttest.NewFakeRepo()
		hoFactory := hoclient.NewFactory(hoclient.DefaultClientConfig())
		logger := slog.Default()

		// Add host. Loopback port 1 refuses connections immediately. An
		// unroutable IP would stall the synctest bubble in real time (the
		// fake-clock check timeout cannot fire while a real dial is in flight).
		hostRepo.Hosts["host-1"] = &host.Host{
			ID:                   "host-1",
			Status:               host.StatusRunning,
			IPAddress:            "127.0.0.1",
			HostOrchestratorPort: 1,
		}

		// Add devices with different statuses
		deviceRepo.NeedingCheck = []*device.Device{
			{ID: "device-running", HostID: "host-1", Status: device.StatusRunning, CVDID: "cvd/1"},
			{ID: "device-stopped", HostID: "host-1", Status: device.StatusStopped, CVDID: "cvd/2"},
			{ID: "device-starting", HostID: "host-1", Status: device.StatusStarting, CVDID: "cvd/3"},
		}
		for _, d := range deviceRepo.NeedingCheck {
			deviceRepo.Devices[d.ID] = d
		}

		config := device.HealthCheckConfig{
			Interval:         50 * time.Millisecond,
			FailureThreshold: 3,
			ZombieThreshold:  10,
		}

		svc := New(config, deviceRepo, hostRepo, hoFactory, logger)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		svc.Start(ctx)

		time.Sleep(150 * time.Millisecond)

		svc.Stop()

		for _, id := range []string{"device-running", "device-starting"} {
			if deviceRepo.Devices[id].ConsecutiveFails == 0 {
				t.Errorf("%s: want a counted failure against an unreachable host", id)
			}
		}
		if got := deviceRepo.Devices["device-stopped"].ConsecutiveFails; got != 0 {
			t.Errorf("stopped device ConsecutiveFails = %d, want 0 (at rest, nothing to fail)", got)
		}
		if _, exists := deviceRepo.HealthUpdates["device-stopped"]; !exists {
			t.Error("stopped device was not visited; drift under it would never be observed")
		}
	})
}

// A steady-state healthy device produces a timestamp-only update (the hot
// path writes no health columns); a recovering or first-checked device gets
// the full write.
func TestService_TimestampOnlyForSteadyHealthy(t *testing.T) {
	t.Parallel()
	config := device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10}
	svc := New(config, devicetest.NewFakeRepo(), hosttest.NewFakeRepo(), hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())

	cvdMap := map[string]*hoclient.CVDHealthStatus{
		"cvd/1": {CVDID: "cvd/1", Status: device.CVDStateRunning},
	}
	healthUpdateFor := func(d *device.Device) device.HealthUpdate {
		cvdState := ""
		if cvd, ok := cvdMap[d.CVDID]; ok {
			cvdState = cvd.Status
		}
		derived, settled := device.DerivePhase(d, cvdState)
		return svc.deviceHealthUpdate(d, cvdState, derived, settled)
	}

	// Steady state requires the CVD observation to also be unchanged (Running),
	// so the hot path writes nothing but timestamps.
	steady := &device.Device{
		ID: "d1", HostID: "h1", CVDID: "cvd/1",
		HealthStatus: device.HealthStatusHealthy, ConsecutiveFails: 0,
		CVDStatus: device.CVDStateRunning,
	}
	if u := healthUpdateFor(steady); !u.TimestampOnly {
		t.Errorf("steady healthy device: TimestampOnly = false, want true (%+v)", u)
	}

	recovering := &device.Device{
		ID: "d2", HostID: "h1", CVDID: "cvd/1",
		HealthStatus: device.HealthStatusUnhealthy, ConsecutiveFails: 4,
	}
	if u := healthUpdateFor(recovering); u.TimestampOnly || u.Status != device.HealthStatusHealthy || u.ConsecutiveFails != 0 {
		t.Errorf("recovering device must get a full healthy reset, got %+v", u)
	}

	firstCheck := &device.Device{ID: "d3", HostID: "h1", CVDID: "cvd/1"}
	if u := healthUpdateFor(firstCheck); u.TimestampOnly {
		t.Errorf("first-ever check must be a full write, got %+v", u)
	}
}

// The health condition is derived from the real CVD state, not collapsed into a
// blind "not Running" counter: a Running CVD is healthy (the fleet listing is
// the live probe), Unreachable/absent counts toward zombie, and bring-up
// transients and lifecycle-owned states hold the counter.
func TestService_HealthFromCVDState(t *testing.T) {
	t.Parallel()
	cfg := device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10}
	svc := New(cfg, devicetest.NewFakeRepo(), hosttest.NewFakeRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
	running := &device.Device{ID: "a", HostID: "h", Status: device.StatusRunning}
	check := func(cvdState string) device.HealthUpdate {
		derived, settled := device.DerivePhase(running, cvdState)
		return svc.deviceHealthUpdate(running, cvdState, derived, settled)
	}

	if u := check(device.CVDStateRunning); u.Status != device.HealthStatusHealthy || u.CVDStatus != device.CVDStateRunning {
		t.Errorf("Running → %+v, want healthy with the observation recorded", u)
	}
	if u := check(device.CVDStateUnreachable); u.ConsecutiveFails != 1 || u.CVDStatus != device.CVDStateUnreachable {
		t.Errorf("Unreachable → %+v, want a counted failure (the zombie signal)", u)
	}
	if u := check(""); u.ConsecutiveFails != 1 || u.FailReason == "" {
		t.Errorf("absent CVD → %+v, want a counted failure", u)
	}
	for _, held := range []string{
		device.CVDStatePreparing, device.CVDStateStarting, device.CVDStateStopped,
		device.CVDStateBootFailed, device.CVDStateUnknown,
	} {
		if u := check(held); u.ConsecutiveFails != 0 {
			t.Errorf("%q → ConsecutiveFails=%d, want 0 (held; not a health failure)", held, u.ConsecutiveFails)
		}
	}
}

// A stopped row whose CVD is running again heals to running. That is the
// shape left behind when a start request dies mid-flight. The host listing is
// the only place that drift is visible, which is why stopped devices are
// visited at all.
func TestService_HealsStoppedDeviceWithRunningCVD(t *testing.T) {
	t.Parallel()
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cvds" && r.Method == http.MethodGet {
			w.Write([]byte(`{"cvds": [{"group": "cvdstopped", "name": "i1", "status": "Running"}]}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer ho.Close()
	port := int64(ho.Listener.Addr().(*net.TCPAddr).Port)

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{ID: "h", Status: host.StatusRunning, InternalIP: "127.0.0.1", HostOrchestratorPort: port}

	deviceRepo := devicetest.NewFakeRepo()
	d := &device.Device{
		ID: "stopped01", HostID: "h", CVDID: "cvdstopped/i1",
		Status: device.StatusStopped, HealthStatus: device.HealthStatusHealthy,
	}
	deviceRepo.Devices[d.ID] = d

	deviceRepo.NeedingCheck = []*device.Device{d}

	cfg := device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10}
	svc := New(cfg, deviceRepo, hostRepo, hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
	// The full cycle, not checkHostDevices directly: the visit filter is what
	// used to drop stopped rows before any observation could reach them.
	if err := svc.runHealthChecks(t.Context()); err != nil {
		t.Fatalf("runHealthChecks: %v", err)
	}

	if got := deviceRepo.StatusUpdates["stopped01"]; got != device.StatusRunning {
		t.Errorf("status update = %q, want running (a live CVD under a stopped row is drift)", got)
	}
}

// A stopped device is at rest. Nothing about its host, whether gone,
// unreachable or settled stopped, makes it unhealthy, and neither does a CVD
// that is absent or stopped. Only the running-CVD drift above is worth
// reporting.
func TestService_StoppedDeviceIsNeverFailed(t *testing.T) {
	t.Parallel()
	cfg := device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10}

	for _, hostStatus := range []host.Status{host.StatusStopped, host.StatusUnreachable, host.StatusError} {
		hostRepo := hosttest.NewFakeRepo()
		hostRepo.Hosts["h"] = &host.Host{ID: "h", Status: hostStatus}
		deviceRepo := devicetest.NewFakeRepo()
		d := &device.Device{
			ID: "stopped01", HostID: "h", CVDID: "cvd/1",
			Status: device.StatusStopped, HealthStatus: device.HealthStatusHealthy,
		}
		deviceRepo.Devices[d.ID] = d

		svc := New(cfg, deviceRepo, hostRepo, hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
		svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], []*device.Device{d})

		if len(deviceRepo.HealthBatches) != 1 {
			t.Fatalf("host %s: want one health batch, got %+v", hostStatus, deviceRepo.HealthBatches)
		}
		if got := deviceRepo.HealthBatches[0][0]; got.ConsecutiveFails != 0 {
			t.Errorf("host %s: ConsecutiveFails = %d, want 0 (a stopped device cannot fail)", hostStatus, got.ConsecutiveFails)
		}
		if got := deviceRepo.StatusUpdates["stopped01"]; got != "" {
			t.Errorf("host %s: lifecycle status rewritten to %q, want untouched", hostStatus, got)
		}
	}
}

// A host orchestrator with no /cvds endpoint falls back to the operator's
// device list, which carries no status, so the cycle observed nothing about
// any CVD. That must hold health rather than fail it: counting it marched
// every device on the host to zombie within ten minutes over the host's
// missing endpoint, while the devices themselves were fine.
func TestService_UnobservedListingHoldsHealth(t *testing.T) {
	t.Parallel()
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cvds":
			http.Error(w, `{"error": "404 page not found"}`, http.StatusNotFound)
		case "/devices":
			w.Write([]byte(`[{"device_id": "cvdfallbk0-1", "group_name": "cvdfallbk0", "name": "1", "adb_port": 6520}]`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer ho.Close()
	port := int64(ho.Listener.Addr().(*net.TCPAddr).Port)

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{ID: "h", Status: host.StatusRunning, InternalIP: "127.0.0.1", HostOrchestratorPort: port}

	deviceRepo := devicetest.NewFakeRepo()
	d := &device.Device{
		ID: "fallback1", HostID: "h", CVDID: "cvdfallbk0/1",
		Status: device.StatusRunning, HealthStatus: device.HealthStatusHealthy,
	}
	deviceRepo.Devices[d.ID] = d

	cfg := device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10}
	svc := New(cfg, deviceRepo, hostRepo, hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
	svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], []*device.Device{d})

	if len(deviceRepo.HealthBatches) != 1 || len(deviceRepo.HealthBatches[0]) != 1 {
		t.Fatalf("want exactly one health update, got %+v", deviceRepo.HealthBatches)
	}
	got := deviceRepo.HealthBatches[0][0]
	if got.ConsecutiveFails != 0 {
		t.Errorf("ConsecutiveFails = %d, want 0 (nothing was observed, so nothing failed)", got.ConsecutiveFails)
	}
	if got.Status != device.HealthStatusHealthy {
		t.Errorf("health = %q, want the prior condition held", got.Status)
	}
	if got.CVDStatus != device.CVDStateUnknown {
		t.Errorf("CVDStatus = %q, want the unobserved state recorded as it was", got.CVDStatus)
	}
	if deviceRepo.Devices["fallback1"].Status != device.StatusRunning {
		t.Error("lifecycle status must be untouched by an unobserved cycle")
	}
}

// A starting host (VM up, HO not yet confirmed) is promoted to running the
// moment its Host Orchestrator answers, which is the ho_reachable condition.
// It has no placed devices yet, so the visit's only effect is the promotion.
func TestService_PromotesStartingHostOnHOContact(t *testing.T) {
	t.Parallel()
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"cvds": []}`)) // HO reachable; a fresh host has no CVDs
	}))
	defer ho.Close()
	port := int64(ho.Listener.Addr().(*net.TCPAddr).Port)

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{
		ID: "h", Status: host.StatusStarting, DesiredState: host.DesiredRunning,
		VMStatus: "RUNNING", InternalIP: "127.0.0.1", HostOrchestratorPort: port,
	}

	cfg := device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10}
	svc := New(cfg, devicetest.NewFakeRepo(), hostRepo, hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
	svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], nil)

	got := hostRepo.Hosts["h"]
	if got.Status != host.StatusRunning {
		t.Errorf("status = %q, want running (HO answered -> promoted)", got.Status)
	}
	if got.HOReadyAt == nil {
		t.Error("HOReadyAt not recorded on first HO contact")
	}
}

// End to end: a running device whose CVD reports a terminal failure has its
// lifecycle status derived to error; an Unreachable CVD crosses the zombie
// threshold; a Running CVD stays healthy.
func TestService_DerivesLifecycleFromCVD(t *testing.T) {
	t.Parallel()
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cvds" && r.Method == http.MethodGet {
			w.Write([]byte(`{"cvds": [
				{"group": "cvdbootfl0", "name": "i1", "status": "Boot failed"},
				{"group": "cvdzombie0", "name": "i1", "status": "Unreachable"},
				{"group": "cvdhealthy", "name": "i1", "status": "Running"}
			]}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer ho.Close()
	port := int64(ho.Listener.Addr().(*net.TCPAddr).Port)

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{ID: "h", Status: host.StatusRunning, InternalIP: "127.0.0.1", HostOrchestratorPort: port}

	deviceRepo := devicetest.NewFakeRepo()
	bootfail := &device.Device{ID: "bootfail1", HostID: "h", CVDID: "cvdbootfl0/i1", Status: device.StatusRunning, HealthStatus: device.HealthStatusHealthy}
	zombie := &device.Device{ID: "zombie001", HostID: "h", CVDID: "cvdzombie0/i1", Status: device.StatusRunning, HealthStatus: device.HealthStatusHealthy, ConsecutiveFails: 9}
	healthy := &device.Device{ID: "healthy01", HostID: "h", CVDID: "cvdhealthy/i1", Status: device.StatusRunning}
	for _, d := range []*device.Device{bootfail, zombie, healthy} {
		deviceRepo.Devices[d.ID] = d
	}

	cfg := device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10}
	svc := New(cfg, deviceRepo, hostRepo, hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
	svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], []*device.Device{bootfail, zombie, healthy})

	if got := deviceRepo.Devices["bootfail1"].Status; got != device.StatusError {
		t.Errorf("boot-failed device status = %q, want error (terminal CVD failure derives error)", got)
	}
	if got := deviceRepo.HealthUpdates["zombie001"]; got != device.HealthStatusZombie {
		t.Errorf("unreachable device health = %q, want zombie", got)
	}
	if got := deviceRepo.HealthUpdates["healthy01"]; got != device.HealthStatusHealthy {
		t.Errorf("running device health = %q, want healthy", got)
	}
}

// The stranded-create suspector files device.reconcile for devices stuck
// pending/creating past the grace with NO live create task, while a slow
// create with a live (leased) task, a fresh create, and settled devices are
// left alone. The reconcile handler heals the filed ones from desired_spec.
func TestService_SuspectsStrandedCreates(t *testing.T) {
	t.Parallel()
	stalePast := time.Now().Add(-staleTransitionalAfter - time.Minute)
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["stranded-pending"] = &device.Device{
		ID: "stranded-pending", HostID: "h", Status: device.StatusPending, UpdatedAt: stalePast,
	}
	deviceRepo.Devices["stranded-creating"] = &device.Device{
		ID: "stranded-creating", HostID: "h", Status: device.StatusCreating, UpdatedAt: stalePast,
	}
	deviceRepo.Devices["slow-owned"] = &device.Device{
		ID: "slow-owned", HostID: "h", Status: device.StatusCreating, UpdatedAt: stalePast,
	}
	deviceRepo.Devices["fresh"] = &device.Device{
		ID: "fresh", HostID: "h", Status: device.StatusCreating, UpdatedAt: time.Now(),
	}
	deviceRepo.Devices["running"] = &device.Device{
		ID: "running", HostID: "h", Status: device.StatusRunning, UpdatedAt: stalePast,
	}

	rec := &taskRecorder{}
	config := device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10}
	svc := New(config, deviceRepo, hosttest.NewFakeRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithReconcileSuspector(ReconcileSuspector{
			Tasks:    &fakeTaskChecker{t: t, wantType: task.TypeDeviceCreateCVD, ownedID: "slow-owned"},
			Enqueuer: rec,
		}))

	svc.suspectStrandedCreates(t.Context())

	wantIDs := map[string]bool{"stranded-pending": true, "stranded-creating": true}
	if len(rec.ids) != 2 || !wantIDs[rec.ids[0]] || !wantIDs[rec.ids[1]] || rec.ids[0] == rec.ids[1] {
		t.Errorf("suspicion enqueued for %v, want exactly the two stranded devices", rec.ids)
	}
	for _, typ := range rec.types {
		if typ != task.TypeDeviceReconcile {
			t.Errorf("filed %v, want only %s", rec.types, task.TypeDeviceReconcile)
		}
	}

	// Unwired: no detector at all.
	rec.ids = nil
	svcOff := New(config, deviceRepo, hosttest.NewFakeRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
	svcOff.suspectStrandedCreates(t.Context())
	if len(rec.ids) != 0 {
		t.Errorf("suspicion enqueued with no suspector wired: %v", rec.ids)
	}
}

// fakeLiveOperations reports a fixed set of devices as being under a live
// DEVICE-WIDE mutating operation, a verb with no instance name, which exempts
// every instance. Per-instance sets are fabricated in checks_instances_test.go.
type fakeLiveOperations struct {
	live map[string]bool
	err  error
}

func (f *fakeLiveOperations) LiveByResources(_ context.Context, _ string, ids []string) (operation.LiveSet, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make(operation.LiveSet, len(ids))
	for _, id := range ids {
		if f.live[id] {
			out[operation.LiveResource{ResourceID: id}] = true
		}
	}
	return out, nil
}

// A powerwash wipes userdata and boots the device again, so `cvd fleet`
// legitimately reports the instance as Stopped for minutes. Settling from that
// observation writes `stopped` onto a device whose powerwash is working. The
// row contradicts the verb, and a user can act on it (starting the device
// races the powerwash). It self-heals when the CVD comes up, but the window is
// the whole verb.
//
// The device is owned by its operation for that duration: observe, do not act.
func TestService_DoesNotSettleLifecycleUnderALiveVerb(t *testing.T) {
	t.Parallel()
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cvds" && r.Method == http.MethodGet {
			// Mid-powerwash: the instance is down between the wipe and first boot.
			w.Write([]byte(`{"cvds": [{"group": "cvdpw", "name": "i1", "status": "Stopped"}]}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	// t.Cleanup, not defer: this test has PARALLEL subtests, so the parent
	// returns (running its defers) before they execute. A closed server made the
	// guard subtest pass for the wrong reason: the check never reached the
	// device loop at all.
	t.Cleanup(ho.Close)
	port := int64(ho.Listener.Addr().(*net.TCPAddr).Port)

	newFixture := func() (*devicetest.FakeRepo, *host.Host) {
		hostRepo := &host.Host{
			ID: "h", Status: host.StatusRunning, InternalIP: "127.0.0.1", HostOrchestratorPort: port,
		}
		deviceRepo := devicetest.NewFakeRepo()
		deviceRepo.Devices["pw01"] = &device.Device{
			ID: "pw01", HostID: "h", CVDID: "cvdpw/i1",
			Status: device.StatusRunning, HealthStatus: device.HealthStatusHealthy,
		}
		return deviceRepo, hostRepo
	}
	cfg := device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10}

	t.Run("guard wired: the row is left alone", func(t *testing.T) {
		t.Parallel()
		deviceRepo, h := newFixture()
		hostRepo := hosttest.NewFakeRepo()
		hostRepo.Hosts["h"] = h

		svc := New(cfg, deviceRepo, hostRepo, hoclient.NewFactory(hoclient.DefaultClientConfig()),
			slog.Default(), WithLiveOperations(&fakeLiveOperations{live: map[string]bool{"pw01": true}}))
		svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], []*device.Device{deviceRepo.Devices["pw01"]})

		if got, ok := deviceRepo.StatusUpdates["pw01"]; ok {
			t.Errorf("wrote status %q to a device under a live powerwash; the verb owns it", got)
		}
		if got := deviceRepo.Devices["pw01"].Status; got != device.StatusRunning {
			t.Errorf("device status = %q, want the unchanged running", got)
		}
		// Health is HELD, not failed: counting these observations walks the device
		// to zombie, which with ZombieRecreate on recreates the CVD mid-verb.
		if len(deviceRepo.HealthBatches) != 1 {
			t.Fatalf("want one health batch, got %d", len(deviceRepo.HealthBatches))
		}
		if got := deviceRepo.HealthBatches[0][0].Status; got == device.HealthStatusUnhealthy {
			t.Errorf("health degraded to %q under a live verb; the observation is expected, not a failure", got)
		}
	})

	t.Run("no guard: the pre-operations behavior", func(t *testing.T) {
		t.Parallel()
		deviceRepo, h := newFixture()
		hostRepo := hosttest.NewFakeRepo()
		hostRepo.Hosts["h"] = h

		svc := New(cfg, deviceRepo, hostRepo, hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
		svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], []*device.Device{deviceRepo.Devices["pw01"]})

		// This is the bug: without the guard the row is settled to stopped from a
		// state the powerwash is passing through.
		if got := deviceRepo.StatusUpdates["pw01"]; got != device.StatusStopped {
			t.Errorf("status update = %q; expected the unguarded plane to settle stopped", got)
		}
	})
}

// A failed lookup must degrade to settling from the observation. A device controller
// that stops settling leaves the console showing stale state, which is the
// worse of the two failures.
func TestService_LiveVerbLookupFailureStillSettles(t *testing.T) {
	t.Parallel()
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cvds" && r.Method == http.MethodGet {
			w.Write([]byte(`{"cvds": [{"group": "cvdpw", "name": "i1", "status": "Stopped"}]}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer ho.Close()
	port := int64(ho.Listener.Addr().(*net.TCPAddr).Port)

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{
		ID: "h", Status: host.StatusRunning, InternalIP: "127.0.0.1", HostOrchestratorPort: port,
	}
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["pw01"] = &device.Device{
		ID: "pw01", HostID: "h", CVDID: "cvdpw/i1",
		Status: device.StatusRunning, HealthStatus: device.HealthStatusHealthy,
	}

	cfg := device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10}
	svc := New(cfg, deviceRepo, hostRepo, hoclient.NewFactory(hoclient.DefaultClientConfig()),
		slog.Default(), WithLiveOperations(&fakeLiveOperations{err: errors.New("spanner unavailable")}))
	svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], []*device.Device{deviceRepo.Devices["pw01"]})

	if got := deviceRepo.StatusUpdates["pw01"]; got != device.StatusStopped {
		t.Errorf("status update = %q; a failed lookup must not stop the plane settling", got)
	}
}

// Settling anyway is the right call, but it means the guard is silently
// absent: the settled devices look identical whether it protected anything
// or not. The
// record must therefore carry the structured marker the live smoke fails on,
// because that field is the ONLY difference between "guard working" and
// "guard skipped".
func TestService_LiveVerbLookupFailureIsMarkedAsDegraded(t *testing.T) {
	t.Parallel()
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cvds" && r.Method == http.MethodGet {
			w.Write([]byte(`{"cvds": [{"group": "cvdpw", "name": "i1", "status": "Stopped"}]}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer ho.Close()
	port := int64(ho.Listener.Addr().(*net.TCPAddr).Port)

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{
		ID: "h", Status: host.StatusRunning, InternalIP: "127.0.0.1", HostOrchestratorPort: port,
	}
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["pw01"] = &device.Device{
		ID: "pw01", HostID: "h", CVDID: "cvdpw/i1",
		Status: device.StatusRunning, HealthStatus: device.HealthStatusHealthy,
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	cfg := device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10}
	svc := New(cfg, deviceRepo, hostRepo, hoclient.NewFactory(hoclient.DefaultClientConfig()),
		logger, WithLiveOperations(&fakeLiveOperations{err: errors.New("spanner unavailable")}))
	svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], []*device.Device{deviceRepo.Devices["pw01"]})

	if !strings.Contains(buf.String(), `"`+telemetry.GuardDegradedKey+`":true`) {
		t.Errorf("degraded lookup logged without the %s marker; the smoke can never see it.\nlogged: %s",
			telemetry.GuardDegradedKey, buf.String())
	}
}

// The marker must not appear when the guard works, or the smoke fails every run
// and the signal is worth nothing.
func TestService_WorkingLiveVerbLookupIsNotMarkedAsDegraded(t *testing.T) {
	t.Parallel()
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cvds" && r.Method == http.MethodGet {
			w.Write([]byte(`{"cvds": [{"group": "cvdpw", "name": "i1", "status": "Stopped"}]}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer ho.Close()
	port := int64(ho.Listener.Addr().(*net.TCPAddr).Port)

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{
		ID: "h", Status: host.StatusRunning, InternalIP: "127.0.0.1", HostOrchestratorPort: port,
	}
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["pw01"] = &device.Device{
		ID: "pw01", HostID: "h", CVDID: "cvdpw/i1",
		Status: device.StatusRunning, HealthStatus: device.HealthStatusHealthy,
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	cfg := device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10}
	svc := New(cfg, deviceRepo, hostRepo, hoclient.NewFactory(hoclient.DefaultClientConfig()),
		logger, WithLiveOperations(&fakeLiveOperations{live: map[string]bool{"pw01": true}}))
	svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], []*device.Device{deviceRepo.Devices["pw01"]})

	if strings.Contains(buf.String(), telemetry.GuardDegradedKey) {
		t.Errorf("a working guard emitted the degraded marker: %s", buf.String())
	}
}

// panickingLens stands in for any panic reachable under a host visit.
type panickingLens struct{}

func (panickingLens) LiveByResources(context.Context, string, []string) (operation.LiveSet, error) {
	panic("boom from under a host visit")
}

// Every host visit runs in an errgroup goroutine with no recovery of its own, so
// before this a panic anywhere beneath one killed the process, and the process
// also serves the API, so Cloud Run answers 503 until it restarts.
//
// CVDGroupName's unchecked deviceID[:8] was exactly such a panic, reachable from
// the census for any device row shorter than 8 characters. That slice is fixed,
// but the recovery is what makes the class survivable rather than the instance.
func TestService_HostVisitPanicDoesNotKillTheProcess(t *testing.T) {
	t.Parallel()
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cvds" && r.Method == http.MethodGet {
			w.Write([]byte(`{"cvds": [{"group": "cvdpanic", "name": "i1", "status": "Running"}]}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(ho.Close)
	port := int64(ho.Listener.Addr().(*net.TCPAddr).Port)

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{
		ID: "h", Status: host.StatusRunning, InternalIP: "127.0.0.1", HostOrchestratorPort: port,
	}
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["panic01"] = &device.Device{
		ID: "panic01", HostID: "h", CVDID: "cvdpanic/i1",
		Status: device.StatusRunning, HealthStatus: device.HealthStatusHealthy,
	}

	cfg := device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10}
	svc := New(cfg, deviceRepo, hostRepo, hoclient.NewFactory(hoclient.DefaultClientConfig()),
		slog.Default(), WithLiveOperations(panickingLens{}))

	// If the recovery is missing this does not fail. It takes the test binary
	// down, which is what the test is for.
	svc.visitHost(t.Context(), hostRepo.Hosts["h"], "h", []*device.Device{deviceRepo.Devices["panic01"]})
}

// A device id shorter than the slice CVDGroupName used to take is the panic that
// found the missing recovery. Production ids are UUIDs, which is the only reason
// it never fired.
func TestCVDGroupNameSurvivesAShortID(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"", "a", "pw01", "1234567", "12345678", "12345678-90ab"} {
		got := device.CVDGroupName(id)
		if !strings.HasPrefix(got, "cvd") {
			t.Errorf("CVDGroupName(%q) = %q, must start with a letter", id, got)
		}
	}
	if got, want := device.CVDGroupName("12345678-90ab"), "cvd12345678"; got != want {
		t.Errorf("CVDGroupName truncation changed: %q, want %q", got, want)
	}
}

// The health cycle also retries the starting-host promotion, and its
// worst case is a pocket of unreachable hosts each burning its full probe
// timeout: at the 8-slot fan-out, 64 hang-until-deadline hosts cost ~8 waves
// of one probe timeout, comfortably under an interval; serialized (a
// collapsed fan-out, or a probe path that stops honoring its bound) the same
// fleet costs 64 timeouts and blows the budget here. The hang server accepts
// TCP and never answers, which is the expensive failure shape. A refused dial
// is cheap.
func TestScaledCycleSurvivesUnreachablePocket(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		var held []net.Conn
		defer func() {
			for _, c := range held {
				_ = c.Close()
			}
		}()
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			held = append(held, conn) // accept and never answer
		}
	}()
	port := int64(ln.Addr().(*net.TCPAddr).Port)

	hostRepo := hosttest.NewFakeRepo()
	for i := range 64 {
		id := fmt.Sprintf("scale-%02d", i)
		hostRepo.Hosts[id] = &host.Host{
			ID: id, Status: host.StatusRunning, MaxDevices: 4,
			IPAddress: "127.0.0.1", HostOrchestratorPort: port,
		}
	}

	const probeTimeout = 150 * time.Millisecond
	svc := New(
		device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10},
		devicetest.NewFakeRepo(), hostRepo,
		hoclient.NewFactory(hoclient.ClientConfig{
			Timeout:    probeTimeout,
			MaxRetries: 0,
			CallBudget: -1, // the per-attempt Timeout is the bound under test
			// All 64 hosts share one URL and therefore one breaker; it must
			// never trip, or fail-fast refusals would fake a fast cycle.
			CircuitBreakerMaxFailures: 1 << 20,
		}),
		slog.New(slog.DiscardHandler))

	start := time.Now()
	if err := svc.runHealthChecks(t.Context()); err != nil {
		t.Fatalf("runHealthChecks: %v", err)
	}
	elapsed := time.Since(start)

	// 64 hosts / 8 slots ≈ 8 waves × 150ms ≈ 1.2s expected; 64 × 150ms ≈
	// 9.6s serialized. The bound separates the two with margin both ways.
	if elapsed >= 4*time.Second {
		t.Fatalf("cycle took %v with 64 hanging hosts; the fan-out is not bounding probe cost "+
			"(expected ~8 waves of %v)", elapsed, probeTimeout)
	}
}
