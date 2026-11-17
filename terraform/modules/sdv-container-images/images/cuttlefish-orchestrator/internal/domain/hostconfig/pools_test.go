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

package hostconfig_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
)

// The whole map goes or none of it does, for each of the three stored halves.
// A partially applied policy is one nobody wrote, and it would look chosen
// on every surface that rendered it, while the pools it silently dropped went
// on holding machines nothing was judging.
//
// Each case names the mutation: relaxing the corresponding rule to per-entry
// salvage turns the row green with a policy the operator never stored.
func TestParsePoolBoundsIsFailSafeOnTheWholeMap(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"not JSON", "default=2"},
		{"JSON of the wrong shape", `[{"pool":"default"}]`},
		{"a standby past the compiled bound", `{"default":{"standby":17,"floor":0}}`},
		{"a floor past the compiled bound", `{"default":{"standby":0,"floor":17}}`},
		{"a negative bound", `{"default":{"standby":-1,"floor":0}}`},
		{"a pool nothing could be stamped with", `{"":{"standby":1,"floor":0}}`},
		{"a pool name wider than the column", `{"` + strings.Repeat("p", 65) + `":{"standby":1}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bounds, ok := hostconfig.ParsePoolBounds(tc.raw)
			assert.False(t, ok, "an unreadable map must not read as stored")
			assert.Empty(t, bounds, "the whole map drops, never the readable pools out of a bad one")
		})
	}

	bounds, ok := hostconfig.ParsePoolBounds(`{"default":{"standby":2,"floor":1},"gpu":{"standby":1,"floor":0}}`)
	require.True(t, ok)
	assert.Equal(t, map[string]hostconfig.PoolBounds{
		"default": {Standby: 2, Floor: 1},
		"gpu":     {Standby: 1},
	}, bounds)

	bounds, ok = hostconfig.ParsePoolBounds("")
	require.True(t, ok, "an unseeded key is no bounds, which is a readable answer")
	assert.Empty(t, bounds)
}

// The ladder's own rules survive the move into the map: a list is judged as a
// list, and a bad one in any pool costs every pool's ladder rather than leaving
// an order nobody wrote in place.
func TestParsePoolLaddersIsFailSafeOnTheWholeMap(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"not JSON", "default=n2-standard-8"},
		{"JSON of the wrong shape", `{"default":{"rungs":[]}}`},
		{"past the rung bound", `{"default":["a","b","c","d","e","f","g","h","i"]}`},
		{"a repeated rung", `{"default":["n2-standard-8","n2-standard-8"]}`},
		{"an empty rung", `{"default":["n2-standard-8",""]}`},
		{"a bad list in a second pool", `{"default":["n2-standard-8"],"gpu":["",""]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ladders, ok := hostconfig.ParsePoolLadders(tc.raw)
			assert.False(t, ok, "an unreadable map must not read as stored")
			assert.Empty(t, ladders, "the whole map drops, never the readable pools out of a bad one")
		})
	}

	ladders, ok := hostconfig.ParsePoolLadders(`{"default":["n2-standard-8","n1-standard-8"]}`)
	require.True(t, ok)
	assert.Equal(t, map[string][]string{"default": {"n2-standard-8", "n1-standard-8"}}, ladders)

	ladders, ok = hostconfig.ParsePoolLadders("")
	require.True(t, ok, "an unseeded key is no ladder, which is a readable answer")
	assert.Empty(t, ladders)
}

// A window that cannot be evaluated must not reach the target composition,
// where it would be skipped silently and read exactly like no schedule at all.
func TestParsePoolSchedulesIsFailSafeOnTheWholeMap(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"not JSON", "default=08:00"},
		{"JSON of the wrong shape", `{"default":{"start":"08:00"}}`},
		{"a window with no name", `{"default":[{"name":"","hosts":2,"start":"08:00","duration_sec":3600,"location":"UTC"}]}`},
		{"a start nothing can parse", `{"default":[{"name":"morning","hosts":2,"start":"8am","duration_sec":3600,"location":"UTC"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schedules, ok := hostconfig.ParsePoolSchedules(tc.raw)
			assert.False(t, ok, "an unreadable map must not read as stored")
			assert.Empty(t, schedules, "the whole map drops, never the readable pools out of a bad one")
		})
	}

	schedules, ok := hostconfig.ParsePoolSchedules(
		`{"default":[{"name":"morning","hosts":2,"start":"08:00","duration_sec":3600,"location":"UTC"}]}`)
	require.True(t, ok)
	require.Len(t, schedules["default"], 1)
	assert.Equal(t, "morning", schedules["default"][0].Name)
}

// Every stored map round-trips, and an empty one stores as an empty object
// rather than null. Absent and empty mean the same thing to every reader, but
// only one of them looks like the empty policy an operator just saved.
func TestPoolMapsRoundTrip(t *testing.T) {
	t.Parallel()
	encoded, err := hostconfig.PoolBoundsJSON(nil)
	require.NoError(t, err)
	assert.Equal(t, "{}", encoded)

	encoded, err = hostconfig.PoolLaddersJSON(map[string][]string{"default": {"n2-standard-8"}})
	require.NoError(t, err)
	ladders, ok := hostconfig.ParsePoolLadders(encoded)
	require.True(t, ok)
	assert.Equal(t, map[string][]string{"default": {"n2-standard-8"}}, ladders)

	_, err = hostconfig.PoolBoundsJSON(map[string]hostconfig.PoolBounds{"": {Standby: 1}})
	require.Error(t, err, "a pool no machine could be stamped with must be refused at the write")
}

// A write that edits one pool must leave every other pool's entry standing.
// Rebuilding the map from what the write was editing compiles, passes every
// assertion about the pool being written, and clears the rest of the fleet's
// policy, which nothing would report until somebody read it back.
func TestWithPoolLeavesEveryOtherPoolStanding(t *testing.T) {
	t.Parallel()
	stored := map[string]hostconfig.PoolBounds{
		"default": {Standby: 2, Floor: 1},
		"gpu":     {Standby: 1},
	}
	updated := hostconfig.WithPool(stored, "gpu", hostconfig.PoolBounds{Standby: 3, Floor: 2})
	assert.Equal(t, hostconfig.PoolBounds{Standby: 3, Floor: 2}, updated["gpu"])
	assert.Equal(t, hostconfig.PoolBounds{Standby: 2, Floor: 1}, updated["default"],
		"the pool this write had no opinion about keeps its bounds")
	assert.Equal(t, hostconfig.PoolBounds{Standby: 1}, stored["gpu"],
		"the stored map is not written through")
}
