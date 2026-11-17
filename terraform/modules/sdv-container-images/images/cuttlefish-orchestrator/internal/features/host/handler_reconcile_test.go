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
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// The host.reconcile handler is the keyed convergence entry point: it heals
// preempted hosts (durable re-provision) and ownerless transitional hosts
// (re-enqueued transition task), and no-ops everywhere else.
func TestReconcileHostHandler(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	ctx := t.Context()
	require.NoError(t, db.SeedQuota(ctx, 10, 0))

	repo := hostfeature.NewHostRepository(db.Client)
	queue, _ := newQueueAndIdem(db)
	svc := hostfeature.NewHostService(hostfeature.HostServiceConfig{}, nil, nil, repo, slog.Default(),
		hostfeature.WithTaskEnqueuer(queue))
	h := hostfeature.NewReconcileHostHandler(svc, queue, slog.Default())

	mustHost := func(id string, status host.Status) {
		require.NoError(t, repo.Create(ctx, &host.Host{
			ID: id, Name: id, Zone: "z", Status: status, MaxDevices: 4,
			ProvisioningModel: host.ProvisioningModelSpot,
		}))
	}
	hasActive := func(taskType, id string) bool {
		active, err := queue.HasActive(ctx, taskType, id)
		require.NoError(t, err)
		return active
	}

	// Preempted -> durable re-provision: status transitions and a provision
	// task is enqueued (Retry's atomic reset+enqueue).
	mustHost("h-preempted", host.StatusPreempted)
	require.NoError(t, h.Handle(ctx, &task.Task{ResourceID: "h-preempted"}))
	got, err := repo.Get(ctx, "h-preempted")
	require.NoError(t, err)
	assert.Equal(t, host.StatusProvisioning, got.Status)
	assert.True(t, hasActive(task.TypeHostProvision, "h-preempted"))
	// Immutable-infrastructure recovery: the re-provision bumped the
	// incarnation and targets a FRESH instance name, so the previous
	// incarnation's husk can never collide with it.
	assert.Equal(t, int64(1), got.Incarnation)
	assert.True(t, strings.HasSuffix(got.GCEInstanceName, "-i1"),
		"recovered instance name must embed the new incarnation, got %q", got.GCEInstanceName)

	// Transitional with no live task -> the transition is re-enqueued.
	mustHost("h-stranded", host.StatusProvisioning)
	require.NoError(t, h.Handle(ctx, &task.Task{ResourceID: "h-stranded"}))
	assert.True(t, hasActive(task.TypeHostProvision, "h-stranded"))

	mustHost("h-del", host.StatusDeleting)
	require.NoError(t, h.Handle(ctx, &task.Task{ResourceID: "h-del"}))
	assert.True(t, hasActive(task.TypeHostDeprovision, "h-del"))

	// Transitional WITH a live task -> owned; nothing enqueued twice.
	require.NoError(t, h.Handle(ctx, &task.Task{ResourceID: "h-stranded"}))
	stats, err := queue.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), stats.Pending, "no duplicate transition tasks")

	// Steady state and missing host -> no-op completes.
	mustHost("h-running", host.StatusRunning)
	require.NoError(t, h.Handle(ctx, &task.Task{ResourceID: "h-running"}))
	require.NoError(t, h.Handle(ctx, &task.Task{ResourceID: "nonexistent"}))
	stats, err = queue.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), stats.Pending)
}
