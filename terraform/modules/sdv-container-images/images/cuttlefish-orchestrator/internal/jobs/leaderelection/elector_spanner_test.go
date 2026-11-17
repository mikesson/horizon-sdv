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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/jobs/leaderelection"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// The elector against the real predicate DML rather than the in-memory Lease:
// takeability, the expiry comparison and the new expiry are all evaluated
// server-side, so the fake proves the elector's logic and this proves the
// arrangement.
//
// The timings here are slack on purpose, and the renew intervals in
// particular. telemetry.CycleBudget gives each renew interval−1s (never less
// than half), so a sub-second interval budgets a renew in tens of
// milliseconds, and the emulator serializes read-write transactions globally,
// so under the full suite a renew queues behind every other package's. Tight
// intervals here passed alone and failed in CI with nobody ever elected.

const (
	// One renew a contended emulator can finish inside its cycle budget, and a
	// TTL three renewals wide so a slow one is not a handover.
	electionRenew = 3 * time.Second
	electionTTL   = 9 * time.Second
	// Room for a whole TTL to lapse plus a renew tick to notice, several times
	// over. This bounds a failure; a passing run finishes far sooner.
	electionWait = 60 * time.Second
)

// Three contending instances leave ONE row behind. The row count is the
// assertion: every periodic job takes this same lease, so a second row means a
// second election was wired somewhere, which the background plane no longer
// has.
func TestBackgroundPlaneElectsOnASingleRow(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewLeaseRepository(db.Client)

	electors := make([]*leaderelection.Elector, 0, 3)
	for _, holder := range []string{"instance-a", "instance-b", "instance-c"} {
		e := leaderelection.New(leaderelection.Config{
			Name:          leaderelection.BackgroundPlaneLease,
			Holder:        holder,
			TTL:           electionTTL,
			RenewInterval: electionRenew,
		}, repo, testLogger())
		e.Start(t.Context())
		t.Cleanup(e.Stop)
		electors = append(electors, e)
	}

	require.Eventually(t, func() bool { return countLeaders(electors) >= 1 },
		electionWait, 100*time.Millisecond, "nobody was ever elected")

	// Read after every contender has had renews of its own, not the moment one
	// of them leads: a contender writes its row on its first renew, so an
	// earlier read can precede the very election this exists to catch.
	time.Sleep(2 * electionRenew)

	leases, err := repo.ListLeases(t.Context())
	require.NoError(t, err)
	require.Len(t, leases, 1, "the background plane must elect on exactly one lease row")
	assert.Equal(t, leaderelection.BackgroundPlaneLease, leases[0].Name)
	assert.NotEmpty(t, leases[0].Holder)
	// At most one, rather than exactly one: a renew that overran its cycle
	// budget steps its instance down until the next tick, so a momentary zero is
	// the plane behaving. Two at once would not be.
	assert.LessOrEqual(t, countLeaders(electors), 1, "two contenders believed they led")
}

// Failover at the TTL, against the lease's own server-side clock: the crashed
// instance releases nothing, so its row stands until CURRENT_TIMESTAMP() passes
// lease_expiry and the successor's ordinary renew takes it.
func TestBackgroundPlaneFailsOverAtTTLExpiry(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewLeaseRepository(db.Client)
	cfg := leaderelection.Config{
		Name:          leaderelection.BackgroundPlaneLease,
		Holder:        "instance-a",
		TTL:           electionTTL,
		RenewInterval: electionRenew,
	}

	crashed := leaderelection.New(cfg, repo, testLogger())
	ctx, kill := context.WithCancel(t.Context())
	crashed.Start(ctx)
	require.Eventually(t, crashed.IsLeader, electionWait, 100*time.Millisecond)

	kill() // renewal stops; the lease is left held, as a SIGKILL leaves it
	killedAt := time.Now()

	cfg.Holder = "instance-b"
	successor := leaderelection.New(cfg, repo, testLogger())
	successor.Start(t.Context())
	t.Cleanup(successor.Stop)

	require.Eventually(t, successor.IsLeader, electionWait, 100*time.Millisecond,
		"the plane never moved off the crashed instance")
	assert.GreaterOrEqual(t, time.Since(killedAt), electionTTL/2,
		"took a live lease from the crashed instance instead of waiting for expiry")

	leases, err := repo.ListLeases(t.Context())
	require.NoError(t, err)
	require.Len(t, leases, 1, "failover moves the plane, it does not add an election")
	assert.Equal(t, "instance-b", leases[0].Holder)
}

func countLeaders(electors []*leaderelection.Elector) int {
	var n int
	for _, e := range electors {
		if e.IsLeader() {
			n++
		}
	}
	return n
}
