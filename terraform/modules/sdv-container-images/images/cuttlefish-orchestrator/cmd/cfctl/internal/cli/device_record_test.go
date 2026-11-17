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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordingFetchBackend serves the archive call and lets the storage download
// be scripted per test.
func recordingFetchBackend(storage http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/recordings/clip.webm/fetch"):
			fmt.Fprint(w, `{"bucket":"bkt","object":"recordings/d1/clip.webm","gs_uri":"gs://bkt/recordings/d1/clip.webm"}`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/storage/"):
			storage(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

// A failed download must leave the previous copy untouched and no partial
// file behind.
func TestRecordGetFailedDownloadPreservesPreviousFile(t *testing.T) {
	backend := recordingFetchBackend(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"title":"boom"}`)
	})

	out := filepath.Join(t.TempDir(), "clip.webm")
	const previous = "PREVIOUS GOOD ARTIFACT"
	if err := os.WriteFile(out, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, backend, "device", "record", "fetch", testDeviceID, "clip.webm", "--instance", "ivi", "-o", out); err == nil {
		t.Fatal("record fetch succeeded against a failing storage backend, want error")
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("previous artifact is gone: %v", err)
	}
	if string(got) != previous {
		t.Errorf("previous artifact clobbered: %q", got)
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(out), "*partial*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

// A body shorter than its Content-Length must fail and leave the previous copy
// untouched.
func TestRecordGetTruncatedBodyPreservesPreviousFile(t *testing.T) {
	backend := recordingFetchBackend(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		fmt.Fprint(w, "torso")
	})

	out := filepath.Join(t.TempDir(), "clip.webm")
	const previous = "PREVIOUS GOOD ARTIFACT"
	if err := os.WriteFile(out, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, backend, "device", "record", "fetch", testDeviceID, "clip.webm", "--instance", "ivi", "-o", out); err == nil {
		t.Fatal("record fetch succeeded on a truncated body, want error")
	}
	if got, _ := os.ReadFile(out); string(got) != previous {
		t.Errorf("previous artifact clobbered: %q", got)
	}
}

// The success path lands the bytes under the requested name.
func TestRecordGetDownloadsToTarget(t *testing.T) {
	backend := recordingFetchBackend(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "fresh recording bytes")
	})

	out := filepath.Join(t.TempDir(), "clip.webm")
	if _, err := runCLI(t, backend, "device", "record", "fetch", testDeviceID, "clip.webm", "--instance", "ivi", "-o", out); err != nil {
		t.Fatalf("record get: %v", err)
	}
	if got, err := os.ReadFile(out); err != nil || string(got) != "fresh recording bytes" {
		t.Errorf("downloaded file = %q, %v", got, err)
	}
}
