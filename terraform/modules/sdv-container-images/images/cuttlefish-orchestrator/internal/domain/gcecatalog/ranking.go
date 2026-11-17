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

package gcecatalog

// ZoneCandidate is one ranked placement choice: a zone that passed every hard
// filter, with its score and the human-readable trace of how the score came
// to be. The trace is surfaced in validate_only resolutions and on failed
// hosts' alternative-zone advice, so a placement decision is always
// explainable after the fact.
type ZoneCandidate struct {
	Zone   string  `json:"zone"`
	Region string  `json:"region"`
	Score  float64 `json:"score"`
	// Reasons records each scoring term's normalized contribution and any
	// degradations (stale quota data passed advisorily, cooldowns ignored
	// because every candidate was cooled).
	Reasons []string `json:"reasons,omitempty"`
}

// RankOptions carries the per-request ranking inputs that are not part of the
// machine shape (which ValidationRequest already describes).
type RankOptions struct {
	// PreferredRegion biases ranking toward a region (a location hint or a
	// /v1 zone token's region); empty = capacity-ranked only.
	PreferredRegion string

	// GeoDominant makes proximity the dominant scoring term instead of a
	// tie-break. Set for interactive intent, where WebRTC latency is bound to
	// the host zone.
	GeoDominant bool

	// ExcludeZone drops one zone from the candidate set, the zone a failure
	// was just observed in, when ranking alternatives for error advice.
	ExcludeZone string

	// SpotMinUptimeSeconds filters Spot candidates whose fresh advice reports
	// an estimated-uptime tier below it (60/600/3600). 0 = no uptime filter.
	// Only meaningful for Spot specs; absent or stale advice never filters.
	SpotMinUptimeSeconds int64
}
