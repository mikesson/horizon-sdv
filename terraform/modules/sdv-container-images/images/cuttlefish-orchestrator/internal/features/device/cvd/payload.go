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

package cvd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"

	"cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/features/device/validation"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// groupMembers filters a host's CVD list to one group's members.
func groupMembers(cvds []*hoclient.CVD, group string) []*hoclient.CVD {
	var members []*hoclient.CVD
	for _, c := range cvds {
		if c.Group == group {
			members = append(members, c)
		}
	}
	return members
}

// transientGroupStatus returns the first in-flight status (Starting/Stopping)
// among the group's CVDs, or "" when the group is quiescent and safe to
// adopt, start, or delete.
func transientGroupStatus(cvds []*hoclient.CVD) string {
	for _, c := range cvds {
		if c.Status == device.CVDStateStarting || c.Status == device.CVDStateStopping {
			return c.Status
		}
	}
	return ""
}

// defaultTarget is the build target a create falls back to when neither the
// build record nor the device names one.
const defaultTarget = "aosp_cf_x86_64_phone-trunk_staging-userdebug"

// abTarget resolves the build target with the payload's fallback chain
// (record → device → default). A record with a target always wins, which is
// what keeps build.PinnedFetchKey, which reads record fields only,
// byte-identical to the key the payload ships for every pinned build.
func abTarget(record *build.Build, d *device.Device) string {
	if record != nil && record.Target != "" {
		return record.Target
	}
	if d.BuildTarget != "" {
		return d.BuildTarget
	}
	return defaultTarget
}

// pinnedArtifactKey resolves the fetch key the device's create shipped, but
// only for a registered, pinned (build-id-keyed) Android Build. Those are the
// only keys whose host-side cache residency CCO can know at placement time.
// Best-effort by contract: any lookup failure reports no key.
func (a *Actuator) pinnedArtifactKey(ctx context.Context, d *device.Device) (string, bool) {
	if d.BuildID == "" {
		return "", false
	}
	record, err := a.builds.Get(ctx, d.BuildID)
	if err != nil {
		return "", false
	}
	return record.PinnedFetchKey()
}

// resolveBuildArtifacts resolves the device's build into diskConfig/bootConfig
// (build references keyed by canonical env_config field) and returns the host
// package URL. With no build record it falls back to treating BuildID as an
// @ab reference; an empty BuildID yields no artifacts.
func (a *Actuator) resolveBuildArtifacts(ctx context.Context, d *device.Device, diskConfig, bootConfig map[string]any) (string, error) {
	if d.BuildID == "" {
		return "", nil
	}

	buildRecord, err := a.builds.Get(ctx, d.BuildID)
	if errors.Is(err, build.ErrNotFound) {
		// Not a registered build: treat BuildID as an Android Build ID or
		// branch directly. Infrastructure failures must not take this
		// fallback, because they would silently target the wrong artifacts.
		a.logger.Info("build not registered; using build ID directly",
			slog.String("device_id", d.ID), slog.String("build_id", d.BuildID))
		diskConfig["default_build"] = build.FetchKey(d.BuildID, abTarget(nil, d))
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("looking up build %s: %w", d.BuildID, err)
	}
	return routeBuildRecord(buildRecord, d, diskConfig, bootConfig, a.logger)
}

// routeBuildRecord routes a registered build's artifacts into diskConfig and
// bootConfig and returns its host package URL. The device-level build and
// each instance's "@build" share it, so both key the same record identically.
func routeBuildRecord(record *build.Build, d *device.Device, diskConfig, bootConfig map[string]any, logger *slog.Logger) (string, error) {
	// URL-sourced build: route each artifact URL to its canonical env_config
	// field. The host's cvd resolves gs:// and https:// build strings, so
	// these reach it as written.
	if record.Source == build.SourceURL && len(record.ArtifactSpecs) > 0 {
		return routeArtifactURLs(diskConfig, bootConfig, record.ArtifactSpecs, logger), nil
	}

	// Android Build source: construct an @ab/<ref>/<target> identifier.
	target := abTarget(record, d)
	switch {
	case record.BuildID != "":
		diskConfig["default_build"] = build.FetchKey(record.BuildID, target)
	case record.Branch != "":
		diskConfig["default_build"] = build.FetchKey(record.Branch, target)
	default:
		return "", fmt.Errorf("build record %s has no android_build_id or branch", record.ID)
	}
	return "", nil
}

// instanceArtifacts is one instance's "@build", routed into the disk and boot
// sections of that instance alone.
type instanceArtifacts struct {
	disk, boot  map[string]any
	hostPackage string
}

// resolveInstanceBuilds resolves every advanced-mode instance's "@build",
// indexed by instance position, nil where an instance names none. A record
// that is gone at launch fails the create. Admission accepts only registered
// ids, so the record was deleted since, and the device-level fallback of
// reading the id as an Android Build reference would fetch something the
// caller never named.
func (a *Actuator) resolveInstanceBuilds(ctx context.Context, d *device.Device) ([]*instanceArtifacts, error) {
	instances, _ := d.EnvConfig["instances"].([]any)
	var resolved []*instanceArtifacts
	for i, raw := range instances {
		inst, _ := raw.(map[string]any)
		id, _ := inst[validation.BuildKey].(string)
		if id == "" {
			continue
		}
		name, _ := inst["name"].(string)
		record, err := a.builds.Get(ctx, id)
		if errors.Is(err, build.ErrNotFound) {
			return nil, fmt.Errorf("instances[%d] (%q): build %s is no longer registered", i, name, id)
		}
		if err != nil {
			return nil, fmt.Errorf("instances[%d] (%q): looking up build %s: %w", i, name, id, err)
		}
		artifacts := &instanceArtifacts{disk: map[string]any{}, boot: map[string]any{}}
		artifacts.hostPackage, err = routeBuildRecord(record, d, artifacts.disk, artifacts.boot, a.logger)
		if err != nil {
			return nil, fmt.Errorf("instances[%d] (%q): %w", i, name, err)
		}
		if resolved == nil {
			resolved = make([]*instanceArtifacts, len(instances))
		}
		resolved[i] = artifacts
	}
	return resolved, nil
}

// withInstanceBuilds returns a copy of d whose env_config carries each
// instance's "@build" artifacts in that instance's disk and boot sections,
// under the fields the caller wrote, with every "@build" key removed. It runs
// before buildEnvConfig merges the device-level build, whose fill-gaps-only
// merge then leaves these values standing. The precedence is the caller's
// fields, then the instance's build, then the device's build.
//
// The first instance whose build carries a host package supplies
// common.host_package, but only when neither the caller nor the device-level
// build supplies one, since buildEnvConfig keeps a value already present.
func withInstanceBuilds(d *device.Device, resolved []*instanceArtifacts, deviceHostPackage string) *device.Device {
	envConfig := cloneMap(d.EnvConfig)
	instances, _ := envConfig["instances"].([]any)
	instanceHostPackage := ""
	for i, raw := range instances {
		inst, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		delete(inst, validation.BuildKey)
		if i >= len(resolved) || resolved[i] == nil {
			continue
		}
		mergeSection(inst, "disk", resolved[i].disk)
		mergeSection(inst, "boot", resolved[i].boot)
		if instanceHostPackage == "" {
			instanceHostPackage = resolved[i].hostPackage
		}
	}
	if instanceHostPackage != "" && deviceHostPackage == "" {
		if common, ok := mapSection(envConfig, "common"); ok {
			if written, _ := common["host_package"].(string); written == "" {
				common["host_package"] = instanceHostPackage
			}
		}
	}
	withBuilds := *d
	withBuilds.EnvConfig = envConfig
	return &withBuilds
}

// routeArtifactURLs places each URL-sourced artifact into its canonical
// env_config field and returns the host package URL. The canonical config
// (load_config.proto) keys build dimensions structurally: kernel, boot image,
// and bootloader under instances[].boot, the system image under
// instances[].disk.super.system. Its parser rejects unknown fields, so flat
// disk.*_build keys fail the whole load instead of being ignored.
// Unknown roles fall back to default_build.
func routeArtifactURLs(diskConfig, bootConfig map[string]any, specs []build.ArtifactSpec, logger *slog.Logger) string {
	var hostPackageURL string
	for _, spec := range specs {
		switch spec.Role {
		case build.ArtifactRoleHostPackage:
			hostPackageURL = spec.URL
		case build.ArtifactRoleDefaultBuild:
			diskConfig["default_build"] = spec.URL
		case build.ArtifactRoleSystemImageBuild:
			diskConfig["super"] = map[string]any{"system": spec.URL}
		case build.ArtifactRoleKernelBuild:
			bootConfig["kernel"] = map[string]any{"build": spec.URL}
		case build.ArtifactRoleBootBuild:
			bootConfig["build"] = spec.URL
		case build.ArtifactRoleBootloaderBuild:
			bootConfig["bootloader"] = map[string]any{"build": spec.URL}
		default:
			logger.Warn("unknown artifact role, treating as default_build",
				slog.String("role", string(spec.Role)), slog.String("url", spec.URL))
			diskConfig["default_build"] = spec.URL
		}
	}
	return hostPackageURL
}

// simpleInstanceConfig is the one instance a simple-mode create describes.
// The shape comes from the domain constants placement also sizes hosts
// against: a literal here would be a second copy, and the two disagreeing
// means a create asking a host for more than it was provisioned to hold.
//
// The renderer is forwarded in cvd's own vocabulary, via launchGPUMode. The
// snapshot gate depends on that translation. An unrecognized mode string does
// not fail; it falls through SILENTLY to auto resolution (observed live:
// "swiftshader" launched the auto candidate walk, "guest_swiftshader" pinned
// the mode), and auto on a GPU host resolves to an accelerated mode a
// snapshot take destroys, which is exactly what the swiftshader-only
// snapshot gate promises cannot happen.
//
// A swiftshader device additionally disables the crosvm sandbox. It exists
// to be snapshotted, and crosvm cannot suspend a virtio-fs device
// (virtio_sleep is unimplemented there). Current guest builds still enable
// the shared folder, riding the sandbox flag, exactly and only under
// guest_swiftshader. Disabling the sandbox is the one channel the load
// config offers that keeps the fs device off the bus; proven live in both
// directions on one host (sandboxed take fails at suspend, unsandboxed
// take → restore round-trips). Revisit when the load config grows an
// enable_virtiofs field or crosvm learns to sleep virtio-fs.
func simpleInstanceConfig(d *device.Device) map[string]any {
	vmConfig := map[string]any{
		"memory_mb":        device.DefaultInstanceMemoryMB,
		"cpus":             device.DefaultInstanceVCPUs,
		"setupwizard_mode": "OPTIONAL",
	}
	instanceConfig := map[string]any{
		"name": d.Name,
		"vm":   vmConfig,
	}
	if d.GPUMode != "" {
		instanceConfig["graphics"] = map[string]any{"gpu_mode": launchGPUMode(d.GPUMode)}
	}
	if d.GPUMode == device.GPUModeSwift {
		vmConfig["crosvm"] = map[string]any{"enable_sandbox": false}
	}
	return instanceConfig
}

// GuestSwiftshaderMode is cvd's launch name for software rendering, the only
// renderer a snapshot can capture. Exported because the device slice's
// snapshot gates read the same string back out of a stored env_config and a
// live launcher.log, and a literal in each of those places is a copy that can
// drift from what the payload actually ships.
const GuestSwiftshaderMode = "guest_swiftshader"

// launchGPUMode translates the device's GPU mode into cvd's launch
// vocabulary. Only swiftshader differs, since cvd's name for it is
// guest_swiftshader; every other stored value is already canonical.
func launchGPUMode(mode device.GPUMode) string {
	if mode == device.GPUModeSwift {
		return GuestSwiftshaderMode
	}
	return string(mode)
}

// alignInstanceLaunchFlags supplies the two launch fields an advanced-mode
// config can leave out and thereby contradict the device row: the renderer,
// and the crosvm sandbox under guest_swiftshader. Absent means cvd's defaults
// decide. The GPU auto walk picks whatever the host can accelerate, and the
// sandbox default drags a virtio-fs device onto the bus, while the row still
// says swiftshader, which is what the snapshot gate reads and what placement
// sized the host for.
//
// A value the caller wrote always wins, including enable_sandbox=true: this
// fills gaps, it does not overrule an advanced-mode config. The take gate
// refuses the contradictions it cannot fill.
func alignInstanceLaunchFlags(envConfig map[string]any, d *device.Device) {
	instances, ok := envConfig["instances"].([]any)
	if !ok {
		return
	}
	for _, raw := range instances {
		inst, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		mode := instanceGPUMode(inst)
		if mode == string(device.GPUModeSwift) {
			// The console's name for the mode. cvd does not know it and
			// silently resolves it as auto.
			mode = GuestSwiftshaderMode
			graphics, _ := inst["graphics"].(map[string]any)
			graphics["gpu_mode"] = mode
		}
		if mode == "" && d.GPUMode != "" {
			graphics, ok := mapSection(inst, "graphics")
			if !ok {
				continue // graphics is not an object; the load will reject it
			}
			mode = launchGPUMode(d.GPUMode)
			graphics["gpu_mode"] = mode
		}
		if mode != GuestSwiftshaderMode {
			continue
		}
		vm, ok := mapSection(inst, "vm")
		if !ok {
			continue
		}
		crosvm, ok := mapSection(vm, "crosvm")
		if !ok {
			continue
		}
		if _, present := crosvm["enable_sandbox"]; !present {
			crosvm["enable_sandbox"] = false
		}
	}
}

// instanceGPUMode reports the renderer an instance names, or "" when it names
// none.
func instanceGPUMode(inst map[string]any) string {
	graphics, _ := inst["graphics"].(map[string]any)
	mode, _ := graphics["gpu_mode"].(string)
	return mode
}

// mapSection returns the named nested object, creating it when absent. A key
// holding something that is not an object reports false and is left untouched.
func mapSection(parent map[string]any, key string) (map[string]any, bool) {
	if existing, present := parent[key]; present {
		section, ok := existing.(map[string]any)
		return section, ok
	}
	section := make(map[string]any)
	parent[key] = section
	return section, true
}

// buildEnvConfig constructs the CVD environment configuration.
// For advanced mode, it uses the user-provided config with injected group name
// and merges build record disk URLs (user-specified fields take precedence).
// For simple mode, it builds a default config with the instance settings.
//
// The fetch section is left alone. The cvd fetcher's compiled-in
// default (androidbuild-pa.googleapis.com/v4) is the only Build API surface
// its built-in catchall API key is valid for. The key is BLOCKED on the v3
// androidbuildinternal surface (every method 403s API_KEY_SERVICE_BLOCKED),
// so overriding fetch.api_base_url breaks all @ab fetches.
func buildEnvConfig(
	d *device.Device,
	groupName string,
	instanceConfig map[string]any,
	hostPackageURL string,
	diskConfig, bootConfig map[string]any,
) map[string]any {
	if len(d.EnvConfig) > 0 {
		// Advanced mode: use user-provided config. The copy is deep because
		// everything below writes into nested maps. A shallow copy shares
		// them, so injecting group_name would rewrite the stored device row.
		envConfig := cloneMap(d.EnvConfig)

		expandImports(envConfig)
		stripInstanceBuilds(envConfig)
		alignInstanceLaunchFlags(envConfig, d)

		// Inject common.group_name, and the build's host package only where
		// the caller wrote none: an SDV pair takes its host tools from one
		// build and its images from two others.
		if envConfig["common"] == nil {
			envConfig["common"] = make(map[string]any)
		}
		if common, ok := envConfig["common"].(map[string]any); ok {
			common["group_name"] = groupName
			if written, _ := common["host_package"].(string); written == "" && hostPackageURL != "" {
				common["host_package"] = hostPackageURL
			}
		}

		mergeInstanceSection(envConfig, "disk", diskConfig)
		mergeInstanceSection(envConfig, "boot", bootConfig)

		return envConfig
	}

	// Simple mode: build config from defaults.
	common := map[string]any{
		"group_name": groupName,
	}
	if hostPackageURL != "" {
		common["host_package"] = hostPackageURL
	}

	return map[string]any{
		"common":    common,
		"instances": []any{instanceConfig},
	}
}

// cloneMap deep-copies a JSON-shaped map.
func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneValue(v)
	}
	return out
}

// cloneValue deep-copies a JSON-shaped value.
func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneMap(t)
	case []any:
		out := make([]any, len(t))
		for i, elem := range t {
			out[i] = cloneValue(elem)
		}
		return out
	default:
		return v
	}
}

// mergeInstanceSection merges resolved build fields into the named section
// (disk, boot) of every instance of an advanced-mode env_config;
// user-specified fields take precedence.
func mergeInstanceSection(envConfig map[string]any, section string, cfg map[string]any) {
	if len(cfg) == 0 {
		return
	}
	instances, ok := envConfig["instances"].([]any)
	if !ok {
		return
	}
	for _, inst := range instances {
		if instMap, ok := inst.(map[string]any); ok {
			mergeSection(instMap, section, cfg)
		}
	}
}

// mergeSection fills the instance's named section with every key of cfg it
// does not already hold.
func mergeSection(inst map[string]any, section string, cfg map[string]any) {
	if len(cfg) == 0 {
		return
	}
	if inst[section] == nil {
		inst[section] = make(map[string]any)
	}
	sec, ok := inst[section].(map[string]any)
	if !ok {
		return
	}
	for k, v := range cfg {
		if _, exists := sec[k]; !exists {
			sec[k] = v
		}
	}
}

// stripInstanceBuilds removes every instance's "@build". The key is CCO's
// own, and cvd's parser rejects the whole load on a field it does not know.
func stripInstanceBuilds(envConfig map[string]any) {
	instances, _ := envConfig["instances"].([]any)
	for _, raw := range instances {
		if inst, ok := raw.(map[string]any); ok {
			delete(inst, validation.BuildKey)
		}
	}
}

// adbPortFromSerial extracts the TCP port from an HO adb_serial such as
// "0.0.0.0:6520". Returns 0 when the serial is absent or not host:port-shaped.
func adbPortFromSerial(serial string) int64 {
	_, portStr, err := net.SplitHostPort(serial)
	if err != nil {
		return 0
	}
	port, err := strconv.ParseInt(portStr, 10, 64)
	if err != nil {
		return 0
	}
	return port
}
