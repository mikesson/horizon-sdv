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

//go:build goexperiment.goroutineleakprofile

// Goroutine-leak assertions for the device controller. Beyond the
// telemetry.Runner loop it shares with every other reconciler, each cycle fans
// out to one goroutine per host through an errgroup. Those goroutines live
// inside a cycle and are joined by it, which the Runner's own assertions
// cannot see.

package devicecontroller

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/device/devicetest"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/tests/leakcheck"
)

func TestGoroutineLeakDetectorIsLive(t *testing.T) {
	leakcheck.AssertDetectorLive(t)
}

// seedHostsAt registers n running hosts pointing at addr, each with a device
// needing a health check, so a cycle has something to fan out to.
func seedHostsAt(t *testing.T, addr string, n int) (*devicetest.FakeRepo, *hosttest.FakeRepo) {
	t.Helper()
	u, err := url.Parse(addr)
	require.NoError(t, err)
	ip, portStr, err := net.SplitHostPort(u.Host)
	require.NoError(t, err)
	port, err := strconv.ParseInt(portStr, 10, 64)
	require.NoError(t, err)

	deviceRepo := devicetest.NewFakeRepo()
	hostRepo := hosttest.NewFakeRepo()
	for i := range n {
		id := "leak-host-" + strconv.Itoa(i)
		hostRepo.Hosts[id] = &host.Host{
			ID:                   id,
			Status:               host.StatusRunning,
			InternalIP:           ip,
			HostOrchestratorPort: port,
		}
		d := &device.Device{
			ID: "leak-device-" + strconv.Itoa(i), HostID: id,
			Status: device.StatusRunning, HealthStatus: device.HealthStatusHealthy,
		}
		deviceRepo.Devices[d.ID] = d
	}
	return deviceRepo, hostRepo
}

func newLeakService(deviceRepo device.Repository, hostRepo host.Repository) *Service {
	return New(
		device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10},
		deviceRepo, hostRepo,
		hoclient.NewFactory(hoclient.DefaultClientConfig()),
		slog.New(slog.DiscardHandler),
	)
}

// The cycle's fan-out goroutines must be joined by the cycle, and the cycle by
// Stop. More hosts than maxConcurrentHostChecks so the errgroup's limit queues
// some of them: a goroutine still waiting for a slot when the cycle ends is as
// much a leak as one still probing.
func TestServiceStopLeavesNoGoroutineLeak(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"cvds":[]}`))
	}))
	defer srv.Close()

	w := leakcheck.Start(t)

	func() {
		deviceRepo, hostRepo := seedHostsAt(t, srv.URL, maxConcurrentHostChecks*2)
		svc := newLeakService(deviceRepo, hostRepo)
		svc.Start(t.Context()) // RunAtStart, so a cycle begins immediately.
		svc.Stop()
	}() // Scoped so the service is unreachable before the detecting GC.

	leakcheck.AssertNone(t, w, "devicecontroller Stop")
}

// Stop landing while the fan-out is in flight. Every probe is parked on a
// server that never answers, so the only thing that can free those goroutines
// is the cycle context Stop cancels, the shutdown shape a real unreachable
// host produces.
func TestServiceStopDuringHostProbeLeavesNoGoroutineLeak(t *testing.T) {
	probing := make(chan struct{}, maxConcurrentHostChecks)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case probing <- struct{}{}:
		default:
		}
		<-r.Context().Done() // Answers only when the client goes away.
	}))
	defer srv.Close()

	w := leakcheck.Start(t)

	func() {
		deviceRepo, hostRepo := seedHostsAt(t, srv.URL, maxConcurrentHostChecks*2)
		svc := newLeakService(deviceRepo, hostRepo)
		svc.Start(t.Context())

		select {
		case <-probing: // At least one host probe is really in flight.
		case <-time.After(30 * time.Second):
			t.Fatal("no host probe reached the test server; the cycle never fanned out")
		}
		svc.Stop()
	}()

	leakcheck.AssertNone(t, w, "devicecontroller Stop mid-probe")
}
