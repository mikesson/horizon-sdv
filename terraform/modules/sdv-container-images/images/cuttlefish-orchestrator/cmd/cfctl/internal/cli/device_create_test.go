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
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	devicefeature "cloud-cuttlefish-orchestrator/internal/features/device"
	"cloud-cuttlefish-orchestrator/internal/features/device/validation"
)

// registeredUUID is the registry ID the fake backend hands out.
const registeredUUID = "0b7e3c1a-5f4d-4e2b-9a61-2c8d7f3e1b90"

// createBackend fakes build registration and device create, recording both
// request bodies.
type createBackend struct {
	mu           sync.Mutex
	paths        []string
	registered   map[string]any
	created      map[string]any
	targetOnFile string // target of the record the registry returns
}

func (b *createBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.paths = append(b.paths, r.Method+" "+r.URL.Path)
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v2/builds":
		_ = json.Unmarshal(body, &b.registered)
		target, _ := b.registered["target"].(string)
		if b.targetOnFile != "" {
			target = b.targetOnFile
		}
		fmt.Fprintf(w, `{"id":%q,"name":%q,"source":"android_build","build_id":%q,"target":%q}`,
			registeredUUID, b.registered["name"], b.registered["build_id"], target)
	case r.Method == http.MethodPost && r.URL.Path == "/v2/devices":
		_ = json.Unmarshal(body, &b.created)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":"d1","name":"n","host_id":"h1","status":"pending"}`)
	default:
		http.NotFound(w, r)
	}
}

// serverDeviceNamePattern reads the server's device-name rule off the create
// input's pattern tag.
func serverDeviceNamePattern(t *testing.T) *regexp.Regexp {
	t.Helper()
	body, ok := reflect.TypeFor[devicefeature.CreateDeviceInput]().FieldByName("Body")
	if !ok {
		t.Fatal("CreateDeviceInput has no Body")
	}
	name, ok := body.Type.FieldByName("Name")
	if !ok || name.Tag.Get("pattern") == "" {
		t.Fatal("CreateDeviceInput.Body.Name has no pattern tag")
	}
	return regexp.MustCompile(name.Tag.Get("pattern"))
}

func TestGenerateDeviceName(t *testing.T) {
	deviceNamePattern := serverDeviceNamePattern(t)
	seen := map[string]bool{}
	for range 50 {
		name := generateDeviceName("")
		if !regexp.MustCompile(`^cvd-[a-z0-9]{8}$`).MatchString(name) || !deviceNamePattern.MatchString(name) {
			t.Fatalf("generated name %q breaks the cvd-XXXXXXXX shape or the server pattern %s", name, deviceNamePattern)
		}
		seen[name] = true
	}
	if len(seen) < 50 {
		t.Errorf("50 generated names had only %d distinct values", len(seen))
	}

	a, b := generateDeviceName("key-1"), generateDeviceName("key-1")
	if a != b || !deviceNamePattern.MatchString(a) {
		t.Errorf("key-derived names %q and %q: want equal and valid", a, b)
	}
	if generateDeviceName("key-2") == a {
		t.Error("different idempotency keys gave the same name")
	}
}

// A retried create with the same key must send the same body, or the server
// refuses the key as reused with a different request.
func TestE2E_DeviceCreateIdempotencyKeyFixesTheGeneratedName(t *testing.T) {
	names := make([]any, 0, 2)
	for range 2 {
		backend := &createBackend{}
		if _, err := runCLI(t, backend, "device", "create", "--idempotency-key", "ci-run-7"); err != nil {
			t.Fatalf("device create: %v", err)
		}
		names = append(names, backend.created["name"])
	}
	if names[0] != names[1] {
		t.Errorf("names %v differ across retries with one idempotency key", names)
	}
}

func TestE2E_DeviceCreateRegistersAnAndroidBuildID(t *testing.T) {
	const target = "aosp_cf_x86_64_phone-trunk_staging-userdebug"
	cases := []struct {
		name string
		args []string
		// buildOf reads the registry ID back out of the create body.
		buildOf func(body map[string]any) any
	}{
		{
			name: "imperative",
			args: []string{"--host", testHostID},
			buildOf: func(body map[string]any) any {
				return body["build_id"]
			},
		},
		{
			name: "declarative",
			args: nil,
			buildOf: func(body map[string]any) any {
				reqs, _ := body["requirements"].(map[string]any)
				build, _ := reqs["build"].(map[string]any)
				return build["registered"]
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := &createBackend{}
			args := append([]string{"device", "create", "--build-id", "12345678", "--build-target", target}, tc.args...)
			var runErr error
			stderr := captureStderr(t, func() { _, runErr = runCLI(t, backend, args...) })
			if runErr != nil {
				t.Fatalf("device create: %v", runErr)
			}
			if want := []string{"POST /v2/builds", "POST /v2/devices"}; strings.Join(backend.paths, ",") != strings.Join(want, ",") {
				t.Errorf("requests = %v, want %v", backend.paths, want)
			}
			reg := backend.registered
			if reg["source"] != "android_build" || reg["build_id"] != "12345678" || reg["target"] != target {
				t.Errorf("registration = %v, want android_build 12345678 for %s", reg, target)
			}
			if reg["name"] != target+"-12345678" {
				t.Errorf("registration name = %v, want TARGET-BUILD_ID", reg["name"])
			}
			if got := tc.buildOf(backend.created); got != registeredUUID {
				t.Errorf("create carries build %v, want the registered ID %s", got, registeredUUID)
			}
			if !strings.Contains(stderr, registeredUUID) {
				t.Errorf("stderr does not name the registered build ID:\n%s", stderr)
			}
		})
	}
}

func TestE2E_DeviceCreateWithARegisteredIDSkipsRegistration(t *testing.T) {
	backend := &createBackend{}
	if _, err := runCLI(t, backend, "device", "create", "--host", testHostID, "--build-id", registeredUUID); err != nil {
		t.Fatalf("device create: %v", err)
	}
	if len(backend.paths) != 1 || backend.paths[0] != "POST /v2/devices" {
		t.Errorf("requests = %v, want only the create", backend.paths)
	}
	if backend.created["build_id"] != registeredUUID {
		t.Errorf("build_id = %v", backend.created["build_id"])
	}
}

func TestDeviceCreateAndroidBuildIDRefusals(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--build-id", "12345678"}, "--build-target"},
		{[]string{"--build-id", "12345678", "--build-target", "t", "--dry-run"}, "cfctl build register"},
	}
	for _, tc := range cases {
		backend := &createBackend{}
		_, err := runCLI(t, backend, append([]string{"device", "create"}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want it to mention %q", tc.args, err, tc.want)
		}
		if len(backend.paths) != 0 {
			t.Errorf("%v: refused create made requests %v", tc.args, backend.paths)
		}
	}
}

// The registry returns the record it already holds for a build ID, whatever
// its target; a different target must be reported.
func TestE2E_BuildRegisterWarnsOnATargetMismatch(t *testing.T) {
	backend := &createBackend{targetOnFile: "other_target-userdebug"}
	var runErr error
	stderr := captureStderr(t, func() {
		_, runErr = runCLI(t, backend, "build", "register", "--build-id", "12345678", "--target", "mine-userdebug")
	})
	if runErr != nil {
		t.Fatalf("build register: %v", runErr)
	}
	if !strings.Contains(stderr, "Warning") || !strings.Contains(stderr, "other_target-userdebug") {
		t.Errorf("stderr lacks the target mismatch warning:\n%s", stderr)
	}
}

func TestE2E_BuildRegister(t *testing.T) {
	backend := &createBackend{}
	out, err := runCLI(t, backend, "build", "register", "--build-id", "12345678",
		"--target", "aosp_cf_x86_64_phone-userdebug", "--branch", "aosp-main",
		"--name", "nightly", "--description", "from CI", "--default", "--json")
	if err != nil {
		t.Fatalf("build register: %v", err)
	}
	want := map[string]any{
		"source": "android_build", "build_id": "12345678", "target": "aosp_cf_x86_64_phone-userdebug",
		"branch": "aosp-main", "name": "nightly", "description": "from CI", "is_default": true,
	}
	for k, v := range want {
		if backend.registered[k] != v {
			t.Errorf("registration %s = %v, want %v", k, backend.registered[k], v)
		}
	}
	var b struct{ ID string }
	if err := json.Unmarshal([]byte(out), &b); err != nil || b.ID != registeredUUID {
		t.Errorf("stdout = %s, want the registered build as JSON", out)
	}

	if _, err := runCLI(t, &createBackend{}, "build", "register", "--build-id", "1"); err == nil ||
		!strings.Contains(err.Error(), "--target") {
		t.Errorf("err = %v, want --target required", err)
	}
}

func TestE2E_DeviceCreateInstances(t *testing.T) {
	backend := &createBackend{}
	if _, err := runCLI(t, backend, "device", "create", "--instances", "3", "--build-id", registeredUUID); err != nil {
		t.Fatalf("device create --instances 3: %v", err)
	}
	if backend.created["mode"] != "advanced" {
		t.Errorf("mode = %v, want advanced", backend.created["mode"])
	}
	adv, _ := backend.created["advanced_config"].(map[string]any)
	env, _ := adv["env_config"].(map[string]any)
	if err := validation.ValidateEnvConfig(env); err != nil {
		t.Fatalf("the server would refuse the generated config: %v\n%v", err, env)
	}
	if got := validation.CountInstances(env); got != 3 {
		t.Errorf("config has %d instances, want 3", got)
	}
	// The build travels beside the config, so the server merges it into
	// every instance.
	reqs, _ := backend.created["requirements"].(map[string]any)
	build, _ := reqs["build"].(map[string]any)
	if build["registered"] != registeredUUID {
		t.Errorf("requirements.build = %v, want the registered build", reqs["build"])
	}

	one := &createBackend{}
	if _, err := runCLI(t, one, "device", "create", "--instances", "1"); err != nil {
		t.Fatalf("device create --instances 1: %v", err)
	}
	if _, has := one.created["mode"]; has {
		t.Errorf("--instances 1 sent mode %v, want the ordinary request", one.created["mode"])
	}
	if _, has := one.created["advanced_config"]; has {
		t.Error("--instances 1 sent an advanced_config")
	}
}

func TestDeviceCreateInstancesRefusals(t *testing.T) {
	envConfig := filepath.Join(t.TempDir(), "env.json")
	if err := os.WriteFile(envConfig, []byte(`{"instances":[{}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--instances", "0"}, "between 1 and 8"},
		{[]string{"--instances", "9"}, "between 1 and 8"},
		{[]string{"--instances", "2", "--env-config", envConfig}, "mutually exclusive"},
	}
	for _, tc := range cases {
		backend := &createBackend{}
		_, err := runCLI(t, backend, append([]string{"device", "create"}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want it to mention %q", tc.args, err, tc.want)
		}
		if len(backend.paths) != 0 {
			t.Errorf("%v: refused create made requests %v", tc.args, backend.paths)
		}
	}
}

// The --instances bound and instance shape copy server values; this pins
// them together.
func TestInstancesMatchServerLimits(t *testing.T) {
	if maxInstances != validation.MaxInstances {
		t.Errorf("maxInstances = %d, server MaxInstances = %d", maxInstances, validation.MaxInstances)
	}
	if instanceVCPUs != device.DefaultInstanceVCPUs || instanceMemoryMB != device.DefaultInstanceMemoryMB {
		t.Errorf("instance shape %d vCPUs/%d MB, server default %d/%d",
			instanceVCPUs, instanceMemoryMB, device.DefaultInstanceVCPUs, device.DefaultInstanceMemoryMB)
	}
	f, _ := reflect.TypeFor[DeviceCreateCmd]().FieldByName("Instances")
	if want := fmt.Sprintf("(1-%d", validation.MaxInstances); !strings.Contains(f.Tag.Get("help"), want) {
		t.Errorf("--instances help %q does not state the range %s", f.Tag.Get("help"), want)
	}
}
