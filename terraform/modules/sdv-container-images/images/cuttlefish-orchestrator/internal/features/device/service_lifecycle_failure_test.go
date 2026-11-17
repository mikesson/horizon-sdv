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
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// failingLifecycleHO accepts a lifecycle verb and then reports the operation as
// having failed, in the HO's stored-error shape, which is a terminal outcome
// rather than a transport problem. This is the realistic "the stop really did
// not take" case, and the one that used to mark the device `error`.
func failingLifecycleHO(t *testing.T) int64 {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/:start"), strings.HasSuffix(r.URL.Path, "/:stop"):
			w.Write([]byte(`{"name": "op-1"}`))
		case strings.Contains(r.URL.Path, "/operations/"):
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error": "cvd stop failed"}`))
		default:
			t.Errorf("unexpected HO request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)

	parsed, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parsing fake HO URL: %v", err)
	}
	port, err := strconv.ParseInt(parsed.Port(), 10, 64)
	if err != nil {
		t.Fatalf("parsing fake HO port: %v", err)
	}
	return port
}

// A failed synchronous start or stop must leave the device row at its current,
// accurate status. The verb failing does not mean the device is in an error
// state. After a failed stop the CVD is still running and the row already says
// so, so writing `error` replaces accurate state with inaccurate state, and for
// stop it is unrecoverable, because error -> stopped is not a legal transition,
// so the device controller can never correct it and swallows the attempt
// silently.
//
// The status write is asserted absent. Asserting only that its effect is
// invisible here would pass with the damaging call still in place.
func TestFailedLifecycleVerbLeavesStatusAccurate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		verb  string
		start device.Status
		call  func(*DeviceService) error
	}{
		{"stop", device.StatusRunning, func(s *DeviceService) error {
			return s.Stop(t.Context(), "d1")
		}},
		{"start", device.StatusStopped, func(s *DeviceService) error {
			return s.Start(t.Context(), "d1")
		}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			t.Parallel()
			port := failingLifecycleHO(t)

			hostRepo := hosttest.NewFakeRepo()
			hostRepo.Hosts["h1"] = &host.Host{
				ID: "h1", Status: host.StatusRunning, MaxDevices: 4,
				IPAddress: "127.0.0.1", HostOrchestratorPort: port,
			}
			deviceRepo := newDeviceRepoFake()
			deviceRepo.Devices["d1"] = &device.Device{
				ID: "d1", Name: "dev", HostID: "h1", Status: tc.start, CVDID: "cvdd1/dev",
			}

			svc, err := NewDeviceService(deviceRepo, hostRepo, newMockBuildRepo(),
				hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
				WithTaskEnqueuer(&fakeEnqueuer{}))
			if err != nil {
				t.Fatalf("building service: %v", err)
			}

			if err := tc.call(svc); err == nil {
				t.Fatal("a failed host operation must reach the caller as an error")
			}

			if got, ok := deviceRepo.StatusUpdates["d1"]; ok {
				t.Errorf("%s wrote status %q; a failed verb must not write the row at all", tc.verb, got)
			}
			if got := deviceRepo.Devices["d1"].Status; got != tc.start {
				t.Errorf("device status = %q after a failed %s, want the unchanged %q", got, tc.verb, tc.start)
			}
		})
	}
}
