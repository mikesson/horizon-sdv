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
	"log/slog"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// urlBuildFixture registers a URL-sourced build in the mock repo.
func urlBuildFixture(buildRepo *mockBuildRepo) *build.Build {
	b := &build.Build{
		ID:     "url-build-id",
		Name:   "gcs-images",
		Source: build.SourceURL,
		ArtifactSpecs: []build.ArtifactSpec{
			{Role: build.ArtifactRoleDefaultBuild, Type: build.ArtifactSpecTypeGCS, URL: "gs://bucket/images.zip"},
		},
	}
	buildRepo.builds[b.ID] = b
	return b
}

// TestCreateAdmitsURLSourceBuild verifies a URL-sourced build reaches the
// create path. The host's cvd resolves gs:// and https:// build strings, so
// admission has nothing left to refuse; the registry's own spec validation is
// the only gate.
func TestCreateAdmitsURLSourceBuild(t *testing.T) {
	t.Parallel()
	deviceRepo := newDeviceRepoFake()
	hostRepo := hosttest.NewFakeRepo()
	buildRepo := newMockBuildRepo()
	b := urlBuildFixture(buildRepo)
	hostRepo.Hosts["h1"] = &host.Host{ID: "h1", Status: host.StatusRunning, MaxDevices: 4}

	svc, _ := NewDeviceService(deviceRepo, hostRepo, buildRepo,
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithTaskEnqueuer(&fakeEnqueuer{}))

	if _, err := svc.Create(t.Context(), device.CreateRequest{
		HostID:  "h1",
		Name:    "url-build",
		BuildID: b.ID,
	}, "tester"); err != nil {
		t.Fatalf("URL-sourced build rejected at admission: %v", err)
	}

	// An unregistered build ID stays legal (used as a raw @ab reference).
	if _, err := svc.Create(t.Context(), device.CreateRequest{
		HostID:  "h1",
		Name:    "raw-ab-ref",
		BuildID: "12345678",
	}, "tester"); err != nil {
		t.Fatalf("raw @ab build ID rejected: %v", err)
	}
}

// TestRunningInstanceOpsRequireRunning verifies restart/powerwash/powerbtn
// refuse non-running devices, mirroring the Start/Stop precondition contract.
func TestRunningInstanceOpsRequireRunning(t *testing.T) {
	t.Parallel()
	deviceRepo := newDeviceRepoFake()
	hostRepo := hosttest.NewFakeRepo()
	deviceRepo.Devices["d1"] = &device.Device{ID: "d1", HostID: "h1", Status: device.StatusStopped}

	svc, _ := NewDeviceService(deviceRepo, hostRepo, newMockBuildRepo(),
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithTaskEnqueuer(&fakeEnqueuer{}))

	// restart and powerwash are now async and return a handle; the guard they
	// share with the synchronous powerbtn is the same one, and it still rejects
	// before anything is admitted or actuated.
	for name, op := range map[string]func() error{
		"restart":   func() error { _, err := svc.Restart(t.Context(), "d1", "inst"); return err },
		"powerwash": func() error { _, err := svc.Powerwash(t.Context(), "d1", "inst"); return err },
		"powerbtn":  func() error { return svc.Powerbtn(t.Context(), "d1", "inst") },
	} {
		if err := op(); !errors.Is(err, device.ErrInvalidState) {
			t.Errorf("%s on a stopped device: want ErrInvalidState, got %v", name, err)
		}
	}
}

// TestPlanAdmitsURLSourceBuild verifies the dry run behaves the way the real
// create does (AIP-163).
func TestPlanAdmitsURLSourceBuild(t *testing.T) {
	t.Parallel()
	deviceRepo := newDeviceRepoFake()
	hostRepo := hosttest.NewFakeRepo()
	buildRepo := newMockBuildRepo()
	b := urlBuildFixture(buildRepo)
	hostRepo.Hosts["h1"] = &host.Host{ID: "h1", Status: host.StatusRunning, MaxDevices: 4}

	svc, _ := NewDeviceService(deviceRepo, hostRepo, buildRepo,
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithTaskEnqueuer(&fakeEnqueuer{}))

	if _, _, err := svc.PlanCreate(t.Context(), device.CreateRequest{
		HostID:  "h1",
		Name:    "url-build-plan",
		BuildID: b.ID,
	}); err != nil {
		t.Fatalf("URL-sourced build rejected by validate_only: %v", err)
	}
}
