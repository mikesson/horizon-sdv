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
	"errors"
	"log/slog"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype/machinetypetest"
)

// The catalog these tests judge against: one integrated shape whose GPU is part
// of the machine, and one attachable shape whose GPU is a request.
func acceleratorCatalog() *machinetypetest.FakeRepo {
	return machinetypetest.NewFakeRepo(
		&machinetype.MachineType{
			ID: "g2-standard-8", Family: "g2", VCPUs: 8, MemoryGB: 32, Fit: 3,
			GPUSupport: true, GPUMode: machinetype.GPUModeIntegrated,
			IntegratedGPUType: "nvidia-l4", IntegratedGPUCount: 1,
			IsEnabled: true,
		},
		&machinetype.MachineType{
			ID: "n1-standard-8", Family: "n1", VCPUs: 8, MemoryGB: 30, Fit: 3,
			GPUSupport: true, GPUMode: machinetype.GPUModeAttachable,
			DefaultGPUType: "nvidia-tesla-t4", MaxGPUs: 4,
			IsDefault: true, IsEnabled: true,
		},
	)
}

func acceleratorService(catalog machinetype.Catalog) *HostService {
	return NewHostService(HostServiceConfig{}, nil, nil, nil, slog.New(slog.DiscardHandler),
		WithMachineTypeCatalog(catalog))
}

// The keystone. An integrated shape carries its GPU in the machine type, so the
// resolver never fills gpu_type/gpu_count for one and a user naming a g2 by hand
// fills nothing either, leaving a row that answers "no GPU" about a machine
// whose GPU is welded on. Once placement asks nothing but gpu_count > 0, such a
// host is invisible to every GPU create for the rest of its life.
func TestBuildHostStampsTheIntegratedAccelerator(t *testing.T) {
	t.Parallel()
	svc := acceleratorService(acceleratorCatalog())

	h, err := svc.buildHost(t.Context(), host.CreateRequest{
		Name: "gpu", Zone: "us-central1-a", MachineType: "g2-standard-8", MaxDevices: 1,
	}, "someone@example.com", host.OriginUser)
	if err != nil {
		t.Fatalf("buildHost: %v", err)
	}
	// Verbatim from the catalog, and that is not cosmetic: the failover and
	// alternative-zone paths re-validate a host from its own row, and an
	// integrated shape accepts a GPU request only when the type AND the count
	// both equal the catalog's. A normalized name or a count of 1 where the
	// catalog says 2 invalidates the host on every later ranking.
	if h.GPUType != "nvidia-l4" || h.GPUCount != 1 {
		t.Errorf("stamped (%q, %d), want the catalog's own (nvidia-l4, 1)", h.GPUType, h.GPUCount)
	}
}

// The stamp answers for integrated shapes only. An attachable shape's GPU is a
// request (zero is a CPU-only N1, which is most of the fleet), so writing the
// catalog's recommendation onto it would hand a GPU to every host that did not
// ask for one and make gpu_count a claim about the catalog rather than the VM.
func TestBuildHostLeavesAttachableShapesToTheRequest(t *testing.T) {
	t.Parallel()
	svc := acceleratorService(acceleratorCatalog())
	ctx := t.Context()

	cpu, err := svc.buildHost(ctx, host.CreateRequest{
		Name: "cpu", Zone: "us-central1-a", MachineType: "n1-standard-8", MaxDevices: 4,
	}, "someone@example.com", host.OriginAuto)
	if err != nil {
		t.Fatalf("buildHost: %v", err)
	}
	if cpu.GPUType != "" || cpu.GPUCount != 0 {
		t.Errorf("a CPU-only n1 was stamped (%q, %d); the catalog's recommendation is not a request",
			cpu.GPUType, cpu.GPUCount)
	}

	attached, err := svc.buildHost(ctx, host.CreateRequest{
		Name: "gpu", Zone: "us-central1-a", MachineType: "n1-standard-8", MaxDevices: 4,
		GPUType: "nvidia-tesla-t4", GPUCount: 2,
	}, "someone@example.com", host.OriginUser)
	if err != nil {
		t.Fatalf("buildHost: %v", err)
	}
	if attached.GPUType != "nvidia-tesla-t4" || attached.GPUCount != 2 {
		t.Errorf("an attachable request came through as (%q, %d), want it verbatim",
			attached.GPUType, attached.GPUCount)
	}
}

// A catalog read that fails must fail the create. After the placement predicate
// reduced to gpu_count > 0 there is no later pass that repairs an unstamped
// integrated host, which is simply never offered to a GPU create again, so
// creating one anyway trades a visible failure now for an invisible one forever.
func TestBuildHostFailsWhenTheCatalogCannotBeRead(t *testing.T) {
	t.Parallel()
	outage := errors.New("catalog unreachable")
	catalog := acceleratorCatalog()
	catalog.GetErr = outage
	svc := acceleratorService(catalog)

	_, err := svc.buildHost(t.Context(), host.CreateRequest{
		Name: "gpu", Zone: "us-central1-a", MachineType: "g2-standard-8", MaxDevices: 1,
	}, "someone@example.com", host.OriginUser)
	if !errors.Is(err, outage) {
		t.Errorf("buildHost error = %v, want the catalog outage wrapped", err)
	}
}

// A create naming no machine type is left alone rather than resolved here. The
// row records no shape, so there is nothing to judge, and a second resolution
// of the catalog default at create time could disagree with the one provisioning
// makes, leaving the row claiming a GPU the VM does not have.
func TestBuildHostSkipsTheStampWithoutAMachineType(t *testing.T) {
	t.Parallel()
	svc := acceleratorService(acceleratorCatalog())

	h, err := svc.buildHost(t.Context(), host.CreateRequest{
		Name: "shapeless", Zone: "us-central1-a", MaxDevices: 1,
	}, "someone@example.com", host.OriginUser)
	if err != nil {
		t.Fatalf("buildHost: %v", err)
	}
	if h.GPUCount != 0 {
		t.Errorf("gpu_count = %d on a row naming no machine type", h.GPUCount)
	}
}

// The property that makes the stamp safe at all, exercised through the chain
// provisioning actually runs: the row's GPU fields go to resolveMachineType,
// which re-derives attachment from the CATALOG's GPU mode, and buildInstance
// takes its accelerators from that rather than from the row.
//
// GCE rejects guestAccelerators on an integrated machine outright, so a stamp
// that reached the instance would fail every g2 provision rather than degrade.
// This goes red the day someone "simplifies" buildInstance to read h.GPUType.
func TestStampedIntegratedHostAttachesNoGuestAccelerators(t *testing.T) {
	t.Parallel()
	svc := acceleratorService(acceleratorCatalog())

	h, err := svc.buildHost(t.Context(), host.CreateRequest{
		Name: "gpu", Zone: "us-central1-a", MachineType: "g2-standard-8", MaxDevices: 1,
	}, "someone@example.com", host.OriginUser)
	if err != nil {
		t.Fatalf("buildHost: %v", err)
	}
	if h.GPUCount == 0 {
		t.Fatal("the host was not stamped, so this proves nothing about a stamped one")
	}

	shape, err := svc.resolveMachineType(t.Context(), h.MachineType, h.GPUType, h.GPUCount)
	if err != nil {
		t.Fatalf("resolveMachineType: %v", err)
	}
	inst := buildInstance(HostServiceConfig{}, h, shape, "img")
	if got := len(inst.GetGuestAccelerators()); got != 0 {
		t.Errorf("guestAccelerators = %d on an integrated shape; GCE rejects the field there", got)
	}
}
