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

// Enablement is one stored set, so enable and disable must send back the zones
// they do not touch. Dropping them would disable every other zone.
func TestE2E_AdminZoneListAndEnablement(t *testing.T) {
	var put []string
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/admin/zones":
			fmt.Fprint(w, `{"curated_shapes":["n1-standard-8","n2-standard-8"],"zones":[
				{"zone":"europe-west4-a","region":"europe-west4","status":"UP","is_enabled":true,
				 "offered_shapes":["n1-standard-8","n2-standard-8"],"shapes_reported":true},
				{"zone":"us-central1-f","region":"us-central1","status":"UP","is_enabled":false,
				 "offered_shapes":[],"shapes_reported":false}]}`)
		case r.Method == http.MethodPut && r.URL.Path == "/v2/admin/zones/enabled":
			b, _ := io.ReadAll(r.Body)
			put = append(put, string(b))
			fmt.Fprint(w, `{"zones":["europe-west4-a"]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	out, err := runCLI(t, backend, "admin", "zone", "list")
	if err != nil {
		t.Fatalf("admin zone list: %v", err)
	}
	if !strings.Contains(out, "europe-west4-a") || !strings.Contains(out, "us-central1-f") {
		t.Errorf("zone list must include disabled zones:\n%s", out)
	}
	if !strings.Contains(out, "2 of 2") || !strings.Contains(out, "not reported") {
		t.Errorf("zone list must report per-zone shape availability:\n%s", out)
	}

	if _, err := runCLI(t, backend, "admin", "zone", "enable", "us-central1-f"); err != nil {
		t.Fatalf("admin zone enable: %v", err)
	}
	if _, err := runCLI(t, backend, "admin", "zone", "disable", "europe-west4-a"); err != nil {
		t.Fatalf("admin zone disable: %v", err)
	}
	if len(put) != 2 ||
		!strings.Contains(put[0], `{"zones":["europe-west4-a","us-central1-f"]}`) ||
		!strings.Contains(put[1], `{"zones":[]}`) {
		t.Errorf("enable/disable PUTs wrong: %v", put)
	}
}

// The PATCH is tri-state. An omitted flag must be absent from the body, since a
// zero value would disable the type.
func TestE2E_AdminMachineTypeUpdateIsTriState(t *testing.T) {
	var bodies []string
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.Method != http.MethodPatch || r.URL.Path != "/v2/admin/machine-types/n1-standard-8" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		fmt.Fprint(w, `{"id":"n1-standard-8","family":"n1","vcpus":8,"memory_gb":30,
			"is_enabled":false,"is_default":false}`)
	})

	if _, err := runCLI(t, backend, "admin", "machine-type", "update", "n1-standard-8", "--no-enabled"); err != nil {
		t.Fatalf("machine-type update: %v", err)
	}
	if bodies[0] != `{"is_enabled":false}` {
		t.Errorf("tri-state broken: --no-enabled alone sent %s", bodies[0])
	}

	if _, err := runCLI(t, backend, "admin", "machine-type", "update", "n1-standard-8", "--default"); err != nil {
		t.Fatalf("machine-type update: %v", err)
	}
	if bodies[1] != `{"is_default":true}` {
		t.Errorf("tri-state broken: --default alone sent %s", bodies[1])
	}

	if _, err := runCLI(t, backend, "admin", "machine-type", "update", "n1-standard-8", "--enabled", "--default"); err != nil {
		t.Fatalf("machine-type update: %v", err)
	}
	if !strings.Contains(bodies[2], `"is_default":true`) || !strings.Contains(bodies[2], `"is_enabled":true`) {
		t.Errorf("both flags must reach the body: %s", bodies[2])
	}
}

// Display order and GPU policy belong to the machine family, so the per-shape
// flags are gone and the parser refuses them before any request is sent.
func TestAdminMachineTypeUpdateRefusesFamilyFlags(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("refused invocation still sent %s %s", r.Method, r.URL.Path)
	})
	for _, flag := range [][]string{
		{"--sort-order", "3"},
		{"--max-gpus", "2"},
		{"--default-gpu-type", "nvidia-tesla-t4"},
		{"--default-gpu-count", "1"},
	} {
		args := append([]string{"admin", "machine-type", "update", "n1-standard-8", "--enabled"}, flag...)
		if _, err := runCLI(t, backend, args...); err == nil {
			t.Errorf("%s was accepted, want the parser to refuse it", flag[0])
		}
	}
}

func TestAdminMachineTypeUpdateRejectsNoChanges(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("rejected invocation still sent %s %s", r.Method, r.URL.Path)
	})
	if _, err := runCLI(t, backend, "admin", "machine-type", "update", "n1-standard-8"); err == nil {
		t.Error("flagless update must be rejected")
	}
}

func TestE2E_AdminCatalogFreshness(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/admin/catalog/freshness" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"built_at":"2026-07-31T11:00:00Z",
			"partial":["machine types: us-central1-f","quota: us-central1"]}`)
	})
	out, err := runCLI(t, backend, "admin", "catalog-freshness")
	if err != nil {
		t.Fatalf("admin catalog-freshness: %v", err)
	}
	for _, want := range []string{"2026-07-31T11:00:00Z", "machine types: us-central1-f", "quota: us-central1"} {
		if !strings.Contains(out, want) {
			t.Errorf("freshness output missing %q:\n%s", want, out)
		}
	}
}
