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
	"testing"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

func hostStatus(t *testing.T, db *spannertest.TestDB, id string) string {
	t.Helper()
	row, err := db.Client.Single().ReadRow(t.Context(), "hosts", spanner.Key{id}, []string{"status"})
	if err != nil {
		t.Fatalf("read host %s: %v", id, err)
	}
	var s string
	if err := row.Columns(&s); err != nil {
		t.Fatalf("scan host status: %v", err)
	}
	return s
}

// TestMarkHostLostReleasesQuotaOnce pins the lost-host quota invariant: the
// mark releases the host's quota in the same commit, and re-marking (a second
// sync cycle) neither errors nor double-releases.
func TestMarkHostLostReleasesQuotaOnce(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)

	if err := db.SeedQuota(ctx, 10, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.SeedHost(ctx, "lost-1", "lost-host", "us-central1-a"); err != nil {
		t.Fatal(err)
	}

	if err := repo.MarkHostLost(ctx, "lost-1", "host VM deleted outside CCO"); err != nil {
		t.Fatalf("MarkHostLost: %v", err)
	}
	if got := hostStatus(t, db, "lost-1"); got != "deleted" {
		t.Errorf("status = %q, want deleted", got)
	}
	if got := quotaUsed(t, db); got != 0 {
		t.Errorf("used_hosts after mark = %d, want 0", got)
	}

	// Idempotent re-mark: no error, no second decrement (floor aside, the
	// deleted short-circuit must fire before the quota read).
	if err := repo.MarkHostLost(ctx, "lost-1", "again"); err != nil {
		t.Fatalf("re-mark: %v", err)
	}
	if got := quotaUsed(t, db); got != 0 {
		t.Errorf("used_hosts after re-mark = %d, want 0", got)
	}
}

// TestMarkHostLostRefusesVMlessStatuses pins the in-transaction eligibility
// guard: hosts whose VM legitimately may not exist (mid-provision, mid-
// teardown) cannot be declared lost, and the refusal is the typed
// InvalidStateError callers treat as a benign race.
func TestMarkHostLostRefusesVMlessStatuses(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)

	if err := db.SeedQuota(ctx, 10, 2); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		id     string
		status host.Status
	}{
		{"lost-provisioning", host.StatusProvisioning},
		{"lost-deleting", host.StatusDeleting},
	} {
		if err := db.SeedHost(ctx, tc.id, tc.id, "us-central1-a"); err != nil {
			t.Fatal(err)
		}
		// SeedHost rows are running; move to the VM-less status under test.
		if _, err := db.Client.Apply(ctx, []*spanner.Mutation{spanner.Update("hosts",
			[]string{"host_id", "status"}, []any{tc.id, string(tc.status)})}); err != nil {
			t.Fatal(err)
		}

		err := repo.MarkHostLost(ctx, tc.id, "stale sync observation")
		var invalid *host.InvalidStateError
		if !errors.As(err, &invalid) {
			t.Fatalf("MarkHostLost(%s in %s) = %v, want *host.InvalidStateError", tc.id, tc.status, err)
		}
		if got := hostStatus(t, db, tc.id); got != string(tc.status) {
			t.Errorf("status changed to %q, want %q untouched", got, tc.status)
		}
	}
	if got := quotaUsed(t, db); got != 2 {
		t.Errorf("used_hosts = %d, want 2 (refused marks must not release quota)", got)
	}
}
