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

package taskworker

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// shortWorkReserveShare is the part of the pool's width that long-occupancy
// work may never claim into: one worker in four, and never fewer than one.
//
// A share rather than a second worker count, because what it divides is already
// a floor and a ceiling (16 workers held back at the ceiling of 64, 2 at the
// floor of 8), and a fixed reserve would either take a small pool's whole width
// or vanish against a large one.
const shortWorkReserveShare = 4

// longOccupancyTypes are the task types whose handler holds its worker for
// minutes AND that something other than a person files in bulk. Both halves
// matter: the reserve exists for a class that can outrun the pool, and a class
// that arrives one request at a time cannot.
//
//   - A CVD create blocks on the host's `cvd load` for 94-156 s, and a cold one
//     fetches multi-GB artifacts before that (>10 m observed). Its host-side
//     wait is sized per guest, so a group create holds longer still. One task is
//     filed per device of a batch create, per zombie recreate, and per stranded
//     create the device reconcile heals.
//   - A capture-on-error bugreport waits out the host's capture on the extended
//     verb envelope (~4 m) and then streams the artifact off the host into
//     object storage, which sits outside any client budget by design. The
//     device controller files one per error device, and error devices arrive
//     together: a poisoned host fails every create that lands on it.
//
// Left out on purpose: the in-place device verbs (restart, powerwash,
// snapshot, restore) hold a worker for minutes too, but each is one user
// request and the operation registry admits one exclusive operation per device,
// so nothing files them faster than the pool retires them. Host provision and
// deprovision hold tens of seconds around a GCE operation, and provisioning is
// what a deferred create is waiting for, so budgeting it would starve the work
// that drains the class the budget exists to bound.
var longOccupancyTypes = map[string]bool{
	task.TypeDeviceCreateCVD:        true,
	task.TypeDeviceCaptureBugreport: true,
}

// isLongOccupancy reports whether a task type is budgeted by the bulkhead.
func isLongOccupancy(taskType string) bool { return longOccupancyTypes[taskType] }

// bulkhead bounds how much of the pool the long-occupancy class may hold at
// once, so short control work (a host teardown, a keyed reconcile, a verb)
// always has a worker left to claim it.
//
// It budgets against the pool's CURRENT width instead of its ceiling, which is
// what makes it work at the floor. Growth is observed on claims, so a
// floor-width pool with every worker inside a create observes nothing and never
// widens (see sizer): a ceiling-derived budget would admit all 8 of those
// creates and leave the pool blind for a whole handler hold, where a
// width-derived one admits 6 and the 2 it holds back keep claiming, which is
// what produces the aged claims that take the pool to its ceiling, where the
// budget is 48.
//
// Enforcement is post-claim by construction: the claim filters on shard, state
// and available_at with no type predicate (see unregisteredTypeGrace for what
// depends on that), so the class of a task is known only once it is held.
type bulkhead struct {
	sizer *sizer

	// inFlight is the long-occupancy count, moved only through admit and
	// release so a check and its increment cannot straddle another worker's.
	inFlight atomic.Int64
}

func newBulkhead(s *sizer) *bulkhead { return &bulkhead{sizer: s} }

// budget is how many workers the long class may hold at the pool's current
// width.
func (b *bulkhead) budget() int64 { return longWorkBudget(b.sizer.active.Load()) }

// longWorkBudget holds a quarter of a pool that wide back for short work, and
// at least one worker. The result is itself floored at one: a pool narrow
// enough that the reserve would take its only worker still runs the long work
// rather than deferring it forever.
func longWorkBudget(width int64) int64 {
	return max(1, width-max(1, width/shortWorkReserveShare))
}

// admit takes one long-occupancy slot, reporting whether the budget had one.
// A caller that gets true owes a release.
func (b *bulkhead) admit() bool {
	for {
		held := b.inFlight.Load()
		if held >= b.budget() {
			return false
		}
		if b.inFlight.CompareAndSwap(held, held+1) {
			return true
		}
	}
}

func (b *bulkhead) release() { b.inFlight.Add(-1) }

// admits reports whether a delivery holds anything the pool could run now.
// It is advisory only, because every slot is taken through admit under the
// counter, so a race here costs at most one wake and never a slot.
func (b *bulkhead) admits(tasks []*task.Task) bool {
	for _, t := range tasks {
		if !isLongOccupancy(t.Type) {
			return true
		}
	}
	return b.inFlight.Load() < b.budget()
}

// Deferral of a task claimed while its class was already at budget.
const (
	// bulkheadDeferBase and bulkheadDeferJitter bound how soon such a task
	// becomes claimable again.
	//
	// Long enough that the pool digs past a backlog before its head returns:
	// the reserve declines one claimed task per queue round trip, so a deep
	// backlog is pushed forward in well under a second and the short work
	// behind it becomes the head. Short enough to be noise against the hold it
	// waits on: a create holds its worker for 94-156 s, so this is what
	// noticing a freed slot costs, not what waiting for one does.
	//
	// Jittered because a backlog is deferred in one pass: unjittered it comes
	// back in lockstep against a budget that frees one slot at a time, and
	// every task in it is declined together a second time.
	bulkheadDeferBase   = 5 * time.Second
	bulkheadDeferJitter = 5 * time.Second
)

// deferOverBudget returns a claimed task to pending because its class already
// holds its share of the pool.
//
// Postpone rather than Fail: the task did nothing wrong and must not burn an
// attempt. Declined often enough, a create would otherwise dead-letter without
// ever having run. Postpone also moves AvailableAt forward, which takes the
// deferral back out of the sizer's starvation signal: the next claim of this
// task reads as fresh, so the pool never widens on work it declined itself.
// What still counts is the age the claim already observed (see pollOnce),
// because that backlog is the queue's property, not this decision's.
func (p *Pool) deferOverBudget(ctx context.Context, log *slog.Logger, t *task.Task) {
	//nolint:gosec // deferral jitter; not security-sensitive
	delay := bulkheadDeferBase + time.Duration(rand.Int64N(int64(bulkheadDeferJitter)))
	reason := fmt.Sprintf("long-running task types hold their share of the pool (%d of %d workers)",
		p.bulkhead.inFlight.Load(), p.sizer.active.Load())
	log.Info("task deferred to keep the pool claimable for short work",
		slog.String("reason", reason),
		slog.Duration("retry_after", delay))
	if err := p.bookkeep(ctx, func(bctx context.Context) error {
		return p.queue.Postpone(bctx, t, reason, delay)
	}); err != nil {
		log.Error("failed to defer task over its class budget; task stays leased until swept",
			slog.Any("error", err))
		return
	}
	p.postponed.Add(1)
	p.bulkheadDeferred.Add(1)
}
