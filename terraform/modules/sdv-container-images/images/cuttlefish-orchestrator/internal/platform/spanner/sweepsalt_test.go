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

package spanner_test

import (
	"testing"

	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

// A salt hoisted to a package variable (the obvious "why compute this every
// call" edit) would leave every instance ordering its sweeps identically,
// which is the whole condition the salt exists to break. Nothing else in the
// listings would look different, so this is where that regression shows up.
func TestSweepSaltIsFreshPerCall(t *testing.T) {
	t.Parallel()
	const draws = 8
	seen := make(map[string]bool, draws)
	for range draws {
		salt := spannerplatform.SweepSalt()
		if salt == "" {
			t.Fatal("SweepSalt returned an empty salt; every row would sort on its key alone")
		}
		if seen[salt] {
			t.Fatalf("SweepSalt returned %q twice in %d draws", salt, draws)
		}
		seen[salt] = true
	}
}
