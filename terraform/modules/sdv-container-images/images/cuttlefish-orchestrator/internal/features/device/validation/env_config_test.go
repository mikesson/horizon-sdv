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

func TestValidateEnvConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  map[string]any
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
			errMsg:  "cannot be nil",
		},
		{
			name:    "missing instances",
			config:  map[string]any{},
			wantErr: true,
			errMsg:  "must have an 'instances' field",
		},
		{
			name: "instances not an array",
			config: map[string]any{
				"instances": "not-an-array",
			},
			wantErr: true,
			errMsg:  "must be an array",
		},
		{
			name: "empty instances array",
			config: map[string]any{
				"instances": []any{},
			},
			wantErr: true,
			errMsg:  "at least one instance",
		},
		{
			name: "valid single instance",
			config: map[string]any{
				"instances": []any{
					map[string]any{
						"name": "cvd-1",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid multiple instances",
			config: map[string]any{
				"instances": []any{
					map[string]any{"name": "cvd-1"},
					map[string]any{"name": "cvd-2"},
					map[string]any{"name": "cvd-3"},
				},
			},
			wantErr: false,
		},
		{
			name: "too many instances",
			config: map[string]any{
				"instances": []any{
					map[string]any{"name": "cvd-1"},
					map[string]any{"name": "cvd-2"},
					map[string]any{"name": "cvd-3"},
					map[string]any{"name": "cvd-4"},
					map[string]any{"name": "cvd-5"},
					map[string]any{"name": "cvd-6"},
					map[string]any{"name": "cvd-7"},
					map[string]any{"name": "cvd-8"},
					map[string]any{"name": "cvd-9"},
				},
			},
			wantErr: true,
			errMsg:  "cannot have more than 8 instances",
		},
		{
			name: "instance not an object",
			config: map[string]any{
				"instances": []any{
					"not-an-object",
				},
			},
			wantErr: true,
			errMsg:  "instances[0] must be an object",
		},
		{
			name: "too many displays",
			config: map[string]any{
				"instances": []any{
					map[string]any{
						"name": "cvd-1",
						"graphics": map[string]any{
							"displays": []any{
								map[string]any{"width": 1920},
								map[string]any{"width": 1920},
								map[string]any{"width": 1920},
								map[string]any{"width": 1920},
								map[string]any{"width": 1920},
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "cannot have more than 4 displays",
		},
		{
			name: "valid displays count",
			config: map[string]any{
				"instances": []any{
					map[string]any{
						"name": "cvd-1",
						"graphics": map[string]any{
							"displays": []any{
								map[string]any{"width": 1920},
								map[string]any{"width": 1920},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "unknown import template",
			config: map[string]any{
				"instances": []any{
					map[string]any{"name": "cvd-1", "@import": "desktop"},
				},
			},
			wantErr: true,
			errMsg:  `@import: unknown template "desktop"`,
		},
		{
			name: "empty import template",
			config: map[string]any{
				"instances": []any{
					map[string]any{"name": "cvd-1", "@import": ""},
				},
			},
			wantErr: true,
			errMsg:  "@import: unknown template",
		},
		{
			name: "import template not a string",
			config: map[string]any{
				"instances": []any{
					map[string]any{"name": "cvd-1", "@import": 7},
				},
			},
			wantErr: true,
			errMsg:  "@import must be a string",
		},
		{
			name: "import template on the second instance",
			config: map[string]any{
				"instances": []any{
					map[string]any{"name": "cvd-1"},
					map[string]any{"name": "cvd-2", "@import": "nosuch"},
				},
			},
			wantErr: true,
			errMsg:  "instances[1].@import",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEnvConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateEnvConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("ValidateEnvConfig() error = %v, want error containing %q", err, tt.errMsg)
			}
		})
	}
}

func TestValidateEnvConfig_BlockedPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  map[string]any
		wantErr bool
		errMsg  string
	}{
		{
			name: "local kernel path blocked",
			config: map[string]any{
				"instances": []any{
					map[string]any{
						"boot": map[string]any{
							"kernel": map[string]any{
								"build": "/local/path/kernel",
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "boot.kernel.build: local paths not allowed",
		},
		{
			name: "local bootloader path blocked",
			config: map[string]any{
				"instances": []any{
					map[string]any{
						"boot": map[string]any{
							"bootloader": map[string]any{
								"build": "/local/path/bootloader",
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "boot.bootloader.build: local paths not allowed",
		},
		{
			name: "local efi loader path blocked",
			config: map[string]any{
				"instances": []any{
					map[string]any{
						"boot": map[string]any{
							"android_efi_loader": map[string]any{
								"build": "/opt/efi/loader",
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "boot.android_efi_loader.build: local paths not allowed",
		},
		{
			name: "local otatools path blocked",
			config: map[string]any{
				"instances": []any{
					map[string]any{
						"disk": map[string]any{
							"otatools": "/opt/android/otatools",
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "disk.otatools: local paths not allowed",
		},
		{
			name: "v4l2_proxy blocked",
			config: map[string]any{
				"instances": []any{
					map[string]any{
						"vm": map[string]any{
							"crosvm": map[string]any{
								"v4l2_proxy": "/dev/video0",
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "vm.crosvm.v4l2_proxy: device access not allowed",
		},
		{
			name: "local host_package blocked",
			config: map[string]any{
				"common": map[string]any{
					"host_package": "/local/host_package.tar.gz",
				},
				"instances": []any{
					map[string]any{"name": "cvd-1"},
				},
			},
			wantErr: true,
			errMsg:  "common.host_package: local paths not allowed",
		},
		{
			name: "ab reference allowed for kernel",
			config: map[string]any{
				"instances": []any{
					map[string]any{
						"boot": map[string]any{
							"kernel": map[string]any{
								"build": "@ab/git_main/cf_x86_64_phone",
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "https URL allowed",
			config: map[string]any{
				"common": map[string]any{
					"host_package": "https://storage.googleapis.com/bucket/host.tar.gz",
				},
				"instances": []any{
					map[string]any{"name": "cvd-1"},
				},
			},
			wantErr: false,
		},
		{
			name: "gs URL allowed",
			config: map[string]any{
				"common": map[string]any{
					"host_package": "gs://bucket/host.tar.gz",
				},
				"instances": []any{
					map[string]any{"name": "cvd-1"},
				},
			},
			wantErr: false,
		},
		{
			name: "cleartext http host_package blocked",
			config: map[string]any{
				"common": map[string]any{
					"host_package": "http://storage.googleapis.com/bucket/host.tar.gz",
				},
				"instances": []any{
					map[string]any{"name": "cvd-1"},
				},
			},
			wantErr: true,
			errMsg:  "cleartext http://",
		},
		{
			name: "cleartext http default_build blocked",
			config: map[string]any{
				"instances": []any{
					map[string]any{
						"disk": map[string]any{
							"default_build": "http://mirror.example.com/x-img-1.zip",
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "cleartext http://",
		},
		{
			name: "relative path blocked",
			config: map[string]any{
				"instances": []any{
					map[string]any{
						"boot": map[string]any{
							"kernel": map[string]any{
								"build": "./kernel",
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "local paths not allowed",
		},
		{
			name: "parent path blocked",
			config: map[string]any{
				"instances": []any{
					map[string]any{
						"boot": map[string]any{
							"kernel": map[string]any{
								"build": "../kernel",
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "local paths not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEnvConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateEnvConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("ValidateEnvConfig() error = %v, want error containing %q", err, tt.errMsg)
			}
		})
	}
}

func TestFilterBlockedFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		config   map[string]any
		checkFn  func(result map[string]any) bool
		checkMsg string
	}{
		{
			name:   "nil config returns nil",
			config: nil,
			checkFn: func(result map[string]any) bool {
				return result == nil
			},
			checkMsg: "expected nil result",
		},
		{
			name: "removes local kernel path",
			config: map[string]any{
				"instances": []any{
					map[string]any{
						"name": "cvd-1",
						"boot": map[string]any{
							"kernel": map[string]any{
								"build": "/local/kernel",
							},
						},
					},
				},
			},
			checkFn: func(result map[string]any) bool {
				instances := result["instances"].([]any)
				inst := instances[0].(map[string]any)
				boot := inst["boot"].(map[string]any)
				kernel := boot["kernel"].(map[string]any)
				_, hasBuild := kernel["build"]
				return !hasBuild
			},
			checkMsg: "expected kernel.build to be removed",
		},
		{
			name: "keeps ab reference",
			config: map[string]any{
				"instances": []any{
					map[string]any{
						"name": "cvd-1",
						"boot": map[string]any{
							"kernel": map[string]any{
								"build": "@ab/git_main/cf_x86_64",
							},
						},
					},
				},
			},
			checkFn: func(result map[string]any) bool {
				instances := result["instances"].([]any)
				inst := instances[0].(map[string]any)
				boot := inst["boot"].(map[string]any)
				kernel := boot["kernel"].(map[string]any)
				build, ok := kernel["build"]
				return ok && build == "@ab/git_main/cf_x86_64"
			},
			checkMsg: "expected kernel.build to be preserved",
		},
		{
			name: "removes v4l2_proxy",
			config: map[string]any{
				"instances": []any{
					map[string]any{
						"name": "cvd-1",
						"vm": map[string]any{
							"crosvm": map[string]any{
								"v4l2_proxy":   "/dev/video0",
								"other_option": "keep-me",
							},
						},
					},
				},
			},
			checkFn: func(result map[string]any) bool {
				instances := result["instances"].([]any)
				inst := instances[0].(map[string]any)
				vm := inst["vm"].(map[string]any)
				crosvm := vm["crosvm"].(map[string]any)
				_, hasV4l2 := crosvm["v4l2_proxy"]
				other, hasOther := crosvm["other_option"]
				return !hasV4l2 && hasOther && other == "keep-me"
			},
			checkMsg: "expected v4l2_proxy removed but other options preserved",
		},
		{
			name: "removes local host_package",
			config: map[string]any{
				"common": map[string]any{
					"host_package": "/local/host.tar.gz",
					"group_name":   "test-group",
				},
				"instances": []any{
					map[string]any{"name": "cvd-1"},
				},
			},
			checkFn: func(result map[string]any) bool {
				common := result["common"].(map[string]any)
				_, hasHostPkg := common["host_package"]
				groupName, hasGroup := common["group_name"]
				return !hasHostPkg && hasGroup && groupName == "test-group"
			},
			checkMsg: "expected host_package removed but group_name preserved",
		},
		{
			name: "deep copy does not mutate original",
			config: map[string]any{
				"instances": []any{
					map[string]any{
						"name": "cvd-1",
						"vm": map[string]any{
							"crosvm": map[string]any{
								"v4l2_proxy": "/dev/video0",
							},
						},
					},
				},
			},
			checkFn: func(result map[string]any) bool {
				return result != nil
			},
			checkMsg: "result should not be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterBlockedFields(tt.config)
			if !tt.checkFn(result) {
				t.Errorf("FilterBlockedFields(): %s", tt.checkMsg)
			}
		})
	}

	// Verify deep copy doesn't mutate original
	t.Run("deep copy immutability", func(t *testing.T) {
		original := map[string]any{
			"instances": []any{
				map[string]any{
					"vm": map[string]any{
						"crosvm": map[string]any{
							"v4l2_proxy": "/dev/video0",
						},
					},
				},
			},
		}

		FilterBlockedFields(original)

		// Original should still have v4l2_proxy
		instances := original["instances"].([]any)
		inst := instances[0].(map[string]any)
		vm := inst["vm"].(map[string]any)
		crosvm := vm["crosvm"].(map[string]any)
		if _, ok := crosvm["v4l2_proxy"]; !ok {
			t.Error("FilterBlockedFields mutated original config")
		}
	})
}

func TestCountInstances(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		config map[string]any
		want   int
	}{
		{
			name:   "nil config",
			config: nil,
			want:   0,
		},
		{
			name:   "missing instances",
			config: map[string]any{},
			want:   0,
		},
		{
			name: "instances not array",
			config: map[string]any{
				"instances": "not-an-array",
			},
			want: 0,
		},
		{
			name: "single instance",
			config: map[string]any{
				"instances": []any{
					map[string]any{"name": "cvd-1"},
				},
			},
			want: 1,
		},
		{
			name: "multiple instances",
			config: map[string]any{
				"instances": []any{
					map[string]any{"name": "cvd-1"},
					map[string]any{"name": "cvd-2"},
					map[string]any{"name": "cvd-3"},
				},
			},
			want: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CountInstances(tt.config)
			if got != tt.want {
				t.Errorf("CountInstances() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsLocalPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		want bool
	}{
		{"", false},
		{"@ab/git_main/cf_x86_64", false},
		{"https://example.com/file.zip", false},
		{"gs://bucket/file.zip", false},
		{"/absolute/local/path", true},
		{"./relative/path", true},
		{"../parent/path", true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isLocalPath(tt.path)
			if got != tt.want {
				t.Errorf("isLocalPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// TestValidateEnvConfig_ImportTemplates checks every name cvd's parser
// resolves is accepted. The list is a second copy of upstream's map; a name
// missing here rejects a create the host would have served.
func TestValidateEnvConfig_ImportTemplates(t *testing.T) {
	t.Parallel()
	want := []string{"phone", "tablet", "tv", "wearable", "auto", "slim", "go", "foldable"}
	if got := ImportTemplateNames(); !slices.Equal(got, want) {
		t.Fatalf("ImportTemplateNames() = %v, want %v", got, want)
	}
	for _, name := range want {
		config := map[string]any{
			"instances": []any{map[string]any{"name": "cvd-1", "@import": name}},
		}
		if err := ValidateEnvConfig(config); err != nil {
			t.Errorf("ValidateEnvConfig(@import %q) = %v, want nil", name, err)
		}
	}
}

// TestValidateEnvConfig_InstanceNames covers the grammar cvd applies to a
// caller-chosen instance name, which is also what makes the name safe as a URL
// path segment on CCO's instance-addressed routes.
//
// The accepted cases are taken from cvd's own implementation rather than from
// the comment above it: instances/device_name.cpp splits on "-" and requires
// each piece to match [A-Za-z_0-9]+, so a trailing multi-character piece like
// "my_phone07" is valid even though the comment's expression rejects it.
func TestValidateEnvConfig_InstanceNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		value   any
		wantErr string
	}{
		{name: "simple", value: "tv"},
		{name: "digits only", value: "0"},
		{name: "underscores and digits", value: "my_phone07"},
		{name: "hyphen joined", value: "ivi-cluster"},
		{name: "empty is left to cvd", value: ""},

		{name: "slash", value: "a/b", wantErr: "not a valid instance name"},
		{name: "percent", value: "a%2fb", wantErr: "not a valid instance name"},
		{name: "dot segment", value: "..", wantErr: "not a valid instance name"},
		{name: "space", value: "my phone", wantErr: "not a valid instance name"},
		{name: "leading hyphen", value: "-a", wantErr: "not a valid instance name"},
		{name: "trailing hyphen", value: "a-", wantErr: "not a valid instance name"},
		{name: "doubled hyphen", value: "a--b", wantErr: "not a valid instance name"},
		{name: "not a string", value: 7, wantErr: "must be a string"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			config := map[string]any{
				"instances": []any{map[string]any{"name": tc.value}},
			}
			err := ValidateEnvConfig(config)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("ValidateEnvConfig(name %v) = %v, want nil", tc.value, err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("ValidateEnvConfig(name %v) = nil, want %q", tc.value, tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("ValidateEnvConfig(name %v) = %v, want %q", tc.value, err, tc.wantErr)
			}
		})
	}
}

// TestValidateEnvConfig_DuplicateInstanceNames rejects two instances answering
// to one path segment. Without this the second is unreachable and the route is
// ambiguous, which is harder to diagnose than a wrong route.
func TestValidateEnvConfig_DuplicateInstanceNames(t *testing.T) {
	t.Parallel()
	config := map[string]any{
		"instances": []any{
			map[string]any{"name": "watch"},
			map[string]any{"name": "phone"},
			map[string]any{"name": "watch"},
		},
	}
	err := ValidateEnvConfig(config)
	if err == nil {
		t.Fatal("ValidateEnvConfig(duplicate names) = nil, want an error")
	}
	if !strings.Contains(err.Error(), "unique within a device") {
		t.Fatalf("ValidateEnvConfig(duplicate names) = %v, want a uniqueness error", err)
	}
	// The message must name both positions; "already used" with no index sends
	// the caller looking through eight instances for the collision.
	if !strings.Contains(err.Error(), "instances[2]") || !strings.Contains(err.Error(), "instances[0]") {
		t.Errorf("error does not name both colliding instances: %v", err)
	}
}

// TestFilterBlockedInstanceFields covers the entry point server-side @import
// expansion uses: it filters one instance map in place, with no surrounding
// env_config to walk.
func TestFilterBlockedInstanceFields(t *testing.T) {
	t.Parallel()
	inst := map[string]any{
		"vm": map[string]any{
			"memory_mb": 4096,
			"crosvm":    map[string]any{"v4l2_proxy": "/dev/video0"},
		},
		"boot": map[string]any{
			"kernel": map[string]any{"build": "/local/kernel"},
		},
	}
	FilterBlockedInstanceFields(inst)

	vm := inst["vm"].(map[string]any)
	if vm["memory_mb"] != 4096 {
		t.Errorf("unblocked field removed: %v", vm)
	}
	if crosvm := vm["crosvm"].(map[string]any); len(crosvm) != 0 {
		t.Errorf("vm.crosvm.v4l2_proxy survived: %v", crosvm)
	}
	if kernel := inst["boot"].(map[string]any)["kernel"].(map[string]any); len(kernel) != 0 {
		t.Errorf("boot.kernel.build survived: %v", kernel)
	}
}
