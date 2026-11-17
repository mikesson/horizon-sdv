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
	"fmt"
	"slices"
	"sort"
)

// dlqRow is one dead-lettered task read back from the database at quiescence.
type dlqRow struct {
	id          string
	attempts    int64
	maxAttempts int64
}

// historyOpts declares which fault injections ran, because they change what a
// clean history looks like: a paused holder legitimately applies its effect
// after lease expiry, a skewed clock legitimately opens overlapping lease
// windows, and any chaos at all can exhaust a healthy task into the DLQ.
type historyOpts struct {
	pausesInjected  bool // stale effects are expected, not violations
	skewInjected    bool // overlapping lease windows are expected, not violations
	crashesPossible bool // collateral (non-poison) dead-letters are expected
}

// findings is the checker output: counts for the report plus the violations
// that fail the run. Sample messages are capped so a systemic failure does not
// drown the report.
type findings struct {
	// History structure.
	claims           int
	effects          int
	distinctEffects  int
	duplicateEffects int
	orphanEffects    int // effect with no matching claim; always a violation
	staleEffects     int // effect after its claim's lease expiry
	overlaps         int // claim opened inside another live claim window
	attemptRepeats   int // same attempt number claimed twice (lost update)

	// Conservation (per enqueued task ID).
	lostTasks      int // non-poison: no effect and not dead-lettered
	poisonEscaped  int // poison task with an effect
	poisonMissing  int // poison task not in the DLQ
	collateralDLQ  int // non-poison dead-letters (attempt budget burned by chaos)
	phantoms       int // effects or DLQ rows for tasks never enqueued
	underAttempted int // DLQ row with attempts below max_attempts

	violations []string
}

const maxViolationSamples = 20

func (f *findings) violate(format string, args ...any) {
	if len(f.violations) < maxViolationSamples {
		f.violations = append(f.violations, fmt.Sprintf(format, args...))
	} else if len(f.violations) == maxViolationSamples {
		f.violations = append(f.violations, "... further violations elided")
	}
}

func (f *findings) failed() bool { return len(f.violations) > 0 }

// analyze validates the recorded history and the quiescent database state
// against the queue's contract: at-least-once execution with per-task lease
// exclusivity, bounded attempts, and no silent loss. enqueued maps every task
// ID the driver committed to its scripted outcome; dlq holds the failed rows
// read back after drain.
func analyze(events []event, enqueued map[string]outcome, dlq []dlqRow, opts historyOpts) findings {
	var f findings

	byTask := make(map[string][]event)
	for _, e := range events {
		byTask[e.task] = append(byTask[e.task], e)
	}
	for _, evs := range byTask {
		sort.Slice(evs, func(i, j int) bool { return evs[i].at.Before(evs[j].at) })
	}

	effectTasks := make(map[string]struct{})
	for task, evs := range byTask {
		f.checkTask(task, evs, opts)
		for _, e := range evs {
			if e.kind == evEffect {
				effectTasks[task] = struct{}{}
			}
		}
	}

	dlqSet := make(map[string]dlqRow, len(dlq))
	for _, row := range dlq {
		dlqSet[row.id] = row
		if _, known := enqueued[row.id]; !known {
			f.phantoms++
			f.violate("DLQ row %s was never enqueued by this run", row.id)
		}
		if row.attempts < row.maxAttempts {
			f.underAttempted++
			f.violate("DLQ row %s dead-lettered at %d/%d attempts", row.id, row.attempts, row.maxAttempts)
		}
	}
	for task := range effectTasks {
		if _, known := enqueued[task]; !known {
			f.phantoms++
			f.violate("effect recorded for task %s that was never enqueued", task)
		}
	}

	f.checkConservation(enqueued, effectTasks, dlqSet, opts)
	return f
}

// checkTask validates one task's event sequence: every effect happened under a
// lease held by its recorder, attempt numbers are never reused (each claim
// commits attempts+1 under an exclusive row lock, so a repeat means a lost
// update), and no two lease windows overlap without an intervening outcome.
func (f *findings) checkTask(task string, evs []event, opts historyOpts) {
	var claims []event
	seenAttempts := make(map[int]struct{})
	for _, e := range evs {
		switch e.kind {
		case evClaim:
			f.claims++
			if _, dup := seenAttempts[e.attempt]; dup {
				f.attemptRepeats++
				f.violate("task %s: attempt %d claimed twice (lost attempts update)", task, e.attempt)
			}
			seenAttempts[e.attempt] = struct{}{}
			claims = append(claims, e)
		case evEffect:
			f.effects++
			f.checkEffect(task, e, evs, opts)
		}
	}

	// Overlap: a claim that opens while an earlier claim's lease is unexpired
	// and unresolved means two workers held the same task at once. Impossible
	// with accurate clocks (the sweeper only reclaims after expiry); with an
	// injected skew it is the measurement we are after. A SIGKILL can also
	// sever the ledger line of a committed Fail, making the legitimate fast
	// retry that follows look unresolved, so overlaps are hard failures only
	// when neither skew nor crashes were injected.
	for i, a := range claims {
		for _, b := range claims[i+1:] {
			if !b.at.Before(a.expiry) {
				continue
			}
			if !outcomeBetween(evs, a, b) {
				f.overlaps++
				if !opts.skewInjected && !opts.crashesPossible {
					f.violate("task %s: claim by %s at %v inside %s's live lease window (expiry %v)",
						task, b.holder, b.at, a.holder, a.expiry)
				}
			}
		}
	}
}

// checkEffect requires a same-holder same-attempt claim before the effect; an
// effect applied after that claim's expiry is a stale-holder overrun (a paused
// worker waking past its lease), expected under pause injection only.
func (f *findings) checkEffect(task string, e event, evs []event, opts historyOpts) {
	idx := slices.IndexFunc(evs, func(c event) bool {
		return c.kind == evClaim && c.holder == e.holder && c.attempt == e.attempt && !c.at.After(e.at)
	})
	if idx < 0 {
		f.orphanEffects++
		f.violate("task %s: effect by %s attempt %d with no matching claim", task, e.holder, e.attempt)
		return
	}
	if e.at.After(evs[idx].expiry) {
		f.staleEffects++
		if !opts.pausesInjected && !opts.skewInjected {
			f.violate("task %s: effect by %s attempt %d landed %v after its lease expired",
				task, e.holder, e.attempt, e.at.Sub(evs[idx].expiry))
		}
	}
}

// outcomeBetween reports whether holder a resolved its lease (complete or
// fail) between its claim and claim b, which makes b a legitimate successor
// rather than an overlapping owner.
func outcomeBetween(evs []event, a, b event) bool {
	for _, e := range evs {
		if (e.kind == evComplete || e.kind == evFail) &&
			e.holder == a.holder && !e.at.Before(a.at) && !e.at.After(b.at) {
			return true
		}
	}
	return false
}

// checkConservation is the set-level invariant: no enqueued task may vanish.
// Every non-poison task either applied its effect at least once or burned its
// whole attempt budget to injected faults and sits in the DLQ; every poison
// task is in the DLQ and never applied an effect. Collateral dead-letters are
// legitimate under any attempt-burning nemesis: crashes consume the attempt a
// claim persisted, and premature reclaims under clock skew or process pause do
// the same without any crash.
func (f *findings) checkConservation(enqueued map[string]outcome, effects map[string]struct{}, dlq map[string]dlqRow, opts historyOpts) {
	attemptBurners := opts.crashesPossible || opts.skewInjected || opts.pausesInjected
	f.distinctEffects = len(effects)
	f.duplicateEffects = f.effects - f.distinctEffects
	for id, o := range enqueued {
		_, hasEffect := effects[id]
		_, deadLettered := dlq[id]
		if o == outcomePoison {
			if hasEffect {
				f.poisonEscaped++
				f.violate("poison task %s applied an effect", id)
			}
			if !deadLettered {
				f.poisonMissing++
				f.violate("poison task %s is not in the DLQ", id)
			}
			continue
		}
		switch {
		case hasEffect:
		case deadLettered:
			f.collateralDLQ++
			if !attemptBurners {
				f.violate("task %s dead-lettered with no fault injection configured", id)
			}
		default:
			f.lostTasks++
			f.violate("task %s lost: no effect, not dead-lettered", id)
		}
	}
}
