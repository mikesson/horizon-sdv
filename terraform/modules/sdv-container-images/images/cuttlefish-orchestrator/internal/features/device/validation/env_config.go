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

// Package validation provides validation and filtering for CVD configurations.
package validation

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// MaxInstances is the maximum number of CVD instances allowed per device.
const MaxInstances = 8

// MaxDisplays is the maximum number of displays allowed per instance.
const MaxDisplays = 4

// importKey is the canonical config's per-instance template selector.
// load_config.proto declares it as import_template with json_name "@import".
const importKey = "@import"

// BuildKey is the CCO-only per-instance build selector. Its value is the id of
// a registered CCO build whose artifacts fill that instance. Payload
// construction strips it before the host sees the config, so cvd never parses
// it.
const BuildKey = "@build"

// importTemplateNames are the launch templates cvd's parser resolves, in its
// declaration order. The template bodies live beside the expansion in the cvd
// package, which asserts that the two agree.
var importTemplateNames = []string{"phone", "tablet", "tv", "wearable", "auto", "slim", "go", "foldable"}

// ImportTemplateNames returns the @import template names an instance may select.
func ImportTemplateNames() []string {
	return slices.Clone(importTemplateNames)
}

// blockedField is a config field users may not set, with how it is detected.
// Setting these allows arbitrary file access or code execution, so they are
// rejected by ValidateEnvConfig and stripped by FilterBlockedFields.
type blockedField struct {
	path   string // dotted path, relative to the instance or common map
	reason string
	// byPresence blocks the field whenever it is set; otherwise it is blocked
	// only when its value is a string holding a local filesystem path.
	byPresence bool
}

const reasonLocalPath = "local paths not allowed"

var (
	blockedInstanceFields = []blockedField{
		{path: "boot.kernel.build", reason: reasonLocalPath},
		{path: "boot.bootloader.build", reason: reasonLocalPath},
		{path: "boot.android_efi_loader.build", reason: reasonLocalPath},
		{path: "disk.otatools", reason: reasonLocalPath},
		{path: "vm.crosvm.v4l2_proxy", reason: "device access not allowed", byPresence: true},
	}
	blockedCommonFields = []blockedField{
		{path: "host_package", reason: reasonLocalPath},
	}

	// The canonical config's build-string fields. cvd rejects cleartext
	// http:// sources (boot images over cleartext invite tampering);
	// rejecting them here turns that into a 400 instead of an opaque
	// host-side load failure.
	instanceBuildStringFields = []string{
		"disk.default_build",
		"disk.super.system",
		"disk.otatools",
		"boot.build",
		"boot.kernel.build",
		"boot.bootloader.build",
		"boot.android_efi_loader.build",
	}
	commonBuildStringFields = []string{"host_package"}
)

// ValidateEnvConfig validates the env_config structure.
// It checks for required fields, validates array sizes, and ensures
// no blocked fields contain local paths.
func ValidateEnvConfig(config map[string]any) error {
	if config == nil {
		return errors.New("env_config cannot be nil")
	}

	// Check instances exist and count is valid
	instances, ok := config["instances"]
	if !ok {
		return errors.New("env_config must have an 'instances' field")
	}

	instancesSlice, ok := instances.([]any)
	if !ok {
		return errors.New("env_config.instances must be an array")
	}

	if len(instancesSlice) == 0 {
		return errors.New("env_config must have at least one instance")
	}

	if len(instancesSlice) > MaxInstances {
		return fmt.Errorf("env_config cannot have more than %d instances (got %d)", MaxInstances, len(instancesSlice))
	}

	// Validate each instance
	names := make(map[string]int, len(instancesSlice))
	for i, inst := range instancesSlice {
		instMap, ok := inst.(map[string]any)
		if !ok {
			return fmt.Errorf("env_config.instances[%d] must be an object", i)
		}
		if err := validateInstance(i, instMap, names); err != nil {
			return err
		}
	}

	// Check common section for blocked fields
	if common, ok := config["common"].(map[string]any); ok {
		if err := checkBlockedFields("common.", common, blockedCommonFields); err != nil {
			return err
		}
		if err := checkCleartextBuildStrings("common.", common, commonBuildStringFields); err != nil {
			return err
		}
	}

	return nil
}

// validateInstance applies every per-instance rule to one entry of the
// instances array. names carries the names already seen, so uniqueness is
// decided across the whole array rather than within one entry.
func validateInstance(index int, inst map[string]any, names map[string]int) error {
	if err := validateInstanceName(index, inst, names); err != nil {
		return err
	}
	prefix := fmt.Sprintf("instances[%d].", index)
	// Check for blocked fields with local paths
	if err := checkBlockedFields(prefix, inst, blockedInstanceFields); err != nil {
		return err
	}
	if err := checkCleartextBuildStrings(prefix, inst, instanceBuildStringFields); err != nil {
		return err
	}
	// Validate displays count
	if err := validateDisplays(index, inst); err != nil {
		return err
	}
	if err := validateBuildRef(index, inst); err != nil {
		return err
	}
	return validateImport(index, inst)
}

// checkCleartextBuildStrings returns an error for the first build-string
// field under root carrying a cleartext http:// URL.
func checkCleartextBuildStrings(prefix string, root map[string]any, paths []string) error {
	for _, path := range paths {
		parent, key, ok := walkPath(root, path)
		if !ok {
			continue
		}
		if s, isStr := parent[key].(string); isStr && strings.HasPrefix(s, "http://") {
			return fmt.Errorf("%s%s: cleartext http:// build sources are not supported; use https://", prefix, path)
		}
	}
	return nil
}

// walkPath descends root following all but the last segment of a dotted path
// and returns the map holding the final segment, that segment's key, and whether
// the descent reached a map at every intermediate step.
func walkPath(root map[string]any, path string) (map[string]any, string, bool) {
	segs := strings.Split(path, ".")
	m := root
	for _, s := range segs[:len(segs)-1] {
		next, ok := m[s].(map[string]any)
		if !ok {
			return nil, "", false
		}
		m = next
	}
	return m, segs[len(segs)-1], true
}

// locateBlocked walks to blocked field f under root and reports the map holding
// it, the field key, and whether the field is present and disallowed.
func locateBlocked(root map[string]any, f blockedField) (map[string]any, string, bool) {
	parent, key, ok := walkPath(root, f.path)
	if !ok {
		return nil, "", false
	}
	val, present := parent[key]
	if !present {
		return nil, "", false
	}
	if f.byPresence {
		return parent, key, true
	}
	s, isStr := val.(string)
	return parent, key, isStr && isLocalPath(s)
}

// checkBlockedFields returns an error for the first blocked field set under root.
// prefix is prepended to the field path in error messages (e.g. "instances[0].").
func checkBlockedFields(prefix string, root map[string]any, fields []blockedField) error {
	for _, f := range fields {
		if _, _, blocked := locateBlocked(root, f); blocked {
			return fmt.Errorf("%s%s: %s", prefix, f.path, f.reason)
		}
	}
	return nil
}

// FilterBlockedInstanceFields deletes every blocked field set in one instance
// map, in place. Server-side @import expansion runs its output through this so
// an expanded template cannot introduce a field admission would have rejected.
func FilterBlockedInstanceFields(inst map[string]any) {
	filterBlockedFields(inst, blockedInstanceFields)
}

// filterBlockedFields deletes every blocked field set under root.
func filterBlockedFields(root map[string]any, fields []blockedField) {
	for _, f := range fields {
		if parent, key, blocked := locateBlocked(root, f); blocked {
			delete(parent, key)
		}
	}
}

// validateDisplays checks that display count is within limits.
func validateDisplays(index int, inst map[string]any) error {
	graphics, ok := inst["graphics"].(map[string]any)
	if !ok {
		return nil
	}

	displays, ok := graphics["displays"].([]any)
	if !ok {
		return nil
	}

	if len(displays) > MaxDisplays {
		return fmt.Errorf("instances[%d].graphics.displays: cannot have more than %d displays (got %d)",
			index, MaxDisplays, len(displays))
	}

	return nil
}

// instanceNamePiece is one hyphen-separated segment of an instance name. cvd
// splits the name on "-" and requires every segment to match this, so "a--b",
// "-a" and "a-" are refused while "my_phone07" and "ivi-cluster" are not
// (instances/device_name.cpp, IsValidInstanceName). The comment above that
// function states a stricter expression than the function applies, and would
// reject its own example, so this follows the code.
var instanceNamePiece = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// validateInstanceName checks one caller-chosen instance name against cvd's
// grammar and against the names already seen in this config, recording it in
// names.
//
// The grammar is upstream's rather than CCO's device-name rule, and honoring
// it exactly is also what keeps a name safe to put in a URL path: these names
// address instances on CCO's own routes, so one needing escaping would surface
// as a 404 indistinguishable from an instance that does not exist. Uniqueness
// is checked here for the same reason. Two instances answering to one path
// segment make the route ambiguous, which is harder to diagnose than a wrong
// route.
//
// An absent or empty name is left alone: cvd generates one, and the create
// records whatever it chose.
func validateInstanceName(index int, inst map[string]any, names map[string]int) error {
	value, present := inst["name"]
	if !present {
		return nil
	}
	name, ok := value.(string)
	if !ok {
		return fmt.Errorf("instances[%d].name must be a string", index)
	}
	if name == "" {
		return nil
	}
	for piece := range strings.SplitSeq(name, "-") {
		if !instanceNamePiece.MatchString(piece) {
			return fmt.Errorf("instances[%d].name: %q is not a valid instance name "+
				"(letters, digits and underscores, joined by single hyphens)", index, name)
		}
	}
	if first, duplicate := names[name]; duplicate {
		return fmt.Errorf("instances[%d].name: %q is already used by instances[%d]; "+
			"instance names must be unique within a device", index, name, first)
	}
	names[name] = index
	return nil
}

// validateImport checks the instance's @import template selector against the
// names cvd resolves. The key is accepted rather than rejected, because
// callers send it as declared provenance, and payload construction expands it
// and strips it before the host sees the config.
func validateImport(index int, inst map[string]any) error {
	selector, present := inst[importKey]
	if !present {
		return nil
	}
	name, ok := selector.(string)
	if !ok {
		return fmt.Errorf("instances[%d].%s must be a string", index, importKey)
	}
	if !slices.Contains(importTemplateNames, name) {
		return fmt.Errorf("instances[%d].%s: unknown template %q (want one of %s)",
			index, importKey, name, strings.Join(importTemplateNames, ", "))
	}
	return nil
}

// validateBuildRef checks the instance's @build selector. Only the shape is
// checked here; whether the id names a registered build is a store read, which
// the device service makes before anything commits. A raw URL is not refused
// here either: it fails that lookup like any other unknown id.
func validateBuildRef(index int, inst map[string]any) error {
	value, present := inst[BuildKey]
	if !present {
		return nil
	}
	id, ok := value.(string)
	if !ok || id == "" {
		return fmt.Errorf("instances[%d].%s must be a non-empty build id", index, BuildKey)
	}
	if disk, ok := inst["disk"].(map[string]any); ok {
		if _, set := disk["default_build"]; set {
			return fmt.Errorf("instances[%d]: set %s or disk.default_build, not both", index, BuildKey)
		}
	}
	return nil
}

// isLocalPath returns true if the path appears to be a local filesystem path
// rather than an @ab/ reference or URL.
func isLocalPath(path string) bool {
	if path == "" {
		return false
	}
	// @ab/ references are allowed (Android Build references)
	if strings.HasPrefix(path, "@ab/") {
		return false
	}
	// URLs are allowed
	if strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "gs://") {
		return false
	}
	// Paths starting with / are local paths
	if strings.HasPrefix(path, "/") {
		return true
	}
	// Relative paths are also local
	if strings.HasPrefix(path, "./") || strings.HasPrefix(path, "../") {
		return true
	}
	return false
}

// FilterBlockedFields returns a deep copy of the config with blocked fields removed.
// This is a defense-in-depth measure in case validation is bypassed.
func FilterBlockedFields(config map[string]any) map[string]any {
	if config == nil {
		return nil
	}

	// Deep copy the config
	filtered := deepCopy(config)

	// Filter instances
	if instances, ok := filtered["instances"].([]any); ok {
		for _, inst := range instances {
			if instMap, ok := inst.(map[string]any); ok {
				filterBlockedFields(instMap, blockedInstanceFields)
			}
		}
	}

	// Filter common section
	if common, ok := filtered["common"].(map[string]any); ok {
		filterBlockedFields(common, blockedCommonFields)
	}

	return filtered
}

// deepCopy creates a deep copy of a map by marshaling and unmarshaling JSON.
// This is safe for our use case since config is always JSON-compatible.
func deepCopy(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}

	data, err := json.Marshal(m)
	if err != nil {
		return nil
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}

	return result
}

// CountInstances returns the number of instances in the config.
func CountInstances(config map[string]any) int {
	if config == nil {
		return 0
	}
	instances, ok := config["instances"].([]any)
	if !ok {
		return 0
	}
	return len(instances)
}

// EveryInstanceHasBuild reports whether every instance in the env_config
// names its own build, through a non-empty disk.default_build or an @build
// selector. Only then can an advanced-mode create with no device-level build
// resolve a system image for each member of the group. A config with no
// instances answers false.
func EveryInstanceHasBuild(config map[string]any) bool {
	instances, _ := config["instances"].([]any)
	if len(instances) == 0 {
		return false
	}
	for _, raw := range instances {
		inst, _ := raw.(map[string]any)
		if !instanceHasBuild(inst) {
			return false
		}
	}
	return true
}

// instanceHasBuild reports whether one instance names its own build.
func instanceHasBuild(inst map[string]any) bool {
	if id, _ := inst[BuildKey].(string); id != "" {
		return true
	}
	disk, _ := inst["disk"].(map[string]any)
	db, _ := disk["default_build"].(string)
	return db != ""
}

// InstanceBuildRefs returns each instance's @build id, indexed like the
// instances array, with an empty string for an instance that sets none. It
// returns nil when no instance sets one.
func InstanceBuildRefs(config map[string]any) []string {
	instances, _ := config["instances"].([]any)
	var refs []string
	for i, raw := range instances {
		inst, _ := raw.(map[string]any)
		id, _ := inst[BuildKey].(string)
		if id == "" {
			continue
		}
		if refs == nil {
			refs = make([]string, len(instances))
		}
		refs[i] = id
	}
	return refs
}
