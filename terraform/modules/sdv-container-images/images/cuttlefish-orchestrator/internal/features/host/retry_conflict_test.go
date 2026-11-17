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
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// A retry while a provisioning task is still live (pending or leased) trips
// the transition-task unique index. That must surface as the state conflict
// it is, since the queued journey will drive the host, and never as a raw
// storage error rendered as a 500.
func TestRetryConflictsWithLiveProvisionTask(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	ctx := t.Context()
	if err := db.SeedQuota(ctx, 4, 0); err != nil {
		t.Fatalf("seed quota: %v", err)
	}
	repo := hostfeature.NewHostRepository(db.Client)
	tq := spannerplatform.NewTaskQueue(db.Client,
		spannerplatform.WithShards(task.DefaultShards), spannerplatform.WithHolder("test"))

	h := newTestHost("retry-busy")
	provisionMut := tq.EnqueueMutation(ctx, task.New(hostfeature.TaskTypeProvisionHost, h.ID))
	if err := repo.CreateWithQuotaIncrement(ctx, h, provisionMut); err != nil {
		t.Fatalf("CreateWithQuotaIncrement: %v", err)
	}

	// The host errors retryably while its provision task is still live.
	if _, err := repo.Mutate(ctx, h.ID, func(h *host.Host) error {
		now := time.Now()
		h.Status = host.StatusError
		h.ErrorMessage = "zone exhausted"
		h.ErrorOccurredAt = &now
		h.ErrorRetryable = true
		return nil
	}); err != nil {
		t.Fatalf("erroring host: %v", err)
	}

	svc := hostfeature.NewHostService(hostfeature.HostServiceConfig{}, nil, nil, repo,
		slog.New(slog.DiscardHandler), hostfeature.WithTaskEnqueuer(tq))
	_, err := svc.Retry(ctx, h.ID, false)
	if !errors.Is(err, host.ErrInvalidState) {
		t.Fatalf("err = %v, want host.ErrInvalidState (provisioning task still live)", err)
	}
}
