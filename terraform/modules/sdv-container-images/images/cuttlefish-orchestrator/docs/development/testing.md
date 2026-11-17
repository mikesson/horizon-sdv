# Testing Guide

CCO is verified in layers. The first three run in CI-style gates; the
harnesses are on-demand tools for exercising the system under faults.

| Layer | Where | What it proves |
|-------|-------|----------------|
| Unit + emulator tests | `*_test.go` throughout | Package behavior; repository logic against a real Spanner emulator |
| Contract tests | `web/cuttlefish-console/src/**`, `internal/platform/hoclient/contract_test.go` | Wire protocols pinned against upstream Cuttlefish: WebRTC signaling and ADB framing in the console, and what the Host Orchestrator answers, the latter against `tests/harness/fakeho` instead of a hand-rolled server |
| Browser e2e | `web/cuttlefish-console/e2e/` | Full console flows against a network-mocked API |
| **Fault-injection harnesses** | `tests/` | System invariants under crashes, pauses, clock skew, network faults, and cloud-side drift |
| **Relay integration** | `cmd/relayhook/turnrs_integration_test.go` + `tests/turnclient/` | The relay's own behavior: that a real turn-rs calls our authorization hook, accepts the key we derive, refuses an expired credential, and carries traffic between two of its clients. Skipped without Docker |
| **Live smoke** | `tests/livesmoke` | What only a real deployment shows: a real Host Orchestrator's answers, the platform's reaction to a slow response, whether a drifted device converges |

## Layout and boundaries

```
cmd/          Deliverables ONLY (orchestrator, cfctl, relayhook)
tests/        Verification infrastructure, builds with ./..., never ships
├── queueload/    Task-queue load + fault-injection harness
├── robustness/   Lifecycle invariant harness (Jepsen-style)
├── livesmoke/    Real device against a real deployment (on-demand, billable)
├── specgen/      Serves the OpenAPI spec for the schema-drift check; -dump feeds cfctl model generation
├── coverreport/  Block + error-branch coverage report (the test job's floor)
└── harness/      Shared libraries: faultproxy (controllable TCP proxy),
                  fakegce (in-memory Compute API), fakeho (in-memory Host
                  Orchestrator)
```

Rules for this tree:

- **Production code never imports `tests/`** (depguard-enforced). `_test.go`
  files may. The host-reconciler regression tests and the host feature's
  zone-failover ladder tests drive a real compute REST client against
  `tests/harness/fakegce` (which also serves `aggregated/instances` for the
  orphan sweep, stamps `creationTimestamp`, offers `PlantOrphan` for
  backdated unclaimed VMs, and injects per-zone stockouts through
  `StockoutZone`, as the operation-level errors real GCE returns).
- **Package-coupled helpers stay colocated** with the package they fake, per
  the stdlib `httptest` idiom: `spannertest`, `hosttest`, `devicetest`. Only
  standalone infrastructure lives in `tests/`.

`fakeho` serves every endpoint `hoclient` calls, and its route table is written
to be read beside the upstream controller's. It covers the parts of the
Host Orchestrator's contract that are easy to guess wrong. Display add, list and
remove answer **directly**, while the screenshot on the same resource answers
with an **operation** whose result carries the image; a bugreport's operation
stores its ID as a bare JSON string; `:start` alone requires a request body.
A fake that smooths any of those over lets a client ship that depends on the
smoothing.

`ServedRoutes()` reports the patterns a run actually exercised, so a suite can
assert its coverage instead of assuming it. `tests/harness/fakeho`'s own
conformance test drives the real client across the whole `hoclient.Client`
interface and then checks that list. It also reflects over the interface, so a
method added to the client fails the test until it has been driven against the
fake. That is what stops the next hand-rolled `httptest.NewServer`, which is
how a fake serving a shape upstream never sends got written in the first place.

`hoclient`'s own tests are split along the same boundary:

- **`contract_test.go`** covers what the host *says*. It runs against
  `fakeho`, with no hand-rolled servers at all.
- **`client_test.go`** covers what the *connection* does: port failover,
  retries, circuit breaking, deadlines, size caps. These keep ad-hoc servers,
  because they need one that misbehaves in ways a real host cannot: hijacking
  a connection mid-exchange, never answering, or counting dials. The minimal
  JSON bodies there are the smallest valid 200 and no assertion reads them;
  where an assertion depends on the response, the server is the fake.

`tests/` stays in the ordinary `./...` build set so it can never bit-rot. It
is never shipped (the container builds `./cmd/orchestrator` only).

## Unit and emulator tests

```bash
docker run -d --name cco-spanner-emu -p 9010:9010 -p 9020:9020 \
    gcr.io/cloud-spanner-emulator/emulator   # thereafter: docker start cco-spanner-emu
SPANNER_EMULATOR_HOST=localhost:9010 go test ./... -count=1
```

CI runs this suite with `CCO_REQUIRE_EMULATOR=1`, which turns
emulator-dependent skips into failures. A broken emulator goes red, never
green-by-skip. `CCO_REQUIRE_DOCKER=1`, set alongside it, does the same for the
relay integration tests in `cmd/relayhook/`, which start a real turn-rs
container and otherwise skip when Docker is unreachable.

`spannertest` creates throwaway databases from
`migrations/spanner/complete_schema.sql`, the authoritative schema. It is
parsed at test time, so tests cannot drift from it. Concurrency-sensitive jobs use
`testing/synctest` (fake clock, deterministic scheduling).

Emulator limits to know:

- Read-write transactions are **globally serialized**, so throughput and
  contention numbers are meaningless; only correctness transfers.
- `ORDER BY ... NULLS FIRST` is rejected (it's the GoogleSQL ASC default
  anyway; don't write it, or the emulator suite goes blind to that query).
- Tables whose primary key has a `DEFAULT` re-apply column defaults on UPDATE
  mutations (emulator bug; the schema loader strips `GENERATE_UUID()`
  defaults).

## The queue harness: `tests/queueload`

Load-tests and fault-injects the durable Spanner task queue. The driver
enqueues through the production commit shape and spawns instance subprocesses
running the real stack (platform client, `TaskQueue`, worker pool, sweeper,
leader elector) with a synthetic handler standing in for HO/GCE work.
Instances record claims, effects, and outcomes to append-only event ledgers;
post-run checkers validate the history (effects under valid leases, no reused
attempt numbers, no unresolved overlapping leases) and ID-level conservation:
every enqueued task either applied its effect or dead-lettered as counted
fault collateral. **Nothing is ever silently lost.**

The documented steady smoke is the opt-in `scripts/ci.sh queueload` job; the
scenarios and nemeses below are direct invocations:

```bash
SPANNER_EMULATOR_HOST=localhost:9010 go run ./tests/queueload \
    -scenario=steady -rate=15 -duration=40s -instances=3 \
    -lease=12s -handle-timeout=8s -handler-min=300ms -handler-max=1s \
    -sweep-interval=5s -poison-rate=0.05 \
    -kill-interval=15s                        # SIGKILL + respawn
```

Nemeses (combine freely): `-kill-interval`, `-pause-interval`/`-pause-duration`
(SIGSTOP past the lease forces sweeper reclaim and exercises write fencing),
`-clock-skew` (per-instance lease-clock offsets; skew ≥ lease − handler time
causes premature reclaims, measured as overlaps/duplicates, and safety must
still hold), `-db-fault latency|blackhole|cut` (faults on one instance's emulator
link; `cut` is the ambiguous-commit fault), `-kill-after-effects` (instance 0
kills itself between applying an effect and acking it). See
`tests/queueload/doc.go` for the full flag reference and scenarios.

## The live harness: `tests/livesmoke`

Every other layer stops at a process boundary. `livesmoke` is the only one that
provisions a real device on the real deployment, and it exists because three
defects shipped through a green suite and were caught by hand:

- the screenshot endpoint had **never** returned an image, because it decoded
  the Host Orchestrator's asynchronous *operation* as if it were the picture;
- a 30s write deadline broke every device verb that ran longer, which the
  platform answered with a 503 **and a recycled instance**, while the work
  completed on the host unseen;
- a device left `stopped` over a running CVD never converged.

Each is now a check. It drives the shipped `cfctl` binary (its timeouts and
exit codes are part of the contract under test) and reads platform facts back
out of Cloud Logging, so a request that breaks its own response is visible.

```bash
CCO_LIVESMOKE_PROJECT=your-project-id CCO_LIVESMOKE_REGION=your-region scripts/ci.sh livesmoke
go run ./tests/livesmoke -project=your-project-id -region=your-region                  # equivalent, direct
go run ./tests/livesmoke -project=your-project-id -region=your-region -scenario=verbs  # lifecycle only
go run ./tests/livesmoke -project=your-project-id -region=your-region -sa=cfctl-test@your-project-id.iam.gserviceaccount.com
```

The `-sa` flag enables the drift check, which needs a signed JWT to reach `/v1`
and start a CVD behind CCO's back; without it that one check skips. It bills
real VMs, so the project has no default and the run always tears down. The
region has no default either.
`-keep` opts out for debugging. The harness builds its own cfctl from the tree under
test; nothing needs to be on PATH. Flags are in `tests/livesmoke/doc.go`.

## The lifecycle harness: `tests/robustness`

Runs the **real** orchestrator stack in instance subprocesses against the
emulator, with fake GCE and fake Host Orchestrator boundaries. The stack is
the device and host services, the production task handlers (CVD transitions,
keyed reconciles, host provisioning, async verb operations), the reaper,
the host reconciler, the device controller, the task reclaimer and the leader
electors. A seeded workload drives concurrent lifecycle operations while
nemeses inject faults.

The operations are host and device create and delete, declarative creates
that place onto existing capacity or auto-provision (with the tracking
default build auto-registered under concurrency), pin/unpin/extend, reads,
stop/start/restart, and duplicate-idempotency-key probes including
cross-instance shared keys. The nemeses are process crashes, pauses,
cloud-side drift (a VM vanishes, or is preempted the way GCE actually
preempts Spot: by deleting the instance), zonal stockouts (one zone at a time
exhausts capacity, alternating between the two seeded zones), DB-link faults
through a TCP proxy (latency, blackhole, and cut, the ambiguous-commit
fault), per-instance clock skew on the lease arithmetic, and Host
Orchestrator degradation (hangs past the client timeout, blanket 500s).

After the fleet quiesces, checkers verify CCO's own invariants:

- **Conservation.** `current_device_count` equals the CVD instances the live
  devices desire (one device can want several); `protected_device_count` and
  `quotas.used_hosts` equal the rows they summarize, exactly (no reconciler
  heals these counters in this composition). Because the counters commit in
  the same transactions as the rows, a sampler also re-evaluates them on
  globally consistent read-only snapshots every few seconds through the run.
  A transient violation something later masks is still a violation.
- **Capacity.** No host ever holds more devices than `max_devices`
  (placement is advisory; the create commit's atomic re-check is the
  enforcement this proves), at quiesce and at every mid-run snapshot
- **Stranded states.** No resource stuck in a transitional status without a
  queue task left to move it
- **Promises.** Lapsed pins are released within the reaper cadence (widened
  by the largest configured clock skew)
- **Orphans.** Every surviving fake VM belongs to a live host, every CVD
  group to a live device. The harness runs the host reconciler with orphan-VM deletion
  ARMED (`DeleteOrphanVMs: true`), so every run also proves the sweep never
  eats a legitimate VM. A wrongful delete trips this and the conservation
  checkers.
- **Idempotency.** Every idempotency key resolves to one device, across
  concurrent in-process races and cross-instance shared-key probes alike
- **Placement.** A zone-pinned host never leaves its requested zone, and a
  failover-eligible host never dead-letters on zonal capacity while another
  zone had room (a stray old-zone instance after a failover is caught by the
  orphan checker)
- **Acked creates.** A create the API acknowledged still has a row unless a
  delete was attempted on it or on its host (host deletes remove rows
  physically and cascade their interleaved devices; an errored delete may
  have committed, so attempts excuse regardless of outcome)
- **Operations settle.** No EXCLUSIVE-verb operations row is still open at
  quiesce; a leaked open row blocks its resource's admission with nothing
  left to heal it
- **Evidence.** The run must prove the system worked, and not only that
  nothing broke. Creates succeeded, at least one CVD group actually launched
  at the HO boundary, and deletes attempted often enough succeeded at least
  once. In a run with no attempt-burning nemesis, any dead-letter or
  error-status resource is a violation outright.
- **Liveness.** Instances exit only when killed or told to; a panic mid-run
  is a violation even when the surviving instance converges the fleet

The checkers themselves are pinned by `checkers_test.go`: every violation
class has a test that plants it and watches it fire. A checker that cannot
go red is not evidence.

The harness wipes rows between runs but keeps the database schema. After a
schema change, run `docker restart cco-spanner-emu` so the bootstrap
recreates the database from the current `complete_schema.sql`. The symptom of
staleness is mass stranded and lapsed violations from failing writes.

The no-nemesis baseline is the opt-in `scripts/ci.sh robustness` job; chaos
and stockout are direct invocations:

```bash
SPANNER_EMULATOR_HOST=localhost:9010 go run ./tests/robustness \
    -instances=2 -duration=45s \
    -kill-interval=20s -pause-interval=25s -drift-interval=15s

# Placement plane under zonal stockouts (composes with the nemeses above):
SPANNER_EMULATOR_HOST=localhost:9010 go run ./tests/robustness \
    -instances=2 -duration=90s -stockout-interval=30s -stockout-duration=15s

# DB-link, clock and HO-boundary faults (tests/robustness/doc.go has the
# full flag reference):
SPANNER_EMULATOR_HOST=localhost:9010 go run ./tests/robustness \
    -instances=2 -duration=60s -db-fault=cut -db-fault-interval=15s \
    -clock-skew=0s,5s -ho-fault=error
```

Operation *errors* under fault injection do not fail the run (capacity
rejections and races with the reaper are expected); the checkers judge the
final state, the ops ledgers explain it. On violations the driver dumps the
lifecycle tables to `state.txt` in the artifacts directory so evidence
survives the next run's database wipe.

Spanner itself is trusted; its guarantees are Google's concern. The target
is CCO's cross-transaction, cross-actor composition: reconcilers racing user
operations, counter bookkeeping, teardown cascades, lease fencing. The
harness's first no-fault baseline caught five real bugs in exactly that
class; a green baseline is the regression gate that keeps them fixed.

## Safety and real-Spanner runs

Both harnesses **wipe tables between runs**. A hard gate refuses any target
database whose name does not contain `loadtest` unless
`SPANNER_EMULATOR_HOST` is set; there is no override. For contention-realistic
measurements (the emulator serializes writes), run against a dedicated
`*-loadtest` database on real Spanner. The `-db-fault` modes are
emulator-only, since they rely on the endpoint override.

## Frontend verification

From `web/cuttlefish-console/`: `npm test` (Vitest unit + contract tests;
`-- --coverage` also enforces the angular.json threshold floors), `npm run
e2e` (Playwright, network-mocked), `npm run lint` (five legs, every one
always run and errors gating: Biome, type-aware ESLint, stylelint over the
inline `styles:` blocks, the e2e tsconfig typecheck, and
`check:fixture-shapes`, which parses what the e2e mock serves through the
console's own Zod schemas), and `npm run check:schema-drift`, which
introspects the real Zod schemas against the backend OpenAPI spec served by
`go run ./tests/specgen`.

## CI

There is no hosted CI. `scripts/ci.sh` is the gate and it runs on this
machine. It gates shell and terraform formatting, deployment-specific names
and stale commands in tracked files (`bleed`), build, vet, the
generated-model diff, lint, the emulator-backed race suite with its
error-branch coverage floor, goroutine leaks, govulncheck, the Zod↔OpenAPI
drift check, and the console's build, unit suite, lint, dead-CSS, Knip,
production `npm audit` and settings-card scan. Only `deadcode` is advisory.
The harnesses are opt-in jobs, never in the default set: run
`scripts/ci.sh queueload robustness` before queue- or lifecycle-touching
deploys, and `CCO_LIVESMOKE_PROJECT=<id> CCO_LIVESMOKE_REGION=<region>
scripts/ci.sh livesmoke` either side
of one.

The exit status is the verdict, and the last line states it in words, as a
`RESULT: PASS` or `RESULT: FAIL` line. Prefer that line: an exit status is
lost as soon as the script runs inside a compound command whose last element
succeeds. Each
job also tees to `/tmp/cco-ci-logs/<job>.log`, and the summary prints that
path beside anything that failed.

`e2e` is opt-in because this machine is a Cloud
Workstation. `/home` is a persistent disk and the rest of the filesystem is an
overlay that every reboot resets. The browser binaries live under
`~/.cache/ms-playwright` and *do* survive; their shared libraries live on the
overlay and do not, so after a reboot Playwright is installed and cannot
start. A preflight checks for that, and for every other tool the requested
jobs need, before any job runs. `scripts/ci.sh --fix-env` restores it.

Jobs that own something machine-global (`test`, `contract`, `webbuild`,
`webtest`, `e2e`) run under a lock, so two runs cannot interleave over the
emulator, the console build output or `:4200`; `--wait` queues instead of
refusing. Spanner test databases are not part of that: they are
named per call and dropped by name, so concurrent suites do not collide there.
