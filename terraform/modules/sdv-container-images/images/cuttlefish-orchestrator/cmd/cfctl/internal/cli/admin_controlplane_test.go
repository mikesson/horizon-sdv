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

const controlPlaneBody = `{
	"now":"2026-07-31T12:00:00Z",
	"queue":{"pending":4,"leased":2,"failed":1,"oldest_pending_age_seconds":32},
	"type_counts":[{"type":"host.provision","state":"pending","count":1},
		{"type":"device.create_cvd","state":"leased","count":2}],
	"live_tasks":[{"id":"t1","type":"device.create_cvd","state":"leased","resource_id":"d1",
		"attempts":1,"max_attempts":5,"priority":0,"available_at":"2026-07-31T11:59:00Z",
		"lease_holder":"instance-a","updated_at":"2026-07-31T11:59:30Z"}],
	"leaders":[{"name":"hostreconciler","holder":"instance-a",
		"acquired_at":"2026-07-31T11:00:00Z","lease_expiry":"2026-07-31T12:01:00Z",
		"renewed_at":"2026-07-31T11:59:50Z"}],
	"workers_per_instance":4}`

// The capacity snapshot: one clear zone, one cooling, and a pool with no
// series. Those are the three the printer has to render differently.
const capacityBody = `{
	"now":"2026-07-31T12:00:00Z",
	"fleet":{"machines":4,"devices":11,"regions":1,"zones_enabled":2,"zones_clear":1},
	"regions":[{"region":"europe-west4","machines":4,"devices":11,"zones_enabled":2,"zones":2,
		"quota_state":"comfortable"}],
	"zones":[
		{"zone":"europe-west4-a","region":"europe-west4","enabled":true,"status":"UP",
			"machines":3,"arriving":0,"devices":8,"verdict":"clear",
			"spot":{"machine_type":"n1-standard-8","obtainability":0.9,"band":"high",
				"uptime_tier_s":3600,"synced_at":"2026-07-31T11:45:00Z"},
			"cells":[{"machine_family":"n1","machines":3,"arriving":0,"devices":8,
				"verdict":"clear","quota_pool":"CPUS","quota_state":"open"}]},
		{"zone":"europe-west4-b","region":"europe-west4","enabled":true,"status":"UP",
			"machines":1,"arriving":0,"devices":3,"verdict":"cooling",
			"verdict_detail":"capacity failed here 2 time(s) in a row on n1",
			"cooling_until":"2026-07-31T12:30:00Z",
			"cells":[{"machine_family":"n1","machines":1,"arriving":0,"devices":3,
				"verdict":"cooling","quota_pool":"CPUS","quota_state":"open",
				"cooldown":{"machine_family":"n1","binding_metric":"capacity",
					"metric_label":"capacity","regional":false,"consecutive_failures":2,
					"in_cooldown":true,"cooldown_until":"2026-07-31T12:30:00Z"}}]}],
	"pools":[
		{"region":"europe-west4","pool":"CPUS","kind":"cpu","families":["n1"],"spot":false,
			"state":"open","usage":120,"limit":500,"reserve":50},
		{"region":"europe-west4","pool":"CPUS_PER_VM_FAMILY:C4","kind":"cpu","families":["c4"],
			"spot":false,"state":"unknown"}],
	"spot":{"fleet":[{"model":"SPOT","status":"running","count":2}],
		"preempted_7d":1,"recovered_7d":1,"typical_recovery_seconds":240,
		"advice":{"enabled":true,"cells":12,"newest_synced_at":"2026-07-31T11:45:00Z"}},
	"reserve_margin_percent":10}`

// The board prints the fleet in one line and the server's verdict per zone. A
// pool with no series prints as unknown so nobody acts on a false zero.
func TestE2E_AdminCapacityBoard(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/admin/capacity" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, capacityBody)
	})
	out, err := runCLI(t, backend, "admin", "capacity")
	if err != nil {
		t.Fatalf("admin capacity: %v", err)
	}
	for _, want := range []string{
		"4 machines, 11 devices across 1 regions; 1 of 2 enabled zones clear",
		"europe-west4-a", "clear", "high",
		"cooling", "capacity failed here 2 time(s) in a row on n1",
		"ranking reserves 10% of each limit",
		"CPUS", "120", "500", "50",
		"1 preempted, 1 recovered (typical 240s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("capacity output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "CPUS_PER_VM_FAMILY:C4") || !strings.Contains(out, "unknown") {
		t.Errorf("the unpublished pool must print as unknown:\n%s", out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "CPUS_PER_VM_FAMILY:C4") && strings.Contains(line, "0") {
			t.Errorf("an unknown pool must print dashes, not zeros: %q", line)
		}
	}
}

func TestE2E_AdminControlPlanePanels(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/admin/control-plane" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, controlPlaneBody)
	})
	out, err := runCLI(t, backend, "admin", "control-plane")
	if err != nil {
		t.Fatalf("admin control-plane: %v", err)
	}
	for _, want := range []string{
		"up to 4 workers/instance", "4 pending (oldest 32s), 2 leased, 1 failed",
		"host.provision pending=1", "Live tasks (1)", "instance-a",
		"hostreconciler",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("control-plane output missing %q:\n%s", want, out)
		}
	}
}

func TestE2E_AdminControlPlaneSkipsEmptyPanelsAndKeepsJSONDeepFields(t *testing.T) {
	empty := `{"now":"2026-07-31T12:00:00Z",
		"queue":{"pending":0,"leased":0,"failed":0,"oldest_pending_age_seconds":0},
		"type_counts":[],"live_tasks":[],"leaders":[],
		"workers_per_instance":4}`
	body := empty
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		fmt.Fprint(w, body)
	})

	out, err := runCLI(t, backend, "admin", "control-plane")
	if err != nil {
		t.Fatalf("admin control-plane (empty): %v", err)
	}
	for _, absent := range []string{"Live tasks", "Leaders"} {
		if strings.Contains(out, absent) {
			t.Errorf("empty panel %q must be skipped:\n%s", absent, out)
		}
	}
	// The placement space and Spot panels belong to `admin capacity`, so this
	// snapshot must not print them.
	for _, absent := range []string{"Zones:", "Region quotas", "Spot:"} {
		if strings.Contains(out, absent) {
			t.Errorf("control-plane must not print %q any more:\n%s", absent, out)
		}
	}

	// A deep field (the live task's lease holder) proves --json carries the
	// nested panels.
	body = controlPlaneBody
	out, err = runCLI(t, backend, "--json", "admin", "control-plane")
	if err != nil {
		t.Fatalf("admin control-plane --json: %v", err)
	}
	for _, want := range []string{`"lease_holder": "instance-a"`, `"max_attempts": 5`} {
		if !strings.Contains(out, want) {
			t.Errorf("json output lost deep field %s:\n%s", want, out)
		}
	}
}
