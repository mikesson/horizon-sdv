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
	"strings"
	"testing"
)

// fleetPolicyEcho is the fleet policy every fleet path serves. Writes echo the
// whole policy as the server does, because cfctl overlays each PUT's echo in
// the next step.
const fleetPolicyEcho = `{"mode":"off","tiers":{` +
	`"default":{"standby":0,"floor":0,"ladder":["n2-standard-8","n1-standard-8"]},` +
	`"gpu":{"standby":1,"floor":0}},"demand_horizon_sec":3600,` +
	`"drain_grace_sec":480,"floor":0,"shape_ladder":["n2-standard-8","n1-standard-8"],` +
	`"max_standby":16,"max_floor":16,"max_demand_horizon_sec":86400,"min_drain_grace_sec":120,` +
	`"max_drain_grace_sec":599,"max_schedules":8,"max_ladder_rungs":8}`

// configBackend serves every system-config pair with fixed values and
// records PUT bodies by key.
func configBackend(t *testing.T, putBodies map[string]string) http.Handler {
	t.Helper()
	gets := map[string]string{
		"zone-pin":                 `{"value":"europe-west4-a"}`,
		"quota-margin":             `{"value":20}`,
		"v1-zone-hint":             `{"value":true}`,
		"spot-auto-default":        `{"value":false}`,
		"default-gpu-machine-type": `{"value":"g2-standard-8"}`,
		"default-upstream-build":   `{"branch":"aosp-android-latest-release","target":"aosp_cf_x86_64_only_phone-userdebug"}`,
		"spot-min-uptime":          `{"single_seconds":600,"batch_seconds":60}`,
		"artifact-buckets":         `{"bugreport_bucket":"","recording_bucket":"rec-bkt","default_bucket":"cco-artifacts","capture_on_error":true}`,
		"sweeps":                   `{"delete_orphan_vms":false,"orphan_cvds":false,"zombie_recreate":false,"reaper_dry_run":true}`,
		"fleet":                    fleetPolicyEcho,
		// The fleet CLI key writes through five endpoints.
		"fleet/autoscaler": fleetPolicyEcho,
		"fleet/tuning":     fleetPolicyEcho,
		"fleet/schedules":  fleetPolicyEcho,
		"fleet/ladder":     fleetPolicyEcho,
		"fleet/pools/gpu":  fleetPolicyEcho,
		"docker-host":      `{"machine_type":"n1-standard-8","ttl_hours":12,"max_ttl_hours":168}`,
		"lifetimes": `{"device_ttl_hours":6,"extend_hours":2,"extend_count":3,"host_ttl_hours":10,` +
			`"max_device_ttl_hours":8,"max_extend_hours":4,"max_extend_count":10,` +
			`"max_host_ttl_hours":168,"max_devices_per_host":16,"grace_period_seconds":900}`,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		key := strings.TrimPrefix(r.URL.Path, "/v2/admin/system-config/")
		body, ok := gets[key]
		if !ok {
			t.Errorf("unexpected path %s", r.URL.Path)
			return
		}
		if r.Method == http.MethodDelete {
			putBodies[r.Method+" "+key] = ""
			fmt.Fprint(w, body)
			return
		}
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			putBodies[key] = string(b)
			if strings.HasPrefix(key, "fleet/") {
				// The fleet writes echo the whole policy.
				fmt.Fprint(w, body)
				return
			}
			// The request body already has the GET shape.
			fmt.Fprint(w, string(b))
			return
		}
		fmt.Fprint(w, body)
	})
}

func TestE2E_AdminConfigGetAllListsEveryKey(t *testing.T) {
	out, err := runCLI(t, configBackend(t, map[string]string{}), "admin", "config", "get")
	if err != nil {
		t.Fatalf("admin config get: %v", err)
	}
	for _, want := range []string{
		"zone-pin", "europe-west4-a",
		"quota-margin", "20",
		"default-upstream-build", "branch=aosp-android-latest-release",
		"spot-min-uptime", "single_seconds=600",
		"artifact-buckets", "default_bucket=cco-artifacts",
		"docker-host", "machine_type=n1-standard-8",
		"fleet", "ladder=n2-standard-8,n1-standard-8", "max_ladder_rungs=8",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("get-all output missing %q:\n%s", want, out)
		}
	}
}

func TestE2E_AdminConfigGetSingleShowsFields(t *testing.T) {
	out, err := runCLI(t, configBackend(t, map[string]string{}), "admin", "config", "get", "default-upstream-build")
	if err != nil {
		t.Fatalf("admin config get: %v", err)
	}
	if !strings.Contains(out, "aosp-android-latest-release") || !strings.Contains(out, "aosp_cf_x86_64_only_phone-userdebug") {
		t.Errorf("single get missing a field:\n%s", out)
	}
}

func TestE2E_AdminConfigSetSendsTypedScalars(t *testing.T) {
	puts := map[string]string{}
	backend := configBackend(t, puts)

	if _, err := runCLI(t, backend, "admin", "config", "set", "quota-margin", "25"); err != nil {
		t.Fatalf("set quota-margin: %v", err)
	}
	if !strings.Contains(puts["quota-margin"], `"value":25`) {
		t.Errorf("quota-margin sent as non-integer: %s", puts["quota-margin"])
	}

	if _, err := runCLI(t, backend, "admin", "config", "set", "v1-zone-hint", "false"); err != nil {
		t.Fatalf("set v1-zone-hint: %v", err)
	}
	if !strings.Contains(puts["v1-zone-hint"], `"value":false`) {
		t.Errorf("v1-zone-hint sent as non-boolean: %s", puts["v1-zone-hint"])
	}

	if _, err := runCLI(t, backend, "admin", "config", "set", "zone-pin", ""); err != nil {
		t.Fatalf("clear zone-pin: %v", err)
	}
	if !strings.Contains(puts["zone-pin"], `"value":""`) {
		t.Errorf("empty zone-pin did not clear: %s", puts["zone-pin"])
	}
}

// The PUT replaces every field, so a partial set must carry the current
// values of the unnamed fields.
func TestE2E_AdminConfigSetCompositeOverlaysUnnamedFields(t *testing.T) {
	puts := map[string]string{}
	backend := configBackend(t, puts)

	if _, err := runCLI(t, backend, "admin", "config", "set", "default-upstream-build", "branch=aosp-main"); err != nil {
		t.Fatalf("set default-upstream-build: %v", err)
	}
	got := puts["default-upstream-build"]
	if !strings.Contains(got, `"branch":"aosp-main"`) {
		t.Errorf("named field not updated: %s", got)
	}
	if !strings.Contains(got, `"target":"aosp_cf_x86_64_only_phone-userdebug"`) {
		t.Errorf("unnamed field lost its current value: %s", got)
	}

	if _, err := runCLI(t, backend, "admin", "config", "set", "artifact-buckets", "capture_on_error=false"); err != nil {
		t.Fatalf("set artifact-buckets: %v", err)
	}
	got = puts["artifact-buckets"]
	if !strings.Contains(got, `"capture_on_error":false`) {
		t.Errorf("named field not updated: %s", got)
	}
	if !strings.Contains(got, `"recording_bucket":"rec-bkt"`) {
		t.Errorf("unnamed bucket cleared by partial set: %s", got)
	}

	if _, err := runCLI(t, backend, "admin", "config", "set", "docker-host", "ttl_hours=48"); err != nil {
		t.Fatalf("set docker-host: %v", err)
	}
	got = puts["docker-host"]
	if !strings.Contains(got, `"ttl_hours":48`) {
		t.Errorf("named field not updated: %s", got)
	}
	if !strings.Contains(got, `"machine_type":"n1-standard-8"`) {
		t.Errorf("unnamed machine type cleared by partial set: %s", got)
	}
}

// The ladder has its own endpoint and must keep the typed order.
func TestE2E_AdminConfigSetFleetLadderKeepsOrderOnItsOwnPath(t *testing.T) {
	puts := map[string]string{}

	if _, err := runCLI(t, configBackend(t, puts), "admin", "config", "set", "fleet",
		"ladder=n2-standard-8,n1-standard-8,c3-standard-8"); err != nil {
		t.Fatalf("set fleet ladder: %v", err)
	}
	if got := puts["fleet/ladder"]; !strings.Contains(got, `"shape_ladder":["n2-standard-8","n1-standard-8","c3-standard-8"]`) {
		t.Errorf("ladder did not reach fleet/ladder in the typed order: %q", got)
	}
	for _, key := range []string{"fleet/autoscaler", "fleet/tuning", "fleet/schedules"} {
		if body, sent := puts[key]; sent {
			t.Errorf("a ladder edit also wrote %s: %s", key, body)
		}
	}
}

func TestE2E_AdminConfigSetFleetLadderClears(t *testing.T) {
	for _, value := range []string{"ladder=none", "ladder="} {
		puts := map[string]string{}
		if _, err := runCLI(t, configBackend(t, puts), "admin", "config", "set", "fleet", value); err != nil {
			t.Fatalf("set fleet %s: %v", value, err)
		}
		if got := puts["fleet/ladder"]; !strings.Contains(got, `"shape_ladder":[]`) {
			t.Errorf("%s did not send an empty list: %q", value, got)
		}
	}
}

// A scalar fleet edit must not write the ladder.
func TestE2E_AdminConfigSetFleetScalarLeavesTheLadderAlone(t *testing.T) {
	puts := map[string]string{}

	if _, err := runCLI(t, configBackend(t, puts), "admin", "config", "set", "fleet",
		"drain_grace_sec=300"); err != nil {
		t.Fatalf("set fleet drain_grace_sec: %v", err)
	}
	if got := puts["fleet/tuning"]; !strings.Contains(got, `"drain_grace_sec":300`) {
		t.Errorf("tuning field not updated: %q", got)
	}
	if body, sent := puts["fleet/ladder"]; sent {
		t.Errorf("a tuning edit wrote the ladder: %s", body)
	}
}

// An empty ladder entry is refused before the ladder PUT.
func TestE2E_AdminConfigSetFleetLadderRejectsAnEmptyRung(t *testing.T) {
	puts := map[string]string{}

	if _, err := runCLI(t, configBackend(t, puts), "admin", "config", "set", "fleet",
		"ladder=n2-standard-8,,n1-standard-8"); err == nil {
		t.Error("an empty rung must be rejected")
	}
	if body, sent := puts["fleet/ladder"]; sent {
		t.Errorf("rejected ladder still wrote: %s", body)
	}
}

func TestAdminConfigRejectsBadGrammar(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("rejected invocation still sent %s %s", r.Method, r.URL.Path)
	})
	cases := [][]string{
		{"admin", "config", "get", "no-such-key"},
		{"admin", "config", "set", "no-such-key", "1"},
		{"admin", "config", "set", "zone-pin", "a", "b"},
		{"admin", "config", "set", "default-upstream-build", "aosp-main"},
		{"admin", "config", "set", "default-upstream-build", "flavor=x"},
		{"admin", "config", "set", "default-upstream-build", "branch=a", "branch=b"},
		{"admin", "config", "set", "quota-margin", "lots"},
		{"admin", "config", "set", "fleet", "n2-standard-8"},
		{"admin", "config", "set", "fleet", "shape_ladder=n2-standard-8"},
		{"admin", "config", "set", "fleet", "ladder=a", "ladder=b"},
		// Two spellings of the default pool's standby name one field.
		{"admin", "config", "set", "fleet", "standby=2", "default.standby=3"},
	}
	for _, args := range cases {
		if _, err := runCLI(t, backend, args...); err == nil {
			t.Errorf("%v must be rejected", args)
		}
	}
}

func TestE2E_AdminConfigJSONPrintsWireBody(t *testing.T) {
	out, err := runCLI(t, configBackend(t, map[string]string{}), "--json", "admin", "config", "get", "spot-min-uptime")
	if err != nil {
		t.Fatalf("admin config get --json: %v", err)
	}
	if !strings.Contains(out, `"single_seconds": 600`) || !strings.Contains(out, `"batch_seconds": 60`) {
		t.Errorf("json output diverged from wire body:\n%s", out)
	}
}

// Pool fields are addressed as POOL.FIELD, and the unprefixed form is the
// default pool. With two pools in one command, each write must name its pool.
func TestE2E_AdminConfigSetFleetScopesWritesToTheirPool(t *testing.T) {
	puts := map[string]string{}

	if _, err := runCLI(t, configBackend(t, puts), "admin", "config", "set", "fleet",
		"standby=2", "gpu.standby=1", "gpu.ladder=g2-standard-8"); err != nil {
		t.Fatalf("set fleet per pool: %v", err)
	}
	if got := puts["fleet/ladder"]; !strings.Contains(got, `"pool":"gpu"`) ||
		!strings.Contains(got, `"shape_ladder":["g2-standard-8"]`) {
		t.Errorf("the ladder write did not name the gpu pool: %q", got)
	}
	// Bounds are one PUT per pool in name order, and the backend records the
	// last, so the recorded body must be the gpu pool's.
	got := puts["fleet/autoscaler"]
	if !strings.Contains(got, `"pool":"gpu"`) || !strings.Contains(got, `"standby":1`) {
		t.Errorf("the last bounds write was not the gpu pool's own: %q", got)
	}
}

// Only the autoscaler body carries the mode, and switching a pool off issues
// no autoscaler write. The mode must still be sent.
func TestE2E_AdminConfigSetFleetModeSurvivesSwitchingAPoolOff(t *testing.T) {
	puts := map[string]string{}

	if _, err := runCLI(t, configBackend(t, puts), "admin", "config", "set", "fleet",
		"mode=on", "gpu.enabled=false"); err != nil {
		t.Fatalf("set fleet mode with a pool switched off: %v", err)
	}
	if _, sent := puts["DELETE fleet/pools/gpu"]; !sent {
		t.Errorf("the pool was not deleted: %v", puts)
	}
	if got := puts["fleet/autoscaler"]; !strings.Contains(got, `"mode":"on"`) ||
		!strings.Contains(got, `"pool":"default"`) {
		t.Errorf("the mode did not reach the server: %q", got)
	}
}

// Switching a pool on stores bounds for it, so enabled=true must issue the
// autoscaler PUT.
func TestE2E_AdminConfigSetFleetPoolEnabledTrueStoresItsBounds(t *testing.T) {
	puts := map[string]string{}

	if _, err := runCLI(t, configBackend(t, puts), "admin", "config", "set", "fleet",
		"gpu.enabled=true"); err != nil {
		t.Fatalf("switch on the gpu pool: %v", err)
	}
	if got := puts["fleet/autoscaler"]; !strings.Contains(got, `"pool":"gpu"`) {
		t.Errorf("switching a pool on wrote no bounds for it: %q", got)
	}
}

// Switching a pool off deletes it. A bounds write of zeros would mean a pool
// held empty on purpose.
func TestE2E_AdminConfigSetFleetPoolEnabledFalseDeletesThePool(t *testing.T) {
	puts := map[string]string{}

	if _, err := runCLI(t, configBackend(t, puts), "admin", "config", "set", "fleet",
		"gpu.enabled=false"); err != nil {
		t.Fatalf("switch off the gpu pool: %v", err)
	}
	if _, sent := puts["DELETE fleet/pools/gpu"]; !sent {
		t.Errorf("the pool was not deleted: %v", puts)
	}
	for _, key := range []string{"fleet/autoscaler", "fleet/ladder", "fleet/schedules"} {
		if body, sent := puts[key]; sent {
			t.Errorf("switching a pool off also wrote %s: %s", key, body)
		}
	}

	// Configuring a pool while deleting it is refused.
	if _, err := runCLI(t, configBackend(t, map[string]string{}), "admin", "config", "set", "fleet",
		"gpu.enabled=false", "gpu.standby=2"); err == nil {
		t.Error("switching a pool off while configuring it must be refused")
	}
}

// versionedFleetBackend serves the fleet setting with an entity tag that
// advances on every write, refuses a write whose If-Match is not the current
// tag, and fails the write named in failOn. It records each write's path and
// If-Match in order.
func versionedFleetBackend(t *testing.T, failOn string, writes *[]string) http.Handler {
	t.Helper()
	version := 0
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		key := strings.TrimPrefix(r.URL.Path, "/v2/admin/system-config/")
		current := fmt.Sprintf(`"v%d"`, version)
		if r.Method != http.MethodGet {
			*writes = append(*writes, key+" If-Match="+r.Header.Get("If-Match"))
			if r.Header.Get("If-Match") != current {
				w.WriteHeader(http.StatusPreconditionFailed)
				fmt.Fprint(w, `{"title":"Precondition Failed","status":412}`)
				return
			}
			if key == failOn {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"title":"Bad Request","status":400,"detail":"rejected"}`)
				return
			}
			version++
		}
		w.Header().Set("ETag", fmt.Sprintf(`"v%d"`, version))
		fmt.Fprint(w, fleetPolicyEcho)
	})
}

// Every fleet PUT carries the tag the previous response returned, starting
// from the read the command overlays onto.
func TestE2E_AdminConfigSetFleetChainsEntityTags(t *testing.T) {
	var writes []string
	if _, err := runCLI(t, versionedFleetBackend(t, "", &writes), "admin", "config", "set", "fleet",
		"drain_grace_sec=300", "standby=2", "ladder=n2-standard-8"); err != nil {
		t.Fatalf("set fleet: %v", err)
	}
	want := []string{
		`fleet/tuning If-Match="v0"`,
		`fleet/autoscaler If-Match="v1"`,
		`fleet/ladder If-Match="v2"`,
	}
	if strings.Join(writes, "\n") != strings.Join(want, "\n") {
		t.Errorf("fleet writes =\n%s\nwant\n%s", strings.Join(writes, "\n"), strings.Join(want, "\n"))
	}
}

// A later fleet write that fails names the writes already applied.
func TestE2E_AdminConfigSetFleetNamesAppliedWritesOnFailure(t *testing.T) {
	var writes []string
	_, err := runCLI(t, versionedFleetBackend(t, "fleet/ladder", &writes), "admin", "config", "set", "fleet",
		"drain_grace_sec=300", "standby=2", "ladder=n2-standard-8")
	if err == nil {
		t.Fatal("a failed ladder write reported success")
	}
	for _, want := range []string{"pool default ladder", "already applied", "fleet tuning", "pool default autoscaler"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// A composite set sends the tag of its read, and a 412 tells the operator to
// run the command again.
func TestE2E_AdminConfigSetCompositeIsConditional(t *testing.T) {
	var ifMatch string
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.Method == http.MethodGet {
			w.Header().Set("ETag", `"read-tag"`)
			fmt.Fprint(w, `{"branch":"aosp-main","target":"aosp_cf_x86_64_only_phone-userdebug"}`)
			return
		}
		ifMatch = r.Header.Get("If-Match")
		w.WriteHeader(http.StatusPreconditionFailed)
		fmt.Fprint(w, `{"title":"Precondition Failed","status":412}`)
	})
	_, err := runCLI(t, backend, "admin", "config", "set", "default-upstream-build", "branch=aosp-next")
	if ifMatch != `"read-tag"` {
		t.Errorf("If-Match = %q, want the GET's ETag", ifMatch)
	}
	if err == nil || !strings.Contains(err.Error(), "changed since it was read") {
		t.Errorf("err = %v, want the changed-since-read message", err)
	}
}
