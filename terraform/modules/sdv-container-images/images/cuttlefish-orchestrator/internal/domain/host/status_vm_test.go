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

// TestExpectsVM pins the lost-host eligibility set: a missing GCE instance
// only means out-of-band deletion in statuses reached after a successful
// provision and before teardown or preemption.
func TestExpectsVM(t *testing.T) {
	expectVM := map[Status]bool{
		StatusStarting:    true,
		StatusRunning:     true,
		StatusStopping:    true,
		StatusStopped:     true,
		StatusUnreachable: true,

		// The VM legitimately does not exist yet...
		StatusPending:      false,
		StatusProvisioning: false,
		// ...or may already be gone by our own or GCE's hand.
		StatusDeleting:  false,
		StatusDeleted:   false,
		StatusError:     false,
		StatusPreempted: false,
	}
	for status, want := range expectVM {
		if got := status.ExpectsVM(); got != want {
			t.Errorf("Status(%q).ExpectsVM() = %v, want %v", status, got, want)
		}
	}
}
