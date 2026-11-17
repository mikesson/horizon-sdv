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

// Command queueload load-tests and fault-injection-tests the Spanner task
// queue without provisioning GCE host VMs or Cuttlefish devices.
//
// The driver process generates enqueue traffic through the production commit
// shape (task insert + counter read-modify-write in one transaction, like
// device creation against its host row) and spawns N instance subprocesses.
// Each instance runs the real stack an orchestrator instance runs around the
// queue (spanner platform client, TaskQueue, taskworker.Pool, taskreclaimer,
// leader elector) with a synthetic handler standing in for HO/GCE work.
// Instances record claims, effects, and outcomes to append-only event ledgers;
// the post-run checkers validate the recorded history (effects under valid
// leases, no reused attempt numbers, no unresolved overlapping leases) and
// ID-level conservation (every enqueued non-poison task either applied its
// effect or dead-lettered as counted fault collateral, with nothing silently
// lost), plus database quiescence after drain.
//
// The database is wiped between runs, so the target database name must
// contain "loadtest" (or SPANNER_EMULATOR_HOST must be set). A missing
// database is created from migrations/spanner/complete_schema.sql.
//
// Scenarios (composable with the -poison-rate / -transient-rate modifiers):
//
//	steady  paced enqueues at -rate for -duration, then drain
//	drain   preload -preload pending tasks, start workers, measure drain
//	burst   steady plus -burst extra tasks injected at full speed mid-run
//	idle    no tasks at all: measures the pure claim-polling cost of a fleet
//	keyed   the ineligible-keyed-head shape, in two halves: a deterministic
//	        pre-run probe (leased keyed sibling + pending dirty-bit head + an
//	        equal-priority task parked behind it) asserts the production
//	        Claim steps past the stuck head instead of returning empty. That
//	        probe is the gate, and it uses no wall clock because emulator
//	        latency is meaningless. Then steady load at resync priority under
//	        keyed churn (-keyed-rate filings over -keyed-resources resources,
//	        each sibling held -keyed-handler) runs the standard conservation
//	        and drain gates
//
// Fault injection (Jepsen-inspired nemeses; combine freely):
//
//	-kill-interval        SIGKILL a random instance, respawn after -restart-delay
//	-pause-interval/-pause-duration
//	                      SIGSTOP a random instance; a pause longer than -lease
//	                      forces sweeper reclaim and exercises write fencing
//	-clock-skew           per-instance lease-clock offsets; skew >= lease minus
//	                      handler time causes premature reclaims (measured as
//	                      overlaps/duplicates, and safety must still hold)
//	-db-fault             latency | blackhole | cut on one instance's emulator
//	                      link via an in-process TCP fault proxy (cut = the
//	                      ambiguous-commit fault)
//	-kill-after-effects   instance 0 SIGKILLs itself right after its Nth ledger
//	                      effect and before acking, which is the deterministic
//	                      crash-between-effect-and-Complete window
//
// The documented emulator steady smoke also runs as the opt-in
// `scripts/ci.sh queueload` job, which supplies the emulator, machine lock
// and verdict line.
//
// Examples (emulator smoke, then a real-Spanner soak):
//
//	SPANNER_EMULATOR_HOST=localhost:9010 go run ./tests/queueload \
//	    -scenario steady -rate 20 -duration 30s -instances 2
//
//	SPANNER_EMULATOR_HOST=localhost:9010 go run ./tests/queueload \
//	    -scenario keyed -rate 5 -duration 30s -instances 2 -workers 4 \
//	    -shards 4 -keyed-resources 2 -keyed-rate 4 -keyed-handler 8s \
//	    -handler-min 50ms -handler-max 200ms
//
//	go run ./tests/queueload -project your-project-id \
//	    -spanner-instance cuttlefish-orchestrator -database orchestrator-loadtest \
//	    -scenario steady -rate 100 -duration 2h -instances 4 -workers 32 \
//	    -handler-min 2s -handler-max 8s
//
// Interpreting results: the report covers the client-observable half
// (latencies, throughput, verification). The server-side half lives in
// SPANNER_SYS: transaction aborts, lock waits, and the claim query's
// scanned/returned ratio drifting up during a soak (tombstone accumulation
// from delete-on-complete). The report prints ready-to-run queries. Scale the
// instance's processing units up for the test window and back down afterwards.
//
// The emulator validates only the harness itself, never performance: it
// serializes read-write transactions globally, so any real backlog or worker
// count collapses to a few tasks per second with wild claim latencies. Keep
// emulator smoke runs tiny (tens of tasks per second, small preloads).
package main
