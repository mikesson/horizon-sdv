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

package batchsettle

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/batch"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

const (
	// atomicShortfallGrace is how long an atomic batch may hold fewer member
	// rows than it admitted before the sweep declares the fill impossible.
	// Membership is fixed at admission, so a shortfall only heals via a keyed
	// admission retry, and the grace is the resume window.
	atomicShortfallGrace = 15 * time.Minute

	// emptyBatchGrace is how old a memberless, non-failed batch row must be
	// before it is garbage. Same reasoning: a younger row may be an
	// admission about to be resumed.
	emptyBatchGrace = time.Hour

	// batchHostGrace is how old a batch-provisioned host must be before its
	// emptiness means "drained" rather than "provisioned moments ago, members
	// still being created".
	batchHostGrace = 30 * time.Minute
)

// memberCounts is the sweep's view of a batch's members. live excludes
// members tearing down: they can never reach running, so for fill purposes
// they are already gone.
type memberCounts struct {
	live    int
	running int
	errored int
}

// judgeAtomicBatch decides an unsettled atomic batch's fate from its member
// counts. Returned reason is empty while the batch may still fill.
func judgeAtomicBatch(b *batch.Batch, m memberCounts, now time.Time) (bool, string) {
	switch {
	case m.running >= b.RequestedCount:
		// >= not ==: membership beyond the admitted target (possible only for
		// rows predating the spec fingerprint) must still settle, not orbit
		// the sweep forever or trip a timeout while every member runs.
		return true, ""
	case m.errored > 0:
		return false, fmt.Sprintf("%d member(s) permanently failed", m.errored)
	case b.ReadyTimeout > 0 && now.Sub(b.CreatedAt) > b.ReadyTimeout:
		return false, fmt.Sprintf("ready_timeout (%s) elapsed before full fill (%d/%d running)",
			b.ReadyTimeout, m.running, b.RequestedCount)
	case m.live < b.RequestedCount && now.Sub(b.CreatedAt) > atomicShortfallGrace:
		return false, fmt.Sprintf("admission incomplete: %d of %d members exist past the resume grace window",
			m.live, b.RequestedCount)
	default:
		return false, ""
	}
}

// sweepAtomicBatches enforces the atomic fill policy: latch ready_at on full
// fill, or trip failed_at and tear the members down. Member reads are strong,
// because a stale view could fail a batch whose last member just reached
// running.
func (s *Service) sweepAtomicBatches(ctx context.Context, dryRun bool) (int, int) {
	var settled, failed int
	batches, err := s.store.ListUnsettledAtomic(ctx)
	if err != nil {
		s.logger.Error("failed to list unsettled atomic batches", slog.Any("error", err))
		return 0, 1
	}

	now := time.Now()
	for _, b := range batches {
		counts, err := s.batchMemberCounts(ctx, b.ID)
		if err != nil {
			s.logger.Error("failed to roll up batch members",
				slog.String("batch_id", b.ID), slog.Any("error", err))
			failed++
			continue
		}
		ready, reason := judgeAtomicBatch(b, counts, now)
		switch {
		case ready:
			if dryRun {
				s.logger.Info("dry run: would mark atomic batch ready", slog.String("batch_id", b.ID))
				continue
			}
			won, err := s.store.MarkReady(ctx, b.ID, now)
			if err != nil {
				s.logger.Error("failed to mark batch ready", slog.String("batch_id", b.ID), slog.Any("error", err))
				failed++
				continue
			}
			if won {
				s.logger.Info("atomic batch filled", slog.String("batch_id", b.ID),
					slog.Int("members", counts.running))
				settled++
				s.totalReadied.Add(1)
			}
		case reason != "":
			if dryRun {
				s.logger.Info("dry run: would fail atomic batch",
					slog.String("batch_id", b.ID), slog.String("reason", reason))
				continue
			}
			if err := s.failAtomicBatch(ctx, b.ID, now, reason); err != nil {
				s.logger.Error("failed to trip atomic batch",
					slog.String("batch_id", b.ID), slog.Any("error", err))
				failed++
				continue
			}
			settled++
		}
	}
	return settled, failed
}

// failAtomicBatch trips the policy and, if this sweep won the transition,
// tears the members down. A teardown that errors, or a winner that dies
// mid-way, is finished by resumeFailedTeardowns on later cycles. The settle
// scan itself never revisits a tripped batch.
func (s *Service) failAtomicBatch(ctx context.Context, batchID string, now time.Time, reason string) error {
	won, err := s.store.MarkFailed(ctx, batchID, now, reason)
	if err != nil {
		return fmt.Errorf("marking batch failed: %w", err)
	}
	if !won {
		return nil
	}
	s.logger.Info("atomic batch failed, tearing down members",
		slog.String("batch_id", batchID), slog.String("reason", reason))
	s.totalTripped.Add(1)
	return s.tearDownMembers(ctx, batchID)
}

// tearDownMembers deletes every member device of batchID through the same
// batched delete users hit. Idempotent (already-deleted members no-op), so
// the winning trip and the resume arm share it safely.
func (s *Service) tearDownMembers(ctx context.Context, batchID string) error {
	members, err := s.devices.List(ctx, device.ListFilter{BatchID: &batchID, Strong: true})
	if err != nil {
		return fmt.Errorf("listing members of failed batch: %w", err)
	}
	if len(members) == 0 {
		return nil
	}
	if _, err := s.devices.BatchDelete(ctx, members); err != nil {
		return fmt.Errorf("tearing down failed batch: %w", err)
	}
	return nil
}

// resumeFailedTeardowns finishes the teardown of tripped batches whose winner
// died or errored between MarkFailed and the member deletes. The settle-CAS
// makes exactly one sweep the owner of a teardown, which is also what orphans
// it when that owner dies. The settle scan excludes tripped batches, so
// without this level check the members would burn host capacity until their
// own TTLs reached the reaper.
func (s *Service) resumeFailedTeardowns(ctx context.Context, dryRun bool) (int, int) {
	ids, err := s.store.ListFailedWithLiveMembers(ctx, s.config.BatchSize)
	if err != nil {
		s.logger.Error("failed to list tripped batches with live members", slog.Any("error", err))
		return 0, 1
	}
	var resumed, failed int
	for _, id := range ids {
		if dryRun {
			s.logger.Info("dry run: would resume tripped-batch teardown", slog.String("batch_id", id))
			continue
		}
		if err := s.tearDownMembers(ctx, id); err != nil {
			s.logger.Error("failed to resume tripped-batch teardown",
				slog.String("batch_id", id), slog.Any("error", err))
			failed++
			continue
		}
		s.logger.Warn("resumed teardown of tripped atomic batch",
			slog.String("batch_id", id))
		resumed++
	}
	return resumed, failed
}

// batchMemberCounts rolls up a batch's members for the sweep (strong read).
func (s *Service) batchMemberCounts(ctx context.Context, batchID string) (memberCounts, error) {
	members, err := s.devices.List(ctx, device.ListFilter{BatchID: &batchID, Strong: true})
	if err != nil {
		return memberCounts{}, fmt.Errorf("listing batch members: %w", err)
	}
	var m memberCounts
	for _, d := range members {
		switch d.Status {
		case device.StatusDeleting, device.StatusDeleted:
			continue
		case device.StatusRunning:
			m.running++
		case device.StatusError:
			m.errored++
		default:
			// Pending/creating/starting/stopping/stopped members are live but
			// neither filled nor failed, so they count toward live only.
		}
		m.live++
	}
	return m, nil
}

// collectEmptyBatches garbage-collects memberless, non-failed batch rows past
// the resume grace window, so an emptied batch 404s instead of lingering to
// the 90-day row TTL. Failed rows are kept for CI post-mortems.
func (s *Service) collectEmptyBatches(ctx context.Context, dryRun bool) (int, int) {
	var collected, failed int
	ids, err := s.store.ListEmptyBefore(ctx, time.Now().Add(-emptyBatchGrace), s.config.BatchSize)
	if err != nil {
		s.logger.Error("failed to list empty batches", slog.Any("error", err))
		return 0, 1
	}
	for _, id := range ids {
		if dryRun {
			s.logger.Info("dry run: would collect empty batch", slog.String("batch_id", id))
			continue
		}
		if err := s.store.Delete(ctx, id); err != nil {
			s.logger.Error("failed to collect empty batch", slog.String("batch_id", id), slog.Any("error", err))
			failed++
			continue
		}
		collected++
	}
	return collected, failed
}

// reapDrainedHosts reclaims drained batch-provisioned hosts ahead of their
// TTL. An exclusive batch host with no live devices can never be placed onto
// again, so keeping it is pure cost.
func (s *Service) reapDrainedHosts(ctx context.Context, dryRun bool) (int, int) {
	var reaped, failed int
	hosts, err := s.hostLister.ListEmptyBatchHosts(ctx, time.Now().Add(-batchHostGrace), s.config.BatchSize)
	if err != nil {
		s.logger.Error("failed to list empty batch hosts", slog.Any("error", err))
		return 0, 1
	}
	for _, h := range hosts {
		if dryRun {
			s.logger.Info("dry run: would reap drained batch host",
				slog.String("host_id", h.ID), slog.String("batch_id", h.BatchID))
			continue
		}
		if err := s.hosts.Delete(ctx, h.ID); err != nil {
			s.logger.Error("failed to reap drained batch host",
				slog.String("host_id", h.ID), slog.Any("error", err))
			failed++
			continue
		}
		s.logger.Info("reaped drained batch host",
			slog.String("host_id", h.ID), slog.String("batch_id", h.BatchID))
		reaped++
		s.totalHostsFreed.Add(1)
	}
	return reaped, failed
}
