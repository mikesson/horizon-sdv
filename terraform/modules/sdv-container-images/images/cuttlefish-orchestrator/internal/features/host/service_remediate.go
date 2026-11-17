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
	"log/slog"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// RunRemediateHost attempts to return a host that CVD-create poisoned to
// service. A host degrades after HostPoisonThreshold consecutive create
// failures whose signature is leaked taps or stale HO registration, state a
// `cvd reset` clears in place. The auto-remediation is one step only:
//
//   - The host must be running and empty. `cvd reset` tears down EVERY CVD on
//     the host, so remediating a host that still carries live devices would
//     destroy healthy tenants. A degraded host with devices stays fenced from
//     placement (degraded_at excludes it in FindAvailableHosts /
//     PickHostForPlacement) and is left for an operator, who can drain it and
//     reboot/recycle through the ordinary host stop/start/delete APIs.
//   - On a successful reset the degraded condition clears; the next create
//     re-validates the host and re-degrades it if the poison persists, so a
//     reset that does not stick simply re-fences rather than looping.
//
// A reset error is returned for the worker to retry with backoff; a task that
// exhausts dead-letters while the host stays fenced for operator review. The
// method is a no-op (nil) when the host is already healthy, not running, or the
// reset leg is disabled, because a redelivery after the condition cleared must
// not fail.
func (s *HostService) RunRemediateHost(ctx context.Context, hostID string) error {
	h, err := s.hostRepo.Get(ctx, hostID)
	if errors.Is(err, host.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("loading host %s for remediation: %w", hostID, err)
	}

	log := s.logger.With(slog.String("host_id", hostID), slog.String("op", "remediate"))

	if !h.IsDegraded() {
		log.Debug("host no longer degraded; remediation is a no-op")
		return nil
	}
	if h.Status != host.StatusRunning {
		// The lifecycle (deprovision, preemption recovery, GCE sync) owns a
		// non-running host; a stopped/deleting host cannot be reset.
		log.Debug("degraded host is not running; leaving to lifecycle", slog.String("status", string(h.Status)))
		return nil
	}
	if s.hoFactory == nil {
		log.Warn("host reset leg disabled (no HO factory); host stays fenced for operator")
		return nil
	}
	hoURL := h.HostOrchestratorURL()
	if hoURL == "" {
		log.Warn("degraded host has no orchestrator URL; cannot reset, stays fenced for operator")
		return nil
	}
	if s.liveDeviceCounter == nil {
		log.Warn("live-device counter unset; cannot confirm the host is safe to reset, staying fenced")
		return nil
	}
	// `cvd reset` is host-wide and destroys every CVD on the host. Gate on LIVE
	// devices (running/creating and anything else that backs or is acquiring a
	// CVD), never on the raw device-row count. The error rows left by the
	// poison-failed creates that degraded this host back no CVD, so counting
	// them would fence exactly the empty host the reset exists to recover.
	live, err := s.liveDeviceCounter(ctx, hostID)
	if err != nil {
		return fmt.Errorf("counting live devices on host %s: %w", hostID, err)
	}
	if live > 0 {
		log.Warn("degraded host carries live devices; fenced from placement, left for operator (auto-reset would destroy them)",
			slog.Int64("live_devices", live))
		return nil
	}

	if err := s.hoFactory.ClientForHost(hoURL).Reset(ctx); err != nil {
		return fmt.Errorf("resetting poisoned host %s: %w", hostID, err)
	}
	if err := s.hostRepo.ClearCreateFailures(ctx, hostID); err != nil {
		return fmt.Errorf("clearing degraded condition after reset of %s: %w", hostID, err)
	}
	log.Info("poisoned host reset and returned to service")
	return nil
}
