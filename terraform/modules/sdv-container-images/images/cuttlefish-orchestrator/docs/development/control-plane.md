# The Control Plane: Task Queue, Workers, and Reconciliation

How CCO's background machinery works: the durable task queue inside Spanner,
the worker pool that drains it, the sweeper that makes it crash-proof, and
the reconciliation loops that keep the database in agreement with GCE and the
Host Orchestrators. Read [backend.md](backend.md) first for the package
layout; read [architecture-decisions.md](architecture-decisions.md) for the
two invariant sets (serializable isolation, server-evaluated ownership time)
this document leans on throughout.

One design principle runs through everything here. **Spanner is the only
stateful dependency.** There is no Cloud Tasks, no Pub/Sub, no Temporal. The
queue is a table, leader election is a table, the durable-execution journal is
a table, and all of them commit atomically with the resources they govern.
(What Compute Engine offers, meaning zones, shapes and quota, is not held as
state here. Each instance caches it in an in-memory snapshot refreshed from
the GCE API, because Google holds those facts on an API with better
availability than any mirror of them.)

## 1. Overview

CCO composes two loop styles, and knowing which one owns a behavior tells
you where to look:

- **Transitions** (create → provision → boot → register; delete → teardown)
  are bounded, ordered journeys. They run as **durable queue tasks**.
- **Maintenance** (drift, health, TTL, orphan collection) is unbounded
  convergence toward a desired state. It runs as **periodic reconciliation
  jobs**, which increasingly *detect* problems and enqueue keyed reconcile
  tasks rather than acting inline.

```
        INTENT (request path)                    OBSERVATION (periodic jobs)
        ─────────────────────                    ───────────────────────────
  POST /v2/devices ─▶ feature service    hostreconciler devicecontroller reaper
                          │                     │           │            │
                          │ ONE read-write      │  leader-gated, ~1/min  │
                          │ transaction:        │  detect → enqueue      │
                          │ resource row        │  keyed reconcile       │
                          │ + counters          │                        │
                          │ + idempotency key   │                        │
                          │ + TASK (outbox)     │                        │
                          ▼                     ▼                        ▼
    ┌─────────────────────────────────────────────────────────────────────┐
    │                            Cloud Spanner                            │
    │   hosts / devices / quotas      tasks (+ task_steps interleaved)    │
    │   idempotency_keys              leader_leases                       │
    └─────────────────────────────────────────────────────────────────────┘
                          ▲
                          │ read-only probe, then
                          │ Claim / Complete / Fail / Postpone
                          │ (fenced per delivery)
              worker pool: 8 goroutines × EVERY instance
                          │
                          ▼
              task handlers ─▶ GCE API / Host Orchestrator (HO)
              (all external I/O OUTSIDE any Spanner transaction)
```

Work and periodic jobs coordinate differently:

- The **worker pool runs on every instance**. The claim transaction is
  serializable, so any number of instances can drain the queue safely.
- **All periodic jobs run on one instance**, under one leader lease
  named `background-plane` that every job takes. N instances multiplying GCE
  list calls, table scans, and deletes would be waste, and would add no safety.

## 2. The task queue

`internal/domain/task` defines the pure domain (Task, states, Queue and
Handler contracts); the `task_*.go` files in
`internal/platform/spanner` are the implementation. One row per task:

```
                    EnqueueMutation, folded into the PRODUCER's transaction
                    (the transactional outbox: task commits with the resource
                     it acts on, or neither does)
                                     │
                                     ▼
                  ┌──────────────────────────────────┐
        ┌────────▶│             pending              │
        │         └────────────────┬─────────────────┘
        │                          │ Claim: SELECT … FOR UPDATE, then one DML
        │                          │ attempts += 1,
        │                          │ lease_expiry = CURRENT_TIMESTAMP()+lease
        │                          │ … THEN RETURN (server-assigned, ADR-2)
        │                          ▼
        │         ┌──────────────────────────────────┐
   Fail: backoff  │              leased              │── Complete = row DELETE
   Postpone: no   └────────────────┬─────────────────┘   (journal rows cascade
   attempt burned                  │                       away with the row)
   Sweep: lease lapsed             │ attempts ≥ max_attempts
        │                          ▼   (on Fail or on sweep)
        │         ┌──────────────────────────────────┐
        └─ ─ ─ ─  │          failed  (DLQ)           │  operator surface:
                  └──────────────────────────────────┘  cfctl admin tasks
```

Mechanics, in the `task_*.go` files:

- **Discovery is read-only; only the claim pays for a transaction.**
  `ClaimableShards` (`task_probe.go`) asks the claimable index which shards
  hold ready work (EXISTS per shard, one covered read-only statement, no
  locks, no commit). An empty answer means no read-write transaction
  runs at all. The probe is a hint and the claim is the authority, so a stale
  probe costs one empty claim and never a wrong one. Idle, this is the
  difference between ~25,000 zero-byte commits/hr and a few hundred
  read-only probes.
- **Sharding.** `shard = fnv32a(task_id) % 16` prefixes the claim index
  (`idx_tasks_claimable_v2` on `(shard, state, priority DESC,
  available_at)`), so claims spread across Spanner splits instead of
  funneling into one hot key range. Workers claim a random probe-named
  shard.
- **The claim query shape cannot be changed freely.** `FORCE_INDEX` is there
  because Spanner never auto-picks a non-covering index. Without it every
  claim would scan the table under read locks. `FOR UPDATE` is there because
  pessimistic locking makes
  contended claimers queue briefly instead of abort-storming (see ADR-1;
  this is the documented Spanner shape for high write contention).
- **Delete-on-complete** keeps the table small; the DLQ scan needs no index.
  A 90-day TTL row-deletion policy removes failed rows.
- **Backoff** is exponential with equal jitter (1s base, 5m cap), 10
  attempts by default, then dead-letter. `Postpone` means the task is waiting
  and has not failed. Attempts are refunded, and the handler owns a wall-clock
  budget instead.

### The delivery fence

Every bookkeeping write (`Complete`/`Fail`/`Postpone`) is fenced on the
**delivery**, not the instance:

```sql
WHERE task_id = @id AND lease_holder = @holder
  AND attempts = @claimed AND lease_expiry = @expiry
```

Why three components. `lease_holder` alone identifies the *instance*, and
after an early sweep the same instance can re-claim its own task, leaving a
zombie holder-matched. `attempts` alone aliases across postpone-separated
deliveries (Postpone refunds the count). The **server-assigned
`lease_expiry` is unique per claim** and is what finally names the delivery.
The same triple seeds the GCE `requestId`, so the actuation-side dedup and
the bookkeeping fence agree about what a "delivery" is.

```
 t0  claim #1  (attempts=1, expiry=E1) ── handler wedges…
 t1  sweeper: E1 < CURRENT_TIMESTAMP() → row back to pending
 t2  claim #2  (attempts=2, expiry=E2) ── handler runs fine
 t3  zombie #1 wakes → Complete(attempts=1, expiry=E1)
        WHERE … attempts=1 AND lease_expiry=E1  →  0 rows.  Fenced out,
        logged (the only trace an ownership handoff leaves).
 t4  #2 → Complete(attempts=2, expiry=E2)  →  row deleted.
```

A fenced-out ack is *success* (the live delivery owns the outcome, and
handlers are idempotent), and it is always logged. Three of the four write
paths pin a fifth predicate, `state = 'leased'`. Complete alone omits it,
soundly: no path leaves a non-NULL `lease_expiry` on a non-leased row, so the
expiry match implies leased.

### Ownership time is server time (ADR-2)

Every lease stamp and every expiry comparison evaluates `CURRENT_TIMESTAMP()`
inside the owning statement. That covers the claim, the sweep re-verify, the
leased-sibling probe and the leader leases. No correctness decision ever
compares two instances' clocks. Cross-instance lease safety is bounded by
TrueTime, and NTP does not enter into it. The `timeNow` indirection in
`clock.go` governs *scheduling* values only (enqueue `available_at` defaults
and the enqueue-side merge floor; the requeue and sweep merge floors are
already server-side). Lease writes must therefore stay DML, because buffered
mutations cannot call `CURRENT_TIMESTAMP()`.

## 3. Task types and their producers

Fourteen types. The four *transition* types carry a journey; the two *keyed
reconcile* types are convergence checks; two are detector-fed maintenance
actions; one settles a user host stop; the five *verb operation* types back
the AIP-151 device verbs. Every producer that writes a resource folds the
enqueue into that same transaction (the outbox: task and resource commit
together or not at all). The detector
and healer filings are standalone commits. They are durable once written, and
safe to lose before that because every detector is level-triggered, so the
next cycle re-observes and re-files.

| Type | Producer(s), all transactional | Handler does |
|---|---|---|
| `host.provision` | `HostService.Create` (host row + quota + idempotency key + task, one commit); `HostService.Retry` (in-txn `CanRetry` + status reset + incarnation bump + task) | GCE Insert (deterministic name + per-delivery `requestId`) → wait → guarded status write. A classified capacity failure (stockout, quota, availability) additionally records the zone in `zone_health` and, while attempts remain and the host is not pinned, moves the host to the best other ranked zone with a fresh incarnation before the next delivery (zone failover; see [managing-hosts](../guides/managing-hosts.md)) |
| `host.deprovision` | `HostService.Delete` (in-txn `deleting` transition + task; double-delete no-ops before enqueue) | GCE Delete (`notFound` ok) → hard row delete + quota decrement |
| `host.stop_settle` | `HostService.Stop` (in-txn `stopping` transition + `desired_state=stopped` + task, after GCE accepted the stop; the stop's `requestId` derives from the task ID) | re-issues the stop with the same `requestId` to get the same operation back → wait → on a parked VM, the reconciler's CAS observation write moves the host `stopping` → `stopped` and its running devices to `stopped` in one commit. A failed stop operation, or the exhausted final attempt, returns `desired_state` to `running`, which moves the host back out of `stopping` |
| `host.remediate` | the device controller's degraded-host sweep (a host repeatedly failing CVD creates with a leaked-tap signature, settled to zero live devices) | in-place HO `cvd reset`, gated on a live-device count of zero |
| `device.create_cvd` | `DeviceService.Create` (device row + host counter + capacity check + task) | HO `cvd load` via the operation journal (§5) → in-txn transition to `running` |
| `device.cleanup_cvd` | folded into the device-delete mutation; dropped when the delete no-ops | probe-first CVD group delete |
| `device.capture_bugreport` | the device controller's failed-capture detector (policy-gated: `artifacts.capture_on_error`, default off) | host-side bugreport captured to GCS; stamps `bugreport_captured_at` so a handled row never re-enqueues |
| `host.reconcile` *(keyed)* | host-reconciler preemption-recovery + stale-transitional detectors | preempted → `Retry`; stranded transition → re-enqueue it |
| `device.reconcile` *(keyed)* | device-controller stale-teardown, stranded-create + zombie suspectors | teardown convergence via `RunCleanupCVD` (launch-in-flight wait bounded by task age; finishes an interrupted delete's tombstone + counter commit); a zombie CVD on a running host gets an in-place restart, then a budgeted recreate from `desired_spec` |
| `device.restart_operation`, `device.powerwash_operation`, `device.snapshot_operation`, `device.snapshot_delete_operation`, `device.snapshot_restore_operation` | `OperationsRepository.Admit` writes the operation row and the task in one commit (the AIP-151 handles behind the synchronous-verb ceiling) | drive the verb through the HO and settle the operation row terminally. Retry budgets come from the operation registry, per verb: destructive or unobservable actuation (powerwash, snapshot take, restore) gets `MaxAttempts` 1 so the sweep dead-letters instead of redelivering it. A verb that loses the admission-to-execution race to a lifecycle change (device deleted, stopping, row gone) settles the operation failed-with-conflict instead of dead-lettering. The operation row is the durable record of a verb that could not run, and the queue stays clean of non-failures |

The worker pool dispatches by type; an *unregistered* type is postponed
through a 15-minute grace first (across a rolling deploy the old revision
routinely claims a new type's tasks), then force dead-lettered by collapsing
`MaxAttempts` to the attempts already used (mutating attempts upward would
break the fence). Handler panics are recovered into normal failures, because
this process also serves the API.

## 4. The worker pool and the reclaimer

`internal/jobs/taskworker`: 8 workers per instance, `ClaimBatch=1`,
`HandleTimeout` 30m under a 35m lease. **The margin is the only guard
against a handler that is alive but stuck racing its replacement** (the
fence catches the *writes*, not the work; see the invariant in
architecture-decisions.md).

Discovery pacing: after an empty probe the delay is jittered between
`PollMin` 200ms and `PollMax` 2s, then stretches exponentially to the
`PollCap` of 30s while the queue stays empty, and resets on any wake or
non-empty probe. The wake is `task.Waker`, a process-local buffered
channel the dispatch loop selects on. Self-committing enqueues wake it
internally; mutation-riding producers wake it after their commit succeeds.
It is **best-effort and lossy by design** (dropped when full, never
persisted). The poll floor is the delivery guarantee, the wake is the
latency path, and a missed wake costs one backoff interval and never a task.
Enqueue-to-claim latency for request-driven work is microseconds on the
enqueuing instance; cross-instance pickup is bounded by the 30s cap and
only matters when the enqueuing instance cannot process its own work.

The outcome ladder in `process()`:

```
 handler returns…            worker does…
 ───────────────             ────────────────────────────────────────────
 nil                         Complete (row delete)
 *task.RescheduleError       Postpone(after), no attempt burned; the
                             handler bounds total waiting by wall clock
 *task.DelayHintError        Fail with retryAfter = max(hint, backoff), so
                             rate-limited cloud calls (429/Retry-After)
                             don't come back on the generic curve
 any other error             Fail → backoff … or dead-letter if exhausted
```

Bookkeeping runs on `context.WithoutCancel` with 3×10s retries, so a deploy
mid-task still records the outcome and an ambiguous commit is retried
blindly (safe: the writes are fenced and idempotent). A graceful shutdown
postpones the interrupted attempt instead of burning it as a failure. The
attempt is refunded and claimable by the replacement instance in seconds.
Only a crash leaves a task leased until the sweep.

`internal/jobs/taskreclaimer` is crash recovery: every minute (leader-gated)
it scans the null-filtered `idx_tasks_lease_expiry` index with a **bounded
staleness** read (safe because every write batch re-verifies
`lease_expiry < CURRENT_TIMESTAMP()` server-side), requeues expired leases,
and dead-letters the exhausted. It tolerates bad rows among the ones it
recovers. Keyed candidates are handled per-row (their requeue may need a
sibling merge), and a failed batch degrades to per-row recovery so one bad
row can never block crash recovery for the rest.

## 5. The keyed reconcile queue (workqueue semantics)

Reconcile tasks get the controller-runtime treatment, expressed in schema:

```sql
reconcile_key STRING(MAX) AS (IF(state = 'pending'
    AND task_type IN ('host.reconcile', 'device.reconcile'),
    CONCAT(task_type, '/', resource_id), NULL)) STORED
-- + UNIQUE NULL_FILTERED INDEX: at most ONE PENDING task per key
```

```
 detector enqueue           claim                     event lands mid-run
       │                      │                              │
       ▼                      ▼                              ▼
 [pending key=h/1] ──────▶ [leased key=NULL, running]   [pending key=h/1]
       ▲                      │        the "dirty bit": recorded, not lost,
       │                      │        NOT dispatched while the sibling runs
 2nd enqueue MERGES:          │        (claim skips keys with a live leased
 priority = max,              │         sibling, an index point-probe)
 available_at = min           ▼
                        Complete → the dirty bit becomes claimable:
                        exactly one follow-up run, never a concurrent one
```

- **Coalesce-merge:** an edge event hitting an existing pending row raises
  its priority (edge 0 > resync −100) and pulls `available_at` forward, so it
  is never dropped behind a waiting resync. The merge loses nothing because
  a task carries nothing to lose. A task's only argument is its
  `resource_id`, and handlers derive everything from resource state. That
  is also what makes any adjacent binary generation safe to claim any task
  across a rolling deploy.
- **Per-key serialization:** the claim skips a key with a live leased
  sibling via a targeted point lookup. A correlated subquery inside the
  locking claim was tried and rejected: that shape serialized all claimers,
  and the queueload harness caught it at ~78s/claim. The claim over-scans a
  few candidates past its batch, so a busy key's dirty bit never starves
  the shard behind it.
- **Requeue merges:** a keyed task going back to pending when a dirty-bit
  sibling already holds the unique slot is *merged into the sibling*
  (attempts and priority take the max, so DLQ pressure survives) instead of
  colliding with it.
- The sibling probe is a plain SELECT validated only under serializable
  isolation, one of the three ADR-1 sites. Don't touch the claim's
  isolation.

## 6. The operation journal (durable execution as a table)

`task_steps(task_id, step_id) → step_name, output` is interleaved in `tasks`
with `ON DELETE CASCADE`, so journal rows die with their task. It memoizes
exactly one thing today: the name of the in-flight remote operation
(`cvd_load`, the HO operation UUID). A redelivery **resumes polling the
same operation** instead of re-actuating or adopting a half-finished launch
by observation. Records are written *after* the remote accepts
(at-least-once); a resumed operation that settled as failed clears the
journal and falls through to the list/adopt/husk ladder in the same attempt,
so a stored failure is never re-read to exhaustion.

Provision and deprovision are not journaled. The per-delivery GCE
`requestId`, the deterministic instance names and the adopt guard already give
resume-equivalent semantics.

## 7. The reconciliation loops

The leader-gated jobs (`internal/jobs/`), each a level-triggered sweep;
cycle health is observable via `telemetry.LoopMetrics` (duration histogram +
`last_success_age` gauge) with per-cycle timeouts.

| Job | Every | What one cycle does |
|---|---|---|
| `hostreconciler` | 1m | Per zone, **in this order**: operations scan (preemption + delete-op evidence, two channels with principal attribution) → preemption marks (in-txn FSM-gated) + recovery via keyed `host.reconcile` enqueue, re-filed level-style for any host still sitting preempted (a failed enqueue or a breaker-suspended cycle must not strand it) → instance-list drift sync (CAS status writes classified {benign, adopted, blocked}). Then once per cycle, not per zone: orphan-VM sweep (aggregatedList, dry-run gated, expected husks auto-deleted) → quota `ReconcileAll` (transactional level recount of `used_hosts`). Blast-radius gates: lost-mark budget per sync interval (counted from the durable tombstones themselves, so overlapping sweeps during a leader handoff share one budget), zone Spot-recovery breaker. |
| `devicecontroller` | 1m | Batched device health via HO. Devices on non-serving hosts get *failure* records, never frozen health; first HO contact promotes a `starting` host to `running` (the `ho_ready_at` latch); CVD census (listed groups vs device rows, 10m grace) catches orphan groups. Post-boot suspicion comes from the boot-watch log tail and is independent of the CVD enum, because a boot-looping guest latches the fleet status at Running, so the launcher log is the only signal that can fire. Its detectors enqueue instead of acting: stale teardowns and stranded creates → `device.reconcile`, zombie devices likewise when zombie recovery is enabled (config-gated, default off), settled degraded hosts → `host.remediate`, uncaptured error devices → `device.capture_bugreport` (policy-gated). |
| `reaper` | 1m | Lapsed pins → expired devices → expired hosts, in that order (an unpin first restores a fresh TTL). Acts only through the services, so every delete is the same durable, idempotent queue journey as a user delete. Underneath it sits the VM-side failsafe: every host VM carries a `max_run_duration` of 120 days (the GCE maximum, action STOP), so even a dead control plane cannot leak a VM forever. Leases have no cap, so the reconciler records when each VM will stop (`vm_terminates_at`) and every lease raise is refused past it. |
| `batchsettle` | 1m | Atomic-fill settlement (latch ready_at on full fill, or trip failed_at + tear members down; CAS settle markers) → resume of tripped batches whose winner died mid-teardown (level check over failed batches with live members) → emptied batch-row GC → drained batch-host early reap. |
| `fleetkeeper` | 30s | The fleet autoscaler. Composes the standby target (standing intent, floor, schedule windows, demand signal, max-wins), then acts only as `fleet.autoscale_mode` permits: provisions standby hosts through the same `HostService.Create` journey as a user create (origin-stamped `pool`, which is what derives the host's `fleet` class), renews or rescues draining fleet hosts, scales in by **shortening leases** (the reaper stays the sole executioner, so a wrong scale-in costs a timestamp, and a device commit raising its host's `expires_at` in the same transaction makes a draining host rescuable for the whole grace), and collapses empty dedicated hosts. Each cycle also advances the warm plane's bake machine (`internal/jobs/warmplane` has no lease of its own; it rides this cycle through a port). The string `poolkeeper` survives only as the keeper's `created_by` actor stamp (informational, and counted by the robustness census). The lease it once named is gone. |
| `catalogsync` | 15m | Spot capacity-advice probe, wired only when `CCO_SPOT_ADVICE` is on. Exempt from the per-cycle budget, because a fan-out over every enabled cell may legitimately outlast its interval. What Compute Engine offers is not synced at all; it lives in a per-instance in-memory snapshot. |
| `taskreclaimer` | 1m | §4's lease reclaim. |

### Drift detection

Reality lives in three places; each pair has a detector:

```
      GCE instances  ◀── hostreconciler ──▶  hosts rows
        (VM state,          ops scan,        status / vm_status /
         orphan VMs)        CAS drift sync,  incarnation
                            orphan-VM GC
      CVD groups     ◀── devicecontroller ─▶  devices rows
        (on each VM's       health polls,    health_status,
         HO)                CVD census       reconcile suspicion
      used_hosts     ◀── hostreconciler ReconcileAll ─▶ host rows
                            (one-transaction recount; the device counters
                             have no recount, only the folded deltas)
```

Detected drift is classified at detection as `benign` (expected churn),
`adopted` (out-of-band but acceptable) or `blocked` (an app-owned state holds
the enum). Remediation is budgeted: a mass-drift anomaly degrades to an
error log plus the `remediation_deferred` counter, never a mass cascade.

The **`zone_health`** table records classified capacity failures per (zone,
machine family). The provision failure path writes it, zone ranking
reads it (cooldowns filter, lapsed failures decay the score), and a
successful provision in the cell clears it. One host's stockout therefore
steers every pending provision away from the dead zone without any per-task
state.
Quiet cells age out under a 7-day row-deletion policy.

An operator can clear a cell by hand with
`DELETE /v2/admin/zone-health/{zone}/{machine_family}` (`cfctl admin zone
clear-cooldown`, or the per-zone menu on the Control Plane's placement
panel), for when the cause is fixed and the decay term would otherwise
outlive it by days. The clear is audited as `zone.cooldown_clear`; a quota
cell repels its whole region, so clearing one reopens every zone in that
region for the family.

### Column ownership (who may write what)

Every observation column has exactly one writer; a writer touching a column
it does not own is a review-level offense:

| Column | Sole writer |
|---|---|
| `desired_state` (spec) | intent paths: Create, Start, Stop, Delete, Retry, MarkHostLost |
| `requested_zone`, `zone_pinned` (spec) | Create only. The placement intent is immutable after admission |
| `zone` (binding) | Create; zone failover may rewrite it (CAS-guarded, pre-serving states only) |
| `vm_status`, `vm_observed_at` | observation writers, three of them: the hostreconciler drift sweep (CAS-fenced), the provision read-back, and the manual `POST /hosts/{id}/sync` |
| `status` | lifecycle writers via transactional `Mutate`/`TransitionTo` |
| `health_status` (devices) | devicecontroller (plus a reset to `unknown` by the CVD create and zombie-recreate handlers, since a fresh CVD must not inherit stale health) |
| `incarnation`, `gce_instance_name` | Create (0) + Retry and zone failover (bump + rename, one txn) |
| preemption fields | hostreconciler (`MarkPreempted`, in-txn FSM-gated) |

`incarnation` is the immutable-infrastructure trick: a retry or preemption
recovery bumps it and the GCE instance name embeds it, so recovery always
targets a *fresh* instance. The previous incarnation's dying husk can never
be adopted. It becomes an unclaimed name for Spot auto-delete or the orphan
GC.

## 8. Idempotency: how at-least-once becomes safe

Delivery is at-least-once and handlers re-derive everything from resource
state. The layers, outermost first:

| Layer | Mechanism |
|---|---|
| API requests | `Idempotency-Key` + request fingerprint (`idempotency_keys`, committed with the create) |
| Task delivery | the fence (§2); stale bookkeeping cannot clobber a live delivery |
| GCE actuation | per-delivery `requestId` (UUIDv5 over task, attempts, lease expiry), so a redelivered Insert returns the original operation; deterministic instance names → `alreadyExists` → adopt-guard (adopt RUNNING/STAGING, reschedule on transient states, never actuate on a husk) |
| HO actuation | the operation journal (§6): resume, don't re-launch; the adopt-guard postpones on `Starting`/`Stopping` groups |
| Terminal writes | in-txn re-read + FSM check (`Mutate`/`TransitionTo`), so a deleted resource is never resurrected by a slow handler |
| Counters/quota | folded into the same transaction as the transition. `used_hosts` additionally has `ReconcileAll`'s one-transaction recount, a periodic full recount that corrects drift; the device counters have no recount, and their correctness depends only on the folded, idempotent deltas |

## 9. How Spanner is used (the capability map)

| Capability | Where and why |
|---|---|
| Serializable + pessimistic (the default, untouched) | Everything. Three transactions *depend* on serializable read-validation: quota RMW, capacity RMW, the sibling probe. See ADR-1 before touching isolation anywhere. |
| `CURRENT_TIMESTAMP()` in DML + `THEN RETURN` | All ownership time (§2, ADR-2). The claim reads its server-assigned lease back; mutations can't do this, hence DML. |
| Transactional outbox (mutation folding) | `EnqueueMutation` is buffered into producer transactions, so task and resource commit atomically; no dual-write problem exists to solve. |
| `FOR UPDATE` + `FORCE_INDEX` | The claim (§2). Spanner requires the index hint for non-covering indexes. |
| Interleaved tables + `ON DELETE CASCADE` | `devices` in `hosts` (host teardown cascades rows); `task_steps` in `tasks` (journal dies with the task). |
| Generated columns + `UNIQUE NULL_FILTERED` indexes | `reconcile_key` (§5): state-dependent uniqueness the PK cannot express. `transition_key` generalizes it to the four transition types, allowing at most one live (pending or leased) transition task per resource and making the transition enqueue a plain insert whose duplicate the index rejects. |
| TTL row-deletion policies | `tasks` 90d, `audit_logs` 90d, `batches` 90d (failed rows kept for post-mortems; emptied ones GC'd sooner by the reaper), `operations` 90d (must outlive `idempotency_keys`), `idempotency_keys` 30d, host/device tombstones 7d (`tombstoned_at`; NULL rows never expire), `zone_health` 7d (quiet cells age out), `spot_advice` 1d. Every history table is bounded. Row hygiene only; the reaper owns business TTL. |
| Bounded-staleness reads (`StaleSingle`, 10s) | List/dashboard reads, queue observability (`Stats`, `ListFailed`), the sweep candidate scan, meaning anything re-verified or merely informative. Strong reads stay wherever a read feeds an actuation decision. (Zone/shape/quota availability is not a Spanner read at all; it comes from the in-process GCE snapshot.) |
| Conditional DML as CAS | Drift writes (`UPDATE … WHERE status = @prev`, applied-count returned), the fence, the sweep re-verify. Mutations cannot carry preconditions; row count is the verdict. |
| Emulator | The whole suite runs against it (`cco-spanner-emu`, :9010). One production-ignored statement hint matters: the null-filtered-index check disable, which asserts the query carries the explicit `IS NOT NULL` real Spanner demands. Remember that the emulator serializes RW transactions globally, so never benchmark on it. |

## 10. Leader election

`leader_leases` + `internal/jobs/leaderelection`. ONE `Elector` for the whole
background plane on the `background-plane` row, 45s TTL, renewal every 15s,
holding the instance identity that also serves as the queue's `lease_holder`.
Acquire/renew is a single predicate DML, takeable when unheld, mine, or
expired, all evaluated on server time. An elector that cannot confirm renewal
steps down locally (a brief, safe no-leader gap).

The TTL is sized for ungraceful death, meaning a killed process, a preempted
VM or pod, anything that never runs `Stop`. That is the only case it governs.
A graceful stop releases the row, and a successor acquires on its next renew
tick without waiting out the TTL. Every job's cycle is 30s-15m and
idempotent, so a 45s leaderless gap costs at most one skipped cycle.

There is one election instead of seven. The leases were renewing forever to say
the same thing (5,040 renewals/hr idle, now 240), they all landed on the
same instance anyway, and a lease cannot shard a job's work. A set of jobs
that outgrows one instance is fixed by sharding the work behind a second
election, which is a wiring diff. kube-controller-manager runs its
controllers under one election per binary on the same reasoning.

Leadership is **best-effort** and is not a fencing token. Every leader-gated
job must stay idempotent and safe under a transient double-run at handoff.
They all are today, because every action goes through the state-machine-guarded
services. Keep it that way (the invariant is recorded in
architecture-decisions.md).

## 11. Where things live

```
internal/domain/task/            Task, states, Queue/Handler contracts,
                                 backoff, journal contract, the lossy
                                 in-process Waker (stdlib only)
internal/platform/spanner/
  task_queue.go                  TaskQueue type, options, shared helpers
  task_enqueue.go                enqueue + keyed coalesce + EnsureTransition
  task_probe.go                  ClaimableShards, the read-only discovery
  task_claim.go                  claim + fenced delivery bookkeeping
  task_sweep.go                  expired-lease reclamation, DLQ
  task_reconcile_keys.go         the shared keyed-merge helpers
  task_steps.go                  the operation journal
  task_admin.go                  DLQ list/requeue, Stats, HasActive
  lease_repository.go            leader leases (predicate DML)
  clock.go                       scheduling-only time indirection
internal/jobs/
  taskworker/                    the pool (§4)
  taskreclaimer/                 crash recovery (§4)
  leaderelection/                electors (§10)
  hostreconciler/  devicecontroller/  reaper/  batchsettle/  catalogsync/
  fleetkeeper/  warmplane/       the autoscaler and its bake machine (§7)
internal/features/{host,device}/
  handler_*.go                   task handlers (provision, create_cvd,
                                 reconcile, …) + their budgets
  service*.go                    producers: the transactional enqueues
migrations/spanner/complete_schema.sql   the single source of schema truth
tests/queueload/                 queue load + fault-injection harness
tests/robustness/                full-stack lifecycle harness (fakegce/fakeho)
```

Verification lives in [testing.md](testing.md); the queueload harness's
nemeses (clock skew, pauses, kills, DB faults) and the robustness harness's
invariant checkers are the only proofs that matter when any of the above
changes.
