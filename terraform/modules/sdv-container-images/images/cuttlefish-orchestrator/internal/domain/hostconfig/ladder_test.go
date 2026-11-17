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

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// ladderCatalog is a catalog with one row per admission rule, so a validation
// that stopped applying one of them has a row that proves it.
func ladderCatalog() []*machinetype.MachineType {
	return []*machinetype.MachineType{
		{ID: "n2-standard-8", Family: "n2", Architecture: machinetype.ArchX86, VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true},
		{
			ID: "n1-standard-8", Family: "n1", Architecture: machinetype.ArchX86, VCPUs: 8, MemoryGB: 30, Fit: 3, IsEnabled: true,
			GPUMode: machinetype.GPUModeAttachable, DefaultGPUType: "nvidia-tesla-t4",
		},
		// Attachable, and the catalog names nothing to attach: provisioning
		// builds it without an accelerator, which is a machine the gpu pool can
		// count and never place on.
		{
			ID: "n1-highcpu-8", Family: "n1", Architecture: machinetype.ArchX86, VCPUs: 8, MemoryGB: 16, Fit: 1, IsEnabled: true,
			GPUMode: machinetype.GPUModeAttachable,
		},
		{
			ID: "c3-standard-8", Family: "c3", Architecture: machinetype.ArchX86, VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true,
			Zones: []string{"europe-west4-a"},
		},
		{ID: "n2-standard-4", Family: "n2", Architecture: machinetype.ArchX86, VCPUs: 4, MemoryGB: 16, Fit: 1, IsEnabled: true},
		{ID: "n2-standard-16", Family: "n2", Architecture: machinetype.ArchX86, VCPUs: 16, MemoryGB: 64, IsEnabled: false},
		{
			ID: "g2-standard-8", Family: "g2", Architecture: machinetype.ArchX86, VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true,
			GPUMode: machinetype.GPUModeIntegrated, IntegratedGPUType: "nvidia-l4", IntegratedGPUCount: 1,
		},
		{ID: "c4a-standard-8-metal", Family: "c4a", Architecture: machinetype.ArchArm, VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true},
		{ID: "c4a-standard-8", Family: "c4a", Architecture: machinetype.ArchArm, VCPUs: 8, MemoryGB: 32, IsEnabled: true},
		{ID: "n4-standard-8-metal", Family: "n4", Architecture: machinetype.ArchX86, VCPUs: 8, MemoryGB: 32, IsEnabled: true},
	}
}

// ladderReasons is what the catalog says about the shapes it does not offer.
// The three refusals have three remedies, and the validation may only report
// the one the catalog actually gave.
func ladderReasons() hostconfig.WhyUnoffered {
	return func(machineType string) error {
		switch machineType {
		case "n1-highmem-2":
			return &machinetype.UnofferedError{
				MachineType: machineType, Reason: machinetype.ReasonBelowFloor,
				Family: "n1", VCPUs: 2, MemoryGB: 13,
			}
		case "n7-standard-8":
			return &machinetype.UnofferedError{
				MachineType: machineType, Reason: machinetype.ReasonFamilyNotCurated, Family: "n7",
			}
		default:
			return nil
		}
	}
}

// Refusing a rung is the half of the ladder that does the work: the catalog is
// the only place that knows which shapes can run Cuttlefish here, so a validation
// that admitted an arbitrary id would answer a stockout with machines that boot
// and cannot launch a device. Each row names the rule, and the refusal has to
// name it too: an operator told only "invalid" cannot tell a typo from a shape
// they have to enable.
//
// Which rule applies depends on the pool that will own the ladder, so the rows
// name one: a shape refused in one pool is the shape another exists to buy, and
// a validation reading the same rules everywhere would refuse every GPU pool's
// ladder while admitting a GPU shape into the CPU pool's.
func TestValidateShapeLadderRefusesEachRuleByName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		pool  string
		rungs []string
		says  string
	}{
		{"a family nobody curated", host.PoolDefault, []string{"n2-standard-8", "n7-standard-8"}, "enable its machine family first"},
		// The refusal an operator hit on a live deployment: the n1 family IS
		// enabled, and the machine is simply too narrow for one guest. Told to
		// enable the family, they would change nothing and try again.
		{"below the capacity floor", host.PoolDefault, []string{"n1-highmem-2"}, "2 vCPUs and 13 GB hold no Android VM"},
		{"the override that admits it", host.PoolDefault, []string{"n1-highmem-2"}, "per-shape capacity override"},
		{"a name the catalog cannot explain", host.PoolDefault, []string{"n9-standard-8"}, "not a machine type the catalog offers"},
		{"disabled", host.PoolDefault, []string{"n2-standard-16"}, "disabled"},
		{"arm", host.PoolDefault, []string{"c4a-standard-8"}, "x86"},
		{"integrated GPU", host.PoolDefault, []string{"g2-standard-8"}, "integrated GPU"},
		{"bare metal", host.PoolDefault, []string{"n4-standard-8-metal"}, "bare metal"},
		{"repeated", host.PoolDefault, []string{"n2-standard-8", "n1-standard-8", "n2-standard-8"}, "more than once"},
		{"empty rung", host.PoolDefault, []string{"n2-standard-8", "  "}, "must name a machine type"},
		{
			"too long", host.PoolDefault,
			[]string{"a", "b", "c", "d", "e", "f", "g", "h", "i"},
			"must not exceed 8 rungs",
		},
		{"no GPU in the gpu pool", host.PoolGPU, []string{"n2-standard-8"}, "carries no gpu"},
		{"nothing to attach in the gpu pool", host.PoolGPU, []string{"n1-highcpu-8"}, "carries no gpu"},
		{"bare metal in the gpu pool", host.PoolGPU, []string{"n4-standard-8-metal"}, "bare metal"},
		{"x86 in the arm pool", host.PoolARM, []string{"n2-standard-8"}, "provisions arm64"},
		{"not bare metal in the arm pool", host.PoolARM, []string{"c4a-standard-8"}, "bare metal alone"},
		{"a pool this platform has not", "experiment", []string{"n2-standard-8"}, "no pool this platform has"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := hostconfig.ValidateShapeLadder(tc.pool, tc.rungs, ladderCatalog(), ladderReasons())
			require.Error(t, err, "the rule must refuse this ladder")
			assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tc.says),
				"the refusal must name the rule it applied")
		})
	}
}

// The other half of a refusal test: a ladder every rule admits has to get
// through, in each pool that admits it. Without this, a validation that refused
// everything would pass every assertion above.
func TestValidateShapeLadderAcceptsWhatEachPoolAdmits(t *testing.T) {
	t.Parallel()
	require.NoError(t, hostconfig.ValidateShapeLadder(host.PoolDefault,
		[]string{"n2-standard-8", "n1-standard-8", "c3-standard-8"}, ladderCatalog(), ladderReasons()))
	require.NoError(t, hostconfig.ValidateShapeLadder(host.PoolGPU,
		[]string{"g2-standard-8", "n1-standard-8"}, ladderCatalog(), ladderReasons()),
		"the gpu pool takes the integrated shape the default pool refuses, and an attachable one the catalog names a GPU for")
	require.NoError(t, hostconfig.ValidateShapeLadder(host.PoolARM,
		[]string{"c4a-standard-8-metal"}, ladderCatalog(), ladderReasons()),
		"the arm pool takes the bare metal every x86 pool refuses")
	require.NoError(t, hostconfig.ValidateShapeLadder(host.PoolDefault, nil, ladderCatalog(), ladderReasons()),
		"no ladder is the catalog default, which is always a legal thing to ask for")
}

// A ladder written for a pool this platform has not is refused for the pool,
// whatever the catalog thinks of the shapes in it. The catalog's sentence
// there would answer a question nobody asked.
func TestValidateShapeLadderKeepsThePoolRefusalAheadOfTheCatalogs(t *testing.T) {
	t.Parallel()
	err := hostconfig.ValidateShapeLadder("experiment", []string{"n1-highmem-2"}, ladderCatalog(), ladderReasons())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pool this platform has")
	assert.NotContains(t, err.Error(), "capacity override")
}

// The stored order and the order the fleet walks are two different lists the
// moment the catalog moves under one, and a surface that only had the second
// would draw a preference silently gone. JudgeLadder keeps both, and
// ResolveLadder is its admitted half, so the two can never disagree about what
// a rung is.
func TestJudgeLadderKeepsTheRefusedRungsResolveLadderDrops(t *testing.T) {
	t.Parallel()
	stored := []string{"n2-standard-8", "n2-standard-16", "n7-standard-8", "n1-standard-8"}
	judged := hostconfig.JudgeLadder(host.PoolDefault, stored, ladderCatalog(), ladderReasons())
	require.Len(t, judged, len(stored), "every stored rung is judged, in the operator's order")

	assert.Empty(t, judged[0].Refusal)
	assert.Equal(t, "n2-standard-8", judged[0].Rung.MachineType)
	assert.Contains(t, judged[1].Refusal, "disabled")
	assert.Contains(t, judged[2].Refusal, "not a machine type the catalog offers")
	assert.Equal(t, "n7-standard-8", judged[2].MachineType,
		"a refused rung still names the shape the operator wrote; it is all it has")
	assert.Empty(t, judged[2].Rung.MachineType, "a refused rung carries no resolved machine")

	resolved := hostconfig.ResolveLadder(host.PoolDefault, stored, ladderCatalog())
	require.Len(t, resolved, 2, "the walk takes only what the pool still admits")
	assert.Equal(t, "n2-standard-8", resolved[0].MachineType)
	assert.Equal(t, "n1-standard-8", resolved[1].MachineType)
}

// A rung the catalog withholds is explained by the catalog wherever it is
// refused. Judged with the generic sentence, a page would tell an operator to
// take the catalog's word for it while the editor that refused the same rung
// named the override that admits it, leaving two accounts of one stored shape.
func TestJudgeLadderRefusesAnUnofferedRungInTheCatalogsOwnWords(t *testing.T) {
	t.Parallel()
	stored := []string{"n1-highmem-2"}
	judged := hostconfig.JudgeLadder(host.PoolDefault, stored, ladderCatalog(), ladderReasons())
	require.Len(t, judged, 1)
	assert.Contains(t, judged[0].Refusal, "2 vCPUs and 13 GB hold no Android VM")
	assert.Contains(t, judged[0].Refusal, "per-shape capacity override")

	write := hostconfig.ValidateShapeLadder(host.PoolDefault, stored, ladderCatalog(), ladderReasons())
	require.Error(t, write)
	assert.Contains(t, write.Error(), judged[0].Refusal,
		"the write path and the view must refuse one rung in one sentence")

	// Provisioning drops its refusals, so it hands no catalog over; the rung is
	// still out, in the words that name no remedy.
	generic := hostconfig.JudgeLadder(host.PoolDefault, stored, ladderCatalog(), nil)
	require.Len(t, generic, 1)
	assert.Contains(t, generic[0].Refusal, "the catalog is the only authority")
	assert.NotContains(t, generic[0].Refusal, "capacity override")
}

// The catalog moves under a stored ladder. Disabling a shape costs the fleet
// that rung and nothing else. Dropping the whole ladder would silently revert
// the fleet's preferred shape, which is the opposite of what the operator did.
func TestResolveLadderDropsOnlyTheRungsTheCatalogNoLongerAdmits(t *testing.T) {
	t.Parallel()
	rungs := hostconfig.ResolveLadder(host.PoolDefault,
		[]string{"n2-standard-16", "n2-standard-8", "g2-standard-8", "n1-standard-8", "n7-standard-8"},
		ladderCatalog())
	require.Len(t, rungs, 2)
	assert.Equal(t, "n2-standard-8", rungs[0].MachineType)
	assert.Equal(t, "n2", rungs[0].Family)
	assert.Equal(t, "n1-standard-8", rungs[1].MachineType,
		"the operator's order survives the drop")
}

// A rung whose every ranked zone carries a fresh stockout observation is
// skipped; one whose observation has decayed is probed again. This is the whole
// of the ladder's memory, and it is why a transient stockout cannot demote the
// fleet for a day.
func TestFirstProbableRungSkipsTheStockedOutAndReEarnsTheDecayed(t *testing.T) {
	t.Parallel()
	rungs := hostconfig.ResolveLadder(host.PoolDefault,
		[]string{"n2-standard-8", "n1-standard-8", "c3-standard-8"}, ladderCatalog())
	require.Len(t, rungs, 3)
	pool := []string{"europe-west4-a", "europe-west4-b"}

	i, ok := hostconfig.FirstProbableRung(rungs, 0, 1, pool, nil)
	require.True(t, ok)
	assert.Zero(t, i, "with nothing remembered, the walk starts at the operator's preference")

	stockedOut := map[string]map[string]bool{
		"n2": {"europe-west4-a": true, "europe-west4-b": true},
	}
	i, ok = hostconfig.FirstProbableRung(rungs, 0, 1, pool, stockedOut)
	require.True(t, ok)
	assert.Equal(t, 1, i, "every zone of rung 0 is stocked out, so rung 1 is the first worth probing")

	// One zone left is a zone worth probing: the skip is "nowhere left", never
	// "somewhere failed".
	partial := map[string]map[string]bool{"n2": {"europe-west4-a": true}}
	i, ok = hostconfig.FirstProbableRung(rungs, 0, 1, pool, partial)
	require.True(t, ok)
	assert.Zero(t, i)

	// The observation decayed: the preference is re-earned with no other change.
	i, ok = hostconfig.FirstProbableRung(rungs, 0, 1, pool, nil)
	require.True(t, ok)
	assert.Zero(t, i)
}

// A rung the catalog offers nowhere the walk is ranking has nothing to probe,
// and a rung too small for the slot ceiling the host already promised would
// overcommit devices already admitted against it. Both are skipped rather than
// landed on.
func TestFirstProbableRungSkipsUnreachableAndUndersizedRungs(t *testing.T) {
	t.Parallel()
	rungs := hostconfig.ResolveLadder(host.PoolDefault,
		[]string{"c3-standard-8", "n2-standard-4", "n2-standard-8"}, ladderCatalog())
	require.Len(t, rungs, 3)

	// c3 is offered only in europe-west4-a, which is not in this pool.
	i, ok := hostconfig.FirstProbableRung(rungs, 0, 1, []string{"us-central1-a"}, nil)
	require.True(t, ok)
	assert.Equal(t, 1, i)

	// An n2-standard-4 holds one 8 GB guest beside the host reserve, so a host
	// already promising three slots steps over it to the shape that holds them.
	i, ok = hostconfig.FirstProbableRung(rungs, 1, 3, []string{"europe-west4-a"}, nil)
	require.True(t, ok)
	assert.Equal(t, 2, i)

	_, ok = hostconfig.FirstProbableRung(rungs, 0, 3,
		[]string{"europe-west4-a"}, map[string]map[string]bool{
			"c3": {"europe-west4-a": true}, "n2": {"europe-west4-a": true},
		})
	assert.False(t, ok, "a ladder with nothing left to probe reports so rather than picking a rung")
}

// What a rung holds and whether it can carry a ceiling are one judgement asked
// twice. A surface reporting a rung's slots and a walk refusing to step onto it
// must never be able to disagree, which is why Holds is expressed
// through Slots rather than beside it.
func TestRungSlotsAndHoldsAreOneJudgement(t *testing.T) {
	t.Parallel()
	rungs := hostconfig.ResolveLadder(host.PoolDefault,
		[]string{"n2-standard-8", "n2-standard-4"}, ladderCatalog())
	require.Len(t, rungs, 2)

	// The rung carries the composed fit: three on the 32 GB shape, one on the
	// 16 GB one.
	assert.Equal(t, int64(3), rungs[0].Slots())
	assert.Equal(t, int64(1), rungs[1].Slots())

	assert.True(t, rungs[0].Holds(rungs[0].Slots()),
		"a rung must hold the ceiling its own shape produced")
	assert.False(t, rungs[1].Holds(rungs[0].Slots()),
		"a rung that packs fewer slots cannot carry the ceiling the rung above promised")

	unmeasurable := hostconfig.LadderRung{MachineType: "n2-standard-8", Family: "n2"}
	assert.True(t, unmeasurable.Holds(4),
		"a catalog row that states no size states no bound; refusing every shape against it would strand the walk")
}

// The families are the key set one stockout read covers, so a ladder that lists
// two shapes of one family must not ask about it twice. The single read exists
// so that eight rungs cost what one does.
func TestLadderFamiliesAreTheReadsKeySetWithoutRepeats(t *testing.T) {
	t.Parallel()
	rungs := hostconfig.ResolveLadder(host.PoolDefault,
		[]string{"n2-standard-8", "n1-standard-8", "n2-standard-4"}, ladderCatalog())
	require.Len(t, rungs, 3)
	assert.Equal(t, []string{"n2", "n1"}, hostconfig.LadderFamilies(rungs),
		"the operator's order survives, and a family appears once")
}

// The zones a rung can go to are the pool narrowed by the catalog's own
// availability list, and an empty list means the shape is offered everywhere.
// It is the same narrowing the stockout question applies, so a count of zones
// and a verdict about them describe the same set.
func TestPlaceableZonesNarrowsByCatalogAvailability(t *testing.T) {
	t.Parallel()
	rungs := hostconfig.ResolveLadder(host.PoolDefault,
		[]string{"c3-standard-8", "n2-standard-8"}, ladderCatalog())
	require.Len(t, rungs, 2)
	pool := []string{"europe-west4-a", "europe-west4-b"}

	assert.Equal(t, []string{"europe-west4-a"}, rungs[0].PlaceableZones(pool),
		"c3 is offered in one of these zones")
	assert.Equal(t, pool, rungs[1].PlaceableZones(pool),
		"a shape with no availability list is offered everywhere the pool ranks")
}

// The walk's position is derived from the host's own shape, never remembered.
// A shape that left the ladder walks no further, which is what keeps a
// reordered ladder from stepping a host somewhere its operator never listed.
func TestLadderIndexIsDerivedFromTheShape(t *testing.T) {
	t.Parallel()
	rungs := hostconfig.ResolveLadder(host.PoolDefault, []string{"n2-standard-8", "n1-standard-8"}, ladderCatalog())
	assert.Equal(t, 1, hostconfig.LadderIndex(rungs, "n1-standard-8"))
	assert.Equal(t, -1, hostconfig.LadderIndex(rungs, "c3-standard-8"))
}

// The fallback is a served fact, and it has to name BOTH shapes: a warning
// naming only what was built cannot be told from an operator's own choice.
func TestShapeFallbackWarningNamesBothShapes(t *testing.T) {
	t.Parallel()
	warning := hostconfig.ShapeFallbackWarning("n2-standard-8", "n1-standard-8")
	assert.Contains(t, warning, "n2-standard-8")
	assert.Contains(t, warning, "n1-standard-8")
	assert.Contains(t, warning, "stocked out")
}

// A rung states the accelerator a machine built on it carries, and only in the
// pool where one attaches: the gpu pool's integrated rungs carry their own,
// its attachable rungs carry the curated default with the attach's floor of
// one, and the same shape resolved for the default pool carries nothing,
// because nothing attaches where no create wants one.
func TestResolveLadderStatesTheGPUAMachineWouldCarry(t *testing.T) {
	t.Parallel()
	gpu := hostconfig.ResolveLadder(host.PoolGPU,
		[]string{"g2-standard-8", "n1-standard-8"}, ladderCatalog())
	if len(gpu) != 2 {
		t.Fatalf("resolved %d gpu rungs, want 2", len(gpu))
	}
	if gpu[0].GPUType != "nvidia-l4" || gpu[0].GPUCount != 1 {
		t.Errorf("integrated rung carries %q × %d, want nvidia-l4 × 1", gpu[0].GPUType, gpu[0].GPUCount)
	}
	if gpu[1].GPUType != "nvidia-tesla-t4" || gpu[1].GPUCount != 1 {
		t.Errorf("attachable rung carries %q × %d, want the curated default floored at one", gpu[1].GPUType, gpu[1].GPUCount)
	}

	deflt := hostconfig.ResolveLadder(host.PoolDefault, []string{"n1-standard-8"}, ladderCatalog())
	if len(deflt) != 1 || deflt[0].GPUType != "" || deflt[0].GPUCount != 0 {
		t.Errorf("default-pool rung carries %+v, want no accelerator; nothing attaches there", deflt)
	}
}

// The width decides the count: one T4 serves 24 vCPUs, and Compute Engine
// refuses an insert under that ratio, so the rung must state, and the attach
// must request, the count the shape actually needs.
func TestResolveLadderScalesTheAttachableCountToTheWidth(t *testing.T) {
	t.Parallel()
	catalog := append(ladderCatalog(), &machinetype.MachineType{
		ID: "n1-standard-96", Family: "n1", Architecture: machinetype.ArchX86,
		VCPUs: 96, MemoryGB: 360, Fit: 44, IsEnabled: true,
		GPUMode: machinetype.GPUModeAttachable, DefaultGPUType: "nvidia-tesla-t4",
		DefaultGPUCount: 1, MaxGPUs: 4,
	}, &machinetype.MachineType{
		ID: "n1-standard-32", Family: "n1", Architecture: machinetype.ArchX86,
		VCPUs: 32, MemoryGB: 120, Fit: 14, IsEnabled: true,
		GPUMode: machinetype.GPUModeAttachable, DefaultGPUType: "nvidia-tesla-t4",
		DefaultGPUCount: 1, MaxGPUs: 4,
	})
	rungs := hostconfig.ResolveLadder(host.PoolGPU,
		[]string{"n1-standard-8", "n1-standard-32", "n1-standard-96"}, catalog)
	if len(rungs) != 3 {
		t.Fatalf("resolved %d rungs, want 3", len(rungs))
	}
	for i, want := range []int64{1, 2, 4} {
		if rungs[i].GPUCount != want {
			t.Errorf("%s carries %d GPUs, want %d", rungs[i].MachineType, rungs[i].GPUCount, want)
		}
	}
}
