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

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
)

const (
	// defaultWaitOnline bounds the wait for adb to report a device online
	// when no --wait-online flag sets it.
	defaultWaitOnline = 60 * time.Second

	// adbStatePoll is how often the wait asks adb for the device's state.
	adbStatePoll = 250 * time.Millisecond

	// adbOnline is the state 'adb get-state' reports for a usable device.
	adbOnline = "device"
)

// adbOfflineError reports that adb did not see the device online within the
// wait.
type adbOfflineError struct {
	address string
	limit   time.Duration
	// state is the last answer of 'adb get-state'.
	state string
	// cause is the last failed tunnel session, or nil.
	cause error
}

func (e *adbOfflineError) Error() string {
	msg := fmt.Sprintf("adb did not see %s online within %s (last adb state: %s", e.address, e.limit, e.state)
	if e.cause != nil {
		msg += "; last tunnel session: " + e.cause.Error()
	}
	return msg + ")"
}

// waitADBOnline polls 'adb -s address get-state' until adb reports the device
// online, for at most limit. A limit of 0 skips the wait. sessions is nil when
// the tunnel runs in another process. Otherwise a failed stream dial ends the
// wait at once with its error, and the last failed session is named on
// timeout.
func waitADBOnline(ctx context.Context, adb adbRunner, address string, limit time.Duration, sessions *onlineSessions) error {
	if limit <= 0 {
		return nil
	}
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(adbStatePoll)
	defer tick.Stop()
	var dialFailed <-chan struct{}
	if sessions != nil {
		dialFailed = sessions.dialFailed
	}

	state := "no answer"
	for {
		answer, err := adbState(ctx, adb, address)
		if errors.Is(err, exec.ErrNotFound) {
			return err
		}
		if err == nil && answer == adbOnline {
			return nil
		}
		// Any other failed run is a state to wait out, such as a device adb
		// does not list yet.
		if answer != "" {
			state = answer
		}
		select {
		case <-dialFailed:
			return sessions.dialError()
		case <-deadline.C:
			return &adbOfflineError{address: address, limit: limit, state: state, cause: sessions.lastFailure()}
		case <-ctx.Done():
			return fmt.Errorf("waiting for adb to see %s online: %w", address, ctx.Err())
		case <-tick.C:
		}
	}
}

// adbState returns adb's state for the device at address, and the error of a
// failed run. adb prints the state on stdout and exits 0 for a device it
// knows. For a device it does not know, and in some versions for one that is
// offline or unauthorized, it prints an error on stderr and exits 1; the last
// line of that error stands in for the state.
func adbState(ctx context.Context, adb adbRunner, address string) (string, error) {
	var stdout, stderr bytes.Buffer
	if err := adb(ctx, &stdout, &stderr, "-s", address, "get-state"); err != nil {
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
			return last, err
		}
		return err.Error(), err
	}
	return strings.TrimSpace(stdout.String()), nil
}

// onlineSessions is the foreground tunnel's tunnelSessions. It tells the wait
// for an online device how the tunnel's sessions ended. An attaching agent
// feeds one through record.
type onlineSessions struct {
	foregroundSessions

	// dialFailed is closed by the first session whose stream dial failed.
	dialFailed chan struct{}

	mu      sync.Mutex
	dialErr error
	last    error
}

func newOnlineSessions() *onlineSessions {
	return &onlineSessions{dialFailed: make(chan struct{})}
}

func (s *onlineSessions) ended(ctx context.Context, err error) {
	s.foregroundSessions.ended(ctx, err)
	s.record(err)
}

// record notes one ended session for the wait.
func (s *onlineSessions) record(err error) {
	// A stream that carried data and then closed is no failure.
	if err == nil || errors.Is(err, errRemoteEnded) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = err
	if errors.Is(err, errStreamDial) && s.dialErr == nil {
		s.dialErr = err
		close(s.dialFailed)
	}
}

// dialError returns the first failed stream dial.
func (s *onlineSessions) dialError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dialErr
}

// lastFailure returns the last failed session, or nil. It accepts a nil
// receiver for a tunnel in another process.
func (s *onlineSessions) lastFailure() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// offlineExit names the device and instance in a failed wait. When adb did
// not see the device online in time, it reads the device again: a running
// device exits 7, because adbd may still be starting, and any other status
// exits 5.
func offlineExit(ctx context.Context, getDevice func(ctx context.Context, id string) (*cco.Device, error), deviceID, instance string, err error) error {
	if _, ok := errors.AsType[*adbOfflineError](err); !ok {
		return fmt.Errorf("device %s instance %s: %w", deviceID, instance, err)
	}
	d, gerr := getDevice(ctx, deviceID)
	if gerr != nil {
		return errors.Join(fmt.Errorf("device %s instance %s: %w", deviceID, instance, err),
			fmt.Errorf("reading device %s again: %w", deviceID, gerr))
	}
	code := ExitConflict
	if d.Status == cco.StatusRunning {
		code = ExitTransient
	}
	return &exitError{err: fmt.Errorf("device %s instance %s (status %s): %w", deviceID, instance, d.Status, err), code: code}
}
