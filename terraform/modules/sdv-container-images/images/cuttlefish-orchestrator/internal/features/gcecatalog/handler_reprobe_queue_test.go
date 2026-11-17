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

package gcecatalogfeature_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
	gcecatalogfeature "cloud-cuttlefish-orchestrator/internal/features/gcecatalog"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/spannertest"
)

// The cell key is the task's ONLY argument, so it has to survive the durable
// round trip intact: a claimed task carrying a mangled resource id would probe
// the wrong cell, or none, and the handler is the only thing that would notice.
//
// It also pins the dispatch key against the queue: a task type the worker pool
// has no handler for postpones until it dead-letters, and the pool matches on
// exactly the string stored here.
func TestReprobeTaskSurvivesTheQueueRoundTrip(t *testing.T) {
	t.Parallel()
	db := spannertest.MustNewTestDB(t)
	defer db.Close()
	q := cspanner.NewTaskQueue(db.Client, cspanner.WithShards(1), cspanner.WithHolder("worker-a"))

	filed := task.New(task.TypeCatalogReprobeSpotAdvice, "europe-west4-b/n1-standard-8")
	filed.Priority = task.PriorityResync
	require.NoError(t, q.Enqueue(t.Context(), filed))

	claimed, err := q.Claim(t.Context(), 0, 1, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	h := gcecatalogfeature.NewReprobeAdviceHandler(nil, nil, nil)
	assert.Equal(t, h.TaskType(), claimed[0].Type,
		"the stored type must be the one the pool dispatches on")
	assert.Equal(t, "europe-west4-b/n1-standard-8", claimed[0].ResourceID)
	assert.Equal(t, task.PriorityResync, claimed[0].Priority)
}
