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
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
)

const testADBAddress = "127.0.0.1:5555"

// readDeviceWithStatus plays a device read that returns dev-1 with status.
func readDeviceWithStatus(status string) func(context.Context, string) (*cco.Device, error) {
	return func(_ context.Context, id string) (*cco.Device, error) {
		return &cco.Device{ID: id, Status: status}, nil
	}
}

// noDeviceRead fails the test if the wait reads the device.
func noDeviceRead(t *testing.T) func(context.Context, string) (*cco.Device, error) {
	t.Helper()
	return func(context.Context, string) (*cco.Device, error) {
		t.Error("the device was read again although the wait did not time out")
		return nil, errors.New("unexpected device read")
	}
}

func TestWaitADBOnlineReturnsOnceTheDeviceIsOnline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		adb := &recordingADB{states: []string{"offline", "offline", adbOnline}}
		start := time.Now()
		if err := waitADBOnline(t.Context(), adb.run, testADBAddress, time.Minute, newOnlineSessions()); err != nil {
			t.Fatalf("waitADBOnline() error = %v, want nil once adb answers %q", err, adbOnline)
		}
		if got := len(adb.got()); got != 3 {
			t.Errorf("adb calls = %v, want three get-state calls", adb.got())
		}
		if elapsed := time.Since(start); elapsed != 2*adbStatePoll {
			t.Errorf("the wait took %s, want two poll intervals (%s)", elapsed, 2*adbStatePoll)
		}
	})
}

func TestWaitADBOnlineZeroSkipsTheWait(t *testing.T) {
	adb := &recordingADB{states: []string{"offline"}}
	if err := waitADBOnline(t.Context(), adb.run, testADBAddress, 0, nil); err != nil {
		t.Errorf("waitADBOnline() error = %v, want nil with a zero limit", err)
	}
	if got := adb.got(); len(got) != 0 {
		t.Errorf("adb calls = %v, want none with a zero limit", got)
	}
}

// A device that stays offline past the limit exits 7 while it is running,
// because adbd may still be starting, and 5 otherwise.
func TestWaitADBOnlineTimeoutExitCode(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   int
	}{
		{cco.StatusRunning, ExitTransient},
		{cco.StatusStopped, ExitConflict},
	} {
		t.Run(tc.status, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				adb := &recordingADB{states: []string{"offline"}}
				limit := 5 * time.Second
				start := time.Now()
				err := waitADBOnline(t.Context(), adb.run, testADBAddress, limit, newOnlineSessions())
				if elapsed := time.Since(start); elapsed < limit {
					t.Errorf("the wait ended after %s, before the %s limit", elapsed, limit)
				}
				if _, ok := errors.AsType[*adbOfflineError](err); !ok {
					t.Fatalf("waitADBOnline() error = %v, want an *adbOfflineError", err)
				}

				err = offlineExit(t.Context(), readDeviceWithStatus(tc.status), "dev-1", "1", err)
				if got := exitCodeFor(err, false); got != tc.want {
					t.Errorf("exit code = %d, want %d (error %v)", got, tc.want, err)
				}
				for _, want := range []string{"dev-1", "instance 1", "status " + tc.status, testADBAddress, "last adb state: offline"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention %q", err, want)
					}
				}
			})
		})
	}
}

// A stream dial that fails (403, 404, 502) cannot succeed on a retry within
// the wait, so it ends the wait at once and its status picks the exit code.
func TestWaitADBOnlineEndsOnAFailedStreamDial(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		adb := &recordingADB{states: []string{"offline"}}
		sessions := newOnlineSessions()
		go func() {
			time.Sleep(time.Second)
			sessions.ended(t.Context(), streamDialFailed("dev-1", &cco.Error{Status: http.StatusForbidden, Message: "denied by IAP"}))
		}()

		start := time.Now()
		err := waitADBOnline(t.Context(), adb.run, testADBAddress, time.Minute, sessions)
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Errorf("the wait ended after %s, want 1s, when the dial failed", elapsed)
		}
		if !errors.Is(err, errStreamDial) {
			t.Fatalf("waitADBOnline() error = %v, want the failed dial", err)
		}

		err = offlineExit(t.Context(), noDeviceRead(t), "dev-1", "1", err)
		if got := exitCodeFor(err, false); got != ExitAuth {
			t.Errorf("exit code = %d, want %d for a 403 (error %v)", got, ExitAuth, err)
		}
		if !strings.Contains(err.Error(), "denied by IAP") {
			t.Errorf("error %q does not carry the API's message", err)
		}
	})
}

// A silent stream may come from an adbd that is still starting, so it does
// not end the wait. It is named as the cause when the wait times out.
func TestWaitADBOnlineKeepsWaitingAfterASilentStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		adb := &recordingADB{states: []string{"offline"}}
		sessions := newOnlineSessions()
		go func() {
			time.Sleep(time.Second)
			sessions.ended(t.Context(), fmt.Errorf("%w for dev-1", errStreamSilent))
		}()

		limit := 5 * time.Second
		start := time.Now()
		err := waitADBOnline(t.Context(), adb.run, testADBAddress, limit, sessions)
		if elapsed := time.Since(start); elapsed < limit {
			t.Errorf("the wait ended after %s, before the %s limit", elapsed, limit)
		}
		offline, ok := errors.AsType[*adbOfflineError](err)
		if !ok {
			t.Fatalf("waitADBOnline() error = %v, want an *adbOfflineError", err)
		}
		if !errors.Is(offline.cause, errStreamSilent) {
			t.Errorf("cause = %v, want the silent stream", offline.cause)
		}
		if !strings.Contains(err.Error(), errStreamSilent.Error()) {
			t.Errorf("error %q does not name the silent stream", err)
		}
	})
}

// adb exits 1 with the reason on stderr for a device it does not know.
func TestADBStateReadsStderrOnFailure(t *testing.T) {
	adb := func(_ context.Context, _, stderr io.Writer, _ ...string) error {
		fmt.Fprintln(stderr, "* daemon started successfully")
		fmt.Fprintf(stderr, "error: device '%s' not found\n", testADBAddress)
		return errors.New("adb -s failed: exit status 1")
	}
	got, err := adbState(t.Context(), adb, testADBAddress)
	if err == nil {
		t.Error("adbState() error = nil, want the failed run")
	}
	if want := fmt.Sprintf("error: device '%s' not found", testADBAddress); got != want {
		t.Errorf("adbState() = %q, want %q", got, want)
	}
}
