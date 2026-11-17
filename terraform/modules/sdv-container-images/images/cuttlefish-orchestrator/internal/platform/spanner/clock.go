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

package spanner

import "time"

// timeNow supplies wall-clock time for the queue's SCHEDULING values only:
// enqueue available_at defaults and the keyed-merge availability floor.
// Ownership is NOT on this clock. Every lease stamp and expiry comparison
// (task claim, sweeper re-verify, leased-sibling probe, leader leases) is
// evaluated server-side via CURRENT_TIMESTAMP(), so cross-instance lease
// correctness rides Spanner's TrueTime rather than NTP agreement between
// instances. Skewing this clock therefore shifts schedules by the offset but
// cannot cause premature reclaims or fenced-out deliveries.
var timeNow = time.Now

// SetClockOffsetForTesting shifts this process's notion of wall-clock time
// for the scheduling values in this package. Test harnesses only; never call
// in production code.
func SetClockOffsetForTesting(offset time.Duration) {
	if offset == 0 {
		timeNow = time.Now
		return
	}
	timeNow = func() time.Time { return time.Now().Add(offset) }
}
