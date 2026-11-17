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

package taskworker

import "testing"

// TestLongWorkBudgetIsAShare pins the budget as arithmetic on the pool's width
// rather than a number: a quarter of it held back for short work, never less
// than one worker, and never taking the last worker of a pool too narrow to
// split.
//
// It fails if the budget is ever written as an absolute (48 satisfies the
// ceiling row and nothing else) or if the reserve is rounded the other way.
func TestLongWorkBudgetIsAShare(t *testing.T) {
	for _, tc := range []struct {
		width, budget int64
	}{
		{1, 1}, // no reserve is possible; the pool is one worker
		{2, 1},
		{3, 2},
		{4, 3},
		{8, 6},   // the deployment's floor: two workers left for short work
		{16, 12}, //
		{64, 48}, // the deployment's ceiling: sixteen left
	} {
		if got := longWorkBudget(tc.width); got != tc.budget {
			t.Errorf("a pool %d workers wide budgeted %d long workers, want %d", tc.width, got, tc.budget)
		}
		if tc.width > 1 && longWorkBudget(tc.width) >= tc.width {
			t.Errorf("a pool %d workers wide kept no worker for short work", tc.width)
		}
	}
}

// TestBulkheadBudgetTracksThePoolWidth is the other half: the budget is taken
// from the width the pool has NOW, so it moves as the sizer grows the pool.
//
// It fails if the budget is ever derived from the configured ceiling: the first
// assertion would read 48 at a pool sitting on its floor of 8, which is the
// case where the reserve is the only thing keeping a worker claiming.
func TestBulkheadBudgetTracksThePoolWidth(t *testing.T) {
	s := newSizer(sizerConfig(8, 64))
	s.start()
	b := newBulkhead(s)

	if got := b.budget(); got != 6 {
		t.Errorf("a pool at its floor of 8 budgeted %d long workers, want 6", got)
	}
	for range 64 {
		s.observeClaim(s.growAfter)
	}
	if got := b.budget(); got != 48 {
		t.Errorf("a pool at its ceiling of 64 budgeted %d long workers, want 48", got)
	}
}
