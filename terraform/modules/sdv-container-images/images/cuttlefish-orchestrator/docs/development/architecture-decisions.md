<!-- Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. -->

# Architecture Decision Records

Decisions that constrain future changes but are invisible in any single file.
Each entry names the code it governs; the code comments point back here.

## ADR-1: Spanner transactions rely on default serializable isolation

**Status: accepted (2026-07-12).**

### Context

Cloud Spanner makes isolation a *per-transaction* choice. The default is
serializable (every read in a read-write transaction is validated at commit),
and the client can instead be configured for **repeatable read**, which
Google's documentation recommends for workloads migrated from MySQL or
PostgreSQL to cut abort rates. Read-lock modes (pessimistic/optimistic) are
equally configurable. CCO sets none of these options. Every read-write
transaction runs serializable and pessimistic, and three invariants depend on
that. They are protected only by serializable read-validation, with no schema
constraint behind them:

1. **Host quota.** `CreateWithQuotaIncrement`
   (`internal/features/host/repository.go`) reads `used_hosts`/`max_hosts`,
   checks headroom, writes `used_hosts + 1`. Two concurrent creates each read
   "one slot free"; serializable aborts one, **repeatable read commits both**
   and breaches the quota. This is the textbook write-skew shape.
2. **Host capacity.** `CreateWithHostIncrement`
   (`internal/features/device/repository.go`) reads
   `current_device_count`/`max_devices`, checks, increments. Same shape; a
   skew overfills a host past `max_devices`.
3. **Keyed-reconcile per-key serialization.** `leasedSiblingExists`
   (`internal/platform/spanner/task_reconcile_keys.go`) is a plain `SELECT`
   inside the claim's transaction. Under serializable it joins the validated
   read-set, so a concurrent claim that leases the sibling conflicts and
   aborts; under repeatable read (which validates only `FOR UPDATE` reads)
   the probe is unvalidated and **two workers reconcile one resource
   concurrently**, the exact outcome the keyed queue exists to prevent.
   (The pending-sibling merge probes have the unique `reconcile_key` index as
   a schema guard; the claim's leased-sibling probe has none.)

These three are the most severe cases, write-skew shapes with no schema
guard at all. A wider family relies on the same validation for lost-update
protection: the `Mutate` full-row write-back on hosts and devices (under repeatable read a stale full-row write silently
clobbers a concurrent incarnation bump or counter commit), the
`usedHostsDecrement` read-then-write fold, the device feature's
in-transaction counter deltas (`hostCountersMutations`, where `used_hosts` has
the transactional `ReconcileUsedHosts` recount behind it and
`current_device_count` has none), and the keyed sweep's in-transaction re-read
(`sweepKeyedExpired`). Flipping isolation breaks these too. The numbered list
is where it breaks first and loudest, and is not exhaustive.

### Decision

Keep the client-wide Spanner defaults. **Never flip the client default to
repeatable read or optimistic concurrency.** If repeatable read is ever
adopted for specific read-heavy paths, the transactions above must either be
pinned to serializable per-transaction or have every correctness-bearing read
converted to `FOR UPDATE`. The change must also re-run the queueload and
robustness harnesses, which are the only things that surface these races.

### Consequences

Claim contention is managed the documented Spanner way: 16-way sharding plus
a pessimistic `FOR UPDATE` claim, where wound-wait makes progress and an
optimistic claim would abort-storm. Weakening isolation is not an option for
reducing aborts. Any abort-rate change that touches isolation is a
correctness change.

## ADR-2: Server-evaluated ownership timestamps (TrueTime)

**Status: accepted (2026-07-12).**

Every lease stamp and expiry comparison in the task queue and leader-lease
store is evaluated server-side via `CURRENT_TIMESTAMP()` inside the owning
statement, never computed from an instance's clock. That covers the claim's
`lease_expiry`, the sweeper's expired-lease re-verify, the keyed-claim
leased-sibling probe, and `leader_leases` takeability. The claim reads its
server-assigned `lease_expiry`/`attempts` back with `THEN RETURN`; that value
is the delivery fence (and the GCE `requestId` nonce), so it must be exactly
what was stored.

Consequences that later changes can easily break:

- **The lease writes must stay DML.** Buffered mutations cannot call
  `CURRENT_TIMESTAMP()`; "simplifying" a lease write back to a mutation
  silently reintroduces the app clock into ownership.
- Cross-instance lease correctness is bounded by TrueTime uncertainty (~ms).
  NTP skew between Cloud Run instances does not enter into it. The `clock.go`
  indirection (`timeNow`/`SetClockOffsetForTesting`) governs scheduling values
  only (enqueue `available_at` defaults, merge availability floors). Skewing it
  must never cause premature reclaims or fenced-out deliveries. The queueload
  clock-skew nemesis asserts immunity to the skew; it does not assert a
  tolerance band.
- Go code that needs to compare a lease expiry outside a DML predicate must
  fetch `serverNow()` (task_queue.go) rather than `timeNow()`.

## Companion engine invariants

Documented here because they span packages; enforced by review.

- **Queue ownership margin.** Nothing but the
  `LeaseDuration (35m) > HandleTimeout (30m)` margin prevents a
  swept-and-reclaimed task's still-running zombie handler from racing its
  replacement (the bookkeeping fence catches the *writes*, not the work). The
  pool enforces the inequality at construction
  (`internal/jobs/taskworker/worker.go`); handlers must not run
  context-ignoring work longer than the margin after cancellation.
- **Periodic jobs are 2×-safe.** Leader election is best-effort, with no
  fencing token (`internal/jobs/leaderelection/doc.go`). Every leader-gated job must
  stay idempotent and safe under a transient double-run at lease handoff.
  Today all of them act through state-machine-guarded transitions (the reaper
  double-run no-ops: host delete → `errHostAlreadyDeleting`, device delete →
  the `transitioned` flag, unpin → tolerated `ErrInvalidState`). A new
  periodic job must preserve this property rather than assume single-flight.

How the machinery these decisions govern fits together is documented in
[control-plane.md](control-plane.md).
