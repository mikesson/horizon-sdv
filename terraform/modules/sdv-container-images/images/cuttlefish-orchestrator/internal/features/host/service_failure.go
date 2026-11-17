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
	"log/slog"
	"slices"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/platform/gce"
)

// terminalWriteTimeout bounds a detached terminal-status write so it cannot
// hang the worker during shutdown.
const terminalWriteTimeout = 15 * time.Second

// markProvisionFailed records a terminal provisioning failure on the host with
// classified error details. The worker calls it once the task is exhausted, or
// at once when the machine family has no bootable image.
func (s *HostService) markProvisionFailed(ctx context.Context, hostID string, cause error) {
	// Detach: the task is exhausted, so this terminal write must land even when
	// the handler's context is already canceled or timed out (often the failure
	// cause itself). Covers the load + zone lookup + write below.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalWriteTimeout)
	defer cancel()
	h, err := s.hostRepo.Get(ctx, hostID)
	if err != nil {
		s.logger.Error("failed to load host to mark provisioning failed",
			slog.String("host_id", hostID), slog.Any("error", err))
		return
	}
	s.updateHostWithClassifiedError(ctx, h, classifyProvisionFailure(cause))
}

// classifyProvisionFailure classifies what failed a provision. A missing boot
// image is the resolver's own verdict and already names the family, the
// project and the fix, so it is carried over whole; everything else is a GCE
// error for the classifier.
func classifyProvisionFailure(cause error) *gce.ClassifiedError {
	unavailable, ok := errors.AsType[*hostconfig.ImageUnavailableError](cause)
	if !ok {
		return gce.ClassifyError(cause)
	}
	errorType := gce.ErrorTypeImageNotFound
	if unavailable.Reason == hostconfig.ImagePermissionDenied {
		errorType = gce.ErrorTypePermissionDenied
	}
	return &gce.ClassifiedError{
		Type:       errorType,
		Code:       string(unavailable.Reason),
		Message:    unavailable.Problem(),
		Suggestion: unavailable.Fix(),
	}
}

// markDeprovisionStalled records that a teardown is stuck after the deprovision
// task is exhausted. It does NOT move the host to "error". GCE has no
// delete-failed state (a failed delete just leaves the VM present, and the
// delete is idempotent), and "error" is retryable, so flipping a user-deleted
// host to error would let recovery re-provision it. The host stays "deleting"
// with a Stalled condition; host.reconcile keeps re-driving the idempotent
// delete until it succeeds, and the stall surfaces for operators.
func (s *HostService) markDeprovisionStalled(ctx context.Context, hostID string, cause error) {
	// Detach for the same reason as markProvisionFailed: the exhausted-attempt
	// context may already be dead.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalWriteTimeout)
	defer cancel()
	_, err := s.hostRepo.Mutate(ctx, hostID, func(h *host.Host) error {
		h.StalledReason = "deprovisioning failed: " + cause.Error()
		h.RecomputeStatus() // stays 'deleting' (desired_state=absent wins).
		return nil
	})
	if err != nil && !errors.Is(err, host.ErrNotFound) { // not-found: already removed
		s.logger.Error("failed to record deprovision stall",
			slog.String("host_id", hostID), slog.Any("error", err))
	}
}

// preferObservedZones picks the zone advice to store. A stockout reports the
// zones that currently have capacity, and that beats the ranking, which scores
// headroom and health rather than live availability and has answered with the
// very zones a request had just been refused in. Observed zones are kept only
// when they are also rankable here: naming a zone this deployment cannot place
// into would trade a wrong answer for an unusable one. Falls back to the
// ranking when GCE named nothing usable, including when it named nothing,
// which is what an exhausted region reports.
func preferObservedZones(observed, ranked []string) []string {
	var usable []string
	for _, zone := range observed {
		if slices.Contains(ranked, zone) {
			usable = append(usable, zone)
		}
	}
	if len(usable) > 0 {
		return usable
	}
	return ranked
}

// updateHostWithClassifiedError updates a host with structured error details from a classified GCE error.
func (s *HostService) updateHostWithClassifiedError(ctx context.Context, h *host.Host, classified *gce.ClassifiedError) {
	// The zone ranking makes catalog reads, so it runs before the transaction.
	// This set is a SUBSET of failoverMetric's (service_placement) on purpose.
	// The regional quota classes are excluded here because alternative-zone
	// advice is computed for the user's requested shape, and a same-region
	// zone cannot escape a regional quota. Failover's zone_health recording
	// still covers them.
	var alternativeZones []string
	if s.catalogValidator != nil {
		switch classified.Type {
		case gce.ErrorTypeZoneDown,
			gce.ErrorTypeZoneNotFound,
			gce.ErrorTypeMachineTypeNotInZone,
			gce.ErrorTypeGPUNotInZone,
			gce.ErrorTypeResourceExhausted:
			candidates, err := s.catalogValidator.RankZones(ctx, gcecatalog.ValidationRequest{
				Zone:              h.Zone,
				MachineType:       h.MachineType,
				GPUType:           h.GPUType,
				GPUCount:          h.GPUCount,
				ProvisioningModel: string(h.ProvisioningModel),
			}, gcecatalog.RankOptions{ExcludeZone: h.Zone})
			if err != nil {
				s.logger.Warn("failed to rank alternative zones",
					slog.String("host_id", h.ID),
					slog.Any("error", err),
				)
			} else {
				for _, c := range candidates {
					alternativeZones = append(alternativeZones, c.Zone)
				}
			}
			alternativeZones = preferObservedZones(classified.AlternativeZones, alternativeZones)
		default:
			// Every other class (quota, permission, rate limit, config typos)
			// gets no advice: a same-region zone cannot escape it.
		}
	}

	if _, err := s.hostRepo.Mutate(ctx, h.ID, func(cur *host.Host) error {
		now := time.Now()
		cur.ErrorMessage = classified.Message
		cur.ErrorType = string(classified.Type)
		cur.ErrorCode = classified.Code
		cur.ErrorSuggestion = classified.Suggestion
		cur.ErrorRetryable = classified.IsRetryable
		cur.ErrorOccurredAt = &now
		cur.ErrorAlternativeZones = alternativeZones
		// Status is derived, not written: DerivePhase returns error while the
		// error condition is set (desired_state=running), and keeps it latched
		// there until Retry clears the error fields and re-provisions.
		cur.RecomputeStatus()
		return nil
	}); err != nil {
		s.logger.Error("failed to update host with error details",
			slog.String("host_id", h.ID),
			slog.Any("error", err),
		)
	}
}

// buildCatalogValidationError wraps a failed gcecatalog.ValidationResult as
// the host-domain error MapError folds into the 422 problem details. The
// detail and warning vocabularies are the catalog's own (aliased in
// domain/host), so the result's slices carry over without translation.
func (s *HostService) buildCatalogValidationError(result *gcecatalog.ValidationResult) *host.CatalogValidationError {
	return &host.CatalogValidationError{
		Errors:           result.Errors,
		Warnings:         result.Warnings,
		AlternativeZones: result.AlternativeZones,
	}
}
