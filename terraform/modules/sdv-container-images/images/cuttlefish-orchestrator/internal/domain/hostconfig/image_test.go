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

package hostconfig_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// The reason enum tag is a second copy of ImageReasons, and the tag is what
// the published schema states.
func TestImageStatusReasonTagMatchesTheReasons(t *testing.T) {
	t.Parallel()
	field, ok := reflect.TypeFor[hostconfig.ImageStatus]().FieldByName("Reason")
	if !ok {
		t.Fatal("ImageStatus has no Reason field")
	}
	tag := strings.Split(field.Tag.Get("enum"), ",")
	want := make([]string, 0, len(hostconfig.ImageReasons()))
	for _, r := range hostconfig.ImageReasons() {
		want = append(want, string(r))
	}
	if !slices.Equal(tag, want) {
		t.Errorf("enum tag %v, want %v", tag, want)
	}
}

func TestArchitectureMatches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		machine, image machinetype.Architecture
		want           bool
	}{
		{machinetype.ArchArm, machinetype.ArchArm, true},
		{machinetype.ArchArm, machinetype.ArchX86, false},
		{machinetype.ArchArm, "", false},
		{machinetype.ArchX86, machinetype.ArchX86, true},
		{machinetype.ArchX86, "", true},
		{machinetype.ArchX86, machinetype.ArchArm, false},
		{"", machinetype.ArchArm, true},
	}
	for _, tc := range tests {
		if got := hostconfig.ArchitectureMatches(tc.machine, tc.image); got != tc.want {
			t.Errorf("ArchitectureMatches(%q, %q) = %v, want %v", tc.machine, tc.image, got, tc.want)
		}
	}
}

func TestEffectiveProject(t *testing.T) {
	t.Parallel()
	if got := hostconfig.EffectiveProject(&hostconfig.HostImageConfig{SourceProject: "row"}, "deploy"); got != "row" {
		t.Errorf("row override = %q, want row", got)
	}
	if got := hostconfig.EffectiveProject(&hostconfig.HostImageConfig{}, "deploy"); got != "deploy" {
		t.Errorf("no override = %q, want deploy", got)
	}
	if got := hostconfig.EffectiveProject(nil, "deploy"); got != "deploy" {
		t.Errorf("no row = %q, want deploy", got)
	}
}
