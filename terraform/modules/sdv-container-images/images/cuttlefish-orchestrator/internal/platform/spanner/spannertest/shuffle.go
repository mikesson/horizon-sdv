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

package spannertest

import (
	"slices"
	"testing"
)

// shuffleSamples is how many times AssertShuffledSweep lists before deciding
// the order never moves. Over a candidate set of a dozen rows a listing that is
// in fact still deterministically ordered fails with certainty, while a truly
// shuffled one would have to draw the same permutation four times running.
const shuffleSamples = 5

// AssertShuffledSweep checks the contract a salted sweep listing owes (see
// spanner.SweepSalt): the same candidate set on every call, more than one order
// across calls, and a caller's limit that still binds to candidates. list takes
// a limit and returns the row keys in the order the query produced them; want is
// the full candidate set, in any order.
//
// It goes red if the salt reaches the WHERE clause (the returned set stops
// matching), if the LIMIT is dropped, or if the order is wired back to a column.
func AssertShuffledSweep(t *testing.T, what string, want []string, list func(limit int) ([]string, error)) {
	t.Helper()
	want = slices.Sorted(slices.Values(want))
	orders := make([][]string, 0, shuffleSamples)
	for range shuffleSamples {
		ids, err := list(len(want))
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		sorted := slices.Clone(ids)
		slices.Sort(sorted)
		if !slices.Equal(sorted, want) {
			t.Fatalf("%s returned %v, want exactly the candidate set %v. The salt orders, it must never filter",
				what, sorted, want)
		}
		orders = append(orders, ids)
	}
	shuffled := false
	for _, o := range orders[1:] {
		if !slices.Equal(o, orders[0]) {
			shuffled = true
			break
		}
	}
	if !shuffled {
		t.Errorf("%s returned one order %v across %d calls; every instance would sweep the same rows in the same sequence",
			what, orders[0], shuffleSamples)
	}

	bound := len(want) / 2
	ids, err := list(bound)
	if err != nil {
		t.Fatalf("%s(limit=%d): %v", what, bound, err)
	}
	if len(ids) != bound {
		t.Errorf("%s(limit=%d) returned %d rows; the shuffle must not cost the bound", what, bound, len(ids))
	}
	for _, id := range ids {
		if !slices.Contains(want, id) {
			t.Errorf("%s(limit=%d) returned %s, which is not a candidate", what, bound, id)
		}
	}
}
