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

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// runResult carries everything the report needs, gathered after the instances
// have exited.
type runResult struct {
	elapsed    time.Duration
	offered    int64
	enqueued   int64
	counterEnq int64 // enqueues that went through the counter txn shape
	enqErrs    int64
	enqLat     histSnap
	poison     int64 // tasks scripted to dead-letter
	keyedCalls int64 // keyed reconcile filings (keyed scenario)
	keyedErrs  int64
	sums
	queueStats task.QueueStats
	counterSum int64 // -1 when unavailable
	quiesce    quiescentRows
	drained    bool
	kills      int
	pauses     int
	dbFaults   int
	interrupt  bool
}

func (d *driver) report(r runResult) error {
	events, err := readEvents(d.cfg.RunDir)
	if err != nil {
		return err
	}
	f := analyze(events, d.enqSnapshot(), r.quiesce.dlq, d.cfg.historyOpts())
	d.mu.Lock()
	f.violations = append(f.violations, d.instDeaths...)
	d.mu.Unlock()

	fmt.Print(d.formatReport(r, f))
	if err := d.writeSummary(r, f); err != nil {
		return err
	}
	return d.verdict(r, f)
}

// formatReport renders the human-readable run report.
func (d *driver) formatReport(r runResult, f findings) string {
	check := func(ok bool) string {
		if ok {
			return "OK"
		}
		return "MISMATCH"
	}
	pct := func(n, of int64) float64 {
		if of == 0 {
			return 0
		}
		return 100 * float64(n) / float64(of)
	}

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w("")
	w("=== queueload report ===================================================")
	w("run:      scenario=%s elapsed=%v instances=%d workers=%d..%d shards=%d claim-batch=%d",
		d.cfg.Scenario, r.elapsed.Round(time.Second), d.cfg.Instances, d.cfg.MinWorkers, d.cfg.Workers, d.cfg.Shards, d.cfg.ClaimBatch)
	w("          handler=%v..%v poison=%.2f%% transient=%.2f%%",
		d.cfg.HandlerMin, d.cfg.HandlerMax, 100*d.cfg.PoisonRate, 100*d.cfg.TransientRate)
	w("nemesis:  kills=%d pauses=%d db-faults=%d%s%s",
		r.kills, r.pauses, r.dbFaults,
		map[bool]string{true: fmt.Sprintf(" skew=%v", d.cfg.ClockSkews), false: ""}[d.cfg.skewInjected()],
		map[bool]string{true: " (INTERRUPTED)", false: ""}[r.interrupt])
	w("enqueue:  offered=%d achieved=%d (%.1f%%) errors=%d shape=%s",
		r.offered, r.enqueued, pct(r.enqueued, r.offered), r.enqErrs, d.cfg.EnqueueShape)
	w("          latency p50=%v p95=%v p99=%v mean=%v",
		r.enqLat.percentile(0.50), r.enqLat.percentile(0.95), r.enqLat.percentile(0.99), r.enqLat.mean())
	w("probes:   %d read-only (%.1f/s) empty=%.0f%%; discovery that opens no transaction",
		r.probes, rate(r.probes, r.elapsed.Seconds()), pct(r.probeEmpty, r.probes))
	w("claims:   %d txns (%.1f/s) empty=%.0f%% errors=%d tasks-per-claim=%.2f",
		r.claims, rate(r.claims, r.elapsed.Seconds()), pct(r.claimEmpty, r.claims), r.claimErrs,
		safeDiv(r.claimTasks, r.claims-r.claimEmpty-r.claimErrs))
	w("          latency p50=%v p95=%v p99=%v mean=%v",
		r.claimLat.percentile(0.50), r.claimLat.percentile(0.95), r.claimLat.percentile(0.99), r.claimLat.mean())
	w("complete: %d acks, latency p50=%v p99=%v",
		r.completes, r.completeLat.percentile(0.50), r.completeLat.percentile(0.99))
	w("e2e:      enqueue->first-attempt p50=%v p95=%v p99=%v (over %d fresh deliveries)",
		r.startLat.percentile(0.50), r.startLat.percentile(0.95), r.startLat.percentile(0.99), r.startLat.Count)
	if d.cfg.Scenario == scenarioKeyed {
		w("keyed:    filings=%d errors=%d processed=%d handler=%v (coalescing makes filings != rows;",
			r.keyedCalls, r.keyedErrs, r.keyedProcessed, d.cfg.KeyedHandler)
		w("          the starvation gate is the pre-run stuck-head probe, so e2e above is informational)")
	}
	w("handled:  processed=%d handler-failures=%d sweeps=%d swept=%d%s",
		r.processed, r.fails, r.sweeps, r.swept,
		map[bool]string{true: " (counters are lower bounds: an instance died without a final snapshot)", false: ""}[r.staleCounters])
	w("history:  claims=%d effects=%d distinct=%d duplicates=%d (%.3f%% redelivered)",
		f.claims, f.effects, f.distinctEffects, f.duplicateEffects, pct(int64(f.duplicateEffects), int64(f.distinctEffects)))
	w("          orphan-effects=%d [%s]  attempt-repeats=%d [%s]",
		f.orphanEffects, check(f.orphanEffects == 0), f.attemptRepeats, check(f.attemptRepeats == 0))
	w("          stale-effects=%d overlapping-leases=%d (hard failures only without pause/skew/crash injection)",
		f.staleEffects, f.overlaps)
	w("conserve: lost=%d [%s]  poison-escaped=%d poison-missing=%d [%s]",
		f.lostTasks, check(f.lostTasks == 0),
		f.poisonEscaped, f.poisonMissing, check(f.poisonEscaped == 0 && f.poisonMissing == 0))
	w("          collateral-dlq=%d (chaos-exhausted) phantoms=%d under-attempted=%d [%s]",
		f.collateralDLQ, f.phantoms, f.underAttempted, check(f.phantoms == 0 && f.underAttempted == 0))
	w("          dead-lettered=%d scripted-poison=%d  drained=%v [%s]",
		len(r.quiesce.dlq), r.poison, r.drained, check(r.drained))
	if r.counterEnq > 0 && r.counterSum >= 0 {
		w("          counter-sum=%d paced-enqueues=%d [%s] (enqueue-txn atomicity)",
			r.counterSum, r.counterEnq, check(r.counterSum == r.counterEnq))
	}
	w("quiesce:  leftover pending=%d leased=%d [%s]",
		r.quiesce.pending, r.quiesce.leased, check(r.quiesce.pending == 0 && r.quiesce.leased == 0))
	w("queue:    pending=%d leased=%d failed=%d oldest=%v",
		r.queueStats.Pending, r.queueStats.Leased, r.queueStats.Failed, r.queueStats.OldestPendingAge.Round(time.Second))
	for _, v := range f.violations {
		w("VIOLATION: %s", v)
	}
	w("artifacts: %s (snapshots.csv, summary.json, ledgers)", d.cfg.RunDir)
	b.WriteString(spannerSysHints(d.cfg))
	return b.String()
}

// writeSummary persists the same numbers as a machine-readable artifact.
func (d *driver) writeSummary(r runResult, f findings) error {
	summary := map[string]any{
		"config": d.cfg, "offered": r.offered, "enqueued": r.enqueued,
		"enqueue_errors": r.enqErrs, "enqueue_lat": r.enqLat,
		"probes": r.probes, "probe_empty": r.probeEmpty,
		"claims": r.claims, "claim_empty": r.claimEmpty, "claim_errors": r.claimErrs,
		"claim_lat": r.claimLat, "complete_lat": r.completeLat, "start_lat": r.startLat,
		"processed": r.processed, "handler_failures": r.fails,
		"sweeps": r.sweeps, "swept": r.swept,
		"effects_total": f.effects, "effects_distinct": f.distinctEffects,
		"effects_duplicates": f.duplicateEffects,
		"orphan_effects":     f.orphanEffects, "stale_effects": f.staleEffects,
		"lease_overlaps": f.overlaps, "attempt_repeats": f.attemptRepeats,
		"lost_tasks": f.lostTasks, "poison_escaped": f.poisonEscaped,
		"poison_missing": f.poisonMissing, "collateral_dlq": f.collateralDLQ,
		"phantoms": f.phantoms, "under_attempted": f.underAttempted,
		"violations":       f.violations,
		"expected_poison":  r.poison,
		"dead_lettered":    len(r.quiesce.dlq),
		"leftover_pending": r.quiesce.pending, "leftover_leased": r.quiesce.leased,
		"counter_sum": r.counterSum, "counter_enqueues": r.counterEnq,
		"drained": r.drained, "kills": r.kills, "pauses": r.pauses,
		"db_faults": r.dbFaults, "elapsed_seconds": r.elapsed.Seconds(),
		"keyed_filings": r.keyedCalls, "keyed_errors": r.keyedErrs,
		"keyed_processed": r.keyedProcessed,
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding summary: %w", err)
	}
	if err := os.WriteFile(filepath.Join(d.cfg.RunDir, "summary.json"), data, 0o600); err != nil {
		return fmt.Errorf("writing summary: %w", err)
	}
	return nil
}

// verdict turns the analysis into the run's exit status.
func (d *driver) verdict(r runResult, f findings) error {
	// An interrupted run reports whatever it saw but is not a verdict.
	if r.interrupt {
		return nil
	}
	if !r.drained {
		return fmt.Errorf("queue did not drain within %v", d.cfg.DrainTimeout)
	}
	if r.quiesce.pending != 0 || r.quiesce.leased != 0 {
		return fmt.Errorf("quiescence failed: %d pending, %d leased rows survived the drain",
			r.quiesce.pending, r.quiesce.leased)
	}
	if f.failed() {
		return fmt.Errorf("verification failed with %d violation(s):\n  %s",
			len(f.violations), strings.Join(f.violations, "\n  "))
	}
	if counterOK := r.counterEnq == 0 || r.counterSum < 0 || r.counterSum == r.counterEnq; !counterOK {
		return fmt.Errorf("counter-sum %d does not match paced enqueues %d (enqueue-txn atomicity broken)",
			r.counterSum, r.counterEnq)
	}
	if d.cfg.Scenario == scenarioKeyed {
		// The load phase is vacuous unless keyed heads actually churned
		// alongside the synthetic load; the starvation gate itself is the
		// deterministic pre-run probe (probeStuckHead).
		if r.keyedCalls == 0 || r.keyedProcessed == 0 {
			return fmt.Errorf("keyed scenario generated no keyed traffic (filings=%d processed=%d)",
				r.keyedCalls, r.keyedProcessed)
		}
	}
	return nil
}

func safeDiv(n, d int64) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

// spannerSysHints prints ready-to-run queries against the statistics tables
// that hold the server-side truth the harness cannot see: transaction aborts,
// lock waits, and the claim query's scanned-vs-returned ratio (the tombstone
// signal). Only real Spanner populates them; the emulator does not.
func spannerSysHints(cfg Cfg) string {
	if cfg.Emulator != "" {
		return "note: SPANNER_SYS statistics are not available on the emulator; run against real Spanner for aborts/locks/scan ratios\n"
	}
	exec := fmt.Sprintf("gcloud spanner databases execute-sql %s --instance=%s --project=%s --sql=",
		cfg.Database, cfg.Instance, cfg.Project)
	return `server-side truth (run within ~10min of the load):
  ` + exec + `"SELECT fprint, commit_attempt_count, commit_abort_count, avg_commit_latency_seconds FROM SPANNER_SYS.TXN_STATS_TOP_10MINUTE ORDER BY commit_abort_count DESC LIMIT 10"
  ` + exec + `"SELECT CAST(row_range_start_key AS STRING) AS row_range, lock_wait_seconds FROM SPANNER_SYS.LOCK_STATS_TOP_10MINUTE ORDER BY lock_wait_seconds DESC LIMIT 10"
  ` + exec + `"SELECT text, execution_count, avg_rows_scanned, avg_rows, avg_cpu_seconds FROM SPANNER_SYS.QUERY_STATS_TOP_10MINUTE ORDER BY avg_cpu_seconds DESC LIMIT 10"
`
}
