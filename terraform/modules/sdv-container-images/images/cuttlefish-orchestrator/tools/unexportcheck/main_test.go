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

package main

import (
	"slices"
	"strings"
	"testing"
)

// analyzeFixture analyzes the fixture module in testdata/mod and returns the two buckets
// as sorted "pkg.Name" lists.
func analyzeFixture(t *testing.T, opts options) (dead, over []string) {
	t.Helper()
	d, o, err := analyze("testdata/mod", []string{"./..."}, opts)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return idents(d), idents(o)
}

func idents(ds []*decl) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.ident())
	}
	slices.Sort(out)
	return out
}

func TestDeadBucket(t *testing.T) {
	t.Parallel()
	dead, _ := analyzeFixture(t, options{})

	want := []string{
		"dead.ColourBlue",  // iota, unreferenced
		"dead.ColourGreen", // iota, unreferenced
		"dead.DeadConst",
		"dead.DeadType",
		"dead.DeadVar",
		"dead.WeekdayMonday",
		"dead.WeekdayTuesday",
	}
	if !slices.Equal(dead, want) {
		t.Errorf("dead bucket:\n got %v\nwant %v", dead, want)
	}
}

// TestLiveNamesAreNotReported pins the three ways a name stays alive: a
// cross-package reference, a reference from a test, and a reference from
// elsewhere in the declaring package.
func TestLiveNamesAreNotReported(t *testing.T) {
	t.Parallel()
	dead, _ := analyzeFixture(t, options{})

	for _, name := range []string{
		"dead.LiveType",         // used from package live
		"dead.LiveConst",        // used from package live
		"dead.ColourRed",        // used from package live
		"dead.TestOnlyType",     // used only from dead_test.go
		"dead.PackageLocalType", // used only within package dead
	} {
		if slices.Contains(dead, name) {
			t.Errorf("%s is referenced but was reported dead", name)
		}
	}
}

// TestIotaLabel guards the distinction the report depends on: a positional
// constant cannot simply be deleted, a grouped string constant can. Labeling
// every grouped constant iota (an easy shortcut) would fail this.
func TestIotaLabel(t *testing.T) {
	t.Parallel()
	d, _, err := analyze("testdata/mod", []string{"./..."}, options{})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}

	want := map[string]bool{
		"dead.ColourGreen":    true,
		"dead.ColourBlue":     true,
		"dead.WeekdayMonday":  false,
		"dead.WeekdayTuesday": false,
		"dead.DeadConst":      false,
	}
	for _, decl := range d {
		if expect, ok := want[decl.ident()]; ok && decl.iota != expect {
			t.Errorf("%s: iota = %v, want %v", decl.ident(), decl.iota, expect)
		}
	}
}

func TestGeneratedFilesSkippedByDefault(t *testing.T) {
	t.Parallel()

	dead, _ := analyzeFixture(t, options{})
	for _, name := range dead {
		if strings.HasPrefix(name, "gen.") {
			t.Errorf("generated declaration %s reported without -generated", name)
		}
	}

	withGen, _ := analyzeFixture(t, options{includeGenerated: true})
	for _, name := range []string{"gen.GeneratedDeadType", "gen.GeneratedDeadVar"} {
		if !slices.Contains(withGen, name) {
			t.Errorf("-generated did not report %s (got %v)", name, withGen)
		}
	}
}

func TestOverExportedBucket(t *testing.T) {
	t.Parallel()

	_, off := analyzeFixture(t, options{})
	if len(off) != 0 {
		t.Errorf("over-exported bucket should be empty without -over, got %v", off)
	}

	_, on := analyzeFixture(t, options{includeOver: true})
	if !slices.Contains(on, "dead.PackageLocalType") {
		t.Errorf("-over did not report dead.PackageLocalType (got %v)", on)
	}
	if slices.Contains(on, "dead.LiveType") {
		t.Error("dead.LiveType has a cross-package use and must not be over-exported")
	}
}

func TestAllowlistSuppresses(t *testing.T) {
	t.Parallel()

	dead, _ := analyzeFixture(t, options{allow: map[string]bool{"dead.DeadType": true}})
	if slices.Contains(dead, "dead.DeadType") {
		t.Error("allowlisted dead.DeadType was still reported")
	}
	if !slices.Contains(dead, "dead.DeadVar") {
		t.Error("allowlist suppressed more than the named identifier")
	}
}
