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
	"testing"

	"github.com/alecthomas/kong"
)

// TestEnumFlagsDocumentTheirVocabulary requires every enum flag's help text
// to name each of its values. Kong shows only the help string.
func TestEnumFlagsDocumentTheirVocabulary(t *testing.T) {
	var c CLI
	parser, err := kong.New(&c, kong.Vars{"version": "test"})
	if err != nil {
		t.Fatalf("building kong model: %v", err)
	}

	enumFlags := 0
	var walk func(path string, node *kong.Node)
	walk = func(path string, node *kong.Node) {
		for _, f := range node.Flags {
			if f.Enum == "" {
				continue
			}
			enumFlags++
			for _, v := range f.EnumSlice() {
				// The empty value only marks the filter as optional.
				if v == "" {
					continue
				}
				re := regexp.MustCompile(`\b` + regexp.QuoteMeta(v) + `\b`)
				if !re.MatchString(f.Help) {
					t.Errorf("%s --%s: help does not mention enum value %q\nhelp: %s",
						path, f.Name, v, f.Help)
				}
			}
		}
		for _, child := range node.Children {
			if child.Type != kong.CommandNode {
				continue
			}
			walk(path+" "+child.Name, child)
		}
	}
	walk("cfctl", parser.Model.Node)

	if enumFlags < 5 {
		t.Fatalf("model walk found only %d enum flags. The walk is broken", enumFlags)
	}
}
