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

package placement

import "cloud-cuttlefish-orchestrator/internal/domain/device"

// GroupShape is what one create asks a host to hold: how many CVD instances,
// and the resources those instances declared between them.
//
// The two resources are summed differently because they behave differently,
// and getting that backwards is how a sizing rule stops matching reality.
type GroupShape struct {
	// Instances is how many of the host's slots this create occupies, one per
	// CVD instance the config declares, and one for a config that declares
	// none. The host counter is denominated in instances, so this is what the
	// capacity check, the placement query and the atomic create all compare
	// against, and they must all read this one derivation.
	Instances int64

	// MemoryMB is the SUM of the instances' declared guest RAM. Guest memory is
	// really allocated, since crosvm maps it, so a host that cannot hold the
	// sum is a host the group thrashes on rather than one it is refused by.
	MemoryMB int64

	// VCPUs is the LARGEST single instance's declared vCPU count, never the sum.
	//
	// vCPUs are time-shared, and this deployment already oversubscribes them
	// heavily by policy: the operator's packing density puts host.DefaultMaxDevices
	// ordinary devices, each declaring device.DefaultInstanceVCPUs, on one
	// host, and a two-instance group was observed running on an eight-vCPU
	// machine. Summing them would hold groups to a rule four times stricter
	// than the one singles run under, and would grow hosts nobody needs. What
	// the host does owe is enough width for its widest single guest, which is
	// exactly what the density policy already guarantees a single device.
	VCPUs int64
}

// SingleInstanceShape is the footprint of an ordinary one-instance create: the
// batch members and pool hosts that have no env_config at all, and the simple
// create path that sends the same defaults to the host.
func SingleInstanceShape() GroupShape {
	return GroupShape{
		Instances: 1,
		MemoryMB:  device.DefaultInstanceMemoryMB,
		VCPUs:     device.DefaultInstanceVCPUs,
	}
}

// ShapeFromEnvConfig reads a create's footprint off the canonical config.
// A config that declares no instances is a create that still needs one slot,
// so it reads as SingleInstanceShape rather than as nothing.
//
// An instance that names no vm.cpus or vm.memory_mb takes the same defaults
// the create itself sends the host for a config-less device, so the host is
// sized against what the guest will actually be asked for.
func ShapeFromEnvConfig(envConfig map[string]any) GroupShape {
	declared, _ := envConfig["instances"].([]any)
	if len(declared) == 0 {
		return SingleInstanceShape()
	}
	shape := GroupShape{Instances: int64(len(declared))}
	for _, raw := range declared {
		inst, _ := raw.(map[string]any)
		vm, _ := inst["vm"].(map[string]any)
		shape.MemoryMB += configInt64(vm["memory_mb"], device.DefaultInstanceMemoryMB)
		shape.VCPUs = max(shape.VCPUs, configInt64(vm["cpus"], device.DefaultInstanceVCPUs))
	}
	return shape
}

// hostMemoryGB is the memory a machine type must carry to hold this group,
// rounded UP to the catalog's whole-gigabyte vocabulary. Rounding down would
// let a shape one megabyte short read as a fit.
func (s GroupShape) hostMemoryGB() int64 {
	return (s.MemoryMB + device.HostReserveMemoryMB + 1023) / 1024
}

// configInt64 reads a positive number out of the canonical config, falling back
// when the key is absent or holds something else. The config arrives as decoded
// JSON, so a number is a float64 on every production path; the integer arms are
// for configs built in Go. A zero or negative value is not a smaller instance,
// it is a caller saying nothing, so it takes the fallback too.
func configInt64(value any, fallback int64) int64 {
	var n int64
	switch v := value.(type) {
	case float64:
		n = int64(v)
	case int:
		n = int64(v)
	case int64:
		n = v
	default:
		return fallback
	}
	if n <= 0 {
		return fallback
	}
	return n
}
