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

package leaderelection

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// Lease is the storage contract the Elector needs; the Spanner LeaseRepository
// satisfies it structurally.
type Lease interface {
	// TryAcquire acquires or renews the named lease for holder, returning whether
	// holder owns it afterward.
	TryAcquire(ctx context.Context, name, holder string, ttl time.Duration) (bool, error)
	// Release relinquishes the lease if holder still owns it.
	Release(ctx context.Context, name, holder string) error
}

// BackgroundPlaneLease is the lease key the whole background plane elects
// under: one election for every periodic job, not one per job. Named here
// because the orchestrator, both harnesses and the autoscaler page's leadership
// gate all have to mean the same row.
//
// Re-sharding the plane later, once one job's work has grown past a single
// instance, is a second election beside this one, which changes only wiring.
const BackgroundPlaneLease = "background-plane"

// Config configures an Elector.
type Config struct {
	// Name is the lease key; contenders share it.
	Name string
	// Holder uniquely identifies this instance (see InstanceID).
	Holder string
	// TTL is the lease duration. Zero takes the package default below. Keep it
	// well above RenewInterval so a transient renewal failure does not drop
	// leadership.
	TTL time.Duration
	// RenewInterval is how often leadership is renewed. Defaults to TTL/3.
	RenewInterval time.Duration
}

// defaultTTL is how long a lease outlives its holder's last renewal, and so how
// long an ungraceful death (a killed process, a preempted VM or pod that never
// runs Stop) leaves the plane leaderless. Sized for that case alone: every
// job's cycle is 30s-15m and idempotent, so a gap this size costs one skipped
// cycle, whereas a TTL near the renew interval turns an ordinary Spanner hiccup
// into a handoff. A graceful stop releases the lease outright, so this bound
// only ever governs crashes.
const defaultTTL = 45 * time.Second

// Elector renews a lease in the background and tracks whether this instance is
// the leader.
type Elector struct {
	cfg    Config
	lease  Lease
	logger *slog.Logger

	leader atomic.Bool
	runner telemetry.Runner
}

// New creates an Elector. A nil logger falls back to slog.Default.
func New(cfg Config, lease Lease, logger *slog.Logger) *Elector {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.TTL <= 0 {
		cfg.TTL = defaultTTL
	}
	if cfg.RenewInterval <= 0 {
		cfg.RenewInterval = cfg.TTL / 3
	}
	e := &Elector{cfg: cfg, lease: lease, logger: logger}
	e.runner = telemetry.Runner{
		Interval: cfg.RenewInterval,
		Cycle:    e.tryRenew,
		Metrics:  telemetry.NewLoopMetrics("leaderelection", attribute.String("lease", cfg.Name)),
		// The default cycle budget bounding each renew (interval−1s, so 14s at
		// the default 15s cadence) is intended. A renew that hangs past its
		// tick steps the instance down conservatively instead of trusting stale
		// leadership, and it does so with two renewals' worth of TTL still to
		// run, so the step-down never races the lease it is protecting.
		RunAtStart: true,
	}
	return e
}

// Start begins renewing leadership in the background. It returns immediately.
func (e *Elector) Start(ctx context.Context) {
	e.runner.Start(ctx)
}

// Stop halts renewal and releases the lease if held, so a successor can take
// over without waiting for expiry.
func (e *Elector) Stop() {
	e.runner.Stop()
	if e.leader.Swap(false) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.lease.Release(ctx, e.cfg.Name, e.cfg.Holder); err != nil {
			e.logger.Warn("failed to release lease on shutdown",
				slog.String("lease", e.cfg.Name), slog.Any("error", err))
		}
	}
}

// IsLeader reports whether this instance currently holds leadership.
func (e *Elector) IsLeader() bool { return e.leader.Load() }

func (e *Elector) tryRenew(ctx context.Context) error {
	ok, err := e.lease.TryAcquire(ctx, e.cfg.Name, e.cfg.Holder, e.cfg.TTL)
	if err != nil {
		// Cannot confirm we still hold the lease: step down conservatively. The
		// stored lease remains ours until its TTL expires, so no successor can
		// take over yet, and this only creates a brief, safe no-leader gap.
		if e.leader.Swap(false) {
			e.logger.Warn("stepping down: lease renewal failed",
				slog.String("lease", e.cfg.Name), slog.Any("error", err))
		}
		return fmt.Errorf("renewing lease %s: %w", e.cfg.Name, err)
	}
	switch was := e.leader.Swap(ok); {
	case ok && !was:
		e.logger.Info("acquired leadership",
			slog.String("lease", e.cfg.Name), slog.String("holder", e.cfg.Holder))
	case !ok && was:
		e.logger.Info("lost leadership", slog.String("lease", e.cfg.Name))
	}
	return nil
}

// InstanceID returns an identifier for this process: the Cloud Run revision (or
// hostname), suffixed with a short random token so instances of the same
// revision are distinct. It fits the leader_leases.holder column (STRING(64)).
func InstanceID() string {
	base := os.Getenv("K_REVISION")
	if base == "" {
		if h, err := os.Hostname(); err == nil {
			base = h
		}
	}
	if base == "" {
		base = "instance"
	}
	if len(base) > 40 {
		base = base[:40]
	}
	return base + "-" + uuid.NewString()[:8]
}
