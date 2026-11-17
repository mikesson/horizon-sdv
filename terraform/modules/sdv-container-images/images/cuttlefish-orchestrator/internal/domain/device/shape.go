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

package device

// The shape one Android VM takes when nothing names one. Two callers need the
// same answer and must not each invent it: the payload builder sends these to
// the host for a create that carried no env_config, and placement sizes a
// host's machine type from them for every instance whose config left the
// numbers out. A second copy would let a create ask for more than the host it
// was sized for could hold.
const (
	// DefaultInstanceVCPUs is the guest vCPU count for an instance that
	// declares none.
	DefaultInstanceVCPUs int64 = 8

	// DefaultInstanceMemoryMB is the guest RAM for an instance that declares
	// none. Unlike vCPUs this is really allocated (crosvm maps it), so it is
	// the quantity that decides how many instances a host actually holds.
	DefaultInstanceMemoryMB int64 = 8192
)

// HostReserveMemoryMB is the RAM a host keeps for its own processes: the Host
// Orchestrator, the operator, cvd, netsim/rootcanal, adb and the OS. It
// exists so a group cannot size itself to exactly the machine's
// memory and leave the launcher that has to start it with nothing.
//
// Here rather than in the placement slice because three callers now apply the
// same packing arithmetic to it: the resolver sizing a host, the catalog
// composing every shape's fit, and the operations card reporting the device
// capacity a standby target stands for.
const HostReserveMemoryMB int64 = 4096

// The CPU half of the packing bound. Guest vCPUs are oversubscribed on
// purpose, because the 8 nominal vCPUs above are crosvm threads and not pinned
// cores, so the packing term uses the EFFECTIVE per-guest footprint, measured
// rather than declared: ten phone guests ran healthy on a 48-core host at
// single-digit CPU (density probe, 2026-08-14). The pair is calibrated so the
// fits that probe validated are unchanged, (48-2)/2 = 23, while a machine wide
// on RAM and narrow on cores gets the bound memory alone missed.
const (
	// HostReserveVCPUs is the CPU the host keeps for itself, the CPU shadow of
	// the memory reserve above.
	HostReserveVCPUs int64 = 2

	// EffectiveInstanceVCPUs is the measured steady-state CPU footprint one
	// guest costs the packing, NOT the nominal guest width.
	EffectiveInstanceVCPUs int64 = 2
)

// MemoryFitInstances is how many default-shaped guests a machine of this size
// holds once the host's own reserve is taken out. It is a hard bound on
// packing rather than an estimate, because the guest RAM above is really
// allocated.
//
// ok is false for a machine that does not state its size: it states no bound.
// A machine that states a size too small to hold even one guest reports 0 with
// ok true, which is an answer and not a missing one. The result is never
// negative, because integer division truncates toward zero, so a reserve
// larger than the machine yields 0.
func MemoryFitInstances(memoryGB int64) (int64, bool) {
	if memoryGB <= 0 {
		return 0, false
	}
	return (memoryGB*1024 - HostReserveMemoryMB) / DefaultInstanceMemoryMB, true
}

// GuestFitInstances is how many default-shaped guests a machine of this width
// and size holds: the memory bound above, and the effective-CPU bound, each
// applied only where Compute Engine stated the axis. A machine that states
// neither states no bound at all, and ok is false.
//
// The tighter axis wins. Memory is really allocated and CPU is really shared,
// so the two bounds fail differently. A RAM-starved packing kills guests and a
// CPU-starved one slows them, and a fit that ignored either would let one
// shape class oversubscribe its way into the other's failure.
func GuestFitInstances(vcpus, memoryGB int64) (int64, bool) {
	memFit, memOK := MemoryFitInstances(memoryGB)
	cpuFit, cpuOK := int64(0), vcpus > 0
	if cpuOK {
		cpuFit = max((vcpus-HostReserveVCPUs)/EffectiveInstanceVCPUs, 0)
	}
	switch {
	case memOK && cpuOK:
		return min(memFit, cpuFit), true
	case memOK:
		return memFit, true
	case cpuOK:
		return cpuFit, true
	}
	return 0, false
}

// HoldsOneGuest reports whether a machine of this size can run a single
// default-shaped Android VM at all. It is the admission floor for a discovered
// machine shape, since a host that cannot hold one guest is a host no device
// can ever land on.
//
// It is the packing arithmetic itself, asked at its smallest interesting
// point, rather than a second rule about size: a shape this admits and packing
// then refuses, or the reverse, would be two sizing rules disagreeing about
// the same machine.
//
// A machine that states no size states no bound, so it passes. That is the
// fail-safe direction the rest of the sizing rules take for an unmeasurable
// row, and it keeps a shape out of the catalog only when Compute Engine
// positively said it is too small.
func HoldsOneGuest(vcpus, memoryGB int64) bool {
	fit, ok := GuestFitInstances(vcpus, memoryGB)
	return !ok || fit >= 1
}

// PackedSlots is how many device slots a machine of this shape carries: its
// computed fit, floored at one.
//
// Never below one, because the create that provisioned the host has to land
// somewhere. The right answer for a machine too small is an oversubscription
// warning from whatever sized it, not a host nothing can be placed on. A row
// that states no size states no bound and carries one slot, the smallest
// claim the floor can make.
//
// It answers two questions that must never diverge: how many slots an
// auto-provisioned host takes, and how many devices the standing capacity a
// target stands for actually holds.
func PackedSlots(vcpus, memoryGB int64) int64 {
	fit, ok := GuestFitInstances(vcpus, memoryGB)
	if !ok {
		return 1
	}
	return max(fit, 1)
}
