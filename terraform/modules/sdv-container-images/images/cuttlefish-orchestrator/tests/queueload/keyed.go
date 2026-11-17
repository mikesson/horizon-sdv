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

package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	gspanner "cloud.google.com/go/spanner"
	"github.com/google/uuid"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// The keyed scenario covers the ineligible-keyed-head shape the claim's
// starvation rescue exists for: a pending keyed reconcile whose sibling is
// leased is skipped by filterEligible, and while the sibling's handler runs,
// everything at equal or lower priority behind that head depends on the claim
// stepping past it. Two halves:
//
//   - The stuck-head PROBE (before the instances spawn, so nothing races the
//     driver's claims) builds the shape deterministically, with a leased
//     sibling, a pending dirty-bit head and an equal-priority task parked
//     behind it on the same shard. It then asserts the production Claim
//     returns the parked task. Without the claim's wider second look it
//     returns empty, so the probe is the gate. It uses no wall clock, because
//     the emulator's global write serialization makes latency thresholds
//     meaningless here.
//   - The load phase then churns keyed dirty-bits for a fixed resource set
//     (handlers hold each sibling for -keyed-handler) under the synthetic
//     load at the same resync priority, and the standard conservation, drain
//     and quiescence gates prove the queue stays live around stuck heads.

// keyedResourcePrefix names the fixed resource set the keyed churn cycles
// over.
const keyedResourcePrefix = "keyres-"

// keyedHandler processes the keyed reconcile type with a fixed latency, which
// is how long a leased sibling blocks its pending dirty-bit head. Keyed tasks
// stay out of the effect ledger on purpose: coalescing merges discard task IDs, so
// ID-level conservation cannot hold for them; the quiescence and DLQ checks
// still cover their rows.
type keyedHandler struct {
	cfg       Cfg
	processed atomic.Int64
}

func (h *keyedHandler) TaskType() string { return task.TypeDeviceReconcile }

func (h *keyedHandler) Handle(ctx context.Context, _ *task.Task) error {
	timer := time.NewTimer(h.cfg.KeyedHandler)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("keyed handler interrupted: %w", ctx.Err())
	case <-timer.C:
	}
	h.processed.Add(1)
	return nil
}

// keyedPreflight runs the stuck-head probe before any instance spawns (the
// driver's claims must not race worker claims); a non-keyed scenario is a
// no-op.
func (d *driver) keyedPreflight(ctx context.Context) error {
	if d.cfg.Scenario != scenarioKeyed {
		return nil
	}
	if err := d.probeStuckHead(ctx); err != nil {
		return fmt.Errorf("keyed stuck-head probe: %w", err)
	}
	fmt.Println("keyed: stuck-head probe OK; the claim stepped past the ineligible head")
	return nil
}

// probeStuckHead is the keyed scenario's gate: with the production Claim
// against real rows, an ineligible keyed head (pending dirty-bit whose
// sibling is leased) must be stepped past, not returned as an empty claim
// that parks the work behind it for the sibling's whole handler runtime.
// Runs before any instance spawns and unwinds every row it created.
func (d *driver) probeStuckHead(ctx context.Context) error {
	const res = keyedResourcePrefix + "probe"
	if err := d.queue.EnqueueResync(ctx, task.TypeDeviceReconcile, res, 0); err != nil {
		return fmt.Errorf("filing the sibling: %w", err)
	}
	sibID, sibShard, err := d.pendingReconcileRow(ctx, "")
	if err != nil {
		return err
	}
	sibling, err := d.claimExactly(ctx, sibShard, sibID)
	if err != nil {
		return fmt.Errorf("leasing the sibling: %w", err)
	}

	// The dirty bit: pending, same (type, resource), ineligible while the
	// sibling stays leased.
	if err := d.queue.EnqueueResync(ctx, task.TypeDeviceReconcile, res, 0); err != nil {
		return fmt.Errorf("filing the dirty bit: %w", err)
	}
	headID, headShard, err := d.pendingReconcileRow(ctx, sibID)
	if err != nil {
		return err
	}

	// Park an equal-priority task behind the head on the head's shard: it is
	// younger, so the head sorts first in claim order.
	parked := task.New("load.probe", "probe")
	parked.ID = idOnShard(headShard, d.cfg.Shards)
	parked.Priority = task.PriorityResync
	if err := d.queue.Enqueue(ctx, parked); err != nil {
		return fmt.Errorf("parking the probe task: %w", err)
	}

	claimed, err := d.queue.Claim(ctx, headShard, 1, time.Minute)
	if err != nil {
		return fmt.Errorf("claiming past the stuck head: %w", err)
	}
	if len(claimed) != 1 || claimed[0].ID != parked.ID {
		got := "empty"
		if len(claimed) > 0 {
			got = claimed[0].ID
		}
		return fmt.Errorf("claim on shard %d returned %s, want the task parked behind the ineligible keyed head %s (head-of-line starvation)",
			headShard, got, parked.ID)
	}

	// Unwind: ack the probe and the sibling, then the head (eligible once its
	// sibling is gone), leaving the table as the load phase expects it.
	if err := d.queue.Complete(ctx, claimed[0]); err != nil {
		return fmt.Errorf("completing the probe task: %w", err)
	}
	if err := d.queue.Complete(ctx, sibling); err != nil {
		return fmt.Errorf("completing the sibling: %w", err)
	}
	head, err := d.claimExactly(ctx, headShard, headID)
	if err != nil {
		return fmt.Errorf("draining the head: %w", err)
	}
	if err := d.queue.Complete(ctx, head); err != nil {
		return fmt.Errorf("completing the head: %w", err)
	}
	return nil
}

// pendingReconcileRow returns the ID and shard of the single pending keyed
// reconcile row other than exclude, through the driver's direct client.
func (d *driver) pendingReconcileRow(ctx context.Context, exclude string) (string, int, error) {
	iter := d.raw.Single().Query(ctx, gspanner.Statement{
		SQL: `SELECT task_id, shard FROM tasks
		      WHERE task_type = @type AND state = 'pending' AND task_id != @exclude`,
		Params: map[string]any{"type": task.TypeDeviceReconcile, "exclude": exclude},
	})
	defer iter.Stop()
	row, err := iter.Next()
	if err != nil {
		return "", 0, fmt.Errorf("locating the pending reconcile row: %w", err)
	}
	var (
		id    string
		shard int64
	)
	if err := row.Columns(&id, &shard); err != nil {
		return "", 0, fmt.Errorf("scanning the pending reconcile row: %w", err)
	}
	return id, int(shard), nil
}

// claimExactly claims one task on shard and verifies it is the expected row.
// The probe owns the whole table, so anything else is a harness bug.
func (d *driver) claimExactly(ctx context.Context, shard int, want string) (*task.Task, error) {
	claimed, err := d.queue.Claim(ctx, shard, 1, time.Minute)
	if err != nil {
		return nil, fmt.Errorf("claiming on shard %d: %w", shard, err)
	}
	if len(claimed) != 1 || claimed[0].ID != want {
		return nil, fmt.Errorf("claim on shard %d returned %d task(s), want exactly %s", shard, len(claimed), want)
	}
	return claimed[0], nil
}

// idOnShard generates a task ID that lands on the given shard.
func idOnShard(shard, numShards int) string {
	for {
		if id := uuid.New().String(); task.ShardFor(id, numShards) == shard {
			return id
		}
	}
}
