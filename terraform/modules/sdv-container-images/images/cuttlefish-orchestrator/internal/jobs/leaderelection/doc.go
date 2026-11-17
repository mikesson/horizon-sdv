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

// Package leaderelection elects a single active instance for the background
// plane.
//
// An Elector renews a named lease in the background and exposes IsLeader so a
// job loop runs its work on exactly one instance at a time. The others no-op.
// This keeps the periodic jobs (the host reconciler, the reaper, the task
// reclaimer, the fleet keeper and the rest) from multiplying GCE API calls,
// table scans, and deletes when several instances serve concurrently.
//
// One election covers all of them: every job takes the same IsLeader, from one
// Elector on the BackgroundPlaneLease row. Per-job leases would be seven leases
// renewing forever to say the same thing, and they cannot shard a job's work
// anyway. A plane that outgrows one instance is fixed by sharding the work,
// which is a second election beside this one. The Elector itself is unchanged
// by that choice: it elects whatever lease it is given.
//
// Leadership is best-effort and carries no fencing token. On a renewal error
// the Elector steps down locally, and the lease's TTL (kept well above the renew interval)
// bounds how long a crashed leader's lease blocks a successor. Jobs gated by it
// must already be idempotent, so a brief no-leader gap or a rare double-run at a
// handoff is harmless. Correctness never rests on the election, only on how
// much duplicate work a handoff costs.
//
// Nothing here reads platform metadata or assumes an instance count, a
// co-location with request traffic, or a graceful shutdown: the plane needs
// Spanner and a unique process identity (InstanceID), which a Cloud Run
// revision, a VM hostname and a pod name all supply.
//
// See docs/development/control-plane.md.
package leaderelection
