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
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// scalarPair is one single-value GET/PUT settings pair sharing a path: the
// operation metadata plus the typed glue between the wire types and the
// service accessors. The per-operation Input/Output types stay hand-written
// (their doc strings and validation tags are the wire contract); the pair's
// registration and handler plumbing collapse here.
type scalarPair[GO, PI, PO, T any] struct {
	path       string
	getID      string
	getSummary string
	getDesc    string
	putID      string
	putSummary string
	putDesc    string
	// putErrors lists the PUT's declared business error codes; nil declares
	// none (pairs whose input schema alone constrains the value).
	putErrors []int
	// auditKey is the system_config resource ID recorded on updates.
	auditKey string
	// keys are the system_config keys the pair's entity tag covers.
	keys []string

	// get is the service accessor, captured as a method value and never
	// called at registration time (specgen wires a nil service). set is a
	// method expression, so the write runs on the guarded view of the
	// service. wrap/echo build the GET and PUT outputs; value extracts the
	// PUT input's value. get reports the value and whether it was stored
	// rather than defaulted; wrap carries both onto the wire so the console
	// can tell them apart.
	get   func(context.Context) (T, bool, error)
	wrap  func(T, bool) *GO
	value func(*PI) T
	set   func(*Service, context.Context, T) error
	echo  func(T) *PO
}

// registerScalar registers a scalar settings pair under the Admin tag: GET
// reads via get and wraps the value into the output; PUT extracts the input
// value, writes via set, audits the change, and echoes the stored value.
func registerScalar[GO, PI, PO, T any](api huma.API, h *Handler, p scalarPair[GO, PI, PO, T]) {
	huma.Register(api, huma.Operation{
		OperationID: p.getID,
		Method:      http.MethodGet,
		Path:        p.path,
		Summary:     p.getSummary,
		Description: p.getDesc,
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
	}, func(ctx context.Context, _ *struct{}) (*GO, error) {
		etag, err := h.service.SettingETag(ctx, p.keys)
		if err != nil {
			return nil, web.MapError(err)
		}
		v, isSet, err := p.get(ctx)
		if err != nil {
			return nil, web.MapError(err)
		}
		return tagged(p.wrap(v, isSet), etag), nil
	})

	huma.Register(api, huma.Operation{
		OperationID: p.putID,
		Method:      http.MethodPut,
		Path:        p.path,
		Summary:     p.putSummary,
		Description: p.putDesc,
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
		Errors:      append(slices.Clone(p.putErrors), http.StatusPreconditionFailed),
	}, func(ctx context.Context, in *PI) (*PO, error) {
		v := p.value(in)
		guard := h.guard(p.keys, in)
		if err := p.set(h.service.Guarded(guard), ctx, v); err != nil {
			return nil, web.MapError(err)
		}
		h.auditConfigChange(ctx, p.auditKey, map[string]any{
			metaKeyKind: metaSystemConfig, "value": v,
		})
		return tagged(p.echo(v), guard.ETag), nil
	})
}

// guard builds the precondition a settings write runs under from the
// request's If-Match header.
func (h *Handler) guard(keys []string, in any) *Guard {
	g := &Guard{Keys: keys}
	if header, ok := in.(interface{ ifMatch() string }); ok {
		g.IfMatch = header.ifMatch()
	}
	return g
}

// tagged sets the entity tag on an output that carries ETagHeader.
func tagged[T any](out *T, etag string) *T {
	if header, ok := any(out).(interface{ setETag(string) }); ok {
		header.setETag(etag)
	}
	return out
}

// registerSettings registers the system-config settings pairs, in the same
// order the surface has always declared them.
func (h *Handler) registerSettings(api huma.API) {
	registerScalar(api, h, scalarPair[GetZonePinOutput, UpdateZonePinInput, UpdateZonePinOutput, string]{
		path:       "/v2/admin/system-config/zone-pin",
		getID:      "get-zone-pin",
		getSummary: "Get placement zone pin",
		getDesc:    "Returns the single zone placement is restricted to (empty = ranked across the enabled catalog)",
		putID:      "update-zone-pin",
		putSummary: "Update placement zone pin",
		putDesc:    "Restricts placement to a single zone; empty clears the pin",
		auditKey:   "zone-pin",
		keys:       keysZonePin,
		get:        h.service.GetZonePin,
		wrap: func(v string, isSet bool) *GetZonePinOutput {
			out := &GetZonePinOutput{}
			out.Body.Value, out.Body.IsSet = v, isSet
			return out
		},
		value: func(in *UpdateZonePinInput) string { return in.Body.Value },
		set:   (*Service).UpdateZonePin,
		echo:  func(v string) *UpdateZonePinOutput { out := &UpdateZonePinOutput{}; out.Body.Value = v; return out },
	})

	registerScalar(api, h, scalarPair[GetQuotaMarginOutput, UpdateQuotaMarginInput, UpdateQuotaMarginOutput, int64]{
		path:       "/v2/admin/system-config/quota-margin",
		getID:      "get-quota-margin",
		getSummary: "Get placement quota margin",
		getDesc:    "Returns the percent of each regional quota limit zone ranking keeps in reserve",
		putID:      "update-quota-margin",
		putSummary: "Update placement quota margin",
		putDesc:    "Sets the percent of each regional quota limit zone ranking keeps in reserve",
		putErrors:  []int{http.StatusBadRequest},
		auditKey:   "quota-margin",
		keys:       keysQuotaMargin,
		get:        h.service.GetQuotaMargin,
		wrap: func(v int64, isSet bool) *GetQuotaMarginOutput {
			out := &GetQuotaMarginOutput{}
			out.Body.Value, out.Body.IsSet = v, isSet
			return out
		},
		value: func(in *UpdateQuotaMarginInput) int64 { return in.Body.Value },
		set:   (*Service).UpdateQuotaMargin,
		echo: func(v int64) *UpdateQuotaMarginOutput {
			out := &UpdateQuotaMarginOutput{}
			out.Body.Value = v
			return out
		},
	})

	registerScalar(api, h, scalarPair[GetV1ZoneHintOutput, UpdateV1ZoneHintInput, UpdateV1ZoneHintOutput, bool]{
		path:       "/v2/admin/system-config/v1-zone-hint",
		getID:      "get-v1-zone-hint",
		getSummary: "Get legacy zone-token mode",
		getDesc:    "Returns whether the legacy /v1 zone token is read as a placement hint (default) or a literal pin",
		putID:      "update-v1-zone-hint",
		putSummary: "Update legacy zone-token mode",
		putDesc:    "Sets whether the legacy /v1 zone token is read as a placement hint or a literal pin",
		auditKey:   "v1-zone-hint",
		keys:       keysV1ZoneHint,
		get:        h.service.GetV1ZoneHint,
		wrap: func(v, isSet bool) *GetV1ZoneHintOutput {
			out := &GetV1ZoneHintOutput{}
			out.Body.Value, out.Body.IsSet = v, isSet
			return out
		},
		value: func(in *UpdateV1ZoneHintInput) bool { return in.Body.Value },
		set:   (*Service).UpdateV1ZoneHint,
		echo:  func(v bool) *UpdateV1ZoneHintOutput { out := &UpdateV1ZoneHintOutput{}; out.Body.Value = v; return out },
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-docker-host",
		Method:      http.MethodGet,
		Path:        "/v2/admin/system-config/docker-host",
		Summary:     "Get legacy docker-host shape",
		Description: "Returns the GCE shape a legacy /v1 docker-only host create compiles to (OmniLab ATS sends that shape)",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
	}, h.GetDockerHost)

	huma.Register(api, huma.Operation{
		OperationID: "update-docker-host",
		Method:      http.MethodPut,
		Path:        "/v2/admin/system-config/docker-host",
		Summary:     "Update legacy docker-host shape",
		Description: "Sets the machine type and lease a legacy docker-only host create provisions. The machine type is validated at create time, not here.",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
		Errors:      []int{http.StatusBadRequest, http.StatusPreconditionFailed},
	}, h.UpdateDockerHost)

	registerScalar(api, h, scalarPair[GetSpotAutoDefaultOutput, UpdateSpotAutoDefaultInput, UpdateSpotAutoDefaultOutput, bool]{
		path:       "/v2/admin/system-config/spot-auto-default",
		getID:      "get-spot-auto-default",
		getSummary: "Get Spot auto-default",
		getDesc:    "Returns whether auto-capacity headless intent resolves to Spot hosts (default false)",
		putID:      "update-spot-auto-default",
		putSummary: "Update Spot auto-default",
		putDesc:    "Sets whether auto-capacity headless intent (devices and batch members) resolves to Spot hosts",
		auditKey:   "spot-auto-default",
		keys:       keysSpotAutoDefault,
		get:        h.service.GetSpotAutoDefault,
		wrap: func(v, isSet bool) *GetSpotAutoDefaultOutput {
			out := &GetSpotAutoDefaultOutput{}
			out.Body.Value, out.Body.IsSet = v, isSet
			return out
		},
		value: func(in *UpdateSpotAutoDefaultInput) bool { return in.Body.Value },
		set:   (*Service).UpdateSpotAutoDefault,
		echo: func(v bool) *UpdateSpotAutoDefaultOutput {
			out := &UpdateSpotAutoDefaultOutput{}
			out.Body.Value = v
			return out
		},
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-spot-min-uptime",
		Method:      http.MethodGet,
		Path:        "/v2/admin/system-config/spot-min-uptime",
		Summary:     "Get Spot min-uptime tiers",
		Description: "Returns the minimum estimated-uptime tiers Spot placement demands from capacity advice",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
	}, h.GetSpotMinUptime)

	huma.Register(api, huma.Operation{
		OperationID: "update-spot-min-uptime",
		Method:      http.MethodPut,
		Path:        "/v2/admin/system-config/spot-min-uptime",
		Summary:     "Update Spot min-uptime tiers",
		Description: "Sets the minimum estimated-uptime tiers Spot placement demands from capacity advice",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
		Errors:      []int{http.StatusBadRequest, http.StatusPreconditionFailed},
	}, h.UpdateSpotMinUptime)

	registerScalar(api, h, scalarPair[GetDefaultGPUMachineTypeOutput, UpdateDefaultGPUMachineTypeInput, UpdateDefaultGPUMachineTypeOutput, string]{
		path:       "/v2/admin/system-config/default-gpu-machine-type",
		getID:      "get-default-gpu-machine-type",
		getSummary: "Get GPU host machine type",
		getDesc:    "Returns the machine-type override for auto-provisioned GPU hosts (empty = catalog fallback)",
		putID:      "update-default-gpu-machine-type",
		putSummary: "Update GPU host machine type",
		putDesc:    "Sets the machine type for auto-provisioned GPU hosts; empty clears the override",
		auditKey:   "default-gpu-machine-type",
		keys:       keysDefaultGPUMachineType,
		get:        h.service.GetDefaultGPUMachineType,
		wrap: func(v string, isSet bool) *GetDefaultGPUMachineTypeOutput {
			out := &GetDefaultGPUMachineTypeOutput{}
			out.Body.Value, out.Body.IsSet = v, isSet
			return out
		},
		value: func(in *UpdateDefaultGPUMachineTypeInput) string { return in.Body.Value },
		set:   (*Service).UpdateDefaultGPUMachineType,
		echo: func(v string) *UpdateDefaultGPUMachineTypeOutput {
			out := &UpdateDefaultGPUMachineTypeOutput{}
			out.Body.Value = v
			return out
		},
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-default-upstream-build",
		Method:      http.MethodGet,
		Path:        "/v2/admin/system-config/default-upstream-build",
		Summary:     "Get upstream default build",
		Description: "Returns the AOSP branch/target used when a declarative create finds no registered builds",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
	}, h.GetDefaultUpstreamBuild)

	huma.Register(api, huma.Operation{
		OperationID: "update-default-upstream-build",
		Method:      http.MethodPut,
		Path:        "/v2/admin/system-config/default-upstream-build",
		Summary:     "Update upstream default build",
		Description: "Sets the AOSP branch/target used when a declarative create finds no registered builds",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
		Errors:      []int{http.StatusPreconditionFailed},
	}, h.UpdateDefaultUpstreamBuild)

	huma.Register(api, huma.Operation{
		OperationID: "get-lifetimes",
		Method:      http.MethodGet,
		Path:        "/v2/admin/system-config/lifetimes",
		Summary:     "Get lifetime policy",
		Description: "Returns how long device sessions and host leases run when the caller does not say, alongside the compiled caps and the teardown grace",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
	}, h.GetLifetimes)

	huma.Register(api, huma.Operation{
		OperationID: "update-lifetimes",
		Method:      http.MethodPut,
		Path:        "/v2/admin/system-config/lifetimes",
		Summary:     "Update lifetime policy",
		Description: "Sets the default device session TTL, the extension grant and allowance, and the default host lease. The caps stay compiled: raising a ceiling is a deploy.",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
		Errors:      []int{http.StatusBadRequest, http.StatusPreconditionFailed},
	}, h.UpdateLifetimes)

	huma.Register(api, huma.Operation{
		OperationID: "get-artifact-buckets",
		Method:      http.MethodGet,
		Path:        "/v2/admin/system-config/artifact-buckets",
		Summary:     "Get artifact buckets",
		Description: "Returns the GCS buckets device artifacts (bugreports, screen recordings) are captured into, and the deployment default they fall back to",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
	}, h.GetArtifactBuckets)

	huma.Register(api, huma.Operation{
		OperationID: "update-artifact-buckets",
		Method:      http.MethodPut,
		Path:        "/v2/admin/system-config/artifact-buckets",
		Summary:     "Update artifact buckets",
		Description: "Sets the per-type artifact bucket overrides; empty clears an override (the deployment default applies)",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
		Errors:      []int{http.StatusBadRequest, http.StatusPreconditionFailed},
	}, h.UpdateArtifactBuckets)

	huma.Register(api, huma.Operation{
		OperationID: "get-sweeps",
		Method:      http.MethodGet,
		Path:        "/v2/admin/system-config/sweeps",
		Summary:     "Get sweep policy",
		Description: "Returns what the background loops may do on their own: the three destructive sweeps (off = detect only) and whether the reaper's deletions are suspended",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
	}, h.GetSweepPolicy)

	huma.Register(api, huma.Operation{
		OperationID: "update-sweeps",
		Method:      http.MethodPut,
		Path:        "/v2/admin/system-config/sweeps",
		Summary:     "Update sweep policy",
		Description: "Changes only the switches named in the body; a switch left out keeps its current value",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
		Errors:      []int{http.StatusBadRequest, http.StatusPreconditionFailed},
	}, h.UpdateSweepPolicy)

	huma.Register(api, huma.Operation{
		OperationID: "get-fleet",
		Method:      http.MethodGet,
		Path:        "/v2/admin/system-config/fleet",
		Summary:     "Get fleet autoscaler policy",
		Description: "Returns the autoscaler policy (mode, per-tier standing capacity, demand horizon, drain grace) with the compiled bounds",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
	}, h.GetFleetPolicy)

	huma.Register(api, huma.Operation{
		OperationID: "update-fleet-autoscaler",
		Method:      http.MethodPut,
		Path:        "/v2/admin/system-config/fleet/autoscaler",
		Summary:     "Update the autoscaler mode and one pool's two lower bounds",
		Description: "Sets how much of the autoscaler is switched on, and how many hosts the named pool keeps free (standby) and keeps running whether or not devices fill them (floor); off provisions nothing, 0 in either bound holds nothing, and naming a pool switches it on",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
		Errors:      []int{http.StatusBadRequest, http.StatusPreconditionFailed},
	}, h.UpdateFleetAutoscaler)

	huma.Register(api, huma.Operation{
		OperationID: "disable-fleet-pool",
		Method:      http.MethodDelete,
		Path:        "/v2/admin/system-config/fleet/pools/{pool}",
		Summary:     "Switch a pool off",
		Description: "Removes the pool's bounds, windows and shape ladder: creates are no longer routed into it and its machines drain by ordinary reclamation. The default pool cannot be switched off",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
		Errors:      []int{http.StatusBadRequest, http.StatusPreconditionFailed},
	}, h.DisableFleetPool)

	huma.Register(api, huma.Operation{
		OperationID: "update-fleet-tuning",
		Method:      http.MethodPut,
		Path:        "/v2/admin/system-config/fleet/tuning",
		Summary:     "Update the fleet demand horizon and drain grace",
		Description: "Sets how far back demand is measured and how much lease a host keeps when it is shortened as surplus",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
		Errors:      []int{http.StatusBadRequest, http.StatusPreconditionFailed},
	}, h.UpdateFleetTuning)

	huma.Register(api, huma.Operation{
		OperationID: "update-fleet-schedules",
		Method:      http.MethodPut,
		Path:        "/v2/admin/system-config/fleet/schedules",
		Summary:     "Update a pool's schedule windows",
		Description: "Replaces the recurring wall-clock windows that raise the named pool's standby target while they are open; an empty list clears them",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
		Errors:      []int{http.StatusBadRequest, http.StatusPreconditionFailed},
	}, h.UpdateFleetSchedules)

	huma.Register(api, huma.Operation{
		OperationID: "update-fleet-ladder",
		Method:      http.MethodPut,
		Path:        "/v2/admin/system-config/fleet/ladder",
		Summary:     "Update a pool's shape ladder",
		Description: "Replaces the machine types the named pool's provisioning prefers, in order; provisioning steps down the ladder when GCE has no capacity for the shape above, and an empty list restores the catalog default. Which rungs are admissible depends on the pool",
		Tags:        []string{tagAdmin},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
		Errors:      []int{http.StatusBadRequest, http.StatusPreconditionFailed},
	}, h.UpdateFleetLadder)
}

// dockerHostWire renders the stored policy with the compiled TTL cap.
func dockerHostWire(policy hostconfig.DockerHost) dockerHostBody {
	return dockerHostBody{
		MachineType: policy.MachineType,
		TTLHours:    policy.TTLHours,
		IsSet:       policy.IsSet,
		MaxTTLHours: host.MaxHostTTLHours,
	}
}

// GetDockerHost returns the docker-host policy.
func (h *Handler) GetDockerHost(ctx context.Context, _ *struct{}) (*GetDockerHostOutput, error) {
	etag, err := h.service.SettingETag(ctx, keysDockerHost)
	if err != nil {
		return nil, web.MapError(err)
	}
	policy, err := h.service.GetDockerHost(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &GetDockerHostOutput{}
	output.Body = dockerHostWire(policy)
	output.setETag(etag)
	return output, nil
}

// UpdateDockerHost stores the docker-host policy.
func (h *Handler) UpdateDockerHost(ctx context.Context, input *UpdateDockerHostInput) (*UpdateDockerHostOutput, error) {
	guard := h.guard(keysDockerHost, input)
	if err := h.service.Guarded(guard).UpdateDockerHost(ctx, input.Body.MachineType, input.Body.TTLHours); err != nil {
		return nil, web.MapError(err)
	}

	h.auditConfigChange(ctx, "docker-host", map[string]any{
		metaKeyKind: metaSystemConfig, "machine_type": input.Body.MachineType, "ttl_hours": input.Body.TTLHours,
	})

	stored, err := h.service.GetDockerHost(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &UpdateDockerHostOutput{}
	output.Body = dockerHostWire(stored)
	output.setETag(guard.ETag)
	return output, nil
}

// GetSpotMinUptime returns the Spot min-uptime tiers.
func (h *Handler) GetSpotMinUptime(ctx context.Context, _ *struct{}) (*GetSpotMinUptimeOutput, error) {
	etag, err := h.service.SettingETag(ctx, keysSpotMinUptime)
	if err != nil {
		return nil, web.MapError(err)
	}
	tiers, err := h.service.GetSpotMinUptimes(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}

	output := &GetSpotMinUptimeOutput{}
	output.Body = spotMinUptimeBody{
		SingleSeconds: tiers.Single, BatchSeconds: tiers.Batch, IsSet: tiers.IsSet,
	}

	output.setETag(etag)
	return output, nil
}

// UpdateSpotMinUptime sets the Spot min-uptime tiers.
func (h *Handler) UpdateSpotMinUptime(ctx context.Context, input *UpdateSpotMinUptimeInput) (*UpdateSpotMinUptimeOutput, error) {
	guard := h.guard(keysSpotMinUptime, input)
	if err := h.service.Guarded(guard).UpdateSpotMinUptimes(ctx, input.Body.SingleSeconds, input.Body.BatchSeconds); err != nil {
		return nil, web.MapError(err)
	}

	h.auditConfigChange(ctx, "spot-min-uptime", map[string]any{
		metaKeyKind: metaSystemConfig, "single_seconds": input.Body.SingleSeconds, "batch_seconds": input.Body.BatchSeconds,
	})

	output := &UpdateSpotMinUptimeOutput{}
	output.Body = spotMinUptimeBody{SingleSeconds: input.Body.SingleSeconds, BatchSeconds: input.Body.BatchSeconds}

	output.setETag(guard.ETag)
	return output, nil
}

// GetDefaultUpstreamBuild returns the upstream default build pair.
func (h *Handler) GetDefaultUpstreamBuild(ctx context.Context, _ *struct{}) (*GetDefaultUpstreamBuildOutput, error) {
	etag, err := h.service.SettingETag(ctx, keysDefaultUpstreamBuild)
	if err != nil {
		return nil, web.MapError(err)
	}
	build, err := h.service.GetDefaultUpstreamBuild(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}

	output := &GetDefaultUpstreamBuildOutput{}
	output.Body.Branch = build.Branch
	output.Body.Target = build.Target
	output.Body.IsSet = build.IsSet

	output.setETag(etag)
	return output, nil
}

// UpdateDefaultUpstreamBuild sets the upstream default build pair.
func (h *Handler) UpdateDefaultUpstreamBuild(ctx context.Context, input *UpdateDefaultUpstreamBuildInput) (*UpdateDefaultUpstreamBuildOutput, error) {
	guard := h.guard(keysDefaultUpstreamBuild, input)
	if err := h.service.Guarded(guard).UpdateDefaultUpstreamBuild(ctx, input.Body.Branch, input.Body.Target); err != nil {
		return nil, web.MapError(err)
	}

	h.auditConfigChange(ctx, "default-upstream-build", map[string]any{
		metaKeyKind: metaSystemConfig, "branch": input.Body.Branch, "target": input.Body.Target,
	})

	output := &UpdateDefaultUpstreamBuildOutput{}
	output.Body.Branch = input.Body.Branch
	output.Body.Target = input.Body.Target

	output.setETag(guard.ETag)
	return output, nil
}

// lifetimesWire renders the stored policy with the compiled caps and grace.
// It is the one place those constants meet the wire, so every client renders
// bounds it never retyped.
func lifetimesWire(policy hostconfig.Lifetimes) lifetimesBody {
	return lifetimesBody{
		DeviceTTLHours:    policy.DeviceTTLHours,
		ExtendHours:       policy.ExtendHours,
		ExtendCount:       policy.ExtendCount,
		HostTTLHours:      policy.HostTTLHours,
		IsSet:             policy.IsSet,
		MaxDeviceTTLHours: device.MaxTTLHours,
		MaxExtendHours:    device.MaxExtendHours,
		MaxExtendCount:    int64(device.MaxExtendCount),
		MaxHostTTLHours:   host.MaxHostTTLHours,
		MaxDevicesPerHost: host.MaxDevicesPerHost,
		// Equal to the cap today, and still a separate field: the cap is
		// a policy choice and this is what the image can physically back. They
		// diverge the moment either the image is respun or the cap is raised,
		// and the ruler's unbacked tail is drawn from the difference.
		HostImageBackedSlots: host.HostImageCVDAccounts,
		GracePeriodSeconds:   host.GracePeriodSeconds,
	}
}

// GetLifetimes returns the lifetime policy.
func (h *Handler) GetLifetimes(ctx context.Context, _ *struct{}) (*GetLifetimesOutput, error) {
	etag, err := h.service.SettingETag(ctx, keysLifetimes)
	if err != nil {
		return nil, web.MapError(err)
	}
	policy, err := h.service.GetLifetimes(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &GetLifetimesOutput{}
	output.Body = lifetimesWire(policy)
	output.setETag(etag)
	return output, nil
}

// UpdateLifetimes stores the lifetime policy.
func (h *Handler) UpdateLifetimes(ctx context.Context, input *UpdateLifetimesInput) (*UpdateLifetimesOutput, error) {
	policy := hostconfig.Lifetimes{
		DeviceTTLHours: input.Body.DeviceTTLHours,
		ExtendHours:    input.Body.ExtendHours,
		ExtendCount:    input.Body.ExtendCount,
		HostTTLHours:   input.Body.HostTTLHours,
	}
	guard := h.guard(keysLifetimes, input)
	if err := h.service.Guarded(guard).UpdateLifetimes(ctx, policy); err != nil {
		return nil, web.MapError(err)
	}

	h.auditConfigChange(ctx, "lifetimes", map[string]any{
		metaKeyKind:        metaSystemConfig,
		"device_ttl_hours": input.Body.DeviceTTLHours,
		"extend_hours":     input.Body.ExtendHours,
		"extend_count":     input.Body.ExtendCount,
		"host_ttl_hours":   input.Body.HostTTLHours,
	})

	stored, err := h.service.GetLifetimes(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &UpdateLifetimesOutput{}
	output.Body = lifetimesWire(stored)
	output.setETag(guard.ETag)
	return output, nil
}

// GetArtifactBuckets returns the artifact bucket settings.
func (h *Handler) GetArtifactBuckets(ctx context.Context, _ *struct{}) (*GetArtifactBucketsOutput, error) {
	etag, err := h.service.SettingETag(ctx, keysArtifactBuckets)
	if err != nil {
		return nil, web.MapError(err)
	}
	buckets, err := h.service.GetArtifactBuckets(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &GetArtifactBucketsOutput{}
	output.Body = artifactBucketsBody{
		IsSet:           buckets.IsSet,
		BugreportBucket: buckets.Bugreport,
		RecordingBucket: buckets.Recording,
		DefaultBucket:   buckets.DefaultBucket,
		CaptureOnError:  buckets.CaptureOnError,
	}
	output.setETag(etag)
	return output, nil
}

// UpdateArtifactBuckets sets the per-type artifact bucket overrides and the
// capture-on-error toggle.
func (h *Handler) UpdateArtifactBuckets(ctx context.Context, input *UpdateArtifactBucketsInput) (*UpdateArtifactBucketsOutput, error) {
	guard := h.guard(keysArtifactBuckets, input)
	if err := h.service.Guarded(guard).UpdateArtifactBuckets(ctx, input.Body.BugreportBucket, input.Body.RecordingBucket, input.Body.CaptureOnError); err != nil {
		return nil, web.MapError(err)
	}

	h.auditConfigChange(ctx, "artifact-buckets", map[string]any{
		metaKeyKind:        metaSystemConfig,
		"bugreport_bucket": input.Body.BugreportBucket, "recording_bucket": input.Body.RecordingBucket,
		"capture_on_error": input.Body.CaptureOnError,
	})

	buckets, err := h.service.GetArtifactBuckets(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &UpdateArtifactBucketsOutput{}
	output.Body = artifactBucketsBody{
		BugreportBucket: buckets.Bugreport,
		RecordingBucket: buckets.Recording,
		DefaultBucket:   buckets.DefaultBucket,
		CaptureOnError:  buckets.CaptureOnError,
	}
	output.setETag(guard.ETag)
	return output, nil
}

// GetSweepPolicy returns what each background loop is allowed to do.
func (h *Handler) GetSweepPolicy(ctx context.Context, _ *struct{}) (*GetSweepPolicyOutput, error) {
	etag, err := h.service.SettingETag(ctx, keysSweeps)
	if err != nil {
		return nil, web.MapError(err)
	}
	policy, err := h.service.GetSweepPolicy(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &GetSweepPolicyOutput{}
	output.Body = policy
	output.setETag(etag)
	return output, nil
}

// UpdateSweepPolicy changes the switches the body names.
func (h *Handler) UpdateSweepPolicy(ctx context.Context, input *UpdateSweepPolicyInput) (*UpdateSweepPolicyOutput, error) {
	patch := hostconfig.SweepPolicyPatch{
		DeleteOrphanVMs: input.Body.DeleteOrphanVMs,
		OrphanCVDs:      input.Body.OrphanCVDs,
		ZombieRecreate:  input.Body.ZombieRecreate,
		ReaperDryRun:    input.Body.ReaperDryRun,
	}
	if patch.IsEmpty() {
		return nil, huma.Error400BadRequest("at least one of delete_orphan_vms, orphan_cvds, zombie_recreate, reaper_dry_run is required")
	}
	guard := h.guard(keysSweeps, input)
	if err := h.service.Guarded(guard).UpdateSweepPolicy(ctx, patch); err != nil {
		return nil, web.MapError(err)
	}

	h.auditConfigChange(ctx, "sweeps", sweepPatchAudit(patch))

	policy, err := h.service.GetSweepPolicy(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &UpdateSweepPolicyOutput{}
	output.Body = policy
	output.setETag(guard.ETag)
	return output, nil
}

// fleetPolicyWire renders the stored policy with the compiled bounds beside
// it, so the console never carries its own copy of a limit.
func fleetPolicyWire(policy hostconfig.FleetPolicy) fleetPolicyBody {
	return fleetPolicyBody{
		Mode:                policy.Mode,
		Tiers:               policy.Tiers,
		Floor:               policy.Floor,
		DemandHorizonSec:    policy.DemandHorizonSec,
		DrainGraceSec:       policy.DrainGraceSec,
		ShapeLadder:         policy.ShapeLadder,
		IsSet:               policy.IsSet,
		MaxStandby:          hostconfig.MaxStandbyHosts,
		MaxFloor:            hostconfig.MaxFleetFloor,
		MaxDemandHorizonSec: hostconfig.MaxDemandHorizonSec,
		MinDrainGraceSec:    hostconfig.MinDrainGraceSec,
		MaxDrainGraceSec:    hostconfig.MaxDrainGraceSec,
		MaxSchedules:        hostconfig.MaxSchedules,
		MaxLadderRungs:      hostconfig.MaxLadderRungs,
	}
}

// GetFleetPolicy returns the autoscaler policy.
func (h *Handler) GetFleetPolicy(ctx context.Context, _ *struct{}) (*GetFleetPolicyOutput, error) {
	etag, err := h.service.SettingETag(ctx, keysFleet)
	if err != nil {
		return nil, web.MapError(err)
	}
	policy, err := h.service.GetFleetPolicy(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &GetFleetPolicyOutput{}
	output.Body = fleetPolicyWire(policy)
	output.setETag(etag)
	return output, nil
}

// namedPool is the pool a per-pool write body meant. An unnamed pool is the
// default one, which is what every client that predates pools is editing, and
// the only pool a deployment with one has.
func namedPool(pool string) string {
	if pool == "" {
		return hostconfig.DefaultTier
	}
	return pool
}

// UpdateFleetAutoscaler sets the mode and one pool's two lower bounds. The mode
// is the whole loop's; the bounds belong to the pool the body names, and naming
// a pool is what switches it on.
func (h *Handler) UpdateFleetAutoscaler(ctx context.Context, input *UpdateFleetAutoscalerInput) (*UpdateFleetAutoscalerOutput, error) {
	mode := hostconfig.AutoscaleMode(input.Body.Mode)
	// The enum tag already rejects anything else; this is the guard for the day
	// the tag and the vocabulary drift apart, which is the one direction the
	// agreement test cannot cover: a tag that gained a value the domain lacks.
	if !mode.Valid() {
		return nil, huma.Error400BadRequest("mode must be one of off, provision_only, on")
	}
	pool := namedPool(input.Body.Pool)
	guard := h.guard(keysFleet, input)
	if err := h.service.Guarded(guard).UpdateFleetAutoscaler(ctx, mode, pool,
		input.Body.Standby, input.Body.Floor); err != nil {
		return nil, web.MapError(err)
	}
	h.auditConfigChange(ctx, "fleet-autoscaler", map[string]any{
		metaKeyKind: metaSystemConfig, "mode": input.Body.Mode, metaKeyPool: pool,
		"standby": input.Body.Standby, "floor": input.Body.Floor,
	})
	policy, err := h.service.GetFleetPolicy(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &UpdateFleetAutoscalerOutput{}
	output.Body = fleetPolicyWire(policy)
	output.setETag(guard.ETag)
	return output, nil
}

// UpdateFleetTuning sets the demand horizon and the drain grace.
func (h *Handler) UpdateFleetTuning(ctx context.Context, input *UpdateFleetTuningInput) (*UpdateFleetTuningOutput, error) {
	guard := h.guard(keysFleet, input)
	if err := h.service.Guarded(guard).UpdateFleetTuning(ctx, input.Body.DemandHorizonSec, input.Body.DrainGraceSec); err != nil {
		return nil, web.MapError(err)
	}
	h.auditConfigChange(ctx, "fleet-tuning", map[string]any{
		metaKeyKind:          metaSystemConfig,
		"demand_horizon_sec": input.Body.DemandHorizonSec,
		"drain_grace_sec":    input.Body.DrainGraceSec,
	})
	policy, err := h.service.GetFleetPolicy(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &UpdateFleetTuningOutput{}
	output.Body = fleetPolicyWire(policy)
	output.setETag(guard.ETag)
	return output, nil
}

// UpdateFleetSchedules replaces one pool's window list.
func (h *Handler) UpdateFleetSchedules(ctx context.Context, input *UpdateFleetSchedulesInput) (*UpdateFleetSchedulesOutput, error) {
	pool := namedPool(input.Body.Pool)
	guard := h.guard(keysFleet, input)
	if err := h.service.Guarded(guard).UpdateFleetSchedules(ctx, pool, input.Body.Schedules); err != nil {
		return nil, web.MapError(err)
	}
	// The windows go into the entry whole rather than as a count. The config row
	// is overwritten in place, so the audit log is the only durable answer to
	// "what did that window say before somebody fixed it".
	h.auditConfigChange(ctx, "fleet-schedules", map[string]any{
		metaKeyKind: metaSystemConfig, metaKeyPool: pool, "schedules": input.Body.Schedules,
	})
	policy, err := h.service.GetFleetPolicy(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &UpdateFleetSchedulesOutput{}
	output.Body = fleetPolicyWire(policy)
	output.setETag(guard.ETag)
	return output, nil
}

// UpdateFleetLadder replaces one pool's machine-type preference order.
func (h *Handler) UpdateFleetLadder(ctx context.Context, input *UpdateFleetLadderInput) (*UpdateFleetLadderOutput, error) {
	pool := namedPool(input.Body.Pool)
	guard := h.guard(keysFleet, input)
	if err := h.service.Guarded(guard).UpdateFleetShapeLadder(ctx, pool, input.Body.ShapeLadder); err != nil {
		return nil, web.MapError(err)
	}
	// The rungs go into the entry whole rather than as a count. The config row
	// is overwritten in place, so the audit log is the only durable answer to
	// which shape the fleet preferred before somebody reordered it.
	h.auditConfigChange(ctx, "fleet-ladder", map[string]any{
		metaKeyKind: metaSystemConfig, metaKeyPool: pool, "shape_ladder": input.Body.ShapeLadder,
	})
	policy, err := h.service.GetFleetPolicy(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &UpdateFleetLadderOutput{}
	output.Body = fleetPolicyWire(policy)
	output.setETag(guard.ETag)
	return output, nil
}

// DisableFleetPool switches a pool off, taking its bounds, windows and ladder
// with it.
func (h *Handler) DisableFleetPool(ctx context.Context, input *DisableFleetPoolInput) (*DisableFleetPoolOutput, error) {
	guard := h.guard(keysFleet, input)
	if err := h.service.Guarded(guard).DisableFleetPool(ctx, input.Pool); err != nil {
		return nil, web.MapError(err)
	}
	h.auditConfigChange(ctx, "fleet-pool-disable", map[string]any{
		metaKeyKind: metaSystemConfig, metaKeyPool: input.Pool,
	})
	policy, err := h.service.GetFleetPolicy(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &DisableFleetPoolOutput{}
	output.Body = fleetPolicyWire(policy)
	output.setETag(guard.ETag)
	return output, nil
}

// sweepPatchAudit records only the switches the operator actually moved, so
// the Activity entry reads as the one change it was.
func sweepPatchAudit(patch hostconfig.SweepPolicyPatch) map[string]any {
	changes := map[string]any{metaKeyKind: metaSystemConfig}
	for key, value := range map[string]*bool{
		"delete_orphan_vms": patch.DeleteOrphanVMs,
		"orphan_cvds":       patch.OrphanCVDs,
		"zombie_recreate":   patch.ZombieRecreate,
		"reaper_dry_run":    patch.ReaperDryRun,
	} {
		if value != nil {
			changes[key] = *value
		}
	}
	return changes
}
