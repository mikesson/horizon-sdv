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
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestE2E_DeviceExtendSendsSecondsAndPrintsExpiry(t *testing.T) {
	var gotBody string
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/devices/"+testDeviceID+"/extend" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		fmt.Fprint(w, `{"id":"d1","name":"n","status":"running",
			"lifecycle":{"mode":"ttl","ttl_seconds":7200,"expires_at":"2026-08-01T12:00:00Z","extend_count":2}}`)
	})
	out, err := runCLI(t, backend, "device", "extend", testDeviceID, "--by", "1h")
	if err != nil {
		t.Fatalf("device extend: %v", err)
	}
	if !strings.Contains(gotBody, `"seconds":3600`) {
		t.Errorf("extend body missing seconds: %s", gotBody)
	}
	if !strings.Contains(out, "2026-08-01T12:00:00Z") || !strings.Contains(out, "extension #2") {
		t.Errorf("extend output incomplete:\n%s", out)
	}
}

func TestE2E_DevicePinUnpinAndListColumn(t *testing.T) {
	pinnedDevice := `{"id":"d1","name":"n","host_id":"h1","status":"running",
		"lifecycle":{"mode":"ttl","pinned_at":"2026-07-31T09:00:00Z","pinned_by":"x@y",
		"pinned_reason":"overnight_test","pinned_until":"2026-08-01T09:00:00Z"}}`
	var gotBody string
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		switch r.URL.Path {
		case "/v2/devices/" + testDeviceID + "/pin":
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			fmt.Fprint(w, pinnedDevice)
		case "/v2/devices/" + testDeviceID + "/unpin":
			fmt.Fprint(w, `{"id":"d1","name":"n","status":"running","lifecycle":{"mode":"ttl"}}`)
		case "/v2/devices":
			fmt.Fprintf(w, `{"items":[%s]}`, pinnedDevice)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})

	out, err := runCLI(t, backend, "device", "pin", testDeviceID, "--reason", "overnight_test", "--for", "24h")
	if err != nil {
		t.Fatalf("device pin: %v", err)
	}
	if !strings.Contains(gotBody, `"reason":"overnight_test"`) || !strings.Contains(gotBody, `"pinned_until"`) {
		t.Errorf("pin body incomplete: %s", gotBody)
	}
	if !strings.Contains(out, "2026-08-01T09:00:00Z") {
		t.Errorf("pin output missing until:\n%s", out)
	}

	if _, err := runCLI(t, backend, "device", "unpin", testDeviceID); err != nil {
		t.Fatalf("device unpin: %v", err)
	}

	out, err = runCLI(t, backend, "device", "list")
	if err != nil {
		t.Fatalf("device list: %v", err)
	}
	if !strings.Contains(out, "PINNED") || !strings.Contains(out, "overnight_test until Aug 1 09:00") {
		t.Errorf("list missing pin column:\n%s", out)
	}
}

func TestE2E_DeviceDisplayLifecycle(t *testing.T) {
	var addBody string
	var removedPath string
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/devices/"+testDeviceID+"/instances/ivi/displays":
			fmt.Fprint(w, `{"displays":[{"display_number":0,"width":720,"height":1280,"dpi":320,"refresh_rate_hz":60}],
				"presets":[{"name":"tablet","width":2560,"height":1800,"dpi":320,"refresh_rate_hz":60}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/devices/"+testDeviceID+"/instances/ivi/displays":
			b, _ := io.ReadAll(r.Body)
			addBody = string(b)
			fmt.Fprint(w, `{"display_number":1}`)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v2/devices/"+testDeviceID+"/instances/ivi/displays/"):
			removedPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	out, err := runCLI(t, backend, "device", "display", "list", testDeviceID, "--instance", "ivi")
	if err != nil {
		t.Fatalf("display list: %v", err)
	}
	if !strings.Contains(out, "720") || !strings.Contains(out, "tablet") {
		t.Errorf("display list incomplete:\n%s", out)
	}

	if _, err := runCLI(t, backend, "device", "display", "add", testDeviceID, "--preset", "tablet", "--instance", "ivi"); err != nil {
		t.Fatalf("display add: %v", err)
	}
	if !strings.Contains(addBody, `"preset":"tablet"`) {
		t.Errorf("add body missing preset: %s", addBody)
	}

	if _, err := runCLI(t, backend, "device", "display", "remove", testDeviceID, "1", "--instance", "ivi"); err != nil {
		t.Fatalf("display remove: %v", err)
	}
	if removedPath != "/v2/devices/"+testDeviceID+"/instances/ivi/displays/1" {
		t.Errorf("remove hit %s", removedPath)
	}
}

func TestDeviceDisplayAddRejectsIncompleteOrAmbiguousFlags(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("rejected invocation still sent %s %s", r.Method, r.URL.Path)
	})
	// A preset already defines the dimensions.
	if _, err := runCLI(t, backend, "device", "display", "add", testDeviceID, "--preset", "tablet", "--width", "100", "--height", "100"); err == nil {
		t.Error("preset+dimensions must be rejected")
	}
	// Dimensions alone must be complete.
	if _, err := runCLI(t, backend, "device", "display", "add", testDeviceID, "--width", "100"); err == nil {
		t.Error("width without height must be rejected")
	}
}

func TestE2E_DeviceLifecycle(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/devices/"+testDeviceID+"/lifecycle" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"mode":"ttl","ttl_seconds":7200,"expires_at":"2026-08-01T12:00:00Z","extend_count":1,
			"pinned_at":"2026-07-31T09:00:00Z","pinned_by":"x@y","pinned_reason":"other","pinned_reason_detail":"soak"}`)
	})
	out, err := runCLI(t, backend, "device", "lifecycle", testDeviceID)
	if err != nil {
		t.Fatalf("device lifecycle: %v", err)
	}
	for _, want := range []string{
		"Mode:         ttl", "TTL:          2h0m0s", "Expires:      2026-08-01T12:00:00Z",
		"Extensions:   1", "other by x@y", "Pin Detail:   soak", "Pin Expires:  never",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lifecycle output missing %q:\n%s", want, out)
		}
	}
}

// A negative or fractional duration must be refused before any request:
// --by -1h would extend by the server default and --for -1h would pin forever.
func TestDeviceExtendAndPinRejectBadDurations(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("rejected invocation still sent %s %s", r.Method, r.URL.Path)
	})
	for _, args := range [][]string{
		{"device", "extend", testDeviceID, "--by", "-1h"},
		{"device", "extend", testDeviceID, "--by", "1500ms"},
		{"device", "pin", testDeviceID, "--reason", "other", "--for", "-1h"},
		{"device", "pin", testDeviceID, "--reason", "other", "--for", "10.5s"},
	} {
		if _, err := runCLI(t, backend, args...); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
	if err := requireWholeSeconds("--by", 0); err != nil {
		t.Errorf("zero must keep meaning the server default: %v", err)
	}
}

// Snapshot delete and restore are destructive, so without --force they prompt
// and a decline sends nothing.
func TestDeviceSnapshotDeleteAndRestoreConfirm(t *testing.T) {
	sent := 0
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent++
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"name":"operations/op-1","done":false,"metadata":{"verb":"device.snapshot"}}`)
	})
	for _, verb := range []string{"delete", "restore"} {
		answerPrompt(t, "n")
		if _, err := runCLI(t, backend, "device", "snapshot", verb, testDeviceID, "s1"); err != nil {
			t.Errorf("snapshot %s declined: %v (a decline is not an error)", verb, err)
		}
	}
	if sent != 0 {
		t.Fatalf("a declined snapshot verb sent %d requests", sent)
	}
	for _, verb := range []string{"delete", "restore"} {
		if _, err := runCLI(t, backend, "device", "snapshot", verb, testDeviceID, "s1", "--force"); err != nil {
			t.Errorf("snapshot %s --force: %v", verb, err)
		}
	}
	if sent != 2 {
		t.Errorf("--force sent %d requests, want 2", sent)
	}
}

// answerPrompt feeds one line to the next confirmation prompt.
func answerPrompt(t *testing.T, answer string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })
	fmt.Fprintln(w, answer)
	_ = w.Close()
}
