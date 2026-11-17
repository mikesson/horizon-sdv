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
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestE2E_HostRetryForwardsFallback(t *testing.T) {
	var gotBody string
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/hosts/"+testHostID+"/retry" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		fmt.Fprintf(w, `{"id":%q,"name":"n","zone":"z","status":"creating"}`, testHostID)
	})
	out, err := runCLI(t, backend, "host", "retry", testHostID, "--fallback-to-standard")
	if err != nil {
		t.Fatalf("host retry: %v", err)
	}
	if !strings.Contains(gotBody, `"fallback_to_standard":true`) {
		t.Errorf("retry body missing fallback: %s", gotBody)
	}
	if !strings.Contains(out, testHostID) {
		t.Errorf("retry output missing host:\n%s", out)
	}
}

func TestE2E_HostSync(t *testing.T) {
	var synced bool
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/hosts/"+testHostID+"/sync" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		synced = true
		w.WriteHeader(http.StatusNoContent)
	})
	if _, err := runCLI(t, backend, "host", "sync", testHostID); err != nil {
		t.Fatalf("host sync: %v", err)
	}
	if !synced {
		t.Error("sync endpoint never called")
	}
}

func TestE2E_HostCVDsTable(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/hosts/"+testHostID+"/cvds" || r.Method != http.MethodGet {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"cvds":[
			{"group":"cvd_1","name":"1","status":"Running","adb_serial":"0.0.0.0:6520"},
			{"group":"cvdabcdef12","name":"1","status":"Starting","device_id":"abcdef12-dev"}
		]}`)
	})
	out, err := runCLI(t, backend, "host", "cvds", testHostID)
	if err != nil {
		t.Fatalf("host cvds: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("want a header and two rows, got:\n%s", out)
	}
	if got := strings.Fields(lines[0]); strings.Join(got, " ") != "GROUP NAME STATUS ADB DEVICE" {
		t.Errorf("header = %q", lines[0])
	}
	if got := strings.Join(strings.Fields(lines[1]), " "); got != "cvd_1 1 Running 0.0.0.0:6520 -" {
		t.Errorf("cvdr row = %q, want the device column to read '-'", got)
	}
	if got := strings.Join(strings.Fields(lines[2]), " "); got != "cvdabcdef12 1 Starting - abcdef12-dev" {
		t.Errorf("managed row = %q, want the owning device and '-' for ADB", got)
	}
}

func TestE2E_HostCVDsJSONPrintsList(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"cvds":[{"group":"cvd_1","name":"1","status":"Running"}]}`)
	})
	out, err := runCLI(t, backend, "host", "cvds", testHostID, "--json")
	if err != nil {
		t.Fatalf("host cvds --json: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not a JSON list: %v\n%s", err, out)
	}
	if len(got) != 1 || got[0]["group"] != "cvd_1" {
		t.Errorf("got %v, want the one CVD", got)
	}
}

func TestE2E_HostCVDsEmptyJSONIsArray(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"cvds":null}`)
	})
	out, err := runCLI(t, backend, "host", "cvds", testHostID, "--json")
	if err != nil {
		t.Fatalf("host cvds --json: %v", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("stdout = %q, want [] (never null)", out)
	}
}

func TestE2E_HostCVDsSurfacesConflict(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"status":409,"title":"Conflict","detail":"host h1 is stopped; its CVDs can only be listed while it is running"}`)
	})
	_, err := runCLI(t, backend, "host", "cvds", testHostID)
	if err == nil || !strings.Contains(err.Error(), "only be listed while it is running") {
		t.Errorf("err = %v, want the server's conflict detail", err)
	}
}

func TestE2E_HostExtendSendsHours(t *testing.T) {
	var gotBody map[string]any
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/hosts/"+testHostID+"/extend" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decoding body: %v", err)
		}
		fmt.Fprintf(w, `{"id":%q,"name":"n","zone":"z","status":"running",
			"lifecycle":{"mode":"ttl","expires_at":"2026-11-01T10:00:00Z","protected_device_count":0,"ttl_seconds":0}}`, testHostID)
	})
	out, err := runCLI(t, backend, "host", "extend", testHostID, "--hours", "24", "--json")
	if err != nil {
		t.Fatalf("host extend: %v", err)
	}
	if gotBody["hours"] != float64(24) {
		t.Errorf("body = %v, want hours 24", gotBody)
	}
	var host struct {
		Lifecycle struct {
			ExpiresAt string `json:"expires_at"`
		} `json:"lifecycle"`
	}
	if err := json.Unmarshal([]byte(out), &host); err != nil {
		t.Fatalf("stdout is not the host: %v\n%s", err, out)
	}
	if host.Lifecycle.ExpiresAt != "2026-11-01T10:00:00Z" {
		t.Errorf("expires_at = %q, want the server's new expiry", host.Lifecycle.ExpiresAt)
	}
}

// The server's refusals keep their meaning as exit codes: a missing host is 4
// and a host that refuses the extend is 5.
func TestE2E_HostExtendExitCodes(t *testing.T) {
	for _, tc := range []struct {
		status int
		detail string
		want   int
	}{
		{http.StatusNotFound, "host not found", ExitNotFound},
		{http.StatusConflict, "host is a fleet host: the orchestrator manages its lease", ExitConflict},
		{http.StatusUnprocessableEntity, "expected number <= 168", ExitGeneral},
	} {
		backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(tc.status)
			fmt.Fprintf(w, `{"status":%d,"title":"refused","detail":%q}`, tc.status, tc.detail)
		})
		_, err := runCLI(t, backend, "host", "extend", testHostID, "--hours", "24")
		if err == nil || !strings.Contains(err.Error(), tc.detail) {
			t.Errorf("status %d: err = %v, want the server's detail", tc.status, err)
			continue
		}
		if got := exitCodeFor(err, false); got != tc.want {
			t.Errorf("status %d: exit code %d, want %d", tc.status, got, tc.want)
		}
	}
}

func TestE2E_HostExtendRejectsZeroHoursLocally(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("--hours 0 must fail before any request, got %s %s", r.Method, r.URL.Path)
	})
	_, err := runCLI(t, backend, "host", "extend", "web", "--hours", "0")
	if err == nil || !strings.Contains(err.Error(), "--hours must be at least 1") {
		t.Errorf("err = %v, want the local --hours check", err)
	}
}
