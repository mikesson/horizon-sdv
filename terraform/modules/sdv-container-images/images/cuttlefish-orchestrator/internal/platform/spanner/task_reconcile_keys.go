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

package spanner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// The keyed-reconcile helpers shared by enqueue (coalesce-merge), the
// delivery bookkeeping (release-with-merge), and the sweeper (expired-lease
// merge): at most one PENDING task per (type, resource) is a database
// invariant via the unique reconcile_key index, and these helpers are how
// every path honors it.

// leasedSiblingExists reports whether a live-leased task of the same (type,
// resource) exists, as an index seek via idx_tasks_by_resource, inside the
// claim's transaction. resource_id is the one nullable column of that index's
// key, so it carries the explicit IS NOT NULL the null-filtered index requires;
// task_type and state are declared NOT NULL and can exclude no row. ("Bound
// non-null" is NOT the justification. That is optimizer inference, and relying
// on it is what left the operations reconciling query refused in production.)
// A leased row whose lease has already lapsed does not count: its handler is
// provably dead (the pool enforces LeaseDuration > HandleTimeout), and letting
// it block the key would hand a stuck row a veto over its own replacement.
//
// This plain SELECT is validated only under Spanner's default SERIALIZABLE
// isolation; under repeatable read it would be unvalidated and two workers
// could reconcile one resource concurrently. Never relax the isolation of the
// claim transaction. See docs/development/architecture-decisions.md (ADR-1).
func (q *TaskQueue) leasedSiblingExists(ctx context.Context, txn *spanner.ReadWriteTransaction, taskType, resourceID string) (bool, error) {
	iter := txn.Query(ctx, spanner.Statement{
		SQL: `@{spanner_emulator.disable_query_null_filtered_index_check=true}
		      SELECT 1 FROM tasks@{FORCE_INDEX=idx_tasks_by_resource}
		      WHERE resource_id IS NOT NULL
		        AND task_type = @type AND resource_id = @resource AND state = @leased
		        AND lease_expiry > CURRENT_TIMESTAMP()
		      LIMIT 1`,
		Params: map[string]any{
			"type":     taskType,
			"resource": resourceID,
			"leased":   string(task.StateLeased),
		},
	})
	defer iter.Stop()
	_, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("probing leased sibling of %s/%s: %w", taskType, resourceID, err)
	}
	return true, nil
}

// pendingSibling is the pending dirty-bit row for a reconcile key, read for
// merge decisions.
type pendingSibling struct {
	id          string
	priority    int64
	attempts    int64
	availableAt time.Time
}

// pendingSiblingForKey returns the pending reconcile row for (taskType,
// resourceID), reporting found=false when the key has none. It runs inside
// txn so the merge decision commits atomically with the write it guards; the
// unique reconcile_key index makes "at most one" a database invariant, not a
// hope.
func (q *TaskQueue) pendingSiblingForKey(ctx context.Context, txn *spanner.ReadWriteTransaction, taskType, resourceID string) (pendingSibling, bool, error) {
	iter := txn.Query(ctx, spanner.Statement{
		// reconcile_key is NULL for every task that is not a pending reconcile,
		// the vast majority, so the null-filtered index drops them and the query
		// states that rather than leaving it to inference.
		SQL: `@{spanner_emulator.disable_query_null_filtered_index_check=true}
		      SELECT task_id, priority, attempts, available_at
		      FROM tasks@{FORCE_INDEX=idx_tasks_reconcile_key}
		      WHERE reconcile_key IS NOT NULL AND reconcile_key = @key`,
		Params: map[string]any{"key": taskType + "/" + resourceID},
	})
	defer iter.Stop()
	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return pendingSibling{}, false, nil
	}
	if err != nil {
		return pendingSibling{}, false, fmt.Errorf("probing pending sibling of %s/%s: %w", taskType, resourceID, err)
	}
	var s pendingSibling
	if err := row.Columns(&s.id, &s.priority, &s.attempts, &s.availableAt); err != nil {
		return pendingSibling{}, false, fmt.Errorf("scanning pending sibling of %s/%s: %w", taskType, resourceID, err)
	}
	return s, true, nil
}

// mergeSiblingMutation folds a removed delivery's retry history into the
// surviving pending sibling. Attempts and priority take the max, so dead-letter
// pressure and urgency both survive the merge and a persistently failing
// reconcile still converges on the DLQ. available_at takes the min, so an edge
// event never inherits a failed attempt's backoff.
func mergeSiblingMutation(s pendingSibling, attempts, priority int64, availableAt time.Time) *spanner.Mutation {
	return spanner.UpdateMap(tasksTable, map[string]any{
		colTaskID:      s.id,
		colAttempts:    max(s.attempts, attempts),
		colPriority:    max(s.priority, priority),
		colAvailableAt: minTime(s.availableAt, availableAt),
		ColUpdatedAt:   timeNow(),
	})
}
