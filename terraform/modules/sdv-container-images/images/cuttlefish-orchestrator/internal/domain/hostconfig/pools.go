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

package hostconfig

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// The three stored parts of the per-pool policy, and why they are three values
// rather than one.
//
// Each is written by exactly one surface (the bounds by the autoscaler control,
// the ladder and the windows each by their own replace-whole write) so no write
// has to carry a field it was not editing, and a client holding a stale read of
// one cannot clear another. That is the same independence the scalars had before
// pools, kept across the reshape.
//
// Each is read whole and fails whole: a value that will not parse, or that
// breaks a bound the write path enforces, reads as NO pools configured. That
// direction is the spend-nothing one for all three: no bounds asks for no
// machines, no windows lowers the target to what standing intent says, and no
// ladder is the catalog default that every deployment ran on before the key
// existed. A partially applied map would be a policy nobody wrote, and it would
// look chosen on every surface that rendered it.

// MaxPoolNameLength bounds a stored pool name. It is the width of the hosts.pool
// column: a policy naming a pool no machine could ever be stamped with is a
// bound nothing can satisfy.
const MaxPoolNameLength = 64

// FieldPool names the pool in a ValidationError, for all three of the maps
// below: the name is what failed, whichever value it was keying.
const FieldPool = "pool"

// PoolBounds is one pool's stored pair of lower bounds. The two travel together
// because one write surface sets them together and because the keeper spends
// against both in one decision.
type PoolBounds struct {
	Standby int64 `json:"standby"`
	Floor   int64 `json:"floor"`
}

// PoolBoundsJSON encodes the bounds map for storage. A nil map stores as "{}"
// rather than "null": absent and empty already mean the same thing to every
// reader, and "null" reads back correctly but does not look like the empty map
// an operator just saved.
func PoolBoundsJSON(bounds map[string]PoolBounds) (string, error) {
	return encodePools(bounds, "the pool bounds")
}

// ParsePoolBounds reads the stored bounds, fail-safe on the whole map.
func ParsePoolBounds(raw string) (map[string]PoolBounds, bool) {
	bounds, ok := decodePools[PoolBounds](raw)
	if !ok {
		return nil, false
	}
	for _, b := range bounds {
		if outOfRange(b.Standby, MaxStandbyHosts) || outOfRange(b.Floor, MaxFleetFloor) {
			return nil, false
		}
	}
	return bounds, true
}

// PoolLaddersJSON encodes the per-pool shape ladders for storage.
func PoolLaddersJSON(ladders map[string][]string) (string, error) {
	return encodePools(ladders, "the pool shape ladders")
}

// ParsePoolLadders reads the stored ladders, fail-safe on the whole map.
//
// The catalog rules are NOT applied here. The catalog moves after a
// ladder is stored (a shape can be disabled, a row deleted) and dropping the
// whole thing for that would silently revert a pool's preferred shape.
// Resolution against the catalog is where a stale rung is dropped, and it drops
// only that rung.
func ParsePoolLadders(raw string) (map[string][]string, bool) {
	ladders, ok := decodePools[[]string](raw)
	if !ok {
		return nil, false
	}
	for _, rungs := range ladders {
		if err := validateLadderShape(rungs); err != nil {
			return nil, false
		}
	}
	return ladders, true
}

// PoolSchedulesJSON encodes the per-pool schedule windows for storage.
func PoolSchedulesJSON(schedules map[string][]FleetSchedule) (string, error) {
	return encodePools(schedules, "the pool schedules")
}

// ParsePoolSchedules reads the stored windows, fail-safe on the whole map.
func ParsePoolSchedules(raw string) (map[string][]FleetSchedule, bool) {
	schedules, ok := decodePools[[]FleetSchedule](raw)
	if !ok {
		return nil, false
	}
	for _, windows := range schedules {
		if err := ValidateSchedules(windows); err != nil {
			return nil, false
		}
	}
	return schedules, true
}

// ValidatePoolName refuses a name this platform will not configure. Names are
// not free text an operator invents per write: a pool exists because the system
// can route a create into it and knows what machine to buy for it, and both are
// compiled knowledge, so a write may only name a pool the registry holds.
func ValidatePoolName(name string) error {
	if err := validPoolKey(name); err != nil {
		return err
	}
	if !host.IsPool(name) {
		return &ValidationError{
			Field:   FieldPool,
			Message: fmt.Sprintf("%q is not a pool this platform has; it partitions its machines into %v", name, host.Pools),
		}
	}
	return nil
}

// validPoolKey refuses a name no machine could carry, applying the rules the
// column imposes, and the only ones a stored map is read against.
//
// Separate from the registry check above, because the two protect different
// things. The write is the door and can name the fix; a READ that dropped a
// whole map over one name would take every other pool's bounds down with it, on
// a value no endpoint can produce.
func validPoolKey(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return &ValidationError{Field: FieldPool, Message: "a pool must be named"}
	case len(name) > MaxPoolNameLength:
		return &ValidationError{
			Field:   FieldPool,
			Message: fmt.Sprintf("a pool name must not exceed %d characters", MaxPoolNameLength),
		}
	}
	return nil
}

// WithoutPool removes one pool's entry from a stored map, which is how a pool is
// switched off: its bounds are what enable it, and taking them away leaves it
// asking for nothing, refused by selection, and holding whatever machines it
// still has until ordinary reclamation collects them.
func WithoutPool[V any](stored map[string]V, pool string) map[string]V {
	updated := make(map[string]V, len(stored))
	maps.Copy(updated, stored)
	delete(updated, pool)
	return updated
}

// WithPool replaces one pool's entry in a stored map and leaves every other
// pool's alone.
//
// Generic over the three maps because the alternative is the same read-overlay
// three times: each write surface edits one pool's half of one value, and a
// write that rebuilt the map from what it was editing would delete every pool it
// had no opinion about.
func WithPool[V any](stored map[string]V, pool string, value V) map[string]V {
	updated := make(map[string]V, len(stored)+1)
	maps.Copy(updated, stored)
	updated[pool] = value
	return updated
}

func encodePools[V any](pools map[string]V, what string) (string, error) {
	if pools == nil {
		pools = map[string]V{}
	}
	for name := range pools {
		if err := validPoolKey(name); err != nil {
			return "", err
		}
	}
	encoded, err := json.Marshal(pools)
	if err != nil {
		return "", fmt.Errorf("encoding %s: %w", what, err)
	}
	return string(encoded), nil
}

// decodePools reads a stored map, reporting whether it was readable at all. An
// empty value is no pools and IS readable, which is what an unseeded key holds.
func decodePools[V any](raw string) (map[string]V, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, true
	}
	var pools map[string]V
	if err := json.Unmarshal([]byte(raw), &pools); err != nil {
		return nil, false
	}
	for name := range pools {
		if err := validPoolKey(name); err != nil {
			return nil, false
		}
	}
	return pools, true
}

func outOfRange(value, ceiling int64) bool { return value < 0 || value > ceiling }
