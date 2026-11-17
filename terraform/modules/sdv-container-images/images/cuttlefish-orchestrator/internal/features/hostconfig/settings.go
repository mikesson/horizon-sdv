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
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// GetZonePin returns the single zone placement is restricted to; a missing
// key reads as empty (placement ranks across the enabled catalog).
func (s *Service) GetZonePin(ctx context.Context) (string, bool, error) {
	return s.optionalConfig(ctx, models.ConfigKeyZonePin)
}

// UpdateZonePin restricts placement to a single zone; empty clears the pin.
func (s *Service) UpdateZonePin(ctx context.Context, zone string) error {
	return s.setConfig(ctx, models.ConfigKeyZonePin, zone,
		"Single zone placement is restricted to (empty = ranked across the enabled catalog)", "zone pin")
}

// defaultQuotaMarginPercent mirrors the ranking default for a missing or
// malformed placement.quota_margin_percent key.
const defaultQuotaMarginPercent = 10

// GetQuotaMargin returns the percent of each regional quota limit zone
// ranking keeps in reserve; a missing or malformed key reads as the default.
func (s *Service) GetQuotaMargin(ctx context.Context) (int64, bool, error) {
	raw, stored, err := s.optionalConfig(ctx, models.ConfigKeyQuotaMarginPercent)
	if err != nil {
		return 0, false, err
	}
	if margin, perr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); perr == nil && margin >= 0 && margin <= 90 {
		return margin, stored, nil
	}
	// A malformed stored value reads as the compiled default, so it is not a
	// value an operator chose. Reporting it as stored would offer a reset
	// that changes nothing visible.
	return defaultQuotaMarginPercent, false, nil
}

// UpdateQuotaMargin sets the placement quota reserve margin.
func (s *Service) UpdateQuotaMargin(ctx context.Context, margin int64) error {
	return s.setConfig(ctx, models.ConfigKeyQuotaMarginPercent, strconv.FormatInt(margin, 10),
		"Percent of each regional quota limit zone ranking keeps in reserve", "quota margin")
}

// GetV1ZoneHint returns whether the legacy /v1 zone token is read as a
// placement hint; a missing key reads as true (the default).
func (s *Service) GetV1ZoneHint(ctx context.Context) (bool, bool, error) {
	return s.boolConfig(ctx, models.ConfigKeyV1ZoneIsHint, true, "v1 zone-hint mode")
}

// UpdateV1ZoneHint sets whether the legacy /v1 zone token is read as a
// placement hint (true) or a literal pin (false).
func (s *Service) UpdateV1ZoneHint(ctx context.Context, hint bool) error {
	return s.setConfig(ctx, models.ConfigKeyV1ZoneIsHint, strconv.FormatBool(hint),
		"Whether the legacy /v1 zone token is a placement hint (true) or a literal pin (false)", "v1 zone-hint mode")
}

// GetDockerHost returns the GCE shape a legacy /v1 docker-only host create
// compiles to; missing, empty, malformed or out-of-range keys read as the
// compiled defaults. Also the legacy facade's policy source, so what the
// settings card shows is exactly what the next docker create provisions.
func (s *Service) GetDockerHost(ctx context.Context) (hostconfig.DockerHost, error) {
	policy := hostconfig.DefaultDockerHost()
	machineType, mtStored, err := s.optionalConfig(ctx, models.ConfigKeyDockerMachineType)
	if err != nil {
		return hostconfig.DockerHost{}, err
	}
	machineType = strings.TrimSpace(machineType)
	if machineType != "" {
		policy.MachineType = machineType
	}
	raw, ttlStored, err := s.optionalConfig(ctx, models.ConfigKeyDockerTTLHours)
	if err != nil {
		return hostconfig.DockerHost{}, err
	}
	ttl, ttlOK := hostconfig.PolicyInt64(raw, 1, host.MaxHostTTLHours, policy.TTLHours)
	policy.TTLHours = ttl
	policy.IsSet = mtStored && machineType != "" && ttlStored && ttlOK
	return policy, nil
}

// UpdateDockerHost stores the docker-host shape, both keys in one write. The
// machine type is not checked against the catalog here, because the catalog
// can change after the key is set and the create path validates it anyway. A
// stale value surfaces as a create-time 422, not a silent fallback.
func (s *Service) UpdateDockerHost(ctx context.Context, machineType string, ttlHours int64) error {
	return s.setConfigs(ctx, "the docker-host shape", []spanner.ConfigEntry{
		{
			Key: models.ConfigKeyDockerMachineType, Value: machineType,
			Description: "GCE machine type a legacy docker-shape host create provisions",
		},
		{
			Key: models.ConfigKeyDockerTTLHours, Value: strconv.FormatInt(ttlHours, 10),
			Description: "Lease (hours) a legacy docker-shape host starts with; nothing can extend it",
		},
	})
}

// GetSpotAutoDefault returns whether auto-capacity headless intent resolves to
// Spot hosts; a missing key reads as false (standard everywhere).
func (s *Service) GetSpotAutoDefault(ctx context.Context) (bool, bool, error) {
	return s.boolConfig(ctx, models.ConfigKeySpotAutoDefault, false, "spot auto-default")
}

// UpdateSpotAutoDefault sets whether auto-capacity headless intent (single
// devices and batch members) resolves to Spot hosts.
func (s *Service) UpdateSpotAutoDefault(ctx context.Context, enabled bool) error {
	return s.setConfig(ctx, models.ConfigKeySpotAutoDefault, strconv.FormatBool(enabled),
		"Route auto-capacity headless intent (devices and batch members) to Spot hosts", "spot auto-default")
}

// Compiled min-uptime defaults, mirroring the placement resolver's for
// missing or malformed keys.
const (
	defaultSpotMinUptimeSingle = 600
	defaultSpotMinUptimeBatch  = 3600
)

// SpotMinUptimes are the minimum estimated-uptime tiers (seconds) Spot
// placement demands, with whether an operator stored them.
type SpotMinUptimes struct {
	Single int64
	Batch  int64
	IsSet  bool
}

// GetSpotMinUptimes returns the minimum estimated-uptime tiers (seconds) Spot
// placement demands for single devices and batch members; missing or
// malformed keys read as the compiled defaults.
func (s *Service) GetSpotMinUptimes(ctx context.Context) (SpotMinUptimes, error) {
	single, singleStored, err := s.uptimeTier(ctx, models.ConfigKeySpotMinUptimeSingle, defaultSpotMinUptimeSingle)
	if err != nil {
		return SpotMinUptimes{}, err
	}
	batch, batchStored, err := s.uptimeTier(ctx, models.ConfigKeySpotMinUptimeBatch, defaultSpotMinUptimeBatch)
	if err != nil {
		return SpotMinUptimes{}, err
	}
	// Written as one pair, so reported as one.
	return SpotMinUptimes{Single: single, Batch: batch, IsSet: singleStored && batchStored}, nil
}

// uptimeTier reads one min-uptime key, defaulting when absent or malformed.
func (s *Service) uptimeTier(ctx context.Context, key string, fallback int64) (int64, bool, error) {
	raw, stored, err := s.optionalConfig(ctx, key)
	if err != nil {
		return 0, false, err
	}
	if tier, perr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); perr == nil && tier >= 0 {
		return tier, stored, nil
	}
	return fallback, false, nil
}

// UpdateSpotMinUptimes sets the minimum estimated-uptime tiers Spot placement
// demands.
func (s *Service) UpdateSpotMinUptimes(ctx context.Context, single, batch int64) error {
	return s.setConfigs(ctx, "the Spot min-uptime tiers", []spanner.ConfigEntry{
		{
			Key: models.ConfigKeySpotMinUptimeSingle, Value: strconv.FormatInt(single, 10),
			Description: "Minimum Spot estimated-uptime tier (seconds) for single-device placement",
		},
		{
			Key: models.ConfigKeySpotMinUptimeBatch, Value: strconv.FormatInt(batch, 10),
			Description: "Minimum Spot estimated-uptime tier (seconds) for batch-member placement",
		},
	})
}

// GetDefaultGPUMachineType returns the machine type for auto-provisioned GPU
// hosts; a missing key reads as empty (catalog fallback applies).
func (s *Service) GetDefaultGPUMachineType(ctx context.Context) (string, bool, error) {
	return s.optionalConfig(ctx, models.ConfigKeyDefaultGPUMachineType)
}

// UpdateDefaultGPUMachineType sets the machine type for auto-provisioned GPU
// hosts; empty clears the override. The value is not checked against the
// catalog here, because the catalog can change after the key is set, so the
// resolver treats a stale or disabled value as unset and falls back.
func (s *Service) UpdateDefaultGPUMachineType(ctx context.Context, machineType string) error {
	return s.setConfig(ctx, models.ConfigKeyDefaultGPUMachineType, machineType,
		"Machine type for auto-provisioned GPU hosts (empty = first enabled GPU-capable catalog type)", "default GPU machine type")
}

// UpstreamBuild is the AOSP branch/target pair a declarative create falls back
// to, with whether an operator stored it.
type UpstreamBuild struct {
	Branch string
	Target string
	IsSet  bool
}

// GetDefaultUpstreamBuild returns the AOSP branch/target used when a
// declarative create finds no registered builds. Missing keys read as empty
// (pre-seed databases).
func (s *Service) GetDefaultUpstreamBuild(ctx context.Context) (UpstreamBuild, error) {
	branch, branchStored, err := s.optionalConfig(ctx, models.ConfigKeyDefaultUpstreamBranch)
	if err != nil {
		return UpstreamBuild{}, err
	}
	target, targetStored, err := s.optionalConfig(ctx, models.ConfigKeyDefaultUpstreamTarget)
	if err != nil {
		return UpstreamBuild{}, err
	}
	// One pair, one answer: the two keys are always written together, so a
	// half-stored state is not one the console should offer to reset.
	return UpstreamBuild{
		Branch: branch, Target: target, IsSet: branchStored && targetStored,
	}, nil
}

// UpdateDefaultUpstreamBuild sets the AOSP branch/target pair used when a
// declarative create finds no registered builds.
func (s *Service) UpdateDefaultUpstreamBuild(ctx context.Context, branch, target string) error {
	return s.setConfigs(ctx, "the default upstream build", []spanner.ConfigEntry{
		{
			Key: models.ConfigKeyDefaultUpstreamBranch, Value: branch,
			Description: "AOSP branch used when a declarative device create finds no registered builds",
		},
		{
			Key: models.ConfigKeyDefaultUpstreamTarget, Value: target,
			Description: "AOSP build target paired with build.default_upstream_branch",
		},
	})
}

// ArtifactBuckets are the artifact-storage settings: the per-type bucket
// overrides ("" = unset), the deployment default they fall back to, and the
// capture-on-error toggle.
type ArtifactBuckets struct {
	Bugreport      string
	Recording      string
	DefaultBucket  string
	CaptureOnError bool
	// IsSet reports whether either bucket override is stored. The buckets
	// already publish DefaultBucket, so this is about the toggle and the
	// reset affordance, not about resolving the effective value.
	IsSet bool
}

// GetArtifactBuckets returns the artifact bucket settings.
func (s *Service) GetArtifactBuckets(ctx context.Context) (*ArtifactBuckets, error) {
	bugreport, bugreportStored, err := s.optionalConfig(ctx, models.ConfigKeyBugreportBucket)
	if err != nil {
		return nil, err
	}
	recording, recordingStored, err := s.optionalConfig(ctx, models.ConfigKeyRecordingBucket)
	if err != nil {
		return nil, err
	}
	captureOnError, captureStored, err := s.optionalConfig(ctx, models.ConfigKeyCaptureOnError)
	if err != nil {
		return nil, err
	}
	return &ArtifactBuckets{
		Bugreport:      bugreport,
		Recording:      recording,
		DefaultBucket:  s.defaultArtifactsBucket,
		CaptureOnError: captureOnError == "true",
		IsSet:          bugreportStored || recordingStored || captureStored,
	}, nil
}

// UpdateArtifactBuckets sets the per-type artifact bucket overrides (an empty
// value clears the override, so the deployment default applies) and the
// capture-on-error toggle.
func (s *Service) UpdateArtifactBuckets(ctx context.Context, bugreport, recording string, captureOnError bool) error {
	return s.setConfigs(ctx, "the artifact buckets", []spanner.ConfigEntry{
		{
			Key: models.ConfigKeyBugreportBucket, Value: bugreport,
			Description: "GCS bucket for device bugreports (empty = deployment default)",
		},
		{
			Key: models.ConfigKeyRecordingBucket, Value: recording,
			Description: "GCS bucket for device screen recordings (empty = deployment default)",
		},
		{
			Key: models.ConfigKeyCaptureOnError, Value: strconv.FormatBool(captureOnError),
			Description: "Automatically capture a bugreport for each device that reaches the error state",
		},
	})
}

// GetLifetimes returns the lifetime policy; missing, malformed or
// out-of-range keys read as the compiled defaults. IsSet reports the whole
// group: the PUT writes all four keys together, so a partial or unparseable
// store reads as "not a policy an operator chose".
func (s *Service) GetLifetimes(ctx context.Context) (hostconfig.Lifetimes, error) {
	defaults := hostconfig.DefaultLifetimes()
	policy := defaults
	stored := true
	for _, field := range []struct {
		key      string
		lo, hi   int64
		into     *int64
		fallback int64
	}{
		{models.ConfigKeyDeviceTTLHours, 1, device.MaxTTLHours, &policy.DeviceTTLHours, defaults.DeviceTTLHours},
		{models.ConfigKeyExtendHours, 1, device.MaxExtendHours, &policy.ExtendHours, defaults.ExtendHours},
		{models.ConfigKeyExtendCount, 0, int64(device.MaxExtendCount), &policy.ExtendCount, defaults.ExtendCount},
		{models.ConfigKeyHostTTLHours, 1, host.MaxHostTTLHours, &policy.HostTTLHours, defaults.HostTTLHours},
	} {
		raw, isStored, err := s.optionalConfig(ctx, field.key)
		if err != nil {
			return hostconfig.Lifetimes{}, err
		}
		value, ok := hostconfig.PolicyInt64(raw, field.lo, field.hi, field.fallback)
		*field.into = value
		stored = stored && isStored && ok
	}
	policy.IsSet = stored
	return policy, nil
}

// UpdateLifetimes stores the lifetime policy, all four keys in one write.
func (s *Service) UpdateLifetimes(ctx context.Context, policy hostconfig.Lifetimes) error {
	return s.setConfigs(ctx, "the lifetime policy", []spanner.ConfigEntry{
		{
			Key: models.ConfigKeyDeviceTTLHours, Value: strconv.FormatInt(policy.DeviceTTLHours, 10),
			Description: "Session TTL (hours) a device create takes when the request names none",
		},
		{
			Key: models.ConfigKeyExtendHours, Value: strconv.FormatInt(policy.ExtendHours, 10),
			Description: "TTL (hours) each extension grants when the request names no amount",
		},
		{
			Key: models.ConfigKeyExtendCount, Value: strconv.FormatInt(policy.ExtendCount, 10),
			Description: "Extensions an ephemeral device is allowed (0 = none)",
		},
		{
			Key: models.ConfigKeyHostTTLHours, Value: strconv.FormatInt(policy.HostTTLHours, 10),
			Description: "Lease (hours) an idle host starts with; a device whose expiry outlasts it raises the lease",
		},
	})
}

// GetHostDensity is the device capacity of the catalog's default shape: the
// composed fit, cores-and-memory computed or per-shape overridden, with the
// shape's name beside it.
//
// A catalog with no default cannot provision at all. The zero value reports
// that rather than inventing a capacity.
func (s *Service) GetHostDensity(ctx context.Context) (hostconfig.HostDensity, error) {
	mt, found, err := s.defaultMachineType(ctx)
	if err != nil || !found {
		return hostconfig.HostDensity{}, err
	}
	return hostconfig.HostDensity{
		MachineType: mt.ID,
		Effective:   max(mt.Fit, 1),
	}, nil
}

// defaultMachineType reads the catalog default. found is false when there is
// nothing to judge against: no reader wired, or no default flagged. A catalog
// with no default cannot provision at all, so declining to bound a density on
// one reports the operator's own number rather than inventing a bound; a read
// that FAILED is a different thing and is returned as one.
func (s *Service) defaultMachineType(ctx context.Context) (*machinetype.MachineType, bool, error) {
	if s.machineTypes == nil {
		return nil, false, nil
	}
	mt, err := s.machineTypes.GetDefault(ctx)
	if errors.Is(err, machinetype.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("resolving the default machine type: %w", err)
	}
	return mt, true, nil
}

// GetFleetPolicy returns the autoscaler policy; every missing, malformed or
// out-of-range key reads as its compiled default, the same answer the keeper
// itself lands on, so the card never shows a policy the keeper is not running.
//
// Six reads whatever the fleet holds: the mode, the two tuning scalars, and one
// map each of bounds, windows and ladders. The per-pool parts are maps rather
// than a key per pool, so this count cannot grow with the pools.
//
// Per key, not per group: the mode is the reversibility switch, and a
// malformed standby count must not be able to drag it back to off, or worse,
// leave a stored off looking unset.
func (s *Service) GetFleetPolicy(ctx context.Context) (hostconfig.FleetPolicy, error) {
	defaults := hostconfig.DefaultFleetPolicy()
	policy := defaults
	stored := true

	raw, modeStored, err := s.optionalConfig(ctx, models.ConfigKeyFleetAutoscaleMode)
	if err != nil {
		return hostconfig.FleetPolicy{}, err
	}
	mode := hostconfig.AutoscaleMode(strings.TrimSpace(raw))
	if mode.Valid() {
		policy.Mode = mode
	} else {
		modeStored = false
	}
	stored = stored && modeStored

	for _, field := range []struct {
		key      string
		lo, hi   int64
		into     *int64
		fallback int64
	}{
		{
			models.ConfigKeyFleetDemandHorizonSec,
			hostconfig.MinDemandHorizonSec, hostconfig.MaxDemandHorizonSec,
			&policy.DemandHorizonSec, defaults.DemandHorizonSec,
		},
		{
			models.ConfigKeyFleetDrainGraceSec,
			hostconfig.MinDrainGraceSec, hostconfig.MaxDrainGraceSec,
			&policy.DrainGraceSec, defaults.DrainGraceSec,
		},
	} {
		value, isStored, err := s.optionalConfig(ctx, field.key)
		if err != nil {
			return hostconfig.FleetPolicy{}, err
		}
		parsed, ok := hostconfig.PolicyInt64(value, field.lo, field.hi, field.fallback)
		*field.into = parsed
		stored = stored && isStored && ok
	}

	tiers, tiersStored, err := s.fleetTiers(ctx)
	if err != nil {
		return hostconfig.FleetPolicy{}, err
	}
	policy.Tiers = tiers
	stored = stored && tiersStored

	// The default pool's two bounds, published fleet-wide for the surfaces that
	// still ask about a fleet. Filled here so there is one derivation of them.
	def := policy.Pool(hostconfig.DefaultTier)
	policy.Floor = def.Floor
	policy.ShapeLadder = def.Ladder

	policy.IsSet = stored
	return policy, nil
}

// fleetTiers assembles every pool's intent from the three stored maps.
//
// Each map is read whole and fails whole, and the three fail independently: a
// ladder that will not parse costs the fleet its preferred shapes and leaves the
// bounds it says nothing about standing. Each failure direction is the one that
// spends nothing: no bounds asks for no machines, no windows leaves the target
// to standing intent and observed demand, and no ladder is the catalog default
// every deployment ran on before the key existed.
//
// The bounds map's KEY SET is the pool registry: a pool exists here because an
// operator gave it bounds, and a window or a ladder stored against a pool nobody
// enabled is carried by neither. One authority rather than the union of three,
// because enablement is asked on every create. A create routed into a pool one
// stored value knows about and another does not would be placed against machines
// no bound is holding.
//
// The default pool is always present, even with nothing stored, because it is
// what an unstamped machine belongs to: a policy that omitted it would leave the
// pool holding the fleet with no bounds to be judged against.
func (s *Service) fleetTiers(ctx context.Context) (map[string]hostconfig.FleetTier, bool, error) {
	bounds, err := s.poolBounds(ctx)
	if err != nil {
		return nil, false, err
	}
	schedules, err := s.poolSchedules(ctx)
	if err != nil {
		return nil, false, err
	}
	ladders, err := s.poolLadders(ctx)
	if err != nil {
		return nil, false, err
	}
	tiers := map[string]hostconfig.FleetTier{hostconfig.DefaultTier: {}}
	for pool, b := range bounds.pools {
		tiers[pool] = hostconfig.FleetTier{Standby: b.Standby, Floor: b.Floor}
	}
	for pool, windows := range schedules.pools {
		tier, enabled := tiers[pool]
		if !enabled {
			continue
		}
		tier.Schedules = windows
		tiers[pool] = tier
	}
	for pool, rungs := range ladders.pools {
		tier, enabled := tiers[pool]
		if !enabled {
			continue
		}
		tier.Ladder = rungs
		tiers[pool] = tier
	}
	return tiers, bounds.configured() && schedules.configured() && ladders.configured(), nil
}

// storedPools is one of the three per-pool maps as it was read: what it holds,
// whether an operator stored the key at all, and whether what they stored could
// be read.
//
// The last two are separate answers because the two callers need different
// ones. The policy read fail-safes an unreadable map to no pools configured;
// a write that overlays ONE pool must refuse it, since storing its own pool over
// a map it could not read deletes every other pool's entry while reporting
// success.
type storedPools[V any] struct {
	pools    map[string]V
	isStored bool
	readable bool
}

// configured reports whether what is stored is a policy an operator chose: a
// key they wrote, holding something that parses.
func (p storedPools[V]) configured() bool { return p.isStored && p.readable }

// readPools reads one of the three per-pool maps. unreadable is what the log
// says when what is stored will not parse. Each of the three costs the fleet
// something different, and the line is the only place that difference is said.
func readPools[V any](ctx context.Context, s *Service, key, unreadable string,
	parse func(string) (map[string]V, bool),
) (storedPools[V], error) {
	raw, isStored, err := s.optionalConfig(ctx, key)
	if err != nil {
		return storedPools[V]{}, err
	}
	pools, ok := parse(raw)
	if !ok {
		s.logger.Warn(unreadable, slog.String("key", key))
		return storedPools[V]{}, nil
	}
	return storedPools[V]{pools: pools, isStored: isStored, readable: true}, nil
}

// poolBounds reads every pool's standing intent and floor. A map that will not
// parse, or that breaks a bound the write path enforces, reads as no pools
// configured, which asks for no machines anywhere.
func (s *Service) poolBounds(ctx context.Context) (storedPools[hostconfig.PoolBounds], error) {
	return readPools(ctx, s, models.ConfigKeyFleetPoolBounds,
		"the stored pool bounds are unreadable; every pool reads as holding nothing",
		hostconfig.ParsePoolBounds)
}

// poolSchedules reads every pool's windows.
//
// The whole map goes, never the readable entries out of a bad one. A partially
// applied schedule set is a policy nobody wrote: it would hold capacity on some
// mornings and not others, look intended on the card, and survive until
// somebody compared it against what they thought they had stored.
func (s *Service) poolSchedules(ctx context.Context) (storedPools[[]hostconfig.FleetSchedule], error) {
	return readPools(ctx, s, models.ConfigKeyFleetPoolSchedules,
		"the stored pool schedules are unreadable; the schedule signal is off",
		hostconfig.ParsePoolSchedules)
}

// poolLadders reads every pool's machine-type preference order. An unreadable
// map is no ladders, which is the catalog default and zone failover alone.
func (s *Service) poolLadders(ctx context.Context) (storedPools[[]string], error) {
	return readPools(ctx, s, models.ConfigKeyFleetPoolLadders,
		"the stored shape ladders are unreadable; provisioning takes the catalog default",
		hostconfig.ParsePoolLadders)
}

// requireEnabledPool refuses a ladder or a window written for a pool that is not
// switched on.
//
// The bounds are what enable a pool, and the policy read carries a ladder or a
// window only for a pool they name, so without this the write would succeed,
// store exactly what it was given, and change nothing an operator could see.
// A success that changes nothing is worse than a refusal that names the order.
//
// The default pool is always on, so it costs no read.
func (s *Service) requireEnabledPool(ctx context.Context, pool string) error {
	if pool == hostconfig.DefaultTier {
		return nil
	}
	bounds, err := s.poolBounds(ctx)
	if err != nil {
		return err
	}
	if !bounds.readable {
		return refuseUnreadable("the stored pool bounds", models.ConfigKeyFleetPoolBounds)
	}
	if _, on := bounds.pools[pool]; !on {
		return &hostconfig.ValidationError{
			Field:   hostconfig.FieldPool,
			Message: fmt.Sprintf("pool %q is not switched on: set its standby and floor first", pool),
		}
	}
	return nil
}

// refuseUnreadable is what a per-pool write returns instead of overlaying a map
// it could not read.
//
// Overlaying one stores the pool the write names and drops every other pool's
// entry, with success reported and nothing anywhere saying a policy was
// deleted. Refusing costs nothing an operator cannot recover: every write here
// encodes through the same JSON, so no endpoint can produce an unreadable map.
// It can only have been stored by hand, and the same hand repairs it by
// replacing the whole key, which is what the refusal names.
func refuseUnreadable(what, key string) error {
	return fmt.Errorf("%s cannot be read, so editing one pool would delete the others: repair %s first", what, key)
}

// UpdateFleetShapeLadder replaces one pool's machine-type preference order and
// leaves every other pool's alone.
//
// Its own key and its own write, so nothing that edits a scalar has to
// remember to carry the ladder along.
//
// Validated against the catalog AND against the pool that will own it, before
// it is stored, and the refusal is what the feature is for: the catalog is the
// only place that knows which shapes can actually run Cuttlefish here, and which
// of them a pool may hold depends on what that pool's machines are, so a ladder
// holding a shape the pair has never admitted would answer a stockout with
// machines that boot and cannot launch a device. The catalog is read once and
// every rung judged against it in memory.
func (s *Service) UpdateFleetShapeLadder(ctx context.Context, pool string, rungs []string) error {
	if err := hostconfig.ValidatePoolName(pool); err != nil {
		return fmt.Errorf("validating the pool: %w", err)
	}
	if err := s.requireEnabledPool(ctx, pool); err != nil {
		return err
	}
	catalog, err := s.ladderCatalog(ctx)
	if err != nil {
		return err
	}
	if err := hostconfig.ValidateShapeLadder(pool, rungs, catalog, s.whyUnoffered(ctx)); err != nil {
		return fmt.Errorf("validating the fleet shape ladder: %w", err)
	}
	// Read to overlay: a write that rebuilt the map from the pool it was editing
	// would clear every other pool's ladder.
	stored, err := s.poolLadders(ctx)
	if err != nil {
		return err
	}
	if !stored.readable {
		return refuseUnreadable("the stored shape ladders", models.ConfigKeyFleetPoolLadders)
	}
	if rungs == nil {
		rungs = []string{}
	}
	encoded, err := hostconfig.PoolLaddersJSON(hostconfig.WithPool(stored.pools, pool, rungs))
	if err != nil {
		return err
	}
	return s.setConfig(ctx, models.ConfigKeyFleetPoolLadders, encoded,
		poolLaddersDescription, "the fleet shape ladder")
}

// ladderCatalog reads every machine type in one list, enabled or not, because
// a rung's refusal has to be able to say "disabled" rather than "unknown".
// The two are different problems with different fixes, and an enabled-only
// read cannot tell them apart.
func (s *Service) ladderCatalog(ctx context.Context) ([]*machinetype.MachineType, error) {
	if s.machineTypes == nil {
		return nil, errors.New("validating a shape ladder requires the machine-type catalog")
	}
	catalog, err := s.machineTypes.List(ctx, machinetype.ListFilter{})
	if err != nil {
		return nil, fmt.Errorf("listing machine types to validate the shape ladder: %w", err)
	}
	return catalog, nil
}

// whyUnoffered lets a refusal state the catalog's own reason for the one rung
// the listing does not hold.
//
// Only that rung costs a lookup, and only on a write that is already failing,
// which is what keeps the one-listing rule above intact. An error that is not
// the catalog's verdict on the shape (a cold catalog, a failed read) answers
// nothing, so the refusal stays the generic one rather than blaming the
// operator's rung for an infrastructure fault.
func (s *Service) whyUnoffered(ctx context.Context) hostconfig.WhyUnoffered {
	return func(machineType string) error {
		if _, err := s.machineTypes.Get(ctx, machineType); err != nil {
			if unoffered, ok := errors.AsType[*machinetype.UnofferedError](err); ok {
				return unoffered
			}
		}
		return nil
	}
}

// UpdateFleetAutoscaler stores the three values an operator moves while watching
// the fleet: the mode, and one pool's two lower bounds.
//
// One write for all three because they are one policy. The two bounds compose
// into a single decision every cycle, and a mode is only meaningful against the
// bounds it is permitted to act on. A split body would let a surface holding a
// stale read of one revert the other while setting its own.
//
// The mode stays a scalar of its own beside the pool map: it is the
// reversibility switch for the whole loop, and a malformed pool map must not be
// able to take it down.
//
// Split from UpdateFleetTuning below for the opposite reason: those two are
// edited from a different surface at a different moment, and a whole-policy PUT
// from either would carry a stale copy of the other's fields.
func (s *Service) UpdateFleetAutoscaler(ctx context.Context, mode hostconfig.AutoscaleMode, pool string, standby, floor int64) error {
	if err := hostconfig.ValidatePoolName(pool); err != nil {
		return fmt.Errorf("validating the pool: %w", err)
	}
	// Read to overlay, for the reason the ladder write does it: the bounds of
	// every pool this write has no opinion about have to survive it.
	stored, err := s.poolBounds(ctx)
	if err != nil {
		return err
	}
	if !stored.readable {
		return refuseUnreadable("the stored pool bounds", models.ConfigKeyFleetPoolBounds)
	}
	encoded, err := hostconfig.PoolBoundsJSON(hostconfig.WithPool(stored.pools, pool,
		hostconfig.PoolBounds{Standby: standby, Floor: floor}))
	if err != nil {
		return err
	}
	return s.setConfigs(ctx, "the autoscaler policy", []spanner.ConfigEntry{
		{
			Key: models.ConfigKeyFleetAutoscaleMode, Value: string(mode),
			Description: "How much of the fleet autoscaler is switched on (off | provision_only | on)",
		},
		{
			Key: models.ConfigKeyFleetPoolBounds, Value: encoded,
			Description: poolBoundsDescription,
		},
	})
}

// DisableFleetPool switches a pool off: its bounds, its windows and its ladder
// all go, in one commit.
//
// Its own verb rather than a bounds write of zeros, because zero bounds are a
// pool an operator is holding empty on purpose and this is a pool they are no
// longer running. Selection refuses a create into it, and its machines drain by
// ordinary reclamation rather than by a teardown of their own.
//
// All three keys, because a ladder or a window left behind would be configuration
// for a pool nothing joins, and re-enabling would silently restore it.
//
// The default pool is refused: it is where every machine naming no pool belongs,
// so switching it off would leave the fleet holding machines against no bounds
// at all.
func (s *Service) DisableFleetPool(ctx context.Context, pool string) error {
	if err := hostconfig.ValidatePoolName(pool); err != nil {
		return fmt.Errorf("validating the pool: %w", err)
	}
	if pool == hostconfig.DefaultTier {
		return &hostconfig.ValidationError{
			Field:   hostconfig.FieldPool,
			Message: "the default pool cannot be switched off: it is where every machine naming no pool belongs",
		}
	}
	bounds, err := s.poolBounds(ctx)
	if err != nil {
		return err
	}
	schedules, err := s.poolSchedules(ctx)
	if err != nil {
		return err
	}
	ladders, err := s.poolLadders(ctx)
	if err != nil {
		return err
	}
	for _, stored := range []struct {
		readable bool
		what     string
		key      string
	}{
		{bounds.readable, "the stored pool bounds", models.ConfigKeyFleetPoolBounds},
		{schedules.readable, "the stored pool schedules", models.ConfigKeyFleetPoolSchedules},
		{ladders.readable, "the stored shape ladders", models.ConfigKeyFleetPoolLadders},
	} {
		if !stored.readable {
			return refuseUnreadable(stored.what, stored.key)
		}
	}
	encodedBounds, err := hostconfig.PoolBoundsJSON(hostconfig.WithoutPool(bounds.pools, pool))
	if err != nil {
		return err
	}
	encodedSchedules, err := hostconfig.PoolSchedulesJSON(hostconfig.WithoutPool(schedules.pools, pool))
	if err != nil {
		return err
	}
	encodedLadders, err := hostconfig.PoolLaddersJSON(hostconfig.WithoutPool(ladders.pools, pool))
	if err != nil {
		return err
	}
	return s.setConfigs(ctx, "the pool", []spanner.ConfigEntry{
		{
			Key: models.ConfigKeyFleetPoolBounds, Value: encodedBounds,
			Description: poolBoundsDescription,
		},
		{
			Key: models.ConfigKeyFleetPoolSchedules, Value: encodedSchedules,
			Description: poolSchedulesDescription,
		},
		{
			Key: models.ConfigKeyFleetPoolLadders, Value: encodedLadders,
			Description: poolLaddersDescription,
		},
	})
}

// The stored descriptions of the three per-pool keys. Named because two writers
// each (the per-pool write and the pool disable) set every one of them, and a
// key whose description depends on which surface last touched it reads as two
// different settings.
const (
	poolBoundsDescription    = "Per pool: the idle hosts the fleet keeper holds free, and the hosts it keeps standing whether or not devices fill them"
	poolSchedulesDescription = "Recurring wall-clock windows that raise a pool's standby target while they are open"
	poolLaddersDescription   = "Machine types each pool's provisioning prefers, in order; provisioning steps down the list on a stockout"
)

// UpdateFleetTuning stores the two rarely-touched values: how far back demand
// is measured and how much lease a shortened host keeps.
func (s *Service) UpdateFleetTuning(ctx context.Context, demandHorizonSec, drainGraceSec int64) error {
	return s.setConfigs(ctx, "the fleet tuning", []spanner.ConfigEntry{
		{
			Key: models.ConfigKeyFleetDemandHorizonSec, Value: strconv.FormatInt(demandHorizonSec, 10),
			Description: "How far back the demand signal looks for placement misses, in seconds (0 disables it)",
		},
		{
			Key: models.ConfigKeyFleetDrainGraceSec, Value: strconv.FormatInt(drainGraceSec, 10),
			Description: "Lease (seconds) a host keeps when it is shortened as surplus",
		},
	})
}

// UpdateFleetSchedules replaces one pool's window list and leaves every other
// pool's alone.
//
// Its own key and its own write, so nothing that edits a horizon has to
// remember to carry the windows along. Validated before encoding: a window that
// cannot be evaluated must be refused at the door rather than stored and then
// silently skipped by OpenAt, which would look identical to having no schedule
// at all.
func (s *Service) UpdateFleetSchedules(ctx context.Context, pool string, schedules []hostconfig.FleetSchedule) error {
	if err := hostconfig.ValidatePoolName(pool); err != nil {
		return fmt.Errorf("validating the pool: %w", err)
	}
	if err := s.requireEnabledPool(ctx, pool); err != nil {
		return err
	}
	if err := hostconfig.ValidateSchedules(schedules); err != nil {
		return fmt.Errorf("validating fleet schedules: %w", err)
	}
	if schedules == nil {
		// An absent list and an empty one mean the same thing, and json.Marshal
		// renders nil as "null", which reads back as no windows but does not
		// look like the empty list an operator just saved.
		schedules = []hostconfig.FleetSchedule{}
	}
	stored, err := s.poolSchedules(ctx)
	if err != nil {
		return err
	}
	if !stored.readable {
		return refuseUnreadable("the stored pool schedules", models.ConfigKeyFleetPoolSchedules)
	}
	encoded, err := hostconfig.PoolSchedulesJSON(hostconfig.WithPool(stored.pools, pool, schedules))
	if err != nil {
		return err
	}
	return s.setConfig(ctx, models.ConfigKeyFleetPoolSchedules, encoded,
		poolSchedulesDescription, "fleet schedules")
}

// GetSweepPolicy returns what each loop is currently allowed to do; every
// missing key reads as the compiled default.
func (s *Service) GetSweepPolicy(ctx context.Context) (hostconfig.SweepPolicy, error) {
	policy := hostconfig.SweepPolicy{}
	for _, field := range []struct {
		key   string
		what  string
		into  *bool
		stock bool
	}{
		{models.ConfigKeyDeleteOrphanVMs, "orphan-VM deletion", &policy.DeleteOrphanVMs, false},
		{models.ConfigKeyOrphanCVDs, "orphan-CVD sweep", &policy.OrphanCVDs, false},
		{models.ConfigKeyZombieRecreate, "zombie recreate", &policy.ZombieRecreate, false},
		{models.ConfigKeyReaperDryRun, "reaper dry run", &policy.ReaperDryRun, false},
	} {
		value, stored, err := s.boolConfig(ctx, field.key, field.stock, field.what)
		if err != nil {
			return hostconfig.SweepPolicy{}, err
		}
		*field.into = value
		policy.IsSet = policy.IsSet || stored
	}
	return policy, nil
}

// UpdateSweepPolicy writes only the switches the patch names.
func (s *Service) UpdateSweepPolicy(ctx context.Context, patch hostconfig.SweepPolicyPatch) error {
	var entries []spanner.ConfigEntry
	for _, field := range []struct {
		key         string
		value       *bool
		description string
	}{
		{
			models.ConfigKeyDeleteOrphanVMs, patch.DeleteOrphanVMs,
			"Let the host reconciler delete managed GCE instances with no host row",
		},
		{
			models.ConfigKeyOrphanCVDs, patch.OrphanCVDs,
			"Let the device controller delete CVD groups with no device row",
		},
		{
			models.ConfigKeyZombieRecreate, patch.ZombieRecreate,
			"Let the device controller recreate a CVD whose guest is gone",
		},
		{
			models.ConfigKeyReaperDryRun, patch.ReaperDryRun,
			"Suspend the reaper's and batch settlement's deletions, logging only",
		},
	} {
		if field.value == nil {
			continue
		}
		entries = append(entries, spanner.ConfigEntry{
			Key: field.key, Value: strconv.FormatBool(*field.value), Description: field.description,
		})
	}
	// A patch naming nothing writes nothing: SetMany takes an empty group as the
	// no-op it is, so this keeps the one-switch-one-key contract intact.
	return s.setConfigs(ctx, "the sweep policy", entries)
}

// optionalConfig reads a key whose absence is a valid state (unseeded
// overrides), mapping not-found to the empty string.
func (s *Service) optionalConfig(ctx context.Context, key string) (string, bool, error) {
	config, err := s.settings.Get(ctx, key)
	if err != nil {
		if errors.Is(err, spanner.ErrKeyNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("getting config %q: %w", key, err)
	}
	return config.AsString(), true, nil
}

// boolConfig reads a bool key whose absence is a valid state, mapping
// not-found to fallback. what names the setting in the error wrap (the wrap
// is served as the 500 detail, so existing texts stay).
func (s *Service) boolConfig(ctx context.Context, key string, fallback bool, what string) (bool, bool, error) {
	config, err := s.settings.Get(ctx, key)
	if err != nil {
		if errors.Is(err, spanner.ErrKeyNotFound) {
			return fallback, false, nil
		}
		return false, false, fmt.Errorf("getting %s: %w", what, err)
	}
	return config.AsBool(), true, nil
}

// setConfig writes one settings key with its stored description, wrapping a
// storage failure as "updating <what>".
func (s *Service) setConfig(ctx context.Context, key, value, description, what string) error {
	if err := s.settings.Set(ctx, key, value, description); err != nil {
		return fmt.Errorf("updating %s: %w", what, err)
	}
	return nil
}

// setConfigs writes a group of settings keys in one commit, wrapping a storage
// failure as "updating <what>".
//
// Every write of more than one key goes through this rather than a sequence of
// setConfig calls. Each of these groups is read back as one fact, and the
// readers above report a partially stored group as not stored at all, so a
// sequence that stops halfway leaves a policy the operator never chose and the console
// offers to reset something other than what it is showing.
func (s *Service) setConfigs(ctx context.Context, what string, entries []spanner.ConfigEntry) error {
	if err := s.settings.SetMany(ctx, entries); err != nil {
		return fmt.Errorf("updating %s: %w", what, err)
	}
	return nil
}
