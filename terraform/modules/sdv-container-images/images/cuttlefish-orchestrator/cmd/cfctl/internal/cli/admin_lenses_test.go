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
	"testing"
)

func TestE2E_AdminStats(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/admin/stats" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"hosts":{"total":5,"active":3,"max_quota":20},
			"devices":{"total":12,"active":9},
			"devices_by_status":{"running":9,"stopped":3},
			"hosts_by_status":{"running":3,"stopped":2},
			"timestamp":"2026-07-31T12:00:00Z"}`)
	})
	out, err := runCLI(t, backend, "admin", "stats")
	if err != nil {
		t.Fatalf("admin stats: %v", err)
	}
	for _, want := range []string{
		"3 active / 5 total (quota 20)", "9 active / 12 total",
		"running=9 stopped=3", "running=3 stopped=2", "2026-07-31T12:00:00Z",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stats output missing %q:\n%s", want, out)
		}
	}
}

func TestE2E_AdminProvisioning(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/admin/provisioning-stats" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"window_days":7,"devices":{"created":42,"errored":3},"hosts":{"created":12,"errored":1}}`)
	})
	out, err := runCLI(t, backend, "admin", "provisioning")
	if err != nil {
		t.Fatalf("admin provisioning: %v", err)
	}
	for _, want := range []string{"Last 7 days", "42 created, 3 now in error", "12 created, 1 now in error"} {
		if !strings.Contains(out, want) {
			t.Errorf("provisioning output missing %q:\n%s", want, out)
		}
	}
}

func TestE2E_AdminReclaimableSectionsAndEmpty(t *testing.T) {
	full := `{"pinned_devices":[{"id":"d1","name":"pin1","host_id":"h1","status":"running",
			"pinned_by":"x@y","pinned_reason":"overnight_test","updated_at":"2026-07-31T09:00:00Z"}],
		"stopped_devices":[],
		"stopped_hosts":[{"id":"h2","name":"idle","zone":"europe-west4-a","status":"stopped",
			"current_device_count":0,"updated_at":"2026-07-31T09:00:00Z"}],
		"expiring_hosts":[]}`
	body := full
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		fmt.Fprint(w, body)
	})

	out, err := runCLI(t, backend, "admin", "reclaimable")
	if err != nil {
		t.Fatalf("admin reclaimable: %v", err)
	}
	for _, want := range []string{"Pinned devices (1)", "overnight_test", "Stopped hosts (still billed) (1)", "europe-west4-a"} {
		if !strings.Contains(out, want) {
			t.Errorf("reclaimable output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Stopped devices") || strings.Contains(out, "expiring") {
		t.Errorf("empty buckets must be skipped:\n%s", out)
	}

	body = `{"pinned_devices":[],"stopped_devices":[],"stopped_hosts":[],"expiring_hosts":[]}`
	out, err = runCLI(t, backend, "admin", "reclaimable")
	if err != nil {
		t.Fatalf("admin reclaimable (empty): %v", err)
	}
	if !strings.Contains(out, "Nothing to reclaim") {
		t.Errorf("empty worklist not stated:\n%s", out)
	}
}

func TestE2E_AdminTasksStatsHintsOnFailures(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/admin/tasks/stats" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"pending":4,"leased":2,"failed":1,"oldest_pending_age_seconds":32}`)
	})
	var out string
	stderr := captureStderr(t, func() {
		var err error
		out, err = runCLI(t, backend, "admin", "tasks", "stats")
		if err != nil {
			t.Errorf("admin tasks stats: %v", err)
		}
	})
	for _, want := range []string{"Pending:  4 (oldest 32s)", "Leased:   2", "Failed:   1"} {
		if !strings.Contains(out, want) {
			t.Errorf("tasks stats output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(stderr, "admin tasks list") {
		t.Errorf("failed>0 must hint at the dead-letter list, got stderr:\n%s", stderr)
	}
}
