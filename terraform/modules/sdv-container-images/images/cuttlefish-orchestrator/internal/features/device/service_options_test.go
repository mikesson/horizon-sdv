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
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// Wire applies WithArtifacts and WithDeclarativeCreate unconditionally, so
// the zero Deps state must make them no-ops: the disable gates then answer
// with a clean 400-class ValidationError instead of a nil-interface panic.
func TestZeroOptionalDepsDisableCapabilities(t *testing.T) {
	t.Parallel()
	svc, err := NewDeviceService(newDeviceRepoFake(), hosttest.NewFakeRepo(), newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithArtifacts(nil, nil, ""),
		WithDeclarativeCreate(DeclarativeDeps{}),
	)
	require.NoError(t, err, "zero optional deps disable capabilities, never fail construction")

	_, err = svc.Bugreport(t.Context(), "d1", false)
	var verr *device.ValidationError
	require.ErrorAs(t, err, &verr, "bugreport on a deployment without artifact capture must 400")

	req := device.CreateRequest{Name: "d1", Requirements: &device.Requirements{Interaction: device.InteractionHeadless}}
	_, _, err = svc.CreateDeclarative(t.Context(), req, "u@example.com")
	require.ErrorAs(t, err, &verr, "a declarative create on a deployment without the capability must 400")
	assert.Contains(t, err.Error(), "not enabled on this deployment")
}

// A partial optional dependency set is a wiring bug. Construction must fail
// loudly instead of deferring a nil dereference to the first request.
func TestPartialOptionalDepsFailConstruction(t *testing.T) {
	t.Parallel()
	newSvc := func(opt DeviceServiceOption) error {
		_, err := NewDeviceService(newDeviceRepoFake(), hosttest.NewFakeRepo(), newMockBuildRepo(),
			hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(), opt)
		return err
	}

	err := newSvc(WithArtifacts(&fakeUploader{}, nil, "bucket"))
	require.ErrorContains(t, err, "artifact capture", "an uploader without a policy reader is a wiring bug")

	err = newSvc(WithArtifacts(nil, &fakePolicy{values: map[string]string{}}, ""))
	require.ErrorContains(t, err, "artifact capture", "a policy reader without an uploader is a wiring bug")

	err = newSvc(WithDeclarativeCreate(DeclarativeDeps{Provisioner: &fakeProvisioner{}}))
	require.ErrorContains(t, err, "DeclarativeDeps", "a lone provisioner is a wiring bug")
}
