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
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype/machinetypetest"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// ladderRanker answers per shape: a ladder asks the ranker a different question
// per rung, and a fake that answered every shape identically could not tell a
// rung step from a zone choice.
type ladderRanker struct {
	byMachineType map[string][]gcecatalog.ZoneCandidate
	fallback      []gcecatalog.ZoneCandidate
	calls         int
}

func (f *ladderRanker) RankZones(_ context.Context, req gcecatalog.ValidationRequest, _ gcecatalog.RankOptions) ([]gcecatalog.ZoneCandidate, error) {
	f.calls++
	if shaped, ok := f.byMachineType[req.MachineType]; ok {
		return shaped, nil
	}
	return f.fallback, nil
}

// ladderMemory serves the stockout observations a test seeded and counts the
// reads it took, so a resolution that grew a read per rung fails rather than
// merely getting slower.
type ladderMemory struct {
	cells    []*gcecatalog.ZoneHealth
	reads    int
	families []string
}

func (f *ladderMemory) ListCapacityCells(_ context.Context, families []string) ([]*gcecatalog.ZoneHealth, error) {
	f.reads++
	f.families = families
	return f.cells, nil
}

func ladderCell(zone, family string, in time.Duration) *gcecatalog.ZoneHealth {
	until := time.Now().Add(in)
	return &gcecatalog.ZoneHealth{
		Zone: zone, MachineFamily: family,
		BindingMetric: gcecatalog.ZoneMetricCapacity, CooldownUntil: &until,
	}
}

// The catalog these tests resolve against: a default shape nobody listed, and
// two enabled x86 CPU shapes in different families that a ladder may.
func ladderCatalog() *machinetypetest.FakeRepo {
	return machinetypetest.NewFakeRepo(
		&machinetype.MachineType{
			ID: "c3-standard-8", Family: "c3", Architecture: machinetype.ArchX86,
			VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true, IsDefault: true,
		},
		&machinetype.MachineType{
			ID: "n2-standard-8", Family: "n2", Architecture: machinetype.ArchX86,
			VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true,
		},
		&machinetype.MachineType{
			ID: "n1-standard-16", Family: "n1", Architecture: machinetype.ArchX86,
			VCPUs: 16, MemoryGB: 60, Fit: 7, IsEnabled: true,
		},
	)
}

func ladderResolver(policy fakePolicy, ranker *ladderRanker, memory *ladderMemory) *Resolver {
	return NewResolver(ResolverDeps{
		Policy:       policy,
		MachineTypes: ladderCatalog(),
		Ranker:       ranker,
		ZoneHealth:   memory,
		Logger:       slog.New(slog.DiscardHandler),
	})
}

func headlessRanker() *ladderRanker {
	return &ladderRanker{
		fallback: []gcecatalog.ZoneCandidate{
			{Zone: "europe-west4-a", Region: "europe-west4", Score: 90},
			{Zone: "europe-west4-b", Region: "europe-west4", Score: 80},
		},
		byMachineType: map[string][]gcecatalog.ZoneCandidate{
			"n1-standard-16": {{Zone: "us-central1-a", Region: "us-central1", Score: 70}},
		},
	}
}

func headlessReq() device.Requirements {
	return device.Requirements{Interaction: device.InteractionHeadless}
}

// A deployment that stored no ladder resolves exactly the shape it resolved
// before the ladder existed: the catalog default, ranked once. This is the
// assertion that a feature meant to be inert until configured actually is,
// and the shape is the catalog's DEFAULT, never the first enabled row that
// happens to sort first.
func TestProvisionSpecWithNoLadderResolvesTheCatalogDefault(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		policy fakePolicy
	}{
		{"key never written", fakePolicy{}},
		{"an empty ladder", fakePolicy{models.ConfigKeyFleetPoolLadders: `{"default":[]}`}},
		{"an unreadable ladder", fakePolicy{models.ConfigKeyFleetPoolLadders: `n2-standard-8,`}},
		{"a ladder the catalog admits nothing from", fakePolicy{
			models.ConfigKeyFleetPoolLadders: `{"default":["n9-standard-8"]}`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ranker, memory := headlessRanker(), &ladderMemory{}
			r := ladderResolver(tc.policy, ranker, memory)

			res, err := r.ProvisionSpec(t.Context(), headlessReq(),
				device.GPUModeAuto, WorkloadSingle, SingleInstanceShape(), host.PoolDefault)
			require.NoError(t, err)

			assert.Equal(t, "c3-standard-8", res.Spec.MachineType, "the catalog default, unchanged")
			assert.Equal(t, "europe-west4-a", res.Spec.Zone)
			assert.Empty(t, res.Warnings, "nothing fell back, so nothing is reported")
			assert.Empty(t, res.Spec.Resolved.PreferredType,
				"a create that got its preferred shape has no second shape to name")
			assert.Equal(t, 1, ranker.calls, "one ranking, as before")
			assert.Zero(t, memory.reads,
				"with nowhere to step there is nothing to remember; the read is not made")
		})
	}
}

// Rung 0 replaces the catalog default as the fleet's preferred shape. Without
// this the ladder would only ever be a fallback list, and an operator who put
// their validated shape first would still get the catalog's.
func TestProvisionSpecPrefersRungZeroOverTheCatalogDefault(t *testing.T) {
	t.Parallel()
	ranker, memory := headlessRanker(), &ladderMemory{}
	r := ladderResolver(fakePolicy{
		models.ConfigKeyFleetPoolLadders: `{"default":["n2-standard-8","n1-standard-16"]}`,
	}, ranker, memory)

	res, err := r.ProvisionSpec(t.Context(), headlessReq(),
		device.GPUModeAuto, WorkloadSingle, SingleInstanceShape(), host.PoolDefault)
	require.NoError(t, err)

	assert.Equal(t, "n2-standard-8", res.Spec.MachineType,
		"rung 0 is the shape the fleet wants")
	assert.Empty(t, res.Warnings, "the preference held, so there is no fallback to report")
	assert.Empty(t, res.Spec.Resolved.PreferredType)
	assert.Equal(t, 1, memory.reads, "one read covers the whole ladder")
	assert.ElementsMatch(t, []string{"n2", "n1"}, memory.families,
		"the one read asks about every rung's family at once")
	assert.Equal(t, 1, ranker.calls, "a preference that held costs the one ranking it always cost")
}

// The create lands on the shape that can actually be provisioned, and says so
// in both shapes. A warning naming only what was built cannot be told from an
// operator's own choice, and the audit entry gains the shape that was wanted
// because the host row keeps only the one it got.
func TestProvisionSpecStepsPastAShapeTheMemorySaysIsStockedOut(t *testing.T) {
	t.Parallel()
	ranker := headlessRanker()
	memory := &ladderMemory{cells: []*gcecatalog.ZoneHealth{
		ladderCell("europe-west4-a", "n2", 5*time.Minute),
		ladderCell("europe-west4-b", "n2", 5*time.Minute),
	}}
	r := ladderResolver(fakePolicy{
		models.ConfigKeyFleetPoolLadders: `{"default":["n2-standard-8","n1-standard-16"]}`,
	}, ranker, memory)

	res, err := r.ProvisionSpec(t.Context(), headlessReq(),
		device.GPUModeAuto, WorkloadSingle, SingleInstanceShape(), host.PoolDefault)
	require.NoError(t, err)

	assert.Equal(t, "n1-standard-16", res.Spec.MachineType)
	assert.Equal(t, "us-central1-a", res.Spec.Zone,
		"the shape it stepped to binds its own ranked zone, not the one it stepped away from")
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "n2-standard-8", "the warning names the shape that was wanted")
	assert.Contains(t, res.Warnings[0], "n1-standard-16", "and the shape that was built")
	assert.Contains(t, res.Warnings[0], "stocked out")
	assert.Equal(t, "n2-standard-8", res.Spec.Resolved.PreferredType,
		"the audit entry's copy of the same fact")
	assert.Equal(t, 1, memory.reads, "still one read, however many rungs it stepped over")
	assert.Equal(t, 2, ranker.calls,
		"one ranking for the shape it wanted and one for the shape it took: a ranking per rung is the shape this must not have")
}

// The memory decays and the preference is re-earned by nothing but time
// passing. A stockout that outlived its own cooldown must not keep demoting
// the fleet.
func TestProvisionSpecReEarnsThePreferenceAsObservationsDecay(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		cells []*gcecatalog.ZoneHealth
	}{
		{"every observation decayed", []*gcecatalog.ZoneHealth{
			ladderCell("europe-west4-a", "n2", -time.Minute),
			ladderCell("europe-west4-b", "n2", -time.Minute),
		}},
		{"one zone still worth probing", []*gcecatalog.ZoneHealth{
			ladderCell("europe-west4-a", "n2", 5*time.Minute),
		}},
		{"the observation belongs to another shape's family", []*gcecatalog.ZoneHealth{
			ladderCell("europe-west4-a", "n1", 5*time.Minute),
			ladderCell("europe-west4-b", "n1", 5*time.Minute),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := ladderResolver(fakePolicy{
				models.ConfigKeyFleetPoolLadders: `{"default":["n2-standard-8","n1-standard-16"]}`,
			}, headlessRanker(), &ladderMemory{cells: tc.cells})

			res, err := r.ProvisionSpec(t.Context(), headlessReq(),
				device.GPUModeAuto, WorkloadSingle, SingleInstanceShape(), host.PoolDefault)
			require.NoError(t, err)

			assert.Equal(t, "n2-standard-8", res.Spec.MachineType)
			assert.Empty(t, res.Warnings)
		})
	}
}

// The host's slot ceiling is judged against the shape it LANDED on, never the
// one it preferred. The demand signal reads these stamps, so a ceiling computed
// from a shape the create did not get would misreport what the fleet holds.
func TestProvisionSpecSizesTheHostFromTheShapeItLandedOn(t *testing.T) {
	t.Parallel()
	memory := &ladderMemory{cells: []*gcecatalog.ZoneHealth{
		ladderCell("europe-west4-a", "n2", 5*time.Minute),
		ladderCell("europe-west4-b", "n2", 5*time.Minute),
	}}
	r := ladderResolver(fakePolicy{
		models.ConfigKeyFleetPoolLadders: `{"default":["n2-standard-8","n1-standard-16"]}`,
	}, headlessRanker(), memory)

	res, err := r.ProvisionSpec(t.Context(), headlessReq(),
		device.GPUModeAuto, WorkloadSingle, SingleInstanceShape(), host.PoolDefault)
	require.NoError(t, err)

	// n1-standard-16 holds seven 8 GB guests beside the host reserve; the
	// 32 GB n2-standard-8 it stepped away from holds three.
	assert.Equal(t, "n1-standard-16", res.Spec.MachineType)
	assert.Equal(t, int64(7), res.Spec.MaxDevices,
		"the ceiling follows the machine the guests will actually be mapped into")
	assert.Equal(t, int64(3), res.Spec.Resolved.Density,
		"the stamp still describes what a STANDBY host would have offered, which is this pool's rung 0 rather than the rung the walk stepped to")
}

// A GPU resolution keeps its own shape. The ladder is the CPU path's, and
// stepping a GPU create onto a CPU rung would answer a stockout by silently
// removing the capability the create asked for.
func TestProvisionSpecLeavesAGPUResolutionAlone(t *testing.T) {
	t.Parallel()
	catalog := machinetypetest.NewFakeRepo(
		&machinetype.MachineType{
			ID: "c3-standard-8", Family: "c3", Architecture: machinetype.ArchX86,
			VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true, IsDefault: true,
		},
		&machinetype.MachineType{
			ID: "n2-standard-8", Family: "n2", Architecture: machinetype.ArchX86,
			VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true,
		},
		&machinetype.MachineType{
			ID: "g2-standard-8", Family: "g2", Architecture: machinetype.ArchX86,
			VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true,
			GPUSupport: true, GPUMode: machinetype.GPUModeIntegrated,
			IntegratedGPUType: "nvidia-l4", IntegratedGPUCount: 1,
		},
	)
	memory := &ladderMemory{}
	r := NewResolver(ResolverDeps{
		Policy:       fakePolicy{models.ConfigKeyFleetPoolLadders: `{"default":["n2-standard-8"]}`},
		MachineTypes: catalog,
		Ranker: &ladderRanker{fallback: []gcecatalog.ZoneCandidate{
			{Zone: "europe-west4-a", Region: "europe-west4", Score: 90},
		}},
		ZoneHealth: memory,
		Logger:     slog.New(slog.DiscardHandler),
	})

	res, err := r.ProvisionSpec(t.Context(),
		device.Requirements{Interaction: device.InteractionHeadless, GPURequired: true},
		device.GPUModeAuto, WorkloadSingle, SingleInstanceShape(), host.PoolDefault)
	require.NoError(t, err)

	assert.Equal(t, "g2-standard-8", res.Spec.MachineType,
		"the GPU arm resolves its own shape and the ladder never touches it")
	assert.Zero(t, memory.reads, "the CPU path's memory is not consulted for a GPU create")
}

// gpuLadderResolver resolves against a catalog that has both an accelerated
// shape and a CPU one, so a pool taking the wrong ladder is visible rather than
// merely possible.
func gpuLadderResolver(policy fakePolicy, memory *ladderMemory) *Resolver {
	return NewResolver(ResolverDeps{
		Policy: policy,
		MachineTypes: machinetypetest.NewFakeRepo(
			&machinetype.MachineType{
				ID: "c3-standard-8", Family: "c3", Architecture: machinetype.ArchX86,
				VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true, IsDefault: true,
			},
			&machinetype.MachineType{
				ID: "n2-standard-8", Family: "n2", Architecture: machinetype.ArchX86,
				VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true,
			},
			&machinetype.MachineType{
				ID: "g2-standard-8", Family: "g2", Architecture: machinetype.ArchX86,
				VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true, GPUSupport: true,
				GPUMode: machinetype.GPUModeIntegrated, IntegratedGPUType: "nvidia-l4", IntegratedGPUCount: 1,
			},
			&machinetype.MachineType{
				ID: "g2-standard-16", Family: "g2", Architecture: machinetype.ArchX86,
				VCPUs: 16, MemoryGB: 64, Fit: 7, IsEnabled: true, GPUSupport: true,
				GPUMode: machinetype.GPUModeIntegrated, IntegratedGPUType: "nvidia-l4", IntegratedGPUCount: 1,
			},
		),
		Ranker:     headlessRanker(),
		ZoneHealth: memory,
		Logger:     slog.New(slog.DiscardHandler),
	})
}

// A pool's ladder replaces the shape that pool's OWN standing template resolves
// to, and only that one.
//
// Both arms are reachable in either pool, since a CPU pool serves interactive
// creates with an accelerator and a GPU want the catalog cannot meet lands on
// the default shape, so a ladder applied to the wrong arm would answer a
// stockout by removing the capability the create asked for. The two rows are
// the same catalog and the same ladders, read from opposite pools.
func TestProvisionSpecTakesTheLadderOfThePoolItProvisionsInto(t *testing.T) {
	t.Parallel()
	// The gpu pool prefers the SMALLER accelerated shape, which is the opposite
	// of what this catalog resolves on its own: with two integrated rows the GPU
	// arm picks g2-standard-16, so a resolution reading the wrong pool's ladder,
	// or no ladder at all, lands on a different machine and says so.
	policy := fakePolicy{
		models.ConfigKeyFleetPoolLadders: `{"default":["n2-standard-8"],"gpu":["g2-standard-8","g2-standard-16"]}`,
	}

	memory := &ladderMemory{}
	res, err := gpuLadderResolver(policy, memory).ProvisionSpec(t.Context(), headlessReq(),
		device.GPUModeAuto, WorkloadSingle, SingleInstanceShape(), host.PoolDefault)
	require.NoError(t, err)
	assert.Equal(t, "n2-standard-8", res.Spec.MachineType, "the default pool's own rung 0")
	assert.Equal(t, host.PoolDefault, res.Spec.Pool, "the resolution stamps the pool it judged")

	memory = &ladderMemory{}
	res, err = gpuLadderResolver(policy, memory).ProvisionSpec(t.Context(), headlessReq(),
		device.GPUModeAuto, WorkloadSingle, SingleInstanceShape(), host.PoolGPU)
	require.NoError(t, err)
	assert.Equal(t, "g2-standard-8", res.Spec.MachineType,
		"the gpu pool's rung 0, not the GPU shape the catalog would have picked and not the CPU pool's rung")
	assert.Equal(t, host.PoolGPU, res.Spec.Pool)
	assert.True(t, res.Spec.Resolved.GPURequired,
		"a create routed into the gpu pool is asking for an accelerator whether or not it said so")
	assert.ElementsMatch(t, []string{"g2"}, memory.families,
		"the stockout question is asked about this pool's rungs alone")
}

// A miss in a pool that stands up a larger machine is judged against THAT
// machine. Judged against the fleet default, every create routed to such a pool
// reads as demand no standing capacity could have absorbed, so the pool's own
// demand could never raise its own target and it would sit at zero however much
// of it arrived.
func TestProvisionSpecJudgesAMissAgainstItsOwnPoolsStandingShape(t *testing.T) {
	t.Parallel()
	res, err := gpuLadderResolver(fakePolicy{}, &ladderMemory{}).ProvisionSpec(t.Context(), headlessReq(),
		device.GPUModeAuto, WorkloadSingle, SingleInstanceShape(), host.PoolGPU)
	require.NoError(t, err)

	// The gpu pool stands up g2-standard-16, which packs to its own fit of
	// seven; the catalog default's 32 GB holds three.
	require.Equal(t, "g2-standard-16", res.Spec.MachineType)
	facts := res.Spec.Resolved
	assert.Equal(t, int64(7), res.Spec.MaxDevices)
	assert.Equal(t, int64(7), facts.Density,
		"a standby host in the gpu pool is a g2, and that is what the miss is compared against")

	miss := hostconfig.Miss{
		Pool:            host.PoolGPU,
		GPURequired:     &facts.GPURequired,
		FitsDefaultHost: &facts.FitsDefaultHost,
		MaxDevices:      &res.Spec.MaxDevices,
		Density:         &facts.Density,
	}
	assert.True(t, miss.Servable(),
		"an ordinary create in the gpu pool is demand that pool can buy for: %v", miss.Reasons())
}

// An interactive create in the DEFAULT pool keeps its GPU shape: the pool's
// ladder is the CPU path's, and stepping the create onto a rung would answer a
// stockout by taking away the accelerator it was placed for.
func TestProvisionSpecLeavesADefaultPoolGPUShapeAlone(t *testing.T) {
	t.Parallel()
	memory := &ladderMemory{}
	res, err := gpuLadderResolver(fakePolicy{
		models.ConfigKeyFleetPoolLadders: `{"default":["n2-standard-8"]}`,
	}, memory).ProvisionSpec(t.Context(),
		device.Requirements{Interaction: device.InteractionInteractive},
		device.GPUModeAuto, WorkloadSingle, SingleInstanceShape(), host.PoolDefault)
	require.NoError(t, err)

	assert.Equal(t, "g2-standard-16", res.Spec.MachineType,
		"the GPU arm resolves its own shape and the CPU pool's ladder never touches it")
	assert.Zero(t, memory.reads, "a ladder that cannot apply is not read")
}

// The pool is derived once, from one policy read, and every step downstream
// takes that answer. A read per pool would put the number of configured pools
// inside a path every declarative create runs.
func TestResolvePoolIsOneReadAndRoutesOnTheEnabledSet(t *testing.T) {
	t.Parallel()
	gpuReq := device.Requirements{Interaction: device.InteractionHeadless, GPURequired: true}

	policy := &countingPolicy{fakePolicy: fakePolicy{}}
	pool, err := ladderResolver(fakePolicy{}, headlessRanker(), &ladderMemory{}).ResolvePool(
		t.Context(), gpuReq, device.GPUModeAuto)
	require.NoError(t, err)
	assert.Equal(t, host.PoolDefault, pool,
		"with no pool switched on, a GPU requirement is still the default pool's demand")

	r := NewResolver(ResolverDeps{
		Policy:       policy,
		MachineTypes: ladderCatalog(),
		Ranker:       headlessRanker(),
		ZoneHealth:   &ladderMemory{},
		Logger:       slog.New(slog.DiscardHandler),
	})
	policy.fakePolicy[models.ConfigKeyFleetPoolBounds] = `{"default":{"standby":0,"floor":0},"gpu":{"standby":1,"floor":0}}`
	pool, err = r.ResolvePool(t.Context(), gpuReq, device.GPUModeAuto)
	require.NoError(t, err)
	assert.Equal(t, host.PoolGPU, pool, "the pool an operator switched on takes the demand it exists for")
	assert.Equal(t, 1, policy.reads, "one policy read decides the pool, whatever the fleet is partitioned into")

	_, err = r.ResolvePool(t.Context(), device.Requirements{
		Interaction: device.InteractionHeadless, Pool: host.PoolARM,
	}, device.GPUModeAuto)
	require.Error(t, err, "a pool nobody switched on is refused rather than fallen back from")
}

// countingPolicy counts the reads a resolution makes of the config store.
type countingPolicy struct {
	fakePolicy
	reads int
}

func (p *countingPolicy) Get(ctx context.Context, key string) (*models.SystemConfig, error) {
	p.reads++
	return p.fakePolicy.Get(ctx, key)
}
