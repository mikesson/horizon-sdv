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
	"fmt"

	"cloud.google.com/go/spanner"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// ClaimableShards names the shards that hold claimable work, in one
// single-use read-only query. Read-only takes no locks and commits nothing, so
// an empty queue costs a read instead of the read-write transaction every poll
// used to open and close over nothing.
//
// Why the query is shaped this way:
//   - One EXISTS per shard, over UNNEST of the shard list, so the cost is one
//     index seek per shard however deep the backlog is. A scan of the
//     claimable range (SELECT DISTINCT shard) answers the same question by
//     reading every eligible entry, which is cheapest exactly when the answer
//     matters least.
//   - The subquery projects a constant, so idx_tasks_claimable_v2 covers it
//     and the non-covering back-join the claim pays for never happens here.
//   - FORCE_INDEX for the same reason the claim needs it: Spanner picks a
//     non-covering index only when directed, and an undirected probe would
//     full-scan tasks.
//
// The result is a hint. A named shard may be drained by a peer before the
// claim arrives, and a shard whose task commits a moment later is missed until
// the next poll; the claim re-reads and re-validates everything either way.
func (q *TaskQueue) ClaimableShards(ctx context.Context) ([]int, error) {
	shards := make([]int64, q.numShards)
	for i := range shards {
		shards[i] = int64(i)
	}
	iter := q.client.Single().Query(ctx, spanner.Statement{
		SQL: `SELECT s FROM UNNEST(@shards) AS s
		      WHERE EXISTS (
		          SELECT 1 FROM tasks@{FORCE_INDEX=idx_tasks_claimable_v2}
		          WHERE shard = s AND state = @pending
		            AND available_at <= CURRENT_TIMESTAMP())`,
		Params: map[string]any{
			"shards":     shards,
			paramPending: string(task.StatePending),
		},
	})
	defer iter.Stop()

	var claimable []int
	if err := iter.Do(func(row *spanner.Row) error {
		var shard int64
		if err := row.Columns(&shard); err != nil {
			return fmt.Errorf("scanning claimable shard: %w", err)
		}
		claimable = append(claimable, int(shard))
		return nil
	}); err != nil {
		return nil, fmt.Errorf("probing claimable shards: %w", err)
	}
	return claimable, nil
}
