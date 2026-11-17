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

// Command robustness is the Jepsen-style lifecycle harness. It runs the REAL
// orchestrator stack (device/host services, the production task handlers on
// a worker pool, reaper, host reconciler, device controller, task reclaimer,
// one background-plane elector) in instance subprocesses against the Spanner
// emulator, with fake GCE and fake Host Orchestrator boundaries. It drives a
// seeded concurrent lifecycle workload, injects faults, waits for the fleet to
// quiesce, and then machine-checks CCO's own invariants:
//
//	conservation  hosts.current_device_count == instances desired by live
//	              devices per host (a device can want several);
//	              protected_device_count == pinned live devices;
//	              quotas.used_hosts == live hosts (exact; no reconciler
//	              heals these counters)
//	stranded      no transitional resource (creating/provisioning/deleting)
//	              without a pending or leased task to move it
//	promises      lapsed pins are released within the reaper cadence
//	orphans       every surviving fake VM belongs to a live host; every
//	              surviving CVD group belongs to a live device
//	idempotency   concurrent duplicate-key creates resolve to one device
//	queue         nothing pending/leased after quiescence; dead-letters are
//	              attempt-exhausted, and tolerated at all only when an
//	              attempt-burning nemesis (kill/pause/drift/stockout) ran.
//	              In a clean run any dead-letter or error-status resource is
//	              a violation
//	evidence      the run must prove the system worked, and not only that
//	              nothing broke: creates succeeded, at least one CVD group
//	              actually launched at the HO boundary, deletes attempted
//	              often enough succeeded at least once
//	acked         every create the API acknowledged still has a row unless a
//	              delete was attempted on it or on its host (host deletes
//	              remove rows physically and cascade their interleaved
//	              devices; an errored delete may have committed, so attempts
//	              excuse regardless of outcome); every EXCLUSIVE-verb
//	              operations row settled by quiesce, because an open one
//	              blocks its resource's admission with nothing left to heal it
//	liveness      instances exit only when killed or told to; a panic mid-run
//	              is a violation even when the surviving instance converges
//	              the fleet
//	election      the background plane elects on ONE lease (a second lease row
//	              is a job electing on its own), some instance actually held it
//	              during the run, and a run whose nemesis stopped the holder
//	              (a kill, or a pause outlasting the lease) handed the plane to
//	              a survivor rather than stopping with it
//
// Conservation, capacity and quota are maintained in the same transactions
// as the rows they summarize, so they are also evaluated on globally
// consistent read-only snapshots every few seconds THROUGH the run. A
// transient violation that something later heals or masks (an over-capacity
// admission followed by a delete, a clamped double-decrement) counts as a
// violation and never as a lucky miss.
//
// Spanner itself is trusted (its guarantees are Google's concern); the target
// is CCO's cross-transaction, cross-actor composition: reconcilers racing
// user operations, counter bookkeeping, teardown cascades.
//
// Nemeses: -kill-interval (SIGKILL+respawn, which is also the
// leadership-handover proof, since the plane must move to the survivor),
// -pause-interval/-pause-duration (SIGSTOP past lease/leader TTLs, which is
// also how an overlapping-leadership window is forced: the paused holder still
// believes it leads while its successor cycles, and the conservation checkers
// must hold through it), -drift-interval (a VM vanishes or is preempted at the
// fake GCE boundary, and the host reconciler must cascade or re-provision),
// -stockout-interval/-stockout-duration (one zone at a time exhausts capacity
// at the fake boundary, alternating between the two enabled zones; the
// provision ladder must fail non-pinned hosts over to the surviving zone,
// pinned hosts must retry in place and never move, and the old-zone instance
// of a failover must not survive it), -db-fault latency|blackhole|cut (the
// instance's Spanner link through a TCP fault proxy; cut is the
// ambiguous-commit fault, where an actuation may have reached the HO while its
// commit never reported), -clock-skew (per-instance lease-clock offsets:
// premature reclaims and late reapers must stay safe), and -ho-fault
// hang|error (the Host Orchestrator boundary degrades, holding responses past
// the client timeout or answering plain 500s; retries must converge and never
// race an in-flight launch).
//
// Operation errors under chaos are DATA rather than failures (capacity
// rejections, races with the reaper). The checkers judge the final state and
// the ops ledgers explain it.
//
// The no-nemesis baseline (seed 42) also runs as the opt-in
// `scripts/ci.sh robustness` job, which supplies the emulator, machine lock
// and verdict line.
//
// Example (emulator):
//
//	SPANNER_EMULATOR_HOST=localhost:9010 go run ./tests/robustness \
//	    -instances=2 -duration=45s -kill-interval=20s -drift-interval=15s
//
// Zone-stockout scenario (placement):
//
//	SPANNER_EMULATOR_HOST=localhost:9010 go run ./tests/robustness \
//	    -instances=2 -duration=90s -stockout-interval=30s -stockout-duration=15s
//
// Standing-capacity scenario (fleet autoscaler). -pool-floor seeds the target
// and provision_only. -pool-reclaim seeds `on` instead, with the demand signal
// off so the target equals the seeded floor rather than wandering, and drives
// the keeper's two reclamation directions in turn: a quarter of the way in it
// runs the fleet's idle leases down below the standby margin, so a deficit and
// a draining host coexist and the deficit is met by renewal rather than by
// buying; two thirds of the way in it drops the standing intent to zero and
// backdates the keeper's own host rows past host.MinShortenAge, so the fleet
// becomes surplus and is given back. One run therefore holds a fleet, renews
// into it, and reclaims it, and the same host can cross between the two, which
// is the only way a run reaches the oscillation bound.
//
// The run depends on every one of those. The mode decides whether a lease may
// be lowered at all, the horizon decides whether anything else is still asking
// for the capacity being given back, the age decides whether an empty host
// reads as drained rather than as one provisioned moments ago, and the drain
// decides whether renewal is reachable at all. Drop any of them and the run
// exercises nothing while reporting it as a failure of reclamation rather than
// of its own setup:
//
//	SPANNER_EMULATOR_HOST=localhost:9010 go run ./tests/robustness \
//	    -instances=2 -duration=90s -pool-floor=3 -pool-reclaim
//
// Fleet-floor scenario (-fleet-floor). This is the other lower bound, and the
// one the buffer cannot express. The floor counts hosts the fleet RUNS,
// occupied or free, where the standby buffer beside it counts hosts it keeps
// FREE. It gets its own flag rather than a second meaning for -pool-floor,
// whose bounds above are all stated against idle inventory.
//
// It seeds `on` with the buffer at ZERO, which is what makes the run able to
// fail: nothing asks for a free host, so a surplus computed from the buffer
// alone calls every host the keeper owns spare and empties the fleet within a
// few cycles. It backdates the keeper's hosts past host.MinShortenAge for the
// whole window, because a fleet that survived only because its hosts were young
// proves nothing about the floor. The checker's vacuity arms say so and fail
// the run rather than pass it.
//
//	SPANNER_EMULATOR_HOST=localhost:9010 go run ./tests/robustness \
//	    -instances=2 -duration=90s -fleet-floor=2
//
// Dedicated-host scenario (-group-collapse). The third reclamation direction,
// and the only one that gives back capacity no target ever asked for. A group
// create too large to place fences the host it provisions, and once the group
// ends that host serves nobody and nothing else reclaims it. The run drives the
// whole life (each group create ends the previous group, so an emptied fenced
// host exists on purpose rather than by luck) and ages every exclusive host
// past the shorten floor. It needs no fleet: on its own it seeds `on` with a
// standing intent of zero, which is what a deployment running with the pool off
// actually has, and where the collapse is the only thing the keeper still does.
//
// Two hosts in it are exclusive for opposite reasons, and telling them apart is
// the whole assertion: the ones the orchestrator fenced FOR a group, which must
// be reclaimed, and the ones the workload fenced as a USER would, which must
// never be. Both are empty, idle and equally old by the time the keeper looks.
// The nemesis ages them together on purpose, so a collapse that reached for the
// second has nothing saving it but the rule under test.
//
//	SPANNER_EMULATOR_HOST=localhost:9010 go run ./tests/robustness \
//	    -instances=2 -duration=90s -group-collapse
//
// Run it SEPARATELY from -pool-reclaim, which is how `scripts/ci.sh
// robustness` invokes the two. Their populations cannot collide (standing
// inventory is shareable by definition and a fenced host is not), but they
// compete for the same host-create throughput, and every group create provisions
// one. Composed into a single run the fleet keeper was starved to keeper-hosts=0,
// leaving nothing to drain, renew or give back, and both of that scenario's arms
// reported a fleet that had never been built.
package main
