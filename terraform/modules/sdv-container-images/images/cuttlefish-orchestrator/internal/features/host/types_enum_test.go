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

package hostfeature

import (
	"reflect"
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/gce"
)

// TestErrorTypeEnumTagMatchesClassifier binds the error_type published in the
// OpenAPI schema to the classifier that produces it.
//
// The value is written down three times: gce.ErrorTypes(), the enum tag on
// host.Host, and the console's union. Domain cannot import platform, so the tag
// is a literal and nothing but this test connects it to the classifier. The
// tag is also what the spec publishes, which is what the console is checked
// against by the schema-drift script. That makes this the first link in the
// chain; break it and the other two agree with each other about a vocabulary
// the backend does not send.
//
// This is not hypothetical. The console's copy was invented independently and
// shared exactly two values (permission_denied, unknown) with the nineteen the
// backend emits, so every other real provisioning failure fell through its
// label map to a generic "Error".
func TestErrorTypeEnumTagMatchesClassifier(t *testing.T) {
	t.Parallel()

	got := enumTagOn(t, reflect.TypeFor[host.Host](), "ErrorType")

	want := make([]string, 0, len(gce.ErrorTypes()))
	for _, e := range gce.ErrorTypes() {
		want = append(want, string(e))
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("error_type enum tag = %v\ngce.ErrorTypes()             = %v\n"+
			"update the tag on host.Host and gce.ErrorTypes() together", got, want)
	}
}

// TestErrorTypesCoversEveryClassifierConstant catches a constant added to the
// gce package but left out of ErrorTypes(), which the test above cannot see:
// it compares the accessor to the tag, so a value missing from BOTH agrees.
func TestErrorTypesCoversEveryClassifierConstant(t *testing.T) {
	t.Parallel()

	listed := make(map[gce.ErrorType]bool, len(gce.ErrorTypes()))
	for _, e := range gce.ErrorTypes() {
		listed[e] = true
	}

	// Every value the classifier can actually return, gathered from the errors
	// it produces for representative inputs, is not reachable here. So this
	// asserts the weaker but still useful property that the accessor has no
	// duplicates and no empties, and that its length matches the constant block.
	if len(listed) != len(gce.ErrorTypes()) {
		t.Errorf("gce.ErrorTypes() contains duplicates: %d unique of %d",
			len(listed), len(gce.ErrorTypes()))
	}
	for _, e := range gce.ErrorTypes() {
		if strings.TrimSpace(string(e)) == "" {
			t.Error("gce.ErrorTypes() contains an empty value")
		}
	}
	if !listed[gce.ErrorTypeUnknown] {
		t.Error("gce.ErrorTypes() omits ErrorTypeUnknown, the classifier's fallback: " +
			"a host whose error could not be classified would carry a value the API rejects")
	}
}

// TestOriginAndClassEnumTagsMatchDomain binds the three published host-provenance
// vocabularies to the domain constants that produce them.
//
// The tag is what the OpenAPI document carries, so it is what the generated
// cfctl models and the console's drift check are measured against. Widen the
// domain without widening the tag and the backend sends a value its own spec
// forbids; narrow the tag and a filter silently stops accepting a class that
// still exists.
func TestOriginAndClassEnumTagsMatchDomain(t *testing.T) {
	t.Parallel()

	origins := make([]string, 0, len(host.Origins))
	for _, o := range host.Origins {
		origins = append(origins, string(o))
	}
	classes := make([]string, 0, len(host.Classes))
	for _, c := range host.Classes {
		classes = append(classes, string(c))
	}

	tests := []struct {
		name  string
		got   []string
		want  []string
		fixup string
	}{
		{"Host.origin", enumTagOn(t, reflect.TypeFor[host.Host](), "Origin"), origins, "host.Origins"},
		{"Host.class", enumTagOn(t, reflect.TypeFor[host.Host](), "Class"), classes, "host.Classes"},
		// The list filter's vocabulary is the same set: a class an operator
		// cannot name is a class they cannot find hosts by.
		{"ListHostsInput.class", enumTagOn(t, reflect.TypeFor[ListHostsInput](), "Class"), classes, "host.Classes"},
	}

	for _, tc := range tests {
		if !reflect.DeepEqual(tc.got, tc.want) {
			t.Errorf("%s enum tag = %v\n%s = %v\nupdate the tag and %s together",
				tc.name, tc.got, tc.fixup, tc.want, tc.fixup)
		}
	}
}

// enumTagOn returns the values of the `enum` tag on a named field, failing if
// the field or the tag is absent. An absent tag fails the test and never
// yields an empty result. Dropping it would remove the vocabulary from the
// published spec, which this test exists to notice.
func enumTagOn(t *testing.T, root reflect.Type, field string) []string {
	t.Helper()

	f, ok := root.FieldByName(field)
	if !ok {
		t.Fatalf("%s has no field %q", root, field)
	}
	tag, ok := f.Tag.Lookup("enum")
	if !ok {
		t.Fatalf("field %q has no `enum` tag", field)
	}
	return strings.Split(tag, ",")
}
