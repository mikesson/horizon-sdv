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

package hostconfig

import (
	"testing"
)

func TestValidateMachineFamily(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		family  string
		wantErr bool
	}{
		{name: "valid n1", family: "n1", wantErr: false},
		{name: "valid n2d", family: "n2d", wantErr: false},
		{name: "valid c4a", family: "c4a", wantErr: false},
		{name: "valid g2", family: "g2", wantErr: false},
		{name: "valid a3", family: "a3", wantErr: false},
		{name: "max length", family: "abcdefghij", wantErr: false},
		{name: "empty", family: "", wantErr: true},
		{name: "uppercase", family: "N1", wantErr: true},
		{name: "starts with number", family: "2n", wantErr: true},
		{name: "too long", family: "abcdefghijk", wantErr: true},
		{name: "contains hyphen", family: "n-1", wantErr: true},
		{name: "contains underscore", family: "n_1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMachineFamily(tt.family)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateMachineFamily(%q) error = %v, wantErr %v", tt.family, err, tt.wantErr)
			}
		})
	}
}

func TestValidateImageFamily(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		family  string
		wantErr bool
	}{
		{name: "valid cco-host", family: "cco-host", wantErr: false},
		{name: "valid simple", family: "cuttlefish", wantErr: false},
		{name: "valid with numbers", family: "cco-host-v2", wantErr: false},
		{name: "valid single letter", family: "a", wantErr: false},
		{name: "empty", family: "", wantErr: true},
		{name: "starts with hyphen", family: "-cco-host", wantErr: true},
		{name: "ends with hyphen", family: "cco-host-", wantErr: true},
		{name: "consecutive hyphens", family: "cco--host", wantErr: true},
		{name: "uppercase", family: "CCO-HOST", wantErr: true},
		{name: "starts with number", family: "2cco-host", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateImageFamily(tt.family)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateImageFamily(%q) error = %v, wantErr %v", tt.family, err, tt.wantErr)
			}
		})
	}
}

func TestValidateSourceProject(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		project string
		wantErr bool
	}{
		{name: "valid project", project: "cloud-android-cuttlefish", wantErr: false},
		{name: "valid short project", project: "my-proj", wantErr: false},
		{name: "empty is valid", project: "", wantErr: false},
		{name: "too short", project: "abc", wantErr: true},
		{name: "uppercase", project: "My-Project", wantErr: true},
		{name: "starts with number", project: "1project", wantErr: true},
		{name: "ends with hyphen", project: "project-", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSourceProject(tt.project)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSourceProject(%q) error = %v, wantErr %v", tt.project, err, tt.wantErr)
			}
		})
	}
}
