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

package devicefeature

import (
	"reflect"
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// TestGPUModeEnumTagMatchesDomain binds the OpenAPI enum to the domain
// vocabulary. Huma validates gpu_mode from a struct tag, and a tag cannot
// reference a constant, so the accepted set is written down twice. Nothing but
// this test stops the two copies diverging, and when they diverge the tag
// wins silently. A mode the domain declares is rejected at the edge with a
// validation error that names no cause.
func TestGPUModeEnumTagMatchesDomain(t *testing.T) {
	t.Parallel()

	got := enumTag(t, reflect.TypeFor[CreateDeviceInput](), "Body", "GPUMode")

	want := make([]string, 0, len(device.GPUModes()))
	for _, m := range device.GPUModes() {
		want = append(want, string(m))
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("gpu_mode enum tag = %v, device.GPUModes() = %v\n"+
			"update the enum tag in types.go and GPUModes() in domain/device together", got, want)
	}
}

// TestSnapshotStateEnumTagMatchesDomain binds the published snapshot-state
// enum to the domain vocabulary. The entity serializes directly onto the wire
// (domain-as-transport), so its tag IS the OpenAPI enum, and the console's
// drift gate compares its own vocabulary against exactly that published set.
func TestSnapshotStateEnumTagMatchesDomain(t *testing.T) {
	t.Parallel()

	got := enumTag(t, reflect.TypeFor[device.Snapshot](), "State")

	want := make([]string, 0, len(device.SnapshotStates()))
	for _, s := range device.SnapshotStates() {
		want = append(want, string(s))
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot state enum tag = %v, device.SnapshotStates() = %v\n"+
			"update the enum tag on Snapshot.State and SnapshotStates() together", got, want)
	}
}

// TestArchitectureEnumTagMatchesDomain binds the declarative create's
// architecture enum to the domain vocabulary. Requirements.Validate accepts
// x86, arm and the cleared field; a tag that omits one of them rejects it at
// the edge instead, with a message that names no cause.
func TestArchitectureEnumTagMatchesDomain(t *testing.T) {
	t.Parallel()

	got := enumTag(t, reflect.TypeFor[RequirementsInput](), "Architecture")
	want := []string{string(device.ArchitectureX86), string(device.ArchitectureARM), ""}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("architecture enum tag = %v, domain vocabulary = %v\n"+
			"update the enum tag in types.go and the Architecture constants together", got, want)
	}
}

// enumTag returns the values of the `enum` tag on a field reached by walking
// the named path from root, failing the test if the path or the tag is absent.
// An absent tag is a failure and never an empty result. A dropped tag would
// remove validation at the edge, which is what this test exists to notice.
func enumTag(t *testing.T, root reflect.Type, path ...string) []string {
	t.Helper()

	cur := root
	for i, name := range path {
		f, ok := cur.FieldByName(name)
		if !ok {
			t.Fatalf("%s has no field %q (path %v)", cur, name, path[:i+1])
		}
		if i == len(path)-1 {
			tag, ok := f.Tag.Lookup("enum")
			if !ok {
				t.Fatalf("field %q has no `enum` tag", strings.Join(path, "."))
			}
			return strings.Split(tag, ",")
		}
		cur = f.Type
	}
	t.Fatalf("empty path")
	return nil
}
