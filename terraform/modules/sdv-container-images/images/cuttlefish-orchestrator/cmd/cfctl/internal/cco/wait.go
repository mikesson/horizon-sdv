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

package cco

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"
)

// Wait targets. Deleted is also satisfied by a 404: the record may be purged.
const (
	StatusRunning = "running"
	StatusStopped = "stopped"
	StatusDeleted = "deleted"
)

// StatusDeleting is the status of a device or host while its teardown runs.
// It is never a wait target.
const StatusDeleting = "deleting"

// statusError is a failure state on every wait, never a wait target.
const statusError = "error"

const defaultPollInterval = 5 * time.Second

// maxConsecutivePollFailures bounds how many failed polls in a row a wait
// tolerates. With the 5s interval and the 30s request cap, a wait survives a
// Cloud Run rollout but gives up on a dead backend within about 3 minutes.
const maxConsecutivePollFailures = 5

// Failure states per wait target. Stopped is no failure when waiting for
// running because `host start` begins there. Only the command context bounds
// a wait.
var (
	hostFailureStates = map[string][]string{
		StatusRunning: {statusError, "unreachable", "preempted", StatusDeleted},
		StatusStopped: {statusError, StatusDeleted},
		StatusDeleted: {statusError},
	}
	deviceFailureStates = map[string][]string{
		StatusRunning: {statusError, StatusDeleted},
		StatusStopped: {statusError, StatusDeleted},
		StatusDeleted: {statusError},
	}
)

// WaitFailedError reports a wait that ended in a failure state, or a
// resource that went back to stopped while a start was awaited.
type WaitFailedError struct {
	State     string
	Target    string
	Detail    string
	Regressed bool
}

func (e *WaitFailedError) Error() string {
	if e.Regressed {
		return fmt.Sprintf("went back to %q while waiting for %q", e.State, e.Target)
	}
	msg := fmt.Sprintf("reached %q while waiting for %q", e.State, e.Target)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// WaitHost polls until the host reaches target, hits a failure state, or ctx
// ends. Waiting for StatusDeleted returns (nil, nil) once the host is gone.
func (c *Client) WaitHost(ctx context.Context, id, target string) (*Host, error) {
	return waitFor(ctx, c.pollInterval, target, hostFailureStates[target],
		func(ctx context.Context) (*Host, error) { return c.GetHost(ctx, id) },
		func(h *Host) (string, string) { return h.Status, h.ErrorMessage })
}

// WaitDevice polls until the device reaches target, hits a failure state, or
// ctx ends. Waiting for StatusDeleted returns (nil, nil) once the device is gone.
func (c *Client) WaitDevice(ctx context.Context, id, target string) (*Device, error) {
	return waitFor(ctx, c.pollInterval, target, deviceFailureStates[target],
		func(ctx context.Context) (*Device, error) { return c.GetDevice(ctx, id) },
		func(d *Device) (string, string) { return d.Status, d.ErrorMessage })
}

// Batch statuses (derived server-side).
const (
	BatchStatusProvisioning = "provisioning"
	BatchStatusReady        = "ready"
	BatchStatusFailed       = "failed"
)

// WaitBatch polls until the batch leaves "provisioning" or ctx ends, and
// returns the settled batch. Degraded and failed batches are returned too, for
// the caller to report.
func (c *Client) WaitBatch(ctx context.Context, id string) (*BatchSummary, error) {
	interval := c.pollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	pollFailures := 0
	for {
		b, err := c.GetBatch(ctx, id)
		switch {
		case err != nil:
			if stop, out := classifyPollError(err, BatchStatusReady, &pollFailures); stop {
				return nil, out
			}
		case b.Status != BatchStatusProvisioning:
			return b, nil
		default:
			pollFailures = 0
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for batch %s to settle: %w", id, context.Cause(ctx))
		case <-ticker.C:
		}
	}
}

// classifyPollError reports whether a failed poll ends the wait. A 404 on a
// deleted-target wait is success, other 4xx errors are fatal, and transient
// errors keep polling up to maxConsecutivePollFailures.
func classifyPollError(err error, target string, consecutive *int) (bool, error) {
	if apiErr, ok := errors.AsType[*Error](err); ok {
		if target == StatusDeleted && apiErr.Status == http.StatusNotFound {
			return true, nil
		}
		// A 429 means the server is alive, so it does not count toward
		// the failure bound.
		if apiErr.Status == http.StatusTooManyRequests {
			return false, nil
		}
		if apiErr.Status < http.StatusInternalServerError {
			return true, err
		}
	}
	*consecutive++
	if *consecutive >= maxConsecutivePollFailures {
		return true, fmt.Errorf("%d consecutive poll failures while waiting for %q: %w", *consecutive, target, err)
	}
	return false, nil
}

// waitFor polls get immediately and then on each tick until the status that
// probe reports reaches target or a failure state. probe is a function because
// the generated model types cannot carry methods.
func waitFor[T any](ctx context.Context, interval time.Duration, target string, failures []string, get func(context.Context) (T, error), probe func(T) (status, detail string)) (T, error) {
	var zero T
	if interval <= 0 {
		interval = defaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	pollFailures := 0
	sawProgress := false
	for {
		v, err := get(ctx)
		var vStatus, vDetail string
		if err == nil {
			vStatus, vDetail = probe(v)
		}
		switch {
		case err != nil:
			if stop, out := classifyPollError(err, target, &pollFailures); stop {
				return zero, out
			}
		case vStatus == target:
			return v, nil
		case slices.Contains(failures, vStatus):
			return zero, &WaitFailedError{State: vStatus, Target: target, Detail: vDetail}
		case target == StatusRunning && vStatus == StatusStopped && sawProgress:
			// Another actor stopped the resource after the start began. The
			// default --timeout of 0 would otherwise poll forever.
			return zero, &WaitFailedError{State: StatusStopped, Target: target, Regressed: true}
		default:
			pollFailures = 0
			if vStatus != StatusStopped {
				sawProgress = true
			}
		}

		select {
		case <-ctx.Done():
			return zero, fmt.Errorf("waiting for %q: %w", target, context.Cause(ctx))
		case <-ticker.C:
		}
	}
}

// WaitBatchDeleted polls until the server returns 404 for the batch, which
// it does once every member is torn down. An already-gone batch returns
// immediately.
func (c *Client) WaitBatchDeleted(ctx context.Context, id string) error {
	interval := c.pollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	pollFailures := 0
	for {
		_, err := c.GetBatch(ctx, id)
		if err != nil {
			if stop, out := classifyPollError(err, StatusDeleted, &pollFailures); stop {
				return out
			}
		} else {
			pollFailures = 0
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for batch %s to be deleted: %w", id, context.Cause(ctx))
		case <-ticker.C:
		}
	}
}
