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
	"reflect"
	"strings"
	"testing"

	buildfeature "cloud-cuttlefish-orchestrator/internal/features/build"
)

func TestE2E_BuildUpdateIsTriState(t *testing.T) {
	var bodies []string
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.Method != http.MethodPatch || !strings.HasPrefix(r.URL.Path, "/v2/builds/") {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		fmt.Fprint(w, `{"id":"b1","name":"renamed","type":"url_artifacts","source":"url",
			"status":"ready","created_at":"2026-07-01T00:00:00Z","updated_at":"2026-07-31T09:00:00Z"}`)
	})

	if _, err := runCLI(t, backend, "build", "update", "b1", "--name", "renamed"); err != nil {
		t.Fatalf("build update: %v", err)
	}
	if bodies[0] != `{"name":"renamed"}` {
		t.Errorf("tri-state broken: --name alone sent %s", bodies[0])
	}

	if _, err := runCLI(t, backend, "build", "update", "b1", "--default",
		"--artifact", "host_package=gs://bkt/host.tar.gz",
		"--artifact", "default_build=https://example.com/img.zip"); err != nil {
		t.Fatalf("build update: %v", err)
	}
	var sent struct {
		IsDefault     bool `json:"is_default"`
		ArtifactSpecs []struct {
			Type string `json:"type"`
			URL  string `json:"url"`
			Role string `json:"role"`
		} `json:"artifact_specs"`
	}
	if err := json.Unmarshal([]byte(bodies[1]), &sent); err != nil {
		t.Fatalf("update body is not JSON: %v: %s", err, bodies[1])
	}
	if !sent.IsDefault {
		t.Errorf("update body missing is_default=true: %s", bodies[1])
	}
	wantSpecs := map[string][2]string{
		"host_package":  {"gcs", "gs://bkt/host.tar.gz"},
		"default_build": {"https", "https://example.com/img.zip"},
	}
	for _, spec := range sent.ArtifactSpecs {
		if want, ok := wantSpecs[spec.Role]; !ok || want[0] != spec.Type || want[1] != spec.URL {
			t.Errorf("unexpected artifact spec %+v", spec)
		}
		delete(wantSpecs, spec.Role)
	}
	for role := range wantSpecs {
		t.Errorf("update body missing artifact spec for role %s: %s", role, bodies[1])
	}
	if strings.Contains(bodies[1], `"name"`) {
		t.Errorf("tri-state broken, name sent unasked: %s", bodies[1])
	}
}

func TestBuildUpdateRejectsBadInvocations(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("rejected invocation still sent %s %s", r.Method, r.URL.Path)
	})
	cases := [][]string{
		{"build", "update", "b1"},
		{"build", "update", "b1", "--artifact", "host_package"},
		{"build", "update", "b1", "--artifact", "sideload=gs://bkt/x"},
		{"build", "update", "b1", "--artifact", "host_package=ftp://bkt/x"},
	}
	for _, args := range cases {
		if _, err := runCLI(t, backend, args...); err == nil {
			t.Errorf("%v must be rejected", args)
		}
	}
}

// The CLI copies the server's artifact-role vocabulary; this pins the two
// together.
func TestBuildArtifactRolesMatchServer(t *testing.T) {
	field, ok := reflect.TypeFor[buildfeature.ArtifactSpecInput]().FieldByName("Role")
	if !ok {
		t.Fatal("server ArtifactSpecInput lost its Role field")
	}
	server := strings.Split(field.Tag.Get("enum"), ",")
	if !reflect.DeepEqual(server, artifactRoles) {
		t.Errorf("artifact roles drifted: server %v, cli %v", server, artifactRoles)
	}
}

func TestE2E_QuotaSetSendsMaxHosts(t *testing.T) {
	var body string
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.Method != http.MethodPut || r.URL.Path != "/v2/quota/platform" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		fmt.Fprint(w, `{"max_hosts":30,"used_hosts":7,"available":23,"percentage":23}`)
	})
	out, err := runCLI(t, backend, "quota", "set", "30")
	if err != nil {
		t.Fatalf("quota set: %v", err)
	}
	if body != `{"max_hosts":30}` {
		t.Errorf("quota set body wrong: %s", body)
	}
	if !strings.Contains(out, "Platform quota set to 30 hosts") {
		t.Errorf("quota set output incomplete:\n%s", out)
	}
}
