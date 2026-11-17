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

package task

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStateIsTerminal(t *testing.T) {
	t.Parallel()
	assert.False(t, StatePending.IsTerminal())
	assert.False(t, StateLeased.IsTerminal())
	assert.True(t, StateDone.IsTerminal())
	assert.True(t, StateFailed.IsTerminal())
}

func TestNewDefaults(t *testing.T) {
	t.Parallel()
	before := time.Now()
	tk := New("device.create_cvd", "dev-1")

	assert.Equal(t, "device.create_cvd", tk.Type)
	assert.Equal(t, "dev-1", tk.ResourceID)
	assert.Equal(t, StatePending, tk.State)
	assert.Equal(t, DefaultMaxAttempts, tk.MaxAttempts)
	assert.Zero(t, tk.Attempts)
	assert.Empty(t, tk.ID, "ID is assigned by the queue at enqueue, not by New")
	assert.False(t, tk.AvailableAt.Before(before), "AvailableAt defaults to now")
}

func TestExhausted(t *testing.T) {
	t.Parallel()
	assert.False(t, (&Task{Attempts: 0, MaxAttempts: 3}).Exhausted())
	assert.False(t, (&Task{Attempts: 2, MaxAttempts: 3}).Exhausted())
	assert.True(t, (&Task{Attempts: 3, MaxAttempts: 3}).Exhausted())
	assert.True(t, (&Task{Attempts: 4, MaxAttempts: 3}).Exhausted())
}

func TestShardForRangeAndDeterminism(t *testing.T) {
	t.Parallel()
	const numShards = 16
	seen := make(map[int]int)
	for i := range 5000 {
		id := fmt.Sprintf("11111111-2222-3333-4444-%012d", i)
		s := ShardFor(id, numShards)
		require.GreaterOrEqual(t, s, 0)
		require.Less(t, s, numShards)
		assert.Equal(t, s, ShardFor(id, numShards), "deterministic in id")
		seen[s]++
	}
	// Every bucket should be exercised by a uniform-ish hash over 5000 ids.
	assert.Len(t, seen, numShards, "all shards covered")
}

func TestShardForDegenerate(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 0, ShardFor("anything", 1))
	assert.Equal(t, 0, ShardFor("anything", 0))
	assert.Equal(t, 0, ShardFor("anything", -4))
}

func TestBackoffBoundsAndCap(t *testing.T) {
	t.Parallel()
	base := 100 * time.Millisecond
	maxD := 10 * time.Second

	// For each attempt the result must lie in [d/2, d] where d is the capped
	// exponential. Sample repeatedly because of jitter.
	for attempt := 1; attempt <= 12; attempt++ {
		want := base
		for i := 1; i < attempt && want < maxD; i++ {
			want *= 2
		}
		if want > maxD {
			want = maxD
		}
		for range 200 {
			got := Backoff(attempt, base, maxD)
			assert.GreaterOrEqual(t, got, want/2, "attempt %d below lower bound", attempt)
			assert.LessOrEqual(t, got, want, "attempt %d above capped delay", attempt)
		}
	}
}

func TestBackoffMonotonicUpperBound(t *testing.T) {
	t.Parallel()
	base := 50 * time.Millisecond
	maxD := time.Hour
	// The capped delay (upper bound) must be non-decreasing in attempt; sample
	// the max over iterations to approximate it.
	var prev time.Duration
	for attempt := 1; attempt <= 10; attempt++ {
		var hi time.Duration
		for range 500 {
			if d := Backoff(attempt, base, maxD); d > hi {
				hi = d
			}
		}
		assert.GreaterOrEqual(t, hi, prev, "upper bound decreased at attempt %d", attempt)
		prev = hi
	}
}

func TestBackoffAttemptFloor(t *testing.T) {
	t.Parallel()
	base := 100 * time.Millisecond
	// attempt < 1 is treated as 1 (no panic, base-level delay).
	got := Backoff(0, base, time.Second)
	assert.GreaterOrEqual(t, got, base/2)
	assert.LessOrEqual(t, got, base)
}
