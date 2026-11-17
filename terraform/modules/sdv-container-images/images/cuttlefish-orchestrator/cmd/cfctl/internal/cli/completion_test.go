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
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

// commandTree walks the Kong model and maps each command group path to its
// sorted child names ("" is the top level).
func commandTree(t *testing.T) map[string][]string {
	t.Helper()

	var c CLI
	parser, err := kong.New(&c, kong.Vars{"version": "test"})
	if err != nil {
		t.Fatalf("building kong model: %v", err)
	}

	tree := map[string][]string{}
	var walk func(path string, nodes []*kong.Node)
	walk = func(path string, nodes []*kong.Node) {
		for _, n := range nodes {
			// Hidden commands are internal callbacks; the scripts must not
			// offer them.
			if n.Type != kong.CommandNode || n.Hidden {
				continue
			}
			tree[path] = append(tree[path], n.Name)
			child := n.Name
			if path != "" {
				child = path + " " + n.Name
			}
			walk(child, n.Children)
		}
	}
	walk("", parser.Model.Children)
	for _, names := range tree {
		slices.Sort(names)
	}
	if len(tree[""]) < 5 {
		t.Fatalf("model walk found only %d top-level commands. The walk is broken", len(tree[""]))
	}
	return tree
}

// TestBashCompletionTracksCommandTree compares the bash script's word-list
// variables with the Kong model as exact sets.
func TestBashCompletionTracksCommandTree(t *testing.T) {
	tree := commandTree(t)

	// commands="setup auth ..." / host_commands="list get ..." lines.
	varRe := regexp.MustCompile(`(?m)^\s*local (\w+)="([^"]*)"`)
	scriptVars := map[string][]string{}
	for _, m := range varRe.FindAllStringSubmatch(bashCompletionScript, -1) {
		words := strings.Fields(m[2])
		slices.Sort(words)
		scriptVars[m[1]] = words
	}

	for path, want := range tree {
		varName := "commands"
		if path != "" {
			// Hyphenated commands (machine-type) map to underscores: a shell
			// variable name cannot carry the hyphen.
			varName = strings.NewReplacer(" ", "_", "-", "_").Replace(path) + "_commands"
		}
		got, ok := scriptVars[varName]
		if !ok {
			t.Errorf("bash script has no %s variable for command group %q", varName, path)
			continue
		}
		if !slices.Equal(got, want) {
			t.Errorf("bash %s = %v, want %v. Update completion.go", varName, got, want)
		}
	}
}

// TestCompletionScriptsTrackConfigKeys checks that the three hand-written
// settings lists match configSettings exactly and in order.
func TestCompletionScriptsTrackConfigKeys(t *testing.T) {
	want := make([]string, 0, len(configSettings()))
	for _, s := range configSettings() {
		want = append(want, s.key)
	}

	for _, script := range []struct {
		shell string
		re    *regexp.Regexp
	}{
		{"bash", regexp.MustCompile(`(?m)^\s*local admin_config_keys="([^"]*)"`)},
		// Anchored on the config branch so a restructured script fails here
		// instead of matching another compadd.
		{"zsh", regexp.MustCompile(`"config" \]\]; then\n\s*compadd -- ([^\n]*)`)},
		{"fish", regexp.MustCompile(`__fish_cfctl_config_key_position' -a '([^']*)'`)},
	} {
		source := map[string]string{
			"bash": bashCompletionScript,
			"zsh":  zshCompletionScript,
			"fish": fishCompletionScript,
		}[script.shell]
		m := script.re.FindStringSubmatch(source)
		if m == nil {
			t.Errorf("%s completion script has no config-key list where the gate reads one", script.shell)
			continue
		}
		if got := strings.Fields(m[1]); !slices.Equal(got, want) {
			t.Errorf("%s config keys = %v, want %v. Update completion.go", script.shell, got, want)
		}
	}
}

// TestZshFishCompletionsTrackCommandTree checks that every command appears with
// its shell-specific anchor ('name: for zsh, -a name for fish).
func TestZshFishCompletionsTrackCommandTree(t *testing.T) {
	tree := commandTree(t)

	for path, names := range tree {
		for _, name := range names {
			if !strings.Contains(zshCompletionScript, "'"+name+":") {
				t.Errorf("zsh completion script is missing %q (group %q). Update completion.go", name, path)
			}
			if !strings.Contains(fishCompletionScript, "-a "+name+" ") {
				t.Errorf("fish completion script is missing %q (group %q). Update completion.go", name, path)
			}
		}
	}
}
