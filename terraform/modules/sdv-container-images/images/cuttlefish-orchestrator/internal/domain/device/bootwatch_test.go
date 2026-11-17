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

package device

import (
	"strings"
	"testing"
	"time"
)

// The incident's logs no longer exist, so these fixtures are synthesized from
// the measured shape (2026-08-02): a healthy boot cycle emits three U-Boot
// banners, one kernel banner and a terminal completion marker across ~430 KB;
// a looping attempt emits two U-Boot banners and one panic across ~24 KB and
// never reaches userspace.

// healthyKernelCycle is one successful boot cycle's kernel.log, ~430 KB.
func healthyKernelCycle() string {
	var b strings.Builder
	b.WriteString("U-Boot SPL 2023.07 (Jan 01 2026)\n")
	b.WriteString("U-Boot 2023.07 (Jan 01 2026 - 00:00:00 +0000)\n")
	b.WriteString("Starting kernel ...\nU-Boot handoff complete\n")
	b.WriteString("[    0.000000] Linux version 6.6.30 (build@host)\n")
	writeFiller(&b, 430*1024)
	b.WriteString("[   61.000000] init: VIRTUAL_DEVICE_BOOT_COMPLETED\n")
	return b.String()
}

// loopingKernelAttempt is one panic-before-init attempt, ~24 KB: the guest
// dies inside the kernel, before the bootloader handoff's third banner and
// long before any userspace marker.
func loopingKernelAttempt() string {
	var b strings.Builder
	b.WriteString("U-Boot SPL 2023.07 (Jan 01 2026)\n")
	b.WriteString("U-Boot 2023.07 (Jan 01 2026 - 00:00:00 +0000)\n")
	b.WriteString("[    0.000000] Linux version 6.6.30 (build@host)\n")
	writeFiller(&b, 23*1024)
	b.WriteString("[    0.730000] Kernel panic - not syncing: Attempted to kill init! exitcode=0x0000000b\n")
	return b.String()
}

func writeFiller(b *strings.Builder, n int) {
	const line = "[    0.100000] pci 0000:00:01.0: reg 0x10: [io  0x1000-0x103f]\n"
	for b.Len() < n {
		b.WriteString(line)
	}
}

func launcherTail(reboots int) []byte {
	var b strings.Builder
	b.WriteString("Instance restarter successfully started\n")
	for range reboots {
		b.WriteString("secure_env detected guest reboot, restarting.\n")
	}
	return []byte(b.String())
}

// tailOf keeps the last BootLogTailBytes, as the bounded fetch would.
func tailOf(s string) []byte {
	if len(s) <= BootLogTailBytes {
		return []byte(s)
	}
	return []byte(s[len(s)-BootLogTailBytes:])
}

// TestThresholdsHealthyCeilingAndTailReachability is the dual check every
// threshold must pass: above the measured healthy worst case (or the rule
// fires on every healthy device) AND reachable within one looping tail (or
// the counter is dead redundancy, structurally incapable of tripping).
func TestThresholdsHealthyCeilingAndTailReachability(t *testing.T) {
	healthy := CountBootMarkers(tailOf(healthyKernelCycle()+healthyKernelCycle()), launcherTail(1))
	if healthy.KernelPanics >= LoopThresholdKernelPanics {
		t.Errorf("panic threshold %d does not clear healthy count %d", LoopThresholdKernelPanics, healthy.KernelPanics)
	}
	if healthy.GuestReboots >= LoopThresholdGuestReboots {
		t.Errorf("reboot threshold %d does not clear healthy count %d", LoopThresholdGuestReboots, healthy.GuestReboots)
	}
	if healthy.UBootStarts >= LoopThresholdUBootStarts {
		t.Errorf("U-Boot threshold %d does not clear healthy count %d", LoopThresholdUBootStarts, healthy.UBootStarts)
	}

	var loop strings.Builder
	for range 20 { // more attempts than one tail holds; the bound must not save a dead threshold
		loop.WriteString(loopingKernelAttempt())
	}
	looping := CountBootMarkers(tailOf(loop.String()), launcherTail(20))
	if looping.KernelPanics < LoopThresholdKernelPanics {
		t.Errorf("panic threshold %d unreachable: one looping tail holds only %d", LoopThresholdKernelPanics, looping.KernelPanics)
	}
	if looping.GuestReboots < LoopThresholdGuestReboots {
		t.Errorf("reboot threshold %d unreachable: one looping tail holds only %d", LoopThresholdGuestReboots, looping.GuestReboots)
	}
	if looping.UBootStarts < LoopThresholdUBootStarts {
		t.Errorf("U-Boot threshold %d unreachable: one looping tail holds only %d", LoopThresholdUBootStarts, looping.UBootStarts)
	}
}

func TestCountBootMarkersHealthyIsBooted(t *testing.T) {
	obs := CountBootMarkers(tailOf(healthyKernelCycle()), launcherTail(1))
	if !obs.BootCompleted {
		t.Fatal("healthy cycle must report ordering-based completion")
	}
	if v := EvaluateBoot(nil, obs); v != BootVerdictBooted {
		t.Fatalf("healthy cycle evaluated %q, want %q", v, BootVerdictBooted)
	}
}

// TestStaleCompletionDoesNotMaskLoop pins the ordering rule: a completion
// marker from a previous successful boot must not mask panics that follow it.
func TestStaleCompletionDoesNotMaskLoop(t *testing.T) {
	var log strings.Builder
	log.WriteString(healthyKernelCycle())
	for range 6 {
		log.WriteString(loopingKernelAttempt())
	}
	obs := CountBootMarkers(tailOf(log.String()), launcherTail(6))
	if obs.BootCompleted {
		t.Fatal("completion preceding fresh attempts must not count as completed")
	}
	if v := EvaluateBoot(nil, obs); v != BootVerdictLooping {
		t.Fatalf("post-completion loop evaluated %q, want %q", v, BootVerdictLooping)
	}
}

// TestStaleFailureDoesNotSettleARetry pins the other direction of the
// ordering rule: a BOOT_FAILED left by a previous attempt must not terminally
// settle a device whose current in-window retry is booting fine.
func TestStaleFailureDoesNotSettleARetry(t *testing.T) {
	failed := "U-Boot SPL\nU-Boot 2023\n[  10.0] VIRTUAL_DEVICE_BOOT_FAILED\n"
	obs := CountBootMarkers([]byte(failed+healthyKernelCycle()), nil)
	if obs.BootFailed {
		t.Fatal("a failure marker preceding a fresh attempt must not count as failed")
	}
	if !obs.BootCompleted {
		t.Fatal("the fresh completed cycle must win")
	}

	// And the failure counts when it IS the last word.
	obs = CountBootMarkers([]byte(healthyKernelCycle()+failed), nil)
	if !obs.BootFailed || obs.BootCompleted {
		t.Fatalf("trailing failure must report failed, not completed: %+v", obs)
	}
	if v := EvaluateBoot(nil, obs); v != BootVerdictFailed {
		t.Fatalf("trailing failure evaluated %q, want %q", v, BootVerdictFailed)
	}
}

func TestCountBootMarkersCarriesLastPanicLine(t *testing.T) {
	obs := CountBootMarkers(tailOf(loopingKernelAttempt()), nil)
	if !strings.Contains(obs.LastPanic, "Kernel panic - not syncing: Attempted to kill init!") {
		t.Fatalf("LastPanic = %q, want the panic line", obs.LastPanic)
	}
	if len(obs.LastPanic) > lastPanicLineCap {
		t.Fatalf("LastPanic length %d exceeds cap %d", len(obs.LastPanic), lastPanicLineCap)
	}
}

func TestEvaluateBoot(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	prevAt := func(size int64, age time.Duration) *BootObservation {
		return &BootObservation{KernelLogBytes: size, ObservedAt: now.Add(-age)}
	}
	tests := []struct {
		name string
		prev *BootObservation
		cur  BootObservation
		want BootVerdict
	}{
		{
			name: "guest declared failure",
			cur:  BootObservation{UBootStarts: 2, BootFailed: true, ObservedAt: now},
			want: BootVerdictFailed,
		},
		{
			name: "panic counter over threshold",
			cur:  BootObservation{KernelPanics: LoopThresholdKernelPanics, ObservedAt: now},
			want: BootVerdictLooping,
		},
		{
			name: "reboot counter over threshold",
			cur:  BootObservation{GuestReboots: LoopThresholdGuestReboots, ObservedAt: now},
			want: BootVerdictLooping,
		},
		{
			name: "uboot counter over threshold",
			cur:  BootObservation{UBootStarts: LoopThresholdUBootStarts, ObservedAt: now},
			want: BootVerdictLooping,
		},
		{
			// The verbose-image loop: too few attempts per tail for the count
			// thresholds, but markers present and the log growing at loop
			// rate with no completion.
			name: "sustained growth with markers and no completion",
			prev: prevAt(1<<20, 2*time.Minute),
			cur:  BootObservation{UBootStarts: 4, KernelLogBytes: 1<<20 + 600*1024, ObservedAt: now},
			want: BootVerdictLooping,
		},
		{
			// A long-running device whose completion marker scrolled out of
			// the tail: chatty dmesg growth alone must never read as a loop.
			name: "growth without any attempt marker keeps waiting",
			prev: prevAt(1<<20, 2*time.Minute),
			cur:  BootObservation{KernelLogBytes: 1<<20 + 600*1024, ObservedAt: now},
			want: BootVerdictNoMarkers,
		},
		{
			// Growth must never settle when the guest completed: this is the
			// healthy path the positive control runs on.
			name: "growth with completion is booted",
			prev: prevAt(1<<20, 2*time.Minute),
			cur:  BootObservation{KernelLogBytes: 1<<20 + 600*1024, BootCompleted: true, UBootStarts: 3, ObservedAt: now},
			want: BootVerdictBooted,
		},
		{
			name: "growth below loop rate keeps waiting",
			prev: prevAt(1<<20, 2*time.Minute),
			cur:  BootObservation{KernelLogBytes: 1<<20 + 100*1024, UBootStarts: 2, ObservedAt: now},
			want: BootVerdictProgress,
		},
		{
			name: "interval too short for a rate",
			prev: prevAt(1<<20, 30*time.Second),
			cur:  BootObservation{KernelLogBytes: 1<<20 + 600*1024, UBootStarts: 2, ObservedAt: now},
			want: BootVerdictProgress,
		},
		{
			name: "below threshold advancing",
			prev: &BootObservation{UBootStarts: 2, KernelLogBytes: 100, ObservedAt: now.Add(-time.Minute)},
			cur:  BootObservation{UBootStarts: 3, KernelLogBytes: 100, ObservedAt: now},
			want: BootVerdictProgress,
		},
		{
			name: "below threshold not advancing",
			prev: &BootObservation{UBootStarts: 2, KernelLogBytes: 100, ObservedAt: now.Add(-time.Minute)},
			cur:  BootObservation{UBootStarts: 2, KernelLogBytes: 100, ObservedAt: now},
			want: BootVerdictStalled,
		},
		{
			name: "no guest markers at all",
			cur:  BootObservation{ObservedAt: now},
			want: BootVerdictNoMarkers,
		},
		{
			name: "first enriched sample with markers",
			cur:  BootObservation{UBootStarts: 3, ObservedAt: now},
			want: BootVerdictProgress,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EvaluateBoot(tt.prev, tt.cur); got != tt.want {
				t.Fatalf("EvaluateBoot() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoopEvidenceNamesTheCountsAndPanic(t *testing.T) {
	obs := BootObservation{
		KernelPanics: 11, GuestReboots: 435, UBootStarts: 22,
		LastPanic: "Kernel panic - not syncing: bad XCR0",
	}
	msg := obs.LoopEvidence()
	for _, want := range []string{"11 kernel panics", "435 guest reboots", "22 bootloader starts", "bad XCR0"} {
		if !strings.Contains(msg, want) {
			t.Errorf("LoopEvidence() = %q, missing %q", msg, want)
		}
	}
}

// The create window scales per instance PAST the first, so a single-instance
// create is unchanged and a group gets a window proportional to what it asked
// for. Nonsense counts fall back to the base rather than underflowing it. A
// negative multiplier would produce a ceiling shorter than the boot-watch dwell
// it has to outlive.
func TestCreateProgressWindow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		instances int
		want      time.Duration
	}{
		{-1, TransientProgressWindow},
		{0, TransientProgressWindow},
		{1, TransientProgressWindow},
		{2, TransientProgressWindow + PerInstanceProgressWindow},
		{8, TransientProgressWindow + 7*PerInstanceProgressWindow},
	} {
		got := CreateProgressWindow(tc.instances)
		if got != tc.want {
			t.Errorf("CreateProgressWindow(%d) = %v, want %v", tc.instances, got, tc.want)
		}
	}
}

// The ceiling must never fall below the boot-watch enrichment dwell: the dwell
// is what makes a resumed wait evaluate immediately instead of serving a second
// full window before its first verdict.
func TestCreateProgressWindowOutlivesTheEnrichmentDwell(t *testing.T) {
	t.Parallel()
	for n := 1; n <= 8; n++ {
		if got := CreateProgressWindow(n); got < TransientProgressWindow {
			t.Errorf("CreateProgressWindow(%d) = %v, below the dwell %v", n, got, TransientProgressWindow)
		}
	}
}

// launcherTimeoutLine is run_cvd's give-up line as captured live, 2026-10-04,
// from a Core instance whose crosvm hung before its kernel started.
const launcherTimeoutLine = "run_cvd(2977)  E 10-04 12:11:51  2977  2982 boot_state_machine.cc:577] " +
	"TimeoutThreadLoop: Did not receive boot completion event after 10m"

// A launcher that timed out the boot fails the sample even with an empty
// kernel.log, which is the shape of a VM that never reached its kernel. The
// timeout line is the evidence both texts carry, and it must not read as a
// loop or a guest's own declaration.
func TestCountBootMarkersLauncherTimeout(t *testing.T) {
	launcher := []byte("Instance restarter successfully started\n" + launcherTimeoutLine +
		"\nprocess_monitor.cc:155] Stopping monitored subprocesses\n")
	obs := CountBootMarkers(nil, launcher)
	if !obs.BootFailed {
		t.Fatalf("launcher timeout must report failed: %+v", obs)
	}
	if obs.LastPanic != launcherTimeoutLine {
		t.Fatalf("LastPanic = %q, want the launcher's timeout line", obs.LastPanic)
	}
	if !obs.LauncherGaveUp() {
		t.Fatal("LauncherGaveUp() = false for a launcher timeout")
	}
	obs.Verdict = EvaluateBoot(nil, obs)
	if obs.Verdict != BootVerdictFailed {
		t.Fatalf("EvaluateBoot() = %q, want %q", obs.Verdict, BootVerdictFailed)
	}

	evidence := obs.LoopEvidence()
	if !strings.Contains(evidence, "launcher gave up") || !strings.Contains(evidence, launcherTimeoutLine) {
		t.Errorf("LoopEvidence() = %q, want the cause and the timeout line", evidence)
	}
	if strings.Contains(evidence, "boot loop") {
		t.Errorf("LoopEvidence() = %q reads as a loop", evidence)
	}
	if line := obs.ProgressLine(); !strings.Contains(line, "launcher gave up") {
		t.Errorf("ProgressLine() = %q, want the launcher cause", line)
	}

	// The timeout wins over a loop the kernel tail also shows: the launcher
	// has stopped, so the loop is over.
	looping := CountBootMarkers(tailOf(strings.Repeat(loopingKernelAttempt(), LoopThresholdKernelPanics)), launcher)
	if v := EvaluateBoot(nil, looping); v != BootVerdictFailed || !looping.LauncherGaveUp() {
		t.Errorf("timeout beside a loop evaluated %q (gave up %v), want %q", v, looping.LauncherGaveUp(), BootVerdictFailed)
	}

	// A completed boot wins, since the launcher only times out a boot that
	// did not complete.
	completed := CountBootMarkers(tailOf(healthyKernelCycle()), launcher)
	if completed.BootFailed || completed.LauncherGaveUp() {
		t.Errorf("a completed boot must not read as a launcher timeout: %+v", completed)
	}
}

// A guest's own failure marker keeps its own wording and is not mistaken for
// the launcher giving up.
func TestGuestDeclaredFailureEvidence(t *testing.T) {
	obs := CountBootMarkers([]byte("U-Boot SPL\nU-Boot 2023\n[  10.0] VIRTUAL_DEVICE_BOOT_FAILED\n"), launcherTail(0))
	obs.Verdict = EvaluateBoot(nil, obs)
	if obs.Verdict != BootVerdictFailed || obs.LauncherGaveUp() {
		t.Fatalf("verdict %q, gave up %v; want guest-declared failure", obs.Verdict, obs.LauncherGaveUp())
	}
	if msg := obs.LoopEvidence(); !strings.Contains(msg, "guest declared boot failure") {
		t.Errorf("LoopEvidence() = %q, want the guest's declaration", msg)
	}
}

// launcherStartLine is the per-launch line run_cvd prints, as captured live
// 2026-10-04.
const launcherStartLine = "run_cvd(2970)  I 10-04 12:01:51  2970  2970 reporting.cpp:44] cvd-1:   " +
	"Launcher Build ID: 363bfbaefb50fdd4334243bfad1623e05c0e991d"

// run_cvd appends to launcher.log across launches, so a timeout from an
// earlier launch must not fail the launch that followed it. Only a timeout
// after the last start line, or one whose start line has left the tail,
// belongs to the current launch.
func TestCountBootMarkersLauncherTimeoutOrdering(t *testing.T) {
	tests := []struct {
		name     string
		launcher string
		want     bool
	}{
		{
			name:     "timeout before a later start line",
			launcher: launcherStartLine + "\n" + launcherTimeoutLine + "\n" + launcherStartLine + "\n",
			want:     false,
		},
		{
			name:     "timeout after the last start line",
			launcher: launcherStartLine + "\n" + launcherTimeoutLine + "\n" + launcherStartLine + "\n" + launcherTimeoutLine + "\n",
			want:     true,
		},
		{
			name:     "timeout with no start line in the tail",
			launcher: "Instance restarter successfully started\n" + launcherTimeoutLine + "\n",
			want:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := CountBootMarkers(nil, []byte(tt.launcher))
			if obs.BootFailed != tt.want || obs.LauncherGaveUp() != tt.want {
				t.Fatalf("BootFailed = %v, LauncherGaveUp() = %v, want %v", obs.BootFailed, obs.LauncherGaveUp(), tt.want)
			}
			if v := EvaluateBoot(nil, obs); (v == BootVerdictFailed) != tt.want {
				t.Fatalf("EvaluateBoot() = %q, want failed %v", v, tt.want)
			}
		})
	}
}
