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

import "crypto/rand"

// SweepSaltParam names the parameter a shuffled sweep listing binds its salt
// to. The listings write @salt into their SQL directly; this only keeps the
// params maps from spelling it themselves.
const SweepSaltParam = "salt"

// SweepSalt returns a fresh salt for one sweep listing, whose rows are ordered
// by FARM_FINGERPRINT(CONCAT(<row key>, @salt)) over the predicate's candidates.
//
// The sweeps that use it run on every instance. Ordered by the column their
// predicate filters on (oldest expiry, oldest failure), every instance draws
// the same rows in the same sequence and the whole fleet contends on one row's
// guards at a time. Each of those actions is idempotent, so what the collision
// costs is throughput and never correctness, but it is throughput paid on
// every row. A salt drawn per call leaves the predicate and the LIMIT exactly as
// they were and permutes only what survives them: two instances taking L of C
// candidates then expect L²/C rows in common instead of min(L, C).
//
// The oldest-first service this gives up is bounded. While the candidates fit
// inside the limit (the steady state for every listing that uses this) all of
// them come back every cycle and the order decides nothing but the sequence in
// which they are acted on. Past that, each candidate is drawn with probability
// L/C per instance per cycle, so the expected wait is C/L cycles. The reaper
// sizes L at a share of the population its candidates are a subset of, which
// puts a floor under L/C and the wait at tens of cycles against lifetimes
// measured in hours.
//
// crypto/rand rather than math/rand: nothing here turns on unpredictability,
// but rand.Text is the standard library's fresh-random-string call and has no
// error to handle.
func SweepSalt() string {
	return rand.Text()
}
