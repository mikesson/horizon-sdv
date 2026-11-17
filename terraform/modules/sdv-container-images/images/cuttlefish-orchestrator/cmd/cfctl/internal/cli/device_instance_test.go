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
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// instanceBackend serves a device with the given instance names and answers
// any screenshot request, recording the path it was asked for.
func instanceBackend(t *testing.T, names []string, gotPath *atomic.Value) http.Handler {
	t.Helper()
	instances := make([]string, len(names))
	for i, n := range names {
		instances[i] = fmt.Sprintf(`{"name":%q}`, n)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/devices/"+testDeviceID:
			fmt.Fprintf(w, `{"id":%q,"name":"dev","status":"running","instances":[%s]}`,
				testDeviceID, strings.Join(instances, ","))
		case strings.HasSuffix(r.URL.Path, "/screenshot"):
			gotPath.Store(r.URL.Path)
			fmt.Fprint(w, `{"screenshot_bytes":"aGk=","screenshot_mime_type":"image/png"}`)
		default:
			http.NotFound(w, r)
		}
	})
}

// A single-instance device needs no --instance.
func TestInstanceDefaultsToTheDevicesOnlyInstance(t *testing.T) {
	var gotPath atomic.Value
	backend := instanceBackend(t, []string{"dev"}, &gotPath)

	out := t.TempDir() + "/shot.png"
	if _, err := runCLI(t, backend, "device", "screenshot", testDeviceID, "--output", out); err != nil {
		t.Fatalf("screenshot without --instance: %v", err)
	}
	if got := gotPath.Load(); got != "/v2/devices/"+testDeviceID+"/instances/dev/displays/0/screenshot" {
		t.Errorf("captured %v, want the resolved instance route", got)
	}
}

// A multi-instance device without --instance is refused, and the refusal lists
// the instance names.
func TestInstanceRefusesToGuessOnAMultiInstanceDevice(t *testing.T) {
	var gotPath atomic.Value
	backend := instanceBackend(t, []string{"ivi", "cluster", "watch"}, &gotPath)

	out := t.TempDir() + "/shot.png"
	_, err := runCLI(t, backend, "device", "screenshot", testDeviceID, "--output", out)
	if err == nil {
		t.Fatal("screenshot without --instance on a 3-instance device succeeded; want a refusal")
	}
	for _, name := range []string{"ivi", "cluster", "watch"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("refusal does not name instance %q: %v", name, err)
		}
	}
	if !strings.Contains(err.Error(), "--instance") {
		t.Errorf("refusal does not say how to choose: %v", err)
	}
	if got := gotPath.Load(); got != nil {
		t.Errorf("a capture was attempted anyway, against %v", got)
	}
}

// An explicit --instance skips the device read.
func TestInstanceFlagSkipsTheDeviceLookup(t *testing.T) {
	var gotPath atomic.Value
	var deviceReads atomic.Int64
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/devices/"+testDeviceID:
			deviceReads.Add(1)
			fmt.Fprint(w, `{"id":"d1","name":"dev","status":"running","instances":[{"name":"dev"}]}`)
		case strings.HasSuffix(r.URL.Path, "/screenshot"):
			gotPath.Store(r.URL.Path)
			fmt.Fprint(w, `{"screenshot_bytes":"aGk=","screenshot_mime_type":"image/png"}`)
		default:
			http.NotFound(w, r)
		}
	})

	out := t.TempDir() + "/shot.png"
	if _, err := runCLI(t, backend, "device", "screenshot", testDeviceID, "--instance", "watch", "--output", out); err != nil {
		t.Fatalf("screenshot with --instance: %v", err)
	}
	if got := gotPath.Load(); got != "/v2/devices/"+testDeviceID+"/instances/watch/displays/0/screenshot" {
		t.Errorf("captured %v, want the named instance route", got)
	}
	if n := deviceReads.Load(); n != 0 {
		t.Errorf("the device was read %d times despite --instance naming the target", n)
	}
}
