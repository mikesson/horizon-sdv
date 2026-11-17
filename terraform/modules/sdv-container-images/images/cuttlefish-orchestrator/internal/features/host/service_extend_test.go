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

package hostfeature_test

import (
	"errors"
	"log/slog"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// newLeaseService builds the host service over the emulator with a real task
// queue, so DeleteExpired's deprovision enqueue lands where countTasks reads.
func newLeaseService(t *testing.T) (*spannertest.TestDB, *hostfeature.HostRepository, *hostfeature.HostService) {
	t.Helper()
	db := spannertest.MustNewTestDB(t)
	repo := hostfeature.NewHostRepository(db.Client)
	tq, _ := newQueueAndIdem(db)
	return db, repo, hostfeature.NewHostService(hostfeature.HostServiceConfig{}, nil, nil, repo, slog.Default(),
		hostfeature.WithTaskEnqueuer(tq))
}

// seedLeasedHost writes a running reserved host whose lease ends at expires.
func seedLeasedHost(t *testing.T, repo *hostfeature.HostRepository, id string, expires time.Time, terminates *time.Time) {
	t.Helper()
	h := newTestHost(id)
	h.Status = host.StatusRunning
	h.Origin = host.OriginUser
	h.VMTerminatesAt = terminates
	h.Lifecycle = host.HostLifecycle{Mode: host.LifecycleModeEphemeral, TTLSeconds: 3600, ExpiresAt: &expires}
	if err := repo.Create(t.Context(), h); err != nil {
		t.Fatalf("seeding host %s: %v", id, err)
	}
}

// A host extended after the reaper listed it expired must never reach
// deleting. Drop DeleteExpired's in-transaction expiry re-check and the
// reaper deletes a host the user just extended.
func TestHostExtendedAfterListingIsNotReaped(t *testing.T) {
	t.Parallel()
	db, repo, svc := newLeaseService(t)
	ctx := t.Context()
	seedLeasedHost(t, repo, "hx-1", time.Now().Add(-time.Minute), nil)

	expired, err := repo.ListExpired(ctx, 10)
	if err != nil || len(expired) != 1 {
		t.Fatalf("ListExpired = %v, %v; want the one expired host", idsOf(expired), err)
	}
	if _, err := svc.Extend(ctx, "hx-1", 1); err != nil {
		t.Fatalf("extend: %v", err)
	}

	err = svc.DeleteExpired(ctx, expired[0].ID)
	if !errors.Is(err, host.ErrInvalidState) {
		t.Fatalf("DeleteExpired on an extended host = %v, want ErrInvalidState", err)
	}
	got, err := repo.Get(ctx, "hx-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != host.StatusRunning || got.DesiredState == host.DesiredAbsent {
		t.Errorf("extended host status = %s desired = %q, want running and not absent", got.Status, got.DesiredState)
	}
	if n := countTasks(t, db, "hx-1"); n != 0 {
		t.Errorf("deprovision tasks = %d, want 0", n)
	}

	// The guard refuses only what is no longer expired.
	seedLeasedHost(t, repo, "hx-2", time.Now().Add(-time.Minute), nil)
	if err := svc.DeleteExpired(ctx, "hx-2"); err != nil {
		t.Fatalf("DeleteExpired on an expired host: %v", err)
	}
	if got, err := repo.Get(ctx, "hx-2"); err != nil || got.Status != host.StatusDeleting {
		t.Errorf("expired host after DeleteExpired = %v, %v; want deleting", got, err)
	}
}

// One request is bounded at MaxHostTTLHours; repeated extends are not. Two
// maximum extends both commit and stack from the later of now and the lease.
func TestTwoMaximumHostExtendsBothSucceed(t *testing.T) {
	t.Parallel()
	db, repo, svc := newLeaseService(t)
	ctx := t.Context()
	start := time.Now().Add(time.Hour)
	seedLeasedHost(t, repo, "hx-3", start, nil)

	for i := range 2 {
		if _, err := svc.Extend(ctx, "hx-3", host.MaxHostTTLHours); err != nil {
			t.Fatalf("extend %d: %v", i+1, err)
		}
	}
	got := leaseOf(t, db, "hx-3")
	want := start.Add(2 * time.Duration(host.MaxHostTTLHours) * time.Hour)
	if !got.Valid || got.Time.Sub(want).Abs() > time.Second {
		t.Errorf("expires_at = %v, want %v", got, want)
	}
}

// A host extend past the VM's stop time is refused and leaves the lease alone.
func TestHostExtendPastVMHorizonIsRefused(t *testing.T) {
	t.Parallel()
	db, repo, svc := newLeaseService(t)
	ctx := t.Context()
	start := time.Now().Add(time.Hour)
	terminates := time.Now().Add(3 * time.Hour)
	seedLeasedHost(t, repo, "hx-4", start, &terminates)

	_, err := svc.Extend(ctx, "hx-4", 2)
	if _, ok := errors.AsType[*host.VMHorizonError](err); !ok {
		t.Fatalf("extend past the VM horizon = %v, want *host.VMHorizonError", err)
	}
	if got := leaseOf(t, db, "hx-4"); !got.Valid || got.Time.Sub(start).Abs() > time.Millisecond {
		t.Errorf("expires_at after the refused extend = %v, want %v", got, start)
	}
	if _, err := svc.Extend(ctx, "hx-4", 1); err != nil {
		t.Errorf("extend inside the horizon refused: %v", err)
	}
}

func TestHostExtendUnknownHostIsNotFound(t *testing.T) {
	t.Parallel()
	_, _, svc := newLeaseService(t)
	if _, err := svc.Extend(t.Context(), "no-such-host", 1); !errors.Is(err, host.ErrNotFound) {
		t.Errorf("extend of a missing host = %v, want ErrNotFound", err)
	}
}
