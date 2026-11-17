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

package quotafeature

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"cloud-cuttlefish-orchestrator/internal/domain/quota"
)

// UsedHostsReconciler heals the used_hosts counter against the live host
// count in one transaction, the one host capability this service needs. The
// host repository satisfies it: the counter and the host rows it must agree
// with live behind the same repository, so only it can put the recount and
// the healing write into a single serializable transaction.
type UsedHostsReconciler interface {
	// ReconcileUsedHosts recounts quota-holding hosts and heals the global
	// counter when the two disagree, reporting the values it settled on. It
	// healed exactly when they differ.
	ReconcileUsedHosts(ctx context.Context) (counter, actual int64, err error)
}

// Service implements the domain quota service: limit administration and
// drift reconciliation for the single global host quota. The used_hosts
// counter itself is maintained by the host feature's atomic commits, not
// through this service.
type Service struct {
	repo     quota.Repository
	hostRepo UsedHostsReconciler
	logger   *slog.Logger
}

// NewService creates a new quota service. hosts supplies the transactional
// used_hosts recount that ReconcileAll delegates to.
func NewService(repo quota.Repository, hosts UsedHostsReconciler, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{repo: repo, hostRepo: hosts, logger: logger}
}

// SetLimit updates the max_hosts for a quota (creates if not exists).
//
// The single repo.Set call applies create-or-update atomically and never
// writes used_hosts to an existing row, so a host-create or host-delete
// counter commit racing an admin limit change is preserved.
func (s *Service) SetLimit(ctx context.Context, scope quota.Scope, scopeID string, maxHosts int64) error {
	q := &quota.Quota{
		Scope:    scope,
		ScopeID:  scopeID,
		MaxHosts: maxHosts,
	}
	if err := s.repo.Set(ctx, q); err != nil {
		return fmt.Errorf("setting quota limit: %w", err)
	}

	s.logger.Info("quota limit set",
		slog.String("scope", string(scope)),
		slog.String("scope_id", scopeID),
		slog.Int64("max_hosts", maxHosts),
	)
	return nil
}

// Get retrieves quota for a scope.
func (s *Service) Get(ctx context.Context, scope quota.Scope, scopeID string) (*quota.Quota, error) {
	// Returned unwrapped so the NotFoundError's own message is the 404 body.
	return s.repo.Get(ctx, scope, scopeID)
}

// EnsureDefaultLimit seeds the platform quota with the default limit when it
// is missing or zero. Called once at startup so read paths never initialize
// state.
func (s *Service) EnsureDefaultLimit(ctx context.Context) error {
	q, err := s.Get(ctx, quota.ScopePlatform, quota.GlobalScopeID)
	_, isNotFound := errors.AsType[*quota.NotFoundError](err)
	switch {
	case isNotFound, err == nil && q.MaxHosts == 0:
		return s.SetLimit(ctx, quota.ScopePlatform, quota.GlobalScopeID, quota.DefaultPlatformMaxHosts)
	case err != nil:
		return fmt.Errorf("reading platform quota: %w", err)
	default:
		return nil
	}
}

// ReconcileAll recalculates used_hosts for the global quota based on the
// actual host count. This corrects any drift between the counter and reality.
// Returns the number of quotas that had drift corrected. The recount and the
// healing write share one transaction in the host repository, so an in-flight
// create or delete counter commit can never be overwritten by a recount that
// predates it. A "drift detected" here is real drift, never a racing snapshot.
func (s *Service) ReconcileAll(ctx context.Context) (int, error) {
	if s.hostRepo == nil {
		return 0, errors.New("host repository not configured for quota reconciliation")
	}

	counter, actual, err := s.hostRepo.ReconcileUsedHosts(ctx)
	if err != nil {
		return 0, fmt.Errorf("reconciling global quota: %w", err)
	}
	if counter == actual {
		return 0, nil
	}
	s.logger.Warn("quota drift healed",
		slog.Int64("counter", counter),
		slog.Int64("actual", actual),
		slog.Int64("drift", counter-actual),
	)
	return 1, nil
}

// Ensure Service implements quota.Service.
var _ quota.Service = (*Service)(nil)
