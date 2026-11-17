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

package catalogsync

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"go.opentelemetry.io/otel/attribute"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// Service probes Spot capacity advice into the durable spot_advice table.
type Service struct {
	config Config

	// Spot capacity-advice sync dependencies (all nil unless enabled).
	spotAdviceProber       SpotAdviceProber
	spotAdviceStore        gcecatalog.SpotAdviceStore
	spotAdviceMachineTypes MachineTypeLister
	spotAdviceZones        EnabledZones

	logger *slog.Logger

	// leaderCheck gates every loop; the runner reads it through isLeader, so
	// it may be set any time before Start.
	leaderCheck func() bool
	// runners drive the periodic loops, one LoopMetrics series per loop.
	runners []*telemetry.Runner

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
}

// Option configures a Service.
type Option func(*Service)

// WithLeaderCheck installs a leadership predicate; sync cycles run only while
// it returns true (or when unset), so just the leader instance probes the
// rate-limited advice API.
func WithLeaderCheck(fn func() bool) Option {
	return func(s *Service) {
		s.leaderCheck = fn
	}
}

// New creates the catalog sync service.
func New(config Config, logger *slog.Logger, opts ...Option) (*Service, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	s := &Service{
		config: config,
		logger: logger,
	}

	for _, opt := range opts {
		opt(s)
	}

	if !s.spotAdviceConfigured() {
		return nil, &ConfigError{Field: "SpotAdvice", Message: "dependencies are not wired"}
	}
	s.runners = []*telemetry.Runner{{
		Interval:    config.SpotAdviceSyncInterval,
		Cycle:       s.syncSpotAdvice,
		LeaderCheck: s.isLeader,
		Metrics:     telemetry.NewLoopMetrics("catalogsync", attribute.String("loop", "spot-advice")),
		RunAtStart:  true,
		// Unbounded: the probe fans out over every enabled cell and can
		// legitimately outlast its interval, and the default budget would
		// cancel it mid-pass every tick without it ever completing. The serial
		// loop already keeps an over-interval cycle from overlapping the next
		// tick, and the lifecycle context still cancels at shutdown.
		Budget: -1,
	}}

	return s, nil
}

// Start begins the sync loops. It returns immediately and is a no-op while
// already running.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.mu.Unlock()

	s.logger.Info("starting catalog sync service",
		slog.Duration("spot_advice_interval", s.config.SpotAdviceSyncInterval))

	for _, r := range s.runners {
		r.Start(runCtx)
	}
}

func (s *Service) isLeader() bool {
	return s.leaderCheck == nil || s.leaderCheck()
}

// Stop stops the sync loops, joining every runner.
func (s *Service) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()

	cancel()
	for _, r := range s.runners {
		r.Stop()
	}
	s.logger.Info("stopped catalog sync service")
}
