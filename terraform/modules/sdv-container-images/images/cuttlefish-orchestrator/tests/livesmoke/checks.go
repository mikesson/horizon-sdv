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
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	devicedomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// pngMagic frames every PNG. The screenshot endpoint once answered 200 with an
// empty body for months; "some bytes arrived" is not the assertion that would
// have caught it.
var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// cmdDevice is cfctl's device command group.
const cmdDevice = "device"

// cmdRecord is cfctl's screen-recording subcommand group.
const cmdRecord = "record"

// cmdSnapshot is cfctl's snapshot subcommand group.
const cmdSnapshot = "snapshot"

// checkCreate provisions the device the rest of the run uses. Its failure is
// fatal to the scenario: everything downstream needs a running device.
func (d *driver) checkCreate() error {
	name := fmt.Sprintf("%s%d", d.cfg.NamePrefix, time.Now().Unix()%100000)
	start := time.Now()

	// swiftshader is declared rather than left to auto because the snapshot arm
	// needs it: a take is admitted in no other mode, and auto resolves on the
	// host, so leaving it open made this run's coverage depend on which host
	// placement happened to pick. It used to pick a GPU host sometimes, and
	// there the take destroyed the device and every verb after it failed.
	out, err := d.cfctl(d.cfg.CreateTimeout, "device", "create",
		"--name="+name, "--interaction="+d.cfg.Interaction,
		"--gpu-mode=swiftshader", "--wait", "--json")
	if err != nil {
		d.f.fail("create", time.Since(start), err.Error())
		return fmt.Errorf("creating device: %w", err)
	}
	if err := json.Unmarshal([]byte(out), &d.dev); err != nil {
		d.f.fail("create", time.Since(start), "decoding device JSON: "+err.Error())
		return err
	}
	if d.dev.ID == "" || d.dev.Status != "running" {
		d.f.fail("create", time.Since(start), fmt.Sprintf("device %q status %q, want running", d.dev.ID, d.dev.Status))
		return errors.New("device did not reach running")
	}
	d.created = true
	d.f.pass("create", time.Since(start), fmt.Sprintf("%s (%s) on host %s", d.dev.Name, d.dev.ID, d.dev.HostID))

	if err := d.loadHost(); err != nil {
		slog.Warn("host detail unavailable; VM-level checks will skip", slog.Any("error", err))
	}
	return nil
}

func (d *driver) loadHost() error {
	out, err := d.cfctl(time.Minute, "host", "get", d.dev.HostID, "--json")
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(out), &d.host)
}

// checkScreenshot pins the whole capture contract: the host produces an image,
// the server waits for the operation that produces it, and the bytes that
// arrive are a real PNG of the declared type.
func (d *driver) checkScreenshot() {
	start := time.Now()
	path, err := d.writeTemp("livesmoke-*.png")
	if err != nil {
		d.f.fail("screenshot", time.Since(start), err.Error())
		return
	}
	defer os.Remove(path)

	out, err := d.cfctl(2*time.Minute, "device", "screenshot", d.dev.ID, "--output="+path, "--json")
	if err != nil {
		d.f.fail("screenshot", time.Since(start), err.Error())
		return
	}
	var shot struct {
		File     string `json:"file"`
		MimeType string `json:"mime_type"`
		Bytes    int    `json:"bytes"`
	}
	if err := json.Unmarshal([]byte(out), &shot); err != nil {
		d.f.fail("screenshot", time.Since(start), "decoding screenshot JSON: "+err.Error())
		return
	}

	img, err := os.ReadFile(path)
	switch {
	case err != nil:
		d.f.fail("screenshot", time.Since(start), "reading capture: "+err.Error())
	case len(img) == 0:
		d.f.fail("screenshot", time.Since(start), "capture is empty (the host answered, the image did not)")
	case !bytes.HasPrefix(img, pngMagic):
		d.f.fail("screenshot", time.Since(start), "capture is not PNG-framed")
	case shot.MimeType != "image/png":
		d.f.fail("screenshot", time.Since(start), fmt.Sprintf("mime type %q, want image/png", shot.MimeType))
	default:
		d.f.pass("screenshot", time.Since(start), fmt.Sprintf("%d bytes, %s", len(img), shot.MimeType))
	}
}

// bugreportMembers are entries a real capture always carries. Matched by suffix
// so the check survives instance renumbering, and chosen as the files a human
// actually opens during triage. Their presence is what makes the archive worth
// keeping, so their absence should fail even when the ZIP itself is well formed.
var bugreportMembers = []string{"/logs/kernel.log", "/logs/launcher.log", "fetch.log"}

// checkBugreport pins the whole artifact pipeline: the host captures, the server
// streams it into the bucket, and what comes back out is a readable archive with
// the triage logs in it.
//
// Opening every entry rather than sniffing the first bytes is the idiom the
// upstream cuttlefish e2e suite uses for the same artifact. This path went
// unasserted for its whole life, and the cost was real: a capture that was
// silently a sign-in page got recorded as a passing "927 KB ZIP" twice,
// because the only thing ever checked was the file's size.
func (d *driver) checkBugreport() {
	start := time.Now()
	path, err := d.writeTemp("livesmoke-*.zip")
	if err != nil {
		d.f.fail("bugreport", time.Since(start), err.Error())
		return
	}
	defer os.Remove(path)

	out, err := d.cfctl(5*time.Minute, cmdDevice, "bugreport", d.dev.ID, "--output="+path, "--json")
	if err != nil {
		d.f.fail("bugreport", time.Since(start), err.Error())
		return
	}
	var got struct {
		File  string `json:"file"`
		GSURI string `json:"gs_uri"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		d.f.fail("bugreport", time.Since(start), "decoding bugreport JSON: "+err.Error())
		return
	}
	if !strings.HasPrefix(got.GSURI, "gs://") {
		d.f.fail("bugreport", time.Since(start), fmt.Sprintf("gs_uri = %q, want a gs:// location", got.GSURI))
		return
	}

	names, err := readZIP(path)
	if err != nil {
		d.f.fail("bugreport", time.Since(start), "stored artifact is not a readable archive: "+err.Error())
		return
	}
	var missing []string
	for _, want := range bugreportMembers {
		if !slices.ContainsFunc(names, func(n string) bool { return strings.HasSuffix(n, want) }) {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		d.f.fail("bugreport", time.Since(start),
			fmt.Sprintf("archive opened but %d entries carry none of %v", len(names), missing))
		return
	}
	d.f.pass("bugreport", time.Since(start), fmt.Sprintf("%d entries, %s", len(names), got.GSURI))
}

// webmMagic is the EBML header every Matroska/WebM stream carries. Screen
// recordings are WebM, confirmed against a stored artifact rather than
// inferred from the file suffix.
var webmMagic = []byte{0x1A, 0x45, 0xDF, 0xA3}

// Matroska element IDs. A usable recording carries a Tracks entry describing
// the video stream and at least one Cluster holding frames; a byte-count floor
// cannot tell those apart from a large header, which is why this reads the
// container instead of its size.
var (
	webmTracks  = []byte{0x16, 0x54, 0xAE, 0x6B}
	webmCluster = []byte{0x1F, 0x43, 0xB6, 0x75}
)

// recordingSeconds is how long the arm records for. It sets the recording's
// length; nothing synchronizes on it.
const recordingSeconds = 5

// driveDisplayFrames makes the display emit frames for the recording window.
//
// Without this the arm records nothing, and nothing is broken. The recorder
// is a sink on the WebRTC frame stream, which only carries frames when the guest
// composes a new one or when the host's 20ms frame repeater is running. That
// repeater is gated on an attached display client, and this suite opens no
// WebRTC session against an idle device, so a recording here captured zero
// frames and mkvmuxer, which writes the Segment, Tracks and Clusters lazily on
// the first frame, wrote none of them. Two such artifacts are archived, from
// runs two days apart, byte-identical at 110 bytes.
//
// Taking screenshots is what fixes it. The host's screenshot path attaches a
// display client for the duration of the capture, which is what starts the
// repeater. The same host and session that produced a 110-byte recording also
// produced a 121 KB one, so the pipeline works and only ever needed something
// to look at it.
func (d *driver) driveDisplayFrames() {
	deadline := time.Now().Add(recordingSeconds * time.Second)
	for time.Now().Before(deadline) {
		path, err := d.writeTemp("livesmoke-frame-*.png")
		if err != nil {
			continue
		}
		// Best-effort: a failed capture costs this arm nothing but frames, and
		// the sibling screenshot arm already owns that assertion.
		_, _ = d.cfctl(time.Minute, cmdDevice, "screenshot", d.dev.ID, "--output="+path)
		_ = os.Remove(path)
	}
}

// checkRecording pins the recording pipeline the way checkBugreport pins the
// bugreport one: the host captures, the server archives it to the bucket, and
// what comes back is a real WebM rather than whatever the host happened to
// answer with.
func (d *driver) checkRecording() {
	start := time.Now()
	if _, err := d.cfctl(2*time.Minute, cmdDevice, cmdRecord, "start", d.dev.ID); err != nil {
		d.f.fail("recording", time.Since(start), "starting the recording: "+err.Error())
		return
	}
	d.driveDisplayFrames()
	if _, err := d.cfctl(2*time.Minute, cmdDevice, cmdRecord, "stop", d.dev.ID); err != nil {
		d.f.fail("recording", time.Since(start), "stopping the recording: "+err.Error())
		return
	}

	out, err := d.cfctl(time.Minute, cmdDevice, cmdRecord, "list", d.dev.ID, "--json")
	if err != nil {
		d.f.fail("recording", time.Since(start), "listing recordings: "+err.Error())
		return
	}
	var names []string
	if err := json.Unmarshal([]byte(out), &names); err != nil {
		d.f.fail("recording", time.Since(start), "decoding the recording list: "+err.Error())
		return
	}
	if len(names) == 0 {
		d.f.fail("recording", time.Since(start), "the host listed no recording after a stop")
		return
	}

	path, err := d.writeTemp("livesmoke-*.webm")
	if err != nil {
		d.f.fail("recording", time.Since(start), err.Error())
		return
	}
	defer os.Remove(path)

	if _, err := d.cfctl(3*time.Minute, cmdDevice, cmdRecord, "fetch", d.dev.ID, names[0],
		"--output="+path, "--json"); err != nil {
		d.f.fail("recording", time.Since(start), "archiving the recording: "+err.Error())
		return
	}

	clip, err := os.ReadFile(path)
	switch {
	case err != nil:
		d.f.fail("recording", time.Since(start), "reading the archived recording: "+err.Error())
	case !bytes.HasPrefix(clip, webmMagic):
		// Everything up to here is CCO's: the verbs, the archive, the framing
		// guard. A body that is not even EBML means one of those broke.
		d.f.fail("recording", time.Since(start),
			fmt.Sprintf("archived %d bytes that are not WebM-framed", len(clip)))
	case !bytes.Contains(clip, webmTracks), !bytes.Contains(clip, webmCluster):
		// The arm drove screenshots for the whole window so that frames would
		// exist, so an empty container now means that stopped working. Either
		// the host no longer attaches a display client per screenshot, or the
		// recorder is broken. This is reported rather than failed because the
		// mechanism being relied on is an upstream implementation detail, and a
		// change there should be visible without reading as a CCO regression.
		// A 110-byte artifact is the known signature.
		d.f.skip("recording", fmt.Sprintf(
			"%d bytes with no frames despite driven display activity. The host stopped "+
				"producing frames for the recorder; capture, archive and framing all worked", len(clip)))
	default:
		d.f.pass("recording", time.Since(start), fmt.Sprintf("%s, %d bytes", names[0], len(clip)))
	}
}

// maxArchiveBytes bounds what one bugreport may expand to. A capture with an adb
// bugreport folded in runs to tens of MB, so this is far above any real artifact
// and exists only so a malformed or hostile archive cannot be inflated
// indefinitely by the act of checking it.
const maxArchiveBytes = 2 << 30

// readZIP opens an archive and reads every entry through, returning the member
// names. Each entry is decompressed because a truncated or corrupt archive has
// a perfectly intact directory and fails only when the bytes are actually pulled.
func readZIP(filename string) ([]string, error) {
	r, err := zip.OpenReader(filename)
	if err != nil {
		return nil, fmt.Errorf("opening %s as an archive: %w", filename, err)
	}
	defer r.Close()

	names := make([]string, 0, len(r.File))
	budget := int64(maxArchiveBytes)
	for _, f := range r.File {
		names = append(names, f.Name)
		rc, err := f.Open()
		if err != nil {
			return names, fmt.Errorf("opening %s: %w", f.Name, err)
		}
		// LimitReader rather than a bare Copy: reading to EOF is what verifies
		// the entry's checksum, but an archive that never reaches EOF must not
		// be able to run this to exhaustion.
		n, err := io.Copy(io.Discard, io.LimitReader(rc, budget))
		_ = rc.Close()
		if err != nil {
			return names, fmt.Errorf("reading %s: %w", f.Name, err)
		}
		if budget -= n; budget <= 0 {
			return names, fmt.Errorf("archive expands beyond %d bytes", int64(maxArchiveBytes))
		}
	}
	return names, nil
}

// checkRunDurationFailsafe asserts the VM carries its own termination bound, so
// a control plane that dies does not leave it billing forever.
func (d *driver) checkRunDurationFailsafe() {
	start := time.Now()
	if d.host.GCEInstanceName == "" || d.host.Zone == "" {
		d.f.skip("failsafe", "host VM identity unknown")
		return
	}
	out, err := d.gcloud(2*time.Minute, "compute", "instances", "describe", d.host.GCEInstanceName,
		"--zone="+d.host.Zone, "--format=value(scheduling.maxRunDuration.seconds,scheduling.instanceTerminationAction)")
	if err != nil {
		d.f.fail("failsafe", time.Since(start), err.Error())
		return
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 || fields[0] == "" {
		d.f.fail("failsafe", time.Since(start), "VM has no maxRunDuration: it outlives the control plane")
		return
	}
	d.f.pass("failsafe", time.Since(start), "maxRunDuration="+strings.Join(fields, " action="))
}

// checkVerbs runs the lifecycle verbs that outrun an ordinary request. Each is
// its own row: the interesting output is which ones fail and how long they
// took, since the failure mode being guarded against is a deadline that sits
// just under their real runtime.
//
// restart and powerwash pass --wait, and it is NOT optional. They return an
// operation handle now, so without it the command exits 0 the moment the verb is
// ADMITTED, and this arm would time an enqueue and call the verb proven. With it,
// the row covers strictly more than it did when the verbs were synchronous: the
// handler actuating, the terminal write landing, and cfctl's tri-valued status
// distinguishing a settled failure from a settled success (a two-valued one
// reports a failed powerwash as a pass).
func (d *driver) checkVerbs() {
	for _, v := range []struct {
		name  string
		args  []string
		after string // device status to wait for, "" to skip
	}{
		{name: "restart", args: []string{cmdDevice, "restart", d.dev.ID, "--wait"}},
		{name: "powerwash", args: []string{cmdDevice, "powerwash", d.dev.ID, "--force", "--wait"}},
		{name: "stop", args: []string{cmdDevice, "stop", d.dev.ID}, after: "stopped"},
		{name: "start", args: []string{cmdDevice, "start", d.dev.ID}, after: "running"},
	} {
		start := time.Now()
		if _, err := d.cfctl(d.cfg.VerbTimeout, v.args...); err != nil {
			d.f.fail(v.name, time.Since(start), err.Error())
			continue
		}
		if v.after != "" {
			if got, err := d.awaitDeviceStatus(v.after, d.cfg.VerbTimeout); err != nil {
				d.f.fail(v.name, time.Since(start), fmt.Sprintf("verb returned but %v (status %q)", err, got.Status))
				continue
			}
		}
		d.f.pass(v.name, time.Since(start), "")
	}
}

// checkSnapshot pins the host-pinned snapshot loop end to end, running the
// same proof the upstream e2e suite does: take on the running device, mutate
// its state away with a powerwash, stop, restore, and land running again with
// a responsive launcher; then delete and confirm the inventory is empty.
//
// The take gets one retry after a settling pause, and only while the device
// still reads running, which means the platform's compensating restart saved
// it. A guest freshly (re)started can refuse the suspend for a minute or so,
// and a take that succeeds on retry means snapshots work as users are told to
// use them. A device marked error is the destroyed-launcher shape: the retry
// could only 4xx, and the arm should report the take that killed it.
func (d *driver) checkSnapshot() {
	const arm = "snapshot"
	start := time.Now()

	snapID, err := d.snapshotTake()
	if err != nil {
		if got, aerr := d.awaitDeviceStatus("running", d.cfg.VerbTimeout); aerr != nil {
			d.f.fail(arm, time.Since(start),
				fmt.Sprintf("take failed (%v) and the device did not come back: %v (status %q)", err, aerr, got.Status))
			return
		}
		slog.Info("snapshot take failed on a live device; retrying after a settling pause", slog.Any("error", err))
		d.retriedTakes++
		time.Sleep(90 * time.Second)
		snapID, err = d.snapshotTake()
	}
	if err == nil {
		if err = d.snapshotRestoreLeg(snapID); err == nil {
			err = d.snapshotDeleteLeg(snapID)
		}
	}
	if err != nil {
		d.f.fail(arm, time.Since(start), err.Error())
		return
	}
	d.f.pass(arm, time.Since(start), "take→powerwash→stop→restore→powerbtn→delete, snapshot "+snapID)
}

// snapshotTake captures a snapshot and verifies the inventory holds exactly
// it, ready.
func (d *driver) snapshotTake() (string, error) {
	out, err := d.cfctl(d.cfg.CreateTimeout, cmdDevice, cmdSnapshot, "take", d.dev.ID, "--wait", "--json")
	if err != nil {
		return "", fmt.Errorf("take: %w", err)
	}
	var op struct {
		Response struct {
			SnapshotID string `json:"snapshot_id"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(out), &op); err != nil || op.Response.SnapshotID == "" {
		return "", fmt.Errorf("take: settled operation carries no snapshot_id: %s", out)
	}
	snapID := op.Response.SnapshotID

	out, err = d.cfctl(time.Minute, cmdDevice, cmdSnapshot, "list", d.dev.ID, "--json")
	if err != nil {
		return "", fmt.Errorf("list: %w", err)
	}
	var snaps []struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &snaps); err != nil {
		return "", fmt.Errorf("list: decoding snapshot list: %w", err)
	}
	if len(snaps) != 1 || snaps[0].ID != snapID || snaps[0].State != "ready" {
		return "", fmt.Errorf("list: inventory = %+v, want exactly %s ready", snaps, snapID)
	}
	return snapID, nil
}

// snapshotRestoreLeg mutates the guest away from the snapshot, stops the
// device, restores, and proves the restored guest answers with a framed
// capture. The powerwash guarantees the pre-restore state differs from the
// snapshot, so a restore that silently no-ops has something to be caught
// against (upstream's e2e proves this with a file; the lifecycle plus a live
// capture is what this harness can assert).
func (d *driver) snapshotRestoreLeg(snapID string) error {
	if _, err := d.cfctl(d.cfg.VerbTimeout, cmdDevice, "powerwash", d.dev.ID, "--force", "--wait"); err != nil {
		return fmt.Errorf("powerwash before restore: %w", err)
	}
	if _, err := d.cfctl(d.cfg.VerbTimeout, cmdDevice, "stop", d.dev.ID); err != nil {
		return fmt.Errorf("stop before restore: %w", err)
	}
	if got, err := d.awaitDeviceStatus("stopped", d.cfg.VerbTimeout); err != nil {
		return fmt.Errorf("stop before restore: %w (status %q)", err, got.Status)
	}

	if _, err := d.cfctl(d.cfg.CreateTimeout, cmdDevice, cmdSnapshot, "restore", d.dev.ID, snapID, "--force", "--wait"); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	if got, err := d.awaitDeviceStatus("running", d.cfg.VerbTimeout); err != nil {
		return fmt.Errorf("restore settled but %w (status %q)", err, got.Status)
	}

	// Aliveness is asserted through the launcher (powerbtn), NOT a capture.
	// The upstream streamer has no restore path. The resumed guest keeps
	// pushing frames at its pre-snapshot streamer session, so screenshots on
	// a restored device fail indefinitely (proven live: 16 attempts over
	// 8 minutes, quiet and active snapshots alike, powerbtn kick included).
	// ADB is the working consumer of a restored device; livesmoke has no ADB
	// arm, so the launcher round-trip is the strongest assertion left.
	if _, err := d.cfctl(d.cfg.VerbTimeout, cmdDevice, "powerbtn", d.dev.ID); err != nil {
		return fmt.Errorf("post-restore powerbtn: %w", err)
	}
	return nil
}

// snapshotDeleteLeg removes the snapshot and confirms the inventory is empty.
func (d *driver) snapshotDeleteLeg(snapID string) error {
	if _, err := d.cfctl(d.cfg.VerbTimeout, cmdDevice, cmdSnapshot, "delete", d.dev.ID, snapID, "--force", "--wait"); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	out, err := d.cfctl(time.Minute, cmdDevice, cmdSnapshot, "list", d.dev.ID, "--json")
	if err != nil {
		return fmt.Errorf("list after delete: %w", err)
	}
	var left []json.RawMessage
	if err := json.Unmarshal([]byte(out), &left); err != nil || len(left) != 0 {
		return fmt.Errorf("list after delete: inventory not empty: %s", out)
	}
	return nil
}

// checkBootWatch is the boot-watch observation's positive control, and the
// only guard against an upstream marker rename or verbosity drift: a fixture
// test cannot catch either, because fixture and parser drift together and
// stay green forever. It fetches the live device's own log tails through the
// /v1 passthrough and runs the SHARED domain parser over them, asserting the
// range form, which is every loop counter below its threshold plus a
// completion marker ordered after the last attempt marker. Healthy creates
// vary between one and two boot cycles, so exact counts would flake; the
// thresholds' own margins are the assertion.
func (d *driver) checkBootWatch() {
	if d.cfg.SA == "" && !d.cfg.AuthNone {
		d.f.skip("bootwatch", "-sa not set (needs a signed JWT to reach /v1)")
		return
	}
	start := time.Now()

	group, name, ok := strings.Cut(d.dev.CVDID, "/")
	if !ok {
		d.f.fail("bootwatch", time.Since(start), "device has no group/name CVD id: "+d.dev.CVDID)
		return
	}
	token, err := d.v1Token()
	if err != nil {
		d.f.fail("bootwatch", time.Since(start), err.Error())
		return
	}
	base := fmt.Sprintf("%s/v1/zones/%s/hosts/%s/cvds/%s/%s/logs",
		d.serverURL, d.host.Zone, d.dev.HostID, group, name)
	kernelTail, err := d.v1GetTail(token, base+"/"+devicedomain.KernelLogFile)
	if err != nil {
		d.f.fail("bootwatch", time.Since(start), "fetching kernel.log tail: "+err.Error())
		return
	}
	launcherTail, err := d.v1GetTail(token, base+"/"+devicedomain.LauncherLogFile)
	if err != nil {
		d.f.fail("bootwatch", time.Since(start), "fetching launcher.log tail: "+err.Error())
		return
	}

	obs := devicedomain.CountBootMarkers(kernelTail, launcherTail)
	var faults []string
	// The completion marker is the rename guard this control can actually
	// hold. A healthy boot writes ~430 KB, so the bootloader banners at the
	// cycle's head scroll out of a 256 KiB tail on a completed boot (measured
	// live, 2026-08-03), and their absence proves nothing. BOOT_COMPLETED is
	// emitted at the cycle's END and is always tail-resident on a healthy
	// device; if upstream renames it, this line is what goes red.
	if !obs.BootCompleted {
		faults = append(faults, "no completion marker after the last attempt marker "+
			"(renamed upstream, or the guest never completed boot?)")
	}
	if obs.KernelPanics >= devicedomain.LoopThresholdKernelPanics {
		faults = append(faults, fmt.Sprintf("kernel panics %d >= threshold %d on a healthy boot",
			obs.KernelPanics, devicedomain.LoopThresholdKernelPanics))
	}
	if obs.GuestReboots >= devicedomain.LoopThresholdGuestReboots {
		faults = append(faults, fmt.Sprintf("guest reboots %d >= threshold %d on a healthy boot",
			obs.GuestReboots, devicedomain.LoopThresholdGuestReboots))
	}
	if obs.UBootStarts >= devicedomain.LoopThresholdUBootStarts {
		faults = append(faults, fmt.Sprintf("bootloader starts %d >= threshold %d on a healthy boot",
			obs.UBootStarts, devicedomain.LoopThresholdUBootStarts))
	}
	if len(faults) > 0 {
		d.f.fail("bootwatch", time.Since(start), strings.Join(faults, "; "))
		return
	}
	d.f.pass("bootwatch", time.Since(start), fmt.Sprintf(
		"healthy range holds: completed after %d U-Boot / %d panic / %d reboot markers in the tail",
		obs.UBootStarts, obs.KernelPanics, obs.GuestReboots))
}

// v1GetTail fetches a bounded log tail through the /v1 passthrough, the same
// suffix-range request the platform's own enrichment sends.
func (d *driver) v1GetTail(token, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=-%d", devicedomain.BootLogTailBytes))

	client := &http.Client{Timeout: time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, devicedomain.BootLogTailBytes+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s: HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// checkDriftHeal manufactures the state an aborted start leaves behind (a
// stopped row over a running CVD) by starting the CVD through the /v1
// passthrough, where CCO cannot see it, and asserts the controller notices.
func (d *driver) checkDriftHeal() {
	if d.cfg.SA == "" && !d.cfg.AuthNone {
		d.f.skip("drift", "-sa not set (needs a signed JWT to reach /v1)")
		return
	}
	start := time.Now()

	if _, err := d.cfctl(d.cfg.VerbTimeout, "device", "stop", d.dev.ID); err != nil {
		d.f.fail("drift", time.Since(start), "stopping device: "+err.Error())
		return
	}
	if _, err := d.awaitDeviceStatus("stopped", d.cfg.VerbTimeout); err != nil {
		d.f.fail("drift", time.Since(start), err.Error())
		return
	}

	group, name, ok := strings.Cut(d.dev.CVDID, "/")
	if !ok {
		d.f.fail("drift", time.Since(start), "device has no group/name CVD id: "+d.dev.CVDID)
		return
	}
	if err := d.startCVDBehindCCO(group, name); err != nil {
		d.f.fail("drift", time.Since(start), err.Error())
		return
	}

	got, err := d.awaitDeviceStatus("running", d.cfg.HealTimeout)
	if err != nil {
		d.f.fail("drift", time.Since(start),
			fmt.Sprintf("row never converged: status=%q cvd_status=%q", got.Status, got.CVDStatus))
		return
	}
	d.f.pass("drift", time.Since(start), "row healed to running from the CVD observation alone")
}

// startCVDBehindCCO starts the CVD through the legacy passthrough and waits out
// the host operation, leaving CCO's own row untouched.
func (d *driver) startCVDBehindCCO(group, name string) error {
	token, err := d.v1Token()
	if err != nil {
		return err
	}
	base := fmt.Sprintf("%s/v1/zones/%s/hosts/%s", d.serverURL, d.host.Zone, d.dev.HostID)

	var op struct {
		Name string `json:"name"`
	}
	if err := d.v1Post(token, fmt.Sprintf("%s/cvds/%s/%s/:start", base, group, name), &op); err != nil {
		return fmt.Errorf("starting CVD via /v1: %w", err)
	}
	if op.Name == "" {
		return errors.New("/v1 :start returned no operation")
	}
	if err := d.v1Post(token, fmt.Sprintf("%s/operations/%s/:wait", base, op.Name), nil); err != nil {
		return fmt.Errorf("waiting for the /v1 start operation: %w", err)
	}
	return nil
}

func (d *driver) v1Post(token, url string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s: HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// v1Token returns the Authorization token for direct /v1 calls: a signed SA
// JWT normally, or "" in auth-none mode. A network deployment accepts bare
// requests, and sending none is what the mode is being smoked for.
func (d *driver) v1Token() (string, error) {
	if d.cfg.AuthNone {
		return "", nil
	}
	return d.signJWT()
}

// signJWT mints the self-signed service-account JWT that built-in IAP accepts.
// The audience must carry the trailing /*, because the bare service URL is
// rejected.
func (d *driver) signJWT() (string, error) {
	claims, err := d.writeTemp("livesmoke-claims-*.json")
	if err != nil {
		return "", err
	}
	defer os.Remove(claims)
	out, err := d.writeTemp("livesmoke-token-*.jwt")
	if err != nil {
		return "", err
	}
	defer os.Remove(out)

	now := time.Now().Unix()
	body := fmt.Sprintf(`{"iss":%q,"sub":%q,"aud":"%s/*","iat":%d,"exp":%d}`,
		d.cfg.SA, d.cfg.SA, d.serverURL, now, now+3000)
	if err := os.WriteFile(claims, []byte(body), 0o600); err != nil {
		return "", fmt.Errorf("writing JWT claims: %w", err)
	}
	if _, err := d.gcloud(2*time.Minute, "iam", "service-accounts", "sign-jwt", claims, out,
		"--iam-account="+d.cfg.SA); err != nil {
		return "", fmt.Errorf("signing JWT as %s: %w", d.cfg.SA, err)
	}
	token, err := os.ReadFile(out)
	if err != nil {
		return "", fmt.Errorf("reading signed JWT: %w", err)
	}
	return strings.TrimSpace(string(token)), nil
}

// checkTeardown deletes what the run created and confirms the VM went with it.
func (d *driver) checkTeardown() {
	start := time.Now()
	var problems []string

	if _, err := d.cfctl(d.cfg.VerbTimeout, "device", "delete", d.dev.ID, "--force", "--wait"); err != nil {
		problems = append(problems, "device delete: "+err.Error())
	}
	if !d.ownsHost() {
		if len(problems) > 0 {
			d.f.fail("teardown", time.Since(start), strings.Join(problems, "; "))
			return
		}
		d.f.pass("teardown", time.Since(start),
			"device gone; host "+d.dev.HostID+" pre-dates this run and was left alone")
		return
	}
	if _, err := d.cfctl(d.cfg.CreateTimeout, "host", "delete", d.dev.HostID, "--force", "--wait"); err != nil {
		problems = append(problems, "host delete: "+err.Error())
	}
	if d.host.GCEInstanceName != "" {
		out, err := d.gcloud(2*time.Minute, "compute", "instances", "list",
			"--filter=name="+d.host.GCEInstanceName, "--format=value(name)")
		switch {
		case err != nil:
			problems = append(problems, "listing instances: "+err.Error())
		case strings.TrimSpace(out) != "":
			problems = append(problems, "VM "+d.host.GCEInstanceName+" survived teardown")
		}
	}

	if len(problems) > 0 {
		d.f.fail("teardown", time.Since(start), strings.Join(problems, "; "))
		return
	}
	d.f.pass("teardown", time.Since(start), "device, host and VM all gone")
}

// platformChecks read the whole run's window back out of Cloud Logging. A
// request that breaks its own response, or a handler that outruns a deadline,
// shows up here as a 5xx and an instance the platform decided to replace.
// No client-side assertion can see either one.
func (d *driver) platformChecks() {
	window := d.started.Format(time.RFC3339)

	start := time.Now()
	svc := fmt.Sprintf(`resource.type="cloud_run_revision" AND resource.labels.service_name=%q`, d.cfg.Service)
	errs, err := d.gcloud(3*time.Minute, "logging", "read",
		fmt.Sprintf(`%s AND severity>=ERROR AND timestamp>=%q`, svc, window),
		"--limit=20", "--format=value(timestamp,httpRequest.status,httpRequest.requestUrl)")
	switch {
	case err != nil:
		d.f.fail("no-5xx", time.Since(start), err.Error())
	case strings.TrimSpace(errs) != "":
		d.f.fail("no-5xx", time.Since(start), firstLines(errs, 3))
	default:
		d.f.pass("no-5xx", time.Since(start), "")
	}

	start = time.Now()
	recycles, err := d.gcloud(3*time.Minute, "logging", "read",
		fmt.Sprintf(`%s AND jsonPayload.msg="Shutting down..." AND timestamp>=%q`, svc, window),
		"--limit=10", "--format=value(timestamp)")
	switch {
	case err != nil:
		d.f.fail("no-recycles", time.Since(start), err.Error())
	case strings.TrimSpace(recycles) != "":
		// A deploy during the run legitimately shows one; the harness cannot
		// tell them apart, so it reports rather than guesses.
		d.f.fail("no-recycles", time.Since(start),
			"instance shutdown during the run (a concurrent deploy also looks like this): "+firstLines(recycles, 3))
	default:
		d.f.pass("no-recycles", time.Since(start), "")
	}

	// Guards that degrade instead of failing are invisible to every other check
	// here, because the run stays green while the protection is absent. That has
	// happened: the reconciling guard shipped with a query real Spanner refused,
	// and this whole suite passed while it fell back on every health cycle.
	//
	// The query is keyed on the structured field rather than on message text.
	// The first version of this arm grepped for a phrase, which meant it could
	// only ever catch the one guard whose wording someone had remembered; every
	// guard written afterwards would have been born unwatched.
	start = time.Now()
	degraded, err := d.gcloud(3*time.Minute, "logging", "read",
		fmt.Sprintf(`%s AND jsonPayload.%s=true AND timestamp>=%q`, svc, telemetry.GuardDegradedKey, window),
		"--limit=10", "--format=value(timestamp,jsonPayload.msg,jsonPayload.error)")
	switch {
	case err != nil:
		d.f.fail("no-degrade", time.Since(start), err.Error())
	case strings.TrimSpace(degraded) != "":
		d.f.fail("no-degrade", time.Since(start),
			"a guard fell back to its unprotected path: "+firstLines(degraded, 2))
	default:
		d.f.pass("no-degrade", time.Since(start), "")
	}

	// --json, not the human form: cfctl reports an empty queue on stderr, so
	// stdout alone cannot tell "no tasks" from "no output".
	start = time.Now()
	out, err := d.cfctl(time.Minute, "admin", "tasks", "list", "--json")
	var dlq struct {
		Tasks []deadTask `json:"tasks"`
	}
	switch {
	case err != nil:
		d.f.fail("dlq", time.Since(start), err.Error())
	case json.Unmarshal([]byte(out), &dlq) != nil:
		d.f.fail("dlq", time.Since(start), "undecodable task list: "+firstLines(out, 2))
	default:
		unexpected, excused := excuseRetriedTakes(dlq.Tasks, d.dev.ID, d.retriedTakes)
		switch {
		case len(unexpected) > 0:
			d.f.fail("dlq", time.Since(start), fmt.Sprintf("%d dead-lettered task(s), first %s %s",
				len(unexpected), unexpected[0].Type, unexpected[0].ID))
		case excused > 0:
			d.f.pass("dlq", time.Since(start), fmt.Sprintf("only the %d take(s) the snapshot arm retried", excused))
		default:
			d.f.pass("dlq", time.Since(start), "empty")
		}
	}
}

type deadTask struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	ResourceID string `json:"resource_id"`
}

// excuseRetriedTakes drops up to retried snapshot takes on deviceID from tasks,
// and returns what is left and how many it dropped.
func excuseRetriedTakes(tasks []deadTask, deviceID string, retried int) ([]deadTask, int) {
	var unexpected []deadTask
	excused := 0
	for _, t := range tasks {
		if excused < retried && t.Type == "device.snapshot_operation" && t.ResourceID == deviceID {
			excused++
			continue
		}
		unexpected = append(unexpected, t)
	}
	return unexpected, excused
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = append(lines[:n], fmt.Sprintf("... and %d more", len(lines)-n))
	}
	return strings.Join(lines, " | ")
}
