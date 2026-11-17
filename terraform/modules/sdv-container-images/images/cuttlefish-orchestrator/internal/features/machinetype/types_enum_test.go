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

package machinetypefeature

import (
	"reflect"
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// TestCategoryEnumTagMatchesDomain binds the list endpoint's category filter to
// the domain vocabulary. Huma validates the query parameter from a struct tag,
// and a tag cannot reference a constant, so the accepted set is written down
// twice. Nothing but this test stops the copies diverging, and the tag wins
// silently when they do: a category the domain declares is rejected at the edge
// with a validation error that names no cause, while a category dropped from
// the domain keeps being accepted.
//
// Five of these constants had no Go reader at all, which is why the dead-code
// pass reported them. They are consumed through the tag, by every client that
// filters, and deleting them on that report would have deleted the only place
// the vocabulary is written down as code.
func TestCategoryEnumTagMatchesDomain(t *testing.T) {
	t.Parallel()

	got := enumTag(t, reflect.TypeFor[ListMachineTypesInput](), "Category")

	want := make([]string, 0, len(machinetype.Categories()))
	for _, c := range machinetype.Categories() {
		want = append(want, string(c))
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("category enum tag = %v, machinetype.Categories() = %v\n"+
			"update the enum tag in types.go and Categories() in domain/machinetype together", got, want)
	}
}

// TestArchitectureEnumTagMatchesDomain does the same for the architecture
// filter. ArchArm is the constant with no Go reader here.
func TestArchitectureEnumTagMatchesDomain(t *testing.T) {
	t.Parallel()

	got := enumTag(t, reflect.TypeFor[ListMachineTypesInput](), "Architecture")

	want := make([]string, 0, len(machinetype.Architectures()))
	for _, a := range machinetype.Architectures() {
		want = append(want, string(a))
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("architecture enum tag = %v, machinetype.Architectures() = %v\n"+
			"update the enum tag in types.go and Architectures() in domain/machinetype together", got, want)
	}
}

// enumTag returns the values of the `enum` tag on a field reached by walking the
// named path from root, failing the test if the path or the tag is absent. An
// absent tag fails the test and never yields an empty result. A dropped tag
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
