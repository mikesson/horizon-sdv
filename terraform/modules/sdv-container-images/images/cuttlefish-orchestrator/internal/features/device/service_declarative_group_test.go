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

package devicefeature

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// sdvPairReq is a declarative create for an SDV Core and IVI pair whose
// instances name their own builds: the Core by registered id, the IVI by URL.
func sdvPairReq(coreBuild string) device.CreateRequest {
	req := declarativeReq(device.InteractionHeadless)
	req.Mode = modeAdvanced
	req.EnvConfig = map[string]any{"instances": []any{
		map[string]any{"name": "core", "@build": coreBuild},
		map[string]any{"name": "ivi", "disk": map[string]any{"default_build": "gs://b/sdv_ivi_cf-img-user.zip"}},
	}}
	return req
}

func (f *declarativeFixture) addURLBuild(t *testing.T, id string) {
	t.Helper()
	require.NoError(t, f.buildRepo.Create(t.Context(), &build.Build{ID: id, Name: id, Source: build.SourceURL}))
}

// A group owns its host. The host here has room for both instances and is
// the only candidate, so a pick that admitted it would place the pair beside
// another device's radio medium and bridges instead of provisioning.
func TestDeclarativeGroupProvisionsInsteadOfJoiningAHostWithRoom(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.addHost("h-shared", host.StatusRunning, 1, 8, 0)

	d, res, err := f.svc.CreateDeclarative(t.Context(), groupDeclarativeReq(2), "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, device.HostActionProvision, res.HostAction)
	require.Len(t, f.provisioner.specs, 1)
	assert.True(t, f.provisioner.specs[0].Exclusive)
	assert.NotEqual(t, "h-shared", d.HostID)
	assert.Equal(t, int64(1), f.hostRepo.Hosts["h-shared"].CurrentDeviceCount, "the shared host gained nothing")
}

// The plan tells the same story as the create.
func TestPlanDeclarativeGroupProvisionsInsteadOfJoiningAHostWithRoom(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.addHost("h-shared", host.StatusRunning, 1, 8, 0)

	d, res, err := f.svc.PlanCreate(t.Context(), groupDeclarativeReq(2))
	require.NoError(t, err)
	assert.Equal(t, device.HostActionProvision, res.HostAction)
	assert.Empty(t, res.HostID)
	assert.Empty(t, d.HostID)
	assert.Empty(t, f.provisioner.specs, "a plan provisions nothing")
}

// The control for the two above: a single-instance create in the same fleet
// still packs onto the host with room.
func TestDeclarativeSingleInstanceStillJoinsAHostWithRoom(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.addHost("h-shared", host.StatusRunning, 1, 8, 0)

	d, res, err := f.svc.CreateDeclarative(t.Context(), groupDeclarativeReq(1), "u@example.com")
	require.NoError(t, err)
	assert.Equal(t, device.HostActionPlace, res.HostAction)
	assert.Equal(t, "h-shared", d.HostID)
	assert.Empty(t, f.provisioner.specs)
}

// A pair that names a build for every instance carries no device-level build,
// so the registry default cannot merge its host package and disk fields in.
func TestDeclarativePairThatNamesItsBuildsGetsNoDefaultBuild(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addBuild(t, true)
	f.addURLBuild(t, "b-core")

	d, res, err := f.svc.CreateDeclarative(t.Context(), sdvPairReq("b-core"), "u@example.com")
	require.NoError(t, err)
	assert.Empty(t, d.BuildID)
	assert.Empty(t, res.BuildID)
	assert.Equal(t, device.BuildActionUse, res.BuildAction)
	assert.Equal(t, device.HostActionProvision, res.HostAction)
}

// An @build naming no registered build is the caller's mistake, refused before
// anything resolves or commits: no tracking build registered, no host
// provisioned, no device row.
func TestInstanceBuildMustBeRegistered(t *testing.T) {
	t.Parallel()
	const wantMsg = `env_config.instances[0].@build: build "nope" is not registered`

	t.Run("declarative create", func(t *testing.T) {
		t.Parallel()
		f := newDeclarativeFixture(t)
		_, _, err := f.svc.CreateDeclarative(t.Context(), sdvPairReq("nope"), "u@example.com")
		var verr *device.ValidationError
		require.ErrorAs(t, err, &verr)
		assert.Equal(t, wantMsg, verr.Message)
		assert.Empty(t, f.buildRepo.builds, "no tracking build registered for a refused create")
		assert.Empty(t, f.provisioner.specs)
		assert.Empty(t, f.deviceRepo.Devices)
	})

	t.Run("declarative plan", func(t *testing.T) {
		t.Parallel()
		f := newDeclarativeFixture(t)
		_, _, err := f.svc.PlanCreate(t.Context(), sdvPairReq("nope"))
		var verr *device.ValidationError
		require.ErrorAs(t, err, &verr)
		assert.Equal(t, wantMsg, verr.Message)
	})

	t.Run("imperative create", func(t *testing.T) {
		t.Parallel()
		f := newDeclarativeFixture(t)
		f.addHost("h1", host.StatusRunning, 0, 4, 0)
		req := sdvPairReq("nope")
		req.Requirements, req.HostID, req.BuildID = nil, "h1", "b-device"
		_, err := f.svc.Create(t.Context(), req, "u@example.com")
		var verr *device.ValidationError
		require.ErrorAs(t, err, &verr)
		assert.Equal(t, wantMsg, verr.Message)
		assert.Empty(t, f.deviceRepo.Devices)
	})

	t.Run("imperative plan", func(t *testing.T) {
		t.Parallel()
		f := newDeclarativeFixture(t)
		f.addHost("h1", host.StatusRunning, 0, 4, 0)
		req := sdvPairReq("nope")
		req.Requirements, req.HostID = nil, "h1"
		_, _, err := f.svc.PlanCreate(t.Context(), req)
		var verr *device.ValidationError
		require.ErrorAs(t, err, &verr)
		assert.Equal(t, wantMsg, verr.Message)
	})

	t.Run("a registered build passes", func(t *testing.T) {
		t.Parallel()
		f := newDeclarativeFixture(t)
		f.addURLBuild(t, "b-core")
		f.addHost("h1", host.StatusRunning, 0, 4, 0)
		req := sdvPairReq("b-core")
		req.Requirements, req.HostID = nil, "h1"
		_, err := f.svc.Create(t.Context(), req, "u@example.com")
		require.NoError(t, err)
	})
}

// A registry that cannot answer is an outage, never a 400: the caller's
// request may be fine.
func TestInstanceBuildLookupFailureIsNotAValidationError(t *testing.T) {
	t.Parallel()
	f := newDeclarativeFixture(t)
	f.addHost("h1", host.StatusRunning, 0, 4, 0)
	outage := errors.New("spanner unavailable")
	f.buildRepo.getErr = outage
	req := sdvPairReq("b-core")
	req.Requirements, req.HostID = nil, "h1"

	_, err := f.svc.Create(t.Context(), req, "u@example.com")
	require.ErrorIs(t, err, outage)
	var verr *device.ValidationError
	assert.NotErrorAs(t, err, &verr)
	assert.Empty(t, f.deviceRepo.Devices)
}
