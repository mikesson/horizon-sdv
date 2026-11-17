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

package placement

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// The live catalog's shapes, so a case names the machine it is about.
var (
	n1Standard8  = &machinetype.MachineType{ID: "n1-standard-8", Family: "n1", VCPUs: 8, MemoryGB: 30}
	n1Standard16 = &machinetype.MachineType{ID: "n1-standard-16", Family: "n1", VCPUs: 16, MemoryGB: 60}
	g2Standard4  = &machinetype.MachineType{ID: "g2-standard-4", Family: "g2", VCPUs: 4, MemoryGB: 16}
	g2Standard8  = &machinetype.MachineType{ID: "g2-standard-8", Family: "g2", VCPUs: 8, MemoryGB: 32}
	sizelessRow  = &machinetype.MachineType{ID: "broken", Family: "n1"}
)

// fakePolicy answers system_config reads; an absent key reads as not found,
// which every policy resolver here takes as the compiled default.
type fakePolicy map[string]string

func (f fakePolicy) Get(_ context.Context, key string) (*models.SystemConfig, error) {
	v, ok := f[key]
	if !ok {
		return nil, cspanner.ErrKeyNotFound
	}
	return &models.SystemConfig{Key: key, Value: v}, nil
}

func resolverWithPolicy(p fakePolicy) *Resolver {
	return NewResolver(ResolverDeps{Policy: p})
}

// The ceiling a create takes on a machine. The memory bound applies to the
// operator's density and to nothing else. The create's own instance count is
// what it needs and is never bounded away.
func TestSlotCeiling(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		mt        *machinetype.MachineType
		density   int64
		instances int64
		want      int64
	}{
		{
			// The oversubscription fix itself: the shipped default packed four
			// 8 GB guests onto 30 GB beside a 4 GB reserve.
			name: "one device on the default shape", mt: n1Standard8,
			instances: 1, want: 3,
		},
		{
			name: "a group that fits the default shape", mt: n1Standard8,
			instances: 3, want: 3,
		},
		{
			// The row the demand signal turns on. Four default instances grow
			// the machine, whose own fit exceeds what a standby host offers.
			name: "four instances on the grown machine", mt: n1Standard16,
			instances: 4, want: 7,
		},
		{
			name: "five instances fit the grown machine", mt: n1Standard16,
			instances: 5, want: 7,
		},
		{
			// growShapeToFit's no-fit path: the family's largest type, returned
			// with an oversubscription warning. Here the memory bound is BELOW
			// the instance count, and a rule that let it win would refuse the
			// group from the very host provisioned for it. min-outermost goes
			// red here and nowhere else in this table.
			name: "the no-fit path still admits its group", mt: n1Standard8,
			instances: 8, want: 8,
		},
		{
			// Every single-device GPU create's shape, after growth.
			name: "a GPU host", mt: g2Standard8,
			instances: 1, want: 3,
		},
		{
			// The same create judged against its PRE-growth base. Nothing
			// should ever do this. It is what a ceiling computed before
			// growShapeToFit ran would answer, and the gap from 3 is the
			// mutation this case kills.
			name: "the ungrown GPU base is not the machine", mt: g2Standard4,
			instances: 1, want: 1,
		},
		{
			// A row that states no size states no bound, and carries the one
			// slot the floor can promise.
			name: "a sizeless catalog row", mt: sizelessRow,
			instances: 1, want: 1,
		},
		{
			// The per-host ceiling is the last word, even for a group that
			// wants more slots than any host may offer.
			name: "the per-host cap binds last", mt: n1Standard16,
			instances: 40, want: 10,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, slotCeiling(tc.mt, tc.instances))
		})
	}
}

// The ceiling and the stamp answer different questions about different
// machines, and the whole demand signal rests on them being allowed to differ:
// a create whose ceiling exceeds what a standby host offers is a create no
// standing buffer could have absorbed.
//
// A single expression serving both, which is the shape this replaced, makes
// the two equal by construction, and every four-instance group then reads as
// ordinary demand the fleet could have answered.
func TestHostCapacityForSeparatesTheCeilingFromTheStamp(t *testing.T) {
	t.Parallel()
	r := resolverWithPolicy(fakePolicy{})

	// A four-instance group, grown to n1-standard-16, against the default
	// n1-standard-8 template.
	ceiling, stamp := r.hostCapacityFor(4, n1Standard16, n1Standard8)
	assert.Equal(t, int64(7), ceiling, "the grown machine packs to its own fit")
	assert.Equal(t, int64(3), stamp, "a standby host would have offered three")
	assert.Greater(t, ceiling, stamp,
		"the gap is what the demand signal reads; equal numbers make the group look absorbable")
}

// An ordinary single-device create is the case the buffer exists for, so its
// ceiling and the stamp must agree, or every miss reads as unservable and the
// target never rises.
func TestHostCapacityForOrdinaryCreateMatchesTheStamp(t *testing.T) {
	t.Parallel()
	r := resolverWithPolicy(fakePolicy{})
	ceiling, stamp := r.hostCapacityFor(1, n1Standard8, n1Standard8)
	assert.Equal(t, int64(3), ceiling)
	assert.Equal(t, stamp, ceiling)
}

// The stamp is judged against the DEFAULT template, never against the machine
// this create resolved to. A GPU create provisions a g2 and is still absorbable
// by CPU standby, since PlaceHost degrades a GPU preference onto a CPU host,
// so a stamp taken from the g2 would describe a buffer that does not exist.
func TestHostCapacityForStampIgnoresTheResolvedMachine(t *testing.T) {
	t.Parallel()
	r := resolverWithPolicy(fakePolicy{})
	_, stamp := r.hostCapacityFor(1, g2Standard8, n1Standard8)
	assert.Equal(t, int64(3), stamp, "the standby figure is about the default template")
}

// The ceiling and the stamp are each machine's own fit, with no fleet scalar
// in the way, so the two differ exactly when the machines do.
func TestHostCapacityForIsTheMachinesOwnFit(t *testing.T) {
	t.Parallel()
	r := resolverWithPolicy(fakePolicy{})
	ceiling, stamp := r.hostCapacityFor(1, n1Standard16, n1Standard8)
	assert.Equal(t, int64(7), ceiling, "n1-standard-16 holds seven")
	assert.Equal(t, int64(3), stamp, "the standby template n1-standard-8 holds three")
}
