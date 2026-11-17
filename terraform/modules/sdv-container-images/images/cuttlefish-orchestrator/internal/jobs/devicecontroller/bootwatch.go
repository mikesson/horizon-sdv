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

package devicecontroller

import (
	"context"
	"slices"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// Boot-watch pacing for the health window. The trigger is independent of the
// CVD enum by design. A post-create boot loop latches the fleet status at
// Running (run_cvd answers the status probe with an unconditional success
// however the guest behaves), so nothing enum-shaped can ever fire. The log
// fetch itself is the trigger, budgeted because the HO's logs route spawns
// `cvd fleet` before serving a byte. One tail GET per instance per interval,
// and the second file only when the first shows suspicion.
const (
	// bootSettleWindow keeps the device controller out of the boot window: a
	// device running less than this had its boot watched by the create
	// sampler, and enriching mid-boot would read healthy boot-rate log
	// growth as suspicion.
	bootSettleWindow = device.TransientProgressWindow
	// bootProbeInterval is the per-device enrichment cadence.
	bootProbeInterval = 5 * time.Minute
)

// enrichBootObservation takes one budgeted boot-progress sample for a settled
// running device. It returns nil when this cycle owes none (wrong state,
// inside the settle window or the cadence, or a failed fetch). Every miss
// degrades to an un-enriched cycle, which is today's behavior. The caller
// attaches the sample to the device and its HealthUpdate, so observation and
// enum ride one mutation. DerivePhase then settles a terminal verdict through
// the ordinary derivation path, deferred under live verbs exactly as enum
// settles are.
//
// The logs are addressed by PERSISTED INSTANCE NAME: an advanced-mode create
// names its own instances, so the device row's name is a path the host has no
// logs for, and the watch would 404 its way through exactly the creates most
// likely to be booting badly.
//
// Every instance is sampled, because upstream resets the whole group when one
// instance fails to boot: a loop in the third instance is the device's loop,
// and reading only the first would be blind to it. Only the first instance's
// sample rides, because log growth is a rate against the previously persisted
// sample and means nothing measured across two different files. The others
// contribute their verdict alone, terminal or not at all. Counts over
// threshold and the guest's own failure marker are single-sample facts that
// need no baseline. A terminal one wins immediately and carries its own panic
// line as the evidence.
func (s *Service) enrichBootObservation(ctx context.Context, client hoclient.Client, d *device.Device) *device.BootObservation {
	if d.Status != device.StatusRunning || d.CVDID == "" {
		return nil
	}
	if d.StartedAt == nil || time.Since(*d.StartedAt) < bootSettleWindow {
		return nil
	}
	prev := d.BootObservation
	if prev != nil && time.Since(prev.ObservedAt) < bootProbeInterval {
		return nil
	}
	group, _ := device.ParseCVDID(d.CVDID)
	if group == "" {
		return nil
	}

	// The device controller writes the observation whole, so a sample without the
	// create's load-status trail would erase it.
	var trail []device.BootTransition
	if prev != nil {
		trail = prev.Transitions
	}

	var riding *device.BootObservation
	for i, inst := range d.Instances {
		var base *device.BootObservation
		if i == 0 {
			base = prev
		}
		cur := sampleInstanceBoot(ctx, client, group, inst.Name, base, trail)
		if cur == nil {
			continue
		}
		if cur.Verdict.Terminal() {
			return cur
		}
		if i == 0 {
			riding = cur
		}
	}
	return riding
}

// sampleInstanceBoot takes one instance's boot sample, evaluated against prev
// (nil for an instance with no comparable predecessor, which stands the growth
// rule down). The sample carries trail, the device's load-status transitions,
// whichever instance it came from. It returns nil when the kernel tail could
// not be fetched, so that instance contributes nothing and no sample is
// fabricated.
func sampleInstanceBoot(ctx context.Context, client hoclient.Client, group, name string, prev *device.BootObservation, trail []device.BootTransition) *device.BootObservation {
	kernelTail, size, err := client.GetCVDLogTail(ctx, group, name, device.KernelLogFile, device.BootLogTailBytes)
	if err != nil {
		return nil
	}
	cur := device.CountBootMarkers(kernelTail, nil)
	cur.KernelLogBytes = size
	cur.ObservedAt = time.Now()

	// Suspicion pays for the second fetch: reboot detections live in
	// launcher.log, and a quiescent healthy device never needs them. Fresh
	// attempt markers with no trailing completion is the loop shape.
	if !cur.BootCompleted && (cur.KernelPanics > 0 || cur.UBootStarts > 0) {
		if launcherTail, _, lerr := client.GetCVDLogTail(ctx, group, name, device.LauncherLogFile, device.BootLogTailBytes); lerr == nil {
			counted := device.CountBootMarkers(kernelTail, launcherTail)
			counted.KernelLogBytes = cur.KernelLogBytes
			counted.ObservedAt = cur.ObservedAt
			cur = counted
		}
	}
	cur.Transitions = slices.Clone(trail)
	cur.Verdict = device.EvaluateBoot(prev, cur)
	return &cur
}
