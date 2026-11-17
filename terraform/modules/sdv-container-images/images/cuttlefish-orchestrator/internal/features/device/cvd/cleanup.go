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
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// hostNotServingError reports that a deleted device's host is not serving its
// Host Orchestrator right now but its VM still exists (host.ExpectsVM). A
// stopped or unreachable VM keeps its CVD state, so cleanup must re-check
// once the host's fate settles instead of assuming the group died. The task
// handlers translate it into a bounded postpone.
type hostNotServingError struct {
	hostID string
	status host.Status
}

func (e *hostNotServingError) Error() string {
	return fmt.Sprintf("host %s is %s; its VM may still hold the group", e.hostID, e.status)
}

// RunCleanupCVD tears down a deleted device's CVD group on its host from a
// queued task. The group is resolved from the recorded CVDID when creation
// completed, else by its deterministic name, so groups orphaned by an
// interrupted create are reclaimed too. It is idempotent and tolerant: it
// no-ops when the device or host is gone, the host is not serving (the CVD is
// reclaimed when the host is torn down), or the host has no such group, and
// returns an error only on a live teardown failure so the worker retries.
// A group whose launch is still in flight (device deleted mid-create) returns
// cvdLaunchInFlightError, because deleting it would leak the launcher's
// processes. The caller waits for it to settle. A launch whose launcher
// already timed out the boot is deleted at once.
func (a *Actuator) RunCleanupCVD(ctx context.Context, deviceID string) error {
	return a.runCleanupCVD(ctx, deviceID, false)
}

// runCleanupCVD implements RunCleanupCVD. force skips the launch-in-flight
// guard. Past the settle cap a still-transient group means a hung launcher,
// and a best-effort `cvd rm` (VM teardown removes whatever it misses) beats
// leaking the group forever.
func (a *Actuator) runCleanupCVD(ctx context.Context, deviceID string, force bool) error {
	d, err := a.devices.Get(ctx, deviceID)
	if err != nil {
		if errors.Is(err, device.ErrNotFound) {
			return nil // device purged; nothing to clean
		}
		return fmt.Errorf("loading device %s: %w", deviceID, err)
	}
	h, err := a.hosts.Get(ctx, d.HostID)
	if err != nil {
		if errors.Is(err, host.ErrNotFound) {
			return nil // host gone; the CVD went with it
		}
		return fmt.Errorf("loading host %s: %w", d.HostID, err)
	}
	if h.Status != host.StatusRunning || h.HostOrchestratorURL() == "" {
		// A host whose VM still exists may return to serving with its CVD
		// state intact (a stopped VM keeps the group on disk; an unreachable
		// one may still be running it). Completing now would leak the group
		// the moment the host came back, with only the orphan-CVD census left
		// to notice. Wait instead. Everywhere else the VM is absent or
		// doomed: pending/provisioning precede it, a preempted Spot VM is
		// deleted by GCE (recovery inserts a fresh one), error recovery
		// re-provisions, and deleting/deleted reclaim the CVD with the VM.
		// A group a still-live create task lays down later is swept by
		// that task's own torn-down-device guard.
		if h.Status.ExpectsVM() {
			return &hostNotServingError{hostID: h.ID, status: h.Status}
		}
		return nil
	}

	group := device.CVDGroupName(d.ID)
	if g, _ := device.ParseCVDID(d.CVDID); g != "" {
		group = g
	}
	client := a.ho.ClientForHost(h.HostOrchestratorURL())

	// Free the device's snapshot bytes while there is still a host to ask.
	// They live outside the group, so the group teardown below never
	// touches them, and a long-lived host would hold them until its own TTL.
	// Best-effort: the inventory rows are removed by the deleted-mark's
	// cascade regardless, and a missed directory dies with the host.
	a.deleteHostSnapshots(ctx, client, d.ID, h.Incarnation)
	// Probe before deleting: HO surfaces `cvd rm` of an absent group as a
	// failed operation (a retryable-looking 500), not a 404, so deleting
	// blindly would grind an already-clean host through the retry ladder.
	cvds, err := client.ListCVDs(ctx)
	if err != nil {
		return fmt.Errorf("listing CVDs on host %s: %w", d.HostID, err)
	}
	groupCVDs := groupMembers(cvds, group)
	if len(groupCVDs) == 0 {
		return nil // nothing on the host; already clean
	}
	if !force {
		// A device deleted mid-create leaves its launch running server-side;
		// `cvd rm` against it leaks the launcher's processes with no group
		// record left to find them. Wait for it to settle, unless the
		// launcher already gave up.
		if transient := transientGroupStatus(groupCVDs); transient != "" &&
			!a.launcherGaveUp(ctx, client, d, group, groupCVDs) {
			return &cvdLaunchInFlightError{group: group, status: transient}
		}
	}
	if err := client.DeleteCVD(ctx, group); err != nil {
		return fmt.Errorf("deleting CVD group %s on host %s: %w", group, d.HostID, err)
	}
	return nil
}

// launcherGaveUp samples a transient group's logs once and reports whether
// its launcher timed out the boot. run_cvd stops its monitored subprocesses
// right after that line while the fleet listing keeps saying Starting, so
// without this sample the cleanup holds the host's instance slots for up to
// launchWaitCap. No other verdict releases the group early: a looping guest's
// launcher is still live, and removing it leaks the processes the wait
// protects.
//
// The launcher's timeout is the boot window, and the create's dwell clock
// starts before the launch does, so a group whose recorded clock is younger
// than bootEnrichDwell cannot have timed out yet and is not sampled.
func (a *Actuator) launcherGaveUp(ctx context.Context, client hoclient.Client, d *device.Device, group string, members []*hoclient.CVD) bool {
	now := time.Now()
	prev := d.BootObservation
	if prev != nil && prev.TransientSince != nil && now.Sub(*prev.TransientSince) < bootEnrichDwell {
		return false
	}
	obs := sampleGroupBoot(ctx, client, group, members, prev, now)
	if obs == nil || !obs.LauncherGaveUp() {
		return false
	}
	a.logger.Warn("launcher of a deleted device's group gave up its boot; removing the group without waiting for it to settle",
		slog.String("device_id", d.ID),
		slog.String("group_name", group),
		slog.String("evidence", obs.LastPanic))
	return true
}

// deleteHostSnapshots removes a deleted device's snapshots from its host,
// best-effort. Only rows whose bytes live on the CURRENT incarnation are
// dialed, because a stale row addresses a disk that no longer exists.
func (a *Actuator) deleteHostSnapshots(ctx context.Context, client hoclient.Client, deviceID string, incarnation int64) {
	snaps, err := a.devices.ListSnapshotsByDevice(ctx, deviceID)
	if err != nil {
		a.logger.WarnContext(ctx, "listing snapshots for cleanup failed",
			slog.String("device_id", deviceID), slog.Any("error", err))
		return
	}
	for _, s := range snaps {
		if s.HostIncarnation != incarnation || s.State == device.SnapshotStateLost {
			continue
		}
		if err := client.DeleteSnapshot(ctx, s.ID); err != nil {
			a.logger.WarnContext(ctx, "removing snapshot from host failed; its bytes die with the host",
				slog.String("device_id", deviceID),
				slog.String("snapshot_id", s.ID),
				slog.Any("error", err))
		}
	}
}
