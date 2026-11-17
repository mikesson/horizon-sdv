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

package batchfeature

import (
	"reflect"
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// TestArchitectureEnumTagMatchesDomain binds the batch create's architecture
// enum to the domain vocabulary. The batch hands its requirements to the same
// device.Requirements every member is admitted against, which accepts x86, arm
// and the cleared field; a tag that omits one of them rejects it at the edge
// instead, with a message that names no cause.
func TestArchitectureEnumTagMatchesDomain(t *testing.T) {
	t.Parallel()

	got := enumTag(t, reflect.TypeFor[batchRequirements](), "Architecture")
	want := []string{string(device.ArchitectureX86), string(device.ArchitectureARM), ""}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("architecture enum tag = %v, domain vocabulary = %v\n"+
			"update the enum tag in types.go and the Architecture constants together", got, want)
	}
}

// enumTag returns the values of the `enum` tag on a field reached by walking
// the named path from root, failing the test if the path or the tag is absent.
// An absent tag fails the test and never yields an empty result. A dropped tag
// would remove validation at the edge, which this test exists to notice.
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
