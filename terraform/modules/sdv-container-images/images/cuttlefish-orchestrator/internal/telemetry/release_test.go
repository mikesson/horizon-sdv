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

package telemetry

import (
	"runtime/debug"
	"strings"
	"testing"
)

// The GCP detector and the SDK each stamp a semconv schema URL on the
// resource, and resource.New refuses to merge two different ones, which
// disables all telemetry. Off GCP the detector returns nothing, so only Cloud
// Run shows the conflict. A contrib release is built against the SDK release
// with the same minor version, which keeps the two URLs equal.
func TestGCPDetectorMatchesSDKRelease(t *testing.T) {
	t.Parallel()
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("the test binary carries no build info")
	}
	versions := map[string]string{}
	for _, dep := range info.Deps {
		versions[dep.Path] = dep.Version
	}
	detector := versions["go.opentelemetry.io/contrib/detectors/gcp"]
	sdk := versions["go.opentelemetry.io/otel/sdk"]
	if detector == "" || sdk == "" {
		t.Fatalf("build info lacks a version: detectors/gcp %q, otel/sdk %q", detector, sdk)
	}
	if majorMinor(detector) != majorMinor(sdk) {
		t.Errorf("contrib/detectors/gcp %s and otel/sdk %s are different releases; move them together", detector, sdk)
	}
}

// majorMinor returns "v1.45" for "v1.45.0".
func majorMinor(version string) string {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return version
	}
	return parts[0] + "." + parts[1]
}
