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

package models

// Well-known system configuration keys.
const (
	// ConfigKeyZonePin restricts server-side zone ranking to a single zone,
	// the small-deployment operating mode where exactly one zone is networked
	// and intended. Unseeded by design; empty or missing means placement ranks
	// across the whole enabled catalog.
	ConfigKeyZonePin = "placement.zone_pin"

	// ConfigKeyZonesEnabled is the set of zones an operator has admitted for
	// host creation, as a JSON array of zone names. It is the placement space
	// and the regional-network guard in one. Subnets are regional, so ranking
	// never wanders past it.
	//
	// JSON for the reason the fleet's per-pool keys are: a set cannot be a
	// scalar. Same whole-list fail-safe too, so a value that will not
	// parse reads as NO enabled zones, which stops provisioning visibly rather
	// than sending hosts to regions nobody networked. Absent means the same
	// thing, and is where every deployment starts.
	ConfigKeyZonesEnabled = "zones.enabled"

	// ConfigKeyQuotaMarginPercent is the fraction of each regional quota limit
	// zone ranking keeps in reserve when judging headroom. Unseeded; empty or
	// missing means the compiled default (10).
	ConfigKeyQuotaMarginPercent = "placement.quota_margin_percent"

	// ConfigKeyV1ZoneIsHint controls how the legacy /v1 zone token is read:
	// absent or "true" (the default) compiles it to a location hint so legacy
	// clients get dynamic placement with zero changes; "false" preserves the
	// literal pin behavior.
	ConfigKeyV1ZoneIsHint = "placement.v1_zone_is_hint"

	// ConfigKeyDefaultGPUMachineType is the machine type for auto-provisioned
	// GPU hosts. Unseeded by design; empty or missing means the resolver
	// falls back to the first enabled GPU-capable catalog type.
	ConfigKeyDefaultGPUMachineType = "host.default_gpu_machine_type"

	// ConfigKeyDefaultUpstreamBranch and ConfigKeyDefaultUpstreamTarget
	// identify the AOSP build used when a declarative device create finds no
	// registered builds. Seeded to the public latest-release phone build.
	ConfigKeyDefaultUpstreamBranch = "build.default_upstream_branch"
	ConfigKeyDefaultUpstreamTarget = "build.default_upstream_target"

	// ConfigKeyBugreportBucket and ConfigKeyRecordingBucket name the GCS
	// buckets device artifacts are captured into, overriding the deployment
	// default (CCO_ARTIFACTS_BUCKET). Unseeded by design; the two keys may
	// name the same bucket.
	ConfigKeyBugreportBucket = "artifacts.bugreport_bucket"
	ConfigKeyRecordingBucket = "artifacts.recording_bucket"

	// ConfigKeyCaptureOnError toggles automatic bugreport capture for devices
	// that reach the error state. Absent (the default) is off; the admin
	// Artifacts page writes "true"/"false". A no-op without an artifact bucket.
	ConfigKeyCaptureOnError = "artifacts.capture_on_error"

	// The lifetime policy: how long sessions and leases run when the caller
	// does not say. Absent, malformed or out-of-range keys read as the
	// compiled defaults (4h sessions, 3 × 2h extensions, 10h host leases).
	// The caps stay compiled on purpose. The API maximums are struct tags
	// enforced before any handler runs, so raising a ceiling is a deploy,
	// never a setting.
	ConfigKeyDeviceTTLHours = "lifetimes.device_ttl_hours"
	ConfigKeyExtendHours    = "lifetimes.extend_hours"
	ConfigKeyExtendCount    = "lifetimes.extend_count"
	ConfigKeyHostTTLHours   = "lifetimes.host_ttl_hours"

	// ConfigKeyDockerMachineType and ConfigKeyDockerTTLHours shape the GCE
	// host a legacy /v1 docker-only create request compiles to (the request
	// itself carries no usable shape). Unseeded; missing, empty or malformed
	// values read as the compiled defaults in hostconfig.DefaultDockerHost.
	ConfigKeyDockerMachineType = "legacy.docker_machine_type"
	ConfigKeyDockerTTLHours    = "legacy.docker_ttl_hours"

	// ConfigKeySpotAutoDefault routes auto-capacity headless intent (single
	// devices and batch members alike) to Spot hosts. Absent (the default) is
	// off: auto capacity resolves to standard everywhere until the operator
	// enables it. Interactive intent always resolves to standard.
	ConfigKeySpotAutoDefault = "spot.auto_default"

	// ConfigKeySpotMinUptimeSingle and ConfigKeySpotMinUptimeBatch set the
	// minimum estimated-uptime tier (seconds: 60, 600, or 3600) a zone's Spot
	// capacity advice must report before placement lands there. Batch members
	// demand the hour tier by default (a CI shard dying at minute 9 of 10
	// wastes the whole run), singles the 10-minute tier. Absent or malformed
	// keys read as the compiled defaults; without fresh advice the filter is
	// inert.
	ConfigKeySpotMinUptimeSingle = "spot.min_uptime_single"
	ConfigKeySpotMinUptimeBatch  = "spot.min_uptime_batch"

	// The autonomy switches: whether a background loop may act on what it
	// detects, rather than only recording it. All three are absent by design,
	// which reads as detect-only. A loop that deletes unattended is granted
	// that power explicitly, and a failed read of these keys must land on
	// the same answer as an absent one.
	//
	// ConfigKeyDeleteOrphanVMs lets the host reconciler delete GCE instances
	// carrying the managed label with no host row behind them.
	ConfigKeyDeleteOrphanVMs = "sweeps.delete_orphan_vms"

	// ConfigKeyOrphanCVDs lets the device controller delete CVD groups the
	// host census finds with no device row behind them.
	ConfigKeyOrphanCVDs = "sweeps.orphan_cvds"

	// ConfigKeyZombieRecreate lets the device controller recreate a CVD whose
	// device row is healthy but whose guest is gone.
	ConfigKeyZombieRecreate = "sweeps.zombie_recreate"

	// ConfigKeyReaperDryRun suspends the reaper's and batch settlement's
	// deletions, leaving both loops logging what they would have done. Absent
	// (the default) is false: the reaper deletes, which is its whole job.
	// This is the one key here whose safe value is false.
	ConfigKeyReaperDryRun = "reaper.dry_run"

	// The fleet autoscaler's policy. Every one of these is absent by default,
	// and every bad state (absent, malformed, out of range, or a failed read)
	// lands on the compiled default, which for the mode is off. A loop that
	// spends money on a guess is the one outcome none of them may produce.
	//
	// Scalars, not one JSON blob, and separate keys rather than a grouped
	// value: the mode is the reversibility switch, so a malformed standby count
	// must not be able to take the mode down with it.
	//
	// ConfigKeyFleetAutoscaleMode is how much of the autoscaler is switched on
	// (off | provision_only | on). It replaced nothing. Before it, the keeper
	// had no off switch other than a floor of 0, which is a different statement.
	ConfigKeyFleetAutoscaleMode = "fleet.autoscale_mode"

	// ConfigKeyFleetPoolBounds holds every pool's two lower bounds, as a JSON
	// object keyed by pool name: {"default":{"standby":2,"floor":1}}.
	//
	// The standing capacity a pool holds free so a create can skip the
	// host-provisioning leg, beside the machines it keeps running whether or not
	// devices fill them. The two travel together because one control sets them
	// together and the keeper spends against both in one decision.
	//
	// One key for all pools, to bound the read budget. A key per pool would
	// make the number of reads the keeper's cycle opens a function of how many
	// pools an operator configured. The whole map is read whole and fails whole.
	// An unreadable value is no bounds at all, which asks for no machines.
	ConfigKeyFleetPoolBounds = "fleet.pool_bounds"

	// ConfigKeyFleetDemandHorizonSec is how far back the demand signal looks
	// for placement misses. 0 disables the signal, which is not the same as
	// "no history". It is an operator declining to let observed demand raise
	// the target at all.
	ConfigKeyFleetDemandHorizonSec = "fleet.demand_horizon_sec"

	// ConfigKeyFleetDrainGraceSec is the lease a surplus host keeps when it is
	// shortened. Bounded below the provisioning lead on purpose: at or above
	// it, a shortened host would still count as standby and the surplus loop
	// would keep shortening.
	ConfigKeyFleetDrainGraceSec = "fleet.drain_grace_sec"

	// ConfigKeyFleetPoolSchedules holds every pool's recurring wall-clock
	// windows, as a JSON object keyed by pool name. It is an operator declaring
	// demand they know is coming, where fleet.demand_horizon_sec can only report
	// demand that already arrived.
	//
	// config_value is STRING(MAX), so a JSON-valued key needs no schema change.
	ConfigKeyFleetPoolSchedules = "fleet.pool_schedules"

	// ConfigKeyFleetPoolLadders holds every pool's ordered machine-type list, as
	// a JSON object keyed by pool name. Provisioning walks a pool's list when
	// GCE has no capacity for the shape it wanted; rung 0 replaces the catalog
	// default as that pool's preferred shape, and an absent or empty list is the
	// catalog default and zone failover alone.
	//
	// Its own key, like the windows beside it, so a malformed ladder cannot take
	// the bounds down with it, and so a client editing bounds cannot clear a
	// ladder by writing back a stale read.
	ConfigKeyFleetPoolLadders = "fleet.pool_ladders"
)
