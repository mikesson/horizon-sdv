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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
)

// fakeAgentClock records sleeps instead of sleeping. A sleep advances the
// clock unless the clock is frozen, which plays sessions that fail at the same
// moment.
type fakeAgentClock struct {
	mu     sync.Mutex
	now    time.Time
	frozen bool
	slept  []time.Duration
}

func newFakeAgentClock() *fakeAgentClock {
	return &fakeAgentClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeAgentClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeAgentClock) Sleep(_ context.Context, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slept = append(c.slept, d)
	if !c.frozen {
		c.now = c.now.Add(d)
	}
}

func (c *fakeAgentClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeAgentClock) sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.slept)
}

// sessionsHarness drives agentSessions with a fake clock and a scripted
// device lookup.
type sessionsHarness struct {
	sessions *agentSessions
	clock    *fakeAgentClock
	lookups  int
	stops    int
	lines    []string
}

// newSessionsHarness answers every device lookup with lookup.
func newSessionsHarness(lookup func() (*cco.Device, error)) *sessionsHarness {
	h := &sessionsHarness{clock: newFakeAgentClock()}
	h.sessions = &agentSessions{
		deviceID: "dev-1", instance: "1", clock: h.clock,
		getDevice: func(context.Context, string) (*cco.Device, error) {
			h.lookups++
			return lookup()
		},
		stop: func() { h.stops++ },
		log: &agentLog{
			clock: h.clock,
			emit:  func(line string) { h.lines = append(h.lines, line) },
		},
	}
	return h
}

func deviceWithStatus(status string) func() (*cco.Device, error) {
	return func() (*cco.Device, error) { return &cco.Device{ID: "dev-1", Status: status}, nil }
}

func apiError(status int) error {
	return &cco.Error{Status: status, Message: fmt.Sprintf("request failed with status %d", status)}
}

// dialFailure is the session result of a dial the server answered with status.
func dialFailure(status int) error {
	return streamDialFailed("dev-1", apiError(status))
}

func TestAgentSessionsStopOnADialNotFound(t *testing.T) {
	h := newSessionsHarness(deviceWithStatus("running"))
	h.sessions.ended(t.Context(), dialFailure(http.StatusNotFound))
	if h.stops != 1 || h.lookups != 0 || len(h.clock.sleeps()) != 0 {
		t.Errorf("stops %d lookups %d sleeps %v; want one stop, no lookup and no hold", h.stops, h.lookups, h.clock.sleeps())
	}
}

// A server can answer the ADB route for a retired device with 502, so the
// agent asks the API before it holds the connection.
func TestAgentSessionsStopWhenTheDeviceIsGone(t *testing.T) {
	for name, lookup := range map[string]func() (*cco.Device, error){
		"404":      func() (*cco.Device, error) { return nil, apiError(http.StatusNotFound) },
		"deleted":  deviceWithStatus(cco.StatusDeleted),
		"deleting": deviceWithStatus(cco.StatusDeleting),
	} {
		for result, err := range map[string]error{
			"dial 502":      dialFailure(http.StatusBadGateway),
			"silent stream": silentStream(),
		} {
			t.Run(name+" after "+result, func(t *testing.T) {
				h := newSessionsHarness(lookup)
				h.sessions.ended(t.Context(), err)
				if h.stops != 1 || h.lookups != 1 {
					t.Errorf("stops %d lookups %d; want one stop after one lookup", h.stops, h.lookups)
				}
				if got := h.clock.sleeps(); len(got) != 0 {
					t.Errorf("held the connection for %v before stopping", got)
				}
			})
		}
	}
}

func silentStream() error { return fmt.Errorf("%w for dev-1", errStreamSilent) }

func TestAgentSessionsKeepRunningForALiveDevice(t *testing.T) {
	for name, lookup := range map[string]func() (*cco.Device, error){
		"running":       deviceWithStatus(cco.StatusRunning),
		"stopped":       deviceWithStatus(cco.StatusStopped),
		"lookup 500":    func() (*cco.Device, error) { return nil, apiError(http.StatusInternalServerError) },
		"lookup failed": func() (*cco.Device, error) { return nil, errors.New("connection refused") },
	} {
		t.Run(name, func(t *testing.T) {
			h := newSessionsHarness(lookup)
			h.sessions.ended(t.Context(), dialFailure(http.StatusBadGateway))
			if h.stops != 0 {
				t.Errorf("stopped the agent for a device that may still exist")
			}
			if got := h.clock.sleeps(); !slices.Equal(got, []time.Duration{time.Second}) {
				t.Errorf("holds = %v, want one 1s hold", got)
			}
		})
	}
}

// backoffSteps is the hold after each of seven consecutive failed dials.
var backoffSteps = []time.Duration{
	time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
	16 * time.Second, 30 * time.Second, 30 * time.Second,
}

func TestDialBackoffDoublesToTheCap(t *testing.T) {
	for i, want := range backoffSteps {
		if got := dialBackoff(i + 1); got != want {
			t.Errorf("dialBackoff(%d) = %v, want %v", i+1, got, want)
		}
	}
	if got := dialBackoff(1000); got != dialBackoffMax {
		t.Errorf("dialBackoff(1000) = %v, want the cap %v", got, dialBackoffMax)
	}
}

func TestAgentSessionsBackOffAndResetOnASessionThatCarriedData(t *testing.T) {
	h := newSessionsHarness(deviceWithStatus(cco.StatusRunning))
	ctx := t.Context()
	for range backoffSteps {
		h.sessions.ended(ctx, dialFailure(http.StatusBadGateway))
	}
	if got := h.clock.sleeps(); !slices.Equal(got, backoffSteps) {
		t.Fatalf("holds = %v, want %v", got, backoffSteps)
	}
	// Each hold moves the clock to the end of its step, so every failure
	// starts a step and checks once.
	if h.lookups != 7 {
		t.Errorf("lookups = %d, want one per backoff step", h.lookups)
	}

	// A stream that opened and ended silently is a failure, so the backoff
	// stays at the cap.
	before := len(h.clock.sleeps())
	h.sessions.ended(ctx, silentStream())
	if got := h.clock.sleeps()[before:]; !slices.Equal(got, []time.Duration{dialBackoffMax}) {
		t.Errorf("after a silent stream the hold = %v, want the cap", got)
	}

	// bridgeADB reports a clean end or a relay failure only for a stream the
	// device sent data on.
	for _, result := range []error{nil, errors.New("relaying ADB for dev-1: connection reset")} {
		h.sessions.ended(ctx, result)
		before := len(h.clock.sleeps())
		h.sessions.ended(ctx, dialFailure(http.StatusBadGateway))
		if got := h.clock.sleeps()[before:]; !slices.Equal(got, []time.Duration{time.Second}) {
			t.Errorf("after %v the next hold = %v, want the backoff reset to 1s", result, got)
		}
	}
	if h.stops != 0 {
		t.Errorf("stops = %d, want none for a running device", h.stops)
	}
}

func TestAgentSessionsCountOneStepForFailuresInsideIt(t *testing.T) {
	h := newSessionsHarness(deviceWithStatus(cco.StatusRunning))
	h.clock.frozen = true
	ctx := t.Context()

	// Three failures at the same moment, as from connections adb opened
	// together.
	for range 3 {
		h.sessions.ended(ctx, dialFailure(http.StatusBadGateway))
	}
	if h.lookups != 1 {
		t.Errorf("lookups = %d, want 1 within one backoff step", h.lookups)
	}
	if got := h.clock.sleeps(); !slices.Equal(got, []time.Duration{time.Second, time.Second, time.Second}) {
		t.Errorf("holds = %v, want each held to the end of the first step", got)
	}

	// Once the step has passed, the next failure starts the second step: one
	// more check and a 2s hold, not the 8s that counting each failure gives.
	h.clock.advance(time.Second)
	before := len(h.clock.sleeps())
	h.sessions.ended(ctx, dialFailure(http.StatusBadGateway))
	if h.lookups != 2 {
		t.Errorf("lookups = %d, want a second check once the step passed", h.lookups)
	}
	if got := h.clock.sleeps()[before:]; !slices.Equal(got, []time.Duration{2 * time.Second}) {
		t.Errorf("second step hold = %v, want 2s", got)
	}
}

func TestAgentSessionsHoldDialsUntilTheStepEnds(t *testing.T) {
	h := newSessionsHarness(deviceWithStatus(cco.StatusRunning))
	h.clock.frozen = true
	ctx := t.Context()

	h.sessions.beforeDial(ctx)
	if got := h.clock.sleeps(); len(got) != 0 {
		t.Fatalf("waited %v before the first dial, want no wait", got)
	}
	h.sessions.ended(ctx, dialFailure(http.StatusBadGateway))
	h.clock.advance(400 * time.Millisecond)
	before := len(h.clock.sleeps())
	h.sessions.beforeDial(ctx)
	if got := h.clock.sleeps()[before:]; !slices.Equal(got, []time.Duration{600 * time.Millisecond}) {
		t.Errorf("a dial 400ms into a 1s step waited %v, want the remaining 600ms", got)
	}
	h.clock.advance(600 * time.Millisecond)
	before = len(h.clock.sleeps())
	h.sessions.beforeDial(ctx)
	if got := h.clock.sleeps()[before:]; len(got) != 0 {
		t.Errorf("a dial after the step waited %v, want no wait", got)
	}
}

// failingADBBackend answers every ADB dial with 502 and reads device dev-1
// back as running. It counts both.
func failingADBBackend(t *testing.T) (client *cco.Client, dials, lookups *atomic.Int32) {
	t.Helper()
	dials, lookups = new(atomic.Int32), new(atomic.Int32)
	client = agentClientFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/devices/dev-1":
			lookups.Add(1)
			fmt.Fprint(w, `{"id":"dev-1","name":"n","host_id":"h1","status":"running","instances":[{"name":"1"}]}`)
		case strings.HasSuffix(r.URL.Path, "/adb"):
			dials.Add(1)
			http.Error(w, "Failed to connect to device", http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
	return client, dials, lookups
}

// awaitTunnelClose waits until the tunnel closes the local connection.
func awaitTunnelClose(t *testing.T, local net.Conn) {
	t.Helper()
	if err := local.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(local); err != nil {
		t.Fatalf("the tunnel did not close the connection: %v", err)
	}
}

func TestADBTunnelPacesDialsThroughTheAgentBackoff(t *testing.T) {
	client, dials, lookups := failingADBBackend(t)
	clock := newFakeAgentClock()
	clock.frozen = true
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sessions := &agentSessions{
		deviceID: "dev-1", instance: "1", getDevice: client.GetDevice,
		clock: clock, stop: cancel,
		log: &agentLog{clock: clock, emit: func(string) {}},
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- serveADBTunnel(ctx, listener, client, "dev-1", "1", sessions) }()

	open := func() net.Conn {
		var dialer net.Dialer
		local, err := dialer.DialContext(ctx, "tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = local.Close() })
		return local
	}

	// Three connections at once, as when adb opens several together.
	conns := []net.Conn{open(), open(), open()}
	for _, local := range conns {
		awaitTunnelClose(t, local)
	}
	if dials.Load() != 3 || lookups.Load() != 1 {
		t.Errorf("dials %d lookups %d; want 3 dials and one check in the first step", dials.Load(), lookups.Load())
	}

	// A redial inside the step waits for the step to end before it dials,
	// then the tunnel holds the failed connection to the step's end.
	before := len(clock.sleeps())
	awaitTunnelClose(t, open())
	if got := clock.sleeps()[before:]; !slices.Equal(got, []time.Duration{time.Second, time.Second}) {
		t.Errorf("waits = %v, want 1s before the dial and 1s hold after it", got)
	}

	// The next step is the second, whatever the number of failures before.
	clock.advance(time.Second)
	before = len(clock.sleeps())
	awaitTunnelClose(t, open())
	if got := clock.sleeps()[before:]; !slices.Equal(got, []time.Duration{2 * time.Second}) {
		t.Errorf("waits = %v, want the 2s hold of the second step", got)
	}
	if dials.Load() != 5 || lookups.Load() != 2 {
		t.Errorf("dials %d lookups %d; want 5 dials and one check per step", dials.Load(), lookups.Load())
	}

	cancel()
	if err := <-served; err != nil {
		t.Errorf("serveADBTunnel() error = %v, want nil", err)
	}
}

// recordingSessions records each session result.
type recordingSessions struct {
	results chan error
}

func (recordingSessions) beforeDial(context.Context) {}

func (r recordingSessions) ended(_ context.Context, err error) { r.results <- err }

func TestADBTunnelReportsAStreamThatCarriedNoData(t *testing.T) {
	for name, tc := range map[string]struct {
		serve      func(ctx context.Context, c *websocket.Conn)
		wantSilent bool
	}{
		"closed at once": {
			serve:      func(_ context.Context, c *websocket.Conn) { c.Close(websocket.StatusNormalClosure, "") },
			wantSilent: true,
		},
		"answered": {serve: echoStream},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			client := adbBackend(t, tc.serve)
			var lc net.ListenConfig
			listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			sessions := recordingSessions{results: make(chan error, 1)}
			go func() { _ = serveADBTunnel(ctx, listener, client, "dev-1", "1", sessions) }()

			var dialer net.Dialer
			local, err := dialer.DialContext(ctx, "tcp", listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			// The tunnel may already have closed a silent stream's connection.
			if _, err := local.Write([]byte("CNXN")); err != nil && !tc.wantSilent {
				t.Fatal(err)
			}
			if !tc.wantSilent {
				if err := local.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadFull(local, make([]byte, 4)); err != nil {
					t.Fatalf("reading the echo: %v", err)
				}
			}
			_ = local.Close()

			select {
			case err := <-sessions.results:
				if got := errors.Is(err, errStreamSilent); got != tc.wantSilent {
					t.Errorf("session result = %v, want silent %v", err, tc.wantSilent)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the session never ended")
			}
		})
	}
}

// messages strips the timestamp prefix from each logged line.
func messages(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		_, msg, _ := strings.Cut(line, "Z: ")
		out[i] = msg
	}
	return out
}

func TestAgentLogCountsRepeatsInsteadOfWritingThem(t *testing.T) {
	clock := newFakeAgentClock()
	var lines []string
	l := &agentLog{clock: clock, emit: func(line string) { lines = append(lines, line) }}

	for range 5 {
		l.printf("ADB session ended: %s", "502")
	}
	if got := messages(lines); !slices.Equal(got, []string{"ADB session ended: 502"}) {
		t.Fatalf("lines = %q, want the message once", got)
	}
	clock.advance(repeatSummaryEvery)
	l.printf("ADB session ended: %s", "502")
	l.printf("ADB session ended: %s", "502")
	l.printf("ADB session ended: %s", "502")
	l.printf("Checking device dev-1: timeout")
	clock.advance(repeatSummaryEvery)
	l.tick()
	want := []string{
		"ADB session ended: 502",
		"Repeated 5 more times: ADB session ended: 502",
		"Checking device dev-1: timeout",
		"Repeated 2 more times: ADB session ended: 502",
	}
	if got := messages(lines); !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
	if !strings.HasPrefix(lines[0], "2026-10-01T12:00:00Z: ") {
		t.Errorf("line %q lacks the timestamp", lines[0])
	}
}

// A failing device check logs two messages per failed dial, alternating.
func TestAgentLogCountsAlternatingMessages(t *testing.T) {
	clock := newFakeAgentClock()
	var lines []string
	l := &agentLog{clock: clock, emit: func(line string) { lines = append(lines, line) }}

	for range 10 {
		l.printf("ADB session ended: 502")
		l.printf("Checking device dev-1: 500")
	}
	clock.advance(repeatSummaryEvery)
	l.tick()
	want := []string{
		"ADB session ended: 502",
		"Checking device dev-1: 500",
		"Repeated 9 more times: ADB session ended: 502",
		"Repeated 9 more times: Checking device dev-1: 500",
	}
	if got := messages(lines); !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
}

func TestAgentLogWritesAMessageAgainOnceItLeftTheRecentSet(t *testing.T) {
	clock := newFakeAgentClock()
	var lines []string
	l := &agentLog{clock: clock, emit: func(line string) { lines = append(lines, line) }}

	l.printf("m0")
	l.printf("m0")
	for i := 1; i <= agentLogRecent; i++ {
		l.printf("m%d", i)
	}
	l.printf("m0")
	want := []string{"m0", "m1", "m2", "m3", "m4", "Repeated 1 more times: m0", "m0"}
	if got := messages(lines); !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
}

// openAgentLog opens a log file the way the parent opens it for the agent.
func openAgentLog(t *testing.T) (string, *os.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return path, f
}

func TestAgentLogTruncatesTheFileAtTheLimit(t *testing.T) {
	path, f := openAgentLog(t)
	const limit = 200
	l := &agentLog{
		clock: newFakeAgentClock(), path: path, limit: limit,
		emit: func(line string) { _, _ = fmt.Fprintln(f, line) },
	}

	var data []byte
	for i := range 20 {
		l.printf("message %d padded to take some room in the log", i)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		// One line may land past the limit before the next write truncates.
		if info.Size() > limit+100 {
			t.Fatalf("log is %d bytes after message %d, want it capped near %d", info.Size(), i, limit)
		}
		if data, err = os.ReadFile(path); err != nil {
			t.Fatal(err)
		}
		if newest := fmt.Sprintf("message %d ", i); !strings.Contains(string(data), newest) {
			t.Fatalf("the newest message %q is missing:\n%s", newest, data)
		}
	}
	if strings.Contains(string(data), "message 0 ") {
		t.Errorf("the first message survived the truncation:\n%s", data)
	}
	if !strings.Contains(string(data), "Log truncated") {
		t.Errorf("log lacks the truncation note:\n%s", data)
	}
	if !strings.HasPrefix(string(data), "2026-10-01T12:00:00Z: ") || strings.Contains(string(data), "\x00") {
		t.Errorf("log has a hole of zero bytes, so writes did not append:\n%q", data)
	}
}

// Output the agent log does not write itself, such as --verbose request
// traces, goes to the same file and is capped by the tick after each session.
func TestAgentLogCapsOutputItDidNotWrite(t *testing.T) {
	path, f := openAgentLog(t)
	const limit = 200
	l := &agentLog{
		clock: newFakeAgentClock(), path: path, limit: limit,
		emit: func(line string) { _, _ = fmt.Fprintln(f, line) },
	}
	l.printf("ADB session ended: 502")
	if _, err := f.WriteString(strings.Repeat("[DEBUG] --> WS wss://cco.example/v2/devices/dev-1/instances/1/adb\n", 5)); err != nil {
		t.Fatal(err)
	}
	// A repeat is only counted, yet the file is still capped.
	l.printf("ADB session ended: 502")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > limit || strings.Contains(string(data), "[DEBUG]") {
		t.Errorf("log is %d bytes after a repeat, want the trace truncated:\n%s", len(data), data)
	}

	if _, err := f.WriteString(strings.Repeat("[DEBUG] <-- 200 OK\n", 20)); err != nil {
		t.Fatal(err)
	}
	l.tick()
	if data, err = os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
	if len(data) > limit || !strings.Contains(string(data), "Log truncated") {
		t.Errorf("log is %d bytes after a tick, want it truncated:\n%s", len(data), data)
	}
}
