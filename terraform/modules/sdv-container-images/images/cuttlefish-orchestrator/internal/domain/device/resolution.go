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

// Resolution actions.
const (
	// BuildActionUse: an already-registered build satisfies the requirement.
	BuildActionUse = "use"
	// BuildActionRegister: a tracking build is (or would be) registered.
	BuildActionRegister = "register"
	// HostActionPlace: the device lands on an existing host with capacity.
	HostActionPlace = "place"
	// HostActionProvision: a new host is (or would be) provisioned.
	HostActionProvision = "provision"
)

// Resolution records what a declarative create resolved the intent to. It is
// returned alongside the device, and on a validate_only request instead of a
// commit, so callers see the plan the platform executed (or would execute).
type Resolution struct {
	// BuildID is empty on a validate_only plan that would register a build
	// (nothing was created, so there is no ID yet).
	BuildID     string `json:"build_id,omitempty"`
	BuildName   string `json:"build_name,omitempty"`
	BuildTarget string `json:"build_target,omitempty"`
	BuildAction string `json:"build_action" enum:"use,register" doc:"use an existing build, or register a tracking build"`
	// HostID is empty on a validate_only plan that would provision a host.
	HostID     string `json:"host_id,omitempty"`
	HostName   string `json:"host_name,omitempty"`
	HostAction string `json:"host_action" enum:"place,provision" doc:"place on existing capacity, or provision a new host"`
	// Pool is the subset of the fleet the create was routed into, as the
	// derivation answered it. A create that named no pool still lands in one,
	// and the answer decides both the hosts the device may be placed on and the
	// shape a provisioned host takes.
	Pool string `json:"pool,omitempty" doc:"Subset of the fleet the create was routed into; a create that named no pool still resolves to one"`
	// MachineType and Zone describe the provisioning shape (host_action=provision).
	MachineType string `json:"machine_type,omitempty"`
	Zone        string `json:"zone,omitempty"`
	// CapacityModel is the provisioning economics the resolution chose for a
	// provisioned host: "spot" or "standard" (host_action=provision only).
	CapacityModel string `json:"capacity_model,omitempty" doc:"Provisioning economics for the provisioned host: spot or standard"`
	// PlacementReasons is the zone ranking's trace for the chosen zone: why
	// placement picked it (headroom, health, proximity contributions and any
	// degradations). The zone is initial placement, and provisioning may still
	// fail over to another ranked zone.
	PlacementReasons []string `json:"placement_reasons,omitempty"`
	Warnings         []string `json:"warnings,omitempty" doc:"Degradations the caller should know about (e.g. software rendering fallback)"`
}
