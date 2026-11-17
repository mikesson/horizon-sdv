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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// device is the subset of cfctl's device JSON this harness reads. It has its
// own decoder rather than sharing a type, so a wire change that nobody taught
// this file about surfaces here as a failed check.
type device struct {
	ID           string `json:"id"`
	HostID       string `json:"host_id"`
	Name         string `json:"name"`
	CVDID        string `json:"cvd_id"`
	Status       string `json:"status"`
	HealthStatus string `json:"health_status"`
	CVDStatus    string `json:"cvd_status"`
}

type host struct {
	ID              string    `json:"id"`
	Zone            string    `json:"zone"`
	GCEInstanceName string    `json:"gce_instance_name"`
	CreatedAt       time.Time `json:"created_at"`
}

type driver struct {
	cfg     Cfg
	started time.Time
	f       *findings

	serverURL string
	dev       device
	host      host

	created  bool        // a device exists and teardown must run
	tornDown atomic.Bool // guards the normal and signal teardown paths

	// retriedTakes counts snapshot takes the snapshot arm tolerated before its
	// retry. A take never redelivers, so each one dead-letters its task, and
	// the dlq check excuses exactly that many on this device.
	retriedTakes int
}

// ownsHost reports whether this run provisioned the host its device landed on.
// Placement bin-packs, so a create can land on a host that was already serving
// somebody else's devices, and tearing that one down cascades every device on
// it. An unreadable or older host is treated as not ours: leaking a VM the
// reaper will expire costs less than destroying a device nobody asked us to.
func (d *driver) ownsHost() bool {
	return !d.host.CreatedAt.IsZero() && d.host.CreatedAt.After(d.started)
}

func run(cfg Cfg) error {
	d := &driver{cfg: cfg, started: time.Now().UTC(), f: &findings{}}

	// A run that provisions billable VMs must not leak them when it is cut
	// short. SIGKILL still can, but an interrupt or a wrapper's timeout (which
	// is how the first leak happened) arrives as a catchable signal.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-stop
		slog.Warn("interrupted: tearing down before exit", slog.String("signal", sig.String()))
		d.teardown()
		os.Exit(130)
	}()

	execErr := d.execute()
	d.teardown()
	// The platform window covers teardown too: a recycle triggered by the
	// delete path is exactly as interesting as one during a verb.
	d.platformChecks()
	d.report()

	if d.f.failed() {
		return errors.New("livesmoke: checks failed (see the table above)")
	}
	return execErr
}

// execute runs the scenario. A hard error here still reaches teardown and the
// report; it just stops further checks.
func (d *driver) execute() error {
	if err := d.preflight(); err != nil {
		return err
	}
	if err := d.checkCreate(); err != nil {
		return err
	}

	switch d.cfg.Scenario {
	case scenarioScreenshot:
		d.checkScreenshot()
	case scenarioVerbs:
		d.checkVerbs()
	case scenarioFull:
		// First, while the tail still holds the first boot cycle: the verbs
		// below add stop/start cycles that shift the healthy baseline.
		d.checkBootWatch()
		d.checkScreenshot()
		d.checkBugreport()
		// Before the verbs, because recording start/stop require a running
		// device and checkVerbs leaves it stopped part of the time.
		d.checkRecording()
		d.checkRunDurationFailsafe()
		d.checkVerbs()
		d.checkDriftHeal()
		// This arm runs LAST. A failed take can kill the launcher and every
		// verb after it, so ahead of the verbs it turns one failure into a red
		// check for each later verb and costs the whole run its value as a
		// signal. Later failures are not independent evidence when an earlier
		// step can destroy the subject. The arm's old head-of-line rationale
		// (take needs a settled guest, so run it before any stop→start cycle)
		// died with the diagnosis it rested on: on the images where the take
		// fails it is deterministic, settled or not. The settle window that IS
		// real is handled inside the arm by one retry after a pause, never by
		// ordering.
		d.checkSnapshot()
	}
	return nil
}

func (d *driver) preflight() error {
	out, err := d.gcloud(2*time.Minute, "run", "services", "describe", d.cfg.Service,
		"--region="+d.cfg.Region, "--format=value(status.url)")
	if err != nil {
		return fmt.Errorf("resolving service URL: %w", err)
	}
	d.serverURL = strings.TrimSpace(out)
	if d.serverURL == "" {
		return fmt.Errorf("service %q has no URL in region %s", d.cfg.Service, d.cfg.Region)
	}

	if _, err := d.cfctl(time.Minute, "status"); err != nil {
		return fmt.Errorf("service not healthy: %w", err)
	}
	slog.Info("preflight ok", slog.String("service", d.serverURL))
	return nil
}

// teardown removes what the run created, and records whether it left anything
// behind. It runs even when the scenario failed, because a harness that leaks
// VMs on failure trains people to stop running it. It runs at most once, since
// the signal path and the normal path can both reach it.
func (d *driver) teardown() {
	if !d.created {
		return
	}
	if !d.tornDown.CompareAndSwap(false, true) {
		return
	}
	if d.cfg.Keep {
		slog.Warn("-keep: leaving resources up",
			slog.String("device", d.dev.ID), slog.String("host", d.dev.HostID))
		return
	}
	d.checkTeardown()
}

// cfctl runs the shipped binary. The --timeout is passed through so the client
// waits for the server's answer rather than capping itself at its own
// per-request bound; harness-side context is the real deadline.
func (d *driver) cfctl(timeout time.Duration, args ...string) (string, error) {
	full := append([]string{fmt.Sprintf("--timeout=%s", timeout)}, args...)
	var env []string
	if d.cfg.AuthNone {
		env = []string{"CFCTL_AUTH_NONE=1"}
	}
	return d.commandEnv(timeout+30*time.Second, env, d.cfg.Cfctl, full...)
}

func (d *driver) gcloud(timeout time.Duration, args ...string) (string, error) {
	return d.command(timeout, "gcloud", append(args, "--project="+d.cfg.Project)...)
}

func (d *driver) command(timeout time.Duration, bin string, args ...string) (string, error) {
	return d.commandEnv(timeout, nil, bin, args...)
}

// commandEnv runs a binary with extra environment entries appended to the
// inherited environment.
func (d *driver) commandEnv(timeout time.Duration, extraEnv []string, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err,
			strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// deviceState re-reads the device through cfctl.
func (d *driver) deviceState() (device, error) {
	out, err := d.cfctl(time.Minute, "device", "get", d.dev.ID, "--json")
	if err != nil {
		return device{}, err
	}
	var got device
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		return device{}, fmt.Errorf("decoding device JSON: %w", err)
	}
	return got, nil
}

// awaitDeviceStatus polls until the device reaches want or the bound expires.
func (d *driver) awaitDeviceStatus(want string, bound time.Duration) (device, error) {
	deadline := time.Now().Add(bound)
	var last device
	for time.Now().Before(deadline) {
		got, err := d.deviceState()
		if err != nil {
			return last, err
		}
		last = got
		if got.Status == want {
			return got, nil
		}
		time.Sleep(10 * time.Second)
	}
	return last, fmt.Errorf("device stayed %q, want %q after %v", last.Status, want, bound)
}

func (d *driver) writeTemp(pattern string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("closing temp file: %w", err)
	}
	return name, nil
}
