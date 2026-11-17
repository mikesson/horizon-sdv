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
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistPercentiles(t *testing.T) {
	h := &hist{}
	for range 90 {
		h.observe(1 * time.Millisecond)
	}
	for range 10 {
		h.observe(80 * time.Millisecond)
	}
	s := h.snapshot()
	if s.Count != 100 {
		t.Fatalf("count = %d, want 100", s.Count)
	}
	// Bucket upper bounds are powers of two over 100µs: 1ms lands under 1.6ms,
	// 80ms under 102.4ms.
	if p := s.percentile(0.50); p > 2*time.Millisecond {
		t.Errorf("p50 = %v, want <= 1.6ms", p)
	}
	if p := s.percentile(0.99); p < 50*time.Millisecond || p > 110*time.Millisecond {
		t.Errorf("p99 = %v, want ~102ms", p)
	}
}

func TestHistMerge(t *testing.T) {
	a, b := &hist{}, &hist{}
	a.observe(time.Millisecond)
	b.observe(time.Second)
	s := a.snapshot()
	s.merge(b.snapshot())
	if s.Count != 2 {
		t.Fatalf("merged count = %d, want 2", s.Count)
	}
	if p := s.percentile(1.0); p < time.Second {
		t.Errorf("merged p100 = %v, want >= 1s", p)
	}
}

func TestHistOverflowBucket(t *testing.T) {
	h := &hist{}
	h.observe(24 * time.Hour)
	s := h.snapshot()
	if s.Buckets[histBuckets-1] != 1 {
		t.Errorf("oversized observation not in last bucket: %+v", s.Buckets)
	}
}

func TestOutcomeForDeterministicAndProportional(t *testing.T) {
	const n = 100000
	counts := map[outcome]int{}
	for i := range n {
		id := fmt.Sprintf("task-%d", i)
		o := outcomeFor(id, 0.05, 0.20)
		if again := outcomeFor(id, 0.05, 0.20); again != o {
			t.Fatalf("outcomeFor(%q) not deterministic", id)
		}
		counts[o]++
	}
	poisonFrac := float64(counts[outcomePoison]) / n
	transientFrac := float64(counts[outcomeTransient]) / n
	if poisonFrac < 0.04 || poisonFrac > 0.06 {
		t.Errorf("poison fraction = %.4f, want ~0.05", poisonFrac)
	}
	if transientFrac < 0.18 || transientFrac > 0.22 {
		t.Errorf("transient fraction = %.4f, want ~0.20", transientFrac)
	}
}

func TestLedgerEventRoundTrip(t *testing.T) {
	dir := t.TempDir()
	l, err := openLedger(dir, 0, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(90 * time.Second)
	l.claim("a", "h1", 1, expiry)
	if err := l.effect("a", "h1", 1); err != nil {
		t.Fatal(err)
	}
	l.complete("a", "h1")
	l.fail("b", "h1", 3, true)
	if err := l.close(); err != nil {
		t.Fatal(err)
	}

	events, err := readEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("parsed %d events, want 4: %+v", len(events), events)
	}
	byKind := map[string]event{}
	for _, e := range events {
		byKind[e.kind] = e
	}
	c := byKind[evClaim]
	if c.task != "a" || c.holder != "h1" || c.attempt != 1 {
		t.Errorf("claim = %+v", c)
	}
	if got := c.expiry.UnixMicro(); got != expiry.UnixMicro() {
		t.Errorf("claim expiry = %d, want %d", got, expiry.UnixMicro())
	}
	if f := byKind[evFail]; !f.dead || f.attempt != 3 {
		t.Errorf("fail = %+v, want dead attempt 3", f)
	}
}

// hb (history builder) keeps checker tests terse.
type hb struct {
	evs []event
	t0  time.Time
}

func newHB() *hb { return &hb{t0: time.Unix(1_700_000_000, 0)} }

func (h *hb) at(sec int) time.Time { return h.t0.Add(time.Duration(sec) * time.Second) }

func (h *hb) claim(task, holder string, attempt, atSec, expirySec int) *hb {
	h.evs = append(h.evs, event{
		kind: evClaim, task: task, holder: holder,
		attempt: attempt, at: h.at(atSec), expiry: h.at(expirySec),
	})
	return h
}

func (h *hb) effect(task, holder string, attempt, atSec int) *hb {
	h.evs = append(h.evs, event{
		kind: evEffect, task: task, holder: holder,
		attempt: attempt, at: h.at(atSec),
	})
	return h
}

func (h *hb) complete(task, holder string, atSec int) *hb {
	h.evs = append(h.evs, event{kind: evComplete, task: task, holder: holder, at: h.at(atSec)})
	return h
}

func (h *hb) fail(task, holder string, attempt, atSec int, dead bool) *hb {
	h.evs = append(h.evs, event{
		kind: evFail, task: task, holder: holder,
		attempt: attempt, at: h.at(atSec), dead: dead,
	})
	return h
}

func TestAnalyzeCleanHistory(t *testing.T) {
	h := newHB().
		claim("a", "h1", 1, 0, 90).effect("a", "h1", 1, 5).complete("a", "h1", 6).
		claim("b", "h2", 1, 1, 91).effect("b", "h2", 1, 4).complete("b", "h2", 5)
	f := analyze(h.evs, map[string]outcome{"a": outcomeSucceed, "b": outcomeSucceed}, nil, historyOpts{})
	if f.failed() {
		t.Fatalf("clean history failed: %v", f.violations)
	}
	if f.distinctEffects != 2 || f.duplicateEffects != 0 || f.claims != 2 {
		t.Errorf("findings = %+v", f)
	}
}

func TestAnalyzeFlagsLostTask(t *testing.T) {
	f := analyze(nil, map[string]outcome{"gone": outcomeSucceed}, nil, historyOpts{})
	if f.lostTasks != 1 || !f.failed() {
		t.Fatalf("lost task not flagged: %+v", f)
	}
}

func TestAnalyzePoisonAccounting(t *testing.T) {
	// Poison in the DLQ with exhausted attempts: clean.
	f := analyze(nil, map[string]outcome{"p": outcomePoison},
		[]dlqRow{{id: "p", attempts: 3, maxAttempts: 3}}, historyOpts{})
	if f.failed() {
		t.Fatalf("dead-lettered poison flagged: %v", f.violations)
	}
	// Poison missing from the DLQ.
	f = analyze(nil, map[string]outcome{"p": outcomePoison}, nil, historyOpts{})
	if f.poisonMissing != 1 || !f.failed() {
		t.Fatalf("missing poison not flagged: %+v", f)
	}
	// Poison with an effect.
	h := newHB().claim("p", "h1", 1, 0, 90).effect("p", "h1", 1, 1)
	f = analyze(h.evs, map[string]outcome{"p": outcomePoison},
		[]dlqRow{{id: "p", attempts: 3, maxAttempts: 3}}, historyOpts{})
	if f.poisonEscaped != 1 || !f.failed() {
		t.Fatalf("poison effect not flagged: %+v", f)
	}
}

func TestAnalyzeFlagsOrphanEffect(t *testing.T) {
	h := newHB().effect("a", "h1", 1, 5).complete("a", "h1", 6)
	f := analyze(h.evs, map[string]outcome{"a": outcomeSucceed}, nil, historyOpts{})
	if f.orphanEffects != 1 || !f.failed() {
		t.Fatalf("orphan effect not flagged: %+v", f)
	}
}

func TestAnalyzeFlagsAttemptRepeat(t *testing.T) {
	h := newHB().
		claim("a", "h1", 1, 0, 90).fail("a", "h1", 1, 5, false).
		claim("a", "h2", 1, 10, 100). // attempt 1 again: lost update
		effect("a", "h2", 1, 12).complete("a", "h2", 13)
	f := analyze(h.evs, map[string]outcome{"a": outcomeSucceed}, nil, historyOpts{})
	if f.attemptRepeats != 1 || !f.failed() {
		t.Fatalf("attempt repeat not flagged: %+v", f)
	}
}

func TestAnalyzeStaleEffectPolicy(t *testing.T) {
	h := newHB().
		claim("a", "h1", 1, 0, 90).
		effect("a", "h1", 1, 120). // past expiry: a paused worker waking up
		claim("a", "h2", 2, 95, 185).effect("a", "h2", 2, 100).complete("a", "h2", 101)
	enq := map[string]outcome{"a": outcomeSucceed}

	f := analyze(h.evs, enq, nil, historyOpts{})
	if f.staleEffects != 1 || !f.failed() {
		t.Fatalf("stale effect not flagged without pause injection: %+v", f)
	}
	f = analyze(h.evs, enq, nil, historyOpts{pausesInjected: true})
	if f.staleEffects != 1 || f.failed() {
		t.Fatalf("stale effect should be tolerated under pause injection: %v", f.violations)
	}
}

func TestAnalyzeOverlapPolicy(t *testing.T) {
	// h2 claims at t=50 while h1's lease runs to t=90 with no outcome: overlap.
	overlapping := newHB().
		claim("a", "h1", 1, 0, 90).
		claim("a", "h2", 2, 50, 140).
		effect("a", "h2", 2, 55).complete("a", "h2", 56).effect("a", "h1", 1, 60)
	enq := map[string]outcome{"a": outcomeSucceed}

	f := analyze(overlapping.evs, enq, nil, historyOpts{})
	if f.overlaps != 1 || !f.failed() {
		t.Fatalf("overlap not flagged with accurate clocks: %+v", f)
	}
	f = analyze(overlapping.evs, enq, nil, historyOpts{skewInjected: true})
	if f.overlaps != 1 || f.failed() {
		t.Fatalf("overlap should be a measurement under skew: %v", f.violations)
	}

	// A Fail between the claims legitimizes the successor.
	resolved := newHB().
		claim("a", "h1", 1, 0, 90).fail("a", "h1", 1, 10, false).
		claim("a", "h2", 2, 50, 140).effect("a", "h2", 2, 55).complete("a", "h2", 56)
	f = analyze(resolved.evs, enq, nil, historyOpts{})
	if f.overlaps != 0 || f.failed() {
		t.Fatalf("resolved handoff misread as overlap: %+v %v", f, f.violations)
	}
}

func TestAnalyzeCollateralDLQPolicy(t *testing.T) {
	enq := map[string]outcome{"s": outcomeSucceed}
	dlq := []dlqRow{{id: "s", attempts: 3, maxAttempts: 3}}

	f := analyze(nil, enq, dlq, historyOpts{crashesPossible: true})
	if f.collateralDLQ != 1 || f.failed() {
		t.Fatalf("collateral DLQ should be tolerated under chaos: %+v %v", f, f.violations)
	}
	// Skew and pause burn attempt budgets via premature reclaims, so no crash
	// is required for a healthy task to exhaust.
	f = analyze(nil, enq, dlq, historyOpts{skewInjected: true})
	if f.collateralDLQ != 1 || f.failed() {
		t.Fatalf("collateral DLQ should be tolerated under skew: %v", f.violations)
	}
	f = analyze(nil, enq, dlq, historyOpts{pausesInjected: true})
	if f.failed() {
		t.Fatalf("collateral DLQ should be tolerated under pause: %v", f.violations)
	}
	f = analyze(nil, enq, dlq, historyOpts{})
	if !f.failed() {
		t.Fatalf("collateral DLQ without fault injection not flagged: %+v", f)
	}
}

func TestAnalyzeFlagsPhantomsAndUnderAttempted(t *testing.T) {
	f := analyze(nil, map[string]outcome{}, []dlqRow{{id: "ghost", attempts: 3, maxAttempts: 3}}, historyOpts{})
	if f.phantoms != 1 || !f.failed() {
		t.Fatalf("phantom DLQ row not flagged: %+v", f)
	}
	f = analyze(nil, map[string]outcome{"u": outcomePoison},
		[]dlqRow{{id: "u", attempts: 1, maxAttempts: 3}}, historyOpts{})
	if f.underAttempted != 1 || !f.failed() {
		t.Fatalf("under-attempted DLQ row not flagged: %+v", f)
	}
}

func TestValidateRejectsUnsafeDatabase(t *testing.T) {
	cfg := defaultTestCfg()
	cfg.Database = "orchestrator"
	if err := validate(&cfg); err == nil {
		t.Fatal("expected rejection of a non-loadtest database")
	}
	cfg.Emulator = "localhost:9010"
	if err := validate(&cfg); err != nil {
		t.Fatalf("emulator target rejected: %v", err)
	}
}

func TestValidateEnforcesPoolInvariants(t *testing.T) {
	cfg := defaultTestCfg()
	cfg.Lease = 30 * time.Second
	cfg.HandleTO = 30 * time.Second
	if err := validate(&cfg); err == nil {
		t.Fatal("expected rejection of lease <= handle-timeout")
	}
	cfg = defaultTestCfg()
	cfg.HandlerMax = cfg.HandleTO
	if err := validate(&cfg); err == nil {
		t.Fatal("expected rejection of handler-max >= handle-timeout")
	}
}

func TestValidateNemesisKnobs(t *testing.T) {
	cfg := defaultTestCfg()
	cfg.KillAfterEffects = 5
	cfg.Instances = 1
	if err := validate(&cfg); err == nil {
		t.Fatal("expected rejection of kill-after-effects with a single instance")
	}
	cfg = defaultTestCfg()
	cfg.DBFault = dbFaultCut
	cfg.Emulator = ""
	cfg.Database = "orchestrator-loadtest"
	if err := validate(&cfg); err == nil {
		t.Fatal("expected rejection of db-fault without the emulator")
	}
	cfg = defaultTestCfg()
	cfg.DBFault = "meteor"
	cfg.Emulator = "localhost:9010"
	if err := validate(&cfg); err == nil {
		t.Fatal("expected rejection of an unknown db-fault mode")
	}
}

func TestSkewForCyclesByIndex(t *testing.T) {
	cfg := Cfg{ClockSkews: []time.Duration{0, 2 * time.Second, -2 * time.Second}}
	if got := cfg.skewFor(1); got != 2*time.Second {
		t.Errorf("skewFor(1) = %v", got)
	}
	if got := cfg.skewFor(4); got != 2*time.Second {
		t.Errorf("skewFor(4) = %v, want cycled value", got)
	}
	if !cfg.skewInjected() {
		t.Error("skewInjected() = false with nonzero skews")
	}
	if (&Cfg{}).skewInjected() {
		t.Error("skewInjected() = true with no skews")
	}
}

func TestParseSkews(t *testing.T) {
	got, err := parseSkews("0s, 2s ,-2s")
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{0, 2 * time.Second, -2 * time.Second}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseSkews = %v, want %v", got, want)
		}
	}
	if _, err := parseSkews("2s,banana"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLedgerFileNaming(t *testing.T) {
	dir := t.TempDir()
	l, err := openLedger(dir, 3, 12345)
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	if _, err := os.Stat(filepath.Join(dir, "ledger-3-12345.log")); err != nil {
		t.Errorf("expected per-index-and-pid ledger file: %v", err)
	}
}

func defaultTestCfg() Cfg {
	return Cfg{
		Database:     "orchestrator-loadtest",
		Scenario:     "steady",
		EnqueueShape: "counter",
		Lease:        90 * time.Second,
		HandleTO:     60 * time.Second,
		HandlerMin:   time.Second,
		HandlerMax:   5 * time.Second,
		HotRows:      64,
		MaxAttempts:  3,
	}
}
