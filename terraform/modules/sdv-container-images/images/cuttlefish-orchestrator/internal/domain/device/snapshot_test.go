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

package device

import (
	"errors"
	"strings"
	"testing"
)

// Restorable is the admission rule for the restore verb; each refusing case
// must carry a reason a user can act on, and the incarnation comparison must
// refuse even when lost-marking has not caught up (State still ready).
func TestSnapshotRestorable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		state           SnapshotState
		snapIncarnation int64
		hostIncarnation int64
		wantReason      string // empty = restorable
	}{
		{"ready on the same incarnation", SnapshotStateReady, 3, 3, ""},
		{"still creating", SnapshotStateCreating, 3, 3, "still being taken"},
		{"marked lost", SnapshotStateLost, 3, 3, "lost"},
		{"ready but the host was re-provisioned", SnapshotStateReady, 3, 4, "re-provisioned"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &Snapshot{ID: "snap-1", State: tt.state, HostIncarnation: tt.snapIncarnation}
			err := s.Restorable(tt.hostIncarnation)
			if tt.wantReason == "" {
				if err != nil {
					t.Fatalf("Restorable() = %v, want nil", err)
				}
				return
			}
			var nre *SnapshotNotRestorableError
			if !errors.As(err, &nre) {
				t.Fatalf("Restorable() = %v, want *SnapshotNotRestorableError", err)
			}
			if !strings.Contains(nre.Reason, tt.wantReason) {
				t.Errorf("reason %q does not mention %q", nre.Reason, tt.wantReason)
			}
		})
	}
}
