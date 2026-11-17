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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The universe the snapshot reports; an empty one is a snapshot that has not
// landed.
func testUniverse() map[string]ZoneStatus {
	return map[string]ZoneStatus{
		"europe-west4-a": ZoneStatusUp,
		"europe-west4-b": ZoneStatusUp,
		"us-central1-a":  ZoneStatusDown,
	}
}

// Fail-closed against the snapshot: a zone Compute Engine does not offer can
// never take a host, so admitting one would leave an operator with a zone that
// looks enabled and silently ranks nothing.
func TestValidateEnabledZonesRefusesWhatTheSnapshotDoesNotOffer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		zones []string
		want  string
	}{
		{"unknown zone", []string{"europe-west4-a", "mars-east1-a"}, `does not offer "mars-east1-a"`},
		{"not a zone name", []string{"europe-west4"}, `is not a Compute Engine zone name`},
		{"blank", []string{""}, `is not a Compute Engine zone name`},
		{"repeat", []string{"europe-west4-a", "europe-west4-a"}, "appears more than once"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateEnabledZones(tc.zones, testUniverse())
			var policyErr *ZonePolicyError
			require.ErrorAs(t, err, &policyErr)
			assert.Contains(t, policyErr.Error(), tc.want)
		})
	}
}

// A DOWN zone is still admissible: enablement is a standing decision and a
// zone Compute Engine is having trouble with today comes back tomorrow.
// Ranking is where DOWN removes the candidate.
func TestValidateEnabledZonesAdmitsTheSnapshotsOwnZones(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidateEnabledZones(
		[]string{"europe-west4-a", "europe-west4-b", "us-central1-a"}, testUniverse()))
	require.NoError(t, ValidateEnabledZones(nil, testUniverse()))
}

// Without a snapshot only the name shape is checked. A deployment must be
// configurable before its first catalog refresh lands, and there is nothing
// yet to check the names against.
func TestValidateEnabledZonesAcceptsAnyZoneWithoutASnapshot(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidateEnabledZones([]string{"mars-east1-a"}, nil))
	require.NoError(t, ValidateEnabledZones([]string{"mars-east1-a"}, map[string]ZoneStatus{}))
}

// The stored value is the set, not the keystrokes: two writes that say the
// same thing store the same bytes.
func TestEnabledZonesJSONNormalizes(t *testing.T) {
	t.Parallel()
	encoded, err := EnabledZonesJSON([]string{"us-central1-a", " europe-west4-a ", "", "us-central1-a"})
	require.NoError(t, err)
	assert.JSONEq(t, `["europe-west4-a","us-central1-a"]`, encoded)

	empty, err := EnabledZonesJSON(nil)
	require.NoError(t, err)
	assert.Equal(t, "[]", empty, "an empty set stores as [], never as null")
}

// The whole-set fail-safe: a value that will not parse reads as NO enabled
// zones, which stops provisioning visibly rather than sending hosts to regions
// nobody networked.
func TestParseEnabledZonesFailsSafe(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
		want []string
		ok   bool
	}{
		{"absent", "", nil, true},
		{"empty array", "[]", []string{}, true},
		{"set", `["us-central1-a","europe-west4-a"]`, []string{"europe-west4-a", "us-central1-a"}, true},
		{"not JSON", "europe-west4-a", nil, false},
		{"wrong shape", `{"zones":[]}`, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			zones, ok := ParseEnabledZones(tc.raw)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, zones)
		})
	}
}

// Absence never removes a placement candidate: a zone the snapshot said
// nothing about carries no status and stays available.
func TestZoneIsAvailableOnlyRefusesAPositiveDown(t *testing.T) {
	t.Parallel()
	assert.True(t, (&Zone{Zone: "europe-west4-a"}).IsAvailable(), "an unknown status must not remove a zone")
	assert.True(t, (&Zone{Zone: "europe-west4-a", Status: ZoneStatusUp}).IsAvailable())
	assert.False(t, (&Zone{Zone: "europe-west4-a", Status: ZoneStatusDown}).IsAvailable())
}
