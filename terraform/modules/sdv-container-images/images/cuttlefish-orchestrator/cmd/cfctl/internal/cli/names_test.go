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

package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// IDs the tests pass where a command takes a device or host. Anything that is
// not a UUID is looked up as a name.
const (
	testDeviceID = "0d000000-0000-4000-8000-000000000001"
	testHostID   = "0a000000-0000-4000-8000-000000000001"
)

// Two more IDs for the name-lookup tests.
const (
	otherDeviceID = "0d000000-0000-4000-8000-000000000002"
	otherHostID   = "0a000000-0000-4000-8000-000000000002"
)

// nameBackend serves GET /v2/devices?name=web-1 and GET /v2/hosts?name=cf-host-3
// from the given list bodies, GET by ID for any device or host, and counts
// the lookups. A lookup for any other name fails the test.
type nameBackend struct {
	t       *testing.T
	devices string
	hosts   string
	lookups atomic.Int32
	// gets records the IDs read by GET /v2/devices/{id} and /v2/hosts/{id}.
	gets []string
	// created is the body of the last POST /v2/devices.
	created map[string]any
}

func (b *nameBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requireJWT(b.t, r)
	switch {
	case r.Method == http.MethodGet && (r.URL.Path == "/v2/devices" || r.URL.Path == "/v2/hosts"):
		b.lookups.Add(1)
		body, want := b.devices, "web-1"
		if r.URL.Path == "/v2/hosts" {
			body, want = b.hosts, "cf-host-3"
		}
		if got := r.URL.Query().Get("name"); got != want {
			b.t.Errorf("list %s with name filter %q, want %q", r.URL.Path, got, want)
		}
		fmt.Fprintf(w, `{"items":[%s],"next_page_token":""}`, body)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/devices/"):
		id := strings.TrimPrefix(r.URL.Path, "/v2/devices/")
		b.gets = append(b.gets, id)
		fmt.Fprintf(w, `{"id":%q,"name":"web-1","host_id":%q,"status":"running"}`, id, testHostID)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/hosts/"):
		id := strings.TrimPrefix(r.URL.Path, "/v2/hosts/")
		b.gets = append(b.gets, id)
		fmt.Fprintf(w, `{"id":%q,"name":"cf-host-3","zone":"z","status":"running"}`, id)
	case r.Method == http.MethodPost && r.URL.Path == "/v2/devices":
		if err := json.NewDecoder(r.Body).Decode(&b.created); err != nil {
			b.t.Errorf("decoding create body: %v", err)
		}
		fmt.Fprintf(w, `{"id":%q,"name":"d","host_id":%q,"status":"pending"}`, testDeviceID, testHostID)
	default:
		b.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

func deviceItem(id, host, status, creator string) string {
	return fmt.Sprintf(`{"id":%q,"name":"web-1","host_id":%q,"status":%q,"created_by":%q,"created_at":"2026-10-20T08:00:00Z"}`,
		id, host, status, creator)
}

func hostItem(id, zone, status string) string {
	return fmt.Sprintf(`{"id":%q,"name":"cf-host-3","zone":%q,"status":%q,"created_by":"a@example.com","created_at":"2026-10-20T08:00:00Z"}`,
		id, zone, status)
}

func TestResolveDevice_OneMatchIsUsed(t *testing.T) {
	b := &nameBackend{t: t, devices: deviceItem(testDeviceID, testHostID, "running", "a@example.com")}
	out, err := runCLI(t, b, "device", "get", "web-1")
	if err != nil {
		t.Fatalf("device get web-1: %v", err)
	}
	if b.lookups.Load() != 1 {
		t.Errorf("lookups = %d, want 1", b.lookups.Load())
	}
	if len(b.gets) != 1 || b.gets[0] != testDeviceID {
		t.Errorf("GETs = %v, want the matched ID only", b.gets)
	}
	if !strings.Contains(out, testDeviceID) {
		t.Errorf("output does not show the resolved device:\n%s", out)
	}
}

func TestResolveDevice_SeveralMatchesFailWithATable(t *testing.T) {
	b := &nameBackend{t: t, devices: deviceItem(testDeviceID, testHostID, "running", "a@example.com") + "," +
		deviceItem(otherDeviceID, otherHostID, "stopped", "b@example.com")}
	_, err := runCLI(t, b, "device", "delete", "web-1", "--force")
	if err == nil {
		t.Fatal("an ambiguous name must fail")
	}
	if got := exitCodeFor(err, false); got != ExitConflict {
		t.Errorf("exit code = %d, want %d", got, ExitConflict)
	}
	msg := err.Error()
	if got := strings.Join(strings.Fields(strings.Split(msg, "\n")[2]), " "); got != "ID HOST STATUS CREATED BY CREATED" {
		t.Errorf("table header = %q in:\n%s", got, msg)
	}
	for _, want := range []string{testDeviceID, otherDeviceID, otherHostID, "stopped", "b@example.com"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error is missing %q:\n%s", want, msg)
		}
	}
	if len(b.gets) != 0 {
		t.Errorf("GETs = %v: an ambiguous name must not pick one", b.gets)
	}
}

func TestResolveDevice_NoMatchIsNotFound(t *testing.T) {
	b := &nameBackend{t: t}
	_, err := runCLI(t, b, "device", "stop", "web-1")
	if err == nil || !strings.Contains(err.Error(), "no device is named web-1") {
		t.Fatalf("err = %v, want the not-found message", err)
	}
	if got := exitCodeFor(err, false); got != ExitNotFound {
		t.Errorf("exit code = %d, want %d", got, ExitNotFound)
	}
}

// A UUID is an ID: it goes straight to the device and never lists.
func TestResolveDevice_UUIDNeverLists(t *testing.T) {
	b := &nameBackend{t: t, devices: deviceItem(otherDeviceID, testHostID, "running", "a@example.com")}
	if _, err := runCLI(t, b, "device", "get", testDeviceID); err != nil {
		t.Fatalf("device get: %v", err)
	}
	if b.lookups.Load() != 0 {
		t.Errorf("a UUID argument listed devices %d times", b.lookups.Load())
	}
	if len(b.gets) != 1 || b.gets[0] != testDeviceID {
		t.Errorf("GETs = %v, want the given ID", b.gets)
	}
}

func TestResolveHost_OneMatchIsUsed(t *testing.T) {
	b := &nameBackend{t: t, hosts: hostItem(testHostID, "z-a", "running")}
	if _, err := runCLI(t, b, "host", "get", "cf-host-3"); err != nil {
		t.Fatalf("host get cf-host-3: %v", err)
	}
	if len(b.gets) != 1 || b.gets[0] != testHostID {
		t.Errorf("GETs = %v, want the matched host", b.gets)
	}
}

func TestResolveHost_SeveralMatchesFailWithATable(t *testing.T) {
	b := &nameBackend{t: t, hosts: hostItem(testHostID, "z-a", "running") + "," + hostItem(otherHostID, "z-b", "stopped")}
	_, err := runCLI(t, b, "host", "extend", "cf-host-3", "--hours", "2")
	if err == nil {
		t.Fatal("an ambiguous name must fail")
	}
	if got := exitCodeFor(err, false); got != ExitConflict {
		t.Errorf("exit code = %d, want %d", got, ExitConflict)
	}
	for _, want := range []string{"ZONE", testHostID, otherHostID, "z-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%s", want, err)
		}
	}
}

func TestResolveHost_NoMatchIsNotFound(t *testing.T) {
	b := &nameBackend{t: t}
	_, err := runCLI(t, b, "host", "sync", "cf-host-3")
	if got := exitCodeFor(err, false); err == nil || got != ExitNotFound {
		t.Errorf("err = %v (exit %d), want not found (exit %d)", err, got, ExitNotFound)
	}
}

func TestResolveHost_UUIDNeverLists(t *testing.T) {
	b := &nameBackend{t: t, hosts: hostItem(otherHostID, "z", "running")}
	if _, err := runCLI(t, b, "host", "get", testHostID); err != nil {
		t.Fatalf("host get: %v", err)
	}
	if b.lookups.Load() != 0 {
		t.Errorf("a UUID argument listed hosts %d times", b.lookups.Load())
	}
}

// The --host flag takes a name, so the help's example works as written.
func TestResolveHost_DeviceCreateHostFlagTakesAName(t *testing.T) {
	b := &nameBackend{t: t, hosts: hostItem(testHostID, "z", "running")}
	if _, err := runCLI(t, b, "device", "create", "--name", "dbg-1", "--host", "cf-host-3"); err != nil {
		t.Fatalf("device create --host cf-host-3: %v", err)
	}
	if b.created["host_id"] != testHostID {
		t.Errorf("host_id = %v, want the resolved %s", b.created["host_id"], testHostID)
	}
}
