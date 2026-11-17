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

package taskreclaimer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// Queue is the subset of the task queue the sweeper needs.
type Queue interface {
	// Sweep returns expired-lease tasks to pending and reports the count.
	Sweep(ctx context.Context) (int, error)
}

// Config configures the sweeper.
type Config struct {
	// Enabled gates the sweeper. DefaultConfig sets it true.
	Enabled bool
	// Interval is the time between sweeps.
	Interval time.Duration
}

// DefaultConfig returns production-ready defaults.
func DefaultConfig() Config {
	return Config{Enabled: true, Interval: time.Minute}
}

// Service periodically reclaims expired task leases (crash recovery). It runs on
// every instance but sweeps only while it holds the taskreclaimer lease.
type Service struct {
	enabled bool
	queue   Queue
	logger  *slog.Logger
	runner  telemetry.Runner
}

// Option configures a Service.
type Option func(*Service)

// WithLeaderCheck installs a leadership predicate; sweeps run only while it
// returns true (or when unset).
func WithLeaderCheck(fn func() bool) Option {
	return func(s *Service) {
		s.runner.LeaderCheck = fn
	}
}

// New creates a sweeper. A nil logger falls back to slog.Default.
func New(cfg Config, queue Queue, logger *slog.Logger, opts ...Option) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultConfig().Interval
	}
	s := &Service{enabled: cfg.Enabled, queue: queue, logger: logger}
	s.runner = telemetry.Runner{
		Interval:   cfg.Interval,
		Cycle:      s.sweep,
		Metrics:    telemetry.NewLoopMetrics("taskreclaimer"),
		RunAtStart: true,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start begins the sweep loop. It returns immediately; a disabled sweeper
// never starts.
func (s *Service) Start(ctx context.Context) {
	if !s.enabled {
		return
	}
	s.runner.Start(ctx)
}

// Stop halts the sweep loop and waits for an in-flight sweep to return, so the
// goroutine is fully drained before Stop returns.
func (s *Service) Stop() {
	s.runner.Stop()
}

func (s *Service) sweep(ctx context.Context) error {
	reclaimed, err := s.queue.Sweep(ctx)
	if err != nil {
		s.logger.Error("task sweep failed", slog.Any("error", err))
		return fmt.Errorf("sweeping expired task leases: %w", err)
	}
	if reclaimed > 0 {
		s.logger.Info("reclaimed expired task leases", slog.Int("count", reclaimed))
	}
	return nil
}
