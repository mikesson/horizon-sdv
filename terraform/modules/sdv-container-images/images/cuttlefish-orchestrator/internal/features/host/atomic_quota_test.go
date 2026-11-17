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
	"sync"
	"testing"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/idempotency"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

func newTestHost(id string) *host.Host {
	return &host.Host{
		ID: id, Name: id, Zone: "us-central1-a",
		Status: host.StatusPending, MaxDevices: 4, HostOrchestratorPort: 2080,
	}
}

func quotaUsed(t *testing.T, db *spannertest.TestDB) int64 {
	t.Helper()
	row, err := db.Client.Single().ReadRow(t.Context(), "quotas",
		spanner.Key{"platform", "global"}, []string{"used_hosts"})
	if err != nil {
		t.Fatalf("read quota: %v", err)
	}
	var n int64
	if err := row.Columns(&n); err != nil {
		t.Fatalf("scan quota: %v", err)
	}
	return n
}

func countTasks(t *testing.T, db *spannertest.TestDB, resourceID string) int64 {
	t.Helper()
	iter := db.Client.Single().Query(t.Context(), spanner.Statement{
		SQL:    "SELECT COUNT(*) FROM tasks WHERE resource_id = @r",
		Params: map[string]any{"r": resourceID},
	})
	defer iter.Stop()
	row, err := iter.Next()
	if err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	var n int64
	if err := row.Columns(&n); err != nil {
		t.Fatalf("scan count: %v", err)
	}
	return n
}

func TestHostRepository_CreateWithQuotaIncrement(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedQuota(ctx, 2, 0); err != nil {
		t.Fatalf("seed quota: %v", err)
	}
	repo := hostfeature.NewHostRepository(db.Client)
	tq := spannerplatform.NewTaskQueue(db.Client, spannerplatform.WithShards(task.DefaultShards), spannerplatform.WithHolder("test"))

	h := newTestHost("hq-1")
	taskMut := tq.EnqueueMutation(ctx, task.New(hostfeature.TaskTypeProvisionHost, h.ID))

	if err := repo.CreateWithQuotaIncrement(ctx, h, taskMut); err != nil {
		t.Fatalf("CreateWithQuotaIncrement: %v", err)
	}
	if _, err := repo.Get(ctx, h.ID); err != nil {
		t.Errorf("host not created: %v", err)
	}
	if got := quotaUsed(t, db); got != 1 {
		t.Errorf("used_hosts = %d, want 1", got)
	}
	// The provision task committed in the same transaction as the host + quota.
	if n := countTasks(t, db, h.ID); n != 1 {
		t.Errorf("provision tasks for host = %d, want 1", n)
	}
}

func TestHostRepository_CreateWithQuotaIncrement_Exceeded(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedQuota(ctx, 1, 1); err != nil { // already at capacity
		t.Fatalf("seed quota: %v", err)
	}
	repo := hostfeature.NewHostRepository(db.Client)

	h := newTestHost("hq-full")
	err := repo.CreateWithQuotaIncrement(ctx, h)
	var exceeded *quota.ExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("want *quota.ExceededError, got %v", err)
	}
	if _, getErr := repo.Get(ctx, h.ID); getErr == nil {
		t.Error("host must not be created when quota is exceeded")
	}
	if got := quotaUsed(t, db); got != 1 {
		t.Errorf("used_hosts = %d, want unchanged 1", got)
	}
}

// ReconcileUsedHosts heals only real drift: an accurate counter is untouched,
// a corrupted one (a lost decrement, an errored host still counted) converges
// to the live quota-holding count in one transaction.
func TestHostRepository_ReconcileUsedHosts(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedQuota(ctx, 10, 0); err != nil {
		t.Fatalf("seed quota: %v", err)
	}
	repo := hostfeature.NewHostRepository(db.Client)

	for _, id := range []string{"rq-1", "rq-2"} {
		if err := repo.CreateWithQuotaIncrement(ctx, newTestHost(id)); err != nil {
			t.Fatalf("CreateWithQuotaIncrement(%s): %v", id, err)
		}
	}

	// Counter and reality agree -> no drift, no write.
	counter, actual, err := repo.ReconcileUsedHosts(ctx)
	if err != nil {
		t.Fatalf("ReconcileUsedHosts: %v", err)
	}
	if counter != 2 || actual != 2 {
		t.Errorf("(counter, actual) = (%d, %d), want (2, 2)", counter, actual)
	}

	// An errored host holds no quota: the recount must exclude it and heal
	// the counter down.
	if _, err := repo.Mutate(ctx, "rq-2", func(h *host.Host) error {
		h.Status = host.StatusError
		return nil
	}); err != nil {
		t.Fatalf("erroring rq-2: %v", err)
	}
	counter, actual, err = repo.ReconcileUsedHosts(ctx)
	if err != nil {
		t.Fatalf("ReconcileUsedHosts: %v", err)
	}
	if counter != 2 || actual != 1 {
		t.Errorf("(counter, actual) = (%d, %d), want (2, 1)", counter, actual)
	}
	if got := quotaUsed(t, db); got != 1 {
		t.Errorf("used_hosts = %d, want healed to 1", got)
	}

	// Healing is idempotent.
	if counter, actual, err = repo.ReconcileUsedHosts(ctx); err != nil || counter != actual {
		t.Errorf("(counter, actual, err) = (%d, %d, %v), want a no-op second pass", counter, actual, err)
	}
}

func TestHostRepository_DeleteWithQuotaDecrement(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedQuota(ctx, 4, 0); err != nil {
		t.Fatalf("seed quota: %v", err)
	}
	repo := hostfeature.NewHostRepository(db.Client)

	h := newTestHost("hq-del")
	if err := repo.CreateWithQuotaIncrement(ctx, h); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := quotaUsed(t, db); got != 1 {
		t.Fatalf("setup used_hosts = %d, want 1", got)
	}

	if err := repo.DeleteWithQuotaDecrement(ctx, h.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := quotaUsed(t, db); got != 0 {
		t.Errorf("used_hosts = %d, want 0", got)
	}
	if _, err := repo.Get(ctx, h.ID); err == nil {
		t.Error("host should be deleted")
	}
}

// TestHostRepository_DeleteWithQuotaDecrement_Concurrent is the Q4 invariant: two
// reap cycles (or a deprovision redelivery overlapping the leader) can call
// DeleteWithQuotaDecrement for the same host at once. The host-existence read
// joins the serializable read-set, so the loser aborts, retries, re-observes the
// row gone, and no-ops. Quota is decremented exactly once, never double-freed.
func TestHostRepository_DeleteWithQuotaDecrement_Concurrent(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedQuota(ctx, 4, 0); err != nil {
		t.Fatalf("seed quota: %v", err)
	}
	repo := hostfeature.NewHostRepository(db.Client)

	h := newTestHost("hq-race")
	if err := repo.CreateWithQuotaIncrement(ctx, h); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := quotaUsed(t, db); got != 1 {
		t.Fatalf("setup used_hosts = %d, want 1", got)
	}

	const racers = 2
	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i := range racers {
		wg.Go(func() {
			errs[i] = repo.DeleteWithQuotaDecrement(ctx, h.ID)
		})
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("racer %d: DeleteWithQuotaDecrement = %v, want nil", i, err)
		}
	}
	if got := quotaUsed(t, db); got != 0 {
		t.Errorf("used_hosts = %d, want exactly 0 (decremented once, not %d times)", got, 1-got)
	}
	if _, err := repo.Get(ctx, h.ID); !errors.Is(err, host.ErrNotFound) {
		t.Errorf("host should be deleted, got %v", err)
	}
}

func newQueueAndIdem(db *spannertest.TestDB) (*spannerplatform.TaskQueue, *spannerplatform.IdempotencyRepository) {
	return spannerplatform.NewTaskQueue(db.Client, spannerplatform.WithShards(task.DefaultShards), spannerplatform.WithHolder("test")),
		spannerplatform.NewIdempotencyRepository(db.Client)
}

func TestHostService_Create_Idempotent(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedQuota(ctx, 10, 0); err != nil {
		t.Fatalf("seed quota: %v", err)
	}
	repo := hostfeature.NewHostRepository(db.Client)
	tq, idem := newQueueAndIdem(db)
	svc := hostfeature.NewHostService(hostfeature.HostServiceConfig{}, nil, nil, repo, slog.Default(),
		hostfeature.WithTaskEnqueuer(tq), hostfeature.WithHostIdempotencyStore(idem))

	req := host.CreateRequest{Name: "hi-1", Zone: "us-central1-a", MaxDevices: 4, IdempotencyKey: "k1"}
	h1, err := svc.Create(ctx, req, "user@example.com", host.OriginUser)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	h2, err := svc.Create(ctx, req, "user@example.com", host.OriginUser)
	if err != nil {
		t.Fatalf("retried create: %v", err)
	}
	if h1.ID != h2.ID {
		t.Errorf("retry with the same key returned a different host: %s != %s", h1.ID, h2.ID)
	}
	// Only one provision task -> only one host actually created.
	if n := countTasks(t, db, h1.ID); n != 1 {
		t.Errorf("provision tasks = %d, want 1 (no second create)", n)
	}
	if got := quotaUsed(t, db); got != 1 {
		t.Errorf("used_hosts = %d, want 1", got)
	}
}

func TestHostService_Create_IdempotencyConflict(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedQuota(ctx, 10, 0); err != nil {
		t.Fatalf("seed quota: %v", err)
	}
	repo := hostfeature.NewHostRepository(db.Client)
	tq, idem := newQueueAndIdem(db)
	svc := hostfeature.NewHostService(hostfeature.HostServiceConfig{}, nil, nil, repo, slog.Default(),
		hostfeature.WithTaskEnqueuer(tq), hostfeature.WithHostIdempotencyStore(idem))

	if _, err := svc.Create(ctx, host.CreateRequest{Name: "hc-1", Zone: "us-central1-a", MaxDevices: 4, IdempotencyKey: "k1"}, "u", host.OriginUser); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := svc.Create(ctx, host.CreateRequest{Name: "hc-2", Zone: "us-central1-a", MaxDevices: 4, IdempotencyKey: "k1"}, "u", host.OriginUser)
	if !errors.Is(err, idempotency.ErrConflict) {
		t.Errorf("reused key with a different request: got %v, want idempotency.ErrConflict", err)
	}
}

func TestHostService_DeleteAndDeprovision(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedQuota(ctx, 10, 0); err != nil {
		t.Fatalf("seed quota: %v", err)
	}
	repo := hostfeature.NewHostRepository(db.Client)
	tq, _ := newQueueAndIdem(db)
	svc := hostfeature.NewHostService(hostfeature.HostServiceConfig{}, nil, nil, repo, slog.Default(),
		hostfeature.WithTaskEnqueuer(tq))

	h := newTestHost("hdp-1")
	h.GCEInstanceName = "" // RunDeprovision skips the (nil) GCE client
	if err := repo.CreateWithQuotaIncrement(ctx, h); err != nil {
		t.Fatalf("setup create: %v", err)
	}

	// Delete marks the host deleting and enqueues a deprovision task (fast, durable).
	if err := svc.Delete(ctx, h.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err := repo.Get(ctx, h.ID)
	if err != nil {
		t.Fatalf("host should still exist (deleting): %v", err)
	}
	if got.Status != host.StatusDeleting {
		t.Errorf("status = %q, want deleting", got.Status)
	}
	if n := countTasks(t, db, h.ID); n != 1 {
		t.Errorf("deprovision tasks = %d, want 1", n)
	}

	// The deprovision handler removes the host row and frees quota atomically.
	if err := svc.RunDeprovision(ctx, h.ID, ""); err != nil {
		t.Fatalf("deprovision: %v", err)
	}
	if _, err := repo.Get(ctx, h.ID); !errors.Is(err, host.ErrNotFound) {
		t.Errorf("host should be removed, got %v", err)
	}
	if got := quotaUsed(t, db); got != 0 {
		t.Errorf("used_hosts = %d, want 0 after deprovision", got)
	}
	// Idempotent: a redelivery after removal no-ops.
	if err := svc.RunDeprovision(ctx, h.ID, ""); err != nil {
		t.Errorf("repeat deprovision = %v, want nil", err)
	}
}

// TestHostService_RunProvision_Idempotent covers the guard paths that do not touch
// GCE (so a nil instances client is safe): a missing host and an already-running
// host both no-op without error or state change.
func TestHostService_RunProvision_Idempotent(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	repo := hostfeature.NewHostRepository(db.Client)
	svc := hostfeature.NewHostService(hostfeature.HostServiceConfig{}, nil, nil, repo, slog.Default())

	if err := svc.RunProvision(ctx, "does-not-exist", ""); err != nil {
		t.Errorf("RunProvision(missing) = %v, want nil", err)
	}

	h := newTestHost("hp-run")
	h.Status = host.StatusRunning
	if err := repo.Create(ctx, h); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.RunProvision(ctx, h.ID, ""); err != nil {
		t.Errorf("RunProvision(running) = %v, want nil (no-op)", err)
	}
	got, err := repo.Get(ctx, h.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != host.StatusRunning {
		t.Errorf("status changed to %q; RunProvision must no-op on a running host", got.Status)
	}
}
