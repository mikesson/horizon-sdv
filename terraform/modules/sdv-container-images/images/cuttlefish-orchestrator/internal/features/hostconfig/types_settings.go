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

package hostconfigfeature

import (
	"strings"

	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
)

// ETagHeader carries a setting's entity tag on its GET and write responses.
type ETagHeader struct {
	ETag string `header:"ETag" doc:"Entity tag of the stored setting; send it back as If-Match to make a write conditional"`
}

// setETag records the entity tag a handler read or wrote, quoted as RFC 9110
// requires.
func (h *ETagHeader) setETag(etag string) {
	if etag != "" {
		h.ETag = `"` + etag + `"`
	}
}

// IfMatchHeader is the optional precondition on a setting write. Absent, the
// write is unconditional.
type IfMatchHeader struct {
	IfMatch string `header:"If-Match" required:"false" doc:"Entity tag from a GET of this setting; the write fails with 412 when the stored setting no longer matches it"`
}

// ifMatch returns the bare tag the store compares. "*" matches any stored
// setting, so it adds no condition. A weak or listed tag stays as sent and
// never matches.
func (h *IfMatchHeader) ifMatch() string {
	tag := strings.TrimSpace(h.IfMatch)
	if tag == "*" {
		return ""
	}
	if len(tag) >= 2 && strings.HasPrefix(tag, `"`) && strings.HasSuffix(tag, `"`) {
		return tag[1 : len(tag)-1]
	}
	return tag
}

// The settings GET operations take no input; their handlers use *struct{}
// directly (an empty input contributes nothing to the OpenAPI spec). Each
// body type below is per-operation because its doc strings (and on PUT
// inputs the validation tags) are part of the wire contract.

// GetZonePinOutput carries the placement zone pin.
type GetZonePinOutput struct {
	ETagHeader

	Body struct {
		Value string `json:"value" doc:"Single zone placement is restricted to; empty means placement ranks across the enabled catalog"`
		IsSet bool   `json:"is_set" doc:"Whether an operator stored this value; false means the response carries the compiled or deployment default"`
	}
}

// UpdateZonePinInput sets the placement zone pin.
type UpdateZonePinInput struct {
	IfMatchHeader

	Body struct {
		Value string `json:"value" pattern:"^([a-z]+-[a-z]+[0-9]+-[a-z])?$" maxLength:"63" doc:"GCE zone (e.g. us-central1-a); empty clears the pin"`
	}
}

// UpdateZonePinOutput echoes the stored zone pin.
type UpdateZonePinOutput struct {
	ETagHeader

	Body struct {
		Value string `json:"value" doc:"The stored zone pin"`
	}
}

// GetQuotaMarginOutput carries the placement quota reserve margin.
type GetQuotaMarginOutput struct {
	ETagHeader

	Body struct {
		Value int64 `json:"value" doc:"Percent of each regional quota limit zone ranking keeps in reserve"`
		IsSet bool  `json:"is_set" doc:"Whether an operator stored this value; false means the response carries the compiled or deployment default"`
	}
}

// UpdateQuotaMarginInput sets the placement quota reserve margin.
type UpdateQuotaMarginInput struct {
	IfMatchHeader

	Body struct {
		Value int64 `json:"value" minimum:"0" maximum:"90" doc:"Percent of each regional quota limit to keep in reserve"`
	}
}

// UpdateQuotaMarginOutput echoes the stored margin.
type UpdateQuotaMarginOutput struct {
	ETagHeader

	Body struct {
		Value int64 `json:"value" doc:"The stored margin percent"`
	}
}

// GetV1ZoneHintOutput carries the legacy zone-token mode.
type GetV1ZoneHintOutput struct {
	ETagHeader

	Body struct {
		Value bool `json:"value" doc:"Whether the legacy /v1 zone token is read as a placement hint (true, default) or a literal pin (false)"`
		IsSet bool `json:"is_set" doc:"Whether an operator stored this value; false means the response carries the compiled or deployment default"`
	}
}

// UpdateV1ZoneHintInput sets the legacy zone-token mode.
type UpdateV1ZoneHintInput struct {
	IfMatchHeader

	Body struct {
		Value bool `json:"value" doc:"true reads the /v1 zone token as a placement hint; false pins to it literally"`
	}
}

// UpdateV1ZoneHintOutput echoes the stored mode.
type UpdateV1ZoneHintOutput struct {
	ETagHeader

	Body struct {
		Value bool `json:"value" doc:"The stored mode"`
	}
}

// GetSpotAutoDefaultOutput carries the Spot auto-default.
type GetSpotAutoDefaultOutput struct {
	ETagHeader

	Body struct {
		Value bool `json:"value" doc:"Whether auto-capacity headless intent resolves to Spot hosts (default false)"`
		IsSet bool `json:"is_set" doc:"Whether an operator stored this value; false means the response carries the compiled or deployment default"`
	}
}

// UpdateSpotAutoDefaultInput sets the Spot auto-default.
type UpdateSpotAutoDefaultInput struct {
	IfMatchHeader

	Body struct {
		Value bool `json:"value" doc:"true routes auto-capacity headless intent to Spot hosts; false keeps standard on-demand"`
	}
}

// UpdateSpotAutoDefaultOutput echoes the stored value.
type UpdateSpotAutoDefaultOutput struct {
	ETagHeader

	Body struct {
		Value bool `json:"value" doc:"The stored value"`
	}
}

// dockerHostBody is the wire shape of the docker-host policy: the GCE shape
// a legacy /v1 docker-only host create compiles to, plus the compiled TTL cap
// so clients render the bound without retyping it.
type dockerHostBody struct {
	MachineType string `json:"machine_type" doc:"GCE machine type a docker-shape create provisions"`
	TTLHours    int64  `json:"ttl_hours" doc:"Lease (hours) a docker-shape host starts with; its CVDs have no device rows, so nothing can ever extend it"`
	IsSet       bool   `json:"is_set,omitempty" doc:"Whether an operator stored this policy; false means the compiled defaults"`
	MaxTTLHours int64  `json:"max_ttl_hours" doc:"Compiled cap on ttl_hours; raising it is a deploy, not a setting"`
}

// GetDockerHostOutput carries the docker-host policy.
type GetDockerHostOutput struct {
	ETagHeader

	Body dockerHostBody
}

// UpdateDockerHostInput sets the docker-host policy. One endpoint for the
// pair: the two values describe one host shape, and separate PUTs would land
// two audit entries for one conceptual change. The machine type is validated
// against the catalog at create time, not here, because the catalog can change
// after the key is set.
type UpdateDockerHostInput struct {
	IfMatchHeader

	Body struct {
		MachineType string `json:"machine_type" pattern:"^[a-z][a-z0-9-]*$" maxLength:"63" doc:"Catalog machine type (e.g. n1-standard-8)"`
		TTLHours    int64  `json:"ttl_hours" minimum:"1" maximum:"168" doc:"Lease (hours) a docker-shape host starts with"`
	}
}

// UpdateDockerHostOutput echoes the stored policy.
type UpdateDockerHostOutput struct {
	ETagHeader

	Body dockerHostBody
}

// spotMinUptimeBody is the wire shape of the Spot min-uptime tiers.
type spotMinUptimeBody struct {
	SingleSeconds int64 `json:"single_seconds" doc:"Minimum estimated-uptime tier for single-device Spot placement (seconds)"`
	BatchSeconds  int64 `json:"batch_seconds" doc:"Minimum estimated-uptime tier for batch-member Spot placement (seconds)"`
	IsSet         bool  `json:"is_set,omitempty" doc:"Whether an operator stored these tiers; false means both are the compiled defaults"`
}

// GetSpotMinUptimeOutput carries the Spot min-uptime tiers.
type GetSpotMinUptimeOutput struct {
	ETagHeader

	Body spotMinUptimeBody
}

// UpdateSpotMinUptimeInput sets the Spot min-uptime tiers. The advice API
// discretizes uptime to the 60/600/3600 tiers, so only those values (and 0 =
// no filter) are meaningful.
type UpdateSpotMinUptimeInput struct {
	IfMatchHeader

	Body struct {
		SingleSeconds int64 `json:"single_seconds" enum:"0,60,600,3600" doc:"Minimum uptime tier for single devices (0 disables the filter)"`
		BatchSeconds  int64 `json:"batch_seconds" enum:"0,60,600,3600" doc:"Minimum uptime tier for batch members (0 disables the filter)"`
	}
}

// UpdateSpotMinUptimeOutput echoes the stored tiers.
type UpdateSpotMinUptimeOutput struct {
	ETagHeader

	Body spotMinUptimeBody
}

// GetDefaultGPUMachineTypeOutput carries the GPU host machine-type override.
type GetDefaultGPUMachineTypeOutput struct {
	ETagHeader

	Body struct {
		Value string `json:"value" doc:"Machine type for auto-provisioned GPU hosts; empty means the first enabled GPU-capable catalog type"`
		IsSet bool   `json:"is_set" doc:"Whether an operator stored this value; false means the response carries the compiled or deployment default"`
	}
}

// UpdateDefaultGPUMachineTypeInput sets the GPU host machine-type override.
type UpdateDefaultGPUMachineTypeInput struct {
	IfMatchHeader

	Body struct {
		Value string `json:"value" pattern:"^([a-z][a-z0-9-]*)?$" maxLength:"63" doc:"Catalog machine type (e.g. g2-standard-8); empty clears the override"`
	}
}

// UpdateDefaultGPUMachineTypeOutput echoes the stored machine-type override.
type UpdateDefaultGPUMachineTypeOutput struct {
	ETagHeader

	Body struct {
		Value string `json:"value" doc:"The stored machine-type override"`
	}
}

// GetDefaultUpstreamBuildOutput carries the upstream default build pair.
type GetDefaultUpstreamBuildOutput struct {
	ETagHeader

	Body struct {
		Branch string `json:"branch" doc:"AOSP branch used when no builds are registered"`
		Target string `json:"target" doc:"AOSP build target paired with the branch"`
		IsSet  bool   `json:"is_set" doc:"Whether an operator stored this pair; false means the seeded default"`
	}
}

// UpdateDefaultUpstreamBuildInput sets the upstream default build pair.
type UpdateDefaultUpstreamBuildInput struct {
	IfMatchHeader

	Body struct {
		Branch string `json:"branch" minLength:"1" maxLength:"100" doc:"AOSP branch (e.g. aosp-android-latest-release)"`
		Target string `json:"target" minLength:"1" maxLength:"100" doc:"AOSP build target (e.g. aosp_cf_x86_64_only_phone-userdebug)"`
	}
}

// UpdateDefaultUpstreamBuildOutput echoes the stored upstream build pair.
type UpdateDefaultUpstreamBuildOutput struct {
	ETagHeader

	Body struct {
		Branch string `json:"branch" doc:"The stored AOSP branch"`
		Target string `json:"target" doc:"The stored AOSP build target"`
	}
}

// artifactBucketsBody is the wire shape of the artifact bucket settings.
type artifactBucketsBody struct {
	BugreportBucket string `json:"bugreport_bucket" doc:"Bucket override for bugreports (empty = default)"`
	RecordingBucket string `json:"recording_bucket" doc:"Bucket override for screen recordings (empty = default)"`
	DefaultBucket   string `json:"default_bucket" doc:"Deployment-default artifacts bucket (CCO_ARTIFACTS_BUCKET; empty = artifact capture unconfigured unless overridden)"`
	CaptureOnError  bool   `json:"capture_on_error" doc:"Automatically capture a bugreport for each device that reaches the error state (no-op without a bucket)"`
	IsSet           bool   `json:"is_set,omitempty" doc:"Whether an operator stored any artifact setting; false means the deployment default alone applies"`
}

// GetArtifactBucketsOutput carries the artifact bucket settings.
type GetArtifactBucketsOutput struct {
	ETagHeader

	Body artifactBucketsBody
}

// UpdateArtifactBucketsInput sets the per-type artifact bucket overrides;
// empty clears an override (the deployment default applies).
type UpdateArtifactBucketsInput struct {
	IfMatchHeader

	Body struct {
		BugreportBucket string `json:"bugreport_bucket" required:"false" maxLength:"222" pattern:"^[a-z0-9][a-z0-9._-]*[a-z0-9]$|^$" doc:"Bucket for bugreports (empty = deployment default)"`
		RecordingBucket string `json:"recording_bucket" required:"false" maxLength:"222" pattern:"^[a-z0-9][a-z0-9._-]*[a-z0-9]$|^$" doc:"Bucket for screen recordings (empty = deployment default)"`
		CaptureOnError  bool   `json:"capture_on_error" required:"false" doc:"Automatically capture a bugreport for each device that reaches the error state (no-op without a bucket)"`
	}
}

// UpdateArtifactBucketsOutput echoes the stored artifact bucket settings.
type UpdateArtifactBucketsOutput struct {
	ETagHeader

	Body artifactBucketsBody
}

// lifetimesBody is the wire shape of the lifetime policy: the four settable
// defaults plus the compiled caps and the teardown grace, so the console and
// CLI render bounds and derived consequences (longest possible session, the
// lease a device's promise raises) without retyping a single number.
type lifetimesBody struct {
	DeviceTTLHours int64 `json:"device_ttl_hours" doc:"Session TTL (hours) a device create takes when the request names none"`
	ExtendHours    int64 `json:"extend_hours" doc:"TTL (hours) each extension grants when the request names no amount"`
	ExtendCount    int64 `json:"extend_count" doc:"Extensions an ephemeral device is allowed (0 = none: pin it or lose it)"`
	HostTTLHours   int64 `json:"host_ttl_hours" doc:"Lease (hours) an idle host starts with; a device whose expiry outlasts it raises the host's lease, so this bounds idle and standby hosts only"`
	IsSet          bool  `json:"is_set,omitempty" doc:"Whether an operator stored this policy; false means the compiled defaults"`

	MaxDeviceTTLHours    int64 `json:"max_device_ttl_hours" doc:"Compiled cap on ttl_hours at device create; raising it is a deploy, not a setting"`
	MaxExtendHours       int64 `json:"max_extend_hours" doc:"Compiled cap on a single TTL extension (hours)"`
	MaxExtendCount       int64 `json:"max_extend_count" doc:"Compiled cap on the extension allowance"`
	MaxHostTTLHours      int64 `json:"max_host_ttl_hours" doc:"Compiled cap on ttl_hours at host create and on the hours one host extend adds; repeated extends have no cumulative cap"`
	MaxDevicesPerHost    int64 `json:"max_devices_per_host" doc:"Compiled cap on max_devices at host create and on the auto-host density setting"`
	HostImageBackedSlots int64 `json:"host_image_backed_slots" doc:"Instance slots the host image preallocates a TAP set for; a slot past this exists to cvd's allocator but has no TAP device to launch against"`
	GracePeriodSeconds   int64 `json:"grace_period_seconds" doc:"Teardown margin a host keeps beyond every promise made to its devices"`
}

// GetLifetimesOutput carries the lifetime policy.
type GetLifetimesOutput struct {
	ETagHeader

	Body lifetimesBody
}

// UpdateLifetimesInput sets the lifetime policy. One endpoint for the group:
// the values constrain each other, and four scalar PUTs would land four audit
// entries for one conceptual change.
type UpdateLifetimesInput struct {
	IfMatchHeader

	Body struct {
		DeviceTTLHours int64 `json:"device_ttl_hours" minimum:"1" maximum:"8" doc:"Session TTL (hours) a device create takes when the request names none"`
		ExtendHours    int64 `json:"extend_hours" minimum:"1" maximum:"4" doc:"TTL (hours) each extension grants when the request names no amount"`
		ExtendCount    int64 `json:"extend_count" minimum:"0" maximum:"10" doc:"Extensions an ephemeral device is allowed (0 = none)"`
		HostTTLHours   int64 `json:"host_ttl_hours" minimum:"1" maximum:"168" doc:"Lease (hours) an idle host starts with"`
	}
}

// UpdateLifetimesOutput echoes the stored policy.
type UpdateLifetimesOutput struct {
	ETagHeader

	Body lifetimesBody
}

// fleetPolicyBody is the wire shape of the autoscaler policy: the stored
// values plus the compiled bounds, so the console renders limits and the
// modified marker without hardcoding a single number.
//
// Flat rather than embedding hostconfig.FleetPolicy, following lifetimesBody:
// an embedded struct's schema shape is the reflection layer's choice, and this
// body is drift-gated against the console's Zod schema, so the one thing it
// must not do is depend on how a generator decides to render inheritance.
type fleetPolicyBody struct {
	Mode             hostconfig.AutoscaleMode        `json:"mode" enum:"off,provision_only,on" doc:"How much of the autoscaler is switched on: off publishes a target and provisions nothing, provision_only may only add capacity, on may also give it back"`
	Tiers            map[string]hostconfig.FleetTier `json:"tiers" doc:"Standing-capacity intent per pool, keyed by pool name; the default pool is always present"`
	Floor            int64                           `json:"floor" doc:"The default pool's floor: hosts that stand whether or not devices fill them; scale-in stops here (0 = none)"`
	DemandHorizonSec int64                           `json:"demand_horizon_sec" doc:"How far back the demand signal looks for placement misses, in seconds (0 disables it)"`
	DrainGraceSec    int64                           `json:"drain_grace_sec" doc:"Lease (seconds) a host keeps when it is shortened as surplus"`
	ShapeLadder      []string                        `json:"shape_ladder,omitempty" required:"false" doc:"The default pool's machine types, in preference order; empty means the catalog default"`
	IsSet            bool                            `json:"is_set,omitempty" doc:"Whether an operator stored this policy; false means at least one field is the compiled default"`

	MaxStandby          int64 `json:"max_standby" doc:"Compiled cap on a pool's standby count and on its composed target; raising it is a deploy, not a setting"`
	MaxFloor            int64 `json:"max_floor" doc:"Compiled cap on a pool's floor; raising it is a deploy, not a setting"`
	MaxDemandHorizonSec int64 `json:"max_demand_horizon_sec" doc:"Compiled cap on the demand horizon (seconds)"`
	MinDrainGraceSec    int64 `json:"min_drain_grace_sec" doc:"Compiled floor on the drain grace (seconds)"`
	MaxDrainGraceSec    int64 `json:"max_drain_grace_sec" doc:"Compiled cap on the drain grace; it is one second below the provisioning lead, because a shortened host whose remaining lease reaches the lead would still count as standby"`
	MaxSchedules        int64 `json:"max_schedules" doc:"Compiled cap on how many windows a pool may hold"`
	MaxLadderRungs      int64 `json:"max_ladder_rungs" doc:"Compiled cap on how many machine types the shape ladder may hold"`
}

// GetFleetPolicyOutput carries the autoscaler policy.
type GetFleetPolicyOutput struct {
	ETagHeader

	Body fleetPolicyBody
}

// UpdateFleetAutoscalerInput sets the mode and both lower bounds, the values an
// operator moves while watching the fleet. The enum and the maxima mirror
// hostconfig.AutoscaleModes, hostconfig.MaxStandbyHosts and
// hostconfig.MaxFleetFloor, which the keeper also enforces on read; the
// agreement tests bind those copies.
//
// One body for all three: they are one policy, and a client that could set the
// mode without carrying the bounds would revert whichever it last read stale.
//
// The pool the bounds belong to rides the body rather than the path, because the
// mode beside them applies to the whole loop, and a path
// segment would say the mode was the pool's too. Writing bounds for a pool is
// also what switches it on, so an unnamed pool is the default one: every client that
// predates pools is editing exactly the pool it always was.
type UpdateFleetAutoscalerInput struct {
	IfMatchHeader

	Body struct {
		Mode    string `json:"mode" enum:"off,provision_only,on" doc:"off publishes a target and provisions nothing; provision_only may only add capacity; on may also give it back"`
		Pool    string `json:"pool,omitempty" required:"false" doc:"Which pool these two bounds belong to; empty is the default pool, and naming a pool switches it on"`
		Standby int64  `json:"standby" minimum:"0" maximum:"16" doc:"Idle placement-eligible hosts the fleet keeper holds free in this pool (0 = off)"`
		Floor   int64  `json:"floor" minimum:"0" maximum:"16" doc:"Hosts this pool stands whether or not devices fill them; scale-in stops here (0 = none)"`
	}
}

// DisableFleetPoolInput switches a pool off, naming it in the path: this is a
// deletion of everything stored about it, not an edit of one of its values.
type DisableFleetPoolInput struct {
	IfMatchHeader

	Pool string `path:"pool" doc:"The pool to switch off; the default pool cannot be"`
}

// DisableFleetPoolOutput echoes the stored policy, so a caller sees the fleet
// the disable left behind rather than having to read it back.
type DisableFleetPoolOutput struct {
	ETagHeader

	Body fleetPolicyBody
}

// UpdateFleetAutoscalerOutput echoes the stored policy.
type UpdateFleetAutoscalerOutput struct {
	ETagHeader

	Body fleetPolicyBody
}

// UpdateFleetTuningInput sets the two tuning scalars. Separate from the
// autoscaler PUT above so the operations card and the settings page cannot
// overwrite each other's fields with whatever they last read.
type UpdateFleetTuningInput struct {
	IfMatchHeader

	Body struct {
		DemandHorizonSec int64 `json:"demand_horizon_sec" minimum:"0" maximum:"86400" doc:"How far back the demand signal looks for placement misses, in seconds (0 disables it)"`
		DrainGraceSec    int64 `json:"drain_grace_sec" minimum:"120" maximum:"599" doc:"Lease (seconds) a host keeps when it is shortened as surplus"`
	}
}

// UpdateFleetTuningOutput echoes the stored policy.
type UpdateFleetTuningOutput struct {
	ETagHeader

	Body fleetPolicyBody
}

// UpdateFleetSchedulesInput replaces the window list whole.
//
// Its own endpoint rather than a third field on the tuning PUT, and the reason
// is the same one that split the tuning PUT off the autoscaler PUT. It just
// took a second client to see it. A list carried on the tuning body has to be
// re-sent by everything that changes a horizon, so cfctl's read-overlay-write
// would clear every window on `set fleet demand_horizon_sec=1800` unless it
// remembered to carry them. That is an omission with no symptom at the call
// site and a destructive result, and a separate endpoint makes it
// unrepresentable instead of something to remember.
//
// Sending the list replaces it, so an empty array is how every window is
// removed. Explicit, because there is no other verb.
type UpdateFleetSchedulesInput struct {
	IfMatchHeader

	Body struct {
		Pool      string                     `json:"pool,omitempty" required:"false" doc:"Which pool's windows these are; empty is the default pool"`
		Schedules []hostconfig.FleetSchedule `json:"schedules" maxItems:"8" doc:"Recurring windows that raise this pool's target while open; replaces the stored list whole, and an empty array clears it"`
	}
}

// UpdateFleetSchedulesOutput echoes the stored policy.
type UpdateFleetSchedulesOutput struct {
	ETagHeader

	Body fleetPolicyBody
}

// UpdateFleetLadderInput replaces the machine-type preference order whole.
//
// Its own endpoint for the reason the schedules list has one: a list carried on
// a body that also holds scalars must be re-sent by every client that edits one
// of those scalars, and cfctl edits them by reading, overlaying and writing
// back, so a drain-grace change would clear the ladder. The split makes that
// unrepresentable rather than something each client has to remember.
//
// Sending the list replaces it, so an empty array is how the ladder is removed
// and the catalog default restored. Explicit, because there is no other verb.
//
// Which rungs are admissible depends on the pool that will own the ladder. Bare
// metal belongs to an arm pool and an integrated GPU to the GPU one, so the
// pool travels with the list and the server judges the pair.
type UpdateFleetLadderInput struct {
	IfMatchHeader

	Body struct {
		Pool        string   `json:"pool,omitempty" required:"false" doc:"Which pool's provisioning order this is; empty is the default pool"`
		ShapeLadder []string `json:"shape_ladder" maxItems:"8" doc:"Machine types this pool's provisioning prefers, in order; replaces the stored ladder whole, and an empty array restores the catalog default"`
	}
}

// UpdateFleetLadderOutput echoes the stored policy.
type UpdateFleetLadderOutput struct {
	ETagHeader

	Body fleetPolicyBody
}

// GetSweepPolicyOutput carries what each loop is currently allowed to do.
type GetSweepPolicyOutput struct {
	ETagHeader

	Body hostconfig.SweepPolicy
}

// UpdateSweepPolicyInput changes the switches it names and leaves the rest
// alone: one flip is one audited change, not a rewrite of all four.
type UpdateSweepPolicyInput struct {
	IfMatchHeader

	Body struct {
		DeleteOrphanVMs *bool `json:"delete_orphan_vms,omitempty" doc:"Let the host reconciler delete managed GCE instances with no host row"`
		OrphanCVDs      *bool `json:"orphan_cvds,omitempty" doc:"Let the device controller delete CVD groups with no device row"`
		ZombieRecreate  *bool `json:"zombie_recreate,omitempty" doc:"Let the device controller recreate a CVD whose guest is gone"`
		ReaperDryRun    *bool `json:"reaper_dry_run,omitempty" doc:"Suspend the reaper's and batch settlement's deletions"`
	}
}

// UpdateSweepPolicyOutput echoes the stored policy.
type UpdateSweepPolicyOutput struct {
	ETagHeader

	Body hostconfig.SweepPolicy
}
