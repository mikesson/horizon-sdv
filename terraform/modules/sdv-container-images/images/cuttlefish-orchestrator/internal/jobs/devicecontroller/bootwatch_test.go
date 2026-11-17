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
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/device/devicetest"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// bootWatchHO fakes the HO for the hard case in the health window. The fleet
// enum stays Running however the guest behaves (run_cvd's status reply is an
// unconditional success), so the ONLY place a post-create loop is visible is
// the group's own logs.
func bootWatchHO(t *testing.T, kernelLog string, logRequests *atomic.Int32) (*hosttest.FakeRepo, *devicetest.FakeRepo, *Service) {
	t.Helper()
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cvds" && r.Method == http.MethodGet:
			w.Write([]byte(`{"cvds": [{"group": "cvddeadbeef", "name": "dev-1", "status": "Running"}]}`))
		case strings.HasSuffix(r.URL.Path, "/logs/kernel.log"):
			logRequests.Add(1)
			w.Write([]byte(kernelLog))
		case strings.HasSuffix(r.URL.Path, "/logs/launcher.log"):
			logRequests.Add(1)
			w.Write([]byte("secure_env detected guest reboot, restarting.\n"))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(ho.Close)
	return healthServiceFor(t, ho)
}

// healthServiceFor wires a health service against one fake Host Orchestrator.
func healthServiceFor(t *testing.T, ho *httptest.Server) (*hosttest.FakeRepo, *devicetest.FakeRepo, *Service) {
	t.Helper()
	port := int64(ho.Listener.Addr().(*net.TCPAddr).Port)

	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{
		ID: "h", Status: host.StatusRunning, InternalIP: "127.0.0.1", HostOrchestratorPort: port,
	}
	deviceRepo := devicetest.NewFakeRepo()
	svc := New(device.HealthCheckConfig{Interval: time.Minute, FailureThreshold: 3, ZombieThreshold: 10},
		deviceRepo, hostRepo, hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
	return hostRepo, deviceRepo, svc
}

func settledRunningDevice() *device.Device {
	started := time.Now().Add(-(bootSettleWindow + 5*time.Minute))
	return &device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "dev-1",
		Status: device.StatusRunning, HealthStatus: device.HealthStatusHealthy,
		CVDID: "cvddeadbeef/dev-1", CVDStatus: device.CVDStateRunning,
		StartedAt: &started,
		// What the repository hands every device row: the array the host
		// reported, or the one instance synthesized from cvd_id.
		Instances: []device.Instance{{Name: "dev-1", State: device.CVDStateRunning}},
	}
}

func loopingKernelTail() string {
	return loopingKernelTailPanicking("bad XCR0")
}

// loopingKernelTailPanicking crosses the panic threshold within one tail, with
// a caller-chosen panic line so a per-instance test can tell whose log the
// promoted evidence came from.
func loopingKernelTailPanicking(panicLine string) string {
	var b strings.Builder
	for range device.LoopThresholdKernelPanics + 1 {
		b.WriteString("U-Boot SPL 2023.07\nU-Boot 2023.07\n")
		fmt.Fprintf(&b, "[    0.730000] Kernel panic - not syncing: %s\n", panicLine)
	}
	return b.String()
}

func healthyKernelTail() string {
	return "U-Boot SPL\nU-Boot 2023\nhandoff\n[   61.0] init: VIRTUAL_DEVICE_BOOT_COMPLETED\n"
}

// The post-create loop: every layer reads green (enum Running, health
// healthy) while the guest reboots under it. The boot watch must settle the
// row to error with the evidence, through the ordinary derivation path.
func TestService_BootLoopUnderRunningEnumSettlesError(t *testing.T) {
	t.Parallel()
	var logRequests atomic.Int32
	hostRepo, deviceRepo, svc := bootWatchHO(t, loopingKernelTail(), &logRequests)
	d := settledRunningDevice()
	deviceRepo.Devices[d.ID] = d

	svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], []*device.Device{d})

	if got := deviceRepo.StatusUpdates["deadbeef-dev"]; got != device.StatusError {
		t.Fatalf("status update = %q, want error settled from the boot observation", got)
	}
	if msg := deviceRepo.Devices["deadbeef-dev"].ErrorMessage; !strings.Contains(msg, "guest boot loop") ||
		!strings.Contains(msg, "bad XCR0") {
		t.Errorf("error message must carry the evidence, got %q", msg)
	}
	if len(deviceRepo.HealthBatches) != 1 || deviceRepo.HealthBatches[0][0].BootObs == nil {
		t.Fatal("the enriched observation must ride the health batch (one sample with the enum)")
	}
	if v := deviceRepo.HealthBatches[0][0].BootObs.Verdict; v != device.BootVerdictLooping {
		t.Errorf("riding verdict = %q, want looping", v)
	}
}

// A quiescent healthy device pays one kernel-tail probe per cadence and
// nothing else: no launcher fetch, no settle, verdict booted.
func TestService_BootWatchHealthyQuiescentStaysGreen(t *testing.T) {
	t.Parallel()
	var logRequests atomic.Int32
	hostRepo, deviceRepo, svc := bootWatchHO(t, healthyKernelTail(), &logRequests)
	d := settledRunningDevice()
	deviceRepo.Devices[d.ID] = d

	svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], []*device.Device{d})

	if got, ok := deviceRepo.StatusUpdates["deadbeef-dev"]; ok {
		t.Fatalf("healthy device was lifecycle-written to %q", got)
	}
	if n := logRequests.Load(); n != 1 {
		t.Errorf("log fetches = %d, want exactly 1 (no suspicion, no launcher fetch)", n)
	}
	if len(deviceRepo.HealthBatches) != 1 || deviceRepo.HealthBatches[0][0].BootObs == nil {
		t.Fatal("the healthy sample must still ride (it is the growth baseline)")
	}
	if v := deviceRepo.HealthBatches[0][0].BootObs.Verdict; v != device.BootVerdictBooted {
		t.Errorf("riding verdict = %q, want booted", v)
	}
}

// The settle window and the cadence budget both gate the fetch: a freshly
// started device is the create task's to watch, and a device sampled less
// than an interval ago owes nothing this cycle.
func TestService_BootWatchBudget(t *testing.T) {
	t.Parallel()
	t.Run("inside the settle window", func(t *testing.T) {
		t.Parallel()
		var logRequests atomic.Int32
		hostRepo, deviceRepo, svc := bootWatchHO(t, loopingKernelTail(), &logRequests)
		d := settledRunningDevice()
		started := time.Now().Add(-time.Minute)
		d.StartedAt = &started
		deviceRepo.Devices[d.ID] = d

		svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], []*device.Device{d})
		if n := logRequests.Load(); n != 0 {
			t.Errorf("log fetches = %d, want 0 inside the settle window", n)
		}
		if _, ok := deviceRepo.StatusUpdates["deadbeef-dev"]; ok {
			t.Error("no observation, no settle")
		}
	})
	t.Run("inside the cadence", func(t *testing.T) {
		t.Parallel()
		var logRequests atomic.Int32
		hostRepo, deviceRepo, svc := bootWatchHO(t, loopingKernelTail(), &logRequests)
		d := settledRunningDevice()
		d.BootObservation = &device.BootObservation{ObservedAt: time.Now().Add(-time.Minute)}
		deviceRepo.Devices[d.ID] = d

		svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], []*device.Device{d})
		if n := logRequests.Load(); n != 0 {
			t.Errorf("log fetches = %d, want 0 inside the probe cadence", n)
		}
	})
}

// logPaths records the log paths a fake HO was asked for. The requested path
// is what has to be asserted: an instance name the group never had 404s
// silently, so a watch that fetched nothing is indistinguishable from a
// healthy one by its result alone.
type logPaths struct {
	mu   sync.Mutex
	seen []string
}

func (l *logPaths) record(reqPath string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, reqPath)
}

func (l *logPaths) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.seen)
}

// instanceLog is one instance's kernel.log as the fake serves it: the tail,
// and the total size its Content-Range reports (zero = the tail's own length).
// The two are separable so a test can put loop-rate growth on an instance
// without carrying megabytes of fixture. The real host answers the same way,
// a suffix range over a file far larger than the tail.
type instanceLog struct {
	tail  string
	total int64
}

// perInstanceLogHO fakes a host whose group holds several instances: each
// named instance serves its own kernel.log, a name the group does not have
// 404s as the real logs route does, and the fleet listing reports every
// instance Running, so the enum is green whatever the guests are doing.
func perInstanceLogHO(t *testing.T, group string, logs map[string]instanceLog, seen *logPaths) *httptest.Server {
	t.Helper()
	entries := make([]string, 0, len(logs))
	for _, name := range slices.Sorted(maps.Keys(logs)) {
		entries = append(entries, fmt.Sprintf(`{"group":%q,"name":%q,"status":%q}`,
			group, name, device.CVDStateRunning))
	}
	listing := `{"cvds": [` + strings.Join(entries, ",") + `]}`

	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/logs/") {
			w.Write([]byte(listing))
			return
		}
		seen.record(r.URL.Path)
		// /cvds/<group>/<instance>/logs/<file>
		instLog, ok := logs[path.Base(path.Dir(path.Dir(r.URL.Path)))]
		switch {
		case !ok:
			http.Error(w, `{"error":"cvd not found"}`, http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, device.LauncherLogFile):
			w.Write([]byte("secure_env detected guest reboot, restarting.\n"))
		case instLog.total > 0:
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(instLog.tail)-1, instLog.total))
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte(instLog.tail))
		default:
			w.Write([]byte(instLog.tail))
		}
	}))
	t.Cleanup(ho.Close)
	return ho
}

// The logs belong to the instance the HOST created, not to the device row: an
// advanced-mode create names its own instances, and the row's name is then a
// path with no logs behind it.
func TestService_BootWatchAddressesThePersistedInstance(t *testing.T) {
	t.Parallel()
	var seen logPaths
	ho := perInstanceLogHO(t, "cvddeadbeef", map[string]instanceLog{
		"watch-1": {tail: healthyKernelTail()},
	}, &seen)
	hostRepo, deviceRepo, svc := healthServiceFor(t, ho)
	d := settledRunningDevice()
	d.Name = "dev-a"
	d.CVDID = "cvddeadbeef/dev-a"
	d.Instances = []device.Instance{{Name: "watch-1", State: device.CVDStateRunning}}
	deviceRepo.Devices[d.ID] = d

	svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], []*device.Device{d})

	if got := seen.all(); !slices.Contains(got, "/cvds/cvddeadbeef/watch-1/logs/kernel.log") {
		t.Fatalf("log paths = %q, want the instance's own kernel.log", got)
	}
	for _, p := range seen.all() {
		if strings.Contains(p, "/dev-a/") {
			t.Errorf("fetched %q: the device row's name is not an instance the host knows", p)
		}
	}
	if len(deviceRepo.HealthBatches) != 1 || deviceRepo.HealthBatches[0][0].BootObs == nil {
		t.Fatal("the sample must ride the health batch; a 404 would leave the cycle un-enriched")
	}
	if v := deviceRepo.HealthBatches[0][0].BootObs.Verdict; v != device.BootVerdictBooted {
		t.Errorf("riding verdict = %q, want booted from watch-1's own log", v)
	}
}

// Instances boot independently and upstream resets the whole group when any
// one of them fails, so a loop in a later instance is the device's loop. The
// watch reads every instance and promotes the failing one's evidence.
func TestService_BootWatchSamplesEveryInstance(t *testing.T) {
	t.Parallel()
	var seen logPaths
	ho := perInstanceLogHO(t, "cvddeadbeef", map[string]instanceLog{
		"cvd-1": {tail: healthyKernelTail()},
		"cvd-2": {tail: healthyKernelTail()},
		"cvd-3": {tail: loopingKernelTailPanicking("bad XCR0 on cvd-3")},
	}, &seen)
	hostRepo, deviceRepo, svc := healthServiceFor(t, ho)
	d := settledRunningDevice()
	d.CVDID = "cvddeadbeef/cvd-1"
	d.Instances = []device.Instance{
		{Name: "cvd-1", State: device.CVDStateRunning},
		{Name: "cvd-2", State: device.CVDStateRunning},
		{Name: "cvd-3", State: device.CVDStateRunning},
	}
	deviceRepo.Devices[d.ID] = d

	svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], []*device.Device{d})

	got := seen.all()
	for _, name := range []string{"cvd-1", "cvd-2", "cvd-3"} {
		if !slices.Contains(got, "/cvds/cvddeadbeef/"+name+"/logs/kernel.log") {
			t.Errorf("log paths = %q, want %s sampled too", got, name)
		}
	}
	if st := deviceRepo.StatusUpdates["deadbeef-dev"]; st != device.StatusError {
		t.Fatalf("status update = %q, want error settled from the looping instance", st)
	}
	if msg := deviceRepo.Devices["deadbeef-dev"].ErrorMessage; !strings.Contains(msg, "bad XCR0 on cvd-3") {
		t.Errorf("error message must carry the failing instance's panic, got %q", msg)
	}
	if len(deviceRepo.HealthBatches) != 1 || deviceRepo.HealthBatches[0][0].BootObs == nil {
		t.Fatal("the enriched observation must ride the health batch")
	}
	if v := deviceRepo.HealthBatches[0][0].BootObs.Verdict; v != device.BootVerdictLooping {
		t.Errorf("riding verdict = %q, want the looping instance's", v)
	}
}

// The sample that rides is the first instance's, and the growth rate is
// measured against it alone. Here the second instance's kernel.log is
// megabytes bigger than the persisted sample: rating one instance's size
// against another's would read that difference as loop-rate growth and settle
// a group whose first instance has booted and whose second is still coming up.
func TestService_BootWatchGrowthRateStaysWithinOneInstance(t *testing.T) {
	t.Parallel()
	var seen logPaths
	ho := perInstanceLogHO(t, "cvddeadbeef", map[string]instanceLog{
		"cvd-1": {tail: healthyKernelTail()},
		"cvd-2": {tail: "U-Boot SPL 2023.07\nU-Boot 2023.07\n", total: 8 << 20},
	}, &seen)
	hostRepo, deviceRepo, svc := healthServiceFor(t, ho)
	d := settledRunningDevice()
	d.CVDID = "cvddeadbeef/cvd-1"
	d.Instances = []device.Instance{
		{Name: "cvd-1", State: device.CVDStateRunning},
		{Name: "cvd-2", State: device.CVDStateStarting},
	}
	sampledAt := time.Now().Add(-(bootProbeInterval + time.Minute))
	d.BootObservation = &device.BootObservation{KernelLogBytes: 4096, ObservedAt: sampledAt}
	deviceRepo.Devices[d.ID] = d

	svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], []*device.Device{d})

	if st, ok := deviceRepo.StatusUpdates["deadbeef-dev"]; ok {
		t.Fatalf("settled %q from a size compared across two instances' logs", st)
	}
	if len(deviceRepo.HealthBatches) != 1 || deviceRepo.HealthBatches[0][0].BootObs == nil {
		t.Fatal("the first instance's sample must still ride")
	}
	if v := deviceRepo.HealthBatches[0][0].BootObs.Verdict; v != device.BootVerdictBooted {
		t.Errorf("riding verdict = %q, want cvd-1's own", v)
	}
}

// Under a live verb the observation still accumulates but the settle is
// deferred: a powerwash legitimately transits boot states, and the verb owns
// the row until its operation clears.
func TestService_BootWatchUnderLiveVerbObservesButDefers(t *testing.T) {
	t.Parallel()
	var logRequests atomic.Int32
	hostRepo, deviceRepo, svc := bootWatchHO(t, loopingKernelTail(), &logRequests)
	svc.operations = &fakeLiveOperations{live: map[string]bool{"deadbeef-dev": true}}
	d := settledRunningDevice()
	deviceRepo.Devices[d.ID] = d

	svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], []*device.Device{d})

	if got, ok := deviceRepo.StatusUpdates["deadbeef-dev"]; ok {
		t.Fatalf("settled %q under a live verb; the verb owns the row", got)
	}
	if len(deviceRepo.HealthBatches) != 1 || deviceRepo.HealthBatches[0][0].BootObs == nil {
		t.Fatal("the observation must still accumulate under the verb (settle once it clears)")
	}
}

// A create that reaches Running keeps only its load-status trail on the row.
// The device controller writes its sample over the whole column, so the first
// enrichment after the create must carry that trail, from whichever instance
// the riding sample came.
func TestService_BootWatchKeepsCreateTransitions(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	trail := []device.BootTransition{
		{Status: "", At: t0},
		{Status: device.CVDStateStarting, At: t0.Add(3 * time.Minute)},
		{Status: device.CVDStateRunning, At: t0.Add(5 * time.Minute)},
	}
	tests := []struct {
		name        string
		logs        map[string]instanceLog
		wantVerdict device.BootVerdict
	}{
		{
			name:        "healthy first instance rides",
			logs:        map[string]instanceLog{"cvd-1": {tail: healthyKernelTail()}},
			wantVerdict: device.BootVerdictBooted,
		},
		{
			name: "terminal later instance rides",
			logs: map[string]instanceLog{
				"cvd-1": {tail: healthyKernelTail()},
				"cvd-2": {tail: loopingKernelTail()},
			},
			wantVerdict: device.BootVerdictLooping,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var seen logPaths
			ho := perInstanceLogHO(t, "cvddeadbeef", tc.logs, &seen)
			hostRepo, deviceRepo, svc := healthServiceFor(t, ho)
			d := settledRunningDevice()
			d.CVDID = "cvddeadbeef/cvd-1"
			d.Instances = nil
			for _, name := range slices.Sorted(maps.Keys(tc.logs)) {
				d.Instances = append(d.Instances, device.Instance{Name: name, State: device.CVDStateRunning})
			}
			// The shape the create's final commit leaves: the trail and nothing else.
			d.BootObservation = &device.BootObservation{Transitions: slices.Clone(trail)}
			deviceRepo.Devices[d.ID] = d

			svc.checkHostDevices(t.Context(), hostRepo.Hosts["h"], []*device.Device{d})

			if len(deviceRepo.HealthBatches) != 1 || len(deviceRepo.HealthBatches[0]) != 1 {
				t.Fatalf("health batches = %d, want one batch of one update", len(deviceRepo.HealthBatches))
			}
			obs := deviceRepo.HealthBatches[0][0].BootObs
			if obs == nil {
				t.Fatal("the enrichment must ride the health batch")
			}
			if obs.Verdict != tc.wantVerdict {
				t.Errorf("riding verdict = %q, want %q", obs.Verdict, tc.wantVerdict)
			}
			if !slices.Equal(obs.Transitions, trail) {
				t.Errorf("riding transitions = %v, want the create's trail %v", obs.Transitions, trail)
			}
		})
	}
}
