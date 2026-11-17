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

package cvd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// stepCVDLoad journals the HO cvd-load operation a create attempt started.
const stepCVDLoad = "cvd_load"

// RunCreateCVD executes the queued CVD-creation task for a device, deriving
// the CVD configuration from the row's persisted desired_spec (hydrated by the
// repository read). It is idempotent: it no-ops when the device is gone or
// already past creation, and a non-nil error tells the worker to retry (or
// dead-letter when exhausted). The Host Orchestrator call runs outside any
// database transaction. t is the claimed task delivering this run; it keys
// the operation journal and fences its writes on the delivery. nil disables
// journaling for the call.
func (a *Actuator) RunCreateCVD(ctx context.Context, deviceID string, t *task.Task) error {
	d, err := a.devices.Get(ctx, deviceID)
	if err != nil {
		if errors.Is(err, device.ErrNotFound) {
			return nil // device was deleted; nothing to do
		}
		return fmt.Errorf("loading device %s: %w", deviceID, err)
	}
	if d.Status != device.StatusPending && d.Status != device.StatusCreating {
		// Already handled: running, deleted, or error. Error is terminal here
		// with TWO writers, exhaustion (markCreateFailed) and the boot-watch
		// verdict, whose crash-safety rests on this guard: a settle followed
		// by a death before the task completes redelivers here and exits nil.
		// In-flight attempt failures leave the device `creating`, so this
		// guard lets retries through.
		//
		// A device already in teardown must not be absorbed silently: a prior
		// attempt of THIS task may have created the host-side group after the
		// cleanup task's probe ran (create and delete race on independent
		// workers), and nothing else will ever visit that group again. Sweep
		// it before completing the redelivery. The probe-first cleanup makes
		// this one cheap list when no group was ever created.
		if d.Status == device.StatusDeleting || d.Status == device.StatusDeleted {
			if err := a.RunCleanupCVD(ctx, deviceID); err != nil {
				return fmt.Errorf("sweeping group of torn-down device %s: %w", deviceID, err)
			}
		}
		return nil
	}

	// First delivery only: a not-ready host postpones this task every 20s for
	// the whole host leg, and re-writing an unchanged status each time is a
	// Spanner write per pending device per postpone.
	if d.Status != device.StatusCreating {
		if err := a.devices.UpdateStatus(ctx, d.ID, device.StatusCreating, ""); err != nil {
			return fmt.Errorf("marking device creating: %w", err)
		}
	}

	h, err := a.hosts.Get(ctx, d.HostID)
	if err != nil {
		return fmt.Errorf("loading host %s: %w", d.HostID, err)
	}

	// A host that is still coming up is waited for (auto-provisioned
	// hosts take ~1 min); a host that can never serve this create fails it
	// permanently. Other states (stopped, unreachable, preempted) fall through
	// to the normal HO error/retry path, because they may recover.
	switch h.Status {
	case host.StatusPending, host.StatusProvisioning:
		return &hostNotReadyError{hostID: h.ID, status: h.Status}
	case host.StatusStarting:
		// The row lags reality here: HO typically serves tens of seconds after
		// the VM observation flips the host to starting, and nothing but the
		// device controller's 1-minute cycle would promote it. Ask the host
		// directly. If HO answers, promote now and run the create instead of
		// postponing into that cycle.
		if !a.probeStartingHost(ctx, h) {
			return &hostNotReadyError{hostID: h.ID, status: h.Status}
		}
	case host.StatusDeleting, host.StatusDeleted, host.StatusError:
		return &hostGoneError{hostID: h.ID, status: h.Status}
	default:
		// Running serves the create; stopped/unreachable/preempted fall
		// through to the normal HO error/retry path, because they may recover.
	}

	return a.createCVD(ctx, d, h, t)
}

// hoReadyProbeTimeout bounds one ready-probe of a starting host. Short: a
// miss costs only this attempt's postpone, and the probe re-runs on every
// redelivery until the host serves or the wait budget expires.
const hoReadyProbeTimeout = 3 * time.Second

// probeStartingHost reports whether a still-`starting` host's Host
// Orchestrator already answers, promoting the row (the ho_reachable latch)
// when it does. The probe is breaker-safe (ProbeReady): boot-window
// connection refusals are expected, and recording them would trip the shared
// breaker right as the host comes up. A failed promotion write does not
// block the create. HO answered, so the create can run, and the device
// controller's next cycle backfills the latch.
func (a *Actuator) probeStartingHost(ctx context.Context, h *host.Host) bool {
	if h.HostOrchestratorURL() == "" {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, hoReadyProbeTimeout)
	defer cancel()
	if _, err := a.ho.ClientForHost(h.HostOrchestratorURL()).ProbeCVDs(probeCtx); err != nil {
		return false
	}
	if _, err := a.hosts.MarkHOReady(ctx, h.ID); err != nil {
		a.logger.Warn("failed to promote HO-ready host after create-side probe",
			slog.String("host_id", h.ID), slog.Any("error", err))
	}
	return true
}

// hostNotReadyError reports that the device's host is still provisioning; the
// task handler translates it into a postpone (wait without burning attempts).
type hostNotReadyError struct {
	hostID string
	status host.Status
}

func (e *hostNotReadyError) Error() string {
	return fmt.Sprintf("host %s is %s", e.hostID, e.status)
}

// retryDelay picks the postpone for this wait: a starting host is probed
// directly on every redelivery and typically serves within tens of seconds,
// so it re-checks fast; pending/provisioning rides the host leg, which takes
// a minute or more.
func (e *hostNotReadyError) retryDelay() time.Duration {
	if e.status == host.StatusStarting {
		return hostStartingRetry
	}
	return hostNotReadyRetry
}

// hostGoneError reports that the device's host reached a state it will never
// serve CVDs from; the create fails permanently.
type hostGoneError struct {
	hostID string
	status host.Status
}

func (e *hostGoneError) Error() string {
	return fmt.Sprintf("host %s is %s; cannot create CVD", e.hostID, e.status)
}

// cvdLaunchInFlightError reports that the device's group exists on the host in
// a transient state (a prior attempt's launch or stop is still running). The
// task handler translates it into a postpone: the group settles into Running
// (adopted as success) or Boot Failed/Stopped (the quiescent husk path) on its
// own, and actuating before then races the live launcher.
type cvdLaunchInFlightError struct {
	group  string
	status string
}

func (e *cvdLaunchInFlightError) Error() string {
	return fmt.Sprintf("CVD group %s is %s; a prior attempt's operation is still in flight", e.group, e.status)
}

// createCVD creates the CVD on the host orchestrator. It runs from the task
// worker, not a request goroutine, and returns an error for the worker to retry.
// The status->creating transition is performed by the caller (RunCreateCVD).
func (a *Actuator) createCVD(ctx context.Context, d *device.Device, h *host.Host, t *task.Task) error {
	hoURL := h.HostOrchestratorURL()
	if hoURL == "" {
		return fmt.Errorf("host %s has no IP address", h.ID)
	}

	// Tag the ambient (consumer) span so CVD-create traces are filterable by
	// host and build; the HO calls themselves are already traced by hoclient.
	trace.SpanFromContext(ctx).SetAttributes(
		attribute.String("device.host_id", h.ID),
		attribute.String("device.build_id", d.BuildID),
	)

	a.logger.Info("creating CVD on host",
		slog.String("device_id", d.ID),
		slog.String("host_id", h.ID),
		slog.String("host_url", hoURL),
	)

	// Create client for this host
	client := a.ho.ClientForHost(hoURL)

	groupName := device.CVDGroupName(d.ID)

	// Journal resume: a prior attempt recorded the load operation it started
	// (an abandoned wait or crashed worker; the operation itself never
	// stopped). Poll the SAME operation instead of re-actuating or adopting
	// the half-finished result by observation.
	if resumed, done, err := a.resumeCVDLoad(ctx, d, client, groupName, t); resumed {
		if done || err != nil {
			return err
		}
		// The resumed operation failed terminally: journey restarted; the
		// list/adopt/husk ladder below handles whatever the launch left.
	}

	// Redelivery guard (journal-less fallback: pre-journal tasks, a crash in
	// the accept→record window, or a lost journal read): a prior attempt may
	// have created the group on the host and died before recording it. HO
	// creation is not idempotent. Re-creating an existing group fails every
	// retry to dead-letter, so adopt it instead.
	existing, err := client.ListCVDs(ctx)
	if err != nil {
		return fmt.Errorf("listing CVDs on host %s: %w", h.ID, err)
	}
	if handled, err := a.adoptExistingGroup(ctx, d, client, groupMembers(existing, groupName), groupName); handled {
		return err
	}

	createReq, err := a.buildCreateRequest(ctx, d, groupName)
	if err != nil {
		return err
	}

	// Create the CVD: start the operation, journal its name (so a redelivery
	// resumes it), then wait. The record is best-effort. A failed write
	// leaves this attempt correct and the adopt guard covers its redelivery.
	opName, err := client.CreateCVDOperation(ctx, createReq)
	if err != nil {
		return fmt.Errorf("starting CVD creation: %w", err)
	}
	if a.journal != nil && t != nil {
		if err := a.journal.RecordOperation(ctx, t, stepCVDLoad, opName); err != nil {
			a.logger.Warn("failed to journal CVD load operation",
				slog.String("device_id", d.ID), slog.String("operation", opName), slog.Any("error", err))
		}
	}
	resp, stand, err := a.awaitCVDCreate(ctx, client, opName, d, groupName)
	if a.standDownConcluded(stand, d, groupName) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to create CVD: %w", withURLFetchHint(err, createReq.EnvConfig, h))
	}

	return a.updateDeviceAfterCVDCreate(ctx, d, resp, client, groupName)
}

// standDownConcluded reports whether a stood-down wait already concluded the
// create, logging which way. Both reasons complete the task without touching
// the host. A deleted device's cleanup task, enqueued in the delete's own
// transaction, owns teardown (deleting the group here would race the live
// launcher and leak its processes). A settled boot verdict leaves the group
// standing, because its logs are the diagnosis and its launcher is still
// live.
func (a *Actuator) standDownConcluded(stand standDown, d *device.Device, groupName string) bool {
	switch stand {
	case standDownDeleted:
		a.logger.Info("device deleted during CVD creation; abandoning the wait",
			slog.String("device_id", d.ID), slog.String("group_name", groupName))
		return true
	case standDownSettled:
		a.logger.Info("boot verdict settled device during create; abandoning the wait",
			slog.String("device_id", d.ID), slog.String("group_name", groupName))
		return true
	default:
		return false
	}
}

// adoptExistingGroup handles a group a prior attempt left on the host. The
// first result is false (only when the group list is empty) to send the
// caller down the fresh-create path. A group in a transient state must not be actuated on:
// the prior attempt's operation may still be RUNNING server-side, starting a
// second launcher collides on the tap devices and the operator registration,
// and deleting it mid-launch leaks the launcher's processes, which poisons the
// host for every future create. It postpones until the launch settles:
// Running adopts cleanly, Boot Failed/Stopped take the quiescent husk path in
// updateDeviceAfterCVDCreate.
//
// The postpone loop has no sampler under it, so it is otherwise
// observation-free for up to launchWaitCap, and a hung looping launcher
// waits here. One budgeted boot-watch tick per redelivery closes that
// window; a settle completes the task and leaves the group as evidence.
func (a *Actuator) adoptExistingGroup(ctx context.Context, d *device.Device, client hoclient.Client, groupCVDs []*hoclient.CVD, groupName string) (bool, error) {
	if len(groupCVDs) == 0 {
		return false, nil
	}
	if transient := transientGroupStatus(groupCVDs); transient != "" {
		if a.bootWatchTick(ctx, client, d, groupName, groupCVDs, nil) {
			return true, nil
		}
		return true, &cvdLaunchInFlightError{group: groupName, status: transient}
	}
	a.logger.Info("adopting existing CVD group from a prior attempt",
		slog.String("device_id", d.ID),
		slog.String("group_name", groupName),
		slog.Int("cvd_count", len(groupCVDs)))
	return true, a.updateDeviceAfterCVDCreate(ctx, d,
		&hoclient.CreateCVDResponse{CVDs: groupCVDs}, client, groupName)
}

// standDown says why an in-flight create wait was abandoned early. The row is
// the coordination bus for both reasons: a delete writes deleting/deleted, a
// boot verdict writes error, and the row-poll notices either. No verdict is
// threaded through the wait itself.
type standDown int32

const (
	standDownNone    standDown = iota
	standDownDeleted           // teardown owns the device; its cleanup task tears down the group
	standDownSettled           // a terminal error settle (the boot verdict); the group is left as evidence
)

// awaitCVDCreate waits for the Host Orchestrator create operation, sampling
// the group's load progress alongside it and abandoning the wait early when
// the device row leaves the create's ownership, either deleted under it or
// settled to error by the boot verdict (cooperative cancellation). A non-none
// standDown is reported ONLY for a row settle the poll observed; with no
// cancel-poll interval configured the watcher is off, so a context
// cancellation (shutdown, handler timeout) surfaces as an error to retry and
// is never mistaken for either.
func (a *Actuator) awaitCVDCreate(ctx context.Context, client hoclient.Client, opName string, d *device.Device, groupName string) (*hoclient.CreateCVDResponse, standDown, error) {
	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()

	var stand atomic.Int32
	var wg sync.WaitGroup
	if a.loadSample > 0 {
		wg.Go(func() { a.sampleCVDLoad(waitCtx, client, d, groupName) })
	}
	if a.cancelPoll > 0 {
		wg.Go(func() {
			if r := a.watchCreateRow(waitCtx, d.ID); r != standDownNone {
				stand.Store(int32(r))
				cancelWait() // interrupt the wait; WaitCVDOperation returns promptly
			}
		})
	}

	resp, err := client.WaitCVDOperation(waitCtx, opName, d.DesiredInstanceCount())
	cancelWait() // stop the watchers whether or not one fired
	wg.Wait()    // join before reading `stand`, and before the caller's row
	// Mutate runs, which is what makes a settle racing a completed operation
	// benign (error → running is a legal transition).

	if r := standDown(stand.Load()); r != standDownNone {
		return nil, r, nil
	}
	//nolint:wrapcheck // the caller (createCVD/resumeCVDLoad) adds context.
	return resp, standDownNone, err
}

// defaultLoadSampleInterval paces the load-wait sampler: coarse enough to be
// noise-free (a handful of extra listings per create), fine enough to place
// the fetch→boot boundary within seconds.
const defaultLoadSampleInterval = 10 * time.Second

// sampleCVDLoad observes the group's listing status for as long as the HO
// load wait runs, recording each change as an event on the ambient attempt
// span. The multi-minute create wait is otherwise a black box, one long
// :wait poll whose artifact-fetch and cvd-boot halves are indistinguishable.
// These events are what splits it (absent → a transient state → Running,
// each timestamped). Observations go through ProbeCVDs, which bypasses the
// host breaker: the recording :wait poll running beside this sampler is the
// accurate health signal, and a recording sampler would double-count the same
// outage. It is best-effort. A failed sample is skipped, and the ticker never
// fires for waits shorter than the interval.
//
// Each successful probe also feeds the boot watch (self-budgeted: nothing is
// fetched before the enrichment dwell, then once per interval), which is what
// separates a slow boot from a crash-looping guest. A terminal verdict
// settles the row; the row-poll in awaitCVDCreate is what stands the wait
// down, so this sampler just stops observing.
func (a *Actuator) sampleCVDLoad(ctx context.Context, client hoclient.Client, d *device.Device, groupName string) {
	ticker := time.NewTicker(a.loadSample)
	defer ticker.Stop()
	span := trace.SpanFromContext(ctx)
	// The transition trail accumulates in-process and is persisted whole by
	// the boot watch, seeded from the row so a resumed wait extends the prior
	// attempt's trail instead of restarting (and re-stamping) it.
	var transitions []device.BootTransition
	last := ""
	if prev := d.BootObservation; prev != nil {
		transitions = slices.Clone(prev.Transitions)
		if len(transitions) > 0 {
			last = transitions[len(transitions)-1].Status
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		cvds, err := client.ProbeCVDs(ctx)
		if err != nil {
			continue
		}
		members := groupMembers(cvds, groupName)
		status := groupLoadStatus(members)
		if status != last {
			last = status
			transitions = append(transitions, device.BootTransition{Status: status, At: time.Now()})
			span.AddEvent("cvd.load.observed", trace.WithAttributes(
				attribute.String("cvd.group", groupName),
				attribute.String("cvd.status", status),
			))
		}
		if a.bootWatchTick(ctx, client, d, groupName, members, transitions) {
			return
		}
	}
}

// groupLoadStatus summarizes the group's presence in a listing: absent until
// the launcher registers it, else the fold over every instance it registered.
// Folded rather than read off the first member, because the trail exists to
// place the fetch→boot boundary: a group reaches Running when its LAST
// instance does, and a first member that came up early would stamp the leg as
// over while the rest of the group was still booting.
func groupLoadStatus(members []*hoclient.CVD) string {
	if len(members) == 0 {
		return "absent"
	}
	return device.FoldInstanceStates(instancesFromCVDs(members))
}

// watchCreateRow polls the device row until it leaves the create's ownership,
// through teardown (deleting/deleted, or the row is gone) or a terminal error
// settle (the boot verdict's write), or until ctx ends. It is tick-first and
// never reads before the first interval, so a wait that completes within one
// interval does no repository read at all. A transient read failure keeps
// waiting; only an explicit status (or ErrNotFound) reports a stand-down.
func (a *Actuator) watchCreateRow(ctx context.Context, deviceID string) standDown {
	ticker := time.NewTicker(a.cancelPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return standDownNone
		case <-ticker.C:
		}
		d, err := a.devices.Get(ctx, deviceID)
		switch {
		case errors.Is(err, device.ErrNotFound):
			return standDownDeleted
		case err != nil:
			continue // transient read failure: keep waiting
		case d.Status == device.StatusDeleting || d.Status == device.StatusDeleted:
			return standDownDeleted
		case d.Status == device.StatusError:
			return standDownSettled
		}
	}
}

// buildCreateRequest assembles the HO create request: the instance shape,
// build artifacts routed into disk fields, and the env_config (user-provided
// in advanced mode, defaults in simple mode).
// See: https://android.googlesource.com/device/google/cuttlefish/+/refs/heads/main/host/commands/cvd/parser/README.md
func (a *Actuator) buildCreateRequest(ctx context.Context, d *device.Device, groupName string) (*hoclient.CreateCVDRequest, error) {
	instanceConfig := simpleInstanceConfig(d)

	// diskConfig/bootConfig hold resolved build references keyed by canonical
	// env_config field (instances[].disk.* and instances[].boot.* respectively);
	// they are merged into advanced-mode env_config. hostPackageURL is set at
	// common level.
	diskConfig := map[string]any{}
	bootConfig := map[string]any{}
	hostPackageURL, err := a.resolveBuildArtifacts(ctx, d, diskConfig, bootConfig)
	if err != nil {
		return nil, err
	}

	// Apply the resolved sections to the instance config (used by simple mode).
	if len(diskConfig) > 0 {
		instanceConfig["disk"] = diskConfig
	}
	if len(bootConfig) > 0 {
		instanceConfig["boot"] = bootConfig
	}

	// Each instance's "@build" fills that instance only, ahead of the
	// device-level build. The copy keeps d.EnvConfig as stored.
	source := d
	if len(d.EnvConfig) > 0 {
		resolved, err := a.resolveInstanceBuilds(ctx, d)
		if err != nil {
			return nil, err
		}
		if resolved != nil {
			source = withInstanceBuilds(d, resolved, hostPackageURL)
		}
	}

	envConfig := buildEnvConfig(source, groupName, instanceConfig, hostPackageURL, diskConfig, bootConfig)
	if len(d.EnvConfig) > 0 {
		a.logger.Info("using advanced mode env_config",
			slog.String("device_id", d.ID),
			slog.String("group_name", groupName),
			slog.Int("instance_count", d.InstanceCount),
		)
	}
	return &hoclient.CreateCVDRequest{EnvConfig: envConfig}, nil
}

// resumeCVDLoad checks the journal for a load operation a prior attempt
// started and, when found, waits on it. The first result reports whether a
// resume happened at all (false = no journal, no record, so run the normal
// ladder). On a resume, the second result reports whether the create was
// carried to completion (its error is the outcome); false means the resumed
// operation failed terminally, the journey has been cleared, and the caller
// falls through to the normal create ladder.
func (a *Actuator) resumeCVDLoad(
	ctx context.Context,
	d *device.Device,
	client hoclient.Client,
	groupName string,
	t *task.Task,
) (bool, bool, error) {
	if a.journal == nil || t == nil {
		return false, false, nil
	}
	opName, err := a.journal.RecordedOperation(ctx, t, stepCVDLoad)
	if err != nil {
		// A journal read failure must not fail the create; the adopt guard
		// below covers the redelivery semantics.
		a.logger.Warn("failed to read CVD load journal",
			slog.String("device_id", d.ID), slog.Any("error", err))
		return false, false, nil
	}
	if opName == "" {
		return false, false, nil
	}

	a.logger.Info("resuming CVD load operation from a prior attempt",
		slog.String("device_id", d.ID), slog.String("operation", opName))
	resp, stand, waitErr := a.awaitCVDCreate(ctx, client, opName, d, groupName)
	switch {
	case stand == standDownDeleted:
		// The device was deleted while resuming: bail cleanly (the create is
		// "done" as far as this journey is concerned); the cleanup task tears
		// down whatever the resumed launch left.
		a.logger.Info("device deleted during resumed CVD load; abandoning",
			slog.String("device_id", d.ID), slog.String("operation", opName))
		return true, true, nil
	case stand == standDownSettled:
		// The boot verdict concluded the journey mid-resume: the row is
		// error, the evidence is on it, the group stays live for diagnosis.
		a.logger.Info("boot verdict settled device during resumed CVD load; abandoning",
			slog.String("device_id", d.ID), slog.String("operation", opName))
		return true, true, nil
	case waitErr == nil:
		return true, true, a.updateDeviceAfterCVDCreate(ctx, d, resp, client, groupName)
	case hoclient.IsTerminalOperationError(waitErr):
		// The operation itself settled as failed (or the HO restarted and
		// lost it). Clear the journey so the next pass re-runs the load from
		// the top instead of re-reading the same stored failure to
		// exhaustion; a failed clear just repeats this branch next attempt.
		a.logger.Info("resumed CVD load settled as failed; restarting journey",
			slog.String("device_id", d.ID), slog.String("operation", opName), slog.Any("error", waitErr))
		if clearErr := a.journal.ClearOperations(ctx, t); clearErr != nil {
			return true, true, fmt.Errorf("clearing journal after failed load %s: %w", opName, clearErr)
		}
		return true, false, nil
	default:
		// Transient (unreachable, MaxWait): retry resumes the same operation.
		return true, true, fmt.Errorf("resuming CVD load %s: %w", opName, waitErr)
	}
}

// errDeviceGoneDuringCreate signals that the device row was deleted while its
// CVD was being created on the host.
var errDeviceGoneDuringCreate = errors.New("device deleted during CVD creation")

// recordArtifactCache upserts the affinity-ledger row once a create has left
// the group's artifacts on the host. Pinned keys only (pinnedArtifactKey),
// and best-effort throughout: the ledger is advisory, so a failed write
// costs a warm pick, never the create.
func (a *Actuator) recordArtifactCache(ctx context.Context, d *device.Device) {
	if a.artifacts == nil {
		return
	}
	key, ok := a.pinnedArtifactKey(ctx, d)
	if !ok {
		return
	}
	h, err := a.hosts.Get(ctx, d.HostID)
	if err != nil {
		a.logger.Warn("artifact-cache ledger skipped: host read failed",
			slog.String("device_id", d.ID), slog.String("host_id", d.HostID), slog.Any("error", err))
		return
	}
	if err := a.artifacts.Record(ctx, d.HostID, key, d.BuildID, h.Incarnation); err != nil {
		a.logger.Warn("failed to record artifact-cache ledger row",
			slog.String("host_id", d.HostID), slog.String("fetch_key", key), slog.Any("error", err))
	}
}

// adbPortBase is the host-side ADB port cvd gives instance slot 1; every
// further slot takes the next port up, which is what makes the slot
// recoverable from the serial the host published.
const adbPortBase = 6520

// instancesFromCVDs is the host's own report of a CVD group as the device's
// observed instance array, in the order the host listed it. The WebRTC device
// id is copied rather than derived: the host appends a fleet-unique instance
// id to "<group>-<name>", and only the host knows it.
//
// This is the one place the slot is established. Everything downstream carries
// the stored value forward, so an instance the host described without a serial
// keeps slot zero, meaning unknown, instead of an arithmetic result on a port
// that was never reported.
func instancesFromCVDs(cvds []*hoclient.CVD) []device.Instance {
	instances := make([]device.Instance, len(cvds))
	for i, c := range cvds {
		port := adbPortFromSerial(c.ADBSerial)
		instances[i] = device.Instance{
			Name:           c.Name,
			State:          c.Status,
			WebRTCDeviceID: c.WebRTCDeviceID,
			ADBPort:        port,
			Displays:       c.Displays,
		}
		if port >= adbPortBase {
			instances[i].Slot = port - adbPortBase + 1
		}
	}
	return instances
}

// startCreatedGroup brings a created group the rest of the way up when the
// host did not report every instance running, and returns the failure that
// leaves the create for the worker to retry.
//
// A group whose worst instance failed outright is never startable: cvd resets
// the whole group when one instance fails to boot, so whatever did come up is
// debris. It takes the husk path directly rather than paying a `:start` that
// cannot succeed.
func (a *Actuator) startCreatedGroup(ctx context.Context, d *device.Device, client hoclient.Client, group, groupStatus string) error {
	var failure error
	switch groupStatus {
	case device.CVDStateBootFailed, device.CVDStatePrepareFailed, device.CVDStateCancelled:
		failure = fmt.Errorf("CVD group %s did not boot: %s", group, groupStatus)
	default:
		// The GROUP verb, not the instance one against the first member: the
		// host serves them as separate handlers, and starting instance 0 of a
		// stopped group leaves every sibling stopped. The fold then settles
		// the whole device stopped and the create looks like a failure
		// nobody caused.
		if err := client.StartCVDGroup(ctx, group); err != nil {
			failure = fmt.Errorf("starting CVD group after creation: %w", err)
		}
	}
	if failure == nil {
		return nil
	}

	// A group that cannot start is usually a husk from an interrupted create
	// (e.g. a failed artifact fetch leaves host_tools empty) and will never
	// become startable. Delete it so the next attempt creates the group from
	// scratch instead of re-adopting the husk until exhaustion. A transiently
	// failing start pays one re-fetch; a poisoned group converges. Best
	// effort: if the delete fails too, the next attempt repeats it.
	//
	// INVARIANT: only quiescent groups reach this delete, because the adopt
	// path postpones on Starting/Stopping. `cvd rm` against a live launch
	// leaks its processes (holding tap devices and the operator registration)
	// with no group record left to find them.
	if delErr := client.DeleteCVD(ctx, group); delErr != nil {
		a.logger.Warn("failed to delete unstartable CVD group",
			slog.String("device_id", d.ID),
			slog.String("group_name", group),
			slog.Any("error", delErr))
	}
	// Surface the error to the worker: it retries. The device must stay
	// `creating`. Setting `error` here would make RunCreateCVD's
	// already-handled guard complete the retry as a no-op and strand the
	// device. Only exhaustion (markCreateFailed) records error.
	return failure
}

// updateDeviceAfterCVDCreate commits device state after successful CVD creation.
//
// The commit re-reads the row transactionally. A device deleted mid-create is
// never resurrected. The freshly created CVD is torn down instead, because
// the delete's cleanup task was enqueued before the CVD ID existed.
func (a *Actuator) updateDeviceAfterCVDCreate(
	ctx context.Context,
	d *device.Device,
	resp *hoclient.CreateCVDResponse,
	client hoclient.Client,
	groupName string,
) error {
	cvdGroup, cvdName := groupName, d.Name
	if len(resp.CVDs) > 0 {
		cvdGroup, cvdName = resp.CVDs[0].Group, resp.CVDs[0].Name
	}
	// Every instance the host reported, in the order it listed them. The flat
	// columns below stay instance 0's: that one is the device's own binding,
	// and the only instance a single-instance caller ever means.
	instances := instancesFromCVDs(resp.CVDs)
	adbPort := int64(0)
	if len(instances) > 0 {
		adbPort = instances[0].ADBPort
	}
	// The group stands for its worst instance: cvd resets the whole group when
	// one instance fails to boot, so a partly-up group is a dead group rather
	// than a degraded one, and a device settles running only unanimously.
	groupStatus := device.FoldInstanceStates(instances)
	cvdID := fmt.Sprintf("%s/%s", cvdGroup, cvdName)
	a.logger.Info("CVD created",
		slog.String("device_id", d.ID),
		slog.String("cvd_id", cvdID),
		slog.String("status", groupStatus),
		slog.Int("instances", len(instances)),
	)

	if groupStatus != device.CVDStateRunning {
		if err := a.startCreatedGroup(ctx, d, client, cvdGroup, groupStatus); err != nil {
			return err
		}
	}

	// The group is real past the start hurdle, so its artifacts are on the
	// host's disk. That fact outlives even a deleted-mid-create device
	// (`cvd rm` keeps the download cache by construction), which is why the
	// ledger write precedes the row commit.
	a.recordArtifactCache(ctx, d)

	_, err := a.devices.Mutate(ctx, d.ID, func(cur *device.Device) error {
		if cur.Status == device.StatusDeleted || cur.Status == device.StatusDeleting {
			return errDeviceGoneDuringCreate
		}
		cur.CVDID = cvdID
		cur.Instances = instances
		// The host assigns the ADB port per instance slot (6520+N); report what
		// HO actually said (0 = unknown) instead of asserting slot 0's port.
		cur.ADBPort = adbPort
		// The operator serves every instance's stream from 1080 on the host image.
		cur.WebRTCPort = 1080
		// Leaving the transient clears the boot observation (TransientSince's
		// contract): a stale "booting" line must not outlive the boot. The
		// transition trail is the one exception. It records history, and it is
		// the wait's only durable record of its fetch/boot split, which exists
		// for the creates that SUCCEED.
		if cur.BootObservation != nil && len(cur.BootObservation.Transitions) > 0 {
			cur.BootObservation = &device.BootObservation{Transitions: cur.BootObservation.Transitions}
		} else {
			cur.BootObservation = nil
		}
		// A fresh CVD's health is not yet verified: reset to unknown so a stale
		// zombie carried in from a prior (recreated) incarnation cannot linger on
		// the now-running row and draw a spurious extra recreate before the next
		// health check re-establishes the real status.
		cur.HealthStatus = device.HealthStatusUnknown
		cur.ConsecutiveFails = 0
		if err := cur.TransitionTo(device.StatusRunning); err != nil {
			return err
		}
		now := time.Now()
		cur.StartedAt = &now
		return nil
	})
	if errors.Is(err, errDeviceGoneDuringCreate) {
		// Delete the whole group rather than this instance alone. The delete's
		// cleanup task was enqueued before any of the group existed.
		a.logger.Info("device deleted during CVD creation; tearing down CVD group",
			slog.String("device_id", d.ID), slog.String("group_name", cvdGroup))
		if derr := client.DeleteCVD(ctx, cvdGroup); derr != nil {
			return fmt.Errorf("cleaning up CVD group %s for deleted device: %w", cvdGroup, derr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("updating device after CVD creation: %w", err)
	}
	// A clean create clears any accumulated host-poison suspicion: the
	// consecutive-failure counter resets because the host just served a create.
	a.clearHostPoison(ctx, d.HostID)
	return nil
}
