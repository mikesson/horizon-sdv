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

package spanner_test

import (
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// leaseEntry builds a lease write the way the fleet service does, through the
// same metadata key the reader discriminates on. Two populations reach one
// action, so a test that stamped only one of them would leave the split
// unexercised while both counts happened to look right.
func leaseEntry(at time.Time, action audit.Action, reclaim string) audit.Entry {
	metadata := map[string]any{
		"name":        "fleet-host",
		"expires_at":  at.Add(8 * time.Minute).UTC().Format(time.RFC3339),
		"age_seconds": int64(3600),
	}
	if reclaim != "" {
		metadata[audit.MetaReclaim] = reclaim
	}
	e := audit.New(action, audit.ResourceHost).
		WithResource(resourceID("host-", at)).
		WithMetadata(metadata).
		Build()
	e.Timestamp = at
	return e
}

// The four lanes are four different verbs, and the trail is the only place they
// survive: the host rows they describe hard-delete, so a row-based count would
// go blank exactly when reclamation had been busiest.
//
// The discriminators are what this test owns. `origin` is a generated column
// over the create's metadata, so a create by a USER or by a declarative miss
// must not read as a keeper top-up; and the two lease directions share one
// action, split only by the reclaim key. Both are invisible to a unit test.
func TestKeeperActivityCountsEachLaneByItsOwnDiscriminator(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewAuditRepository(db.Client)
	now := time.Now()
	inside := now.Add(-2 * time.Hour)

	writeEntries(t, repo,
		// Two keeper top-ups, and two creates that are not: the pool origin is
		// the whole discriminator, and a filter written as "system-created"
		// would sweep the declarative miss back in.
		hostCreateEntry(inside, host.OriginPool, 3, 3),
		hostCreateEntry(inside.Add(time.Second), host.OriginPool, 3, 3),
		hostCreateEntry(inside.Add(2*time.Second), host.OriginAuto, 3, 3),
		hostCreateEntry(inside.Add(3*time.Second), host.OriginUser, 3, 0),
		// One renewal.
		leaseEntry(inside.Add(4*time.Second), audit.ActionHostLeaseExtend, ""),
		// Three shortenings through ONE action, two of them surplus.
		leaseEntry(inside.Add(5*time.Second), audit.ActionHostLeaseShorten, audit.ReclaimFleetSurplus),
		leaseEntry(inside.Add(6*time.Second), audit.ActionHostLeaseShorten, audit.ReclaimFleetSurplus),
		leaseEntry(inside.Add(7*time.Second), audit.ActionHostLeaseShorten, audit.ReclaimDedicatedEmpty),
	)

	activity, err := repo.KeeperActivity(t.Context(), now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("KeeperActivity: %v", err)
	}
	for _, tc := range []struct {
		lane string
		got  int64
		want int64
	}{
		{"provisions", activity.Provisions, 2},
		{"rescues", activity.Rescues, 1},
		{"shortens", activity.Shortens, 2},
		{"collapses", activity.Collapses, 1},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.lane, tc.got, tc.want)
		}
	}
}

// The window is a window. An edge that kept counting last week's reclamation
// would animate forever and stop meaning "this lane moved lately".
func TestKeeperActivityHonorsItsWindow(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewAuditRepository(db.Client)
	now := time.Now()

	writeEntries(t, repo,
		hostCreateEntry(now.Add(-2*time.Hour), host.OriginPool, 3, 3),
		hostCreateEntry(now.Add(-48*time.Hour), host.OriginPool, 3, 3),
		leaseEntry(now.Add(-48*time.Hour), audit.ActionHostLeaseShorten, audit.ReclaimFleetSurplus),
	)

	activity, err := repo.KeeperActivity(t.Context(), now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("KeeperActivity: %v", err)
	}
	if activity.Provisions != 1 {
		t.Errorf("provisions = %d, want 1. The two-day-old top-up is outside the window", activity.Provisions)
	}
	if activity.Shortens != 0 {
		t.Errorf("shortens = %d, want 0", activity.Shortens)
	}
}

// A quiet day is zeros, not an error and not an absent row. COUNTIF answers 0
// over an empty scan; a reader that treated that as no data would draw the whole
// diagram as unknown whenever the fleet had been steady.
func TestKeeperActivityOnAQuietWindowIsZeros(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	repo := cspanner.NewAuditRepository(db.Client)

	activity, err := repo.KeeperActivity(t.Context(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("KeeperActivity: %v", err)
	}
	if activity != (hostconfig.KeeperActivity{}) {
		t.Errorf("activity = %+v, want all zeros", activity)
	}
}
