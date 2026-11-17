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

package devicefeature

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// The three fixtures are real launcher.logs, captured 2026-08-11 from one
// device on the live deployment as it was driven through the three launches
// that matter. Each is its capture with whole lines dropped and nothing
// rewritten, keeping the assemble header, the GPU decision, the crosvm argument
// blocks and the boot tail:
//
//   - launcher-pinned-swiftshader.log: the launch CCO's create pins
//     (--gpu_mode=guest_swiftshader requested, sandbox off). Source lines
//     1-22, 193-216, 1085-1245, 1500-1528 of 1528.
//   - launcher-pinned-swiftshader-restarted.log: the same file after a
//     launcher-level restart, which APPENDS a second run_cvd launch without
//     re-assembling. Source lines 1-22, 193-216, 1085-1245, 1500-1600,
//     2460-2610, 2740-2757 of 2757. The first four ranges are byte-identical
//     to the fixture above, which is what the append means.
//   - launcher-defaults-relaunch.log: what a stop → start leaves running. The
//     file is REWRITTEN, the GPU auto walk runs and happens to land on
//     guest_swiftshader, and the launch carries two virtio-fs shares anyway.
//     Source lines 1-22, 196-262, 1190-1350, 1640-1671 of 1671.
//
// A fourth, launcher-pid-prefix-swiftshader.log, is a pinned create captured
// 2026-10-27 on cuttlefish 1.58 (upstream 7a4f72d3d), whose log lines name the
// process with its pid, as in "assemble_cvd(2031)  D ...". Source lines 1-22,
// 790-810, 1471-1640, 1900-1931 of 1931.
const (
	pinnedLaunchLog    = "launcher-pinned-swiftshader.log"
	restartedLaunchLog = "launcher-pinned-swiftshader-restarted.log"
	defaultsLaunchLog  = "launcher-defaults-relaunch.log"
	pidPrefixLaunchLog = "launcher-pid-prefix-swiftshader.log"
)

func launcherLog(t *testing.T, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return content
}

// The verdicts the fixtures were captured to establish. The third is the one
// that cost a device. Its renderer resolved to guest_swiftshader, so every
// check CCO held before this one passed it, and the capture still killed the
// guest on the virtio-fs share the same log records.
func TestLaunchSnapshotBlockerOnCapturedLaunches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		fixture     string
		wantBlocker string // substring; "" means the launch is snapshottable
	}{
		{pinnedLaunchLog, ""},
		{restartedLaunchLog, ""},
		{defaultsLaunchLog, "virtio-fs"},
		{pidPrefixLaunchLog, ""},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			t.Parallel()
			blocker, err := launchSnapshotBlocker(launcherLog(t, tt.fixture))
			if err != nil {
				t.Fatalf("launchSnapshotBlocker = %v, want a verdict", err)
			}
			if tt.wantBlocker == "" {
				if blocker != "" {
					t.Fatalf("launch refused as %q, want snapshottable", blocker)
				}
				return
			}
			if !strings.Contains(blocker, tt.wantBlocker) {
				t.Fatalf("blocker = %q, want it to name %q", blocker, tt.wantBlocker)
			}
		})
	}
}

// A `cvd start` rewrites launcher.log, so one segment is the normal case, but
// nothing guarantees it, and a file holding two launches has to be read as the
// later one. Both directions are asserted because only the second can fail on
// segment SELECTION. A parser anchored to the first segment still scans to the
// end of the file, so a safe launch followed by an unsafe one refuses either
// way. Re-assembling out of a wreck is the case that must go the other way.
// The device is snapshottable again, and a check that kept refusing on the
// superseded launch would make the remedy unreachable.
func TestLaunchSnapshotBlockerReadsTheLastLaunch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		log         []byte
		wantBlocker string
	}{
		{
			"a defaults relaunch behind a pinned one still refuses",
			append(launcherLog(t, pinnedLaunchLog), launcherLog(t, defaultsLaunchLog)...),
			"virtio-fs",
		},
		{
			"re-assembling into a pinned launch clears the earlier one",
			append(launcherLog(t, defaultsLaunchLog), launcherLog(t, pinnedLaunchLog)...),
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			blocker, err := launchSnapshotBlocker(tt.log)
			if err != nil {
				t.Fatalf("launchSnapshotBlocker = %v, want a verdict", err)
			}
			if tt.wantBlocker == "" {
				if blocker != "" {
					t.Fatalf("blocker = %q, want the LAST launch's verdict: snapshottable", blocker)
				}
				return
			}
			if !strings.Contains(blocker, tt.wantBlocker) {
				t.Fatalf("blocker = %q, want the LAST launch's %q refusal", blocker, tt.wantBlocker)
			}
		})
	}
}

// Nothing readable must ever read as safe. The fetch exists because silence is
// not evidence.
func TestLaunchSnapshotBlockerFailsClosedOnUnreadableLogs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		log  []byte
	}{
		{"empty", nil},
		{"not a launcher log", []byte("<html>404 not found</html>\n")},
		{"launch lines but no assemble run", []byte(
			"run_cvd D 08-11 16:16:04  2165  2165 subprocess.cpp:474] --disable-sandbox\n")},
		{"a process that only starts with assemble_cvd", []byte(
			"assemble_cvd_helper(2031)  D 10-27 20:00:10  2031  2031 graphics_flags.cc:425] " +
				"Checking requirements for --gpu_mode=guest_swiftshader\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			blocker, err := launchSnapshotBlocker(tt.log)
			if !errors.Is(err, errLaunchUnverifiable) {
				t.Fatalf("launchSnapshotBlocker = (%q, %v), want errLaunchUnverifiable", blocker, err)
			}
		})
	}
}

// The auto walk checks every candidate mode on its way to a decision, so the
// requirement checks are only a verdict when there is exactly one. A walk whose
// selection line never arrived says nothing, and saying nothing must not pass.
func TestLaunchedGPUMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		selected string
		checked  []string
		want     string
		wantOK   bool
	}{
		{"auto walk decides", "guest_swiftshader", []string{"gfxstream", "guest_swiftshader", "none"}, "guest_swiftshader", true},
		{"explicit mode is checked once", "", []string{"gfxstream"}, "gfxstream", true},
		{"walk without a selection is unreadable", "", []string{"gfxstream", "guest_swiftshader"}, "", false},
		{"no gpu evidence at all", "", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := launchedGPUMode(tt.selected, tt.checked)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("launchedGPUMode(%q, %v) = (%q, %v), want (%q, %v)",
					tt.selected, tt.checked, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// An accelerated renderer is refused on the log's word alone, whatever the
// device row claims. That divergence is why the fetch exists.
func TestLaunchSnapshotBlockerRefusesAnAcceleratedRenderer(t *testing.T) {
	t.Parallel()

	log := strings.ReplaceAll(string(launcherLog(t, pinnedLaunchLog)),
		"Checking requirements for --gpu_mode=guest_swiftshader",
		"Checking requirements for --gpu_mode=gfxstream")
	blocker, err := launchSnapshotBlocker([]byte(log))
	if err != nil {
		t.Fatalf("launchSnapshotBlocker = %v, want a verdict", err)
	}
	if !strings.Contains(blocker, "gfxstream") {
		t.Fatalf("blocker = %q, want it to name the mode the launch used", blocker)
	}
}

// Every refusal names the instance and the remedy. An advanced-mode config is
// the caller's own text, and a rejection they cannot act on is a dead end.
func TestSnapshotSafeEnvConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		instance    map[string]any
		wantRefusal []string // substrings; nil means the take passes to the log check
	}{
		{
			name:     "no env config fields",
			instance: map[string]any{"name": "cvd-1"},
		},
		{
			name: "swiftshader with the sandbox off",
			instance: map[string]any{
				"graphics": map[string]any{"gpu_mode": "guest_swiftshader"},
				"vm":       map[string]any{"crosvm": map[string]any{"enable_sandbox": false}},
			},
		},
		{
			name: "sandbox explicitly on",
			instance: map[string]any{
				"vm": map[string]any{"crosvm": map[string]any{"enable_sandbox": true}},
			},
			wantRefusal: []string{"instance 0", "enable_sandbox", "recreate"},
		},
		{
			name: "an accelerated renderer",
			instance: map[string]any{
				"graphics": map[string]any{"gpu_mode": "gfxstream"},
			},
			wantRefusal: []string{"instance 0", "gfxstream", "guest_swiftshader"},
		},
		{
			// The payload translates the console's name to guest_swiftshader,
			// so the stored value launched software rendering.
			name: "the console's swiftshader name",
			instance: map[string]any{
				"graphics": map[string]any{"gpu_mode": "swiftshader"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := snapshotSafeEnvConfig(map[string]any{"instances": []any{tt.instance}})
			if tt.wantRefusal == nil {
				if err != nil {
					t.Fatalf("snapshotSafeEnvConfig = %v, want the take to reach the log check", err)
				}
				return
			}
			var ve *device.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("snapshotSafeEnvConfig = %v, want *ValidationError", err)
			}
			for _, want := range tt.wantRefusal {
				if !strings.Contains(ve.Message, want) {
					t.Errorf("refusal %q does not mention %q", ve.Message, want)
				}
			}
		})
	}
}

// The offending instance is named by its own index, not the first one's.
func TestSnapshotSafeEnvConfigNamesTheOffendingInstance(t *testing.T) {
	t.Parallel()

	err := snapshotSafeEnvConfig(map[string]any{"instances": []any{
		map[string]any{"graphics": map[string]any{"gpu_mode": "guest_swiftshader"}},
		map[string]any{"graphics": map[string]any{"gpu_mode": "gfxstream"}},
	}})
	var ve *device.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("snapshotSafeEnvConfig = %v, want *ValidationError", err)
	}
	if !strings.Contains(ve.Message, "instance 1") {
		t.Errorf("refusal %q does not name instance 1", ve.Message)
	}
}
