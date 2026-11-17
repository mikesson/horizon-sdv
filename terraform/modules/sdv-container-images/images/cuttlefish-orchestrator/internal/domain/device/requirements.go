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

// Interaction is the declared use of a device; the platform maps it to host
// placement constraints (interactive prefers GPU-capable hosts).
type Interaction string

// Interaction values.
const (
	// InteractionInteractive: the device will be viewed and controlled in the
	// browser; GPU-accelerated rendering is preferred.
	InteractionInteractive Interaction = "interactive"

	// InteractionHeadless: ADB/automation use; software rendering suffices.
	InteractionHeadless Interaction = "headless"
)

// CapacityModel selects the provisioning economics of an auto-provisioned
// host: discounted-but-preemptible Spot capacity, standard on-demand, or the
// operator-policy default.
type CapacityModel string

// CapacityModel values.
const (
	// CapacityAuto (and the zero value) lets operator policy decide: headless
	// intent resolves to Spot when spot.auto_default is enabled, interactive
	// always to standard.
	CapacityAuto CapacityModel = "auto"

	// CapacitySpot requires Spot capacity. The create fails when no viable
	// Spot zone exists, instead of falling back to on-demand.
	CapacitySpot CapacityModel = "spot"

	// CapacityStandard requires standard on-demand capacity.
	CapacityStandard CapacityModel = "standard"
)

// Architecture is the CPU architecture a device's guest requires. Cuttlefish
// runs the guest on the host's own architecture (an arm64 guest needs an
// arm64 host), so this is a hard constraint on the machine, never a
// preference.
type Architecture string

// Architecture values, matching the machine catalog's vocabulary.
const (
	// ArchitectureX86 (and the zero value): an x86_64 guest on an x86 host,
	// which is every device this platform built before the value existed.
	ArchitectureX86 Architecture = "x86"

	// ArchitectureARM: an arm64 guest, which only an arm64 bare-metal host can
	// run.
	ArchitectureARM Architecture = "arm"
)

// Requirements is a declarative create: the caller states what it needs and
// the platform resolves the build and host (placing onto existing capacity or
// provisioning a new host from operator policy defaults).
type Requirements struct {
	// Build selects the Android build. The zero value means the platform
	// default build (auto-registered from the upstream default when no builds
	// are registered).
	Build BuildRequirement `json:"build,omitzero"`

	// Interaction declares how the device will be used.
	Interaction Interaction `json:"interaction"`

	// GPURequired makes GPU capacity a hard constraint: the create fails
	// instead of degrading to software rendering when none exists.
	GPURequired bool `json:"gpu_required,omitempty"`

	// Pool names the subset of the fleet this device's host must belong to,
	// overriding what the requirements above would derive. Absent is the
	// ordinary case and the one to prefer: the platform routes a create from
	// what it declared it needs, and naming a pool is for the case that is not
	// yet a requirement anyone can state.
	//
	// A pool this deployment does not offer is refused rather than fallen back
	// from. The caller asked for particular machines, and silently serving
	// them another pool's would be the wrong answer reported as success.
	Pool string `json:"pool,omitempty"`

	// Location biases host placement toward a region. Absent means
	// capacity-ranked placement (the CI default). For interactive intent the
	// preference dominates the ranking (WebRTC latency is bound to the host
	// zone); for headless it tie-breaks.
	Location *LocationRequirement `json:"location,omitempty"`

	// Capacity selects the provisioning economics when a host is
	// auto-provisioned. Ignored when the device lands on an existing host,
	// whose model is already fixed.
	Capacity CapacityModel `json:"capacity,omitempty"`

	// Architecture is the guest's CPU architecture. Absent means x86. An arm64
	// guest is routed into the arm pool's bare-metal machines, and is refused
	// in words while this deployment cannot build one. Falling back to an x86
	// host would place a guest on a machine that cannot run it.
	Architecture Architecture `json:"architecture,omitempty" enum:"x86,arm,"`
}

// LocationRequirement is the declarative placement preference.
type LocationRequirement struct {
	// PreferredRegion is a GCE region (e.g. "us-central1"). A preference,
	// never a constraint: placement still lands wherever capacity exists.
	PreferredRegion string `json:"preferred_region"`
}

// Validate checks the requirement rules every declarative admission shares
// (device create and batch create): a known interaction and an unambiguous
// build selection. Caller-specific rules (host_id exclusivity, feature wiring)
// stay with the caller.
func (r Requirements) Validate() error {
	if r.Build.Registered != "" && r.Build.TrackBranch != "" {
		return &ValidationError{Message: "at most one of requirements.build.registered and requirements.build.track may be set"}
	}
	switch r.Interaction {
	case InteractionInteractive, InteractionHeadless:
	default:
		return &ValidationError{Message: `requirements.interaction must be "interactive" or "headless"`}
	}
	switch r.Capacity {
	case "", CapacityAuto, CapacitySpot, CapacityStandard:
	default:
		return &ValidationError{Message: `requirements.capacity must be "spot", "standard", or "auto"`}
	}
	switch r.Architecture {
	case "", ArchitectureX86, ArchitectureARM:
		return nil
	default:
		return &ValidationError{Message: `requirements.architecture must be "x86" or "arm"`}
	}
}

// BuildRequirement selects the build for a declarative create. At most one of
// Registered and Track may be set; the zero value means the platform default.
type BuildRequirement struct {
	// Registered is the ID of an already-registered build.
	Registered string `json:"registered,omitempty"`

	// TrackBranch/TrackTarget track the latest build on an AOSP branch and
	// target (registered as a tracking build on first use).
	TrackBranch string `json:"track_branch,omitempty"`
	TrackTarget string `json:"track_target,omitempty"`
}
