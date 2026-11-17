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
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
)

// lossyADB plays a local adb server that can drop the agent's address. While
// lost, get-state answers the way adb does for an address it does not know.
// A connect makes the address known again.
type lossyADB struct {
	mu    sync.Mutex
	calls []string
	lost  bool
}

func (a *lossyADB) run(_ context.Context, stdout, stderr io.Writer, args ...string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, strings.Join(args, " "))
	switch {
	case slices.Contains(args, "get-state"):
		if a.lost {
			fmt.Fprintf(stderr, "error: device '%s' not found\n", args[1])
			return errors.New("adb get-state failed: exit status 1")
		}
		fmt.Fprintln(stdout, adbOnline)
	case args[0] == "connect":
		a.lost = false
	}
	return nil
}

func (a *lossyADB) lose() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lost = true
}

// connects counts the 'adb connect' runs for address.
func (a *lossyADB) connects(address string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, c := range a.calls {
		if c == "connect "+address {
			n++
		}
	}
	return n
}

// attachmentHarness drives agentAttachment.check with a scripted adb and
// device lookup.
func attachmentHarness(adb adbRunner, lookup func() (*cco.Device, error)) (*agentAttachment, *sessionsHarness) {
	h := newSessionsHarness(lookup)
	return &agentAttachment{
		adb: adb, adbLog: io.Discard, every: time.Hour, sessions: h.sessions,
		write: func(connectionState) error { return nil },
	}, h
}

func TestAgentAttachmentConnectsAgainWhenADBDroppedALiveDevice(t *testing.T) {
	adb := &lossyADB{lost: true}
	a, h := attachmentHarness(adb.run, deviceWithStatus(cco.StatusRunning))
	st := connectionState{Address: "127.0.0.1:41001", Attach: true, AttachDone: true}

	got := a.check(t.Context(), st)
	if adb.connects(st.Address) != 1 || got.Reattaches != 1 {
		t.Errorf("connects %d reattaches %d, want one of each", adb.connects(st.Address), got.Reattaches)
	}
	if !got.Online || got.ADBState != adbOnline {
		t.Errorf("state = %+v, want online after the connect", got)
	}
	if h.lookups != 1 || h.stops != 0 {
		t.Errorf("lookups %d stops %d, want one liveness check and no stop", h.lookups, h.stops)
	}
}

func TestAgentAttachmentStopsWhenADBDroppedAGoneDevice(t *testing.T) {
	adb := &lossyADB{lost: true}
	a, h := attachmentHarness(adb.run, deviceWithStatus(cco.StatusDeleting))
	st := connectionState{Address: "127.0.0.1:41001", Attach: true, AttachDone: true}

	if got := a.check(t.Context(), st); got.Reattaches != 0 {
		t.Errorf("reattaches = %d, want none", got.Reattaches)
	}
	if adb.connects(st.Address) != 0 || h.stops != 1 {
		t.Errorf("connects %d stops %d, want no connect and one stop", adb.connects(st.Address), h.stops)
	}
}

// adb connect exits 0 when no handshake arrives, so a connect during an
// outage leaves the address unknown and is no reattach.
func TestAgentAttachmentCountsNoReattachWhileADBStillLacksTheAddress(t *testing.T) {
	adb := func(_ context.Context, stdout, stderr io.Writer, args ...string) error {
		if args[0] == "connect" {
			fmt.Fprintf(stdout, "failed to connect to '%s'\n", args[1])
			return nil
		}
		fmt.Fprintf(stderr, "error: device '%s' not found\n", args[1])
		return errors.New("adb get-state failed: exit status 1")
	}
	a, h := attachmentHarness(adb, deviceWithStatus(cco.StatusRunning))
	st := connectionState{Address: "127.0.0.1:41001", Attach: true, AttachDone: true}

	for range 3 {
		st = a.check(t.Context(), st)
	}
	if st.Reattaches != 0 || st.ADBState != adbStateNotFound || st.Online {
		t.Errorf("state %+v, want no reattach and the address still not found", st)
	}
	failed := 0
	for _, line := range h.lines {
		if strings.Contains(line, "failed to connect") {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("log %q has %d 'failed to connect' lines, want one, with its repeats counted", h.lines, failed)
	}
}

// Without the failed dial reaching the wait, the attach runs until the test's
// 5s context ends and records no attach error.
func TestAgentAttachmentEndsTheOnlineWaitAtAFailedDial(t *testing.T) {
	var a *agentAttachment
	dialed := false
	adb := func(ctx context.Context, stdout, _ io.Writer, args ...string) error {
		if slices.Contains(args, "get-state") && !dialed {
			dialed = true
			a.sessions.ended(ctx, dialFailure(http.StatusForbidden))
		}
		fmt.Fprintln(stdout, "offline")
		return nil
	}
	a, _ = attachmentHarness(adb, deviceWithStatus(cco.StatusRunning))
	a.sessions.watch = newOnlineSessions()
	a.waitOnline = time.Minute

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	got := a.attach(ctx, connectionState{Address: "127.0.0.1:41001", Attach: true})
	if ctx.Err() != nil {
		t.Fatalf("the attach ran until the context ended; state %+v", got)
	}
	if !strings.Contains(got.AttachError, "403") || got.AttachExit != ExitAuth || got.Online {
		t.Errorf("state %+v, want the 403 dial as the attach error with exit %d", got, ExitAuth)
	}
}

func TestAgentAttachmentLeavesAKnownAddressAlone(t *testing.T) {
	for _, state := range []string{adbOnline, "offline", "unauthorized"} {
		adb := func(_ context.Context, stdout, _ io.Writer, args ...string) error {
			if args[0] == "connect" {
				t.Errorf("%s: adb connect ran for an address adb knows", state)
			}
			fmt.Fprintln(stdout, state)
			return nil
		}
		a, h := attachmentHarness(adb, deviceWithStatus(cco.StatusRunning))
		got := a.check(t.Context(), connectionState{Address: "127.0.0.1:41001", Attach: true, AttachDone: true})
		if got.ADBState != state || got.Online != (state == adbOnline) || h.lookups != 0 {
			t.Errorf("%s: state %+v lookups %d, want the state recorded and no lookup", state, got, h.lookups)
		}
	}
}

// awaitAgent polls the agent's state file until ok accepts it.
func awaitAgent(t *testing.T, store connectionStore, done chan error, what string, ok func(connectionState) bool) connectionState {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		st, live, err := store.lookup("dev-1", "1")
		if err != nil {
			t.Fatalf("lookup() error = %v", err)
		}
		if live && ok(st) {
			return st
		}
		select {
		case err := <-done:
			t.Fatalf("the agent exited before %s: %v", what, err)
		case <-deadline:
			t.Fatalf("no %s within 10s; last state %+v", what, st)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// closingStreamClient talks to a backend where device dev-1 is running and
// each ADB stream echoes one message, then stays open until endStream runs.
func closingStreamClient(t *testing.T) (client *cco.Client, endStream func()) {
	t.Helper()
	closeStream := make(chan struct{})
	client = agentClientFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/devices/dev-1":
			fmt.Fprint(w, `{"id":"dev-1","name":"phone","host_id":"h1","status":"running","instances":[{"name":"1"}]}`)
		case strings.HasSuffix(r.URL.Path, "/adb"):
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer c.CloseNow()
			typ, data, err := c.Read(r.Context())
			if err != nil || c.Write(r.Context(), typ, data) != nil {
				return
			}
			<-closeStream
		default:
			http.NotFound(w, r)
		}
	}))
	// The server's Close waits for its handlers, so a failed test must still
	// release the stream.
	endStream = sync.OnceFunc(func() { close(closeStream) })
	t.Cleanup(endStream)
	return client, endStream
}

// exchangeOverAgent sends one message through the agent at address and reads
// its echo, so the session carries data.
func exchangeOverAgent(t *testing.T, ctx context.Context, address string) {
	t.Helper()
	var dialer net.Dialer
	local, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		t.Fatalf("dialing the agent at %s: %v", address, err)
	}
	t.Cleanup(func() { _ = local.Close() })
	if _, err := local.Write([]byte("CNXN")); err != nil {
		t.Fatal(err)
	}
	if err := local.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(local, make([]byte, 4)); err != nil {
		t.Fatalf("reading the echo: %v", err)
	}
}

// The supervision tick is 15s, and the test waits at most 10s for the
// reattach, so only the check after the remote close can pass it.
func TestADBAgentConnectsAgainAfterARemoteCloseLosesTheDevice(t *testing.T) {
	m, _, _ := newTestManager(t)
	adb := &lossyADB{}
	m.adb = adb.run
	useTestManager(t, m)
	client, endStream := closingStreamClient(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		cmd := &DeviceADBAgentCmd{ID: "dev-1", Instance: "1", Attach: true, WaitOnline: 10 * time.Second}
		done <- cmd.Run(ctx, &CLI{Server: testServer}, client)
	}()
	st := awaitAgent(t, m.store, done, "the device online", func(st connectionState) bool { return st.Online })
	if st.DeviceName != "phone" || st.Reattaches != 0 || adb.connects(st.Address) != 1 {
		t.Fatalf("state %+v after %d connects, want the name, no reattach and one connect", st, adb.connects(st.Address))
	}

	exchangeOverAgent(t, ctx, st.Address)

	// adb gives up on the address while the device stream is down.
	adb.lose()
	endStream()

	st = awaitAgent(t, m.store, done, "a reattach", func(st connectionState) bool { return st.Reattaches == 1 })
	if adb.connects(st.Address) != 2 || !st.Online {
		t.Errorf("state %+v after %d connects, want online after a second connect", st, adb.connects(st.Address))
	}

	cancel()
	if err := awaitAgentExit(t, m.store, done); err != nil {
		t.Errorf("Run() error = %v, want nil on SIGTERM", err)
	}
}
