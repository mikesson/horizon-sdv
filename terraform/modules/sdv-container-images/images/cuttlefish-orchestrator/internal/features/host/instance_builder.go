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
	"cmp"
	"fmt"
	"maps"
	"strconv"
	"strings"

	"cloud.google.com/go/compute/apiv1/computepb"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// crosvmSandboxOffScript marks /var/empty non-empty on every boot.
//
// Upstream assemble_cvd computes the enable_sandbox default from the host
// environment (x86_64, /var/empty present AND EMPTY, not in a container), and
// enable_virtiofs takes that same default (v1.55.0 flags.cc:1398-1417;
// kCrosvmVarEmptyDir is "/var/empty", and IsDirectoryEmpty counts dirents, so
// one file flips it). crosvm cannot suspend virtio-fs, so a launch that took
// the default is unsnapshottable. CCO's create pins --enable_sandbox=false for
// its own launch only: any later `cvd start` re-assembles from defaults, which
// this marker keeps suspend-safe. A launch asking for --enable_sandbox=true
// fails on a marked host. That is accepted, since the snapshot gate refuses
// those devices anyway.
const crosvmSandboxOffScript = `#!/bin/sh
mkdir -p /var/empty && touch /var/empty/.cco-crosvm-sandbox-off
`

// buildInstance assembles the GCE instance resource for a host. It is a pure
// function of its inputs (no I/O, no service state), so the full shape of the
// instance (machine type URL, disks, networking, scheduling, GPU, service
// account, labels) is unit-testable without a GCE client.
func buildInstance(cfg HostServiceConfig, h *host.Host, shape machineTypeConfig, sourceImage string) *computepb.Instance {
	diskSizeGB := cmp.Or(h.BootDiskSizeGB, host.DefaultBootDiskSizeGB)
	labels := instanceLabels(cfg, h)

	instance := &computepb.Instance{
		Name:        &h.GCEInstanceName,
		Description: new(instanceDescription(h)),
		MachineType: new(fmt.Sprintf("zones/%s/machineTypes/%s", h.Zone, shape.machineType)),
		Disks: []*computepb.AttachedDisk{
			{
				AutoDelete: new(true),
				Boot:       new(true),
				InitializeParams: &computepb.AttachedDiskInitializeParams{
					DiskSizeGb:  new(diskSizeGB),
					SourceImage: new(sourceImage),
					// Empty leaves the zonal default; the catalog names a type
					// only for shapes that reject it (C4 and N4 boot from
					// hyperdisk-balanced alone, and say so at create time).
					DiskType: diskTypeURL(h.Zone, shape.bootDiskType),
					// The disk bills on its own line, so it carries the
					// instance's labels for cost to be attributed per install.
					Labels: maps.Clone(labels),
				},
			},
		},
		NetworkInterfaces: []*computepb.NetworkInterface{
			{
				Network:    new(networkURL(cfg)),
				Subnetwork: new(subnetworkURL(cfg, h.Zone)),
			},
		},
		Labels: labels,
		Tags: &computepb.Tags{
			Items: cfg.NetworkTags,
		},
		Metadata: &computepb.Metadata{
			Items: []*computepb.Items{
				{
					Key:   new("startup-script"),
					Value: new(crosvmSandboxOffScript),
				},
			},
		},
	}

	// Cuttlefish needs /dev/kvm, which means the host VM has to nest. This
	// flag is what grants it and it is what Google documents as the way to
	// ask; the enable-vmx license the host image carries is the older
	// mechanism, kept because it costs nothing, but it is not what a
	// verified-working G2 relies on. Bare metal is excluded because there is
	// nothing to nest. The hardware is the host.
	if !machinetype.IsBareMetalID(shape.machineType) {
		instance.AdvancedMachineFeatures = &computepb.AdvancedMachineFeatures{
			EnableNestedVirtualization: new(true),
		}
	}

	// GCE rejects a platform the zone/family cannot honor; that failure rides
	// the classified-error path like any other zone-dependent constraint.
	if h.MinCPUPlatform != "" {
		instance.MinCpuPlatform = new(h.MinCPUPlatform)
	}

	if cfg.UseExternalIP {
		instance.NetworkInterfaces[0].AccessConfigs = []*computepb.AccessConfig{
			{
				Name: new("External NAT"),
				Type: new("ONE_TO_ONE_NAT"),
			},
		}
	}

	// The reaper is the real TTL; this is the failsafe for when there is no
	// reaper (a hung control plane, a deleted database, an abandoned
	// project), where a host VM would otherwise bill until somebody notices.
	// The bound is the GCE maximum, 120 days of uptime, because leases have no
	// cap and GCE will not move it on a running VM. The reconciler records when
	// it fires (vm_terminates_at), and every lease raise is refused past that
	// time, so it never fires under a lease the API granted.
	scheduling := &computepb.Scheduling{
		OnHostMaintenance: new("TERMINATE"), // Required for GPU and Spot VMs
		MaxRunDuration:    &computepb.Duration{Seconds: new(hostMaxRunDurationSeconds)},
		// STOP rather than DELETE, because stopping is reversible and the host
		// reconciler already adopts TERMINATED into host 'stopped', which
		// settles the devices with it. The failsafe then surfaces as state an
		// operator can see and undo. A device pin has no required expiry, so a
		// host CAN legitimately outlive the bound; losing its compute beats
		// losing it.
		InstanceTerminationAction: new("STOP"),
	}
	if h.ProvisioningModel == host.ProvisioningModelSpot {
		scheduling.ProvisioningModel = new("SPOT")
		// Spot shares this one field between preemption and the duration
		// bound, and a preempted Spot VM is gone either way: DELETE keeps the
		// preemption contract the recovery path is written against.
		scheduling.InstanceTerminationAction = new("DELETE")
	}
	instance.Scheduling = scheduling

	if shape.gpuType != "" && shape.gpuCount > 0 {
		instance.GuestAccelerators = []*computepb.AcceleratorConfig{
			{
				AcceleratorType:  new(fmt.Sprintf("zones/%s/acceleratorTypes/%s", h.Zone, shape.gpuType)),
				AcceleratorCount: new(int32(shape.gpuCount)), //nolint:gosec // gpuCount validated to be 0-16 in API layer
			},
		}
	}

	if cfg.ServiceAccount != "" {
		instance.ServiceAccounts = []*computepb.ServiceAccount{
			{
				Email:  new(cfg.ServiceAccount),
				Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
			},
		}
	}

	return instance
}

// instanceLabels carries the host row's identity onto the VM. Without them a
// VM can only be traced back to CCO by parsing its name, which is why orphan
// GC reconstructs every historical instance name instead of looking one up,
// and why an operator in the Cloud Console has nothing to filter on.
//
// Label values are restricted to lowercase letters, numbers, hyphens and
// underscores, so the creator's email cannot ride here; it goes in the
// description.
func instanceLabels(cfg HostServiceConfig, h *host.Host) map[string]string {
	labels := maps.Clone(cfg.Labels)
	if labels == nil {
		labels = make(map[string]string, 3)
	}
	labels["cco-host-id"] = h.ID
	labels["cco-incarnation"] = strconv.FormatInt(h.Incarnation, 10)
	labels["cco-provisioning"] = strings.ToLower(string(h.ProvisioningModel))
	return labels
}

// instanceDescription is the human half of the same join, rendered in the
// Cloud Console's instance detail pane.
//
// It carries no expiry on purpose. The reaper extends a host's lease as its
// devices are used, and an instance description cannot be kept in step, so a
// stale timestamp here would read as authoritative.
func instanceDescription(h *host.Host) string {
	desc := fmt.Sprintf("CCO host %q · host_id %s", h.Name, h.ID)
	if h.CreatedBy != nil && *h.CreatedBy != "" {
		desc += " · owner " + *h.CreatedBy
	}
	return desc
}

// networkURL returns the full network URL for GCE API.
// If the config already contains a full path (projects/...), use it as-is.
// Otherwise, construct the full path from the project ID and network name.
func networkURL(cfg HostServiceConfig) string {
	if strings.HasPrefix(cfg.Network, "projects/") {
		return cfg.Network
	}
	return fmt.Sprintf("projects/%s/global/networks/%s", cfg.ProjectID, cfg.Network)
}

// subnetworkURL returns the full subnetwork URL for GCE API.
// If the config already contains a full path (projects/...), use it as-is.
// Otherwise, construct the full path from the project ID, region, and subnetwork name.
func subnetworkURL(cfg HostServiceConfig, zone string) string {
	if strings.HasPrefix(cfg.Subnetwork, "projects/") {
		return cfg.Subnetwork
	}
	return fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", cfg.ProjectID, gcecatalog.RegionFromZone(zone), cfg.Subnetwork)
}

// diskTypeURL renders a zonal disk-type URL, or nil to leave GCE its default.
func diskTypeURL(zone, diskType string) *string {
	if diskType == "" {
		return nil
	}
	return new(fmt.Sprintf("zones/%s/diskTypes/%s", zone, diskType))
}
