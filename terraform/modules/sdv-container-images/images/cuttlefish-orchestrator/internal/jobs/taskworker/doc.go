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

// Package taskworker runs the in-process pool that drains the durable Spanner
// task queue.
//
// Worker goroutines each claim eligible tasks, dispatch every task to its
// registered task.Handler, and complete or fail it. Concurrency is bounded by
// the worker count, which is the natural fit for long-running handlers (e.g.
// CVD creation). There is no per-task goroutine. The pool depends only on the
// task.Queue and task.Handler contracts, never on a feature, so feature
// handlers are registered at wiring time in main.
//
// The count is not fixed: each instance sizes its own active workers between a
// floor and a compiled ceiling from the AGE of the tasks it claims, growing on
// the first claim older than a healthy poll explains and giving a worker back
// per interval of pool-wide quiet. An instance with nothing to do therefore
// costs exactly what the floor costs. See sizer.go.
//
// One pool serves every task type, so a class that both holds its workers for
// minutes and arrives in bulk, such as a flood of CVD creates, could otherwise
// occupy all of them and leave a host teardown or a keyed reconcile nothing to
// be claimed by. A share of the pool's current width is therefore held back
// from that class, enforced after the claim (which carries no type
// predicate) by postponing what it cannot admit. See bulkhead.go.
//
// Discovery is separate from the claim. A worker first asks the queue which
// shards hold claimable work, a non-locking read that commits nothing, and
// opens the read-write claim only against a shard that answered yes, so an
// empty queue costs reads instead of transactions that write nothing. When the
// answer is "nothing anywhere" the worker waits, stretching that wait toward
// PollCap for as long as the queue stays empty.
//
// Delivery is at-least-once: a worker leases a task, runs the handler outside
// any database transaction, then completes it (delete) or fails it (backoff, or
// dead-letter when exhausted). Work interrupted by a graceful shutdown is
// postponed back to pending with the attempt refunded; work interrupted by a
// crash is redelivered once its lease lapses. Handlers must be idempotent
// either way.
//
// See docs/development/control-plane.md.
package taskworker
