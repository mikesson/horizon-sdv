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

package device_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// Three callers apply the packing rule: the resolver sizing a host, the settings
// write telling an operator what a density can mean, and the card reporting the
// capacity a standby target stands for. They agree only because they call this.
//
// The live catalog's shapes are named in the cases on purpose. The numbers here
// are the ones the fleet's demand signal compares against, so a machine-type
// row changing under them has to show up as a failure and not as a silently
// different fleet.
func TestPackedSlots(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		vcpus, memoryGB int64
		want            int64
	}{
		{
			// The defect this rule exists to close: four 8 GB guests plus the
			// 4 GB host reserve want 36 GB from a 30 GB machine. The old
			// fleet-wide policy default packed them anyway.
			name:  "the default shape holds three",
			vcpus: 8, memoryGB: 30, want: 3,
		},
		{
			// n1-standard-16, what a four-instance group grows into: memory
			// holds 7 guests and the effective-CPU bound also 7.
			name:  "a grown machine packs to its own fit",
			vcpus: 16, memoryGB: 60, want: 7,
		},
		{
			// g2-standard-8, every single-device GPU create's shape.
			name:  "the GPU shape holds three",
			vcpus: 8, memoryGB: 32, want: 3,
		},
		{
			// g2-standard-4, the shape a GPU create resolves to BEFORE growth.
			// Judging a host against its pre-growth base would give it this.
			name:  "the ungrown GPU base holds one",
			vcpus: 4, memoryGB: 16, want: 1,
		},
		{
			// A row that states no memory is bounded by its width alone.
			name:  "a row with no memory takes the CPU bound",
			vcpus: 8, memoryGB: 0, want: 3,
		},
		{
			name:  "a row with no vCPUs takes the memory bound",
			vcpus: 0, memoryGB: 30, want: 3,
		},
		{
			// Not zero. A machine too small for one guest is still a machine
			// with one slot: the create that provisioned it has to land
			// somewhere, and the right answer is the oversubscription warning
			// whatever sized it already emits.
			name:  "a machine smaller than one guest still offers a slot",
			vcpus: 2, memoryGB: 4, want: 1,
		},
		{
			// The reserve is really subtracted. Most sizes hide a rule that
			// forgot it (40 GB holds 4 guests beside the reserve and 5
			// without it), and here the effective-CPU bound (three on eight
			// cores) is the tighter axis, which is the other rule this pins.
			name:  "the reserve comes out, and the tighter axis wins",
			vcpus: 8, memoryGB: 40, want: 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, device.PackedSlots(tc.vcpus, tc.memoryGB))
		})
	}
}

// The reserve and the default guest size are the two numbers every fit above is
// derived from, and both are consumed across a firewall boundary now. A change
// to either is a change to how many devices the whole fleet holds, so it should
// require an explicit edit here.
func TestPackingConstantsAreWhatTheFitsAssume(t *testing.T) {
	t.Parallel()
	assert.Equal(t, int64(4096), device.HostReserveMemoryMB)
	assert.Equal(t, int64(8192), device.DefaultInstanceMemoryMB)
}
