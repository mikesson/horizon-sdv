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

package devicefeature

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	deviceDomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/features/device/cvd"
)

// Markers assemble_cvd and run_cvd write to launcher.log, transcribed from
// live logs captured on this deployment (host image cco-host 20260802).
// Nothing pins them against upstream's sources, so the live smoke take is what
// catches a rename: it snapshots a real device, and a marker that stopped
// matching fails the take closed rather than passing it silently.
const (
	// assembleProcess opens every assemble_cvd line: followed by a space up to
	// cuttlefish 1.57, and by "(<pid>)" from 1.58. A `cvd start` re-assembles
	// and rewrites the file; a launcher-level restart appends to it without
	// re-assembling, so an assemble run is what starts a segment.
	assembleProcess = "assemble_cvd"

	// gpuAutoSelectionMarker is the auto walk's verdict (graphics_flags.cc:489),
	// written once after the candidate list is filtered.
	gpuAutoSelectionMarker = "GPU auto mode: selecting --gpu_mode="

	// gpuRequirementCheckMarker opens each mode's requirement check
	// (graphics_flags.cc:411). An explicitly requested mode is checked once;
	// the auto walk checks every candidate.
	gpuRequirementCheckMarker = "Checking requirements for --gpu_mode="

	// crosvmSharedDirFlag and virtioFSSharedDirType are how run_cvd logs the
	// device crosvm cannot suspend:
	// --shared-dir=<path>:shared:type=fs:security_ctx=false.
	crosvmSharedDirFlag   = "--shared-dir="
	virtioFSSharedDirType = "type=fs"
)

// errLaunchUnverifiable reports that the log does not say how the device is
// running, either from no launch segment or from GPU evidence that does not
// resolve to one mode.
var errLaunchUnverifiable = errors.New("the log carries no readable launch")

// verifyLaunchIsSnapshottable refuses a take whose CURRENT launch a capture
// would destroy rather than record.
//
// The device row says which renderer was asked for, and the create pins the
// launch to match it, but only that launch. The host's start verb re-assembles
// from cvd's defaults, and an advanced-mode config can leave both fields out,
// so the row is a statement of intent while launcher.log is what is actually
// running. Only the log can refuse the takes that destroyed devices here.
//
// It fails CLOSED. A log that cannot be read or cannot be understood refuses
// the take. An unverifiable launch was admitted twice before and cost the
// device both times, and the failure is retryable prose rather than a dead end.
//
// Admission only. The worker does not re-check. The window between the two is
// bounded by exclusivity, and a launch that changed inside it lands on
// runSnapshotTake's error-mark recovery, which is what a destroyed device needs
// anyway. It also costs a `cvd fleet` probe on the host per fetch (the logs
// route spawns one before serving a byte), which the rare, explicitly-requested
// take can afford and a per-attempt re-check could not.
func (s *DeviceService) verifyLaunchIsSnapshottable(ctx context.Context, hoURL, group, name string) error {
	log, err := s.hoClientFactory.ClientForHost(hoURL).FetchCVDLogFile(ctx, group, name, deviceDomain.LauncherLogFile)
	if err != nil {
		return &deviceDomain.ValidationError{Message: fmt.Sprintf(
			"cannot confirm how this device is currently running: reading %s from its host failed (%v). "+
				"The snapshot is refused rather than risk destroying the device, so retry it",
			deviceDomain.LauncherLogFile, err)}
	}
	blocker, err := launchSnapshotBlocker(log)
	if err != nil {
		return &deviceDomain.ValidationError{Message: fmt.Sprintf(
			"cannot confirm how this device is currently running: %s does not record a launch this can read (%v). "+
				"The snapshot is refused rather than risk destroying the device. Retry it, "+
				"and stop and start the device if it keeps failing",
			deviceDomain.LauncherLogFile, err)}
	}
	if blocker != "" {
		return &deviceDomain.ValidationError{Message: fmt.Sprintf(
			"the launch this device is currently running cannot be captured: %s. "+
				"Taking the snapshot would kill the guest instead of saving it. "+
				"Stop and start the device to re-assemble its launch, then retry",
			blocker)}
	}
	return nil
}

// launchSnapshotBlocker reports why the launch a launcher.log describes cannot
// be snapshotted, or "" when it can. It reads the LAST launch segment. The file
// survives a launcher-level restart by being appended to, and only the most
// recent assemble decides what is on the bus now.
func launchSnapshotBlocker(log []byte) (string, error) {
	segment, ok := lastLaunchSegment(log)
	if !ok {
		return "", errLaunchUnverifiable
	}

	var (
		sharedFS bool
		selected string
		checked  []string
	)
	for line := range bytes.Lines(segment) {
		if dir, ok := markerValue(line, crosvmSharedDirFlag); ok && isVirtioFSShare(dir) {
			sharedFS = true
		}
		if mode, ok := markerValue(line, gpuAutoSelectionMarker); ok {
			selected = mode
		}
		if mode, ok := markerValue(line, gpuRequirementCheckMarker); ok && !slices.Contains(checked, mode) {
			checked = append(checked, mode)
		}
	}

	// virtio-fs comes first. It is the blocker a launch can carry while its
	// renderer looks fine, which is exactly what a defaults relaunch produces.
	if sharedFS {
		return "a virtio-fs shared directory is on its crosvm bus, and crosvm cannot suspend one", nil
	}
	mode, ok := launchedGPUMode(selected, checked)
	if !ok {
		return "", errLaunchUnverifiable
	}
	if mode != cvd.GuestSwiftshaderMode {
		return fmt.Sprintf("it renders with --gpu_mode=%s, and crosvm dies snapshotting an accelerated GPU device", mode), nil
	}
	return "", nil
}

// lastLaunchSegment returns the log from the start of its most recent
// assemble_cvd run. A file with no assemble run at all describes no launch.
func lastLaunchSegment(log []byte) ([]byte, bool) {
	start, offset, inAssemble := -1, 0, false
	for line := range bytes.Lines(log) {
		assembling := isAssembleLine(line)
		if assembling && !inAssemble {
			start = offset
		}
		inAssemble = assembling
		offset += len(line)
	}
	if start < 0 {
		return nil, false
	}
	return log[start:], true
}

// isAssembleLine reports whether line was written by assemble_cvd, in either
// log format. The byte after the name must end it, so a process whose name
// only starts with assemble_cvd does not count.
func isAssembleLine(line []byte) bool {
	rest, ok := bytes.CutPrefix(line, []byte(assembleProcess))
	return ok && len(rest) > 0 && (rest[0] == ' ' || rest[0] == '(')
}

// launchedGPUMode reports the renderer the launch settled on. The auto walk's
// selection wins when it ran, because the modes it checked on the way are
// candidates rather than decisions; without it a single checked mode is the one
// the launch asked for. Anything else (no evidence, or a candidate walk whose
// selection is missing) is not a verdict this can read.
func launchedGPUMode(selected string, checked []string) (string, bool) {
	if selected != "" {
		return selected, true
	}
	if len(checked) == 1 {
		return checked[0], true
	}
	return "", false
}

// isVirtioFSShare reports whether a --shared-dir value mounts virtio-fs; the
// value is colon-separated (path:tag:type=fs:security_ctx=false) and the other
// share types suspend fine.
func isVirtioFSShare(dir string) bool {
	return slices.Contains(strings.Split(dir, ":"), virtioFSSharedDirType)
}

// markerValue returns the whitespace-delimited value following marker on a log
// line.
func markerValue(line []byte, marker string) (string, bool) {
	_, after, ok := bytes.Cut(line, []byte(marker))
	if !ok {
		return "", false
	}
	fields := bytes.Fields(after)
	if len(fields) == 0 {
		return "", false
	}
	return string(fields[0]), true
}

// snapshotSafeEnvConfig refuses a take on an advanced-mode device whose stored
// config explicitly asks for a launch no capture survives. The payload fills
// these fields in when the config leaves them out, so what reaches here is a
// choice the caller made, and the answer is to name it. The device has to be
// recreated without it.
//
// Local and free, so it runs before the log fetch, which is neither.
func snapshotSafeEnvConfig(envConfig map[string]any) error {
	instances, ok := envConfig["instances"].([]any)
	if !ok {
		return nil
	}
	for i, raw := range instances {
		inst, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		vm, _ := inst["vm"].(map[string]any)
		crosvm, _ := vm["crosvm"].(map[string]any)
		if sandbox, ok := crosvm["enable_sandbox"].(bool); ok && sandbox {
			return &deviceDomain.ValidationError{Message: fmt.Sprintf(
				"instance %d of this device's env_config sets vm.crosvm.enable_sandbox true, "+
					"which puts a virtio-fs device on the crosvm bus that crosvm cannot suspend, so "+
					"recreate the device without it to snapshot it", i)}
		}

		graphics, _ := inst["graphics"].(map[string]any)
		mode, _ := graphics["gpu_mode"].(string)
		switch mode {
		// The payload launches a stored "swiftshader" as guest_swiftshader,
		// so it is the same software renderer.
		case "", "swiftshader", cvd.GuestSwiftshaderMode:
		default:
			return &deviceDomain.ValidationError{Message: fmt.Sprintf(
				"instance %d of this device's env_config sets graphics.gpu_mode %q; crosvm dies snapshotting an "+
					"accelerated GPU device, so only %q can be captured. Recreate the device with that mode",
				i, mode, cvd.GuestSwiftshaderMode)}
		}
	}
	return nil
}
