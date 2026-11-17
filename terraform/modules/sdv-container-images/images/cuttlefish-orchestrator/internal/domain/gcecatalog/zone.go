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

// ZoneStatus represents the operational status of a GCE zone.
type ZoneStatus string

const (
	// ZoneStatusUp indicates the zone is operational.
	ZoneStatusUp ZoneStatus = "UP"
	// ZoneStatusDown indicates the zone is experiencing issues.
	ZoneStatusDown ZoneStatus = "DOWN"
)

// Zone is one placement zone: its identity plus what Compute Engine last said
// about it. Enablement is not a field here. The enabled set is operator
// policy read whole from system_config, so a Zone value is only ever built for
// a zone that is already in it or, on the curation view, beside the set.
type Zone struct {
	// Region is the parent region (e.g., "us-central1").
	Region string `json:"region"`

	// Zone is the zone identifier (e.g., "us-central1-a").
	Zone string `json:"zone"`

	// Status is what Compute Engine reported for the zone; empty when the
	// catalog snapshot has not landed or did not report it.
	Status ZoneStatus `json:"status"`
}

// IsAvailable reports whether the zone may take a host. Only a positive DOWN
// removes it: an empty status is absence of knowledge, and absence never
// removes a placement candidate.
func (z *Zone) IsAvailable() bool {
	return z.Status != ZoneStatusDown
}

// RegionFromZone extracts the region from a zone identifier.
// For example, "us-central1-a" returns "us-central1".
func RegionFromZone(zone string) string {
	if len(zone) < 2 {
		return zone
	}
	// Find the last dash and remove everything after it.
	for i := len(zone) - 1; i >= 0; i-- {
		if zone[i] == '-' {
			return zone[:i]
		}
	}
	return zone
}
