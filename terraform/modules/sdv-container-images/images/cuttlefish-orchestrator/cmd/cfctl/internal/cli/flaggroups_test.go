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

	"github.com/alecthomas/kong"
)

// deviceCreateNode resolves device create in the production parser, where
// group titles and descriptions come from kong.ExplicitGroups.
func deviceCreateNode(t *testing.T) *kong.Node {
	t.Helper()
	var c CLI
	parser, err := NewParser(&c, nil, "test")
	if err != nil {
		t.Fatalf("building kong model: %v", err)
	}
	for _, top := range parser.Model.Children {
		if top.Name != "device" {
			continue
		}
		for _, sub := range top.Children {
			if sub.Name == "create" {
				return sub
			}
		}
	}
	t.Fatal("device create not found in the kong model")
	return nil
}

// TestDeviceCreateFlagsAreGrouped requires device create's mode-specific flags
// to sit under titled Kong groups.
func TestDeviceCreateFlagsAreGrouped(t *testing.T) {
	create := deviceCreateNode(t)

	wantGroup := map[string]string{
		"host":         "Imperative placement",
		"interaction":  "Declarative placement",
		"gpu-required": "Declarative placement",
		"region":       "Declarative placement",
		"capacity":     "Declarative placement",
		"pool":         "Declarative placement",
		"architecture": "Declarative placement",
		"build-id":     "Build source",
		"build-target": "Build source",
		"track-branch": "Build source",
		"track-target": "Build source",
		"local-image":  "Build source",
		"product-out":  "Build source",
		"host-package": "Build source",
	}
	for _, f := range create.Flags {
		want, grouped := wantGroup[f.Name]
		var got string
		if f.Group != nil {
			got = f.Group.Title
		}
		switch {
		case grouped && !strings.HasPrefix(got, want):
			t.Errorf("--%s: group %q, want prefix %q", f.Name, got, want)
		case !grouped && got != "":
			t.Errorf("--%s: unexpectedly grouped under %q. Add it to this test's map", f.Name, got)
		}
		delete(wantGroup, f.Name)
	}
	for name := range wantGroup {
		t.Errorf("--%s: flag gone from device create. Update this test", name)
	}
}

// The placement and build-source groups need a description, because a title
// cannot say when each mode applies.
func TestDeviceCreateGroupsExplainThemselves(t *testing.T) {
	for _, f := range deviceCreateNode(t).Flags {
		if f.Group == nil {
			continue
		}
		if strings.Contains(f.Group.Title, "placement") || strings.HasPrefix(f.Group.Title, "Build source") {
			if f.Group.Description == "" {
				t.Errorf("group %q has no description", f.Group.Title)
			}
		}
	}
}
