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
	"slices"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// Boot-watch pacing. The dwell keeps the fetch cost off the healthy path. A
// livesmoke create is ~3 minutes end to end, so a healthy device never
// reaches it. The interval budgets the host-side cost explicitly. The HO's
// logs route runs `cvd fleet` before serving a byte, so every enriched sample
// spawns one live probe per file against the very launcher being diagnosed.
const (
	bootEnrichDwell    = device.TransientProgressWindow
	bootEnrichInterval = time.Minute
)

// bootWatchTick advances the device's boot-progress observation by one
// sample and reports whether it settled the row to error. Two feed points in
// the create window reach it: the load-wait sampler on every probe tick, and
// the launch-in-flight postpone path once per redelivery. That postpone path
// is otherwise observation-free for up to launchWaitCap, which is where a
// hung looping launcher sits.
//
// members is the group as the SAME probe listed it, and it carries both facts
// this tick needs. The instance NAMES are the log paths. An advanced create
// names its own instances, so the device row's name addresses a logs directory
// the host does not have, and the watch would 404 its way through exactly the
// creates most likely to be booting badly. The instance STATES fold into the
// enum recorded beside the observation, one sample, so the derivation never
// reads a fresh enum against a stale count, and a group is never written
// Running on its first member's word while a sibling is still coming up.
//
// transitions is the sampler's accumulated load-status trail, nil at feed
// points with no sampler state (the postpone path), which then carry the
// row's persisted trail forward untouched.
//
// Every failure path degrades to an un-enriched observation, which is today's
// behavior by construction.
func (a *Actuator) bootWatchTick(ctx context.Context, client hoclient.Client, d *device.Device, groupName string, members []*hoclient.CVD, transitions []device.BootTransition) bool {
	now := time.Now()
	cvdStatus := device.FoldInstanceStates(instancesFromCVDs(members))
	prev := d.BootObservation
	if prev == nil || prev.TransientSince == nil {
		// First sample, so start the dwell clock. updated_at cannot serve as
		// one, because every observation write would reset it. The observation
		// carries its own entry timestamp instead.
		obs := &device.BootObservation{TransientSince: &now, ObservedAt: now, Transitions: slices.Clone(transitions)}
		if err := a.devices.RecordBootObservation(ctx, d.HostID, d.ID, cvdStatus, obs); err != nil {
			a.logger.Warn("failed to start boot-watch dwell clock",
				slog.String("device_id", d.ID), slog.Any("error", err))
			return false
		}
		d.BootObservation = obs
		return false
	}
	// The persisted dwell clock makes resume-entry evaluation automatic: a
	// create resumed after MaxWait re-enters with TransientSince already old,
	// so the first tick here enriches instead of waiting out a second dwell.
	if now.Sub(*prev.TransientSince) < bootEnrichDwell || now.Sub(prev.ObservedAt) < bootEnrichInterval {
		// A healthy create finishes inside the dwell, so a grown trail must
		// be persisted here or the fetch/boot split never becomes durable for
		// exactly the creates worth measuring. The trail rides the previous
		// sample unchanged, so the dwell clock and the enrichment pacing are
		// both unaffected.
		if len(transitions) > len(prev.Transitions) {
			obs := *prev
			obs.Transitions = slices.Clone(transitions)
			if err := a.devices.RecordBootObservation(ctx, d.HostID, d.ID, cvdStatus, &obs); err != nil {
				a.logger.Warn("failed to record load-status transition",
					slog.String("device_id", d.ID), slog.Any("error", err))
				return false
			}
			d.BootObservation = &obs
		}
		return false
	}

	cur := sampleGroupBoot(ctx, client, groupName, members, prev, now)
	if cur == nil {
		return false // nothing fetched, nothing enriched, so the wait continues as today
	}
	cur.TransientSince = prev.TransientSince
	cur.Transitions = prev.Transitions
	if transitions != nil {
		cur.Transitions = slices.Clone(transitions)
	}
	if err := a.devices.RecordBootObservation(ctx, d.HostID, d.ID, cvdStatus, cur); err != nil {
		a.logger.Warn("failed to record boot observation",
			slog.String("device_id", d.ID), slog.Any("error", err))
	}
	d.BootObservation = cur

	if !cur.Verdict.Terminal() {
		return false
	}
	return a.settleBootVerdict(ctx, d, cur, groupName, members[0].Name)
}

// sampleGroupBoot takes one boot sample per instance the host listed and
// returns the one that speaks for the group. It returns nil when not a single
// instance answered, and never fabricates a sample.
//
// Every instance is sampled, because cvd resets the whole group when one
// instance fails to boot. A loop in the third instance is the device's loop,
// and reading only the first would be blind to it. Only the FIRST instance's
// sample rides, though, because log growth is a rate against the previously
// persisted sample and measures nothing across two different files. The
// others carry their verdict alone, terminal or not at all. Counts over
// threshold and the guest's own failure marker are single-sample facts that
// need no baseline. A terminal verdict wins immediately and brings its own
// panic line as the evidence.
func sampleGroupBoot(ctx context.Context, client hoclient.Client, group string, members []*hoclient.CVD, prev *device.BootObservation, now time.Time) *device.BootObservation {
	var riding *device.BootObservation
	for i, m := range members {
		var base *device.BootObservation
		if i == 0 {
			base = prev
		}
		kernelTail, kernelSize, kernelErr := client.GetCVDLogTail(ctx, group, m.Name, device.KernelLogFile, device.BootLogTailBytes)
		launcherTail, _, launcherErr := client.GetCVDLogTail(ctx, group, m.Name, device.LauncherLogFile, device.BootLogTailBytes)
		if kernelErr != nil && launcherErr != nil {
			continue
		}
		cur := device.CountBootMarkers(kernelTail, launcherTail)
		cur.KernelLogBytes = kernelSize
		// Stamped before the evaluation, not after: the growth rate is measured
		// against prev.ObservedAt and reads a zero time as no interval at all.
		cur.ObservedAt = now
		cur.Verdict = device.EvaluateBoot(base, cur)
		if cur.Verdict.Terminal() {
			return &cur
		}
		if i == 0 {
			riding = &cur
		}
	}
	return riding
}

// errBootSettleSuperseded aborts a verdict settle that lost to teardown or an
// earlier terminal writer. In both cases the device is no longer this
// settle's to conclude.
var errBootSettleSuperseded = errors.New("boot verdict superseded")

// settleBootVerdict marks the row error with the observation's evidence and
// stamps the deterministic group name, so capture-on-error has a group to
// archive. CVDID is otherwise recorded only on success, and the capture
// handler's "no CVD to capture from" arm would dedup-stamp the row without
// capturing anything, which once lost a boot loop's evidence.
//
// instanceName is the FIRST instance the host listed, never the device row's
// name. The read rule synthesizes an error row's instance array by parsing this
// id, so a device name the group never used would put an instance nothing can
// address on the row that most needs looking at.
//
// It touches NOTHING host-side. The group's own logs are the whole diagnosis
// (the count without the panic line is not actionable), nothing upstream ever
// stops a looping guest, and deleting a group under a live launcher leaks its
// processes. The cost of leaving it is one device slot and log growth on a
// billed-anyway VM. The device TTL reaps it, which is why the message names
// the row's expiry. The DEVICE TTL bounds the evidence, not the host's.
func (a *Actuator) settleBootVerdict(ctx context.Context, d *device.Device, obs *device.BootObservation, groupName, instanceName string) bool {
	msg := d.BootLoopEvidence()
	_, err := a.devices.Mutate(ctx, d.ID, func(cur *device.Device) error {
		if cur.Status == device.StatusDeleting || cur.Status == device.StatusDeleted ||
			cur.Status == device.StatusError {
			return errBootSettleSuperseded
		}
		if cur.CVDID == "" {
			cur.CVDID = groupName + "/" + instanceName
		}
		cur.BootObservation = obs
		cur.ErrorMessage = msg
		return cur.TransitionTo(device.StatusError)
	})
	switch {
	case errors.Is(err, errBootSettleSuperseded), errors.Is(err, device.ErrNotFound):
		return false
	case err != nil:
		a.logger.Warn("failed to settle boot verdict",
			slog.String("device_id", d.ID), slog.Any("error", err))
		return false
	}
	a.logger.Warn("boot verdict settled device to error",
		slog.String("device_id", d.ID),
		slog.String("group_name", groupName),
		slog.String("verdict", string(obs.Verdict)),
		slog.Int("kernel_panics", obs.KernelPanics),
		slog.Int("guest_reboots", obs.GuestReboots),
		slog.String("last_panic", obs.LastPanic))
	a.recordLoopAttribution(ctx, d)
	return true
}

// recordLoopAttribution decides host-versus-build attribution from data the
// system already holds. Every terminal verdict counts, whether a loop, the
// guest's own failure marker or the launcher's boot timeout. Distinct builds
// failing to boot on one host indict the HOST. An AMX-class host loops every build placed on it, and without a degradation
// signal placement keeps feeding it devices forever. One build looping,
// possibly across hosts, indicts the BUILD and must not poison the host. A
// single sample stays with the error row alone. Loop evidence cannot match
// the create-path poison signatures, so without this call site nothing would
// ever degrade such a host.
func (a *Actuator) recordLoopAttribution(ctx context.Context, d *device.Device) {
	rows, err := a.devices.ListByHost(ctx, d.HostID)
	if err != nil {
		a.logger.Warn("loop attribution could not list the host's devices",
			slog.String("host_id", d.HostID), slog.Any("error", err))
		return
	}
	builds := make(map[string]bool)
	for _, row := range rows {
		if row.Status == device.StatusError && row.BootObservation != nil &&
			row.BootObservation.Verdict.Terminal() {
			builds[row.BuildID] = true
		}
	}
	if len(builds) < 2 {
		return
	}
	reason := fmt.Sprintf("boot failures across %d distinct builds", len(builds))
	newlyDegraded, err := a.hosts.RecordCreateFailure(ctx, d.HostID, reason)
	if err != nil {
		a.logger.Warn("failed to record boot-failure host attribution",
			slog.String("host_id", d.HostID), slog.Any("error", err))
		return
	}
	if newlyDegraded {
		a.logger.Warn("host degraded: boot failures across distinct builds",
			slog.String("host_id", d.HostID), slog.Int("distinct_builds", len(builds)))
	}
}
