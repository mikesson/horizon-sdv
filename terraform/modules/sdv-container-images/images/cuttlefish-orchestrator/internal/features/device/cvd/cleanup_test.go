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
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/device/devicetest"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// launcherTimeoutLog is the launcher.log tail of the live incident: run_cvd's
// boot state machine gives up, then the launcher stops its subprocesses.
const launcherTimeoutLog = "run_cvd(2977)  E 10-04 12:11:51  2977  2982 boot_state_machine.cc:577] " +
	"TimeoutThreadLoop: Did not receive boot completion event after 10m\n" +
	"run_cvd(2977)  I 10-04 12:11:51  2977  2982 process_monitor.cc:155] Stopping monitored subprocesses\n"

// stuckPairHO fakes a host whose SDV pair the fleet listing keeps reporting
// Starting, serving each instance's logs from the given maps (an instance
// absent from a map serves that file empty). It records every logs fetch and
// whether the group was deleted.
type stuckPairHO struct {
	srv     *httptest.Server
	deleted atomic.Bool
	mu      sync.Mutex
	fetched []string
}

func newStuckPairHO(t *testing.T, kernelLogs, launcherLogs map[string]string) *stuckPairHO {
	t.Helper()
	ho := &stuckPairHO{}
	ho.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cvds" && r.Method == http.MethodGet:
			w.Write([]byte(`{"cvds":[` +
				`{"group":"cvddeadbeef","name":"core","status":"Starting"},` +
				`{"group":"cvddeadbeef","name":"ivi","status":"Starting"}]}`))
		case r.URL.Path == "/cvds/cvddeadbeef" && r.Method == http.MethodDelete:
			ho.deleted.Store(true)
			w.Write([]byte(`{"name": "op-del"}`))
		case strings.Contains(r.URL.Path, "/logs/"):
			// /cvds/{group}/{name}/logs/{file}
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			ho.mu.Lock()
			ho.fetched = append(ho.fetched, r.URL.Path)
			ho.mu.Unlock()
			if len(parts) != 5 {
				http.NotFound(w, r)
				return
			}
			logs := kernelLogs
			if parts[4] == device.LauncherLogFile {
				logs = launcherLogs
			}
			w.Write([]byte(logs[parts[2]]))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(ho.srv.Close)
	return ho
}

func (ho *stuckPairHO) logFetches() int {
	ho.mu.Lock()
	defer ho.mu.Unlock()
	return len(ho.fetched)
}

// cleanupActuator builds an actuator whose running host is the fake, with the
// deleted device's row carrying the given boot observation.
func cleanupActuator(t *testing.T, ho *stuckPairHO, obs *device.BootObservation) *Actuator {
	t.Helper()
	u, _ := url.Parse(ho.srv.URL)
	port, _ := strconv.ParseInt(u.Port(), 10, 64)
	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{
		ID: "h", Status: host.StatusRunning, MaxDevices: 4,
		IPAddress: "127.0.0.1", HostOrchestratorPort: port,
	}
	deviceRepo := devicetest.NewFakeRepo()
	deviceRepo.Devices["deadbeef-dev"] = &device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "sdv", Status: device.StatusDeleted,
		BootObservation: obs,
	}
	return newTestActuator(t, deviceRepo, hostRepo)
}

func freshCleanupTask() *task.Task {
	return &task.Task{ResourceID: "deadbeef-dev", Attempts: 1, MaxAttempts: task.DefaultMaxAttempts, CreatedAt: time.Now()}
}

// The live incident: the Core's launcher timed out its boot and stopped its
// subprocesses, but the listing still says Starting. The cleanup must delete
// the group on its first delivery instead of postponing for launchWaitCap
// while the pair holds the host's instance slots. Either instance's launcher
// log may carry the timeout, so both positions are covered.
func TestCleanupDeletesGroupWhoseLauncherGaveUp(t *testing.T) {
	t.Parallel()
	for _, instance := range []string{"core", "ivi"} {
		t.Run(instance, func(t *testing.T) {
			t.Parallel()
			ho := newStuckPairHO(t, nil, map[string]string{instance: launcherTimeoutLog})
			h := NewCleanupHandler(cleanupActuator(t, ho, dwellServed()))

			require.NoError(t, h.Handle(t.Context(), freshCleanupTask()),
				"a launcher that gave up leaves nothing to wait for")
			assert.True(t, ho.deleted.Load(), "the group must be deleted on the first delivery")
		})
	}
}

// A guest that loops still has a live launcher, which is the case the wait
// protects: a terminal loop verdict must not release the group.
func TestCleanupWaitsOnLoopingLaunch(t *testing.T) {
	t.Parallel()
	ho := newStuckPairHO(t,
		map[string]string{"core": loopingKernelLog()},
		map[string]string{"core": "secure_env detected guest reboot, restarting.\n"})
	h := NewCleanupHandler(cleanupActuator(t, ho, dwellServed()))

	err := h.Handle(t.Context(), freshCleanupTask())
	resched, ok := errors.AsType[*task.RescheduleError](err)
	require.True(t, ok, "a looping launch must keep the cleanup waiting, got %v", err)
	assert.Equal(t, launchInFlightRetry, resched.After)
	assert.False(t, ho.deleted.Load(), "deleting under a live launcher leaks its processes")
	assert.Positive(t, ho.logFetches(), "the postpone must have sampled the logs")
}

// A launch younger than the boot window cannot have timed out, so the
// postpone skips the sample and costs no log fetch.
func TestCleanupSkipsSampleInsideBootWindow(t *testing.T) {
	t.Parallel()
	since := time.Now().Add(-time.Minute)
	ho := newStuckPairHO(t, nil, map[string]string{"core": launcherTimeoutLog})
	h := NewCleanupHandler(cleanupActuator(t, ho,
		&device.BootObservation{TransientSince: &since, ObservedAt: since}))

	err := h.Handle(t.Context(), freshCleanupTask())
	_, ok := errors.AsType[*task.RescheduleError](err)
	require.True(t, ok, "inside the boot window the cleanup postpones, got %v", err)
	assert.False(t, ho.deleted.Load())
	assert.Zero(t, ho.logFetches(), "no sample inside the boot window")
}
