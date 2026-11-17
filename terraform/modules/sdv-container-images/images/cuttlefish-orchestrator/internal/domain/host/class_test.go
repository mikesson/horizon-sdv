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

package host

import "testing"

// TestDeriveClass walks the whole origin × exclusive × batch_id grid, because
// the class decides whether the fleet may take a host away and every cell is
// therefore a decision rather than a case.
//
// Four rows are the ones that matter, and each names the mutation it catches:
//
//   - user + exclusive must be reserved. `exclusive` is a field users set to
//     fence a host from placement while a test runs; an arm keyed on
//     exclusivity alone derives dedicated, whose rule is "reclaim it when
//     empty", and this row goes red.
//   - an unknown origin, and the empty one a pre-column row reads back as,
//     must be reserved. A function that switches on only the four named
//     origins without a default lands them somewhere else and goes red.
//   - batch wins over everything, including an unstamped origin. Moving the
//     fallback above the batch arm strands batch hosts in reserved, which
//     nothing reclaims, and this goes red.
func TestDeriveClass(t *testing.T) {
	t.Parallel()

	const someBatch = "batch-1"

	tests := []struct {
		name      string
		origin    Origin
		exclusive bool
		batchID   string
		want      Class
	}{
		// Shared, system-created: the managed fleet.
		{"auto shared", OriginAuto, false, "", ClassFleet},
		{"pool shared", OriginPool, false, "", ClassFleet},

		// System-created and fenced: nothing can be placed here again.
		{"auto exclusive", OriginAuto, true, "", ClassDedicated},
		{"pool exclusive", OriginPool, true, "", ClassDedicated},

		// Asked for by a person, however it is shaped.
		{"user shared", OriginUser, false, "", ClassReserved},
		{"user exclusive", OriginUser, true, "", ClassReserved},

		// Batch membership decides on its own, whatever else the row says.
		{"batch origin", OriginBatch, false, someBatch, ClassBatch},
		{"batch origin exclusive", OriginBatch, true, someBatch, ClassBatch},
		{"batch id with auto origin", OriginAuto, false, someBatch, ClassBatch},
		{"batch id with user origin", OriginUser, true, someBatch, ClassBatch},
		{"batch id unstamped", "", false, someBatch, ClassBatch},

		// A batch origin without the id is a contradiction the derivation must
		// still answer: it is not batch settlement's without the id, and it is
		// nobody's fleet either.
		{"batch origin without id", OriginBatch, false, "", ClassReserved},

		// Unstamped and unrecognized both fail safe.
		{"unstamped", "", false, "", ClassReserved},
		{"unstamped exclusive", "", true, "", ClassReserved},
		{"unknown origin", Origin("spot-fleet"), false, "", ClassReserved},
		{"unknown origin exclusive", Origin("spot-fleet"), true, "", ClassReserved},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DeriveClass(tc.origin, tc.exclusive, tc.batchID); got != tc.want {
				t.Errorf("DeriveClass(%q, %t, %q) = %q, want %q",
					tc.origin, tc.exclusive, tc.batchID, got, tc.want)
			}
		})
	}
}

// TestDeriveClassIsTotal asserts the property the grid above can only sample:
// every origin the type can hold derives a class in the published vocabulary.
// A derivation that returned "" for an input nobody enumerated would put a
// value on the wire that the OpenAPI enum rejects.
func TestDeriveClassIsTotal(t *testing.T) {
	t.Parallel()

	valid := make(map[Class]bool, len(Classes))
	for _, c := range Classes {
		valid[c] = true
	}

	origins := append([]Origin{"", "unknown", "USER", "Fleet"}, Origins...)
	for _, o := range origins {
		for _, exclusive := range []bool{false, true} {
			for _, batchID := range []string{"", "b-1"} {
				if got := DeriveClass(o, exclusive, batchID); !valid[got] {
					t.Errorf("DeriveClass(%q, %t, %q) = %q, which is not in Classes %v",
						o, exclusive, batchID, got, Classes)
				}
			}
		}
	}
}

// TestOriginSystem pins which origins the orchestrator created on its own
// initiative. Both class arms that consult provenance read this, and the SQL
// predicate builds its origin list from it, so a wrong answer here moves the
// Go derivation and the query together, silently and in agreement.
func TestOriginSystem(t *testing.T) {
	t.Parallel()

	want := map[Origin]bool{
		OriginUser:  false,
		OriginAuto:  true,
		OriginPool:  true,
		OriginBatch: false,
	}
	for _, o := range Origins {
		if got := o.System(); got != want[o] {
			t.Errorf("Origin(%q).System() = %t, want %t", o, got, want[o])
		}
	}
	if Origin("").System() {
		t.Error(`Origin("").System() = true; an unstamped host must not read as system-created`)
	}
	if len(want) != len(Origins) {
		t.Errorf("Origins has %d values but this test pins %d; add the new origin here", len(Origins), len(want))
	}
}
