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
	"strings"
	"testing"
	"time"

	gspanner "cloud.google.com/go/spanner"

	devicedomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/jobs/hostreconciler"
	"cloud-cuttlefish-orchestrator/internal/jobs/leaderelection"
)

// Every checker here is proven able to fire: each test plants exactly the
// violation its checker advertises and asserts it goes red, alongside the
// clean case that must stay green. A checker without such a proof is how
// "lost or preempted" verified nothing and how a gate that structurally
// cannot fail ships.

func newTestFindings() *findings {
	return &findings{
		opCounts:          make(map[string]int),
		ackedHostIDs:      make(map[string]struct{}),
		ackedDeviceIDs:    make(map[string]string),
		deviceDeleteTried: make(map[string]struct{}),
		hostDeleteTried:   make(map[string]struct{}),
		idemKeyIDs:        make(map[string][]string),
	}
}

func quietCfg() *Cfg { return &Cfg{} }

func burnerCfg() *Cfg { return &Cfg{KillEvery: time.Second} }

func TestCheckCapacity(t *testing.T) {
	f := newTestFindings()
	checkCapacity(f, []hostRow{
		{id: "h-over", status: "running", deviceCount: 5, maxDevices: 4},
		{id: "h-full", status: "running", deviceCount: 4, maxDevices: 4},
		{id: "h-deleted", status: statusDeleted, deviceCount: 9, maxDevices: 4},
	})
	if len(f.overCapacity) != 1 || !strings.Contains(f.overCapacity[0], "h-over") {
		t.Fatalf("over-capacity host not flagged exactly once: %v", f.overCapacity)
	}
}

func TestCheckPins(t *testing.T) {
	lapsed := gspanner.NullTime{Valid: true, Time: time.Now().Add(-2 * time.Minute)}
	fresh := gspanner.NullTime{Valid: true, Time: time.Now().Add(-10 * time.Second)}
	f := newTestFindings()
	checkPins(f, []deviceRow{
		{id: "d-lapsed", status: "running", pinned: true, pinnedUntil: lapsed},
		{id: "d-fresh", status: "running", pinned: true, pinnedUntil: fresh},
		{id: "d-deleted", status: statusDeleted, pinned: true, pinnedUntil: lapsed},
		{id: "d-unpinned", status: "running", pinned: false, pinnedUntil: lapsed},
	}, time.Minute)
	if len(f.lapsedPins) != 1 || !strings.Contains(f.lapsedPins[0], "d-lapsed") {
		t.Fatalf("lapsed pin not flagged exactly once: %v", f.lapsedPins)
	}

	// The skew-widened grace must absorb a reaper running late by its offset.
	f = newTestFindings()
	checkPins(f, []deviceRow{
		{id: "d-lapsed", status: "running", pinned: true, pinnedUntil: lapsed},
	}, 3*time.Minute)
	if len(f.lapsedPins) != 0 {
		t.Fatalf("pin inside the widened grace flagged: %v", f.lapsedPins)
	}
}

func TestConserveCounters(t *testing.T) {
	f := newTestFindings()
	live := conserveCounters(f, []hostRow{
		{id: "h-drift", status: "running", deviceCount: 2, protectedCount: 1},
		{id: "h-ok", status: "running", deviceCount: 1},
		{id: "h-gone", status: statusDeleted, deviceCount: 7},
	}, []deviceRow{
		{id: "d1", hostID: "h-drift", status: "running"},
		{id: "d2", hostID: "h-drift", status: statusDeleted},
		{id: "d3", hostID: "h-ok", status: "running"},
	})
	if live != 2 {
		t.Fatalf("live hosts = %d, want 2", live)
	}
	if len(f.deviceCountDrift) != 1 || !strings.Contains(f.deviceCountDrift[0], "h-drift") {
		t.Fatalf("device-count drift not flagged exactly once: %v", f.deviceCountDrift)
	}
	if len(f.protectedCountDrift) != 1 {
		t.Fatalf("protected-count drift not flagged: %v", f.protectedCountDrift)
	}
}

func TestJudgePreempt(t *testing.T) {
	ev := driftEvent{kind: "preempt", hostID: "h1", instance: "vm-1"}
	cases := []struct {
		name string
		cfg  *Cfg
		host hostRow
		want string // substring of the violation; "" = must stay green
	}{
		{"lost mark fires", quietCfg(), hostRow{id: "h1", status: statusDeleted, errMsg: hostreconciler.LostMarkReason}, "marked lost"},
		{"stuck preempted fires", quietCfg(), hostRow{id: "h1", status: statusPreempted}, "recovery never completed"},
		{"error without burners fires", quietCfg(), hostRow{id: "h1", status: statusError, errMsg: "provision dead-lettered"}, "only drift chaos"},
		{"error with burners tolerated", burnerCfg(), hostRow{id: "h1", status: statusError}, ""},
		{"recovered stays green", quietCfg(), hostRow{id: "h1", status: "running"}, ""},
		{"user tombstone stays green", quietCfg(), hostRow{id: "h1", status: statusDeleted, errMsg: "deleted by user"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTestFindings()
			judgePreempt(f, tc.cfg, ev, tc.host)
			if tc.want == "" {
				if len(f.driftOutcomes) != 0 {
					t.Fatalf("unexpected violation: %v", f.driftOutcomes)
				}
				return
			}
			if len(f.driftOutcomes) != 1 || !strings.Contains(f.driftOutcomes[0], tc.want) {
				t.Fatalf("want violation containing %q, got %v", tc.want, f.driftOutcomes)
			}
		})
	}
}

func TestJudgeVanish(t *testing.T) {
	alive := func() map[string]bool { return map[string]bool{"vm-new": true} }
	dead := func() map[string]bool { return map[string]bool{} }
	cases := []struct {
		name    string
		ev      driftEvent
		host    hostRow
		liveNow func() map[string]bool
		want    bool
	}{
		{
			"unhonored vanish fires",
			driftEvent{hostID: "h1", instance: "vm-1", hostStatus: "running"},
			hostRow{id: "h1", status: "running", gceName: "vm-1"},
			dead, true,
		},
		{
			"honored tombstone green",
			driftEvent{hostID: "h1", instance: "vm-1", hostStatus: "running"},
			hostRow{id: "h1", status: statusDeleted},
			dead, false,
		},
		{
			"mid-provision redriven green",
			driftEvent{hostID: "h1", instance: "vm-old", hostStatus: "provisioning"},
			hostRow{id: "h1", status: "running", gceName: "vm-new"},
			alive, false,
		},
		{
			"mid-provision dangling claim fires",
			driftEvent{hostID: "h1", instance: "vm-old", hostStatus: "provisioning"},
			hostRow{id: "h1", status: "running", gceName: "vm-old"},
			dead, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTestFindings()
			judgeVanish(f, tc.ev, tc.host, tc.liveNow)
			if got := len(f.driftOutcomes) > 0; got != tc.want {
				t.Fatalf("violation=%v, want %v (%v)", got, tc.want, f.driftOutcomes)
			}
		})
	}
}

func TestCheckDriftOutcomesResolvesByInstanceName(t *testing.T) {
	// The injection-time owner lookup can transiently fail, leaving the
	// event with an empty hostID. The checker must still judge it via the
	// final row claiming the dead instance's name. This exact gap let a
	// recovery-less run pass the drift checker unjudged.
	d := &driver{}
	d.driftEvents = []driftEvent{{kind: "preempt", hostID: "", instance: "vm-stuck"}}
	f := newTestFindings()
	d.checkDriftOutcomes(f, []hostRow{
		{id: "h1", status: statusPreempted, gceName: "vm-stuck"},
	})
	if len(f.driftOutcomes) != 1 || !strings.Contains(f.driftOutcomes[0], "recovery never completed") {
		t.Fatalf("empty-hostID preempt event not judged by instance name: %v", f.driftOutcomes)
	}

	// A recovered host renamed its instance, so the dead name matches no
	// row. That must stay green.
	f = newTestFindings()
	d.checkDriftOutcomes(f, []hostRow{
		{id: "h1", status: "running", gceName: "vm-new-incarnation"},
	})
	if len(f.driftOutcomes) != 0 {
		t.Fatalf("renamed recovery flagged: %v", f.driftOutcomes)
	}
}

func TestCheckAckedCreates(t *testing.T) {
	f := newTestFindings()
	f.ackedHostIDs["h-lost"] = struct{}{}
	f.ackedHostIDs["h-alive"] = struct{}{}
	f.ackedHostIDs["h-deleted"] = struct{}{}
	f.hostDeleteTried["h-deleted"] = struct{}{}
	f.hostDeleteTried["h-cascade"] = struct{}{}
	f.ackedDeviceIDs["d-lost"] = "h-alive"
	f.ackedDeviceIDs["d-own-delete"] = "h-alive"
	f.deviceDeleteTried["d-own-delete"] = struct{}{}
	f.ackedDeviceIDs["d-cascaded"] = "h-cascade"
	f.ackedDeviceIDs["d-alive"] = "h-alive"

	checkAckedCreates(f,
		[]hostRow{{id: "h-alive", status: "running"}},
		[]deviceRow{{id: "d-alive", hostID: "h-alive", status: "running"}})

	joined := strings.Join(f.ackedLost, "\n")
	if len(f.ackedLost) != 2 {
		t.Fatalf("want exactly the uncommanded losses, got %v", f.ackedLost)
	}
	if !strings.Contains(joined, "h-lost") || !strings.Contains(joined, "d-lost") {
		t.Fatalf("h-lost and d-lost must be flagged: %v", f.ackedLost)
	}
}

func TestCheckIdem(t *testing.T) {
	f := newTestFindings()
	f.idemKeyIDs["k-good"] = []string{"d1", "d1", "d1"}
	f.idemKeyIDs["k-split"] = []string{"d1", "d2"}
	checkIdem(f)
	if len(f.idemViolations) != 1 || !strings.Contains(f.idemViolations[0], "k-split") {
		t.Fatalf("split key not flagged exactly once: %v", f.idemViolations)
	}
}

func TestReadOpsLine(t *testing.T) {
	f := newTestFindings()
	d := &driver{}
	lines := []string{
		"op device_create ok d1 123 host=h1",
		"op device_create err d-failed 124 \"capacity\"",
		"op host_create ok h1 125",
		"op device_delete err d9 126 \"deadline\"",
		"op host_delete ok h9 127",
		"idem k1 da db host=h2",
		"garbage",
	}
	for _, line := range lines {
		d.readOpsLine(f, strings.Fields(line))
	}
	if f.ackedDeviceIDs["d1"] != "h1" {
		t.Fatalf("acked device host not parsed: %v", f.ackedDeviceIDs)
	}
	if _, acked := f.ackedDeviceIDs["d-failed"]; acked {
		t.Fatal("an errored create must never count as acked")
	}
	if _, ok := f.ackedHostIDs["h1"]; !ok {
		t.Fatal("acked host not collected")
	}
	if _, ok := f.deviceDeleteTried["d9"]; !ok {
		t.Fatal("an errored delete must still count as attempted (it may have committed)")
	}
	if _, ok := f.hostDeleteTried["h9"]; !ok {
		t.Fatal("host delete attempt not collected")
	}
	if f.ackedDeviceIDs["da"] != "h2" || f.ackedDeviceIDs["db"] != "h2" {
		t.Fatalf("idem devices not acked with host: %v", f.ackedDeviceIDs)
	}
	if got := f.idemKeyIDs["k1"]; len(got) != 2 {
		t.Fatalf("idem key grouping = %v", got)
	}
	if f.opCounts["device_create/ok"] != 1 || f.opCounts["device_create/err"] != 1 {
		t.Fatalf("op tallies wrong: %v", f.opCounts)
	}
}

func TestCheckEvidence(t *testing.T) {
	launched := map[string][]string{"10.0.0.1": {"cvd12345678"}}

	t.Run("vacuous run fires every leg", func(t *testing.T) {
		f := newTestFindings()
		f.opCounts[opDeviceDelete+"/err"] = 6
		checkEvidence(f, quietCfg(), nil, nil, nil)
		joined := strings.Join(f.vacuity, "\n")
		for _, want := range []string{"host_create", "device_create", "device_delete", "HO boundary"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("vacuity missing %q: %v", want, f.vacuity)
			}
		}
	})

	t.Run("healthy run stays green", func(t *testing.T) {
		f := newTestFindings()
		f.opCounts[opHostCreate+"/ok"] = 1
		f.opCounts[opDeviceCreate+"/ok"] = 1
		checkEvidence(f, quietCfg(), nil, nil, launched)
		if len(f.vacuity) != 0 {
			t.Fatalf("clean run tripped vacuity: %v", f.vacuity)
		}
	})

	t.Run("error resources fire only without burners", func(t *testing.T) {
		hosts := []hostRow{{id: "h-err", status: statusError, errMsg: "boom"}}
		devices := []deviceRow{{id: "d-err", status: statusError}}
		f := newTestFindings()
		f.opCounts[opHostCreate+"/ok"] = 1
		f.opCounts[opDeviceCreate+"/ok"] = 1
		checkEvidence(f, quietCfg(), hosts, devices, launched)
		if len(f.unexpectedErrs) != 2 {
			t.Fatalf("error resources not flagged without burners: %v", f.unexpectedErrs)
		}
		f = newTestFindings()
		f.opCounts[opHostCreate+"/ok"] = 1
		f.opCounts[opDeviceCreate+"/ok"] = 1
		checkEvidence(f, burnerCfg(), hosts, devices, launched)
		if len(f.unexpectedErrs) != 0 {
			t.Fatalf("error resources flagged despite burners: %v", f.unexpectedErrs)
		}
	})
}

func TestJudgeTask(t *testing.T) {
	f := newTestFindings()
	judgeTask(f, quietCfg(), taskRow{id: "t1", state: "pending"})
	if f.tasksLeft != 1 {
		t.Fatalf("pending not counted: %d", f.tasksLeft)
	}

	f = newTestFindings()
	judgeTask(f, quietCfg(), taskRow{id: "t2", taskType: "x", state: "failed", attempts: 2, maxAttempts: 5})
	if len(f.underAttDLQ) != 1 {
		t.Fatalf("under-attempted dead-letter not flagged: %v", f.underAttDLQ)
	}
	if len(f.unexpectedDLQ) != 1 {
		t.Fatalf("dead-letter without burners not flagged: %v", f.unexpectedDLQ)
	}

	f = newTestFindings()
	judgeTask(f, burnerCfg(), taskRow{id: "t3", taskType: "x", state: "failed", attempts: 5, maxAttempts: 5})
	if len(f.underAttDLQ) != 0 || len(f.unexpectedDLQ) != 0 {
		t.Fatalf("attempt-exhausted dead-letter under chaos must pass: %v %v", f.underAttDLQ, f.unexpectedDLQ)
	}
}

func TestPlacementRowChecks(t *testing.T) {
	f := newTestFindings()
	placementRowChecks(f, []hostRow{
		{id: "h-moved", zonePinned: true, requestedZone: "zone-a", zone: "zone-b", status: "running"},
		{id: "h-held", zonePinned: true, requestedZone: "zone-a", zone: "zone-a", status: "running"},
		{id: "h-dlq", zonePinned: false, status: statusError, errMsg: "ZONE_RESOURCE_POOL_EXHAUSTED", zone: "zone-a"},
		{id: "h-otherr", zonePinned: false, status: statusError, errMsg: "quota denied", zone: "zone-a"},
	})
	joined := strings.Join(f.placementViolations, "\n")
	if len(f.placementViolations) != 2 {
		t.Fatalf("want the moved pin and the stockout dead-letter, got %v", f.placementViolations)
	}
	if !strings.Contains(joined, "h-moved") || !strings.Contains(joined, "h-dlq") {
		t.Fatalf("wrong hosts flagged: %v", f.placementViolations)
	}
}

func TestCheckOrphans(t *testing.T) {
	f := newTestFindings()
	devID := "abcdef1234567890"
	checkOrphans(f,
		[]hostRow{{id: "h1", status: "running", gceName: "vm-live"}},
		[]deviceRow{{id: devID, hostID: "h1", status: "running"}},
		[]string{"vm-live", "vm-leaked"},
		map[string][]string{"10.0.0.1": {
			"cvd" + devID[:8],
			"cvd-orphan",
			devicedomain.WarmGroupName,
		}},
		[]string{"stop on in-flight launch"},
	)
	if len(f.leakedVMs) != 1 || !strings.Contains(f.leakedVMs[0], "vm-leaked") {
		t.Fatalf("leaked VM not flagged exactly once: %v", f.leakedVMs)
	}
	if len(f.leakedCVDs) != 1 || !strings.Contains(f.leakedCVDs[0], "cvd-orphan") {
		t.Fatalf("leaked CVD not flagged exactly once (warm group must be skipped): %v", f.leakedCVDs)
	}
	if len(f.actuationRaces) != 1 {
		t.Fatalf("actuation violations not passed through: %v", f.actuationRaces)
	}
}

func TestJudgeElection(t *testing.T) {
	plane := leaderelection.BackgroundPlaneLease
	quiet := []string(nil)
	killed := []string{"2 kill(s)"}

	// The healthy shape: one lease, held by somebody, nothing took it away.
	f := newTestFindings()
	judgeElection(f, []string{plane}, 1, quiet)
	if len(f.electionViolations) != 0 {
		t.Fatalf("one held election is the shape production runs: %v", f.electionViolations)
	}

	// A job electing on a lease of its own.
	f = newTestFindings()
	judgeElection(f, []string{plane, "taskreclaimer"}, 1, quiet)
	if len(f.electionViolations) != 1 || !strings.Contains(f.electionViolations[0], "taskreclaimer") {
		t.Fatalf("a second election not flagged exactly once: %v", f.electionViolations)
	}

	// Nothing ever led: every leader-gated assertion in the run was vacuous.
	f = newTestFindings()
	judgeElection(f, []string{plane}, 0, quiet)
	if len(f.electionViolations) != 1 {
		t.Fatalf("a plane nobody led must fail the run: %v", f.electionViolations)
	}

	// The holder was stopped and the plane stayed with it.
	f = newTestFindings()
	judgeElection(f, []string{plane}, 1, killed)
	if len(f.electionViolations) != 1 || !strings.Contains(f.electionViolations[0], "never changed hands") {
		t.Fatalf("a run that never handed over must fail: %v", f.electionViolations)
	}

	// The same run, with the handover the nemesis exists to force.
	f = newTestFindings()
	judgeElection(f, []string{plane}, 2, killed)
	if len(f.electionViolations) != 0 {
		t.Fatalf("a plane that moved to the survivor is the passing case: %v", f.electionViolations)
	}
}

// Which nemeses must have taken the plane away from its holder: kills, and
// only kills. A pause is NOT a forcer, because the emulator serializes
// read-write transactions globally, so a SIGSTOPped holder's parked
// transaction blocks the survivor's acquire for the whole pause, and the
// handover the pause would force on real Spanner cannot be observed here
// (handoverForcers's comment holds the full account).
func TestHandoverForcers(t *testing.T) {
	if got := handoverForcers(0); len(got) != 0 {
		t.Fatalf("a run with no kills forces no handover: %v", got)
	}
	if got := handoverForcers(1); len(got) != 1 {
		t.Fatalf("a kill forces a handover: %v", got)
	}
}
