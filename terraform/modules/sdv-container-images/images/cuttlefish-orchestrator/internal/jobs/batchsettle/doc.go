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

// Package batchsettle enforces batch policy in the background: it is the
// atomic fill policy's single enforcement point plus garbage collection of
// settled batches, extracted from the reaper so batch settlement keeps its
// own cadence and its own metrics.
//
// It runs on EVERY instance, without the background plane's leader gate. The
// settle markers are compare-and-set and the deletes are idempotent, so the
// same properties that made leader churn safe make concurrency safe, and the
// gate bought nothing while costing the fleet a serialization point. What
// concurrent instances would otherwise collide on is de-correlated in the
// listings, which come back in a per-call random order (see
// platform/spanner.SweepSalt).
//
// Each cycle runs, in order:
//
//   - Atomic-batch settlement: judges every unsettled atomic batch from a
//     strong member roll-up, latching ready_at on full fill, or tripping
//     failed_at and tearing the members down. The MarkReady/MarkFailed
//     settle markers are compare-and-set, so no two sweeps can double-judge
//     a batch. The admission path never settles, because a keyed retry must
//     stay able to resume, which makes this sweep the policy's single
//     enforcement point.
//   - Batch collection: memberless, non-failed batch rows past the resume
//     grace are garbage-collected (failed rows are kept for post-mortems).
//   - Drained-host reclamation: exclusive batch hosts that have settled to
//     no live devices are deleted ahead of their TTL. They can never be
//     placed onto again, so keeping them is pure cost.
//
// DryRun logs what would happen without deleting. Metrics land under
// cco.batchsettle.*.
package batchsettle
