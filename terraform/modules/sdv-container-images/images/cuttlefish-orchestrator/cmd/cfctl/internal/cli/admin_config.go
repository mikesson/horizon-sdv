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

package cli

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// AdminConfigCmd reads and writes every platform settings key through one get
// and one set command.
type AdminConfigCmd struct {
	Get AdminConfigGetCmd `cmd:"" help:"Read a platform setting, or all of them when KEY is omitted"`
	Set AdminConfigSetCmd `cmd:"" help:"Write a platform setting"`
}

// configField is one printed field of a setting's value.
type configField struct {
	name  string
	value string
}

// scalarArg is the assignment name of a scalar setting's bare VALUE.
const scalarArg = "value"

// configSetting describes how to read and write one settings key. The any
// return is the wire body printed under --json, and the fields are the human
// rendering.
type configSetting struct {
	key string
	doc string
	// fields lists the settable FIELD=VALUE names. Nil marks a scalar that
	// takes one bare VALUE.
	fields []string
	// poolScoped lets a field be addressed as POOL.FIELD. The unprefixed form
	// addresses the default pool.
	poolScoped bool
	get        func(context.Context, *cco.Client) (any, []configField, error)
	// set receives the parsed assignments, with a scalar under scalarArg.
	// Composite settings overlay them onto the current value because the PUT
	// replaces every field, and send the read's ETag as If-Match so an edit
	// made in between fails instead of being overwritten.
	set func(context.Context, *cco.Client, map[string]string) (any, []configField, error)
}

// scalarSetting builds a one-value setting around typed accessors.
func scalarSetting[T any](key, doc string,
	read func(*cco.Client, context.Context) (T, error),
	write func(*cco.Client, context.Context, T) (T, error),
	parse func(string) (T, error),
) configSetting {
	wrap := func(v T) (any, []configField) {
		return map[string]any{"value": v}, []configField{{scalarArg, fmt.Sprintf("%v", v)}}
	}
	return configSetting{
		key: key, doc: doc,
		get: func(ctx context.Context, client *cco.Client) (any, []configField, error) {
			v, err := read(client, ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to read %s: %w", key, err)
			}
			body, fields := wrap(v)
			return body, fields, nil
		},
		set: func(ctx context.Context, client *cco.Client, args map[string]string) (any, []configField, error) {
			parsed, err := parse(args[scalarArg])
			if err != nil {
				return nil, nil, err
			}
			v, err := write(client, ctx, parsed)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to set %s: %w", key, err)
			}
			body, fields := wrap(v)
			return body, fields, nil
		},
	}
}

// parseString accepts any value. An empty value clears the setting.
func parseString(s string) (string, error) { return s, nil }

// parseInt64 rejects non-integers with the setting's name in the message.
func parseInt64(key string) func(string) (int64, error) {
	return func(s string) (int64, error) {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s takes an integer, got %q", key, s)
		}
		return v, nil
	}
}

// parseBool rejects non-booleans with the setting's name in the message.
func parseBool(key string) func(string) (bool, error) {
	return func(s string) (bool, error) {
		v, err := strconv.ParseBool(s)
		if err != nil {
			return false, fmt.Errorf("%s takes true or false, got %q", key, s)
		}
		return v, nil
	}
}

// configSettings lists the settings in display order.
func configSettings() []configSetting {
	return []configSetting{
		scalarSetting("zone-pin",
			"Single zone placement is restricted to (empty = ranked across the enabled catalog)",
			(*cco.Client).GetZonePin, (*cco.Client).UpdateZonePin, parseString),
		scalarSetting("quota-margin",
			"Percent of each regional quota limit zone ranking keeps in reserve",
			(*cco.Client).GetQuotaMargin, (*cco.Client).UpdateQuotaMargin, parseInt64("quota-margin")),
		scalarSetting("v1-zone-hint",
			"Read the legacy /v1 zone token as a placement hint (true) or a literal pin (false)",
			(*cco.Client).GetV1ZoneHint, (*cco.Client).UpdateV1ZoneHint, parseBool("v1-zone-hint")),
		scalarSetting("spot-auto-default",
			"Resolve auto-capacity headless intent to Spot hosts",
			(*cco.Client).GetSpotAutoDefault, (*cco.Client).UpdateSpotAutoDefault, parseBool("spot-auto-default")),
		scalarSetting("default-gpu-machine-type",
			"Machine-type override for auto-provisioned GPU hosts (empty = catalog fallback)",
			(*cco.Client).GetDefaultGPUMachineType, (*cco.Client).UpdateDefaultGPUMachineType, parseString),
		upstreamBuildSetting(),
		spotMinUptimeSetting(),
		lifetimesSetting(),
		dockerHostSetting(),
		artifactBucketsSetting(),
		sweepsSetting(),
		fleetSetting(),
	}
}

// The lifetimes FIELD names.
const (
	fieldDeviceTTLHours = "device_ttl_hours"
	fieldExtendHours    = "extend_hours"
	fieldExtendCount    = "extend_count"
	fieldHostTTLHours   = "host_ttl_hours"
)

// lifetimesSetting is the default lifetime of device sessions and host
// leases. Only the four defaults are settable; the caps are read-only.
func lifetimesSetting() configSetting {
	return configSetting{
		key:    "lifetimes",
		doc:    "Default device session TTL, extension grant and allowance, and host lease (hours)",
		fields: []string{fieldDeviceTTLHours, fieldExtendHours, fieldExtendCount, fieldHostTTLHours},
		get: func(ctx context.Context, client *cco.Client) (any, []configField, error) {
			v, _, err := client.GetLifetimes(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to read lifetimes: %w", err)
			}
			return v, lifetimesFields(v), nil
		},
		set: func(ctx context.Context, client *cco.Client, args map[string]string) (any, []configField, error) {
			cur, etag, err := client.GetLifetimes(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to read lifetimes before update: %w", err)
			}
			req := cco.UpdateLifetimesRequest{
				DeviceTTLHours: cur.DeviceTTLHours,
				ExtendHours:    cur.ExtendHours,
				ExtendCount:    cur.ExtendCount,
				HostTTLHours:   cur.HostTTLHours,
			}
			parse := parseInt64("lifetimes")
			for name, into := range map[string]*int64{
				fieldDeviceTTLHours: &req.DeviceTTLHours,
				fieldExtendHours:    &req.ExtendHours,
				fieldExtendCount:    &req.ExtendCount,
				fieldHostTTLHours:   &req.HostTTLHours,
			} {
				if v, ok := args[name]; ok {
					if *into, err = parse(v); err != nil {
						return nil, nil, err
					}
				}
			}
			stored, _, err := client.UpdateLifetimes(ctx, etag, req)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to set lifetimes: %w", err)
			}
			return stored, lifetimesFields(stored), nil
		},
	}
}

// lifetimesFields renders the settable defaults, then the server's compiled
// caps.
func lifetimesFields(v *cco.Lifetimes) []configField {
	return []configField{
		{fieldDeviceTTLHours, strconv.FormatInt(v.DeviceTTLHours, 10)},
		{fieldExtendHours, strconv.FormatInt(v.ExtendHours, 10)},
		{fieldExtendCount, strconv.FormatInt(v.ExtendCount, 10)},
		{fieldHostTTLHours, strconv.FormatInt(v.HostTTLHours, 10)},
		{"max_device_ttl_hours", strconv.FormatInt(v.MaxDeviceTTLHours, 10)},
		{"max_extend_hours", strconv.FormatInt(v.MaxExtendHours, 10)},
		{"max_extend_count", strconv.FormatInt(v.MaxExtendCount, 10)},
		{"max_host_ttl_hours", strconv.FormatInt(v.MaxHostTTLHours, 10)},
		{"max_devices_per_host", strconv.FormatInt(v.MaxDevicesPerHost, 10)},
		{"grace_period_seconds", strconv.FormatInt(v.GracePeriodSeconds, 10)},
	}
}

// dockerHostSetting is the GCE machine type and TTL that a legacy /v1
// docker-only host create provisions. The TTL cap is read-only.
func dockerHostSetting() configSetting {
	return configSetting{
		key:    "docker-host",
		doc:    "GCE machine type and lease (hours) a legacy docker-shape host create provisions",
		fields: []string{"machine_type", "ttl_hours"},
		get: func(ctx context.Context, client *cco.Client) (any, []configField, error) {
			v, _, err := client.GetDockerHost(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to read docker-host: %w", err)
			}
			return v, dockerHostFields(v), nil
		},
		set: func(ctx context.Context, client *cco.Client, args map[string]string) (any, []configField, error) {
			cur, etag, err := client.GetDockerHost(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to read docker-host before update: %w", err)
			}
			req := cco.UpdateDockerHostRequest{MachineType: cur.MachineType, TTLHours: cur.TTLHours}
			if v, ok := args["machine_type"]; ok {
				req.MachineType = v
			}
			if v, ok := args["ttl_hours"]; ok {
				if req.TTLHours, err = parseInt64("ttl_hours")(v); err != nil {
					return nil, nil, err
				}
			}
			stored, _, err := client.UpdateDockerHost(ctx, etag, req)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to set docker-host: %w", err)
			}
			return stored, dockerHostFields(stored), nil
		},
	}
}

// dockerHostFields renders the settable values, then the server's compiled
// cap.
func dockerHostFields(v *cco.DockerHost) []configField {
	return []configField{
		{"machine_type", v.MachineType},
		{"ttl_hours", strconv.FormatInt(v.TTLHours, 10)},
		{"max_ttl_hours", strconv.FormatInt(v.MaxTTLHours, 10)},
	}
}

// The fleet FIELD names.
const (
	fieldMode             = "mode"
	fieldStandby          = "standby"
	fieldFloor            = "floor"
	fieldDemandHorizonSec = "demand_horizon_sec"
	fieldDrainGraceSec    = "drain_grace_sec"
	fieldSchedules        = "schedules"
	fieldLadder           = "ladder"
	fieldEnabled          = "enabled"
)

// valueNone clears a list setting, and an empty list prints as it.
const valueNone = "none"

// fleetSetting is the autoscaler policy, one CLI key over five server
// endpoints. Pool fields are written as POOL.FIELD, for example
// `gpu.standby=1`, and the unprefixed form addresses the default pool.
func fleetSetting() configSetting {
	return configSetting{
		key: "fleet",
		doc: "Autoscaler mode, demand horizon and drain grace for the fleet; per pool the hosts kept free, the hosts kept running, the schedule windows and the machine shapes provisioning falls back through",
		fields: []string{
			fieldMode, fieldStandby, fieldFloor, fieldDemandHorizonSec,
			fieldDrainGraceSec, fieldSchedules, fieldLadder, fieldEnabled,
		},
		poolScoped: true,
		get: func(ctx context.Context, client *cco.Client) (any, []configField, error) {
			v, _, err := client.GetFleetPolicy(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to read fleet: %w", err)
			}
			return v, fleetFields(v), nil
		},
		set: func(ctx context.Context, client *cco.Client, args map[string]string) (any, []configField, error) {
			cur, etag, err := client.GetFleetPolicy(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to read fleet before update: %w", err)
			}
			stored, err := setFleetFields(ctx, &fleetWriter{client: client, etag: etag}, cur, args)
			if err != nil {
				return nil, nil, err
			}
			return stored, fleetFields(stored), nil
		},
	}
}

// fleetWriter issues the fleet PUTs of one set command. Each write is
// conditional on the tag the previous one returned, so a concurrent edit
// between the read and any write fails that write instead of being
// overwritten. It records what was applied, so a failure part way can say
// which writes already took effect.
type fleetWriter struct {
	client  *cco.Client
	etag    string
	applied []string
}

// write issues one PUT through put and chains its tag into the next.
func (w *fleetWriter) write(what string,
	put func(ifMatch string) (*cco.FleetPolicy, string, error),
) (*cco.FleetPolicy, error) {
	stored, etag, err := put(w.etag)
	if err != nil {
		if len(w.applied) > 0 {
			return nil, fmt.Errorf("failed to set %s: %w (already applied: %s)",
				what, err, strings.Join(w.applied, "; "))
		}
		return nil, fmt.Errorf("failed to set %s: %w", what, err)
	}
	w.etag = etag
	w.applied = append(w.applied, what)
	return stored, nil
}

// setFleetFields overlays the assignments onto the current policy and issues
// only the PUTs whose fields were named. Pools are written one at a time in
// name order.
func setFleetFields(ctx context.Context, w *fleetWriter, cur *cco.FleetPolicy, args map[string]string) (*cco.FleetPolicy, error) {
	fleetArgs, poolArgs := splitPoolArgs(args)
	stored, err := setFleetTuning(ctx, w, cur, fleetArgs)
	if err != nil {
		return nil, err
	}
	carried := false
	for _, pool := range slices.Sorted(maps.Keys(poolArgs)) {
		var wrote bool
		if stored, wrote, err = setFleetPool(ctx, w, stored, pool, poolArgs[pool], fleetArgs); err != nil {
			return nil, err
		}
		carried = carried || wrote
	}
	// Only the autoscaler PUT carries the mode, and switching a pool off issues
	// none. Send the mode with the default pool's current bounds.
	if _, named := fleetArgs[fieldMode]; named && !carried {
		stored, _, err = setFleetAutoscaler(ctx, w, stored, defaultFleetTier, nil, fleetArgs)
		return stored, err
	}
	return stored, nil
}

// splitPoolArgs separates the fleet-wide assignments from the pool-scoped ones,
// keyed by pool. parseConfigAssignments has already refused two spellings of
// one pool field, so no entries collide here.
func splitPoolArgs(args map[string]string) (map[string]string, map[string]map[string]string) {
	fleetArgs := make(map[string]string, len(args))
	poolArgs := make(map[string]map[string]string)
	for name, value := range args {
		pool, field, prefixed := strings.Cut(name, ".")
		if !prefixed {
			pool, field = defaultFleetTier, name
		}
		if !slices.Contains(poolFields, field) {
			fleetArgs[field] = value
			continue
		}
		if poolArgs[pool] == nil {
			poolArgs[pool] = map[string]string{}
		}
		poolArgs[pool][field] = value
	}
	return fleetArgs, poolArgs
}

// poolFields are the assignments that belong to a pool.
var poolFields = []string{fieldStandby, fieldFloor, fieldSchedules, fieldLadder, fieldEnabled}

// setFleetPool switches one pool off, or writes its bounds, windows and
// ladder. The second result reports whether it issued the autoscaler PUT.
func setFleetPool(ctx context.Context, w *fleetWriter, cur *cco.FleetPolicy,
	pool string, args, fleetArgs map[string]string,
) (*cco.FleetPolicy, bool, error) {
	off, err := switchingOff(pool, args)
	if err != nil {
		return nil, false, err
	}
	if off {
		stored, err := w.write("pool "+pool+" switched off", func(ifMatch string) (*cco.FleetPolicy, string, error) {
			return w.client.DisableFleetPool(ctx, ifMatch, pool)
		})
		if err != nil {
			return nil, false, err
		}
		return stored, false, nil
	}
	stored, wrote, err := setFleetAutoscaler(ctx, w, cur, pool, args, fleetArgs)
	if err != nil {
		return nil, false, err
	}
	if stored, err = clearFleetSchedules(ctx, w, stored, pool, args); err != nil {
		return nil, false, err
	}
	stored, err = setFleetLadder(ctx, w, stored, pool, args)
	return stored, wrote, err
}

// switchingOff reports whether the assignments set enabled=false. That value
// deletes the pool, so it is refused alongside any other field for the pool.
func switchingOff(pool string, args map[string]string) (bool, error) {
	raw, named := args[fieldEnabled]
	if !named {
		return false, nil
	}
	on, err := parseBool(fieldEnabled)(raw)
	if err != nil {
		return false, err
	}
	if on {
		return false, nil
	}
	if len(args) > 1 {
		return false, fmt.Errorf("%s.%s=false switches the pool off; drop the other %s assignments",
			pool, fieldEnabled, pool)
	}
	return true, nil
}

// clearFleetSchedules clears a pool's schedule windows. Windows are authored
// in the console; the CLI only clears them, as a recovery path.
func clearFleetSchedules(ctx context.Context, w *fleetWriter, cur *cco.FleetPolicy, pool string, args map[string]string) (*cco.FleetPolicy, error) {
	raw, ok := args[fieldSchedules]
	if !ok {
		return cur, nil
	}
	switch strings.TrimSpace(raw) {
	case "", valueNone, "[]":
		return w.write("pool "+pool+" schedules cleared", func(ifMatch string) (*cco.FleetPolicy, string, error) {
			return w.client.UpdateFleetSchedules(ctx, ifMatch, pool, nil)
		})
	default:
		return nil, fmt.Errorf(
			"%s takes only an empty value, %q or \"[]\" to clear every window; "+
				"windows are authored on the console's Settings page", fieldSchedules, valueNone)
	}
}

// setFleetAutoscaler issues the autoscaler PUT for one pool when any of its
// fields was named, and reports whether it wrote. The PUT replaces mode and
// both bounds, so the request starts from the current values. enabled=true
// switches a pool on by storing its current bounds.
func setFleetAutoscaler(ctx context.Context, w *fleetWriter, cur *cco.FleetPolicy,
	pool string, args, fleetArgs map[string]string,
) (*cco.FleetPolicy, bool, error) {
	_, setMode := fleetArgs[fieldMode]
	_, setStandby := args[fieldStandby]
	_, setFloor := args[fieldFloor]
	_, switchOn := args[fieldEnabled]
	if !setMode && !setStandby && !setFloor && !switchOn {
		return cur, false, nil
	}
	tier := cur.Tiers[pool]
	req := cco.UpdateFleetAutoscalerRequest{
		Mode: string(cur.Mode), Pool: pool, Standby: tier.Standby, Floor: tier.Floor,
	}
	if v, ok := fleetArgs[fieldMode]; ok {
		req.Mode = v
	}
	for _, field := range []struct {
		name string
		into *int64
	}{{fieldStandby, &req.Standby}, {fieldFloor, &req.Floor}} {
		v, ok := args[field.name]
		if !ok {
			continue
		}
		parsed, err := parseInt64(field.name)(v)
		if err != nil {
			return nil, false, err
		}
		*field.into = parsed
	}
	stored, err := w.write("pool "+pool+" autoscaler", func(ifMatch string) (*cco.FleetPolicy, string, error) {
		return w.client.UpdateFleetAutoscaler(ctx, ifMatch, req)
	})
	if err != nil {
		return nil, false, err
	}
	return stored, true, nil
}

// setFleetTuning issues the tuning PUT when the operator named either of its
// fields, and returns cur untouched otherwise.
func setFleetTuning(ctx context.Context, w *fleetWriter, cur *cco.FleetPolicy, args map[string]string) (*cco.FleetPolicy, error) {
	_, setHorizon := args[fieldDemandHorizonSec]
	_, setGrace := args[fieldDrainGraceSec]
	if !setHorizon && !setGrace {
		return cur, nil
	}
	req := cco.UpdateFleetTuningRequest{
		DemandHorizonSec: cur.DemandHorizonSec,
		DrainGraceSec:    cur.DrainGraceSec,
	}
	for name, into := range map[string]*int64{
		fieldDemandHorizonSec: &req.DemandHorizonSec,
		fieldDrainGraceSec:    &req.DrainGraceSec,
	} {
		if v, ok := args[name]; ok {
			parsed, err := parseInt64(name)(v)
			if err != nil {
				return nil, err
			}
			*into = parsed
		}
	}
	return w.write("fleet tuning", func(ifMatch string) (*cco.FleetPolicy, string, error) {
		return w.client.UpdateFleetTuning(ctx, ifMatch, req)
	})
}

// setFleetLadder issues the ladder PUT when the field was named. The server
// validates the machine types, so they are sent as typed.
func setFleetLadder(ctx context.Context, w *fleetWriter, cur *cco.FleetPolicy, pool string, args map[string]string) (*cco.FleetPolicy, error) {
	raw, ok := args[fieldLadder]
	if !ok {
		return cur, nil
	}
	rungs, err := parseLadder(raw)
	if err != nil {
		return nil, err
	}
	return w.write("pool "+pool+" ladder", func(ifMatch string) (*cco.FleetPolicy, string, error) {
		return w.client.UpdateFleetLadder(ctx, ifMatch, pool, rungs)
	})
}

// parseLadder splits a comma-separated list of machine types, keeping the
// order. An empty value or valueNone clears the ladder to the catalog default.
// An empty entry is refused here because the commas are CLI syntax.
func parseLadder(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == valueNone {
		return nil, nil
	}
	rungs := strings.Split(trimmed, ",")
	for i, rung := range rungs {
		rungs[i] = strings.TrimSpace(rung)
		if rungs[i] == "" {
			return nil, fmt.Errorf(
				"%s takes machine types separated by commas, most preferred first, got %q",
				fieldLadder, raw)
		}
	}
	return rungs, nil
}

// defaultFleetTier mirrors hostconfig.DefaultTier. The generated model does
// not name the map keys.
const defaultFleetTier = "default"

// fleetPools lists the pools to render, the default first and the rest in
// name order.
func fleetPools(v *cco.FleetPolicy) []string {
	rest := make([]string, 0, len(v.Tiers))
	for name := range v.Tiers {
		if name != defaultFleetTier {
			rest = append(rest, name)
		}
	}
	slices.Sort(rest)
	return append([]string{defaultFleetTier}, rest...)
}

// fleetFields renders the fleet-wide policy, each pool's fields, then the
// server's compiled bounds. Field names use the same POOL.FIELD syntax that
// set takes.
func fleetFields(v *cco.FleetPolicy) []configField {
	fields := []configField{
		{fieldMode, string(v.Mode)},
		{fieldDemandHorizonSec, strconv.FormatInt(v.DemandHorizonSec, 10)},
		{fieldDrainGraceSec, strconv.FormatInt(v.DrainGraceSec, 10)},
	}
	for _, pool := range fleetPools(v) {
		fields = append(fields, fleetPoolFields(pool, v.Tiers[pool])...)
	}
	return append(fields,
		configField{"max_standby", strconv.FormatInt(v.MaxStandby, 10)},
		configField{"max_floor", strconv.FormatInt(v.MaxFloor, 10)},
		configField{"max_demand_horizon_sec", strconv.FormatInt(v.MaxDemandHorizonSec, 10)},
		configField{"min_drain_grace_sec", strconv.FormatInt(v.MinDrainGraceSec, 10)},
		configField{"max_drain_grace_sec", strconv.FormatInt(v.MaxDrainGraceSec, 10)},
		configField{"max_schedules", strconv.FormatInt(v.MaxSchedules, 10)},
		configField{"max_ladder_rungs", strconv.FormatInt(v.MaxLadderRungs, 10)},
	)
}

// fleetPoolFields renders one pool's four settable values.
func fleetPoolFields(pool string, tier cco.FleetTier) []configField {
	name := func(field string) string {
		if pool == defaultFleetTier {
			return field
		}
		return pool + "." + field
	}
	fields := []configField{
		{name(fieldStandby), strconv.FormatInt(tier.Standby, 10)},
		{name(fieldFloor), strconv.FormatInt(tier.Floor, 10)},
	}
	if len(tier.Schedules) == 0 {
		fields = append(fields, configField{name(fieldSchedules), valueNone})
	}
	for i, w := range tier.Schedules {
		fields = append(fields, configField{
			fmt.Sprintf("%s[%d]", name(fieldSchedules), i), scheduleSummary(w),
		})
	}
	// The ladder prints in the comma-separated form that set takes.
	ladder := valueNone
	if len(tier.Ladder) > 0 {
		ladder = strings.Join(tier.Ladder, ",")
	}
	return append(fields, configField{name(fieldLadder), ladder})
}

// scheduleSummary renders one schedule window on one line.
func scheduleSummary(w cco.FleetSchedule) string {
	return fmt.Sprintf("%s: %d host(s) at %s for %s on %s (%s)",
		w.Name, w.Hosts, w.Start,
		(time.Duration(w.DurationSec) * time.Second).String(),
		scheduleDays(w.Days), w.Location)
}

// scheduleDays renders the weekday set. An empty set means every day.
func scheduleDays(days []int64) string {
	if len(days) == 0 {
		return "every day"
	}
	names := make([]string, 0, len(days))
	for _, d := range days {
		if d < int64(time.Sunday) || d > int64(time.Saturday) {
			// The API cannot store this value. Print it so it stays visible.
			names = append(names, strconv.FormatInt(d, 10))
			continue
		}
		names = append(names, time.Weekday(d).String()[:3])
	}
	return strings.Join(names, ",")
}

func upstreamBuildSetting() configSetting {
	return configSetting{
		key:    "default-upstream-build",
		doc:    "AOSP branch/target used when a declarative create finds no registered builds",
		fields: []string{"branch", "target"},
		get: func(ctx context.Context, client *cco.Client) (any, []configField, error) {
			v, _, err := client.GetDefaultUpstreamBuild(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to read default-upstream-build: %w", err)
			}
			return v, upstreamBuildFields(v), nil
		},
		set: func(ctx context.Context, client *cco.Client, args map[string]string) (any, []configField, error) {
			cur, etag, err := client.GetDefaultUpstreamBuild(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to read default-upstream-build before update: %w", err)
			}
			if v, ok := args["branch"]; ok {
				cur.Branch = v
			}
			if v, ok := args["target"]; ok {
				cur.Target = v
			}
			stored, _, err := client.UpdateDefaultUpstreamBuild(ctx, etag, *cur)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to set default-upstream-build: %w", err)
			}
			return stored, upstreamBuildFields(stored), nil
		},
	}
}

func spotMinUptimeSetting() configSetting {
	return configSetting{
		key:    "spot-min-uptime",
		doc:    "Minimum estimated-uptime tiers Spot placement demands (0, 60, 600 or 3600 seconds)",
		fields: []string{"single_seconds", "batch_seconds"},
		get: func(ctx context.Context, client *cco.Client) (any, []configField, error) {
			v, _, err := client.GetSpotMinUptime(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to read spot-min-uptime: %w", err)
			}
			return v, spotMinUptimeFields(v), nil
		},
		set: func(ctx context.Context, client *cco.Client, args map[string]string) (any, []configField, error) {
			cur, etag, err := client.GetSpotMinUptime(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to read spot-min-uptime before update: %w", err)
			}
			parse := parseInt64("spot-min-uptime")
			if v, ok := args["single_seconds"]; ok {
				if cur.SingleSeconds, err = parse(v); err != nil {
					return nil, nil, err
				}
			}
			if v, ok := args["batch_seconds"]; ok {
				if cur.BatchSeconds, err = parse(v); err != nil {
					return nil, nil, err
				}
			}
			stored, _, err := client.UpdateSpotMinUptime(ctx, etag, *cur)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to set spot-min-uptime: %w", err)
			}
			return stored, spotMinUptimeFields(stored), nil
		},
	}
}

func artifactBucketsSetting() configSetting {
	return configSetting{
		key:    "artifact-buckets",
		doc:    "Per-type artifact bucket overrides and the capture-on-error toggle",
		fields: []string{"bugreport_bucket", "recording_bucket", "capture_on_error"},
		get: func(ctx context.Context, client *cco.Client) (any, []configField, error) {
			v, _, err := client.GetArtifactBuckets(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to read artifact-buckets: %w", err)
			}
			return v, artifactBucketsFields(v), nil
		},
		set: func(ctx context.Context, client *cco.Client, args map[string]string) (any, []configField, error) {
			cur, etag, err := client.GetArtifactBuckets(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to read artifact-buckets before update: %w", err)
			}
			req := cco.UpdateArtifactBucketsRequest{
				BugreportBucket: cur.BugreportBucket,
				RecordingBucket: cur.RecordingBucket,
				CaptureOnError:  cur.CaptureOnError,
			}
			if v, ok := args["bugreport_bucket"]; ok {
				req.BugreportBucket = v
			}
			if v, ok := args["recording_bucket"]; ok {
				req.RecordingBucket = v
			}
			if v, ok := args["capture_on_error"]; ok {
				if req.CaptureOnError, err = parseBool("capture_on_error")(v); err != nil {
					return nil, nil, err
				}
			}
			stored, _, err := client.UpdateArtifactBuckets(ctx, etag, req)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to set artifact-buckets: %w", err)
			}
			return stored, artifactBucketsFields(stored), nil
		},
	}
}

// The sweep switch names.
const (
	fieldDeleteOrphanVMs = "delete_orphan_vms"
	fieldOrphanCVDs      = "orphan_cvds"
	fieldZombieRecreate  = "zombie_recreate"
	fieldReaperDryRun    = "reaper_dry_run"
)

// sweepsSetting holds what each background loop may do unattended. Its PUT is
// partial, so set sends only the named fields.
func sweepsSetting() configSetting {
	return configSetting{
		key:    "sweeps",
		doc:    "What the background loops may do on their own (destructive sweeps, reaper dry run)",
		fields: []string{fieldDeleteOrphanVMs, fieldOrphanCVDs, fieldZombieRecreate, fieldReaperDryRun},
		get: func(ctx context.Context, client *cco.Client) (any, []configField, error) {
			v, _, err := client.GetSweepPolicy(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to read sweeps: %w", err)
			}
			return v, sweepFields(v), nil
		},
		set: func(ctx context.Context, client *cco.Client, args map[string]string) (any, []configField, error) {
			var req cco.UpdateSweepPolicyRequest
			for name, into := range map[string]**bool{
				fieldDeleteOrphanVMs: &req.DeleteOrphanVMs,
				fieldOrphanCVDs:      &req.OrphanCVDs,
				fieldZombieRecreate:  &req.ZombieRecreate,
				fieldReaperDryRun:    &req.ReaperDryRun,
			} {
				raw, ok := args[name]
				if !ok {
					continue
				}
				parsed, err := parseBool(name)(raw)
				if err != nil {
					return nil, nil, err
				}
				*into = &parsed
			}
			// The PUT changes only the named switches, so there is nothing to overlay
			// and nothing a concurrent write could lose.
			stored, _, err := client.UpdateSweepPolicy(ctx, "", req)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to set sweeps: %w", err)
			}
			return stored, sweepFields(stored), nil
		},
	}
}

func sweepFields(v *cco.SweepPolicy) []configField {
	return []configField{
		{fieldDeleteOrphanVMs, strconv.FormatBool(v.DeleteOrphanVms)},
		{fieldOrphanCVDs, strconv.FormatBool(v.OrphanCvds)},
		{fieldZombieRecreate, strconv.FormatBool(v.ZombieRecreate)},
		{fieldReaperDryRun, strconv.FormatBool(v.ReaperDryRun)},
	}
}

func upstreamBuildFields(v *cco.UpstreamBuild) []configField {
	return []configField{{"branch", v.Branch}, {"target", v.Target}}
}

func spotMinUptimeFields(v *cco.SpotMinUptime) []configField {
	return []configField{
		{"single_seconds", strconv.FormatInt(v.SingleSeconds, 10)},
		{"batch_seconds", strconv.FormatInt(v.BatchSeconds, 10)},
	}
}

func artifactBucketsFields(v *cco.ArtifactBuckets) []configField {
	return []configField{
		{"bugreport_bucket", v.BugreportBucket},
		{"recording_bucket", v.RecordingBucket},
		{"default_bucket", v.DefaultBucket},
		{"capture_on_error", strconv.FormatBool(v.CaptureOnError)},
	}
}

// lookupConfigSetting resolves a key or errors with the full key list.
func lookupConfigSetting(key string) (configSetting, error) {
	settings := configSettings()
	i := slices.IndexFunc(settings, func(s configSetting) bool { return s.key == key })
	if i < 0 {
		keys := make([]string, len(settings))
		for j, s := range settings {
			keys[j] = s.key
		}
		return configSetting{}, fmt.Errorf("unknown setting %q; keys: %s", key, strings.Join(keys, ", "))
	}
	return settings[i], nil
}

// joinFields renders a setting's fields for the one-line-per-key table.
func joinFields(s configSetting, fields []configField) string {
	if s.fields == nil && len(fields) > 0 {
		return fields[0].value
	}
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = f.name + "=" + f.value
	}
	return strings.Join(parts, " ")
}

// AdminConfigGetCmd implements 'cfctl admin config get'.
type AdminConfigGetCmd struct {
	Key string `arg:"" optional:"" placeholder:"KEY" help:"Setting key; omit to read every setting"`
}

// Run reads one setting, or all of them.
func (c *AdminConfigGetCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if c.Key != "" {
		s, err := lookupConfigSetting(c.Key)
		if err != nil {
			return err
		}
		body, fields, err := s.get(ctx, client)
		if err != nil {
			return err
		}
		if cli.JSON {
			return printJSON(body)
		}
		output.Info("%s", s.doc)
		for _, f := range fields {
			fmt.Printf("%-18s %s\n", f.name+":", f.value)
		}
		return nil
	}

	settings := configSettings()
	bodies := make(map[string]any, len(settings))
	lines := make([]string, len(settings))
	for i, s := range settings {
		body, fields, err := s.get(ctx, client)
		if err != nil {
			return err
		}
		bodies[s.key] = body
		lines[i] = s.key + "\t" + joinFields(s, fields)
	}
	if cli.JSON {
		return printJSON(bodies)
	}
	return table("KEY\tVALUE", func(w *tabwriter.Writer) {
		for _, line := range lines {
			fmt.Fprintln(w, line)
		}
	})
}

// AdminConfigSetCmd implements 'cfctl admin config set'.
type AdminConfigSetCmd struct {
	Key    string   `arg:"" placeholder:"KEY" help:"Setting key (see 'admin config get')"`
	Values []string `arg:"" placeholder:"VALUE" help:"Scalar settings take one VALUE (empty clears where the setting allows it); composite settings take FIELD=VALUE pairs, and unnamed fields keep their current value"`
}

// Run writes one setting and prints the stored result.
func (c *AdminConfigSetCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	s, err := lookupConfigSetting(c.Key)
	if err != nil {
		return err
	}
	args, err := parseConfigAssignments(s, c.Values)
	if err != nil {
		return err
	}
	body, fields, err := s.set(ctx, client, args)
	if err != nil {
		return err
	}
	if cli.JSON {
		return printJSON(body)
	}
	output.Success("Updated %s", s.key)
	for _, f := range fields {
		fmt.Printf("%-18s %s\n", f.name+":", f.value)
	}
	return nil
}

// parseConfigAssignments validates the positional values against the
// setting's grammar.
func parseConfigAssignments(s configSetting, values []string) (map[string]string, error) {
	args := make(map[string]string, len(values))
	if s.fields == nil {
		if len(values) != 1 {
			return nil, fmt.Errorf("%s takes exactly one value", s.key)
		}
		args[scalarArg] = values[0]
		return args, nil
	}
	// Duplicates are judged on the resolved field. `standby=2` and
	// `default.standby=3` name the same field.
	seen := make(map[string]string, len(values))
	for _, v := range values {
		name, val, ok := strings.Cut(v, "=")
		if !ok {
			return nil, fmt.Errorf("%s takes FIELD=VALUE pairs (fields: %s)", s.key, strings.Join(s.fields, ", "))
		}
		if !slices.Contains(s.fields, fieldName(s, name)) {
			return nil, fmt.Errorf("unknown field %q for %s (fields: %s)", name, s.key, strings.Join(s.fields, ", "))
		}
		key := assignmentKey(s, name)
		if prior, dup := seen[key]; dup {
			if prior == name {
				return nil, fmt.Errorf("field %q given twice", name)
			}
			return nil, fmt.Errorf("%q and %q name the same field; assign it once", prior, name)
		}
		seen[key] = name
		args[name] = val
	}
	return args, nil
}

// fieldName strips a pool prefix from a pool-scoped field. A prefix on a
// fleet-wide field such as `gpu.mode` is kept, so validation refuses it. The
// server validates pool names.
func fieldName(s configSetting, assignment string) string {
	if _, field, ok := poolScoped(s, assignment); ok {
		return field
	}
	return assignment
}

// assignmentKey returns the key duplicates are judged on. Both spellings of a
// default-pool field map to one key.
func assignmentKey(s configSetting, assignment string) string {
	if pool, field, ok := poolScoped(s, assignment); ok {
		return pool + "." + field
	}
	return assignment
}

// poolScoped resolves an assignment to its pool and field. It reports false
// for a fleet-wide field.
func poolScoped(s configSetting, assignment string) (string, string, bool) {
	if !s.poolScoped {
		return "", "", false
	}
	if p, f, prefixed := strings.Cut(assignment, "."); prefixed {
		if slices.Contains(poolFields, f) {
			return p, f, true
		}
		return "", "", false
	}
	if slices.Contains(poolFields, assignment) {
		return defaultFleetTier, assignment, true
	}
	return "", "", false
}
