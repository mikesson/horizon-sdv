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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// Which misses a standing buffer could have absorbed. This rule decides what
// the autoscaler spends money on, and it used to live in the demand query where
// only an emulator could reach it.
//
// The axes are independent on purpose: each names a different thing to fix, and
// a create can fail more than one.
func TestMissReasons(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		miss Miss
		want []Reason
	}{
		{
			// The ordinary miss the buffer exists for.
			name: "a plain miss is servable",
			miss: Miss{
				GPURequired: new(false), FitsDefaultHost: new(true),
				MaxDevices: new(int64(3)), Density: new(int64(3)),
			},
		},
		{
			// The row the whole discriminator rests on. An INTERACTIVE create
			// prefers a GPU and provisions one, but placement degrades an unmet
			// preference onto a CPU host, so standby serves it, and a rule
			// keyed on the provisioned shape would delete this population.
			name: "a GPU preference is still servable",
			miss: Miss{
				GPURequired: new(false), FitsDefaultHost: new(true),
				MaxDevices: new(int64(3)), Density: new(int64(3)),
			},
		},
		{
			name: "a required GPU is not",
			miss: Miss{
				GPURequired: new(true), FitsDefaultHost: new(true),
				MaxDevices: new(int64(3)), Density: new(int64(3)),
			},
			want: []Reason{ReasonGPURequired},
		},
		{
			name: "a shape the default machine cannot hold is not",
			miss: Miss{
				GPURequired: new(false), FitsDefaultHost: new(false),
				MaxDevices: new(int64(3)), Density: new(int64(3)),
			},
			want: []Reason{ReasonNeedsLargerHost},
		},
		{
			// The four-instance default group: it grows the MACHINE without
			// growing the slot count the policy allows, so this is the axis
			// that catches it once the packing ceiling is truthful.
			name: "a host that took more slots than standby offers is not",
			miss: Miss{
				GPURequired: new(false), FitsDefaultHost: new(true),
				MaxDevices: new(int64(4)), Density: new(int64(3)),
			},
			want: []Reason{ReasonOversizedGroup},
		},
		{
			// Per reason, never first-fail. Reporting only the GPU would hide
			// the sizing problem until somebody bought GPU capacity.
			name: "a miss can fail more than one axis",
			miss: Miss{
				GPURequired: new(true), FitsDefaultHost: new(false),
				MaxDevices: new(int64(8)), Density: new(int64(3)),
			},
			want: []Reason{ReasonGPURequired, ReasonNeedsLargerHost, ReasonOversizedGroup},
		},
		{
			// An entry written before these columns existed. A miss nobody can
			// judge is still a miss, and the fail-safe direction is to keep
			// counting demand, never to discard it silently.
			name: "an unstamped miss is servable",
			miss: Miss{},
		},
		{
			// Half-stamped, because the slot pair is written by an older
			// revision than the boolean pair. Neither half may fill in for the
			// other.
			name: "a v73 entry is judged on the axis it stamped",
			miss: Miss{MaxDevices: new(int64(8)), Density: new(int64(4))},
			want: []Reason{ReasonOversizedGroup},
		},
		{
			// One half of the slot pair alone compares against nothing. A rule
			// treating a missing density as zero would call every such entry
			// oversized.
			name: "a ceiling with no standby figure is not a verdict",
			miss: Miss{MaxDevices: new(int64(8))},
		},
		{
			// Equal is servable: the buffer holds exactly this.
			name: "a ceiling equal to the standby figure is servable",
			miss: Miss{MaxDevices: new(int64(3)), Density: new(int64(3))},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.miss.Reasons())
			assert.Equal(t, len(tc.want) == 0, tc.miss.Servable())
		})
	}
}

// The window's two views of one read. They are derived from the same slice at
// the same instant, which is the point: a card whose split disagreed with the
// target it sits beside is indistinguishable from a broken signal.
func TestDemandWindowSplitsOneRead(t *testing.T) {
	t.Parallel()
	now := time.Now()
	window := DemandWindow{Misses: []Miss{
		{At: now.Add(-5 * time.Minute), GPURequired: new(false), FitsDefaultHost: new(true)},
		{At: now.Add(-4 * time.Minute), GPURequired: new(true), FitsDefaultHost: new(true)},
		{At: now.Add(-3 * time.Minute), GPURequired: new(false), FitsDefaultHost: new(false)},
		// Two axes at once, counted under both.
		{At: now.Add(-2 * time.Minute), GPURequired: new(true), FitsDefaultHost: new(false)},
		{At: now.Add(-time.Minute), MaxDevices: new(int64(4)), Density: new(int64(3))},
	}}

	times := window.ServableTimes()
	assert.Len(t, times, 1, "only the first miss could have landed on a standby host")
	assert.Equal(t, now.Add(-5*time.Minute), times[0])

	split := window.Split()
	assert.Equal(t, DemandSplit{
		Servable:        1,
		Unservable:      4,
		GPURequired:     2,
		NeedsLargerHost: 2,
		OversizedGroup:  1,
	}, split)

	// The two halves partition the window exactly. The card states them side by
	// side, so a miss counted in neither, or in both, would show an operator a
	// window that does not add up to what was read.
	assert.Equal(t, int64(len(window.Misses)), split.Servable+split.Unservable)
	// The axes do not, on purpose. A miss failing two is a create with two
	// separate things to fix, and the sum exceeding Unservable is the evidence
	// these are populations rather than shares.
	assert.Greater(t, split.GPURequired+split.NeedsLargerHost+split.OversizedGroup, split.Unservable)

	// The target's population and the split's servable arm are one number
	// derived twice; a divergence here is the card and the keeper disagreeing.
	assert.Equal(t, int64(len(times)), split.Servable)
}

// An empty window is the disabled-signal and unwired-reader case, and it must
// answer without panicking or inventing demand.
func TestDemandWindowEmpty(t *testing.T) {
	t.Parallel()
	var window DemandWindow
	assert.Empty(t, window.ServableTimes())
	assert.Equal(t, DemandSplit{}, window.Split())
}

// One read, split into the pools the misses belong to, never a read per pool,
// which would put the number of configured pools inside a query the keeper runs
// every cycle.
//
// A miss carrying no pool belongs to the default one, the same rule a machine's
// unstamped column follows. Anything else would strand the demand a fleet
// recorded before it was partitioned in a pool nothing is judging.
func TestDemandWindowSplitsByPool(t *testing.T) {
	t.Parallel()
	now := time.Now()
	window := DemandWindow{
		Misses: []Miss{
			{At: now.Add(-5 * time.Minute)},
			{At: now.Add(-4 * time.Minute), Pool: DefaultTier},
			{At: now.Add(-3 * time.Minute), Pool: "gpu"},
		},
		Partial: true,
	}

	pools := window.ByPool()
	assert.Len(t, pools, 2, "a fleet with misses in two pools splits into two")
	assert.Len(t, pools[DefaultTier].Misses, 2, "an unstamped miss is the default pool's")
	assert.Len(t, pools["gpu"].Misses, 1)
	assert.Equal(t, now.Add(-5*time.Minute), pools[DefaultTier].Misses[0].At,
		"each part keeps the read's own order")
	// A truncated tail is a fact about the READ, so no part of the window may
	// claim to hold the whole of anything.
	assert.True(t, pools[DefaultTier].Partial)
	assert.True(t, pools["gpu"].Partial)
}

// Servability is judged inside the miss's OWN pool, because that is the standing
// capacity that would have answered it.
//
// With the gpu pool switched off the derivation leaves such a create in the
// default pool, where no standing host carries an accelerator, so the miss is
// beyond the fleet and must not raise a target that buys CPU hosts. Switch the
// pool on and the same miss is stamped there, where it is ordinary demand the
// keeper can buy for. Keep the rule pool-blind and the gpu pool's target never
// leaves zero however much demand arrives.
func TestGPURequiredIsUnservableOnlyOutsideThePoolThatCarriesGPUs(t *testing.T) {
	t.Parallel()
	required := true
	fits := true
	for _, tc := range []struct {
		name     string
		pool     string
		servable bool
	}{
		{"in the default pool", host.PoolDefault, false},
		{"in an entry that names no pool", "", false},
		{"in the gpu pool", host.PoolGPU, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			miss := Miss{
				At: time.Now(), Pool: tc.pool,
				GPURequired: &required, FitsDefaultHost: &fits,
			}
			if got := miss.Servable(); got != tc.servable {
				t.Errorf("servable = %v, want %v (reasons %v)", got, tc.servable, miss.Reasons())
			}
		})
	}
}
