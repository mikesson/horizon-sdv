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

package validation

import (
	"slices"
	"strings"
	"testing"
)

func buildRefConfig(instances ...map[string]any) map[string]any {
	raw := make([]any, len(instances))
	for i, in := range instances {
		raw[i] = in
	}
	return map[string]any{"instances": raw}
}

func TestValidateEnvConfig_BuildRef(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		config  map[string]any
		wantErr string
	}{
		{
			name:   "registered build id",
			config: buildRefConfig(map[string]any{"name": "core", BuildKey: "b-core"}),
		},
		{
			name: "build id beside other disk fields",
			config: buildRefConfig(map[string]any{
				"name": "core", BuildKey: "b-core",
				"disk": map[string]any{"super": map[string]any{"system": "gs://b/system.img"}},
			}),
		},
		{
			name:    "empty build id",
			config:  buildRefConfig(map[string]any{"name": "core", BuildKey: ""}),
			wantErr: "instances[0].@build must be a non-empty build id",
		},
		{
			name:    "build id not a string",
			config:  buildRefConfig(map[string]any{"name": "core", BuildKey: 42}),
			wantErr: "instances[0].@build must be a non-empty build id",
		},
		{
			name: "build id together with default_build",
			config: buildRefConfig(
				map[string]any{"name": "core", BuildKey: "b-core"},
				map[string]any{
					"name": "ivi", BuildKey: "b-ivi",
					"disk": map[string]any{"default_build": "gs://b/ivi-img.zip"},
				},
			),
			wantErr: "instances[1]: set @build or disk.default_build, not both",
		},
		{
			name: "build id together with an empty default_build",
			config: buildRefConfig(map[string]any{
				"name": "core", BuildKey: "b-core",
				"disk": map[string]any{"default_build": ""},
			}),
			wantErr: "not both",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEnvConfig(tc.config)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateEnvConfig = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateEnvConfig = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestEveryInstanceHasBuild(t *testing.T) {
	t.Parallel()
	withDisk := func(db string) map[string]any {
		return map[string]any{"name": "d", "disk": map[string]any{"default_build": db}}
	}
	withRef := func(id string) map[string]any {
		return map[string]any{"name": "d", BuildKey: id}
	}
	bare := func() map[string]any { return map[string]any{"name": "d"} }

	cases := []struct {
		name   string
		config map[string]any
		want   bool
	}{
		{"nil", nil, false},
		{"no instances key", map[string]any{}, false},
		{"empty instances", buildRefConfig(), false},
		{"instance without disk", buildRefConfig(bare()), false},
		{"empty default_build", buildRefConfig(withDisk("")), false},
		{"empty build ref", buildRefConfig(withRef("")), false},
		{"one instance with default_build", buildRefConfig(withDisk("@ab/123/target")), true},
		{"one instance with build ref", buildRefConfig(withRef("b-1")), true},
		{"pair, both by default_build", buildRefConfig(withDisk("gs://b/core.zip"), withDisk("gs://b/ivi.zip")), true},
		{"pair, mixed sources", buildRefConfig(withRef("b-core"), withDisk("gs://b/ivi.zip")), true},
		{"pair, second has none", buildRefConfig(withRef("b-core"), bare()), false},
		{"pair, first has none", buildRefConfig(bare(), withDisk("@ab/1/t")), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EveryInstanceHasBuild(tc.config); got != tc.want {
				t.Errorf("EveryInstanceHasBuild = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInstanceBuildRefs(t *testing.T) {
	t.Parallel()
	if got := InstanceBuildRefs(nil); got != nil {
		t.Errorf("InstanceBuildRefs(nil) = %v, want nil", got)
	}
	none := buildRefConfig(map[string]any{"disk": map[string]any{"default_build": "gs://b/x.zip"}})
	if got := InstanceBuildRefs(none); got != nil {
		t.Errorf("InstanceBuildRefs(no refs) = %v, want nil", got)
	}
	cfg := buildRefConfig(
		map[string]any{"name": "a"},
		map[string]any{"name": "b", BuildKey: "b-ivi"},
		map[string]any{"name": "c", BuildKey: "b-core"},
	)
	want := []string{"", "b-ivi", "b-core"}
	if got := InstanceBuildRefs(cfg); !slices.Equal(got, want) {
		t.Errorf("InstanceBuildRefs = %q, want %q", got, want)
	}
}

// The filter is a security pass, and @build is not a blocked field: dropping it
// would silently swap a pair's per-instance builds for the device-level one.
func TestFilterBlockedFields_KeepsBuildRef(t *testing.T) {
	t.Parallel()
	cfg := buildRefConfig(map[string]any{"name": "core", BuildKey: "b-core"})
	got := FilterBlockedFields(cfg)
	if refs := InstanceBuildRefs(got); !slices.Equal(refs, []string{"b-core"}) {
		t.Errorf("FilterBlockedFields dropped @build: refs = %q", refs)
	}
}
