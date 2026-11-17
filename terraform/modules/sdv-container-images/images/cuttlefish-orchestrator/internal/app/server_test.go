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

package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/config"
	"cloud-cuttlefish-orchestrator/internal/middleware"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// The write deadline a request runs under has to cover its handler. A device
// verb that waits out a Host Orchestrator operation runs for minutes: under
// the ordinary bound it broke its own response mid-flight, which the platform
// reported as a connection error, answered 503, and recycled the serving
// instance, while the operation completed on the host unseen.
func TestRequestDeadlineClasses(t *testing.T) {
	t.Parallel()
	cfg := config.DefaultConfig()
	classify := requestDeadlines(cfg)

	ordinary := time.Duration(cfg.Server.WriteTimeout)
	slow := time.Duration(cfg.Server.SlowRequestTimeout)
	if slow <= ordinary {
		t.Fatalf("slow write deadline %v must exceed the ordinary %v", slow, ordinary)
	}

	tests := []struct {
		path      string
		wantWrite time.Duration
		why       string
	}{
		{"/v2/devices/abc/restart", slow, "relaunches the CVD"},
		{"/v2/devices/abc/powerwash", slow, "reboots to first boot"},
		{"/v2/devices/abc/start", slow, "boots the CVD"},
		{"/v2/devices/abc/stop", slow, "waits out the stop operation"},
		{"/v2/devices/abc/powerbtn", slow, "waits out the operation"},
		{"/v2/devices/abc/bugreport", slow, "adb bugreport takes minutes"},
		{"/v2/devices/abc/recordings/rec.webm", slow, "streams host->storage"},
		{"/v2/devices/abc/displays/0/screenshot", ordinary, "bounded by its own budget"},
		{"/v2/devices", ordinary, "plain API call"},
		{"/v2/hosts/abc", ordinary, "plain API call"},
		{"/v1/zones/z/hosts/h/cvds", 0, "legacy passthrough streams"},
		{"/v2/storage/buckets/b/objects/o", 0, "object streaming"},
		{"/v2/devices/abc/signaling/ws", 0, "hijacked connection"},
	}
	for _, tc := range tests {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, tc.path, http.NoBody)
		if got := classify(req).Write; got != tc.wantWrite {
			t.Errorf("%s: write deadline = %v, want %v (%s)", tc.path, got, tc.wantWrite, tc.why)
		}
	}
}

// The classifier above decides which deadline a route runs under; hoclient
// decides how long a verb may wait inside it. Those two numbers are chosen in
// different packages and nothing connects them, so this asserts they are the
// same numbers. The arithmetic in hoclient's own budget test is only sound if
// its envelopes are the deadlines actually installed here.
//
// A screenshot is the case that shows why it matters. It sits on the ordinary
// deadline "bounded by its own budget", which held for the wait considered
// alone and not for the request: a 20s wait behind a 20s POST is 40s against a
// 30s bound. The envelope is what makes that claim true.
func TestVerbEnvelopesMatchTheConfiguredDeadlines(t *testing.T) {
	t.Parallel()
	cfg := config.DefaultConfig()

	if got, want := hoclient.InteractiveVerbEnvelope, time.Duration(cfg.Server.WriteTimeout); got != want {
		t.Errorf("interactive verb envelope = %v, but the ordinary write deadline is %v", got, want)
	}
	if got, want := hoclient.ExtendedVerbEnvelope, time.Duration(cfg.Server.SlowRequestTimeout); got != want {
		t.Errorf("extended verb envelope = %v, but the slow write deadline is %v", got, want)
	}
}

// End to end through the real classifier: a handler that outruns the ordinary
// deadline must, on a host-operation route, both deliver its response AND keep
// a live context. The context half is the trap: an expired READ deadline
// cancels the request out from under the handler, which surfaces as a 500
// "context canceled" at exactly the ordinary deadline instead of a broken
// connection, i.e. the same failure with a different status code.
func TestHostOperationRequestOutrunsTheOrdinaryDeadline(t *testing.T) {
	t.Parallel()
	const handlerRuntime = 300 * time.Millisecond

	cfg := config.DefaultConfig()
	cfg.Server.ReadTimeout = config.Duration(50 * time.Millisecond)
	cfg.Server.WriteTimeout = config.Duration(50 * time.Millisecond)
	cfg.Server.SlowRequestTimeout = config.Duration(5 * time.Second)

	srv := httptest.NewServer(middleware.RequestDeadlines(requestDeadlines(cfg))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(handlerRuntime)
			if err := r.Context().Err(); err != nil {
				w.Write([]byte("canceled: " + err.Error()))
				return
			}
			w.Write([]byte("done"))
		})))
	defer srv.Close()

	body, err := get(t, srv.URL+"/v2/devices/abc/restart")
	if err != nil {
		t.Fatalf("host-operation route: %v; want the response delivered", err)
	}
	if body != "done" {
		t.Errorf("host-operation route returned %q, want %q", body, "done")
	}

	if _, err := get(t, srv.URL+"/v2/devices/abc"); err == nil {
		t.Error("ordinary route outran its deadline but arrived intact; the deadline is not applied")
	}
}

func get(t *testing.T, url string) (string, error) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}
