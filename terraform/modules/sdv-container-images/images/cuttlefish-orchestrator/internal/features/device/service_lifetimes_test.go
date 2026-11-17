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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// Every value stored below differs from the compiled default on purpose. A
// test where policy and default agree passes whether or not the create path
// reads policy at all.

func newLifetimeService(t *testing.T, deviceRepo *atomicDeviceRepo, hostRepo *hosttest.FakeRepo, values map[string]string) *DeviceService {
	t.Helper()
	svc, err := NewDeviceService(deviceRepo, hostRepo, newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithTaskEnqueuer(&fakeEnqueuer{}),
		WithLifetimePolicy(&fakePolicy{values: values}))
	require.NoError(t, err)
	return svc
}

func TestCreateTakesPolicyDeviceTTL(t *testing.T) {
	t.Parallel()
	hostRepo := runningHostRepo()
	svc := newLifetimeService(t, newDeviceRepoFakeWithHosts(hostRepo), hostRepo,
		map[string]string{models.ConfigKeyDeviceTTLHours: "6"})

	d, err := svc.Create(t.Context(), device.CreateRequest{HostID: "test-host", Name: "dev-1"}, "u")
	require.NoError(t, err)
	assert.Equal(t, int64(6*3600), d.Lifecycle.TTLSeconds, "a create naming no TTL takes the stored policy")
	assert.WithinDuration(t, time.Now().Add(6*time.Hour), *d.Lifecycle.ExpiresAt, time.Minute)

	// An explicit request always wins over policy.
	d, err = svc.Create(t.Context(), device.CreateRequest{HostID: "test-host", Name: "dev-2", TTLHours: 2}, "u")
	require.NoError(t, err)
	assert.Equal(t, int64(2*3600), d.Lifecycle.TTLSeconds)
}

func TestCreatePolicyTTLOutOfRangeReadsCompiledDefault(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"12", "0", "garbage"} {
		hostRepo := runningHostRepo()
		svc := newLifetimeService(t, newDeviceRepoFakeWithHosts(hostRepo), hostRepo,
			map[string]string{models.ConfigKeyDeviceTTLHours: raw})
		d, err := svc.Create(t.Context(), device.CreateRequest{HostID: "test-host", Name: "dev-1"}, "u")
		require.NoError(t, err)
		assert.Equal(t, device.DefaultDeviceTTLSeconds, d.Lifecycle.TTLSeconds,
			"stored %q must clamp to the compiled default, never grant past the cap", raw)
	}
}

func TestExtendTakesPolicyGrantAndAllowance(t *testing.T) {
	t.Parallel()
	hostRepo := runningHostRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	svc := newLifetimeService(t, deviceRepo, hostRepo, map[string]string{
		models.ConfigKeyExtendHours: "3",
		models.ConfigKeyExtendCount: "1",
	})

	seeded := device.NewEphemeralLifecycle(3600)
	deviceRepo.Devices["d1"] = &device.Device{
		ID: "d1", HostID: "test-host", Status: device.StatusRunning, Lifecycle: seeded,
	}

	d, err := svc.Extend(t.Context(), "d1", device.ExtendRequest{})
	require.NoError(t, err)
	assert.WithinDuration(t, seeded.ExpiresAt.Add(3*time.Hour), *d.Lifecycle.ExpiresAt, time.Minute,
		"an extend naming no amount takes the stored grant")
	assert.Equal(t, 1, d.Lifecycle.ExtendCount)

	_, err = svc.Extend(t.Context(), "d1", device.ExtendRequest{})
	require.Error(t, err, "the stored allowance of one refuses the second extension")
}

func TestExtendAllowanceZeroRefusesFirstExtension(t *testing.T) {
	t.Parallel()
	hostRepo := runningHostRepo()
	deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
	svc := newLifetimeService(t, deviceRepo, hostRepo,
		map[string]string{models.ConfigKeyExtendCount: "0"})

	deviceRepo.Devices["d1"] = &device.Device{
		ID: "d1", HostID: "test-host", Status: device.StatusRunning,
		Lifecycle: device.NewEphemeralLifecycle(3600),
	}
	_, err := svc.Extend(t.Context(), "d1", device.ExtendRequest{})
	require.Error(t, err, "allowance 0 means no extensions; pin it or lose it")
}
