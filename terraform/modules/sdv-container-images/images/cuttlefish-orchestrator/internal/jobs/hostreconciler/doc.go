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

// Package hostreconciler reconciles host rows with observed GCE reality.
//
// One leader-gated cycle (runCycle) runs four programs, and the order
// matters:
//
//  1. Preemption evidence scan (checkPreemptions). Reads recent zone
//     operations (compute.instances.preempted, plus system-principal
//     deletes, the reclaim's visible tail when the preempted operation never
//     appears) and marks Spot hosts preempted (transition-validated,
//     deduped by operation ID). Marked hosts are handed to durable
//     re-provisioning through the queue, bounded per host and suspended
//     cycle-wide by the zone recovery breaker; each detection is written to
//     the audit log with its recovery disposition, and a breaker trip
//     records preemption-storm cooldown cells in zone_health. This scan runs
//     BEFORE the zone sync so absence classification never races its own
//     detector.
//  2. Zone drift sync (SyncZone). Compares host rows against the zone's
//     listed instances. A VM deleted outside CCO marks its host lost,
//     failing over the host's live devices in the same commit (bounded per
//     cycle; Spot absence is first classified against the operation
//     evidence). Out-of-band status changes are adopted via host.DerivePhase
//     with CAS-preconditioned batch writes, and app-owned lifecycle states are
//     never overridden. Hosts sitting in a transitional status with no
//     status write are flagged through keyed host.reconcile suspicion tasks.
//  3. Orphan-VM sweep (sweepOrphanVMs). Aggregated-lists managed instances
//     across every zone and flags those no live host row claims. Dry-run by
//     default (Config.DeleteOrphanVMs enables deletion, capped per cycle).
//     Stale-incarnation husks, CCO's own structural garbage, are deleted
//     even in dry-run.
//  4. Quota reconcile (reconcileQuotas). Heals used_hosts counter drift via
//     quota.Service.ReconcileAll.
//
// The direction is GCE→DB: the inventory adopts observed reality. The only
// GCE writes this job issues are the orphan/husk deletions in program 3.
//
// Configure via Config (zones, interval, managed-instance identity,
// blast-radius caps); optional duties are wired via Options (quota service,
// preemption recovery, operations client, zone health, audit logger,
// reconcile enqueuer). Leader-gated under the hostreconciler lease; metrics
// land under cco.hostreconciler.*.
package hostreconciler
