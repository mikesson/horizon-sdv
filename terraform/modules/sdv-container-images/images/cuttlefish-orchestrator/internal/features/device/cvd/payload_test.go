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

package cvd

import (
	"context"
	"log/slog"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/device/devicetest"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// TestRouteArtifactURLs verifies each artifact role lands on its canonical
// env_config field: the load-config parser rejects unknown fields, so a
// misplaced key fails the entire cvd load rather than only that artifact.
func TestRouteArtifactURLs(t *testing.T) {
	t.Parallel()
	diskConfig := map[string]any{}
	bootConfig := map[string]any{}

	hostPkg := routeArtifactURLs(diskConfig, bootConfig, []build.ArtifactSpec{
		{Role: build.ArtifactRoleHostPackage, URL: "https://x/host.tar.gz"},
		{Role: build.ArtifactRoleDefaultBuild, URL: "https://x/images.zip"},
		{Role: build.ArtifactRoleSystemImageBuild, URL: "https://x/system.zip"},
		{Role: build.ArtifactRoleKernelBuild, URL: "https://x/kernel.zip"},
		{Role: build.ArtifactRoleBootBuild, URL: "https://x/boot.zip"},
		{Role: build.ArtifactRoleBootloaderBuild, URL: "https://x/bootloader.zip"},
	}, slog.Default())

	if hostPkg != "https://x/host.tar.gz" {
		t.Errorf("host package URL = %q", hostPkg)
	}
	if got := diskConfig["default_build"]; got != "https://x/images.zip" {
		t.Errorf("disk.default_build = %v", got)
	}
	super, _ := diskConfig["super"].(map[string]any)
	if super["system"] != "https://x/system.zip" {
		t.Errorf("disk.super = %v", diskConfig["super"])
	}
	kernel, _ := bootConfig["kernel"].(map[string]any)
	if kernel["build"] != "https://x/kernel.zip" {
		t.Errorf("boot.kernel = %v", bootConfig["kernel"])
	}
	if got := bootConfig["build"]; got != "https://x/boot.zip" {
		t.Errorf("boot.build = %v", got)
	}
	bootloader, _ := bootConfig["bootloader"].(map[string]any)
	if bootloader["build"] != "https://x/bootloader.zip" {
		t.Errorf("boot.bootloader = %v", bootConfig["bootloader"])
	}

	for _, invalid := range []string{"kernel_build", "boot_build", "bootloader_build", "system_image_build"} {
		if _, ok := diskConfig[invalid]; ok {
			t.Errorf("disk carries %q, a field the canonical config does not have", invalid)
		}
	}
}

// TestRouteArtifactURLsUnknownRole verifies the default_build fallback.
func TestRouteArtifactURLsUnknownRole(t *testing.T) {
	t.Parallel()
	diskConfig := map[string]any{}
	routeArtifactURLs(diskConfig, map[string]any{}, []build.ArtifactSpec{
		{Role: build.ArtifactRole("mystery"), URL: "https://x/blob.zip"},
	}, slog.Default())
	if diskConfig["default_build"] != "https://x/blob.zip" {
		t.Errorf("unknown role did not fall back to default_build: %v", diskConfig)
	}
}

// TestBuildEnvConfigLeavesStoredConfigAlone builds twice from one device: the
// second build must see the row as the caller wrote it. A shallow copy shares
// every nested map, so group_name injection and the disk/boot merge would land
// in d.EnvConfig itself. In production that is masked by each create attempt
// re-reading the row from Spanner, which this test does not do.
func TestBuildEnvConfigLeavesStoredConfigAlone(t *testing.T) {
	t.Parallel()
	stored := map[string]any{
		"common": map[string]any{"group_name": "caller-choice"},
		"instances": []any{
			map[string]any{"name": "cvd-1", importKey: "phone"},
		},
	}
	want := map[string]any{
		"common": map[string]any{"group_name": "caller-choice"},
		"instances": []any{
			map[string]any{"name": "cvd-1", importKey: "phone"},
		},
	}
	d := &device.Device{ID: "dev-1", Name: "cvd-1", EnvConfig: stored}

	first := buildEnvConfig(d, "group-a", map[string]any{}, "https://x/host.tar.gz",
		map[string]any{"default_build": "@ab/aosp-main/target"}, map[string]any{})
	if common, _ := first["common"].(map[string]any); common["group_name"] != "group-a" {
		t.Fatalf("first build did not inject group_name: %v", first["common"])
	}
	if !reflect.DeepEqual(d.EnvConfig, want) {
		t.Errorf("stored env_config mutated by first build:\n got %v\nwant %v", d.EnvConfig, want)
	}

	second := buildEnvConfig(d, "group-b", map[string]any{}, "", map[string]any{}, map[string]any{})
	if common, _ := second["common"].(map[string]any); common["group_name"] != "group-b" {
		t.Errorf("second build group_name = %v", second["common"])
	}
	if common, _ := second["common"].(map[string]any); common["host_package"] != nil {
		t.Errorf("second build inherited host_package from the first: %v", second["common"])
	}
	inst, _ := second["instances"].([]any)[0].(map[string]any)
	if _, present := inst["disk"]; present {
		t.Errorf("second build inherited disk from the first: %v", inst)
	}
	if !reflect.DeepEqual(d.EnvConfig, want) {
		t.Errorf("stored env_config mutated by second build:\n got %v\nwant %v", d.EnvConfig, want)
	}
}

// The launch config must speak cvd's vocabulary, and swiftshader is where the
// two diverge: CCO stores "swiftshader", cvd's name is "guest_swiftshader",
// and the wrong string does not fail. It falls through silently to auto
// resolution. On a GPU host that resolves to an accelerated mode a snapshot
// take destroys, defeating the swiftshader-only snapshot gate. A swiftshader
// instance must also disable the crosvm sandbox: current guest builds hang a
// virtio-fs device off it exactly and only under guest_swiftshader, and
// crosvm cannot suspend virtio-fs, so a sandboxed swiftshader guest can never
// be snapshotted.
func TestSimpleInstanceConfigGPUModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode        device.GPUMode
		wantGPU     string
		wantSandbox bool // a crosvm block with enable_sandbox=false is present
	}{
		{device.GPUModeSwift, "guest_swiftshader", true},
		{device.GPUModeGfxstream, "gfxstream", false},
		{device.GPUModeVirgl, "drm_virgl", false},
		{device.GPUModeAuto, "auto", false},
	}
	for _, tc := range tests {
		t.Run(string(tc.mode), func(t *testing.T) {
			t.Parallel()
			cfg := simpleInstanceConfig(&device.Device{Name: "cvd-1", GPUMode: tc.mode})
			graphics, _ := cfg["graphics"].(map[string]any)
			if graphics == nil || graphics["gpu_mode"] != tc.wantGPU {
				t.Errorf("gpu_mode = %v, want %q", graphics, tc.wantGPU)
			}
			vm, _ := cfg["vm"].(map[string]any)
			if vm == nil {
				t.Fatal("instance config has no vm section")
			}
			crosvm, hasCrosvm := vm["crosvm"].(map[string]any)
			if tc.wantSandbox {
				if !hasCrosvm || crosvm["enable_sandbox"] != false {
					t.Errorf("crosvm = %v, want enable_sandbox=false", vm["crosvm"])
				}
			} else if hasCrosvm {
				t.Errorf("crosvm = %v, want no crosvm block (sandbox stays on)", crosvm)
			}
		})
	}
}

// An advanced-mode config that omits the renderer or the sandbox is silent
// rather than choosing cvd's defaults on purpose, and cvd fills the silence
// with a GPU auto walk and a sandbox that drags virtio-fs onto the bus, both of
// which contradict the device row the snapshot gate reads. The payload fills
// them from the row instead, and never overrules a value the caller wrote:
// enable_sandbox=true is a real choice, so refusing its snapshot later is
// clearer than flipping it without a word.
func TestBuildEnvConfigAlignsAdvancedInstances(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mode        device.GPUMode
		instance    map[string]any
		wantGPU     string // "" means graphics.gpu_mode must not exist
		wantSandbox any    // nil means no vm.crosvm.enable_sandbox must exist
	}{
		{
			name:        "silence takes the row's mode and the sandbox with it",
			mode:        device.GPUModeSwift,
			instance:    map[string]any{"name": "cvd-1"},
			wantGPU:     GuestSwiftshaderMode,
			wantSandbox: false,
		},
		{
			name:        "an explicit guest_swiftshader still gets the sandbox off",
			mode:        device.GPUModeSwift,
			instance:    map[string]any{"graphics": map[string]any{"gpu_mode": GuestSwiftshaderMode}},
			wantGPU:     GuestSwiftshaderMode,
			wantSandbox: false,
		},
		{
			name: "a sandbox the caller asked for survives",
			mode: device.GPUModeSwift,
			instance: map[string]any{
				"vm": map[string]any{"crosvm": map[string]any{"enable_sandbox": true}},
			},
			wantGPU:     GuestSwiftshaderMode,
			wantSandbox: true,
		},
		{
			name:        "a renderer the caller named survives the row",
			mode:        device.GPUModeSwift,
			instance:    map[string]any{"graphics": map[string]any{"gpu_mode": "gfxstream"}},
			wantGPU:     "gfxstream",
			wantSandbox: nil,
		},
		{
			name:        "no sandbox key for a mode that does not need one",
			mode:        device.GPUModeAuto,
			instance:    map[string]any{"name": "cvd-1"},
			wantGPU:     "auto",
			wantSandbox: nil,
		},
		{
			name:        "a row with no mode invents nothing",
			mode:        "",
			instance:    map[string]any{"name": "cvd-1"},
			wantGPU:     "",
			wantSandbox: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := &device.Device{
				ID: "dev-1", Name: "cvd-1", GPUMode: tc.mode,
				EnvConfig: map[string]any{"instances": []any{tc.instance}},
			}
			built := buildEnvConfig(d, "group-a", nil, "", map[string]any{}, map[string]any{})

			inst, _ := built["instances"].([]any)[0].(map[string]any)
			graphics, _ := inst["graphics"].(map[string]any)
			if tc.wantGPU == "" {
				if _, present := graphics["gpu_mode"]; present {
					t.Errorf("graphics = %v, want no gpu_mode invented", graphics)
				}
			} else if got := graphics["gpu_mode"]; got != tc.wantGPU {
				t.Errorf("graphics.gpu_mode = %v, want %q", got, tc.wantGPU)
			}

			vm, _ := inst["vm"].(map[string]any)
			crosvm, _ := vm["crosvm"].(map[string]any)
			sandbox, present := crosvm["enable_sandbox"]
			if tc.wantSandbox == nil {
				if present {
					t.Errorf("vm.crosvm = %v, want no enable_sandbox", crosvm)
				}
			} else if !present || sandbox != tc.wantSandbox {
				t.Errorf("vm.crosvm.enable_sandbox = %v (present %v), want %v", sandbox, present, tc.wantSandbox)
			}

			// The row is the snapshot gate's evidence; filling the payload must
			// not write the fill back into it.
			stored, _ := d.EnvConfig["instances"].([]any)[0].(map[string]any)
			if _, present := stored["graphics"]; present && tc.instance["graphics"] == nil {
				t.Errorf("stored env_config gained a graphics section: %v", stored)
			}
			if _, present := stored["vm"]; present && tc.instance["vm"] == nil {
				t.Errorf("stored env_config gained a vm section: %v", stored)
			}
		})
	}
}

// TestMergeInstanceSection verifies user-specified fields survive the merge
// while unset fields are filled, for a non-disk section.
func TestMergeInstanceSection(t *testing.T) {
	t.Parallel()
	envConfig := map[string]any{
		"instances": []any{
			map[string]any{"boot": map[string]any{"build": "user-choice"}},
			map[string]any{},
		},
	}
	mergeInstanceSection(envConfig, "boot", map[string]any{
		"build":  "resolved",
		"kernel": map[string]any{"build": "resolved-kernel"},
	})

	instances := envConfig["instances"].([]any)
	first := instances[0].(map[string]any)["boot"].(map[string]any)
	if first["build"] != "user-choice" {
		t.Errorf("user boot.build overridden: %v", first["build"])
	}
	if kernel, _ := first["kernel"].(map[string]any); kernel["build"] != "resolved-kernel" {
		t.Errorf("unset boot.kernel not filled: %v", first["kernel"])
	}
	second := instances[1].(map[string]any)["boot"].(map[string]any)
	if second["build"] != "resolved" {
		t.Errorf("empty instance boot.build not filled: %v", second["build"])
	}
}

// fakeBuilds serves registered build records by id.
type fakeBuilds map[string]*build.Build

func (f fakeBuilds) Get(_ context.Context, id string) (*build.Build, error) {
	if b, ok := f[id]; ok {
		return b, nil
	}
	return nil, build.ErrNotFound
}

func (fakeBuilds) List(context.Context, build.ListFilter) ([]*build.Build, error) { return nil, nil }

func (fakeBuilds) Count(context.Context, build.ListFilter) (int64, error) { return 0, nil }

// payloadActuator builds an actuator whose only dependency in use is the
// build registry, which is all buildCreateRequest reads.
func payloadActuator(builds fakeBuilds) *Actuator {
	return NewActuator(devicetest.NewFakeRepo(), hosttest.NewFakeRepo(), builds,
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default())
}

// sdvBuilds are the two registered URL builds of an SDV pair, each with its
// own host package, plus an Android Build record for the device level.
func sdvBuilds() fakeBuilds {
	return fakeBuilds{
		"b-core": {ID: "b-core", Source: build.SourceURL, ArtifactSpecs: []build.ArtifactSpec{
			{Role: build.ArtifactRoleHostPackage, URL: "gs://b/sdv_core_cf/host.tar.gz"},
			{Role: build.ArtifactRoleDefaultBuild, URL: "gs://b/sdv_core_cf/img.zip"},
			{Role: build.ArtifactRoleKernelBuild, URL: "gs://b/sdv_core_cf/kernel.zip"},
		}},
		"b-ivi": {ID: "b-ivi", Source: build.SourceURL, ArtifactSpecs: []build.ArtifactSpec{
			{Role: build.ArtifactRoleHostPackage, URL: "gs://b/sdv_ivi_cf/host.tar.gz"},
			{Role: build.ArtifactRoleDefaultBuild, URL: "gs://b/sdv_ivi_cf/img.zip"},
		}},
		"b-ab": {ID: "b-ab", Source: build.SourceAB, BuildID: "12345", Target: "sdv_ivi_cf-userdebug"},
		"b-device": {ID: "b-device", Source: build.SourceURL, ArtifactSpecs: []build.ArtifactSpec{
			{Role: build.ArtifactRoleHostPackage, URL: "gs://b/device/host.tar.gz"},
			{Role: build.ArtifactRoleDefaultBuild, URL: "gs://b/device/img.zip"},
			{Role: build.ArtifactRoleBootloaderBuild, URL: "gs://b/device/bootloader.zip"},
		}},
	}
}

// payloadInstances returns the built request's instances keyed by name.
func payloadInstances(t *testing.T, env map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	instances, _ := env["instances"].([]any)
	for _, raw := range instances {
		inst, _ := raw.(map[string]any)
		name, _ := inst["name"].(string)
		out[name] = inst
	}
	return out
}

func section(inst map[string]any, name string) map[string]any {
	sec, _ := inst[name].(map[string]any)
	return sec
}

// Each instance's "@build" fills that instance alone, under what the caller
// wrote and over the device-level build, which still fills the gaps of every
// instance. The key never reaches the host, and the stored row is untouched.
func TestBuildCreateRequestRoutesInstanceBuilds(t *testing.T) {
	t.Parallel()
	stored := map[string]any{
		"instances": []any{
			map[string]any{
				"name":   "core",
				"@build": "b-core",
				"boot":   map[string]any{"kernel": map[string]any{"build": "gs://user/kernel.zip"}},
			},
			map[string]any{"name": "ivi", "@build": "b-ab"},
			map[string]any{"name": "spare"},
		},
	}
	d := &device.Device{ID: "dev-1", Name: "sdv", BuildID: "b-device", EnvConfig: cloneMap(stored)}

	req, err := payloadActuator(sdvBuilds()).buildCreateRequest(t.Context(), d, "group-a")
	require.NoError(t, err)
	got := payloadInstances(t, req.EnvConfig)

	core := got["core"]
	assert.Equal(t, "gs://b/sdv_core_cf/img.zip", section(core, "disk")["default_build"],
		"the instance's own build beats the device-level build")
	assert.Equal(t, map[string]any{"build": "gs://user/kernel.zip"}, section(core, "boot")["kernel"],
		"a field the caller wrote beats the instance's build")
	assert.Equal(t, map[string]any{"build": "gs://b/device/bootloader.zip"}, section(core, "boot")["bootloader"],
		"the device-level build still fills what neither the caller nor the instance's build set")

	ivi := got["ivi"]
	assert.Equal(t, "@ab/12345/sdv_ivi_cf-userdebug", section(ivi, "disk")["default_build"],
		"an Android Build record keys by its build id and target")
	assert.NotContains(t, section(ivi, "boot"), "kernel", "the core's kernel must not leak into the ivi")

	assert.Equal(t, "gs://b/device/img.zip", section(got["spare"], "disk")["default_build"],
		"an instance without @build takes the device-level build")

	for name, inst := range got {
		assert.NotContains(t, inst, "@build", "instance %s carries CCO's key to the host", name)
	}
	common, _ := req.EnvConfig["common"].(map[string]any)
	assert.Equal(t, "gs://b/device/host.tar.gz", common["host_package"],
		"the device-level build's host package beats an instance build's")
	assert.Equal(t, stored, d.EnvConfig, "the stored env_config must not change")
}

// common.host_package: a value the caller wrote, then the device-level
// build's, then the first instance in array order whose build has one.
func TestBuildCreateRequestHostPackagePrecedence(t *testing.T) {
	t.Parallel()
	pair := func(common map[string]any) map[string]any {
		env := map[string]any{"instances": []any{
			map[string]any{"name": "core", "@build": "b-core"},
			map[string]any{"name": "ivi", "@build": "b-ivi"},
		}}
		if common != nil {
			env["common"] = common
		}
		return env
	}
	tests := []struct {
		name    string
		buildID string
		env     map[string]any
		want    string
	}{
		{
			name:    "the caller's value beats the device-level build",
			buildID: "b-device",
			env:     pair(map[string]any{"host_package": "gs://user/host.tar.gz"}),
			want:    "gs://user/host.tar.gz",
		},
		{
			name: "the caller's value beats the instance builds",
			env:  pair(map[string]any{"host_package": "gs://user/host.tar.gz"}),
			want: "gs://user/host.tar.gz",
		},
		{
			name:    "an empty caller value is not a choice",
			buildID: "b-device",
			env:     pair(map[string]any{"host_package": ""}),
			want:    "gs://b/device/host.tar.gz",
		},
		{
			name: "with no device build the first instance's host package wins",
			env:  pair(nil),
			want: "gs://b/sdv_core_cf/host.tar.gz",
		},
		{
			name:    "a device build without a host package defers to the instances",
			buildID: "b-ab",
			env:     pair(nil),
			want:    "gs://b/sdv_core_cf/host.tar.gz",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := &device.Device{ID: "dev-1", Name: "sdv", BuildID: tc.buildID, EnvConfig: tc.env}
			req, err := payloadActuator(sdvBuilds()).buildCreateRequest(t.Context(), d, "group-a")
			require.NoError(t, err)
			common, _ := req.EnvConfig["common"].(map[string]any)
			assert.Equal(t, tc.want, common["host_package"])
			assert.Equal(t, "group-a", common["group_name"])
		})
	}
}

// buildEnvConfig keeps a host package the caller wrote and fills an absent
// one from the device-level build.
func TestBuildEnvConfigKeepsWrittenHostPackage(t *testing.T) {
	t.Parallel()
	d := &device.Device{ID: "dev-1", Name: "cvd-1", EnvConfig: map[string]any{
		"common":    map[string]any{"host_package": "gs://user/host.tar.gz"},
		"instances": []any{map[string]any{"name": "cvd-1"}},
	}}
	built := buildEnvConfig(d, "group-a", nil, "gs://build/host.tar.gz", map[string]any{}, map[string]any{})
	common, _ := built["common"].(map[string]any)
	assert.Equal(t, "gs://user/host.tar.gz", common["host_package"])

	d.EnvConfig = map[string]any{"instances": []any{map[string]any{"name": "cvd-1"}}}
	built = buildEnvConfig(d, "group-a", nil, "gs://build/host.tar.gz", map[string]any{}, map[string]any{})
	common, _ = built["common"].(map[string]any)
	assert.Equal(t, "gs://build/host.tar.gz", common["host_package"])
}

// A registered build deleted between admission and launch fails the create
// with the instance and id named. Reading the id as an Android Build
// reference, the device-level fallback, would fetch an unrelated build.
func TestBuildCreateRequestMissingInstanceBuild(t *testing.T) {
	t.Parallel()
	d := &device.Device{ID: "dev-1", Name: "sdv", EnvConfig: map[string]any{"instances": []any{
		map[string]any{"name": "core", "@build": "b-core"},
		map[string]any{"name": "ivi", "@build": "b-gone"},
	}}}
	_, err := payloadActuator(sdvBuilds()).buildCreateRequest(t.Context(), d, "group-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `instances[1] ("ivi")`)
	assert.Contains(t, err.Error(), "b-gone")
}

// "@build" never reaches cvd, even on a path that resolved nothing, because
// cvd's parser rejects the whole load on a field it does not know.
func TestBuildEnvConfigStripsInstanceBuildKey(t *testing.T) {
	t.Parallel()
	d := &device.Device{ID: "dev-1", Name: "cvd-1", EnvConfig: map[string]any{
		"instances": []any{map[string]any{"name": "cvd-1", "@build": "b-core"}},
	}}
	built := buildEnvConfig(d, "group-a", nil, "", map[string]any{}, map[string]any{})
	inst, _ := built["instances"].([]any)[0].(map[string]any)
	assert.NotContains(t, inst, "@build")
	stored, _ := d.EnvConfig["instances"].([]any)[0].(map[string]any)
	assert.Equal(t, "b-core", stored["@build"], "the stored row keeps the key")
}

// The console and device vocabulary say "swiftshader". cvd does not know that
// name and silently resolves it as auto, so the payload translates it, and the
// sandbox fill that guest_swiftshader needs then applies.
func TestBuildEnvConfigTranslatesSwiftshader(t *testing.T) {
	t.Parallel()
	d := &device.Device{ID: "dev-1", Name: "cvd-1", EnvConfig: map[string]any{
		"instances": []any{
			map[string]any{"name": "core", "graphics": map[string]any{"gpu_mode": "swiftshader"}},
			map[string]any{"name": "ivi", "graphics": map[string]any{"gpu_mode": "gfxstream"}},
		},
	}}
	built := buildEnvConfig(d, "group-a", nil, "", map[string]any{}, map[string]any{})
	got := payloadInstances(t, built)

	assert.Equal(t, GuestSwiftshaderMode, section(got["core"], "graphics")["gpu_mode"])
	crosvm, _ := section(got["core"], "vm")["crosvm"].(map[string]any)
	assert.Equal(t, false, crosvm["enable_sandbox"], "the translated mode takes the sandbox fill with it")

	assert.Equal(t, "gfxstream", section(got["ivi"], "graphics")["gpu_mode"], "other modes pass through")
	assert.NotContains(t, got["ivi"], "vm", "no sandbox fill for an accelerated mode")

	stored, _ := d.EnvConfig["instances"].([]any)[0].(map[string]any)
	assert.Equal(t, "swiftshader", section(stored, "graphics")["gpu_mode"], "the stored row keeps what the caller wrote")
}
