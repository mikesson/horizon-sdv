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
	"cloud-cuttlefish-orchestrator/internal/features/device/validation"
)

// importKey is the canonical config's per-instance template selector.
// load_config.proto declares it as import_template with json_name "@import".
const importKey = "@import"

// launchTemplates transcribes the eight launch templates from the JSON string
// literals in cvd's own parser (host/commands/cvd/cli/parser/
// launch_cvd_templates.cpp). CCO applies them because cvd no longer does: its
// ExtractLaunchTemplates merges the template into the instance and then throws
// the result away. The write-back is commented out and the surviving status
// check re-reads an earlier one, so an @import that reaches the host
// configures nothing, for any caller.
//
// Nothing pins this table against upstream. The vendored cuttlefish tree is
// untracked, so no gate can diff the two; re-transcribe these when the
// vendored tree moves. That risk is accepted.
var launchTemplates = map[string]map[string]any{
	"phone":    launchTemplate(4096, sdcard, screen{720, 1280, 320}),
	"tablet":   launchTemplate(4096, sdcard, screen{2560, 1800, 320}),
	"tv":       launchTemplate(2048, sdcard, screen{1920, 1080, 213}),
	"wearable": launchTemplate(1536, noSDCard, screen{450, 450, 320}),
	"auto":     launchTemplate(4096, sdcard, screen{1080, 600, 120}, screen{400, 600, 120}),
	"slim":     launchTemplate(2048, noSDCard, screen{720, 1280, 320}),
	"go":       launchTemplate(2048, sdcard, screen{720, 1280, 320}),
	"foldable": foldableTemplate(),
}

// screen is one entry of a template's graphics.displays list.
type screen struct{ width, height, dpi int }

const (
	sdcard   = true
	noSDCard = false
)

// launchTemplate assembles one template from the values that vary between
// them. use_sdcard is emitted only where upstream turns it off. An absent key
// is the enabled default, and stating it everywhere would put a key in the
// payload the transcribed source does not have.
func launchTemplate(memoryMB int, useSDCard bool, screens ...screen) map[string]any {
	vm := map[string]any{"memory_mb": memoryMB}
	if !useSDCard {
		vm["use_sdcard"] = false
	}
	displays := make([]any, 0, len(screens))
	for _, s := range screens {
		displays = append(displays, map[string]any{
			"width": s.width, "height": s.height, "dpi": s.dpi,
		})
	}
	return map[string]any{
		"vm":       vm,
		"graphics": map[string]any{"displays": displays},
	}
}

// foldableTemplate is the one template carrying custom_actions: three hinge
// states, each a device state paired with the button that selects it.
func foldableTemplate() map[string]any {
	tmpl := launchTemplate(4096, sdcard, screen{1768, 2208, 374}, screen{832, 2268, 387})
	hinge := func(lidOpen bool, angle int, command, title, icon string) map[string]any {
		return map[string]any{
			"device_states": []any{
				map[string]any{"lid_switch_open": lidOpen, "hinge_angle_value": angle},
			},
			"button": map[string]any{"command": command, "title": title, "icon_name": icon},
		}
	}
	vm, _ := tmpl["vm"].(map[string]any)
	vm["custom_actions"] = []any{
		hinge(false, 0, "device_state_closed", "Device State Closed", "smartphone"),
		hinge(true, 90, "device_state_half_opened", "Device State Half-Opened", "laptop"),
		hinge(true, 180, "device_state_opened", "Device State Opened", "tablet"),
	}
	return tmpl
}

// templateFor returns a private copy of the named launch template with the
// blocked instance fields stripped. The strip is structural rather than a
// judgement about what these eight templates happen to contain: a
// custom_actions entry can carry shell_command or server, and the table is
// hand-transcribed, so expansion must not be a way past admission's blocklist.
func templateFor(name string) (map[string]any, bool) {
	tmpl, ok := launchTemplates[name]
	if !ok {
		return nil, false
	}
	copied := cloneMap(tmpl)
	validation.FilterBlockedInstanceFields(copied)
	return copied, true
}

// expandImports applies each instance's @import template and removes the key.
// The key is stripped rather than forwarded so that a future upstream restore
// of the discarded write-back cannot apply the template a second time.
func expandImports(envConfig map[string]any) {
	instances, ok := envConfig["instances"].([]any)
	if !ok {
		return
	}
	for _, raw := range instances {
		inst, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		selector, present := inst[importKey]
		if !present {
			continue
		}
		delete(inst, importKey)
		name, _ := selector.(string)
		tmpl, ok := templateFor(name)
		if !ok {
			continue // admission rejects unknown names
		}
		mergeTemplate(inst, tmpl)
	}
}

// mergeTemplate fills in every template key the instance leaves unset,
// descending only where both sides hold an object. A value the caller supplied
// wins whole: their graphics.displays replaces the template's list entirely
// rather than merging element by element. cvd's parser carries an unresolved
// request for index-wise merging of repeated fields (b/337089452), so a
// per-element semantic invented here would be one CCO could not keep in step.
//
// tmpl must be a private copy, because it is spliced in and never copied again.
func mergeTemplate(inst, tmpl map[string]any) {
	for k, v := range tmpl {
		existing, present := inst[k]
		if !present {
			inst[k] = v
			continue
		}
		instMap, isMap := existing.(map[string]any)
		tmplMap, tmplIsMap := v.(map[string]any)
		if isMap && tmplIsMap {
			mergeTemplate(instMap, tmplMap)
		}
	}
}
