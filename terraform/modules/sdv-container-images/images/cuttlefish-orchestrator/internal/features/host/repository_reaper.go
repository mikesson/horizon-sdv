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

package hostfeature

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/grpc/codes"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// This file is the lifecycle-reaper cluster: the reaper's expiry listings and
// the reconciler-driven lifecycle writes (lost VMs, preemption, poison
// tracking, HO readiness, GCE state CAS). MarkHostLost and BatchUpdateGCEState
// are the two places this repository writes devices rows: the first fails a
// lost host's live devices over in the same commit, the second settles a
// stopped host's devices to stopped. That is the reverse edge of the
// hosts-column ownership note on HostRepository.

// colErrorMessage is shared by the lost-host writes to hosts and devices rows.
const colErrorMessage = "error_message"

// errNoCreateFailureChange skips a no-op host write when a clean create finds
// no accumulated poison suspicion to clear.
var errNoCreateFailureChange = errors.New("no create-failure change")

// errNoHOReadyChange skips a no-op host write when MarkHOReady is called on a
// host that already recorded HO contact. That is the common case, since the
// device controller probes every running host each cycle.
var errNoHOReadyChange = errors.New("no ho-ready change")

// RecordCreateFailure counts a poison-signature create failure against the host
// and marks it degraded at the threshold, in one transaction. Reports
// whether THIS call newly degraded the host.
func (r *HostRepository) RecordCreateFailure(ctx context.Context, hostID, reason string) (bool, error) {
	newlyDegraded := false
	_, err := r.Mutate(ctx, hostID, func(h *host.Host) error {
		newlyDegraded = h.RecordCreateFailure(reason)
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("recording create failure for host %s: %w", hostID, err)
	}
	return newlyDegraded, nil
}

// ClearCreateFailures resets the poison counter/condition after a clean create
// or successful remediation. A no-op (no write) when the host is already clean.
func (r *HostRepository) ClearCreateFailures(ctx context.Context, hostID string) error {
	_, err := r.Mutate(ctx, hostID, func(h *host.Host) error {
		if h.ConsecutiveCreateFails == 0 && !h.IsDegraded() {
			return errNoCreateFailureChange
		}
		h.ClearDegraded()
		return nil
	})
	if err != nil && !errors.Is(err, errNoCreateFailureChange) {
		return fmt.Errorf("clearing create failures for host %s: %w", hostID, err)
	}
	return nil
}

// MarkHOReady records first Host Orchestrator contact this incarnation and
// promotes a first-boot host 'starting' -> 'running'. A no-op (no write, no
// updated_at bump) once already recorded, so the device controller may call it on
// every running host each cycle without write amplification.
func (r *HostRepository) MarkHOReady(ctx context.Context, hostID string) (bool, error) {
	promoted := false
	_, err := r.Mutate(ctx, hostID, func(h *host.Host) error {
		if h.HOReadyAt != nil {
			return errNoHOReadyChange
		}
		promoted = h.MarkHOReady()
		return nil
	})
	if err != nil && !errors.Is(err, errNoHOReadyChange) {
		return false, fmt.Errorf("marking host %s HO-ready: %w", hostID, err)
	}
	return promoted, nil
}

// CountUndeleted returns how many host rows a sweep can still reach: every row
// that is not already deleted or on its way there, the same NOT-IN set
// ListExpired below applies.
//
// It exists to size the reaper's per-cycle batch against the population it
// reaps. Named for its predicate rather than for "live" because this repository
// has several notions of a live host and none of them is this one.
//
// Bounded-stale: it sizes a bound, and seconds of lag move that bound by
// nothing that matters.
func (r *HostRepository) CountUndeleted(ctx context.Context) (int64, error) {
	stmt := spanner.Statement{
		SQL: `SELECT COUNT(*) FROM hosts WHERE status NOT IN ('deleted', 'deleting')`,
	}
	return spannerplatform.QueryCount(r.client.StaleSingle().Query(ctx, stmt), "undeleted hosts")
}

// The two sweep listings below are salted-shuffled rather than ordered by the
// column they filter on: the reaper and the settle loop run on every instance,
// and one order would put every instance on the same hosts. See
// spannerplatform.SweepSalt for what the lost oldest-first service costs.
// ListExpiringWithin after them is a dashboard lens, not a sweep, and keeps its
// order.

// ListExpired returns hosts that have expired
func (r *HostRepository) ListExpired(ctx context.Context, limit int) ([]*host.Host, error) {
	now := time.Now()

	stmt := spanner.Statement{
		SQL: `SELECT ` + hostColumnsSQL + ` FROM hosts
              WHERE lifecycle_mode = 'ephemeral'
              AND protected_device_count = 0
              AND expires_at IS NOT NULL
              AND expires_at < @now
              AND status NOT IN ('deleted', 'deleting')
              ORDER BY FARM_FINGERPRINT(CONCAT(host_id, @salt))
              LIMIT @limit`,
		Params: map[string]any{
			paramNow:                       now,
			paramLimit:                     int64(limit),
			spannerplatform.SweepSaltParam: spannerplatform.SweepSalt(),
		},
	}

	return spannerplatform.QueryAll(r.client.Single().Query(ctx, stmt),
		"expired hosts", (*models.HostRow).ToDomain)
}

// ListEmptyBatchHosts returns batch-provisioned hosts with no live devices,
// created before cutoff. A drained batch host serves nobody (an exclusive one
// can never be auto-placed onto again), so the reaper reclaims it early
// instead of waiting for the host TTL. Liveness uses the same NOT-IN set as
// CountLiveByHost, never current_device_count (the counter includes error
// rows). The cutoff protects a keyed admission resume: a crashed batch's
// still-empty hosts must survive long enough for the retry to reuse them.
func (r *HostRepository) ListEmptyBatchHosts(ctx context.Context, cutoff time.Time, limit int) ([]*host.Host, error) {
	stmt := spanner.Statement{
		SQL: `SELECT ` + hostColumnsSQL + ` FROM hosts h
              WHERE h.batch_id IS NOT NULL
              AND h.status NOT IN ('deleted', 'deleting')
              AND h.created_at < @cutoff
              AND NOT EXISTS (
                SELECT 1 FROM devices d
                WHERE d.host_id = h.host_id
                  AND d.status NOT IN ('error', 'deleting', 'deleted'))
              ORDER BY FARM_FINGERPRINT(CONCAT(h.host_id, @salt))
              LIMIT @limit`,
		Params: map[string]any{
			paramCutoff:                    cutoff,
			paramLimit:                     int64(limit),
			spannerplatform.SweepSaltParam: spannerplatform.SweepSalt(),
		},
	}

	return spannerplatform.QueryAll(r.client.Single().Query(ctx, stmt),
		"empty batch hosts", (*models.HostRow).ToDomain)
}

// ListExpiringWithin returns live ephemeral hosts whose lease ends inside the
// window, soonest first, for the reclaimable-capacity lens.
func (r *HostRepository) ListExpiringWithin(ctx context.Context, window time.Duration, limit int) ([]*host.Host, error) {
	now := time.Now()

	stmt := spanner.Statement{
		SQL: `SELECT ` + hostColumnsSQL + ` FROM hosts
              WHERE lifecycle_mode = 'ephemeral'
              AND expires_at IS NOT NULL
              AND expires_at >= @now
              AND expires_at < @cutoff
              AND status NOT IN ('deleted', 'deleting')
              ORDER BY expires_at ASC
              LIMIT @limit`,
		Params: map[string]any{
			paramNow:    now,
			paramCutoff: now.Add(window),
			paramLimit:  int64(limit),
		},
	}

	return spannerplatform.QueryAll(r.client.Single().Query(ctx, stmt),
		"expiring hosts", (*models.HostRow).ToDomain)
}

// CountLostMarksSince counts hosts tombstoned with the given reason after
// since. It backs the host reconciler's lost-mark blast budget: deriving the
// budget from the durable marks themselves lets concurrently sweeping
// instances (a leadership handoff overlap) share ONE budget instead of one
// each. Strong read on purpose, because the gate must see the marks this very
// cycle just committed.
func (r *HostRepository) CountLostMarksSince(ctx context.Context, since time.Time, reason string) (int64, error) {
	iter := r.client.Single().Query(ctx, spanner.Statement{
		SQL: `SELECT COUNT(*) FROM hosts
		      WHERE tombstoned_at > @since AND error_message = @reason`,
		Params: map[string]any{"since": since, "reason": reason},
	})
	return spannerplatform.QueryCount(iter, "recent lost marks")
}

// MarkHostLost marks a host deleted after its VM vanished outside CCO and, in
// the same commit, fails over its live devices to error (without the cascade
// they linger as phantom "running" rows until their own TTL) and releases the
// host's quota, which no other flow will ever release for a lost host (the
// deprovision path that normally frees it is unreachable once the VM is gone).
// Idempotent: an already-deleted host is a no-op. The status is re-verified
// under the transaction: a host that no longer expects a VM (raced a delete or
// a preemption, or was never provisioned) returns *host.InvalidStateError so
// the caller can distinguish a superseded mark from a failure.
func (r *HostRepository) MarkHostLost(ctx context.Context, id, reason string) error {
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		row, err := txn.ReadRow(ctx, "hosts", spanner.Key{id}, []string{colStatus})
		if spanner.ErrCode(err) == codes.NotFound {
			return nil // row purged; nothing to mark
		}
		if err != nil {
			return fmt.Errorf("reading host %s: %w", id, err)
		}
		var status string
		if err := row.Columns(&status); err != nil {
			return fmt.Errorf("reading host %s status: %w", id, err)
		}
		current := host.Status(status)
		if current == host.StatusDeleted {
			return nil
		}
		if !current.ExpectsVM() || !current.CanTransitionTo(host.StatusDeleted) {
			return &host.InvalidStateError{
				Current:  string(current),
				Expected: string(host.StatusDeleted),
				Message: fmt.Sprintf("host %s in status %s cannot be marked lost: an absent VM is not out-of-band deletion in this state",
					id, current),
			}
		}

		now := time.Now()
		dec, err := usedHostsDecrement(ctx, txn)
		if err != nil {
			return err
		}
		// tombstoned_at arms the schema's row deletion policy; the eventual
		// TTL delete cascades this host's interleaved device rows with it.
		// desired_state follows the row's fate: a tombstone's spec is absent.
		muts := append([]*spanner.Mutation{spanner.Update("hosts",
			[]string{colHostID, colStatus, "desired_state", colErrorMessage, colUpdatedAt, "tombstoned_at"},
			[]any{id, string(host.StatusDeleted), host.DesiredAbsent, reason, now, now})}, dec...)

		iter := txn.Query(ctx, spanner.Statement{
			SQL: `SELECT device_id FROM devices
			      WHERE host_id = @host AND status NOT IN ('deleted', 'deleting', 'error')`,
			Params: map[string]any{"host": id},
		})
		if err := iter.Do(func(row *spanner.Row) error {
			var deviceID string
			if err := row.Column(0, &deviceID); err != nil {
				return fmt.Errorf("scanning device id: %w", err)
			}
			muts = append(muts, spanner.Update("devices",
				[]string{colHostID, "device_id", colStatus, colErrorMessage, colUpdatedAt},
				[]any{id, deviceID, "error", reason, now}))
			return nil
		}); err != nil {
			return fmt.Errorf("listing devices of lost host %s: %w", id, err)
		}
		// The disk went with the VM: the host's snapshots are bytes that no
		// longer exist, and their rows must say so in the same commit. The
		// artifact-cache ledger went with the same disk and simply deletes.
		snapMuts, err := snapshotsLostMutations(ctx, txn, id, now)
		if err != nil {
			return err
		}
		muts = append(muts, snapMuts...)
		muts = append(muts, spannerplatform.ArtifactCacheLostMutation(id))
		return txn.BufferWrite(muts)
	})
	if err != nil {
		return fmt.Errorf("marking host %s lost: %w", id, err)
	}
	return nil
}

// MarkPreempted records a Spot-VM preemption on a host. It goes through Mutate
// (a transactional read-modify-write) rather than a blind full-row write so a
// concurrent device create/delete that bumps current_device_count is never
// clobbered by a stale in-memory copy. The transition is validated under the
// same transaction: a host that concurrently entered a state preemption cannot
// interrupt (deleting, error, deleted) returns *host.InvalidStateError instead
// of being overwritten. Writing preempted over deleting would make the host
// retryable and let auto-recovery re-provision a VM the user just deleted.
func (r *HostRepository) MarkPreempted(ctx context.Context, id, opID string) (*host.Host, error) {
	return r.Mutate(ctx, id, func(h *host.Host) error {
		if !h.Status.CanTransitionTo(host.StatusPreempted) {
			return &host.InvalidStateError{
				Current:  string(h.Status),
				Expected: string(host.StatusPreempted),
				Message: fmt.Sprintf("host %s in status %s cannot be marked preempted: a concurrent transition owns the outcome",
					id, h.Status),
			}
		}
		h.SetPreempted(opID)
		return nil
	})
}

// BatchUpdateGCEState updates GCE-owned columns for multiple hosts, batched to
// keep latency and Spanner transaction costs low. Each row's write is a
// compare-and-set: DML preconditioned on the status the update was computed
// from (mutations cannot carry preconditions), so a plan gone stale against a
// concurrent lifecycle transition (a user delete between the sync's snapshot
// and its apply) skips the row instead of resurrecting it. It returns the
// number of rows actually written; the shortfall from len(updates) is the
// caller's stale-miss count.
//
// An update that moves a host into stopped also settles the host's running
// and stopping devices to stopped in the same commit, so a host start can
// never find them still reading running (a Spot host's unrequested stop
// excepted; see settleDevicesOnStoppedStatement). The device statement follows its
// host statement and runs only when that host now reads stopped, which a CAS
// miss leaves untouched. Device rows are not counted in the return value.
func (r *HostRepository) BatchUpdateGCEState(ctx context.Context, updates []host.GCEStateUpdate) (int64, error) {
	if len(updates) == 0 {
		return 0, nil
	}

	const batchSize = 500
	var applied int64
	for i := 0; i < len(updates); i += batchSize {
		end := min(i+batchSize, len(updates))
		stmts := make([]spanner.Statement, 0, end-i)
		// hostStmt marks which statements are host CAS writes; only their
		// counts feed applied.
		hostStmt := make([]bool, 0, end-i)
		now := time.Now()
		for _, u := range updates[i:end] {
			stmts = append(stmts, spanner.Statement{
				SQL: `UPDATE hosts
				      SET status = @status, ip_address = @ip, internal_ip = @internalIP,
				          vm_status = @vmStatus, vm_observed_at = @now,
				          vm_terminates_at = @terminatesAt,
				          updated_at = @now
				      WHERE host_id = @id AND status = @prev`,
				Params: map[string]any{
					"id":           u.ID,
					"prev":         string(u.PrevStatus),
					colStatus:      string(u.Status),
					"vmStatus":     u.VMStatus,
					"ip":           u.IP,
					"internalIP":   u.InternalIP,
					"terminatesAt": models.ToNullTime(u.VMTerminatesAt),
					paramNow:       now,
				},
			})
			hostStmt = append(hostStmt, true)
			if u.Status == host.StatusStopped && u.PrevStatus != host.StatusStopped {
				stmts = append(stmts, settleDevicesOnStoppedStatement(u.ID, now))
				hostStmt = append(hostStmt, false)
			}
		}
		var batchApplied int64
		_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
			batchApplied = 0 // Reset: the transaction body may run more than once.
			counts, err := txn.BatchUpdate(ctx, stmts)
			if err != nil {
				return err
			}
			for j, c := range counts {
				if hostStmt[j] {
					batchApplied += c
				}
			}
			return nil
		})
		if err != nil {
			return applied, fmt.Errorf("batch update failed at index %d: %w", i, err)
		}
		applied += batchApplied
	}
	return applied, nil
}

// settleDevicesOnStoppedStatement settles a stopped host's running and
// stopping devices to stopped. The EXISTS reads the host row as written by
// the statements before it in the same transaction, so a host CAS that missed
// leaves the devices alone.
//
// A Spot host that stopped while desired running is skipped: that is a
// preemption whose evidence the ops scan has not seen yet, and preemption
// recovery re-creates the devices from running.
func settleDevicesOnStoppedStatement(hostID string, now time.Time) spanner.Statement {
	return spanner.Statement{
		SQL: `UPDATE devices
		      SET status = 'stopped', stopped_at = @now, updated_at = @now
		      WHERE host_id = @id AND status IN ('running', 'stopping')
		        AND EXISTS (SELECT 1 FROM hosts WHERE host_id = @id AND status = 'stopped'
		                    AND (IFNULL(provisioning_model, '') != @spot OR desired_state = @desiredStopped))`,
		Params: map[string]any{
			"id":             hostID,
			"spot":           string(host.ProvisioningModelSpot),
			"desiredStopped": host.DesiredStopped,
			paramNow:         now,
		},
	}
}
