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

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// zoneNamePattern is the shape of a Compute Engine zone name: a region of at
// least two dash-separated parts, then the zone suffix. It is the only rule
// available when no catalog snapshot has landed, so it is permissive on
// purpose about what those parts contain.
var zoneNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+){2,}$`)

// ZonePolicyError is a refused zone-enablement write.
type ZonePolicyError struct {
	Message string
}

func (e *ZonePolicyError) Error() string {
	return "zone enablement: " + e.Message
}

// EnabledZonesJSON encodes an enabled set for storage, sorted and
// deduplicated so the stored value is the same whatever order it arrived in.
// An empty or nil set stores as "[]" rather than "null": absent and empty
// already mean the same thing to every reader, and "null" reads back correctly
// but does not look like the empty set an operator just saved.
func EnabledZonesJSON(zones []string) (string, error) {
	normalized := normalizeZones(zones)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encoding the enabled zones: %w", err)
	}
	return string(encoded), nil
}

// ParseEnabledZones reads a stored enabled set, fail-safe on the whole list.
//
// A value that will not parse reads as NO enabled zones, and the second result
// says so. That is the direction that cannot spend money in a region nobody
// networked: the enabled set is also the regional-network guard, so widening
// it on a bad read would send hosts to zones with no subnet, while narrowing
// it stops provisioning visibly and is exactly the state a deployment that has
// never enabled a zone is already in.
func ParseEnabledZones(raw string) ([]string, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, true
	}
	var zones []string
	if err := json.Unmarshal([]byte(raw), &zones); err != nil {
		return nil, false
	}
	return normalizeZones(zones), true
}

// ValidateEnabledZones refuses an enabled set at the door, naming the rule the
// refused zone broke.
//
// universe is every zone Compute Engine offers the project, from the catalog
// snapshot. When it is empty the snapshot has not landed, and only the name
// shape is checked. A deployment must be configurable before its first
// catalog refresh, and there is nothing yet to check the names against.
// Once the snapshot is there the check is closed: a zone Compute Engine does
// not offer can never take a host, so storing it would leave the operator with
// an enabled zone that silently ranks nothing.
func ValidateEnabledZones(zones []string, universe map[string]ZoneStatus) error {
	seen := make(map[string]bool, len(zones))
	for _, zone := range zones {
		switch {
		case !zoneNamePattern.MatchString(zone):
			return &ZonePolicyError{Message: fmt.Sprintf("%q is not a Compute Engine zone name", zone)}
		case seen[zone]:
			return &ZonePolicyError{Message: fmt.Sprintf("%q appears more than once", zone)}
		}
		seen[zone] = true
		if len(universe) == 0 {
			continue
		}
		if _, offered := universe[zone]; !offered {
			return &ZonePolicyError{
				Message: fmt.Sprintf("Compute Engine does not offer %q to this project", zone),
			}
		}
	}
	return nil
}

// normalizeZones sorts and deduplicates a zone set, dropping blanks. The set
// has no order an operator chose, so storing one would invent a difference
// between two writes that say the same thing.
func normalizeZones(zones []string) []string {
	trimmed := make([]string, 0, len(zones))
	for _, zone := range zones {
		if z := strings.TrimSpace(zone); z != "" {
			trimmed = append(trimmed, z)
		}
	}
	slices.Sort(trimmed)
	return slices.Compact(trimmed)
}
