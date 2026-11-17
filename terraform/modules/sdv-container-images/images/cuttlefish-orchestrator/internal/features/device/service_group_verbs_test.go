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
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	deviceDomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// Starting and stopping a device act on all of it. The host serves the group and
// per-instance verbs as separate handlers under one verb spelling, and reaching
// the per-instance one moves the first member while its siblings stay as they
// were. A stop leaves them running, still billed and still holding their taps
// and operator registrations, and a start leaves them stopped. Both spellings
// succeed against a real host, so the requested path is the only thing that
// distinguishes them.
func TestDeviceServiceLifecycleVerbsAddressTheWholeGroup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		verb     string
		status   deviceDomain.Status
		call     func(*DeviceService, context.Context) error
		wantPath string
	}{
		{
			name:   "stop",
			verb:   "/:stop",
			status: deviceDomain.StatusRunning,
			call: func(s *DeviceService, ctx context.Context) error {
				return s.Stop(ctx, "test-device")
			},
			wantPath: "/cvds/cvdgroup/:stop",
		},
		{
			name:   "start",
			verb:   "/:start",
			status: deviceDomain.StatusStopped,
			call: func(s *DeviceService, ctx context.Context) error {
				return s.Start(ctx, "test-device")
			},
			wantPath: "/cvds/cvdgroup/:start",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var verbPath atomic.Value
			ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, tc.verb) {
					verbPath.Store(r.URL.Path)
					_, _ = w.Write([]byte(`{"name": "op", "done": true}`))
					return
				}
				_, _ = w.Write([]byte(`{"done": true}`))
			}))
			t.Cleanup(ho.Close)
			hoURL, _ := url.Parse(ho.URL)
			port, _ := strconv.ParseInt(hoURL.Port(), 10, 64)

			hostRepo := hosttest.NewFakeRepo()
			hostRepo.Hosts["test-host"] = &host.Host{
				ID: "test-host", Status: host.StatusRunning,
				IPAddress: "127.0.0.1", HostOrchestratorPort: port,
			}
			deviceRepo := newDeviceRepoFake()
			deviceRepo.Devices["test-device"] = &deviceDomain.Device{
				ID: "test-device", HostID: "test-host", Name: "alpha",
				Status: tc.status, CVDID: "cvdgroup/alpha",
			}

			svc, err := NewDeviceService(deviceRepo, hostRepo, newMockBuildRepo(),
				hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
				WithTaskEnqueuer(&fakeEnqueuer{}))
			if err != nil {
				t.Fatalf("wiring the device service: %v", err)
			}

			if err := tc.call(svc, t.Context()); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			if got := verbPath.Load(); got != tc.wantPath {
				t.Errorf("%s posted to %v, want the group route %s; the per-instance"+
					" route moves the first member and leaves its siblings behind",
					tc.name, got, tc.wantPath)
			}
		})
	}
}
