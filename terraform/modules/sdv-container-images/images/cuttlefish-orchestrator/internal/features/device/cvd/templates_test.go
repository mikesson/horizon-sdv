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
	"slices"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/features/device/validation"
)

// advancedEnvConfig builds the payload for a single-instance advanced-mode
// device and returns instances[0].
func advancedEnvConfig(t *testing.T, inst map[string]any) map[string]any {
	t.Helper()
	d := &device.Device{ID: "dev-1", Name: "cvd-1", EnvConfig: map[string]any{
		"instances": []any{inst},
	}}
	env := buildEnvConfig(d, "cvd-1", map[string]any{}, "", map[string]any{}, map[string]any{})
	instances, ok := env["instances"].([]any)
	if !ok || len(instances) != 1 {
		t.Fatalf("instances = %v", env["instances"])
	}
	got, ok := instances[0].(map[string]any)
	if !ok {
		t.Fatalf("instances[0] = %v", instances[0])
	}
	return got
}

func displays(t *testing.T, inst map[string]any) []any {
	t.Helper()
	graphics, ok := inst["graphics"].(map[string]any)
	if !ok {
		t.Fatalf("graphics = %v", inst["graphics"])
	}
	got, ok := graphics["displays"].([]any)
	if !ok {
		t.Fatalf("graphics.displays = %v", graphics["displays"])
	}
	return got
}

// TestExpandImportSuppliesTemplate covers the shape cfctl and raw API callers
// send: an @import with no graphics at all. Template selection reaches the
// host as a no-op, because cvd's ExtractLaunchTemplates discards the merged
// instance, so everything the template contributes has to be added here.
func TestExpandImportSuppliesTemplate(t *testing.T) {
	t.Parallel()

	auto := advancedEnvConfig(t, map[string]any{"name": "cvd-1", importKey: "auto"})
	if got := displays(t, auto); len(got) != 2 {
		t.Errorf("@import auto: got %d displays, want 2: %v", len(got), got)
	}

	foldable := advancedEnvConfig(t, map[string]any{"name": "cvd-1", importKey: "foldable"})
	vm, ok := foldable["vm"].(map[string]any)
	if !ok {
		t.Fatalf("vm = %v", foldable["vm"])
	}
	actions, ok := vm["custom_actions"].([]any)
	if !ok {
		t.Fatalf("vm.custom_actions = %v", vm["custom_actions"])
	}
	states := 0
	for _, raw := range actions {
		action, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("custom_action = %v", raw)
		}
		list, ok := action["device_states"].([]any)
		if !ok {
			t.Fatalf("custom_action.device_states = %v", action["device_states"])
		}
		states += len(list)
	}
	if states != 3 {
		t.Errorf("@import foldable: got %d device_states, want 3", states)
	}
}

// TestExpandImportCallerKeysWin covers the console shape: the editor emits the
// whole display list, so expansion must leave it alone rather than merge the
// template's entries into it element by element.
func TestExpandImportCallerKeysWin(t *testing.T) {
	t.Parallel()

	// "phone" declares one 720x1280 display; the caller declares two of their
	// own, so nothing of the template's list may survive.
	inst := advancedEnvConfig(t, map[string]any{
		"name":    "cvd-1",
		importKey: "phone",
		"graphics": map[string]any{"displays": []any{
			map[string]any{"width": 1080, "height": 1920, "dpi": 420},
			map[string]any{"width": 1080, "height": 1920, "dpi": 420},
		}},
	})

	got := displays(t, inst)
	if len(got) != 2 {
		t.Fatalf("caller displays: got %d, want 2: %v", len(got), got)
	}
	for i, raw := range got {
		display, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("displays[%d] = %v", i, raw)
		}
		if display["width"] != 1080 || display["height"] != 1920 || display["dpi"] != 420 {
			t.Errorf("displays[%d] = %v, want the caller's own", i, display)
		}
	}

	// The template still supplies what the caller left unset.
	vm, ok := inst["vm"].(map[string]any)
	if !ok || vm["memory_mb"] != 4096 {
		t.Errorf("vm = %v, want the template's memory_mb", inst["vm"])
	}
}

// TestExpandImportStripsKey checks that @import never reaches the host. cvd's
// write-back is commented out today; if it is ever restored, a forwarded key
// would apply the template a second time.
func TestExpandImportStripsKey(t *testing.T) {
	t.Parallel()
	inst := advancedEnvConfig(t, map[string]any{"name": "cvd-1", importKey: "tv"})
	if _, present := inst[importKey]; present {
		t.Errorf("emitted instance still carries %s: %v", importKey, inst)
	}
	if got := displays(t, inst); len(got) != 1 {
		t.Errorf("@import tv: got %d displays, want 1", len(got))
	}
}

// TestTemplateForReturnsPrivateCopy guards the package-level table against the
// merge, which splices template values straight into the caller's instance.
func TestTemplateForReturnsPrivateCopy(t *testing.T) {
	t.Parallel()
	first, ok := templateFor("auto")
	if !ok {
		t.Fatal("no auto template")
	}
	graphics, _ := first["graphics"].(map[string]any)
	graphics["displays"] = []any{}

	second, ok := templateFor("auto")
	if !ok {
		t.Fatal("no auto template")
	}
	if got := displays(t, second); len(got) != 2 {
		t.Errorf("table was mutated: auto now has %d displays", len(got))
	}
}

// TestExpandedTemplatesPassAdmission runs every template's expansion back
// through admission. The table is hand-transcribed and a custom_actions entry
// can carry shell_command or server, so what the eight happen to contain today
// is not the argument. The expansion has to be structurally incapable of
// putting a field into the payload that a caller could not have sent.
func TestExpandedTemplatesPassAdmission(t *testing.T) {
	t.Parallel()
	for _, name := range validation.ImportTemplateNames() {
		d := &device.Device{ID: "dev-1", Name: "cvd-1", EnvConfig: map[string]any{
			"instances": []any{map[string]any{"name": "cvd-1", importKey: name}},
		}}
		env := buildEnvConfig(d, "cvd-1", map[string]any{}, "", map[string]any{}, map[string]any{})
		if err := validation.ValidateEnvConfig(env); err != nil {
			t.Errorf("@import %q expands to a config admission rejects: %v", name, err)
		}
	}
}

// TestTemplateNamesMatchValidation keeps the admission-time vocabulary and the
// table of bodies equal: a name accepted with no body expands to nothing, and
// a body with no name is unreachable.
func TestTemplateNamesMatchValidation(t *testing.T) {
	t.Parallel()
	accepted := validation.ImportTemplateNames()
	for _, name := range accepted {
		if _, ok := launchTemplates[name]; !ok {
			t.Errorf("validation accepts %q but there is no template body", name)
		}
	}
	for name := range launchTemplates {
		if !slices.Contains(accepted, name) {
			t.Errorf("template %q exists but validation rejects the name", name)
		}
	}

	// The selector key is a second copy as well. Were the two to drift,
	// admission would wave every name through at a key expansion never reads.
	bad := map[string]any{"instances": []any{
		map[string]any{"name": "cvd-1", importKey: "nosuch"},
	}}
	if err := validation.ValidateEnvConfig(bad); err == nil {
		t.Errorf("validation accepts an unknown template at %s", importKey)
	}
}
