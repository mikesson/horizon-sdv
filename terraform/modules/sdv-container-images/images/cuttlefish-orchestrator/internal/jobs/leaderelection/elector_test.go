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

package leaderelection_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/jobs/leaderelection"
)

func TestElectorBecomesLeaderAndReleases(t *testing.T) {
	t.Parallel()
	lease := &fakeLease{}
	e := leaderelection.New(electorConfig("A"), lease, testLogger())

	e.Start(t.Context())
	require.Eventually(t, e.IsLeader, time.Second, 2*time.Millisecond)

	e.Stop()
	assert.False(t, e.IsLeader())
	assert.Empty(t, lease.currentHolder(), "lease released on stop")
}

func TestElectorStepsDownOnRenewError(t *testing.T) {
	t.Parallel()
	lease := &fakeLease{}
	e := leaderelection.New(electorConfig("A"), lease, testLogger())

	e.Start(t.Context())
	require.Eventually(t, e.IsLeader, time.Second, 2*time.Millisecond)

	lease.setError(errors.New("spanner unavailable"))
	require.Eventually(t, func() bool { return !e.IsLeader() }, time.Second, 2*time.Millisecond)

	e.Stop()
}

func TestElectorSingleLeaderAcrossContenders(t *testing.T) {
	t.Parallel()
	lease := &fakeLease{}
	a := leaderelection.New(electorConfig("A"), lease, testLogger())
	b := leaderelection.New(electorConfig("B"), lease, testLogger())

	a.Start(t.Context())
	b.Start(t.Context())
	t.Cleanup(func() { a.Stop(); b.Stop() })

	// Exactly one is leader.
	require.Eventually(t, func() bool { return a.IsLeader() != b.IsLeader() },
		time.Second, 2*time.Millisecond)

	leader, follower := a, b
	if b.IsLeader() {
		leader, follower = b, a
	}

	// When the leader steps down, the follower takes over.
	leader.Stop()
	require.Eventually(t, follower.IsLeader, time.Second, 2*time.Millisecond)
}

// The plane's TTL is a design number and not an implementation detail. It is
// exactly how long a killed instance's loops stay stopped, so it is asserted
// where a caller can see it: the value the elector renews with, having named no
// TTL of its own, is the one the composition root gets.
func TestElectorRenewsWithThePlaneTTLByDefault(t *testing.T) {
	t.Parallel()
	lease := &fakeLease{}
	e := leaderelection.New(leaderelection.Config{
		Name: leaderelection.BackgroundPlaneLease, Holder: "A",
	}, lease, testLogger())

	e.Start(t.Context())
	require.Eventually(t, e.IsLeader, time.Second, 2*time.Millisecond)
	t.Cleanup(e.Stop)

	assert.Equal(t, 45*time.Second, lease.lastTTL(), "the background plane's lease TTL")
}

// The TTL is what an ungraceful death costs: a killed instance releases
// nothing, so its lease stands until it expires and the successor must take it
// THEN, not before (that is two leaders by construction) and not never.
//
// Both bounds are asserted, because the two failures look identical from one
// side: a lease that ignores expiry hands over instantly, one that never
// expires hands over never.
func TestElectorSuccessorTakesOverAtExpiryAfterACrash(t *testing.T) {
	t.Parallel()
	const ttl = 600 * time.Millisecond
	lease := &fakeLease{}
	cfg := leaderelection.Config{
		Name:          leaderelection.BackgroundPlaneLease,
		Holder:        "A",
		TTL:           ttl,
		RenewInterval: 10 * time.Millisecond,
	}

	crashed := leaderelection.New(cfg, lease, testLogger())
	ctx, kill := context.WithCancel(t.Context())
	crashed.Start(ctx)
	require.Eventually(t, crashed.IsLeader, time.Second, 2*time.Millisecond)

	// A crash and not a shutdown: renewal stops, and nothing releases.
	kill()
	killedAt := time.Now()

	cfg.Holder = "B"
	successor := leaderelection.New(cfg, lease, testLogger())
	successor.Start(t.Context())
	t.Cleanup(successor.Stop)

	require.Eventually(t, successor.IsLeader, 3*ttl, 5*time.Millisecond)
	assert.GreaterOrEqual(t, time.Since(killedAt), ttl/2,
		"took the crashed instance's live lease instead of waiting for it to expire")
}

// A revision rollover is the common case, and it must not cost the plane a
// TTL of stopped loops: the outgoing instance releases, and the successor
// acquires on its next renew tick. The TTL is the crash path only.
func TestElectorReleaseHandsOverWithoutWaitingOutTheTTL(t *testing.T) {
	t.Parallel()
	const ttl = time.Second
	lease := &fakeLease{}
	cfg := leaderelection.Config{
		Name:          leaderelection.BackgroundPlaneLease,
		Holder:        "A",
		TTL:           ttl,
		RenewInterval: 10 * time.Millisecond,
	}
	leader := leaderelection.New(cfg, lease, testLogger())
	cfg.Holder = "B"
	successor := leaderelection.New(cfg, lease, testLogger())

	leader.Start(t.Context())
	require.Eventually(t, leader.IsLeader, time.Second, 2*time.Millisecond)
	successor.Start(t.Context())
	t.Cleanup(successor.Stop)
	require.False(t, successor.IsLeader(), "two leaders on one lease")

	leader.Stop()

	// Well inside the TTL: a release nobody performed would leave the successor
	// waiting the full second out.
	require.Eventually(t, successor.IsLeader, ttl/2, 5*time.Millisecond)
}

func electorConfig(holder string) leaderelection.Config {
	return leaderelection.Config{
		Name:          "job",
		Holder:        holder,
		TTL:           60 * time.Millisecond,
		RenewInterval: 5 * time.Millisecond,
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// fakeLease is an in-memory Lease with the same takeover rules as the real one.
type fakeLease struct {
	mu     sync.Mutex
	holder string
	expiry time.Time
	err    error
	ttl    time.Duration
}

func (l *fakeLease) TryAcquire(_ context.Context, _, holder string, ttl time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ttl = ttl
	if l.err != nil {
		return false, l.err
	}
	now := time.Now()
	if l.holder == "" || l.holder == holder || l.expiry.Before(now) {
		l.holder = holder
		l.expiry = now.Add(ttl)
		return true, nil
	}
	return false, nil
}

func (l *fakeLease) Release(_ context.Context, _, holder string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder == holder {
		l.holder = ""
		l.expiry = time.Time{}
	}
	return nil
}

func (l *fakeLease) setError(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.err = err
}

func (l *fakeLease) currentHolder() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.holder
}

// lastTTL is the lease duration of the most recent acquire or renew.
func (l *fakeLease) lastTTL() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ttl
}
