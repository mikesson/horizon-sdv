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
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/device/devicetest"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
)

// zombieActuator builds an actuator over a device with the given state and
// returns the actuator + repo for assertions.
func zombieActuator(t *testing.T, d *device.Device) (*Actuator, *devicetest.FakeRepo) {
	t.Helper()
	repo := devicetest.NewFakeRepo(d)
	return newTestActuator(t, repo, hosttest.NewFakeRepo()), repo
}

func runningZombie(id string) *device.Device {
	return &device.Device{
		ID: id, HostID: "h", Name: id, Mode: "simple", InstanceCount: 1,
		Status: device.StatusRunning, HealthStatus: device.HealthStatusZombie,
		ConsecutiveFails: 12,
	}
}

// A fresh zombie is recreated: status flips to creating, the stale health is
// cleared so the row stops lying, and the recreate budget opens at 1.
func TestRecoverZombie_RecreatesFresh(t *testing.T) {
	t.Parallel()
	act, repo := zombieActuator(t, runningZombie("z1"))

	recreate, err := act.RecoverZombie(t.Context(), "z1")
	require.NoError(t, err)
	assert.True(t, recreate, "a healable zombie must schedule a recreate")

	got := repo.Devices["z1"]
	assert.Equal(t, device.StatusCreating, got.Status)
	assert.Equal(t, device.HealthStatusUnknown, got.HealthStatus)
	assert.Zero(t, got.ConsecutiveFails)
	assert.Equal(t, int64(1), got.RecreateCount)
	require.NotNil(t, got.RecreateWindowStart)
}

// Within the window, the budget increments and recreation continues until it is
// spent; the last attempt over budget fails the device with a reason.
func TestRecoverZombie_BudgetExhaustionFails(t *testing.T) {
	t.Parallel()
	d := runningZombie("z2")
	now := time.Now()
	d.RecreateWindowStart = &now
	d.RecreateCount = zombieRecreateBudget // already at the cap
	act, repo := zombieActuator(t, d)

	recreate, err := act.RecoverZombie(t.Context(), "z2")
	require.NoError(t, err)
	assert.False(t, recreate, "over budget must not recreate")

	got := repo.Devices["z2"]
	assert.Equal(t, device.StatusError, got.Status)
	assert.Contains(t, got.ErrorMessage, "zombie")
}

// A lapsed budget window resets the count and recreation resumes.
func TestRecoverZombie_WindowResets(t *testing.T) {
	t.Parallel()
	d := runningZombie("z3")
	old := time.Now().Add(-2 * zombieRecreateWindow)
	d.RecreateWindowStart = &old
	d.RecreateCount = zombieRecreateBudget
	act, repo := zombieActuator(t, d)

	recreate, err := act.RecoverZombie(t.Context(), "z3")
	require.NoError(t, err)
	assert.True(t, recreate, "a lapsed window reopens the budget")
	assert.Equal(t, int64(1), repo.Devices["z3"].RecreateCount)
}

// A zombie with no persisted spec (pre-desired_spec device) cannot be recreated
// correctly, so it is failed instead of rebuilt from defaults.
func TestRecoverZombie_UnhealableFails(t *testing.T) {
	t.Parallel()
	d := runningZombie("z4")
	d.Mode = "" // no desired_spec
	act, repo := zombieActuator(t, d)

	recreate, err := act.RecoverZombie(t.Context(), "z4")
	require.NoError(t, err)
	assert.False(t, recreate)
	assert.Equal(t, device.StatusError, repo.Devices["z4"].Status)
	assert.Contains(t, repo.Devices["z4"].ErrorMessage, "no persisted spec")
}

// A device that recovered on its own (no longer running+zombie) is a clean
// no-op: no recreate, no error, state untouched.
func TestRecoverZombie_RacedRecovery(t *testing.T) {
	t.Parallel()
	d := runningZombie("z5")
	d.HealthStatus = device.HealthStatusHealthy // recovered between scan and reconcile
	act, repo := zombieActuator(t, d)

	recreate, err := act.RecoverZombie(t.Context(), "z5")
	require.NoError(t, err)
	assert.False(t, recreate)
	assert.Equal(t, device.StatusRunning, repo.Devices["z5"].Status, "must not touch a recovered device")
	assert.Empty(t, repo.Devices["z5"].ErrorMessage)
}

// A present-but-unresponsive CVD (Unreachable) is relaunched in place first.
// The device stays running+zombie (a nudge), the CVD :restart is issued, and
// the restart budget opens at 1.
func TestRestartZombieCVD_RelaunchesUnreachable(t *testing.T) {
	t.Parallel()
	var restarted atomic.Int32
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ":restart") && r.Method == http.MethodPost:
			restarted.Add(1)
			w.Write([]byte(`{"name": "op-restart"}`))
		case strings.Contains(r.URL.Path, "/operations/"):
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected HO request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
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
	d := runningZombie("zr1")
	d.CVDStatus = device.CVDStateUnreachable
	d.CVDID = "cvdzr1group/dev-1"
	deviceRepo.Devices[d.ID] = d
	act := newTestActuator(t, deviceRepo, hostRepo)

	ok, err := act.RestartZombieCVD(t.Context(), "zr1")
	require.NoError(t, err)
	assert.True(t, ok, "an Unreachable CVD with budget must be restarted, not recreated")
	assert.Equal(t, int32(1), restarted.Load(), "the CVD :restart must be issued")
	got := deviceRepo.Devices["zr1"]
	assert.Equal(t, int64(1), got.RestartCount, "restart budget opens at 1")
	assert.Equal(t, device.StatusRunning, got.Status, "restart is a nudge, not a transition")
	assert.Equal(t, device.HealthStatusZombie, got.HealthStatus, "health stays zombie until the next check confirms recovery")
}

// The restart step is skipped (fall through to recreate) when the CVD is gone
// (not merely hung) or the restart budget is spent. Neither touches the host.
func TestRestartZombieCVD_SkipsToRecreate(t *testing.T) {
	t.Parallel()
	absent := runningZombie("zr2")
	absent.CVDStatus = "" // CVD gone, not merely unresponsive
	actAbsent, _ := zombieActuator(t, absent)
	ok, err := actAbsent.RestartZombieCVD(t.Context(), "zr2")
	require.NoError(t, err)
	assert.False(t, ok, "an absent CVD must fall through to recreate")

	spent := runningZombie("zr3")
	spent.CVDStatus = device.CVDStateUnreachable
	now := time.Now()
	spent.RestartWindowStart = &now
	spent.RestartCount = zombieRestartBudget
	actSpent, _ := zombieActuator(t, spent)
	ok, err = actSpent.RestartZombieCVD(t.Context(), "zr3")
	require.NoError(t, err)
	assert.False(t, ok, "a spent restart budget must fall through to recreate")
}

// A recreate replaces the CVD, so it resets the restart budget. The fresh CVD
// starts with a full one.
func TestRecoverZombie_RecreateResetsRestartBudget(t *testing.T) {
	t.Parallel()
	d := runningZombie("zr4")
	now := time.Now()
	d.RestartWindowStart = &now
	d.RestartCount = zombieRestartBudget
	act, repo := zombieActuator(t, d)

	recreate, err := act.RecoverZombie(t.Context(), "zr4")
	require.NoError(t, err)
	assert.True(t, recreate)
	got := repo.Devices["zr4"]
	assert.Zero(t, got.RestartCount, "recreate resets the restart budget")
	assert.Nil(t, got.RestartWindowStart)
}
