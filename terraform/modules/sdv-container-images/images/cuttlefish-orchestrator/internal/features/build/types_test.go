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

package buildfeature

import (
	"testing"
)

// A PUT replacing artifact specs must fail at the API edge for everything
// build.Build.Validate would reject. Otherwise the service's re-validation
// surfaces user input as a 500.
func TestUpdateBuildInputResolve(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		specs    []ArtifactSpecInput
		wantErrs int
	}{
		{"absent specs pass", nil, 0},
		{"empty replacement rejected", []ArtifactSpecInput{}, 1},
		{"gcs url prefix mismatch", []ArtifactSpecInput{
			{Type: "gcs", URL: "https://bucket/obj", Role: "host_package"},
		}, 1},
		{"https url prefix mismatch", []ArtifactSpecInput{
			{Type: "https", URL: "gs://bucket/obj", Role: "host_package"},
		}, 1},
		{"each bad spec reported", []ArtifactSpecInput{
			{Type: "gcs", URL: "https://a/b", Role: "host_package"},
			{Type: "https", URL: "gs://a/b", Role: "default_build"},
		}, 2},
		{"valid replacement passes", []ArtifactSpecInput{
			{Type: "gcs", URL: "gs://bucket/obj", Role: "host_package"},
			{Type: "https", URL: "https://host/obj", Role: "default_build"},
		}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := &UpdateBuildInput{ID: "b1"}
			input.Body.ArtifactSpecs = tc.specs
			if got := input.Resolve(nil); len(got) != tc.wantErrs {
				t.Errorf("Resolve returned %d errors (%v), want %d", len(got), got, tc.wantErrs)
			}
		})
	}
}
