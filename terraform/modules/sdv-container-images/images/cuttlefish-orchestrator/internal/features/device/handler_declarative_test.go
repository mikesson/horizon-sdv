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

	"cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// declarativeHandlerAPI registers a device handler over the given service.
func declarativeHandlerAPI(t *testing.T, svc *DeviceService) *testAPI {
	t.Helper()
	api := newTestAPI(t)
	NewDeviceHandler(svc, nil, nil, nil).Register(api.API)
	return api
}

// TestCreateDeviceDeclarativeAPI drives the requirements/validate_only
// surface through the real Huma pipeline: schema validation, the input's
// Resolve rules, routing to the right service method, and error mapping.
func TestCreateDeviceDeclarativeAPI(t *testing.T) {
	t.Parallel()

	t.Run("requirements and host_id are rejected at the transport", func(t *testing.T) {
		t.Parallel()
		f := newDeclarativeFixture(t)
		api := declarativeHandlerAPI(t, f.svc)
		resp := api.Post("/v2/devices", map[string]any{
			"host_id":      "123e4567-e89b-12d3-a456-426614174000",
			"name":         "dev-1",
			"requirements": map[string]any{"interaction": "headless"},
		})
		assert.Equal(t, 422, resp.Code)
		assert.Contains(t, resp.Body.String(), "mutually exclusive")
		assert.Empty(t, f.deviceRepo.Devices, "the rejection must precede any commit")
	})

	t.Run("registered and track are rejected at the transport", func(t *testing.T) {
		t.Parallel()
		f := newDeclarativeFixture(t)
		api := declarativeHandlerAPI(t, f.svc)
		resp := api.Post("/v2/devices", map[string]any{
			"name": "dev-1",
			"requirements": map[string]any{
				"interaction": "headless",
				"build": map[string]any{
					"registered": "some-build",
					"track":      map[string]any{"branch": "b", "target": "t"},
				},
			},
		})
		assert.Equal(t, 422, resp.Code)
		assert.Contains(t, resp.Body.String(), "at most one of registered and track")
		assert.Empty(t, f.deviceRepo.Devices)
	})

	t.Run("advanced mode with requirements needs no explicit build", func(t *testing.T) {
		t.Parallel()
		f := newDeclarativeFixture(t)
		f.addHost("h1", host.StatusRunning, 0, 4, 0)
		api := declarativeHandlerAPI(t, f.svc)

		resp := api.Post("/v2/devices", map[string]any{
			"name": "dev-1",
			"mode": modeAdvanced,
			"advanced_config": map[string]any{
				"env_config": map[string]any{
					"instances": []any{map[string]any{"name": "dev-1"}},
				},
			},
			"requirements": map[string]any{"interaction": "headless"},
		})
		assert.Equal(t, 200, resp.Code)
		assert.Contains(t, resp.Body.String(), `"resolution"`)

		require.Len(t, f.deviceRepo.Devices, 1)
		for _, d := range f.deviceRepo.Devices {
			assert.Equal(t, modeAdvanced, d.Mode)
			assert.NotNil(t, d.EnvConfig, "the env config must survive the handler untouched")
			assert.NotEmpty(t, d.BuildID, "the declarative service resolves the build")
		}
	})

	t.Run("declarative create returns the resolution", func(t *testing.T) {
		t.Parallel()
		f := newDeclarativeFixture(t)
		seeded := f.addBuild(t, true)
		f.addHost("h1", host.StatusRunning, 0, 4, 0)
		api := declarativeHandlerAPI(t, f.svc)

		resp := api.Post("/v2/devices", map[string]any{
			"name":         "dev-1",
			"requirements": map[string]any{"interaction": "headless"},
		})
		assert.Equal(t, 200, resp.Code)
		body := resp.Body.String()
		assert.Contains(t, body, `"resolution"`)
		assert.Contains(t, body, `"host_action":"place"`)

		require.Len(t, f.deviceRepo.Devices, 1)
		for _, d := range f.deviceRepo.Devices {
			assert.Equal(t, "h1", d.HostID)
			assert.Equal(t, seeded.ID, d.BuildID)
		}
	})

	t.Run("validate_only routes to the plan and commits nothing", func(t *testing.T) {
		t.Parallel()
		f := newDeclarativeFixture(t)
		api := declarativeHandlerAPI(t, f.svc)

		resp := api.Post("/v2/devices", map[string]any{
			"name":          "dev-1",
			"validate_only": true,
			"requirements":  map[string]any{"interaction": "interactive", "gpu_required": true},
		})
		assert.Equal(t, 200, resp.Code)
		assert.Contains(t, resp.Body.String(), `"host_action":"provision"`)
		assert.Empty(t, f.deviceRepo.Devices, "a plan commits no device")
		assert.Empty(t, f.provisioner.specs, "a plan provisions no host")
	})

	t.Run("a pool this deployment does not offer is refused by name", func(t *testing.T) {
		t.Parallel()
		f := newDeclarativeFixture(t)
		f.addHost("h1", host.StatusRunning, 0, 4, 0)
		api := declarativeHandlerAPI(t, f.svc)

		// The field has to reach the derivation. A body key Huma drops, or a
		// handler that does not copy it, leaves the create routed to the
		// default pool and placed on h1 with a 200.
		resp := api.Post("/v2/devices", map[string]any{
			"name":         "dev-1",
			"requirements": map[string]any{"interaction": "headless", "pool": host.PoolGPU},
		})
		assert.Equal(t, 400, resp.Code)
		assert.Contains(t, resp.Body.String(), host.PoolGPU, "the refusal names the pool that failed")
		assert.Empty(t, f.deviceRepo.Devices, "the refusal precedes any commit")
	})

	t.Run("the guest architecture reaches the pool derivation", func(t *testing.T) {
		t.Parallel()
		f := newDeclarativeFixture(t)
		f.addHost("h1", host.StatusRunning, 0, 4, 0)
		api := declarativeHandlerAPI(t, f.svc)

		// Huma rejects a body key no input struct declares, so an input that
		// dropped the field answers 422 for an unexpected property instead of
		// the refusal below, and cfctl already sends it. This fixture enables
		// no arm pool, and h1 is an x86 host an arm64 guest cannot run on.
		resp := api.Post("/v2/devices", map[string]any{
			"name":         "dev-1",
			"requirements": map[string]any{"interaction": "headless", "architecture": "arm"},
		})
		assert.Equal(t, 400, resp.Code)
		assert.Contains(t, resp.Body.String(), "requirements.architecture")
		assert.Empty(t, f.deviceRepo.Devices, "the refusal precedes any commit")
	})

	t.Run("service validation error maps to 400", func(t *testing.T) {
		t.Parallel()
		// Catalog with NO GPU-capable machine type, so gpu=required fails in
		// the service (the transport schema accepts the request).
		hostRepo := hosttest.NewFakeRepo()
		deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
		buildRepo := newMockBuildRepo()
		machineTypes := &fakeMachineTypes{
			defaultID: "e2-standard-8",
			types: map[string]*machinetype.MachineType{
				"e2-standard-8": {ID: "e2-standard-8", IsEnabled: true, IsDefault: true, GPUMode: machinetype.GPUModeNone},
			},
		}
		svc, err := NewDeviceService(deviceRepo, hostRepo, buildRepo,
			hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
			WithTaskEnqueuer(&fakeEnqueuer{}),
			WithDeclarativeCreate(DeclarativeDeps{
				Provisioner: &fakeProvisioner{hostRepo: hostRepo}, Policy: &fakePolicy{values: map[string]string{}},
				MachineTypes: machineTypes, Ranker: &fakeRanker{candidates: []gcecatalog.ZoneCandidate{{Zone: "z-a"}}},
				Quota: &fakeQuota{maxHosts: 100}, Registrar: &fakeRegistrar{repo: buildRepo}, Capacity: deviceRepo,
				ZoneHealth: fakeStockoutMemory{},
			}))
		require.NoError(t, err)
		b := &build.Build{
			ID: "b1", Name: "b", Source: build.SourceAB,
			Branch: "br", Target: "tg", IsDefault: true,
		}
		require.NoError(t, buildRepo.Create(t.Context(), b))
		api := declarativeHandlerAPI(t, svc)

		resp := api.Post("/v2/devices", map[string]any{
			"name":         "dev-1",
			"requirements": map[string]any{"interaction": "interactive", "gpu_required": true},
		})
		assert.Equal(t, 400, resp.Code)
	})

	t.Run("quota exhaustion on a plan maps to 429", func(t *testing.T) {
		t.Parallel()
		hostRepo := hosttest.NewFakeRepo()
		deviceRepo := newDeviceRepoFakeWithHosts(hostRepo)
		buildRepo := newMockBuildRepo()
		machineTypes := &fakeMachineTypes{
			defaultID: "n1-standard-8",
			types: map[string]*machinetype.MachineType{
				"n1-standard-8": {ID: "n1-standard-8", IsEnabled: true, IsDefault: true, GPUMode: machinetype.GPUModeNone},
			},
		}
		svc, err := NewDeviceService(deviceRepo, hostRepo, buildRepo,
			hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
			WithTaskEnqueuer(&fakeEnqueuer{}),
			WithDeclarativeCreate(DeclarativeDeps{
				Provisioner: &fakeProvisioner{hostRepo: hostRepo}, Policy: &fakePolicy{values: map[string]string{}},
				MachineTypes: machineTypes, Ranker: &fakeRanker{candidates: []gcecatalog.ZoneCandidate{{Zone: "z-a"}}},
				Quota: &fakeQuota{maxHosts: 5, usedHosts: 5}, Registrar: &fakeRegistrar{repo: buildRepo}, Capacity: deviceRepo,
				ZoneHealth: fakeStockoutMemory{},
			}))
		require.NoError(t, err)
		api := declarativeHandlerAPI(t, svc)

		resp := api.Post("/v2/devices", map[string]any{
			"name":          "dev-1",
			"validate_only": true,
			"requirements":  map[string]any{"interaction": "headless"},
		})
		assert.Equal(t, 429, resp.Code)
	})
}
