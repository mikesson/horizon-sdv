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
	"testing"
	"time"
)

// A re-pin replaces who, why and until, and keeps when the pin began.
func TestRepinKeepsPinnedAtAndReplacesTheRest(t *testing.T) {
	t.Parallel()
	l := NewEphemeralLifecycle(3600)
	first := time.Now().Add(time.Hour)
	l.Pin("a@example.com", PinReasonOther, "soak", &first)
	pinnedAt := *l.PinnedAt

	time.Sleep(time.Millisecond)
	later := time.Now().Add(48 * time.Hour)
	l.Pin("b@example.com", PinReasonLongRunningCI, "", &later)

	if !l.PinnedAt.Equal(pinnedAt) {
		t.Errorf("pinned_at moved from %v to %v on a re-pin", pinnedAt, *l.PinnedAt)
	}
	if !l.PinnedUntil.Equal(later) {
		t.Errorf("pinned_until = %v, want %v", l.PinnedUntil, later)
	}
	if *l.PinnedBy != "b@example.com" || *l.PinnedReason != PinReasonLongRunningCI {
		t.Errorf("re-pin kept the old pinner or reason: %s %s", *l.PinnedBy, *l.PinnedReason)
	}
	if l.PinnedReasonDetail != nil {
		t.Errorf("re-pin with no detail kept the old one: %q", *l.PinnedReasonDetail)
	}

	l.Pin("b@example.com", PinReasonLongRunningCI, "", nil)
	if l.PinnedUntil != nil {
		t.Errorf("re-pin with no until left %v; it must become open-ended", l.PinnedUntil)
	}
}
