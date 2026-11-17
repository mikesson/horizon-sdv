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
	"strings"
	"testing"
)

// renderHelp runs the production parser on args and returns what --help
// printed. The no-op Exit lets Kong continue past help, so the parse error is
// ignored.
func renderHelp(t *testing.T, args ...string) string {
	t.Helper()
	var c CLI
	parser, err := NewParser(&c, nil, "test")
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	var stdout strings.Builder
	parser.Stdout = &stdout
	parser.Exit = func(int) {}
	_, _ = parser.Parse(args)
	return stdout.String()
}

// rootHelpMaxLines is the one-screen budget for 'cfctl --help'. Turning off
// NoExpandSubcommands produces about 140 lines.
const rootHelpMaxLines = 50

func TestRootHelpListsCommandsNotLeaves(t *testing.T) {
	out := renderHelp(t, "--help")

	// Every top-level command is present with its summary.
	for _, want := range []string{
		"Manage Cuttlefish devices", "Administrative operations",
		"Manage CLI configuration", "Diagnose connectivity",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("root help missing top-level summary %q:\n%s", want, out)
		}
	}
	// No leaf paths are expanded into it.
	for _, leaf := range []string{"device snapshot take", "admin host-image", "config set-server"} {
		if strings.Contains(out, leaf) {
			t.Errorf("root help expands leaf %q; discovery is layered on purpose:\n%s", leaf, out)
		}
	}
	// The config flag groups belong on 'cfctl config --help' only.
	if strings.Contains(out, "Server Configuration:") {
		t.Errorf("root help hoists the config groups:\n%s", out)
	}
	if lines := strings.Count(out, "\n"); lines > rootHelpMaxLines {
		t.Errorf("root help is %d lines, budget %d. It must stay one screen", lines, rootHelpMaxLines)
	}
}

// The env:"-" opt-out sentinel must not render. The default formatter would
// print it as "($-)" on every help screen.
func TestHelpNeverRendersTheEnvOptOutSentinel(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"device", "delete", "--help"},
		{"device", "create", "--help"},
	} {
		out := renderHelp(t, args...)
		if strings.Contains(out, "$-") {
			t.Errorf("%v renders the env:\"-\" sentinel:\n%s", args, out)
		}
	}
	// Real env vars keep rendering.
	if out := renderHelp(t, "--help"); !strings.Contains(out, "($CFCTL_SERVER)") {
		t.Errorf("root help lost real env rendering:\n%s", out)
	}
}

func TestNounHelpShowsVerbsAndSubtrees(t *testing.T) {
	out := renderHelp(t, "device", "--help")
	for _, want := range []string{
		"device create", "device snapshot", "Manage device snapshots",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("device help missing %q:\n%s", want, out)
		}
	}
	// The subtree is one summary line with no expanded leaves.
	if strings.Contains(out, "device snapshot take") {
		t.Errorf("device help expands the snapshot subtree:\n%s", out)
	}

	out = renderHelp(t, "config", "--help")
	if !strings.Contains(out, "Server Configuration (selected profile):") || !strings.Contains(out, "config set-server") ||
		!strings.Contains(out, "Profiles:") || !strings.Contains(out, "config create") {
		t.Errorf("config help lost its groups:\n%s", out)
	}
}
