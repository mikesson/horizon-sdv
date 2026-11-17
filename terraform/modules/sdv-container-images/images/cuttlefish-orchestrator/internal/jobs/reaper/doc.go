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

// Package reaper enforces retention policy in the background: expiry and
// pin lapse are decided here.
//
// It runs on EVERY instance rather than on the background plane's leader: every
// action below is idempotent under a concurrent double-run (the delete
// transition, the unpin's pinned-state check), so the gate bought nothing while
// costing the fleet a serialization point. What concurrent instances would
// otherwise collide on is de-correlated in the listings, which come back in a
// per-call random order (see platform/spanner.SweepSalt).
//
// Each cycle runs, in order:
//
//   - Pin lapse: pinned devices whose pinned_until has passed are unpinned
//     via the same Unpin path users call, restoring a fresh ephemeral TTL.
//     This runs first so a just-lapsed pin gets a normal wind-down instead
//     of being caught by the expiry sweep in the same cycle.
//   - Device TTL reap, then host TTL reap, both through the concrete
//     feature services (the exact Delete paths users hit, so teardown is
//     queued, quota-correct, and never duplicated logic), batch-deleting
//     devices where the service supports it.
//
// Atomic-batch settlement and batch garbage collection, which used to ride
// these cycles, live in the batchsettle job. DryRun logs what would happen
// without deleting. Metrics land under cco.reaper.*.
package reaper
