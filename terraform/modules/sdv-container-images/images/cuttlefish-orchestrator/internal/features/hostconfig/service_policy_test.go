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

package hostconfigfeature_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	hostconfigfeature "cloud-cuttlefish-orchestrator/internal/features/hostconfig"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

func newPolicyService(t *testing.T) *hostconfigfeature.Service {
	t.Helper()
	svc, _ := newPolicyServiceWithRepo(t)
	return svc
}

func newPolicyServiceWithRepo(t *testing.T) (*hostconfigfeature.Service, *cspanner.SystemConfigRepository) {
	t.Helper()
	return newPolicyServiceWithShape(t, nil)
}

// newPolicyServiceWithShape is newPolicyServiceWithRepo with the machine
// catalog the packing density is judged against and a shape ladder's rungs are
// admitted against. A nil catalog leaves the density unjudged, which is what
// every policy test but the density and ladder ones wants.
func newPolicyServiceWithShape(t *testing.T, shape hostconfigfeature.MachineCatalog) (*hostconfigfeature.Service, *cspanner.SystemConfigRepository) {
	t.Helper()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewSystemConfigRepository(db.Client)
	return hostconfigfeature.NewService(nil, repo, hostconfigfeature.Images{}, shape, slog.Default(), ""), repo
}

// fakeDefaultShape answers one catalog default and whatever rows a ladder test
// seeds. errNoDefault stands in for a catalog with nothing flagged, which is a
// real state and not a failure.
type fakeDefaultShape struct {
	mt   *machinetype.MachineType
	rows []*machinetype.MachineType
	err  error
	// lists counts the catalog listings one call made, so a validation that
	// grew a per-rung lookup fails instead of merely getting slower.
	lists int
	// gets counts the single-shape lookups, which a refusal makes and an
	// admitted ladder must not.
	gets int
	// withheld is what the catalog says about the shapes it does not offer,
	// keyed by machine type.
	withheld map[string]*machinetype.UnofferedError
}

func (f *fakeDefaultShape) GetDefault(_ context.Context) (*machinetype.MachineType, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.mt, nil
}

func (f *fakeDefaultShape) List(_ context.Context, _ machinetype.ListFilter) ([]*machinetype.MachineType, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lists++
	return f.rows, nil
}

func (f *fakeDefaultShape) Get(_ context.Context, id string) (*machinetype.MachineType, error) {
	f.gets++
	if unoffered, ok := f.withheld[id]; ok {
		return nil, unoffered
	}
	for _, row := range f.rows {
		if row.ID == id {
			return row, nil
		}
	}
	return nil, machinetype.ErrNotFound
}

// n1-standard-8 is the live default: 30 GB, which holds three 8 GB guests
// beside the 4 GB host reserve however high the operator sets the density.
func defaultShapeN1() *fakeDefaultShape {
	return &fakeDefaultShape{mt: &machinetype.MachineType{
		ID: "n1-standard-8", VCPUs: 8, MemoryGB: 30, Fit: 3, IsDefault: true, IsEnabled: true,
	}}
}

// The density read is the default shape's own composed fit (cores and
// memory, or a per-shape override) with the shape named beside it.
func TestServiceHostDensityIsTheDefaultShapesFit(t *testing.T) {
	t.Parallel()
	svc, _ := newPolicyServiceWithShape(t, defaultShapeN1())

	density, err := svc.GetHostDensity(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(3), density.Effective,
		"an n1-standard-8 holds three 8 GB guests beside the host reserve")
	assert.Equal(t, "n1-standard-8", density.MachineType)
}

// A catalog with nothing flagged default, and a deployment with no catalog
// reader wired, are the same answer: no capacity to report. A read that
// FAILED is a different thing and must surface, or the card would publish a
// capacity figure derived from an error.
func TestServiceHostDensity_UnjudgeableVersusUnreadable(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	for _, tc := range []struct {
		name  string
		shape hostconfigfeature.MachineCatalog
	}{
		{"no reader wired", nil},
		{"no default flagged", &fakeDefaultShape{err: machinetype.ErrNotFound}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, _ := newPolicyServiceWithShape(t, tc.shape)
			density, err := svc.GetHostDensity(ctx)
			require.NoError(t, err)
			assert.Zero(t, density.Effective, "no default shape means no capacity to state")
			assert.Empty(t, density.MachineType)
		})
	}

	t.Run("a failed catalog read is an error", func(t *testing.T) {
		t.Parallel()
		svc, _ := newPolicyServiceWithShape(t, &fakeDefaultShape{err: errBoom})
		_, err := svc.GetHostDensity(ctx)
		require.Error(t, err, "a card number derived from a failed read is worse than no card")
	})
}

// The console cannot otherwise tell a quota margin of 10 an operator chose
// from the compiled 10 it would get anyway, which is what makes a
// "modified from default" marker and a reset possible at all.
func TestServicePolicyDefaults_ReportWhetherAValueWasStored(t *testing.T) {
	t.Parallel()
	svc := newPolicyService(t)
	ctx := t.Context()

	_, stored, err := svc.GetQuotaMargin(ctx)
	require.NoError(t, err)
	assert.False(t, stored, "an unseeded key is defaulted, not stored")

	require.NoError(t, svc.UpdateQuotaMargin(ctx, 10))
	margin, stored, err := svc.GetQuotaMargin(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(10), margin)
	assert.True(t, stored, "written explicitly, even though it equals the default")

	_, hintStored, err := svc.GetV1ZoneHint(ctx)
	require.NoError(t, err)
	assert.False(t, hintStored)
	require.NoError(t, svc.UpdateV1ZoneHint(ctx, true))
	_, hintStored, err = svc.GetV1ZoneHint(ctx)
	require.NoError(t, err)
	assert.True(t, hintStored)

	// The paired keys answer as one: half-written is not a state the console
	// should offer to reset.
	tiers, err := svc.GetSpotMinUptimes(ctx)
	require.NoError(t, err)
	assert.False(t, tiers.IsSet)
	require.NoError(t, svc.UpdateSpotMinUptimes(ctx, 600, 3600))
	tiers, err = svc.GetSpotMinUptimes(ctx)
	require.NoError(t, err)
	assert.True(t, tiers.IsSet)
}

// The written values all differ from the compiled defaults (4/2/3/10), so
// this fails if the group endpoint writes or reads the wrong keys.
func TestServiceLifetimes_RoundTrip(t *testing.T) {
	t.Parallel()
	svc, repo := newPolicyServiceWithRepo(t)
	ctx := t.Context()

	policy, err := svc.GetLifetimes(ctx)
	require.NoError(t, err)
	assert.False(t, policy.IsSet, "unseeded keys are defaulted, not stored")
	assert.Equal(t, int64(4), policy.DeviceTTLHours)
	assert.Equal(t, int64(2), policy.ExtendHours)
	assert.Equal(t, int64(3), policy.ExtendCount)
	assert.Equal(t, int64(10), policy.HostTTLHours)

	require.NoError(t, svc.UpdateLifetimes(ctx, hostconfig.Lifetimes{
		DeviceTTLHours: 6, ExtendHours: 3, ExtendCount: 1, HostTTLHours: 24,
	}))
	policy, err = svc.GetLifetimes(ctx)
	require.NoError(t, err)
	assert.True(t, policy.IsSet)
	assert.Equal(t, int64(6), policy.DeviceTTLHours)
	assert.Equal(t, int64(3), policy.ExtendHours)
	assert.Equal(t, int64(1), policy.ExtendCount)
	assert.Equal(t, int64(24), policy.HostTTLHours)

	// A row past the compiled cap, however it got stored, reads as the
	// default and as "not a policy an operator chose".
	require.NoError(t, repo.Set(ctx, "lifetimes.device_ttl_hours", "99", ""))
	policy, err = svc.GetLifetimes(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(4), policy.DeviceTTLHours, "out-of-range clamps to compiled default")
	assert.False(t, policy.IsSet)
	assert.Equal(t, int64(24), policy.HostTTLHours, "sibling keys keep their stored values")
}

// The written values differ from the compiled defaults (n1-standard-8/12h),
// so this fails if the group endpoint writes or reads the wrong keys.
func TestServiceDockerHost_RoundTrip(t *testing.T) {
	t.Parallel()
	svc, repo := newPolicyServiceWithRepo(t)
	ctx := t.Context()

	policy, err := svc.GetDockerHost(ctx)
	require.NoError(t, err)
	assert.False(t, policy.IsSet, "unseeded keys are defaulted, not stored")
	assert.Equal(t, "n1-standard-8", policy.MachineType)
	assert.Equal(t, int64(12), policy.TTLHours)

	require.NoError(t, svc.UpdateDockerHost(ctx, "n2-standard-16", 48))
	policy, err = svc.GetDockerHost(ctx)
	require.NoError(t, err)
	assert.True(t, policy.IsSet)
	assert.Equal(t, "n2-standard-16", policy.MachineType)
	assert.Equal(t, int64(48), policy.TTLHours)

	// A TTL past the compiled cap, however it got stored, reads as the
	// default and as "not a policy an operator chose".
	require.NoError(t, repo.Set(ctx, "legacy.docker_ttl_hours", "9000", ""))
	policy, err = svc.GetDockerHost(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(12), policy.TTLHours, "out-of-range clamps to compiled default")
	assert.False(t, policy.IsSet)
	assert.Equal(t, "n2-standard-16", policy.MachineType, "sibling key keeps its stored value")

	// An emptied machine type reads as the compiled default: a docker create
	// must never reach catalog validation with an empty type.
	require.NoError(t, repo.Set(ctx, "legacy.docker_ttl_hours", "48", ""))
	require.NoError(t, repo.Set(ctx, "legacy.docker_machine_type", "  ", ""))
	policy, err = svc.GetDockerHost(ctx)
	require.NoError(t, err)
	assert.Equal(t, "n1-standard-8", policy.MachineType)
	assert.False(t, policy.IsSet)
}

func TestServicePolicyDefaults_UnsetReadDefaults(t *testing.T) {
	t.Parallel()
	svc := newPolicyService(t)
	ctx := t.Context()

	zone, _, err := svc.GetZonePin(ctx)
	require.NoError(t, err)
	assert.Empty(t, zone)

	margin, _, err := svc.GetQuotaMargin(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(10), margin, "unset margin reads as the compiled default")

	hint, _, err := svc.GetV1ZoneHint(ctx)
	require.NoError(t, err)
	assert.True(t, hint, "the legacy zone token defaults to hint mode")

	machineType, _, err := svc.GetDefaultGPUMachineType(ctx)
	require.NoError(t, err)
	assert.Empty(t, machineType)

	build, err := svc.GetDefaultUpstreamBuild(ctx)
	require.NoError(t, err)
	assert.Empty(t, build.Branch)
	assert.Empty(t, build.Target)

	spotDefault, _, err := svc.GetSpotAutoDefault(ctx)
	require.NoError(t, err)
	assert.False(t, spotDefault, "auto capacity defaults to standard until the operator opts in")

	tiers, err := svc.GetSpotMinUptimes(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(600), tiers.Single, "singles default to the 10-minute tier")
	assert.Equal(t, int64(3600), tiers.Batch, "batch members default to the hour tier")
}

func TestServicePolicyDefaults_RoundTripAndClear(t *testing.T) {
	t.Parallel()
	svc := newPolicyService(t)
	ctx := t.Context()

	require.NoError(t, svc.UpdateZonePin(ctx, "europe-west4-b"))
	zone, _, err := svc.GetZonePin(ctx)
	require.NoError(t, err)
	assert.Equal(t, "europe-west4-b", zone)

	require.NoError(t, svc.UpdateZonePin(ctx, ""))
	zone, _, err = svc.GetZonePin(ctx)
	require.NoError(t, err)
	assert.Empty(t, zone, "empty value clears the pin")

	require.NoError(t, svc.UpdateQuotaMargin(ctx, 25))
	margin, _, err := svc.GetQuotaMargin(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(25), margin)

	require.NoError(t, svc.UpdateV1ZoneHint(ctx, false))
	hint, _, err := svc.GetV1ZoneHint(ctx)
	require.NoError(t, err)
	assert.False(t, hint)

	require.NoError(t, svc.UpdateDefaultGPUMachineType(ctx, "g2-standard-8"))
	machineType, _, err := svc.GetDefaultGPUMachineType(ctx)
	require.NoError(t, err)
	assert.Equal(t, "g2-standard-8", machineType)

	require.NoError(t, svc.UpdateDefaultUpstreamBuild(ctx,
		"aosp-android-latest-release", "aosp_cf_x86_64_only_phone-userdebug"))
	build, err := svc.GetDefaultUpstreamBuild(ctx)
	require.NoError(t, err)
	assert.Equal(t, "aosp-android-latest-release", build.Branch)
	assert.Equal(t, "aosp_cf_x86_64_only_phone-userdebug", build.Target)

	require.NoError(t, svc.UpdateSpotAutoDefault(ctx, true))
	spotDefault, _, err := svc.GetSpotAutoDefault(ctx)
	require.NoError(t, err)
	assert.True(t, spotDefault)

	require.NoError(t, svc.UpdateSpotAutoDefault(ctx, false))
	spotDefault, _, err = svc.GetSpotAutoDefault(ctx)
	require.NoError(t, err)
	assert.False(t, spotDefault)

	require.NoError(t, svc.UpdateSpotMinUptimes(ctx, 60, 600))
	tiers, err := svc.GetSpotMinUptimes(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(60), tiers.Single)
	assert.Equal(t, int64(600), tiers.Batch)
}

func fleetWindow() hostconfig.FleetSchedule {
	return hostconfig.FleetSchedule{
		Name:        "CI morning",
		Hosts:       3,
		Start:       "07:00",
		DurationSec: 14400,
		Days:        []int64{1, 2, 3, 4, 5},
		Location:    "Europe/Stockholm",
	}
}

func TestServiceFleetSchedules_RoundTrip(t *testing.T) {
	t.Parallel()
	svc := newPolicyService(t)
	ctx := t.Context()

	policy, err := svc.GetFleetPolicy(ctx)
	require.NoError(t, err)
	assert.Empty(t, policy.Pool(hostconfig.DefaultTier).Schedules, "an unseeded key is no windows")

	require.NoError(t, svc.UpdateFleetSchedules(ctx, hostconfig.DefaultTier, []hostconfig.FleetSchedule{fleetWindow()}))
	policy, err = svc.GetFleetPolicy(ctx)
	require.NoError(t, err)
	require.Len(t, policy.Pool(hostconfig.DefaultTier).Schedules, 1)
	assert.Equal(t, fleetWindow(), policy.Pool(hostconfig.DefaultTier).Schedules[0], "every field must survive the JSON round trip")

	// An empty list is how every window is removed; it must not read back as
	// "unset" or the console cannot tell a cleared list from an unwritten one.
	require.NoError(t, svc.UpdateFleetSchedules(ctx, hostconfig.DefaultTier, nil))
	policy, err = svc.GetFleetPolicy(ctx)
	require.NoError(t, err)
	assert.Empty(t, policy.Pool(hostconfig.DefaultTier).Schedules)
}

// The per-pool window map gets the same fail-safe as the scalars beside it, and
// the direction that matters is that it cannot take the MODE down with it: an
// unreadable window map must cost the schedule signal, not the autoscaler.
func TestGetFleetPolicy_UnreadableSchedulesLeaveTheRestAlone(t *testing.T) {
	t.Parallel()
	svc, repo := newPolicyServiceWithRepo(t)
	ctx := t.Context()

	require.NoError(t, svc.UpdateFleetAutoscaler(ctx, hostconfig.ModeProvisionOnly, hostconfig.DefaultTier, 2, 0))
	require.NoError(t, svc.UpdateFleetTuning(ctx, 1800, 300))

	for _, stored := range []struct {
		name  string
		value string
	}{
		{"not JSON at all", "{not json"},
		{"JSON of the wrong shape", `[{"pool":"default","schedules":[]}]`},
		// Parseable, and every window in it is unevaluable. Salvaging the
		// readable entries out of a map like this would store a policy nobody
		// wrote, with capacity on some mornings and not others.
		{
			"a window past the compiled bounds",
			`{"default":[{"name":"x","hosts":99,"start":"07:00","duration_sec":3600,"location":"UTC"}]}`,
		},
		{
			"a window in an unknown zone",
			`{"default":[{"name":"x","hosts":1,"start":"07:00","duration_sec":3600,"location":"Mars/Olympus"}]}`,
		},
	} {
		t.Run(stored.name, func(t *testing.T) {
			require.NoError(t, repo.Set(ctx, "fleet.pool_schedules", stored.value, ""))

			policy, err := svc.GetFleetPolicy(ctx)
			require.NoError(t, err, "an unreadable list is a degraded read, never an error")
			assert.Empty(t, policy.Pool(hostconfig.DefaultTier).Schedules, "the whole map drops")
			assert.Equal(t, hostconfig.ModeProvisionOnly, policy.Mode, "the mode survives")
			assert.Equal(t, int64(2), policy.Pool(hostconfig.DefaultTier).Standby, "the standing intent survives")
			assert.Equal(t, int64(1800), policy.DemandHorizonSec, "the horizon survives")
			assert.False(t, policy.IsSet, "an unusable stored value is not a choice to offer resetting")
		})
	}
}

// A window that cannot be evaluated must be refused at the door. Stored, it
// looks configured on the card and holds nothing, and no other surface reports
// the difference.
func TestUpdateFleetSchedules_RefusesAnUnevaluableWindow(t *testing.T) {
	t.Parallel()
	svc := newPolicyService(t)
	ctx := t.Context()

	bad := fleetWindow()
	bad.Location = "Mars/Olympus"
	require.Error(t, svc.UpdateFleetSchedules(ctx, hostconfig.DefaultTier, []hostconfig.FleetSchedule{bad}))

	policy, err := svc.GetFleetPolicy(ctx)
	require.NoError(t, err)
	assert.Empty(t, policy.Pool(hostconfig.DefaultTier).Schedules, "a refused write must store nothing")
}

// The endpoints are split so this holds. A tuning write that carried the window
// list, as it would if the list rode the tuning body, clears every window
// whenever a client changes a horizon without re-sending them, which is
// exactly what cfctl's read-overlay-write does.
func TestUpdateFleetTuning_LeavesTheSchedulesAlone(t *testing.T) {
	t.Parallel()
	svc := newPolicyService(t)
	ctx := t.Context()

	require.NoError(t, svc.UpdateFleetSchedules(ctx, hostconfig.DefaultTier, []hostconfig.FleetSchedule{fleetWindow()}))
	require.NoError(t, svc.UpdateFleetTuning(ctx, 1800, 300))

	policy, err := svc.GetFleetPolicy(ctx)
	require.NoError(t, err)
	assert.Len(t, policy.Pool(hostconfig.DefaultTier).Schedules, 1, "a horizon edit must not clear the windows")
	assert.Equal(t, int64(1800), policy.DemandHorizonSec)

	// And the converse: a window write must not disturb the scalars.
	require.NoError(t, svc.UpdateFleetSchedules(ctx, hostconfig.DefaultTier, nil))
	policy, err = svc.GetFleetPolicy(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1800), policy.DemandHorizonSec)
	assert.Equal(t, int64(300), policy.DrainGraceSec)
}

// A switch flip must write one key. Writing all four would land four audited
// changes for one operator action and would silently re-assert three values
// the operator never touched, which is the defect the Artifacts page carried.
func TestUpdateSweepPolicy_WritesOnlyWhatThePatchNames(t *testing.T) {
	t.Parallel()
	svc := newPolicyService(t)
	ctx := t.Context()

	on := true
	require.NoError(t, svc.UpdateSweepPolicy(ctx, hostconfig.SweepPolicyPatch{DeleteOrphanVMs: &on}))

	policy, err := svc.GetSweepPolicy(ctx)
	require.NoError(t, err)
	assert.True(t, policy.DeleteOrphanVMs, "the named switch was not stored")
	assert.False(t, policy.OrphanCVDs, "an unnamed switch was written")
	assert.False(t, policy.ZombieRecreate, "an unnamed switch was written")
	assert.False(t, policy.ReaperDryRun, "an unnamed switch was written")
	assert.True(t, policy.IsSet, "a stored switch must report the policy as set")
}

// Absent keys are the shipped state: every loop detects and records, none
// deletes, and nothing reports itself as chosen by an operator.
func TestGetSweepPolicy_AbsentKeysReadAsDetectOnly(t *testing.T) {
	t.Parallel()
	policy, err := newPolicyService(t).GetSweepPolicy(t.Context())

	require.NoError(t, err)
	assert.Equal(t, hostconfig.SweepPolicy{}, policy, "an unwritten policy must be all-false and unset")
}

func TestUpdateSweepPolicy_RoundTripsEverySwitch(t *testing.T) {
	t.Parallel()
	svc := newPolicyService(t)
	ctx := t.Context()

	on := true
	require.NoError(t, svc.UpdateSweepPolicy(ctx, hostconfig.SweepPolicyPatch{
		DeleteOrphanVMs: &on, OrphanCVDs: &on, ZombieRecreate: &on, ReaperDryRun: &on,
	}))

	policy, err := svc.GetSweepPolicy(ctx)
	require.NoError(t, err)
	assert.Equal(t, hostconfig.SweepPolicy{
		DeleteOrphanVMs: true, OrphanCVDs: true, ZombieRecreate: true, ReaperDryRun: true, IsSet: true,
	}, policy)
}

// The two bounds are stored per pool, and the published fleet-wide pair is the
// DEFAULT pool's, one derivation, so a surface reading the flat field and the
// keeper reading the pool cannot disagree.
//
// Absent, the bounds read 0, which is both the spend-nothing direction and what
// every deployment did before the key existed. Unreadable, they read 0 too, for
// every pool at once, because a map salvaged per entry is a policy nobody wrote.
func TestServiceFleetBounds_RoundTripAndFailSafe(t *testing.T) {
	t.Parallel()
	svc, repo := newPolicyServiceWithRepo(t)
	ctx := t.Context()

	policy, err := svc.GetFleetPolicy(ctx)
	require.NoError(t, err)
	assert.Zero(t, policy.Floor, "an unseeded floor holds nothing")

	require.NoError(t, svc.UpdateFleetAutoscaler(ctx, hostconfig.ModeOn, hostconfig.DefaultTier, 1, 3))
	policy, err = svc.GetFleetPolicy(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), policy.Pool(hostconfig.DefaultTier).Floor)
	assert.Equal(t, int64(1), policy.Pool(hostconfig.DefaultTier).Standby,
		"the two bounds are stored side by side, not one over the other")
	assert.Equal(t, policy.Pool(hostconfig.DefaultTier).Floor, policy.Floor,
		"the published floor is the default pool's, not a second stored copy")
	assert.Equal(t, hostconfig.ModeOn, policy.Mode)

	for _, stored := range []string{
		"not JSON",
		`{"default":{"standby":1,"floor":999}}`,
		`{"default":{"standby":-1,"floor":0}}`,
	} {
		require.NoError(t, repo.Set(ctx, "fleet.pool_bounds", stored, ""))
		policy, err = svc.GetFleetPolicy(ctx)
		require.NoError(t, err, "unusable bounds are a degraded read, never an error")
		assert.Zero(t, policy.Floor, "stored %q must read as no floor at all", stored)
		assert.Zero(t, policy.Pool(hostconfig.DefaultTier).Standby, "nor a buffer")
		assert.Equal(t, hostconfig.ModeOn, policy.Mode, "bad bounds must not take the mode down with them")
	}
}

// A write edits ONE pool's entry and leaves every other pool's standing, in each
// of the three maps.
//
// This is the read-overlay-write the shape of the storage forces, and getting it
// wrong is silent: a write that rebuilt a map from the pool it was editing
// stores exactly what the caller asked for and clears the rest of the fleet's
// policy, which nothing reports until somebody reads it back.
func TestPoolWritesLeaveTheOtherPoolsPolicyStanding(t *testing.T) {
	t.Parallel()
	svc, _ := newPolicyServiceWithShape(t, ladderShapes())
	ctx := t.Context()

	require.NoError(t, svc.UpdateFleetAutoscaler(ctx, hostconfig.ModeOn, "gpu", 1, 1))
	require.NoError(t, svc.UpdateFleetShapeLadder(ctx, "gpu", []string{"g2-standard-8"}))
	require.NoError(t, svc.UpdateFleetSchedules(ctx, "gpu", []hostconfig.FleetSchedule{fleetWindow()}))

	require.NoError(t, svc.UpdateFleetAutoscaler(ctx, hostconfig.ModeOn, hostconfig.DefaultTier, 2, 3))
	require.NoError(t, svc.UpdateFleetShapeLadder(ctx, hostconfig.DefaultTier, []string{"n1-standard-8"}))
	require.NoError(t, svc.UpdateFleetSchedules(ctx, hostconfig.DefaultTier, nil))

	policy, err := svc.GetFleetPolicy(ctx)
	require.NoError(t, err)
	gpu := policy.Pool("gpu")
	assert.Equal(t, int64(1), gpu.Standby, "the pool this write had no opinion about keeps its buffer")
	assert.Equal(t, int64(1), gpu.Floor, "and its floor")
	assert.Equal(t, []string{"g2-standard-8"}, gpu.Ladder, "and its ladder")
	assert.Len(t, gpu.Schedules, 1, "and its windows")

	def := policy.Pool(hostconfig.DefaultTier)
	assert.Equal(t, int64(2), def.Standby)
	assert.Equal(t, int64(3), def.Floor)
	assert.Equal(t, []string{"n1-standard-8"}, def.Ladder)
	assert.Empty(t, def.Schedules)
}

// ladderShapes is the catalog a ladder test admits rungs against: one enabled
// x86 CPU shape per rung, plus one row per refusal rule.
func ladderShapes() *fakeDefaultShape {
	shape := defaultShapeN1()
	shape.rows = []*machinetype.MachineType{
		{ID: "n1-standard-8", Family: "n1", Architecture: machinetype.ArchX86, VCPUs: 8, MemoryGB: 30, Fit: 3, IsEnabled: true},
		{ID: "n2-standard-8", Family: "n2", Architecture: machinetype.ArchX86, VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true},
		{ID: "n2-standard-16", Family: "n2", Architecture: machinetype.ArchX86, VCPUs: 16, MemoryGB: 64, Fit: 7, IsEnabled: false},
		{
			ID: "g2-standard-8", Family: "g2", Architecture: machinetype.ArchX86, VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true,
			GPUMode: machinetype.GPUModeIntegrated,
		},
		{ID: "c4a-standard-8-metal", Family: "c4a", Architecture: machinetype.ArchArm, VCPUs: 8, MemoryGB: 32, Fit: 3, IsEnabled: true},
	}
	// n1-highmem-2 is a real shape of an ENABLED family that the catalog still
	// withholds: two vCPUs hold no guest, and no capacity override says
	// otherwise yet.
	shape.withheld = map[string]*machinetype.UnofferedError{
		"n1-highmem-2": {
			MachineType: "n1-highmem-2", Reason: machinetype.ReasonBelowFloor,
			Family: "n1", VCPUs: 2, MemoryGB: 13,
		},
		"n9-standard-8": {
			MachineType: "n9-standard-8", Reason: machinetype.ReasonFamilyNotCurated, Family: "n9",
		},
	}
	return shape
}

func TestServiceFleetShapeLadder_RoundTrip(t *testing.T) {
	t.Parallel()
	catalog := ladderShapes()
	svc, _ := newPolicyServiceWithShape(t, catalog)
	ctx := t.Context()

	policy, err := svc.GetFleetPolicy(ctx)
	require.NoError(t, err)
	assert.Empty(t, policy.ShapeLadder, "an unseeded key is no ladder, which is the catalog default")

	require.NoError(t, svc.UpdateFleetShapeLadder(ctx, hostconfig.DefaultTier, []string{"n2-standard-8", "n1-standard-8"}))
	policy, err = svc.GetFleetPolicy(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"n2-standard-8", "n1-standard-8"}, policy.ShapeLadder,
		"the operator's order is the feature; it must survive the round trip intact")

	// An empty list is how the ladder is removed and the catalog default
	// restored; it must not read back as "unset".
	require.NoError(t, svc.UpdateFleetShapeLadder(ctx, hostconfig.DefaultTier, nil))
	policy, err = svc.GetFleetPolicy(ctx)
	require.NoError(t, err)
	assert.Empty(t, policy.ShapeLadder)
}

// A rung the catalog cannot admit must be refused at the door, with the rule
// named. Stored, it would look like a preference on every surface and answer a
// stockout with machines that boot and cannot launch a device.
func TestUpdateFleetShapeLadder_RefusesEveryInadmissibleRung(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	for _, tc := range []struct {
		name  string
		rungs []string
		says  string
	}{
		{"a family nobody curated", []string{"n9-standard-8"}, "enable its machine family first"},
		// The live refusal that sent this fix: the n1 family is enabled and the
		// machine is simply too narrow, so the remedy is a capacity override and
		// not a family switch.
		{"below the capacity floor", []string{"n1-highmem-2"}, "2 vCPUs and 13 GB hold no Android VM at all"},
		{"and the remedy that admits it", []string{"n1-highmem-2"}, "set a per-shape capacity override"},
		{"disabled", []string{"n2-standard-16"}, "disabled"},
		{"integrated GPU", []string{"g2-standard-8"}, "integrated GPU"},
		{"arm bare metal", []string{"c4a-standard-8-metal"}, "x86"},
		{"repeated", []string{"n2-standard-8", "n2-standard-8"}, "more than once"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, _ := newPolicyServiceWithShape(t, ladderShapes())
			err := svc.UpdateFleetShapeLadder(ctx, hostconfig.DefaultTier, tc.rungs)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.says, "the refusal must name the rule it applied")

			policy, perr := svc.GetFleetPolicy(ctx)
			require.NoError(t, perr)
			assert.Empty(t, policy.ShapeLadder, "a refused write must store nothing")
		})
	}
}

// Every rung is admitted against ONE catalog listing. A validation that looked
// each rung up separately would issue a statement per rung, which is the shape
// this refuses to have.
func TestUpdateFleetShapeLadder_ReadsTheCatalogOnce(t *testing.T) {
	t.Parallel()
	catalog := ladderShapes()
	svc, _ := newPolicyServiceWithShape(t, catalog)

	require.NoError(t, svc.UpdateFleetShapeLadder(t.Context(), hostconfig.DefaultTier, []string{"n2-standard-8", "n1-standard-8"}))
	assert.Equal(t, 1, catalog.lists, "one write admits every rung from one listing")
	assert.Zero(t, catalog.gets,
		"a ladder every rule admits must cost no per-shape lookup; the catalog is asked only about a rung being refused")
}

// The ladder gets the same whole-list fail-safe the windows have, and in the
// same direction: an unreadable ladder costs the preference order, never the
// mode. Both JSON keys are read on the same path, so a fail-safe applied to one
// and not the other would be invisible until the day it mattered.
func TestGetFleetPolicy_UnreadableLadderLeavesTheRestAlone(t *testing.T) {
	t.Parallel()
	svc, repo := newPolicyServiceWithRepo(t)
	ctx := t.Context()

	require.NoError(t, svc.UpdateFleetAutoscaler(ctx, hostconfig.ModeProvisionOnly, hostconfig.DefaultTier, 2, 0))

	for _, stored := range []struct {
		name  string
		value string
	}{
		{"not JSON at all", "n2-standard-8,n1-standard-8"},
		{"JSON of the wrong shape", `["n2-standard-8"]`},
		{"a repeated rung", `{"default":["n2-standard-8","n2-standard-8"]}`},
		{"past the rung bound", `{"default":["a","b","c","d","e","f","g","h","i"]}`},
	} {
		t.Run(stored.name, func(t *testing.T) {
			require.NoError(t, repo.Set(ctx, "fleet.pool_ladders", stored.value, ""))

			policy, err := svc.GetFleetPolicy(ctx)
			require.NoError(t, err, "an unreadable ladder is a degraded read, never an error")
			assert.Empty(t, policy.ShapeLadder, "the whole ladder drops")
			assert.Equal(t, hostconfig.ModeProvisionOnly, policy.Mode, "the mode survives")
			assert.Equal(t, int64(2), policy.Pool(hostconfig.DefaultTier).Standby, "the standing intent survives")
			assert.False(t, policy.IsSet)
		})
	}
}

// The ladder has its own endpoint for exactly this reason: a client that edits
// a scalar by reading, overlaying and writing back must not be able to clear
// the ladder by forgetting to carry it.
func TestUpdateFleetTuning_LeavesTheLadderAlone(t *testing.T) {
	t.Parallel()
	svc, _ := newPolicyServiceWithShape(t, ladderShapes())
	ctx := t.Context()

	require.NoError(t, svc.UpdateFleetShapeLadder(ctx, hostconfig.DefaultTier, []string{"n2-standard-8", "n1-standard-8"}))
	require.NoError(t, svc.UpdateFleetTuning(ctx, 1800, 300))
	require.NoError(t, svc.UpdateFleetAutoscaler(ctx, hostconfig.ModeOn, hostconfig.DefaultTier, 1, 1))

	policy, err := svc.GetFleetPolicy(ctx)
	require.NoError(t, err)
	assert.Len(t, policy.ShapeLadder, 2, "a scalar edit must not clear the ladder")
}

// A write that edits one pool refuses a map it could not read, in each of the
// three.
//
// Overlaying an unreadable map is a silent deletion: the write stores the one
// pool it names, reports success, and every other pool's bounds, ladder or
// windows are gone with nothing anywhere saying so. Refusing is what keeps a
// repair, an intended replacement of the whole map, distinguishable from a
// loss nobody asked for.
func TestPoolWritesRefuseAnUnreadableMap(t *testing.T) {
	t.Parallel()
	svc, repo := newPolicyServiceWithShape(t, ladderShapes())
	ctx := t.Context()

	keys := []string{"fleet.pool_bounds", "fleet.pool_ladders", "fleet.pool_schedules"}
	for _, key := range keys {
		require.NoError(t, repo.Set(ctx, key, "{not json", ""))
	}

	require.Error(t, svc.UpdateFleetAutoscaler(ctx, hostconfig.ModeOn, hostconfig.DefaultTier, 1, 1))
	require.Error(t, svc.UpdateFleetShapeLadder(ctx, hostconfig.DefaultTier, []string{"n2-standard-8"}))
	require.Error(t, svc.UpdateFleetSchedules(ctx, hostconfig.DefaultTier,
		[]hostconfig.FleetSchedule{fleetWindow()}))

	for _, key := range keys {
		stored, err := repo.Get(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, "{not json", stored.AsString(), "%s must still hold what a repair has to start from", key)
	}
	// The mode rides the same write as the bounds, so a refusal has to take
	// both: half of an atomic pair stored is a policy nobody chose.
	policy, err := svc.GetFleetPolicy(ctx)
	require.NoError(t, err)
	assert.Equal(t, hostconfig.ModeOff, policy.Mode)
}

// A pool is switched on by being given bounds, and switched off by having them
// taken away. Its windows and its ladder go with it, because configuration for a
// pool nothing joins would be restored silently the day it came back.
//
// The rungs prove the second half of it: a shape the default pool refuses is
// exactly the shape the gpu pool exists to buy, so a validation reading one set
// of rules everywhere would refuse this write.
func TestFleetPools_EnabledByTheirBoundsAndDisabledWholesale(t *testing.T) {
	t.Parallel()
	svc, _ := newPolicyServiceWithShape(t, ladderShapes())
	ctx := t.Context()

	policy, err := svc.GetFleetPolicy(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{hostconfig.DefaultTier}, policy.EnabledPools(),
		"a fresh deployment offers the pool every machine naming none belongs to, and no other")

	// A ladder for a pool nobody switched on is refused rather than stored
	// inert: the read would carry nothing, and the write would have reported
	// success for a change with no effect anywhere.
	require.Error(t, svc.UpdateFleetShapeLadder(ctx, host.PoolGPU, []string{"g2-standard-8"}))

	require.NoError(t, svc.UpdateFleetAutoscaler(ctx, hostconfig.ModeOn, host.PoolGPU, 1, 0))
	require.NoError(t, svc.UpdateFleetShapeLadder(ctx, host.PoolGPU, []string{"g2-standard-8"}))
	require.NoError(t, svc.UpdateFleetSchedules(ctx, host.PoolGPU, []hostconfig.FleetSchedule{fleetWindow()}))

	policy, err = svc.GetFleetPolicy(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{hostconfig.DefaultTier, host.PoolGPU}, policy.EnabledPools())
	assert.Equal(t, int64(1), policy.Pool(host.PoolGPU).Standby)
	assert.Equal(t, []string{"g2-standard-8"}, policy.Pool(host.PoolGPU).Ladder,
		"the gpu pool takes the integrated shape the default pool refuses")
	assert.Len(t, policy.Pool(host.PoolGPU).Schedules, 1)
	assert.Empty(t, policy.Pool(hostconfig.DefaultTier).Ladder,
		"one pool's ladder is not another's")

	require.NoError(t, svc.DisableFleetPool(ctx, host.PoolGPU))
	policy, err = svc.GetFleetPolicy(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{hostconfig.DefaultTier}, policy.EnabledPools())
	assert.Zero(t, policy.Pool(host.PoolGPU).Standby, "a pool switched off asks for nothing")
	assert.Empty(t, policy.Pool(host.PoolGPU).Ladder)
	assert.Empty(t, policy.Pool(host.PoolGPU).Schedules)
}

// The default pool cannot be switched off: it is where every machine naming no
// pool belongs, so a fleet without it would hold machines against no bounds at
// all, and the keeper would read every one of them as surplus.
func TestDisableFleetPool_RefusesTheDefaultPool(t *testing.T) {
	t.Parallel()
	svc := newPolicyService(t)
	ctx := t.Context()

	require.Error(t, svc.DisableFleetPool(ctx, hostconfig.DefaultTier))
	require.Error(t, svc.DisableFleetPool(ctx, "experiment"),
		"a name this platform does not have is refused at the door")
}
