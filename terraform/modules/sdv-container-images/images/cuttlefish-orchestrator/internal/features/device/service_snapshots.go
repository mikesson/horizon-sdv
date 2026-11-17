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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/google/uuid"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	deviceDomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	hostDomain "cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

// maxSnapshotsPerDevice caps a device's live snapshots. Each one is guest RAM
// plus disk overlays on the host's boot disk, and several devices share that
// disk, so an uncapped collection fills it under all of them.
const maxSnapshotsPerDevice = 3

// Response keys shared by the three verbs' payload builders (goconst).
const (
	respSnapshotID = "snapshot_id"
	respCreatedAt  = "created_at"
)

// snapshotVerbRequest is the request payload stored on all three snapshot
// operations: which snapshot the verb addresses. The worker re-derives
// everything else at claim time.
type snapshotVerbRequest struct {
	SnapshotID string `json:"snapshot_id"`
}

// snapshotIDFromRequest recovers the addressed snapshot from an operation's
// stored request.
func snapshotIDFromRequest(rec *operation.Record) (string, error) {
	var req snapshotVerbRequest
	if err := json.Unmarshal(rec.Request, &req); err != nil || req.SnapshotID == "" {
		return "", fmt.Errorf("operation %s has no snapshot id in its request: %w", rec.ID, err)
	}
	return req.SnapshotID, nil
}

// snapshotCompatibleGPUMode refuses every GPU mode a snapshot cannot capture.
// The rule itself lives on the domain type; this turns it into the 4xx.
func snapshotCompatibleGPUMode(mode deviceDomain.GPUMode) error {
	if mode.SupportsSnapshot() {
		return nil
	}
	return &deviceDomain.ValidationError{
		Message: fmt.Sprintf("snapshots require software rendering, so gpu_mode %q cannot be captured. "+
			"Create the device with gpu_mode %s to snapshot it",
			mode, deviceDomain.GPUModeSwift),
	}
}

// TakeSnapshot admits a snapshot capture on a running device and returns its
// operation handle. The inventory row (state creating) commits in the same
// transaction as the operation and its task, so an admitted take always has a
// row to settle.
//
// The guards here produce good 4xx messages; the worker re-derives and
// re-checks everything at claim time.
func (s *DeviceService) TakeSnapshot(ctx context.Context, id string) (*operation.Record, error) {
	d, err := liveDevice(ctx, s.deviceRepo, id)
	if err != nil {
		return nil, fmt.Errorf("device not found: %w", err)
	}
	if d.Status != deviceDomain.StatusRunning {
		return nil, fmt.Errorf("can only snapshot running devices, current status %s: %w",
			d.Status, deviceDomain.ErrInvalidState)
	}
	if err := snapshotCompatibleGPUMode(d.GPUMode); err != nil {
		return nil, err
	}
	if err := snapshotSafeEnvConfig(d.EnvConfig); err != nil {
		return nil, err
	}
	group, name := deviceDomain.ParseCVDID(d.CVDID)
	if group == "" || name == "" {
		return nil, fmt.Errorf("device %s has malformed CVD ID %q", d.ID, d.CVDID)
	}
	h, err := s.hostRepo.Get(ctx, d.HostID)
	if err != nil {
		return nil, fmt.Errorf("failed to get host: %w", err)
	}
	hoURL := h.HostOrchestratorURL()
	if hoURL == "" {
		return nil, fmt.Errorf("host %s has no IP address", h.ID)
	}
	if s.operations == nil {
		return nil, errors.New("snapshot is unavailable: operations are not configured")
	}

	// Exclusivity serializes same-device takes, so this read cannot race
	// another take past the cap; a concurrent delete only frees room.
	n, err := s.deviceRepo.CountLiveSnapshotsByDevice(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("counting snapshots for device %s: %w", id, err)
	}
	if n >= maxSnapshotsPerDevice {
		return nil, &deviceDomain.SnapshotLimitError{Limit: maxSnapshotsPerDevice}
	}

	// Last of the guards because it is the only one that dials the host.
	if err := s.verifyLaunchIsSnapshottable(ctx, hoURL, group, name); err != nil {
		return nil, err
	}

	actor := ""
	if p, ok := auth.PrincipalFromContext(ctx); ok {
		actor = p.Email
	}
	now := time.Now().UTC()
	snap := &deviceDomain.Snapshot{
		ID:              uuid.NewString(),
		DeviceID:        id,
		HostID:          h.ID,
		CVDGroup:        group,
		CVDName:         name,
		HostIncarnation: h.Incarnation,
		State:           deviceDomain.SnapshotStateCreating,
		CreatedBy:       actor,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	ins, err := s.deviceRepo.SnapshotInsert(snap)
	if err != nil {
		return nil, fmt.Errorf("building snapshot row: %w", err)
	}
	return s.admitSnapshotVerb(ctx, operation.VerbDeviceSnapshot, id, snap.ID, actor, []*spanner.Mutation{ins})
}

// ListSnapshots returns a device's snapshot inventory, newest first. The
// device is loaded first so an unknown id answers 404 rather than an empty
// list.
func (s *DeviceService) ListSnapshots(ctx context.Context, id string) ([]*deviceDomain.Snapshot, error) {
	if _, err := s.deviceRepo.Get(ctx, id); err != nil {
		return nil, fmt.Errorf("device not found: %w", err)
	}
	snaps, err := s.deviceRepo.ListSnapshotsByDevice(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("listing snapshots for device %s: %w", id, err)
	}
	return snaps, nil
}

// DeleteSnapshot admits removal of a device's snapshot and returns its
// operation handle. Any state qualifies: deleting a ready snapshot frees host
// disk, deleting a lost one clears the record, and deleting a creating one
// cleans up after a take whose operation was orphaned (a live take blocks
// this verb through exclusivity anyway).
func (s *DeviceService) DeleteSnapshot(ctx context.Context, id, snapshotID string) (*operation.Record, error) {
	if _, err := s.deviceRepo.GetSnapshot(ctx, id, snapshotID); err != nil {
		return nil, fmt.Errorf("loading snapshot %s: %w", snapshotID, err)
	}
	if s.operations == nil {
		return nil, errors.New("snapshot delete is unavailable: operations are not configured")
	}
	actor := ""
	if p, ok := auth.PrincipalFromContext(ctx); ok {
		actor = p.Email
	}
	return s.admitSnapshotVerb(ctx, operation.VerbDeviceSnapshotDelete, id, snapshotID, actor, nil)
}

// RestoreSnapshot admits starting a stopped device from one of its snapshots
// and returns the operation handle.
func (s *DeviceService) RestoreSnapshot(ctx context.Context, id, snapshotID string) (*operation.Record, error) {
	d, err := liveDevice(ctx, s.deviceRepo, id)
	if err != nil {
		return nil, fmt.Errorf("device not found: %w", err)
	}
	if d.Status != deviceDomain.StatusStopped {
		return nil, fmt.Errorf("can only restore stopped devices, current status %s, so stop the device first: %w",
			d.Status, deviceDomain.ErrInvalidState)
	}
	snap, err := s.deviceRepo.GetSnapshot(ctx, id, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("loading snapshot %s: %w", snapshotID, err)
	}
	h, err := s.hostRepo.Get(ctx, d.HostID)
	if err != nil {
		return nil, fmt.Errorf("failed to get host: %w", err)
	}
	if err := restorableOn(snap, h); err != nil {
		return nil, err
	}
	if s.operations == nil {
		return nil, errors.New("snapshot restore is unavailable: operations are not configured")
	}
	actor := ""
	if p, ok := auth.PrincipalFromContext(ctx); ok {
		actor = p.Email
	}
	return s.admitSnapshotVerb(ctx, operation.VerbDeviceSnapshotRestore, id, snapshotID, actor, nil)
}

// restorableOn is the full restore precondition: the snapshot's own state
// plus the identity of the disk it must be read from.
func restorableOn(snap *deviceDomain.Snapshot, h *hostDomain.Host) error {
	if snap.HostID != h.ID {
		return &deviceDomain.SnapshotNotRestorableError{
			ID:     snap.ID,
			Reason: "the snapshot lives on a different host than the device",
		}
	}
	if err := snap.Restorable(h.Incarnation); err != nil {
		return fmt.Errorf("restore refused: %w", err)
	}
	return nil
}

// admitSnapshotVerb is the shared admission tail: one transaction commits the
// operation, its task, and any extra mutations (take's inventory row).
func (s *DeviceService) admitSnapshotVerb(ctx context.Context, verb operation.Verb,
	id, snapshotID, actor string, mutations []*spanner.Mutation,
) (*operation.Record, error) {
	reqPayload, err := json.Marshal(snapshotVerbRequest{SnapshotID: snapshotID})
	if err != nil {
		return nil, fmt.Errorf("encoding %s request: %w", verb, err)
	}
	rec, err := s.operations.Admit(ctx, spannerplatform.AdmitRequest{
		Verb:       verb,
		ResourceID: id,
		CreatedBy:  actor,
		Request:    reqPayload,
		Mutations:  mutations,
	})
	if err != nil {
		return nil, fmt.Errorf("admitting %s on device %s: %w", verb, id, err)
	}
	s.logger.InfoContext(ctx, "admitted device snapshot verb",
		slog.String("device_id", id),
		slog.String("snapshot_id", snapshotID),
		slog.String("verb", string(verb)),
		slog.String("operation_id", rec.ID))
	return rec, nil
}

// runSnapshotTake is the worker half of the take verb.
//
// On a failed capture it compensates with a best-effort restart: the host's
// take sequence is suspend → snapshot → resume with no resume-only endpoint,
// so a failure can strand the CVD suspended and a restart is the only HTTP
// remedy. The restart is unconditional because nothing in the error says
// which leg failed. Rebooting a device that was never suspended costs a
// reboot, and leaving one suspended costs the device.
//
// When the restart is refused too, the device row is marked error. A capture
// that dies mid-take can kill crosvm and run_cvd with it, after which nothing
// serves the device and every later verb fails on the dead launcher socket.
// A row left saying running would be the platform describing a wreck as
// healthy, indefinitely, because an absent CVD is a signal the lifecycle
// derivation declines to settle. The mark is safe against a false alarm. If
// the guest is in fact alive, the next health cycle observes its CVD running
// and settles the row straight back.
func (s *DeviceService) runSnapshotTake(ctx context.Context, rec *operation.Record, snapshotID string) (map[string]any, error) {
	snap, err := s.deviceRepo.GetSnapshot(ctx, rec.ResourceID, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("loading snapshot %s: %w", snapshotID, err)
	}
	// Device-scoped on purpose: the snapshot row records the group and name the
	// capture belongs to, and CCO keeps one inventory per device rather than per
	// instance, so a take addresses the group's first instance as it always has.
	// Whether suspending one member of a group disturbs the others' shared radio
	// medium is unverified, and re-keying these verbs before that is answered
	// would publish semantics nobody has observed.
	target, err := s.resolveRunningInstance(ctx, rec.ResourceID, "", "snapshot")
	if err != nil {
		return nil, err
	}
	if target.hostID != snap.HostID || target.incarnation != snap.HostIncarnation {
		// The disk the row names is already gone; nothing durable can come of
		// this take. No compensation is needed, since no host call was made.
		s.dropSnapshotRow(ctx, snap, "host re-provisioned before the take ran")
		return nil, &deviceDomain.InvalidStateError{
			Message: "the host was re-provisioned while the snapshot was being admitted; re-issue the snapshot",
		}
	}

	client := s.hoClientFactory.ClientForHost(target.hoURL)
	returnedID, err := client.CreateSnapshot(ctx, target.group, target.name, snapshotID)
	if err != nil {
		if rerr := client.RestartCVD(ctx, target.group, target.name); rerr != nil {
			s.logger.ErrorContext(ctx, "compensating restart after failed snapshot also failed; marking the device error",
				slog.String("device_id", rec.ResourceID), slog.Any("error", rerr))
			msg := fmt.Sprintf("snapshot take failed and the recovery restart was refused (%v); "+
				"the guest is likely down, so restart the device, or delete it if that fails", rerr)
			if uerr := s.deviceRepo.UpdateStatus(ctx, rec.ResourceID, deviceDomain.StatusError, msg); uerr != nil {
				s.logger.WarnContext(ctx, "failed to mark device error after failed snapshot recovery",
					slog.String("device_id", rec.ResourceID), slog.Any("error", uerr))
			}
		} else {
			s.logger.InfoContext(ctx, "restarted device after failed snapshot to clear a possible suspended state",
				slog.String("device_id", rec.ResourceID))
		}
		s.dropSnapshotRow(ctx, snap, "take failed")
		return nil, fmt.Errorf("failed to snapshot CVD: %w", err)
	}
	if returnedID != snapshotID {
		// The host honored the request with a different name; the row would
		// address bytes that do not exist. Refuse rather than adopt, since
		// the stored request, the row and the audit all already name ours.
		s.dropSnapshotRow(ctx, snap, "host renamed the snapshot")
		return nil, fmt.Errorf("host stored snapshot %q instead of the requested %q", returnedID, snapshotID)
	}
	if err := s.deviceRepo.MarkSnapshotReady(ctx, snap.HostID, snapshotID); err != nil {
		// The bytes exist but the inventory still says creating. Fail the
		// operation and say so; the row can be cleaned with snapshot delete.
		return nil, fmt.Errorf("snapshot captured but recording it failed: %w", err)
	}
	return map[string]any{respSnapshotID: snapshotID, respCreatedAt: time.Now().UTC()}, nil
}

// dropSnapshotRow removes a creating row that will never become ready. It is
// best-effort. A leftover creating row is visible in the list and deletable
// by hand, so failure here must not mask the take's own error.
func (s *DeviceService) dropSnapshotRow(ctx context.Context, snap *deviceDomain.Snapshot, why string) {
	if err := s.deviceRepo.DeleteSnapshotRow(ctx, snap.HostID, snap.ID); err != nil {
		s.logger.WarnContext(ctx, "removing dead snapshot row failed",
			slog.String("snapshot_id", snap.ID),
			slog.String("reason", why),
			slog.Any("error", err))
	}
}

// runSnapshotDelete is the worker half of the delete verb. The host-side
// remove runs only while the bytes' disk is the live incarnation; in every
// other case the disk is gone (or was never written) and the row is the only
// thing left to remove.
func (s *DeviceService) runSnapshotDelete(ctx context.Context, rec *operation.Record, snapshotID string) (map[string]any, error) {
	snap, err := s.deviceRepo.GetSnapshot(ctx, rec.ResourceID, snapshotID)
	if err != nil {
		// Row already gone: a prior delivery removed it before its terminal
		// write landed. The outcome the verb promises holds.
		if _, ok := errors.AsType[*deviceDomain.SnapshotNotFoundError](err); ok {
			return map[string]any{"deleted_at": time.Now().UTC()}, nil
		}
		return nil, fmt.Errorf("loading snapshot %s: %w", snapshotID, err)
	}

	if snap.State != deviceDomain.SnapshotStateLost {
		h, err := s.hostRepo.Get(ctx, snap.HostID)
		switch {
		case err != nil:
			// Host row gone entirely. The interleave would have taken this
			// row with it, so this is a torn read at worst; the bytes are
			// gone either way.
		case h.Incarnation != snap.HostIncarnation,
			h.Status == hostDomain.StatusDeleted, h.Status == hostDomain.StatusDeleting,
			h.HostOrchestratorURL() == "":
			// Disk gone or unaddressable: nothing host-side to remove.
		default:
			if err := s.hoClientFactory.ClientForHost(h.HostOrchestratorURL()).DeleteSnapshot(ctx, snapshotID); err != nil {
				return nil, fmt.Errorf("failed to delete snapshot on host: %w", err)
			}
		}
	}
	if err := s.deviceRepo.DeleteSnapshotRow(ctx, snap.HostID, snapshotID); err != nil {
		return nil, fmt.Errorf("removing snapshot row: %w", err)
	}
	return map[string]any{"deleted_at": time.Now().UTC()}, nil
}

// runSnapshotRestore is the worker half of the restore verb. On success the
// device row transitions stopped → running exactly as the synchronous start
// does; on failure the device is still stopped and the row already says so.
func (s *DeviceService) runSnapshotRestore(ctx context.Context, rec *operation.Record, snapshotID string) (map[string]any, error) {
	snap, err := s.deviceRepo.GetSnapshot(ctx, rec.ResourceID, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("loading snapshot %s: %w", snapshotID, err)
	}
	d, err := liveDevice(ctx, s.deviceRepo, rec.ResourceID)
	if err != nil {
		return nil, fmt.Errorf("device not found: %w", err)
	}
	if d.Status != deviceDomain.StatusStopped {
		return nil, &deviceDomain.StateConflictError{
			Action: "restore", Required: deviceDomain.StatusStopped, Current: d.Status,
		}
	}
	h, err := s.hostRepo.Get(ctx, d.HostID)
	if err != nil {
		return nil, fmt.Errorf("failed to get host: %w", err)
	}
	if err := restorableOn(snap, h); err != nil {
		return nil, err
	}
	hoURL := h.HostOrchestratorURL()
	if hoURL == "" {
		return nil, fmt.Errorf("host %s has no IP address", h.ID)
	}

	if err := s.hoClientFactory.ClientForHost(hoURL).StartCVDFromSnapshot(ctx,
		snap.CVDGroup, snap.CVDName, snapshotID); err != nil {
		return nil, fmt.Errorf("failed to restore CVD from snapshot: %w", err)
	}

	if _, err := s.deviceRepo.Mutate(ctx, d.ID, func(d *deviceDomain.Device) error {
		if err := d.TransitionTo(deviceDomain.StatusRunning); err != nil {
			return err
		}
		now := time.Now()
		d.StartedAt = &now
		d.StoppedAt = nil
		return nil
	}); err != nil {
		return nil, fmt.Errorf("recording device restore: %w", err)
	}
	return map[string]any{respSnapshotID: snapshotID, "restored_at": time.Now().UTC()}, nil
}
