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
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/launch_templates.json from the Go table")

// goldenPath is read by the console's check-launch-templates gate as well, so
// moving it means moving that gate's path with it.
const goldenPath = "testdata/launch_templates.json"

// goldenTemplate is the subset of a launch template both stacks own: what the
// console's TEMPLATE_DEFAULTS and this package's launchTemplates must agree on.
// The console additionally pins refresh_rate_hertz and states use_sdcard for
// every template; upstream sets neither, so neither is a divergence and
// neither belongs here.
type goldenTemplate struct {
	MemoryMB  int             `json:"memory_mb"`
	UseSDCard bool            `json:"use_sdcard"`
	Displays  []goldenDisplay `json:"displays"`
}

type goldenDisplay struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	DPI    int `json:"dpi"`
}

// TestLaunchTemplatesGolden is one half of the pin between the two
// hand-transcribed copies of upstream's eight templates. It holds the Go table
// against testdata; the console's check-launch-templates gate holds
// TEMPLATE_DEFAULTS against the same file. Neither copy can move alone.
func TestLaunchTemplatesGolden(t *testing.T) {
	t.Parallel()
	got, err := json.MarshalIndent(goldenView(t), "", "  ")
	if err != nil {
		t.Fatalf("marshaling the template view: %v", err)
	}
	got = append(got, '\n')

	if *updateGolden {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("writing %s: %v", goldenPath, err)
		}
		t.Logf("rewrote %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading %s (regenerate with: go test ./internal/features/device/cvd/ "+
			"-run TestLaunchTemplatesGolden -update): %v", goldenPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the Go template table no longer matches %s.\n"+
			"If the table is right, regenerate with:\n"+
			"  go test ./internal/features/device/cvd/ -run TestLaunchTemplatesGolden -update\n"+
			"and expect the console's check-launch-templates gate to fail until "+
			"TEMPLATE_DEFAULTS is updated to match.\n\ngot:\n%s\nwant:\n%s", goldenPath, got, want)
	}
}

// goldenView projects launchTemplates onto the shared subset. It reads through
// the same table the expansion uses rather than a second literal. A golden
// built from its own copy of the values would pin nothing.
func goldenView(t *testing.T) map[string]goldenTemplate {
	t.Helper()
	out := make(map[string]goldenTemplate, len(launchTemplates))
	for name, tmpl := range launchTemplates {
		vm, ok := tmpl["vm"].(map[string]any)
		if !ok {
			t.Fatalf("template %q has no vm section", name)
		}
		memory, ok := vm["memory_mb"].(int)
		if !ok {
			t.Fatalf("template %q has no vm.memory_mb", name)
		}
		// Upstream states use_sdcard only where it turns the card off, so an
		// absent key is the enabled default, which is what the console spells
		// out on every template.
		useSDCard := true
		if v, present := vm["use_sdcard"]; present {
			if b, isBool := v.(bool); isBool {
				useSDCard = b
			}
		}

		graphics, ok := tmpl["graphics"].(map[string]any)
		if !ok {
			t.Fatalf("template %q has no graphics section", name)
		}
		raw, ok := graphics["displays"].([]any)
		if !ok || len(raw) == 0 {
			t.Fatalf("template %q has no graphics.displays", name)
		}
		displays := make([]goldenDisplay, 0, len(raw))
		for i, elem := range raw {
			d, isMap := elem.(map[string]any)
			if !isMap {
				t.Fatalf("template %q display %d is not an object", name, i)
			}
			width, wOK := d["width"].(int)
			height, hOK := d["height"].(int)
			dpi, dOK := d["dpi"].(int)
			if !wOK || !hOK || !dOK {
				t.Fatalf("template %q display %d is missing width/height/dpi", name, i)
			}
			displays = append(displays, goldenDisplay{Width: width, Height: height, DPI: dpi})
		}
		out[name] = goldenTemplate{MemoryMB: memory, UseSDCard: useSDCard, Displays: displays}
	}
	return out
}
