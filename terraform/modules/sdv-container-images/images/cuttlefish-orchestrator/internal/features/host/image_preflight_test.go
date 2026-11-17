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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	compute "cloud.google.com/go/compute/apiv1"
	"github.com/danielgtaylor/huma/v2"
	"google.golang.org/api/option"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype/machinetypetest"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// staticImages resolves every machine family to one x86 image, for the tests
// that exercise provisioning rather than image resolution.
type staticImages struct{}

func (staticImages) ResolveImage(_ context.Context, machineFamily string, _ machinetype.Architecture) (*hostconfig.Image, error) {
	return &hostconfig.Image{
		SelfLink:     "https://www.googleapis.com/compute/v1/projects/test-images/global/images/" + machineFamily + "-1",
		Name:         machineFamily + "-1",
		Family:       "cco-host",
		Project:      "test-images",
		Architecture: machinetype.ArchX86,
	}, nil
}

// fakeImageSource serves image families by their GCE path; anything else is
// the 404 GCE answers.
type fakeImageSource struct {
	mu     sync.Mutex
	images map[string]*hostconfig.Image
}

func familyPath(project, family string) string {
	return "projects/" + project + "/global/images/family/" + family
}

func (f *fakeImageSource) ResolveFamily(_ context.Context, project, family string) (*hostconfig.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if img, ok := f.images[familyPath(project, family)]; ok {
		return img, nil
	}
	return nil, &hostconfig.ImageUnavailableError{ImageFamily: family, Project: project, Reason: hostconfig.ImageNotFound}
}

func (f *fakeImageSource) publish(project, family string, arch machinetype.Architecture) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.images == nil {
		f.images = map[string]*hostconfig.Image{}
	}
	name := family + "-20261018"
	f.images[familyPath(project, family)] = &hostconfig.Image{
		SelfLink:     "https://www.googleapis.com/compute/v1/projects/" + project + "/global/images/" + name,
		Name:         name,
		Family:       family,
		Project:      project,
		Architecture: arch,
	}
}

// rowImages runs the production resolution rule over in-memory rows, so these
// tests judge the real rule without importing the hostconfig slice.
type rowImages struct {
	rows    map[string]*hostconfig.HostImageConfig
	source  hostconfig.ImageSource
	project string
}

func (r rowImages) ResolveImage(ctx context.Context, machineFamily string, arch machinetype.Architecture) (*hostconfig.Image, error) {
	return hostconfig.ResolveImage(ctx, r.source, r.rows[machineFamily], machineFamily, arch, r.project)
}

func imageCatalog() *machinetypetest.FakeRepo {
	return machinetypetest.NewFakeRepo(
		&machinetype.MachineType{
			ID: "n1-standard-8", Family: "n1", VCPUs: 8, MemoryGB: 30, Fit: 3,
			Architecture: machinetype.ArchX86, GPUMode: machinetype.GPUModeAttachable,
			IsDefault: true, IsEnabled: true,
		},
		&machinetype.MachineType{
			ID: "c4a-highcpu-72-metal", Family: "c4a", VCPUs: 72, MemoryGB: 144, Fit: 8,
			Architecture: machinetype.ArchArm, GPUMode: machinetype.GPUModeNone,
			IsEnabled: true,
		},
	)
}

func seededRows() map[string]*hostconfig.HostImageConfig {
	return map[string]*hostconfig.HostImageConfig{
		"n1":  {MachineFamily: "n1", ImageFamily: "cco-host", Enabled: true},
		"c4a": {MachineFamily: "c4a", ImageFamily: "cco-host", Enabled: true},
	}
}

// A fresh install whose image project serves no cco-host family. Before the
// precheck the create succeeded, enqueued a provision task, and failed ten
// retries later with a message naming neither the family nor the project.
func TestCreateRefusesAFamilyWithNoImage(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo()
	images := rowImages{rows: seededRows(), source: &fakeImageSource{}, project: "P"}
	svc, enq := newTestService(t, repo, nil,
		WithMachineTypeCatalog(imageCatalog()), WithImageResolver(images))

	_, err := svc.Create(t.Context(), host.CreateRequest{
		Name: "img", Zone: "europe-west4-a", ZonePinned: true, MachineType: "n1-standard-8", MaxDevices: 1,
	}, "u@example.com", host.OriginUser)

	unavailable, ok := errors.AsType[*hostconfig.ImageUnavailableError](err)
	if !ok {
		t.Fatalf("Create = %v, want an ImageUnavailableError", err)
	}
	if unavailable.Reason != hostconfig.ImageNotFound || unavailable.ImageFamily != "cco-host" || unavailable.Project != "P" {
		t.Errorf("refusal = %+v, want not_found for cco-host in P", unavailable)
	}
	for _, want := range []string{"cco-host", `"P"`, "n1", "CCO_HOST_IMAGE_PROJECT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not name %s", err.Error(), want)
		}
	}
	var status huma.StatusError
	if !errors.As(web.MapError(err), &status) || status.GetStatus() != http.StatusUnprocessableEntity {
		t.Errorf("MapError status = %v, want 422", web.MapError(err))
	}
	if len(repo.Hosts) != 0 {
		t.Errorf("refused create wrote %d host rows", len(repo.Hosts))
	}
	if len(enq.enqueued) != 0 {
		t.Errorf("refused create enqueued %d tasks", len(enq.enqueued))
	}
}

// The same create goes through once the image exists, which is what makes the
// refusal above about the image and nothing else.
func TestCreatePassesOnceTheImageIsPublished(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo()
	source := &fakeImageSource{}
	source.publish("P", "cco-host", machinetype.ArchX86)
	svc, enq := newTestService(t, repo, nil, WithMachineTypeCatalog(imageCatalog()),
		WithImageResolver(rowImages{rows: seededRows(), source: source, project: "P"}))

	if _, err := svc.Create(t.Context(), host.CreateRequest{
		Name: "img", Zone: "europe-west4-a", ZonePinned: true, MachineType: "n1-standard-8", MaxDevices: 1,
	}, "u@example.com", host.OriginUser); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(repo.Hosts) != 1 || len(enq.enqueued) != 1 {
		t.Errorf("hosts=%d tasks=%d, want 1 and 1", len(repo.Hosts), len(enq.enqueued))
	}
}

// The seed used to map c4a to the x86 image family, and arm readiness read
// ready whenever a family was set. An x86 image boots nothing on Axion metal.
func TestArmCreateAgainstAnX86ImageIsArchMismatch(t *testing.T) {
	t.Parallel()
	source := &fakeImageSource{}
	source.publish("P", "cco-host", machinetype.ArchX86)
	images := rowImages{rows: seededRows(), source: source, project: "P"}
	catalog := imageCatalog()
	repo := newAtomicHostRepo()
	svc, _ := newTestService(t, repo, nil, WithMachineTypeCatalog(catalog), WithImageResolver(images))

	_, err := svc.Create(t.Context(), host.CreateRequest{
		Name: "arm", Zone: "europe-west4-a", ZonePinned: true, MachineType: "c4a-highcpu-72-metal", MaxDevices: 1,
	}, "u@example.com", host.OriginUser)
	unavailable, ok := errors.AsType[*hostconfig.ImageUnavailableError](err)
	if !ok || unavailable.Reason != hostconfig.ImageArchMismatch {
		t.Fatalf("Create = %v, want arch_mismatch", err)
	}
	if len(repo.Hosts) != 0 {
		t.Errorf("refused create wrote %d host rows", len(repo.Hosts))
	}

	arm := NewARMCapability(images, catalog, slog.New(slog.DiscardHandler))
	if arm.Ready(t.Context()) {
		t.Error("arm readiness is true with only an x86 image behind c4a")
	}

	// Publishing an arm64 image and pointing c4a at it is seen on the next
	// ask, because a no is never cached.
	source.publish("P", "cco-host-arm64", machinetype.ArchArm)
	images.rows["c4a"] = &hostconfig.HostImageConfig{MachineFamily: "c4a", ImageFamily: "cco-host-arm64", Enabled: true}
	if !arm.Ready(t.Context()) {
		t.Error("arm readiness stayed false after an arm64 image was published")
	}
}

// provisionGCE serves the insert path: it records the instance body, answers
// the operation wait DONE, and reads the instance back RUNNING.
func provisionGCE(t *testing.T) (*compute.InstancesClient, *compute.ZoneOperationsClient, func() map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var inserted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/instances"):
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			_ = json.Unmarshal(body, &inserted)
			mu.Unlock()
			fmt.Fprint(w, `{"name":"op-1","status":"RUNNING"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/wait"):
			fmt.Fprint(w, `{"name":"op-1","status":"DONE"}`)
		case r.Method == http.MethodGet:
			fmt.Fprint(w, adoptInstanceJSON("RUNNING"))
		default:
			http.Error(w, `{"error":{"code":400,"message":"unexpected"}}`, http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	instances, err := compute.NewInstancesRESTClient(t.Context(), option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("instances client: %v", err)
	}
	t.Cleanup(func() { _ = instances.Close() })
	operations, err := compute.NewZoneOperationsRESTClient(t.Context(), option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("operations client: %v", err)
	}
	t.Cleanup(func() { _ = operations.Close() })
	return instances, operations, func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return inserted
	}
}

// The insert names the concrete image, never the family URL, and the host
// records which image it booted.
func TestProvisionInsertsTheConcreteImageAndRecordsIt(t *testing.T) {
	t.Parallel()
	source := &fakeImageSource{}
	source.publish("P", "cco-host", machinetype.ArchX86)
	instances, operations, inserted := provisionGCE(t)
	repo := newAtomicHostRepo(provisioningHost())
	svc := NewHostService(HostServiceConfig{ProjectID: "test-project", HostOrchestratorPort: 2080},
		instances, operations, repo, slog.New(slog.DiscardHandler),
		WithTaskEnqueuer(&fakeEnqueuer{}),
		WithImageResolver(rowImages{rows: seededRows(), source: source, project: "P"}))

	if err := svc.RunProvision(t.Context(), testHostID, ""); err != nil {
		t.Fatalf("RunProvision: %v", err)
	}

	want := "https://www.googleapis.com/compute/v1/projects/P/global/images/cco-host-20261018"
	disks, _ := inserted()["disks"].([]any)
	if len(disks) == 0 {
		t.Fatalf("insert carried no disks: %v", inserted())
	}
	params, _ := disks[0].(map[string]any)["initializeParams"].(map[string]any)
	if got := params["sourceImage"]; got != want {
		t.Errorf("insert sourceImage = %v, want the concrete image %s", got, want)
	}
	if got := repo.Hosts[testHostID].BootImage; got != want {
		t.Errorf("host boot_image = %q, want %q", got, want)
	}
}

// A provision that finds no image fails the host on its first delivery, where
// it used to burn the whole retry ladder first.
func TestProvisionWithNoImageFailsTheHostAtOnce(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo(provisioningHost())
	svc, _ := newTestService(t, repo, nil,
		WithImageResolver(rowImages{rows: seededRows(), source: &fakeImageSource{}, project: "P"}))
	tk := task.New(TaskTypeProvisionHost, testHostID)
	tk.Attempts = 1

	if err := NewProvisionHostHandler(svc).Handle(t.Context(), tk); err != nil {
		t.Fatalf("Handle = %v, want nil (the task is done, the host failed)", err)
	}
	got := repo.Hosts[testHostID]
	if got.Status != host.StatusError {
		t.Errorf("host status = %q, want error", got.Status)
	}
	if got.ErrorCode != string(hostconfig.ImageNotFound) || !strings.Contains(got.ErrorMessage, "cco-host") {
		t.Errorf("error = (%q, %q), want not_found naming cco-host", got.ErrorCode, got.ErrorMessage)
	}
}
