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
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// listHostsToProbe returns the hosts a health cycle visits: running (device +
// CVD census) and starting (probe the Host Orchestrator to promote it to
// running). Two queries because ListFilter carries a single status; either
// failing is logged and the cycle proceeds with whatever it got.
func (s *Service) listHostsToProbe(ctx context.Context) []*host.Host {
	var out []*host.Host
	for _, st := range []host.Status{host.StatusRunning, host.StatusStarting} {
		hosts, err := s.hostRepo.List(ctx, host.ListFilter{Status: &st})
		if err != nil {
			s.logger.Error("failed to list hosts for health cycle",
				slog.String("status", string(st)), slog.Any("error", err))
			continue
		}
		out = append(out, hosts...)
	}
	return out
}

// visitHost runs one host's check, converting a panic into a logged error so a
// single malformed row degrades that host's cycle instead of killing the
// process, which also serves the API. This mirrors the task worker's handler
// recovery and exists for the same reason.
//
// Without it every visit is an unrecovered errgroup goroutine: a panic anywhere
// under it (a short device id reaching CVDGroupName's slice, a nil field from a
// partially-written row) takes the whole container down, and Cloud Run answers
// with 503s until it restarts.
//
// h is the row the cycle's own listing already produced. Re-reading it per
// visit was a point read per host per cycle, O(fleet) load for freshness this
// drift check does not need (a status that moved since the listing degrades
// accurately and re-converges next cycle). It is nil only for a checkable
// device on a host outside the probe lists; the visit then resolves the row.
func (s *Service) visitHost(ctx context.Context, h *host.Host, hostID string, devices []*device.Device) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("host health visit panicked",
				slog.String("host_id", hostID),
				slog.Int("devices", len(devices)),
				slog.Any("panic", r),
				slog.String("stack", string(debug.Stack())))
		}
	}()
	if h == nil {
		var err error
		h, err = s.hostRepo.Get(ctx, hostID)
		if err != nil {
			s.flushHealth(ctx, s.failureUpdates(devices, "host not found"))
			return
		}
	}
	s.checkHostDevices(ctx, h, devices)
}

func (s *Service) checkHostDevices(ctx context.Context, h *host.Host, devices []*device.Device) {
	// A host that is not serving cannot vouch for its devices. A settled
	// 'stopped' host says more than "unreachable": the machine is off, so
	// no CVD on it is running, and the device lifecycle settles from that
	// fact. Every other non-serving state (preempted, error, stopping,
	// deleting, unreachable) records failures instead of skipping, because
	// skipping would freeze health_status at last-known, typically healthy,
	// while the VM may already be gone, so the console lies until recovery.
	// Health degrades accurately (unhealthy → zombie per thresholds), and the
	// FailureThreshold still absorbs brief transitions like a host restart.
	// STARTING hosts are probed too, since reaching HO is what promotes them,
	// but they have no placed devices yet, so their failure branch is a no-op.
	if (h.Status != host.StatusRunning && h.Status != host.StatusStarting) || h.HostOrchestratorURL() == "" {
		if h.Status == host.StatusStopped {
			s.settleDevicesOnStoppedHost(ctx, devices)
			return
		}
		s.flushHealth(ctx, s.failureUpdates(devices, fmt.Sprintf("host not serving (%s)", h.Status)))
		return
	}

	client := s.hoClientFactory.ClientForHost(h.HostOrchestratorURL())
	cvdStatuses, err := client.GetAllCVDStatus(ctx)
	if err != nil {
		// For a starting host this is the normal boot window (no devices to
		// fail); for a running host it degrades its devices accurately.
		s.flushHealth(ctx, s.failureUpdates(devices, "host orchestrator unreachable"))
		return
	}

	// HO answered: record first contact (the ho_reachable condition). Promotes
	// a first-boot host 'starting' -> 'running' and backfills the latch for a
	// host that was already running before the condition existed. Guarded on the
	// row we already read so an already-ready host costs no extra write.
	if h.HOReadyAt == nil {
		if promoted, merr := s.hostRepo.MarkHOReady(ctx, h.ID); merr != nil {
			s.logger.Warn("failed to record host HO-ready",
				slog.String("host_id", h.ID), slog.Any("error", merr))
		} else if promoted {
			s.logger.Info("host orchestrator reachable; host promoted to running",
				slog.String("host_id", h.ID))
		}
	}

	// The listing in hand is also the census input: the other direction of
	// the device-to-CVD drift check. A foreign-CVDs host is exempt by
	// provenance: its CVDs arrive through the /v1 proxy with no device rows,
	// so the census would count every healthy one as an orphan and, with
	// the sweep enabled, delete a running test's device. Exclusive hosts CCO
	// provisions itself stay counted; their CVDs have rows.
	if !h.ForeignCVDs {
		s.censusHost(ctx, h.ID, client, cvdStatuses)
	}

	// Keyed by GROUP rather than by CVD id: a device is one leased unit
	// containing N instances, and every one of them is evidence about it. A
	// map keyed by the device's own cvd_id only ever finds instance 0, so a
	// group whose second instance had died read as entirely healthy.
	byGroup := make(map[string][]*hoclient.CVDHealthStatus, len(cvdStatuses))
	for _, cvd := range cvdStatuses {
		byGroup[cvd.Group] = append(byGroup[cvd.Group], cvd)
	}

	// One query for the host's devices, not one per device.
	live := s.liveVerbsOnDevices(ctx, devices)

	updates := make([]device.HealthUpdate, 0, len(devices))
	for _, d := range devices {
		updates = append(updates, s.observeDevice(ctx, client, d, observeGroup(d, byGroup, live)))
	}
	s.flushHealth(ctx, updates)
}

// groupObservation is one device's CVD-plane sample for this cycle.
type groupObservation struct {
	// instances is the device's whole CVD group as the host listed it.
	instances []device.Instance
	// state is the one CVD wire state that stands for the group, folded over
	// the instances no live verb owns.
	state string
	// held marks a device live verbs own end to end: observe it, do not act.
	held bool
}

// observeGroup builds one device's observation from its whole CVD group.
//
// The live-verb guard is applied PER INSTANCE, and before the fold. Keyed by
// device alone, a restart of instance 2 suppresses the evidence that instance 5
// is dead, and the device sits healthy through a verb that is not touching the
// broken half of it. A verb with no instance name is device-wide (start, stop,
// delete and every group verb) and exempts all of them, which is exactly
// what those verbs are passing through.
//
// A device every verb owns has nothing left to be judged by, so the fold runs
// over the full array instead. The row is held either way, and the raw
// observation still belongs on it.
func observeGroup(d *device.Device, byGroup map[string][]*hoclient.CVDHealthStatus, live operation.LiveSet) groupObservation {
	var listed []*hoclient.CVDHealthStatus
	// A device with no recorded CVD claims no group, and must not collect the
	// listing's group-less entries under the empty key.
	if group, _ := device.ParseCVDID(d.CVDID); group != "" {
		listed = byGroup[group]
	}

	obs := groupObservation{instances: observeInstances(d, listed)}
	folded := unheldInstances(obs.instances, d.ID, live)
	// A device with nothing observed is held only while a verb owns it;
	// otherwise an absent CVD is the zombie signal and still has to be counted.
	obs.held = len(folded) == 0 && (len(obs.instances) > 0 || live.Covers(d.ID, ""))
	if obs.held {
		folded = obs.instances
	}
	obs.state = device.FoldInstanceStates(folded)
	return obs
}

// observeInstances is the device's instance array as this cycle saw it: the
// stored array with every member's state refreshed from the host's listing of
// its group.
//
// The fleet listing carries a name and a state per instance and nothing else.
// It has no operator device id, no ADB port and no displays, so it refreshes
// the stored entries instead of replacing them. An array rebuilt from the
// listing alone would blank the very fields every reachability path addresses
// an instance by.
//
// A stored instance the listing does not name takes the empty state, which is
// the per-instance zombie signal. A listed instance the row does not know is
// appended: a group carrying a member no row records is drift, and the host
// visit is the only place it is visible.
func observeInstances(d *device.Device, listed []*hoclient.CVDHealthStatus) []device.Instance {
	states := make(map[string]string, len(listed))
	for _, cvd := range listed {
		_, name := device.ParseCVDID(cvd.CVDID)
		states[name] = cvd.Status
	}
	out := make([]device.Instance, 0, max(len(d.Instances), len(listed)))
	known := make(map[string]bool, len(d.Instances))
	for _, inst := range d.Instances {
		inst.State = states[inst.Name]
		known[inst.Name] = true
		out = append(out, inst)
	}
	for _, cvd := range listed {
		_, name := device.ParseCVDID(cvd.CVDID)
		if known[name] {
			continue
		}
		out = append(out, device.Instance{Name: name, State: cvd.Status})
	}
	return out
}

// unheldInstances drops the instances a live verb owns, so the fold judges the
// device on the ones nothing is actuating.
func unheldInstances(observed []device.Instance, deviceID string, live operation.LiveSet) []device.Instance {
	if len(live) == 0 {
		return observed
	}
	kept := make([]device.Instance, 0, len(observed))
	for _, inst := range observed {
		if !live.Covers(deviceID, inst.Name) {
			kept = append(kept, inst)
		}
	}
	return kept
}

// sameInstance compares two observations of one instance field by field. It is
// what decides whether the array is worth writing, so it must read every field
// the array carries. A comparison that skipped one would silently stop
// persisting changes to it.
func sameInstance(a, b device.Instance) bool {
	return a.Name == b.Name && a.State == b.State &&
		a.WebRTCDeviceID == b.WebRTCDeviceID && a.ADBPort == b.ADBPort &&
		a.Slot == b.Slot && slices.Equal(a.Displays, b.Displays)
}

// observeDevice runs one device's cycle: the boot-watch enrichment, the
// live-verb hold, the lifecycle derivation, and the health condition.
//
// The boot watch is the only detector a post-create boot loop has, because the
// fleet enum stays Running while the guest loops, so the sample comes from
// the CVD's own logs, budgeted per device. It runs before the verb hold on
// purpose: under a live verb the observation still accumulates (riding the
// HealthUpdate) while the settle stays deferred, so a powerwash's legitimate
// boot transients are observed but never acted on.
//
// A device under a live mutating operation is owned by that operation:
// observe it, do not act on it. A powerwash passes through Stopped and
// (during first boot) BootFailed, which settle to `stopped` and `error`, a
// row contradicting a verb that is working. Health is HELD instead of
// failed for the same reason: counting those observations walks the device
// to `zombie`, and with ZombieRecreate on that files a reconcile that
// recreates the CVD the verb is rebuilding.
func (s *Service) observeDevice(ctx context.Context, client hoclient.Client, d *device.Device, obs groupObservation) device.HealthUpdate {
	freshObs := s.enrichBootObservation(ctx, client, d)
	if freshObs != nil {
		d.BootObservation = freshObs
	}
	var u device.HealthUpdate
	if obs.held {
		u = s.holdHealth(d, obs.state)
	} else {
		derived, settled := device.DerivePhase(d, obs.state)
		if settled && derived != d.Status {
			s.applyDerivedStatus(ctx, d, derived, obs.state)
		}
		u = s.deviceHealthUpdate(d, obs.state, derived, settled)
	}
	u.BootObs = freshObs
	// A non-nil array forces the full write path past the TimestampOnly dedup,
	// so it rides only when it differs from what is stored. Sending one every
	// cycle would retire the cheap two-cell write for the whole healthy fleet.
	if !slices.EqualFunc(d.Instances, obs.instances, sameInstance) {
		u.Instances = obs.instances
	}
	return u
}

// liveVerbsOnDevices reports the live mutating operations over these devices,
// keyed by device and by the instance each verb actuates.
//
// A failed lookup returns an empty set, which degrades to the pre-operations
// behavior (settle from the observation) rather than freezing the device controller:
// a device controller that stops settling is a console that stops telling the truth,
// which is the worse failure of the two.
func (s *Service) liveVerbsOnDevices(ctx context.Context, devices []*device.Device) operation.LiveSet {
	if s.operations == nil || len(devices) == 0 {
		return nil
	}
	ids := make([]string, 0, len(devices))
	for _, d := range devices {
		ids = append(ids, d.ID)
	}
	live, err := s.operations.LiveByResources(ctx, operation.ResourceTypeDevice, ids)
	if err != nil {
		s.logger.Warn("live-operation lookup failed; settling lifecycle from the observation",
			telemetry.GuardDegraded(), slog.Any("error", err))
		return nil
	}
	return live
}

// settleDevicesOnStoppedHost settles the lifecycle of devices on a host whose
// VM is settled stopped: with the machine off no CVD is running, so the host
// status is as authoritative about the devices as a CVD observation would be.
// Without this, a stopped host left its devices claiming "running"
// indefinitely. The host repository settles them in the commit that adopts
// the stop, so this is the fallback for rows that commit skips (a Spot host's
// unrequested stop, or a device that reached running after the commit): the
// failure branch below only degrades HEALTH, no CVD observation can arrive to
// settle the lifecycle, and the lost-host cascade covers deleted VMs only.
// Health is held, not failed, the same contract as an observed CVD "Stopped"
// (the lifecycle derivation owns the outcome). A device with no legal
// transition to stopped (mid-start) stays with its owning flow and keeps the
// accurate failure record.
func (s *Service) settleDevicesOnStoppedHost(ctx context.Context, devices []*device.Device) {
	updates := make([]device.HealthUpdate, 0, len(devices))
	for _, d := range devices {
		if atRest(d) {
			updates = append(updates, s.holdHealth(d, d.CVDStatus))
			continue
		}
		if !d.Status.CanTransitionTo(device.StatusStopped) {
			updates = append(updates, s.failureUpdate(d, "host not serving (stopped)", d.CVDStatus))
			continue
		}
		s.applyDerivedStatus(ctx, d, device.StatusStopped, d.CVDStatus)
		updates = append(updates, s.holdHealth(d, d.CVDStatus))
	}
	s.flushHealth(ctx, updates)
}

// applyDerivedStatus writes the lifecycle status the CVD observation settles
// (Running/Stopped/Stopping/error) through the CAS-safe repository UpdateStatus,
// which re-reads the row and validates the transition. A concurrent transition
// or delete that invalidates the derived status is a benign miss, and the next
// cycle re-observes.
func (s *Service) applyDerivedStatus(ctx context.Context, d *device.Device, derived device.Status, cvdState string) {
	msg := ""
	if derived == device.StatusError {
		msg = "CVD " + cvdState
		// A terminal boot verdict carries its own evidence: the counts, the
		// panic line that names the bug, and the expiry that bounds how long
		// the group's logs exist. "CVD Running" would be worse than nothing.
		if o := d.BootObservation; o != nil && o.Verdict.Terminal() {
			msg = d.BootLoopEvidence()
		}
	}
	err := s.deviceRepo.UpdateStatus(ctx, d.ID, derived, msg)
	if err == nil || errors.Is(err, device.ErrInvalidState) || errors.Is(err, device.ErrNotFound) {
		return
	}
	s.logger.Warn("failed to derive device lifecycle status from CVD state",
		slog.String("device_id", d.ID), slog.String("cvd_status", cvdState),
		slog.String("derived", string(derived)), slog.Any("error", err))
}

// deviceHealthUpdate derives the health condition from the raw CVD observation.
// The fleet listing is the live run_cvd probe, so a Running CVD is a passed
// liveness check (healthy); Unreachable or an absent CVD is the zombie signal
// (fail toward zombie via the threshold counter); Stopped/Stopping/terminal are
// owned by the lifecycle derivation, not health failures; transient bring-up
// and an unobserved state hold the counter. Every update records the raw
// observation.
func (s *Service) deviceHealthUpdate(d *device.Device, cvdState string, derived device.Status, settled bool) device.HealthUpdate {
	switch {
	case settled && derived == device.StatusRunning:
		return device.HealthUpdate{
			DeviceID: d.ID,
			HostID:   d.HostID,
			Status:   device.HealthStatusHealthy,
			// Steady state: health, counter, and observation all unchanged.
			TimestampOnly: d.HealthStatus == device.HealthStatusHealthy &&
				d.ConsecutiveFails == 0 && d.CVDStatus == cvdState,
			CVDStatus: cvdState,
		}
	case settled:
		// Stopped/Stopping/error: the lifecycle derivation owns the outcome.
		return s.holdHealth(d, cvdState)
	case device.CVDStateIsTransient(cvdState):
		// Preparing/Starting: expected mid-bring-up, not a failure.
		return s.holdHealth(d, cvdState)
	case atRest(d):
		// At rest: only a running CVD matters (the lifecycle derivation above
		// heals that drift). Anything else, a stopped CVD or none at all,
		// is the expected shape of a stopped device, not a failure.
		return s.holdHealth(d, cvdState)
	case device.CVDStateIsUnobserved(cvdState):
		// The listing carried no status at all (operator-list fallback), so
		// this cycle saw nothing. Record that and hold. Failing here would
		// march every device on the host to zombie for the host orchestrator's
		// missing endpoint rather than for anything wrong with the devices.
		return s.holdHealth(d, cvdState)
	default:
		// Unreachable, absent, or unmodeled: the CVD is gone or unresponsive.
		return s.failureUpdate(d, cvdFailReason(cvdState), cvdState)
	}
}

// holdHealth records the CVD observation and advances the check timestamp
// without counting a failure or changing the health condition.
func (s *Service) holdHealth(d *device.Device, cvdState string) device.HealthUpdate {
	status := d.HealthStatus
	if status == "" {
		status = device.HealthStatusUnknown
	}
	return device.HealthUpdate{
		DeviceID:         d.ID,
		HostID:           d.HostID,
		Status:           status,
		ConsecutiveFails: int64(d.ConsecutiveFails),
		CVDStatus:        cvdState,
	}
}

// cvdFailReason describes why a CVD observation counts as a health failure.
func cvdFailReason(cvdState string) string {
	if cvdState == "" {
		return "CVD not found on host"
	}
	return "CVD " + cvdState
}

func (s *Service) failureUpdate(d *device.Device, reason, cvdState string) device.HealthUpdate {
	failCount := d.ConsecutiveFails + 1

	// Keep the current status below FailureThreshold: a single blip must not
	// flip a healthy device to unhealthy.
	status := d.HealthStatus
	if status == "" {
		status = device.HealthStatusUnknown
	}
	switch {
	case failCount >= s.config.ZombieThreshold:
		status = device.HealthStatusZombie
	case failCount >= s.config.FailureThreshold:
		status = device.HealthStatusUnhealthy
	}
	return device.HealthUpdate{
		DeviceID:         d.ID,
		HostID:           d.HostID,
		Status:           status,
		ConsecutiveFails: int64(failCount),
		FailReason:       reason,
		CVDStatus:        cvdState,
	}
}

// failureUpdates fails every device with the same reason (a host-wide outage);
// it preserves each device's last CVD observation rather than blanking it,
// since no fresh per-CVD status was obtained. Devices already at rest are held
// instead: a stopped device is not made unhealthy by its host going away.
func (s *Service) failureUpdates(devices []*device.Device, reason string) []device.HealthUpdate {
	updates := make([]device.HealthUpdate, 0, len(devices))
	for _, d := range devices {
		if atRest(d) {
			updates = append(updates, s.holdHealth(d, d.CVDStatus))
			continue
		}
		updates = append(updates, s.failureUpdate(d, reason, d.CVDStatus))
	}
	return updates
}

// atRest reports a device whose lifecycle has settled to a non-serving state.
// It is visited only so a CVD running underneath it can be noticed (the host
// is the one place that drift is visible); nothing about the host's own health
// can make it fail.
func atRest(d *device.Device) bool {
	return d.Status == device.StatusStopped
}

// flushHealth commits one host's check results in a single batch and feeds the
// health gauge. Best effort: health is observability data, and a failed batch
// (e.g. rows cascade-deleted by a concurrent host teardown) only delays that
// host's next check.
func (s *Service) flushHealth(ctx context.Context, updates []device.HealthUpdate) {
	if len(updates) == 0 {
		return
	}
	s.recordStatuses(updates)
	if err := s.deviceRepo.BatchUpdateHealth(ctx, updates); err != nil {
		s.logger.Warn("failed to record device health batch",
			slog.Int("count", len(updates)), slog.Any("error", err))
	}
}
