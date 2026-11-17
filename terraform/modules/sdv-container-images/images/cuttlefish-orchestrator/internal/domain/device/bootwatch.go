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
	"bytes"
	"fmt"
	"strings"
	"time"
)

// Boot-progress markers as they appear in a CVD's own log files. Like the
// CVDState* strings above, this is the SINGLE source of truth for the
// vocabulary: the create-wait sampler (features/device/cvd) and the health
// plane (jobs/devicecontroller) both count these, so the parser lives here on
// the domain side of the firewall.
//
// The three attempt markers have independent origins on purpose (a Linux
// kernel string, a secure_env log line, and a Cuttlefish console pattern), so
// one upstream rename degrades the observation instead of blinding it. The
// kernel-panic string is the most stable, being no part of Cuttlefish's
// vocabulary; log growth (BootObservation.KernelLogBytes) is the fourth
// signal and uses no vocabulary at all.
const (
	// bootMarkerKernelPanic is Linux's own panic banner, echoed into
	// kernel.log by the kernel log monitor. Counted per boot attempt that
	// died in the kernel.
	bootMarkerKernelPanic = "Kernel panic - not syncing"

	// bootMarkerGuestReboot is secure_env noticing the guest restarted
	// (upstream: "secure_env detected guest reboot, restarting.", a VLOG(0)
	// in both secure_env mains). It lands in launcher.log. Matched on the
	// stable middle rather than the full line.
	bootMarkerGuestReboot = "detected guest reboot"

	// bootMarkerUBoot is the bootloader banner pattern upstream's
	// kernel_log_server matches ("U-Boot ", trailing space included). A
	// healthy boot emits it three times (SPL, U-Boot proper, handoff); a
	// panic-before-handoff loop emits ~2 per attempt.
	bootMarkerUBoot = "U-Boot "

	// bootMarkerCompleted / bootMarkerFailed are Android userspace's own
	// emissions (upstream config_constants.h), visible in kernel.log. A guest
	// that panics before init never emits either. That is why counting
	// VIRTUAL_DEVICE_BOOT_STARTED would have read 0 on the incident this
	// detection exists for.
	bootMarkerCompleted = "VIRTUAL_DEVICE_BOOT_COMPLETED"
	bootMarkerFailed    = "VIRTUAL_DEVICE_BOOT_FAILED"

	// bootMarkerLauncherTimeout is run_cvd's boot state machine giving up
	// (upstream boot_state_machine.cc, TimeoutThreadLoop: "Did not receive
	// boot completion event after 10m"). It lands in launcher.log, and the
	// launcher stops its monitored subprocesses right after it, while the
	// fleet listing keeps reporting the group Starting. A guest whose VM
	// never reached its kernel leaves no other marker at all.
	bootMarkerLauncherTimeout = "Did not receive boot completion event"

	// bootMarkerLauncherStart is the line every run_cvd launch prints once
	// per instance (upstream run_cvd reporting.cpp, "Launcher Build ID:").
	// run_cvd appends to an existing launcher.log (main.cc ConfigureLogs
	// starts a fresh file only when the old one is empty), so a timeout from
	// an earlier launch survives a stop and start. Only a timeout after the
	// last start line belongs to the current launch.
	bootMarkerLauncherStart = "Launcher Build ID:"
)

// BootLogTailBytes bounds one log-tail fetch. The thresholds below are
// calibrated against this window: at ~24 KB per looping attempt it holds ~11
// attempts, while a healthy log's tail holds at most one boot cycle. Changing
// it re-derives every threshold.
const BootLogTailBytes = 256 * 1024

// Log file names within a CVD's logs directory (upstream log_names.h), homed
// with the marker vocabulary because both feeders fetch the same two files:
// the create sampler in features and the device controller in jobs. kernel.log
// carries the panic, bootloader and userspace-outcome markers; launcher.log
// carries secure_env's reboot detections.
const (
	KernelLogFile   = "kernel.log"
	LauncherLogFile = "launcher.log"
)

// Loop thresholds, each within one tail. Dual-checked by tests against the
// measured 2026-08-02 baselines: a threshold must clear its healthy worst
// case with margin (panic 0, guest reboot 1, U-Boot 6 across two cycles) AND
// be reachable from what a looping tail actually holds (~11 attempts × 1
// panic, ~1 reboot, ~2 U-Boot lines each).
const (
	LoopThresholdKernelPanics = 3
	LoopThresholdGuestReboots = 5
	LoopThresholdUBootStarts  = 12
)

// loopGrowthBytesPerMin is the corroborating growth signal: a looping guest
// sustains ~490 KB/min of kernel.log while a healthy booted guest is
// quiescent. Growth never settles alone, because a healthy BOOT writes
// 287-430 KB/min. The evaluator admits it only paired with the absence of a
// completion marker, and the feeders only enrich past the boot window
// (TransientProgressWindow), which is the minimum-age guard.
const loopGrowthBytesPerMin = 128 * 1024

// TransientProgressWindow is how long a device may sit in a transient state
// before the platform starts demanding a progress witness. Three mechanisms
// share it so they cannot drift apart: the create-wait sampler's enrichment
// dwell, the device controller's stale-transitional suspicion, and the Host
// Orchestrator client's default operation-wait ceiling.
//
// The create ceiling is now the BASE of CreateProgressWindow, no longer equal
// to it, and that relationship is the correct one. What the equality
// bought was a resumed wait re-entering with the dwell already served, so its
// first tick evaluates instead of serving a second window. Any ceiling at or
// above the dwell buys that. What a wider ceiling adds is that a group's
// create reaches its first log-derived verdict INSIDE the wait, instead of
// only after the redelivery the equal ceiling forced.
const TransientProgressWindow = 10 * time.Minute

// PerInstanceProgressWindow is what each instance past the first adds to a
// create's wait ceiling.
//
// It is a budget sized for the SLOWEST guest the platform serves, which may
// be slower than any guest measurable here.
// Measured on the AOSP phone build, a create is linear in instance count at
// roughly 44 seconds each (176s at N=1, 266s at N=3, 481s at N=8). An
// automotive guest boots several times slower than a phone, and a group of them
// is the workload groups exist for. Sizing this on phone numbers would set the
// ceiling tightest for exactly the case it is there to protect.
const PerInstanceProgressWindow = 2 * time.Minute

// CreateProgressWindow is how long a create of n instances may run before its
// wait is abandoned and the task resumes it.
//
// Scaled rather than raised flat, because a flat ceiling wide enough for eight
// instances would give a single-instance create the same eight-instance license
// to look alive while stuck. The base stays TransientProgressWindow, so N=1 is
// unchanged.
//
// Widening this does NOT widen the window in which a stuck create goes
// unnoticed, which is what an unscaled raise would have done. The boot watch's
// enrichment dwell stays at TransientProgressWindow, so a group's create now
// gets its first log-derived verdict INSIDE the wait, and every minute after,
// instead of only on the redelivery that the old, equal ceiling forced. The
// resume-entry property survives for the same reason: a wait that expires at or
// after the dwell re-enters with the dwell already served.
func CreateProgressWindow(instances int) time.Duration {
	return TransientProgressWindow + time.Duration(max(0, instances-1))*PerInstanceProgressWindow
}

// BootVerdict is the decision the boot observation supports. Stored on the
// observation so the derivation stays a pure read of the row: the feeder that
// took the sample had both the previous and current observation in hand and
// is the only place the rule can be evaluated correctly.
type BootVerdict string

const (
	// BootVerdictBooted reports the last completion marker following the last
	// attempt marker. The rule is ordering-based. A tail can still hold a
	// completion from a previous successful boot while fresh panics
	// accumulate, and "contains COMPLETED" would mask that loop.
	BootVerdictBooted BootVerdict = "booted"
	// BootVerdictFailed reports the guest itself declaring boot failure, or
	// the launcher giving up on a boot that never completed. Terminal.
	BootVerdictFailed BootVerdict = "boot_failed"
	// BootVerdictLooping reports an attempt counter over its threshold, or
	// loop-rate log growth beside attempt markers with no completion.
	// Terminal, because re-running the same build re-loops.
	BootVerdictLooping BootVerdict = "looping"
	// BootVerdictProgress reports markers below threshold and advancing.
	BootVerdictProgress BootVerdict = "progress"
	// BootVerdictStalled reports markers below threshold and not advancing.
	BootVerdictStalled BootVerdict = "stalled"
	// BootVerdictNoMarkers reports no guest markers at all. The artifact
	// fetch or assembly is still running and the guest has not started. This
	// is what separates "fetch is slow" from "guest is broken".
	BootVerdictNoMarkers BootVerdict = "no_markers"
)

// Terminal reports a verdict that settles the device to error. It is the
// single place the create-wait sampler and DerivePhase share the decision.
// Both verdicts are the log-derived equivalent of an explicit CVD failure,
// outside the zombie recreate budget because re-running the same build
// re-loops.
func (v BootVerdict) Terminal() bool {
	return v == BootVerdictLooping || v == BootVerdictFailed
}

// BootTransition is one step of the create wait's load-status trail: the
// group's listing status and the wall-clock it was first observed there. The
// trail (absent → a transient state → Running) is what splits the multi-minute
// HO wait into its artifact-fetch and guest-boot legs after the fact. Absent
// spans the fetch, and the first transient state marks the launcher taking
// over.
// Entries are appended only on an observed change, so the trail is bounded by
// the wait's own tick budget.
type BootTransition struct {
	Status string    `json:"status"`
	At     time.Time `json:"at"`
}

// BootObservation is the widened boot observation beside CVDStatus: a fact
// about the world sampled from the CVD's own logs, never an owned status.
// It is the same kind of value as CVDStatus, and it is written with CVDStatus
// as one sample, because a fresh enum against a stale count lets the
// derivation settle wrongly.
//
// It is self-contained (counters, size, verdict, timestamps) so state lives on
// the row. The mechanism survives leader changes, deploys and task
// redelivery, and the previous sample needed by "markers advancing" and the
// growth rate is the row's copy, not memory.
type BootObservation struct {
	// KernelPanics counts Linux panic banners in the kernel.log tail.
	KernelPanics int `json:"kernel_panics,omitzero"`
	// GuestReboots counts secure_env reboot detections in the launcher.log tail.
	GuestReboots int `json:"guest_reboots,omitzero"`
	// UBootStarts counts bootloader banners in the kernel.log tail.
	UBootStarts int `json:"uboot_starts,omitzero"`
	// BootCompleted reports the ordering-based completion: the last
	// completion marker follows the last attempt marker in the kernel tail.
	BootCompleted bool `json:"boot_completed,omitzero"`
	// BootFailed reports the guest's own terminal boot-failure marker, or the
	// launcher's boot timeout.
	BootFailed bool `json:"boot_failed,omitzero"`
	// KernelLogBytes is kernel.log's total size at ObservedAt, the
	// vocabulary-free growth signal, compared against the previous sample.
	KernelLogBytes int64 `json:"kernel_log_bytes,omitzero"`
	// LastPanic is the last panic line seen, the evidence a loop verdict
	// hands the operator (the count without the panic is not actionable).
	// After a launcher boot timeout it holds the launcher's timeout line
	// instead, which is that verdict's evidence.
	LastPanic string `json:"last_panic,omitempty"`
	// Verdict is the decision this sample supports, evaluated by the feeder
	// against the previous sample. device.DerivePhase consumes it.
	Verdict BootVerdict `json:"verdict,omitempty"`
	// Transitions is the load-status trail, one entry per observed change.
	// Persisted whole on every write. The sampler owns the accumulating
	// copy, so no read-modify-write is ever needed.
	Transitions []BootTransition `json:"transitions,omitempty"`
	// TransientSince marks entry into a transient status, written once by the
	// first sample and cleared on leaving. It exists because updated_at
	// cannot serve as a dwell clock: every observation write would reset it.
	TransientSince *time.Time `json:"transient_since,omitempty"`
	// ObservedAt stamps this sample; with KernelLogBytes it yields the
	// growth rate.
	ObservedAt time.Time `json:"observed_at,omitzero"`
}

// lastPanicLineCap bounds the panic line carried as evidence so it cannot
// dominate the error_message column's 1000-rune budget.
const lastPanicLineCap = 300

// CountBootMarkers parses bounded tails of a CVD's kernel.log and
// launcher.log into a marker sample. Pure bytes→counts; either tail may be
// nil (a failed fetch), which simply contributes nothing.
func CountBootMarkers(kernelTail, launcherTail []byte) BootObservation {
	obs := BootObservation{
		KernelPanics: bytes.Count(kernelTail, []byte(bootMarkerKernelPanic)),
		GuestReboots: bytes.Count(launcherTail, []byte(bootMarkerGuestReboot)),
		UBootStarts:  bytes.Count(kernelTail, []byte(bootMarkerUBoot)),
	}
	// Both outcome markers are ordering-based: a tail can hold a completion
	// (or failure) from a PREVIOUS attempt while a fresh one runs, so only
	// the outcome that follows every later event may count. Presence-based
	// completion masks a loop; presence-based failure terminally settles a
	// device whose current retry is booting fine.
	lastPanic := bytes.LastIndex(kernelTail, []byte(bootMarkerKernelPanic))
	lastAttempt := max(lastPanic, bytes.LastIndex(kernelTail, []byte(bootMarkerUBoot)))
	lastCompleted := bytes.LastIndex(kernelTail, []byte(bootMarkerCompleted))
	lastFailed := bytes.LastIndex(kernelTail, []byte(bootMarkerFailed))
	obs.BootCompleted = lastCompleted >= 0 && lastCompleted > max(lastAttempt, lastFailed)
	obs.BootFailed = lastFailed >= 0 && lastFailed > max(lastAttempt, lastCompleted)
	if lastPanic >= 0 {
		obs.LastPanic = lineAt(kernelTail, lastPanic)
	}
	// The timeout sits in the other file, so it cannot be ordered against the
	// kernel markers. A completion still wins, because the timeout is only
	// ever written for a boot that did not complete. Within launcher.log it
	// counts only after the last launch's start line. A tail with no start
	// line has scrolled any older timeout out with it.
	timeout := bytes.LastIndex(launcherTail, []byte(bootMarkerLauncherTimeout))
	if timeout > bytes.LastIndex(launcherTail, []byte(bootMarkerLauncherStart)) && !obs.BootCompleted {
		obs.BootFailed = true
		obs.LastPanic = lineAt(launcherTail, timeout)
	}
	return obs
}

// LauncherGaveUp reports a sample whose failure is the launcher's boot
// timeout. The launcher has stopped its own subprocesses by then, so the
// group can be removed without leaking them, even though the fleet listing
// still says Starting.
func (o *BootObservation) LauncherGaveUp() bool {
	return o.BootFailed && strings.Contains(o.LastPanic, bootMarkerLauncherTimeout)
}

// lineAt extracts the (capped) log line containing offset idx.
func lineAt(data []byte, idx int) string {
	start := bytes.LastIndexByte(data[:idx], '\n') + 1
	end := bytes.IndexByte(data[idx:], '\n')
	if end < 0 {
		end = len(data)
	} else {
		end += idx
	}
	line := data[start:end]
	if len(line) > lastPanicLineCap {
		line = line[:lastPanicLineCap]
	}
	return string(line)
}

// EvaluateBoot decides what the fresh sample supports, given the previous
// observation from the row (nil for the first enriched sample). The verdict
// order mirrors the decision table: a completion that follows the last
// attempt wins (the guest recovered, whatever the counters say about the
// past), then the guest's own terminal marker, then the loop thresholds, then
// the progress/stall split.
//
// Growth is corroborating only: it fires only with no completion marker, a
// previous sample to rate against, and an interval long enough for the rate
// to mean something. The feeders enforce the minimum-age half of the pairing
// by enriching only past TransientProgressWindow (create) or the boot-settle
// window (health).
func EvaluateBoot(prev *BootObservation, cur BootObservation) BootVerdict {
	if cur.BootCompleted {
		return BootVerdictBooted
	}
	if cur.BootFailed {
		return BootVerdictFailed
	}
	if cur.KernelPanics >= LoopThresholdKernelPanics ||
		cur.GuestReboots >= LoopThresholdGuestReboots ||
		cur.UBootStarts >= LoopThresholdUBootStarts {
		return BootVerdictLooping
	}
	if sustainedLoopGrowth(prev, cur) {
		return BootVerdictLooping
	}
	if !cur.hasMarkers() {
		return BootVerdictNoMarkers
	}
	if prev == nil || cur.advancedFrom(prev) {
		return BootVerdictProgress
	}
	return BootVerdictStalled
}

// sustainedLoopGrowth reports loop-rate kernel.log growth between two
// samples. It requires a real interval, since a single sample has no rate,
// and it tolerates shrinkage (rotation) as no growth. It also requires at
// least one attempt marker in the tail: on a long-running device the
// completion marker eventually scrolls out, and growth alone would then read a
// chatty healthy kernel as a loop. With the marker requirement, growth catches
// the loop a verbose image hides from the count thresholds, where each tail
// holds few attempts and each attempt is large. It is never a stand-alone
// signal.
func sustainedLoopGrowth(prev *BootObservation, cur BootObservation) bool {
	if cur.KernelPanics == 0 && cur.UBootStarts == 0 && cur.GuestReboots == 0 {
		return false
	}
	if prev == nil || prev.ObservedAt.IsZero() || prev.KernelLogBytes <= 0 {
		return false
	}
	interval := cur.ObservedAt.Sub(prev.ObservedAt)
	if interval < time.Minute {
		return false
	}
	grown := cur.KernelLogBytes - prev.KernelLogBytes
	if grown <= 0 {
		return false
	}
	return float64(grown)/interval.Minutes() >= loopGrowthBytesPerMin
}

func (o *BootObservation) hasMarkers() bool {
	return o.KernelPanics > 0 || o.GuestReboots > 0 || o.UBootStarts > 0 ||
		o.BootCompleted || o.BootFailed
}

func (o *BootObservation) advancedFrom(prev *BootObservation) bool {
	return o.KernelPanics > prev.KernelPanics ||
		o.GuestReboots > prev.GuestReboots ||
		o.UBootStarts > prev.UBootStarts ||
		o.KernelLogBytes > prev.KernelLogBytes
}

// LoopEvidence renders the terminal evidence line a loop verdict promotes
// into ErrorMessage: the counts and the last panic, which is what names the
// bug. Callers append the row's expiry. The device TTL, not the host TTL, is
// the window in which the group's logs still exist.
//
// A failed verdict carries no loop, so it renders its own cause instead of
// counts that would read as zero attempts.
func (o *BootObservation) LoopEvidence() string {
	if o.Verdict == BootVerdictFailed {
		if o.LauncherGaveUp() {
			return "launcher gave up waiting for boot completion: " + o.LastPanic
		}
		msg := "guest declared boot failure"
		if o.LastPanic != "" {
			msg += "; last panic: " + o.LastPanic
		}
		return msg
	}
	msg := fmt.Sprintf("guest boot loop: %d kernel panics, %d guest reboots, %d bootloader starts in the last %dKiB of logs",
		o.KernelPanics, o.GuestReboots, o.UBootStarts, BootLogTailBytes/1024)
	if o.LastPanic != "" {
		msg += "; last panic: " + o.LastPanic
	}
	return msg
}

// BootLoopEvidence renders the terminal error message both settle paths
// promote: the observation's evidence plus the row's expiry, because the
// DEVICE TTL (not the host's) is when the reaper destroys the group and its
// logs, and the operator reading the message needs that clock.
func (d *Device) BootLoopEvidence() string {
	if d.BootObservation == nil {
		return ""
	}
	msg := d.BootObservation.LoopEvidence()
	if exp := d.Lifecycle.ExpiresAt; exp != nil {
		msg += "; the group and its logs are reaped with the device at " + exp.UTC().Format(time.RFC3339)
	}
	return msg
}

// ProgressLine renders the live observation for the console: what the device
// is doing right now, server-derived and refresh-stable.
func (o *BootObservation) ProgressLine() string {
	switch o.Verdict {
	case BootVerdictNoMarkers:
		return "no guest boot activity yet (artifacts still fetching or assembling)"
	case BootVerdictProgress, BootVerdictStalled:
		attempts := max(o.KernelPanics, o.GuestReboots)
		if attempts > 0 {
			return fmt.Sprintf("booting; %d failed attempts observed", attempts)
		}
		return "booting"
	case BootVerdictLooping:
		line := fmt.Sprintf("boot loop: rebooted %d×", max(o.GuestReboots, o.KernelPanics))
		if o.LastPanic != "" {
			line += ", last: " + o.LastPanic
		}
		return line
	case BootVerdictFailed:
		if o.LauncherGaveUp() {
			return "launcher gave up waiting for boot completion"
		}
		return "guest declared boot failure"
	case BootVerdictBooted:
		return "booted"
	default:
		return ""
	}
}
