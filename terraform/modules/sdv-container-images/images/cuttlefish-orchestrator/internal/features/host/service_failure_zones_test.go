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
	"slices"
	"testing"
)

// The advice stored on a failed host decides what an operator is told to try
// next. A live g2 stockout once stored the ranking's answer, the two zones the
// request had just been refused in, while GCE was naming a third that had
// capacity.
func TestPreferObservedZones(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		observed []string
		ranked   []string
		want     []string
	}{{
		name:     "GCE named a zone the deployment can use",
		observed: []string{"europe-west4-c"},
		ranked:   []string{"europe-west4-a", "europe-west4-b", "europe-west4-c"},
		want:     []string{"europe-west4-c"},
	}, {
		name:     "the exhausted region names none, so the ranking stands",
		observed: nil,
		ranked:   []string{"europe-west4-a", "europe-west4-b"},
		want:     []string{"europe-west4-a", "europe-west4-b"},
	}, {
		name:     "a named zone this deployment cannot place into is not advice",
		observed: []string{"us-central1-a"},
		ranked:   []string{"europe-west4-a"},
		want:     []string{"europe-west4-a"},
	}, {
		name:     "named zones keep GCE's order, not the ranking's",
		observed: []string{"europe-west4-c", "europe-west4-a"},
		ranked:   []string{"europe-west4-a", "europe-west4-c"},
		want:     []string{"europe-west4-c", "europe-west4-a"},
	}, {
		name:     "nothing observed and nothing ranked stays empty",
		observed: nil,
		ranked:   nil,
		want:     nil,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := preferObservedZones(tc.observed, tc.ranked); !slices.Equal(got, tc.want) {
				t.Errorf("preferObservedZones(%v, %v) = %v, want %v", tc.observed, tc.ranked, got, tc.want)
			}
		})
	}
}
