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

package hostfeature

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype/machinetypetest"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// The ladder's catalog: two enabled x86 CPU shapes in different families, so a
// stockout observation recorded against one says nothing about the other.
func ladderCatalog() *machinetypetest.FakeRepo {
	return machinetypetest.NewFakeRepo(
		&machinetype.MachineType{
			ID: "n2-standard-8", Family: "n2", Architecture: machinetype.ArchX86,
			VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true, IsDefault: true,
		},
		&machinetype.MachineType{
			ID: "n1-standard-8", Family: "n1", Architecture: machinetype.ArchX86,
			VCPUs: 8, MemoryGB: 30, Fit: 3, IsEnabled: true,
		},
	)
}

// ladderPolicy stores the default pool's preference order the walk reads back.
func ladderPolicy(rungs string) fakeLifetimePolicy {
	return fakeLifetimePolicy{models.ConfigKeyFleetPoolLadders: `{"default":` + rungs + `}`}
}

// stockedOutCell is a capacity observation still in force.
func stockedOutCell(zone, family string) *gcecatalog.ZoneHealth {
	until := time.Now().Add(5 * time.Minute)
	return &gcecatalog.ZoneHealth{
		Zone: zone, MachineFamily: family,
		BindingMetric: gcecatalog.ZoneMetricCapacity, CooldownUntil: &until,
	}
}

// decayedCell is the same observation after its cooldown lapsed: remembered,
// and no longer repelling anything.
func decayedCell(zone, family string) *gcecatalog.ZoneHealth {
	until := time.Now().Add(-time.Minute)
	return &gcecatalog.ZoneHealth{
		Zone: zone, MachineFamily: family,
		BindingMetric: gcecatalog.ZoneMetricCapacity, CooldownUntil: &until,
	}
}

// ladderHost is a mid-provision host on the fleet's preferred shape.
func ladderHost(origin host.Origin) *host.Host {
	return &host.Host{
		ID: testHostID, Name: "h", Zone: "europe-west4-a", MachineType: "n2-standard-8",
		GCEInstanceName: host.InstanceName("h", testHostID, 0),
		Status:          host.StatusProvisioning, DesiredState: host.DesiredRunning,
		MaxDevices: 3, Origin: origin,
	}
}

// ladderValidator ranks both zones for the preferred shape and only the second
// for the shape below it, so a rung step is visible in the zone it lands in and
// cannot be confused with an ordinary zone move.
func ladderValidator() *fakePlacementValidator {
	return &fakePlacementValidator{
		candidates: []gcecatalog.ZoneCandidate{
			{Zone: "europe-west4-a", Region: "europe-west4", Score: 90},
			{Zone: "europe-west4-b", Region: "europe-west4", Score: 80},
		},
		byMachineType: map[string][]gcecatalog.ZoneCandidate{
			"n1-standard-8": {{Zone: "europe-west4-b", Region: "europe-west4", Score: 70}},
		},
	}
}

func ladderService(t *testing.T, seed *host.Host, validator *fakePlacementValidator,
	cells []*gcecatalog.ZoneHealth, policy fakeLifetimePolicy,
) (*HostService, *atomicHostRepo, *fakeZoneHealthStore) {
	t.Helper()
	repo := newAtomicHostRepo(seed)
	zh := &fakeZoneHealthStore{cells: cells}
	svc, _ := newTestService(t, repo, nil,
		WithCatalogValidator(validator),
		WithZoneHealth(zh),
		WithMachineTypeCatalog(ladderCatalog()),
		WithSystemConfig(policy))
	return svc, repo, zh
}

// quotaErr is a regional CPU-quota rejection: a real provisioning failure, and
// not a capacity one.
func quotaErr() error {
	return &googleapi.Error{Code: http.StatusForbidden, Errors: []googleapi.ErrorItem{{
		Reason: "quotaExceeded", Message: "Quota CPUS exceeded in region europe-west4",
	}}}
}

// The keystone: every zone ranked for the preferred shape is out of capacity,
// so the create steps down to the shape the operator listed next rather than
// failing. The host keeps its identity and its slot ceiling; only the shape,
// the zone and the incarnation move.
func TestLadderStepsToTheNextShapeWhenEveryZoneIsStockedOut(t *testing.T) {
	t.Parallel()
	validator := ladderValidator()
	svc, repo, zh := ladderService(t, ladderHost(host.OriginAuto), validator,
		[]*gcecatalog.ZoneHealth{
			stockedOutCell("europe-west4-a", "n2"),
			stockedOutCell("europe-west4-b", "n2"),
		},
		ladderPolicy(`["n2-standard-8","n1-standard-8"]`))

	svc.FailoverZone(t.Context(), testHostID, stockoutErr(), true)

	moved, err := repo.Get(t.Context(), testHostID)
	require.NoError(t, err)
	assert.Equal(t, "n1-standard-8", moved.MachineType,
		"a shape with nowhere left to go steps to the next rung the operator listed")
	assert.Equal(t, "europe-west4-b", moved.Zone, "the new shape binds its own ranked zone")
	assert.Equal(t, int64(1), moved.Incarnation)
	assert.True(t, strings.HasSuffix(moved.GCEInstanceName, "-i1"),
		"the shape is applied at incarnation birth, so it needs a fresh instance name; got %q",
		moved.GCEInstanceName)
	assert.Equal(t, int64(3), moved.MaxDevices,
		"the ceiling the host promised its devices survives the step")
	assert.Equal(t, host.StatusProvisioning, moved.Status)

	assert.Equal(t, 1, zh.capacityReads,
		"one stockout read covers the whole ladder; a read per rung is the shape this must not have")
	assert.ElementsMatch(t, []string{"n2", "n1"}, zh.families,
		"the one read asks about every rung's family at once")
}

// Only a stockout advances a rung. A quota error is a real failure the fleet
// must surface, and answering it with a different machine shape would turn a
// misconfiguration into a silent shape change that nothing reports.
func TestLadderStaysOnItsShapeForANonCapacityFailure(t *testing.T) {
	t.Parallel()
	validator := ladderValidator()
	svc, repo, _ := ladderService(t, ladderHost(host.OriginAuto), validator,
		[]*gcecatalog.ZoneHealth{
			stockedOutCell("europe-west4-a", "n2"),
			stockedOutCell("europe-west4-b", "n2"),
		},
		ladderPolicy(`["n2-standard-8","n1-standard-8"]`))

	svc.FailoverZone(t.Context(), testHostID, quotaErr(), true)

	stayed, err := repo.Get(t.Context(), testHostID)
	require.NoError(t, err)
	assert.Equal(t, "n2-standard-8", stayed.MachineType,
		"a quota failure says nothing about capacity for this shape")
}

// The memory decays, and the preference is re-earned by nothing more than time
// passing. Without this a transient stockout would demote the fleet to a lower
// shape until somebody noticed.
func TestLadderStaysOnItsShapeWhileAZoneIsStillWorthProbing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		cells []*gcecatalog.ZoneHealth
	}{
		{"nothing remembered", nil},
		{"one zone still free", []*gcecatalog.ZoneHealth{stockedOutCell("europe-west4-a", "n2")}},
		{"every observation decayed", []*gcecatalog.ZoneHealth{
			decayedCell("europe-west4-a", "n2"), decayedCell("europe-west4-b", "n2"),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, repo, _ := ladderService(t, ladderHost(host.OriginAuto), ladderValidator(), tc.cells,
				ladderPolicy(`["n2-standard-8","n1-standard-8"]`))

			svc.FailoverZone(t.Context(), testHostID, stockoutErr(), true)

			stayed, err := repo.Get(t.Context(), testHostID)
			require.NoError(t, err)
			assert.Equal(t, "n2-standard-8", stayed.MachineType,
				"the zone walk is the inner loop and it is not finished")
		})
	}
}

// Both provisioning callers ride the same policy, so both walk the same ladder:
// the keeper's top-up is standing capacity for the very creates the miss path
// serves, and a fleet whose buffer and whose misses landed on different shapes
// would answer neither contract.
func TestLadderWalksForEveryHostTheFleetProvisionedForItself(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		origin host.Origin
		want   string
	}{
		{"a declarative create that found no capacity", host.OriginAuto, "n1-standard-8"},
		{"a fleet keeper top-up", host.OriginPool, "n1-standard-8"},
		{"a host somebody asked for by shape", host.OriginUser, "n2-standard-8"},
		{"a batch's host, sized for the group it carries", host.OriginBatch, "n2-standard-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, repo, _ := ladderService(t, ladderHost(tc.origin), ladderValidator(),
				[]*gcecatalog.ZoneHealth{
					stockedOutCell("europe-west4-a", "n2"),
					stockedOutCell("europe-west4-b", "n2"),
				},
				ladderPolicy(`["n2-standard-8","n1-standard-8"]`))

			svc.FailoverZone(t.Context(), testHostID, stockoutErr(), true)

			landed, err := repo.Get(t.Context(), testHostID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, landed.MachineType)
		})
	}
}

// With no ladder stored, the failure path is what it was before the ladder
// existed: the zone walk, and nothing else. This is the assertion that a
// feature meant to be inert until configured actually is.
func TestNoLadderLeavesTheZoneWalkExactlyAsItWas(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		policy fakeLifetimePolicy
	}{
		{"key never written", fakeLifetimePolicy{}},
		{"an empty ladder", ladderPolicy(`[]`)},
		{"an unreadable ladder", fakeLifetimePolicy{models.ConfigKeyFleetPoolLadders: `n2-standard-8,n1-standard-8`}},
		{"a shape that is no longer on the ladder", ladderPolicy(`["n1-standard-8"]`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Ranking offers a better zone than the host's own, so the zone
			// walk has something to do: an assertion that only checked the
			// shape would pass on a failover path that had stopped working.
			validator := &fakePlacementValidator{candidates: []gcecatalog.ZoneCandidate{
				{Zone: "europe-west4-b", Region: "europe-west4", Score: 90},
				{Zone: "europe-west4-a", Region: "europe-west4", Score: 80},
			}}
			svc, repo, _ := ladderService(t, ladderHost(host.OriginAuto), validator,
				[]*gcecatalog.ZoneHealth{
					stockedOutCell("europe-west4-a", "n2"),
					stockedOutCell("europe-west4-b", "n2"),
				}, tc.policy)

			svc.FailoverZone(t.Context(), testHostID, stockoutErr(), true)

			landed, err := repo.Get(t.Context(), testHostID)
			require.NoError(t, err)
			assert.Equal(t, "n2-standard-8", landed.MachineType, "the shape is not the fleet's to change")
			assert.Equal(t, "europe-west4-b", landed.Zone, "the zone walk still runs")
		})
	}
}

// A rung too small for the ceiling the host already promised is stepped over,
// never landed on: its devices were admitted against that ceiling, and a host
// that silently shrank under them would overcommit guest RAM that is really
// allocated.
func TestLadderRefusesARungThatCannotHoldTheHostsCeiling(t *testing.T) {
	t.Parallel()
	seed := ladderHost(host.OriginAuto)
	seed.MaxDevices = 3
	repo := newAtomicHostRepo(seed)
	zh := &fakeZoneHealthStore{cells: []*gcecatalog.ZoneHealth{
		stockedOutCell("europe-west4-a", "n2"),
		stockedOutCell("europe-west4-b", "n2"),
	}}
	catalog := machinetypetest.NewFakeRepo(
		&machinetype.MachineType{
			ID: "n2-standard-8", Family: "n2", Architecture: machinetype.ArchX86,
			VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true, IsDefault: true,
		},
		// Half the RAM: one 8 GB guest beside the host reserve, not three.
		&machinetype.MachineType{
			ID: "n1-standard-4", Family: "n1", Architecture: machinetype.ArchX86,
			VCPUs: 4, MemoryGB: 15, Fit: 1, IsEnabled: true,
		},
	)
	svc, _ := newTestService(t, repo, nil,
		WithCatalogValidator(ladderValidator()), WithZoneHealth(zh),
		WithMachineTypeCatalog(catalog),
		WithSystemConfig(ladderPolicy(`["n2-standard-8","n1-standard-4"]`)))

	svc.FailoverZone(t.Context(), testHostID, stockoutErr(), true)

	landed, err := repo.Get(t.Context(), testHostID)
	require.NoError(t, err)
	assert.Equal(t, "n2-standard-8", landed.MachineType,
		"a rung that cannot hold the ceiling is no rung for this host")
}
