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
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/auth/authtest"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
)

// adbBackend serves the device ADB route, handing each accepted stream to
// serve. Anything else answers 404, so a stray call is visible.
func adbBackend(t *testing.T, serve func(ctx context.Context, c *websocket.Conn)) *cco.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/adb") {
			http.NotFound(w, r)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		serve(r.Context(), c)
	}))
	t.Cleanup(srv.Close)

	keyPath, _ := authtest.WriteKeyFile(t)
	// Built the way the command builds it. A client with the 30s request
	// timeout cannot open a WebSocket at all.
	client, err := cco.NewClient(t.Context(), srv.URL,
		&config.Config{Server: srv.URL, CredentialsPath: keyPath}, 0)
	if err != nil {
		t.Fatalf("cco.NewClient: %v", err)
	}
	return client
}

// startTunnel runs the tunnel on a loopback port and returns its address.
func startTunnel(t *testing.T, ctx context.Context, client *cco.Client) (string, chan error) {
	t.Helper()
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- serveADBTunnel(ctx, listener, client, "dev-1", "inst", foregroundSessions{}) }()
	return listener.Addr().String(), served
}

// echoStream writes back every message it reads.
func echoStream(ctx context.Context, c *websocket.Conn) {
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		if err := c.Write(ctx, typ, data); err != nil {
			return
		}
	}
}

func TestADBTunnelCarriesBytesBothWays(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	addr, served := startTunnel(t, ctx, adbBackend(t, echoStream))

	var dialer net.Dialer
	local, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatalf("dialing the tunnel: %v", err)
	}
	defer local.Close()

	// Any payload works here. A CNXN packet is what a real ADB session sends
	// first.
	want := []byte("CNXN\x00\x00\x00\x01host::features=shell_v2")
	if _, err := local.Write(want); err != nil {
		t.Fatalf("writing to the tunnel: %v", err)
	}

	got := make([]byte, len(want))
	if err := local.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("setting a read deadline: %v", err)
	}
	if _, err := io.ReadFull(local, got); err != nil {
		t.Fatalf("reading the echo: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("tunnel returned %q, want %q", got, want)
	}

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("serveADBTunnel() error = %v, want nil on interrupt", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveADBTunnel did not stop when the context ended")
	}
}

// Each local connection gets its own stream, so a closed stream costs one
// reconnect. The second dial stands in for the local adb server redialing.
func TestADBTunnelGivesEachConnectionItsOwnStream(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Answers once, then hangs up, as when the platform request timeout cuts a
	// stream.
	client := adbBackend(t, func(ctx context.Context, c *websocket.Conn) {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		_ = c.Write(ctx, typ, data)
		c.Close(websocket.StatusNormalClosure, "session ended")
	})
	addr, _ := startTunnel(t, ctx, client)

	for attempt := range 2 {
		var dialer net.Dialer
		local, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			t.Fatalf("attempt %d dialing the tunnel: %v", attempt, err)
		}
		if _, err := local.Write([]byte("OPEN")); err != nil {
			t.Fatalf("attempt %d writing: %v", attempt, err)
		}
		if err := local.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatalf("attempt %d deadline: %v", attempt, err)
		}
		got := make([]byte, 4)
		if _, err := io.ReadFull(local, got); err != nil {
			t.Fatalf("attempt %d reading: %v (a dropped stream must not poison the listener)", attempt, err)
		}
		if string(got) != "OPEN" {
			t.Errorf("attempt %d returned %q, want OPEN", attempt, got)
		}
		local.Close()
	}
}

// scriptedADB plays the local adb for the foreground command. Its connect
// sends a handshake into the tunnel and waits for the answer, as adb does.
type scriptedADB struct {
	t *testing.T

	mu     sync.Mutex
	events []string
	// states are the answers to successive get-state calls, the last repeated.
	states []string
	// handshake is the result of connect's handshake.
	handshake error
}

const passthroughOutput = "R1A2.240101.001\n"

func (s *scriptedADB) run(ctx context.Context, stdout, _ io.Writer, args ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case args[0] == "connect":
		s.events = append(s.events, "connect")
		s.handshake = adbHandshake(ctx, args[1])
		fmt.Fprintf(stdout, "connected to %s\n", args[1])
	case args[0] == "disconnect":
		s.events = append(s.events, "disconnect")
		fmt.Fprintf(stdout, "disconnected %s\n", args[1])
	case slices.Equal(args[2:], []string{"get-state"}):
		state := s.states[0]
		if len(s.states) > 1 {
			s.states = s.states[1:]
		}
		s.events = append(s.events, "get-state "+state)
		fmt.Fprintln(stdout, state)
	case slices.Equal(args[2:], []string{"shell", "getprop", "ro.build.id"}):
		s.events = append(s.events, "passthrough")
		fmt.Fprint(stdout, passthroughOutput)
	default:
		s.t.Errorf("unexpected adb call %v", args)
	}
	return nil
}

// adbHandshake sends a CNXN packet to addr and reads the echo, as adb does
// on connect. adb gives up after 10s; 5s is ample for a loopback echo.
func adbHandshake(ctx context.Context, addr string) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dialing %s: %w", addr, err)
	}
	defer conn.Close()
	want := []byte("CNXN\x00\x00\x00\x01host::")
	if _, err := conn.Write(want); err != nil {
		return fmt.Errorf("writing the handshake: %w", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return fmt.Errorf("setting a read deadline: %w", err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		return fmt.Errorf("reading the answer: %w", err)
	}
	return nil
}

// useLocalADB points the foreground command at fake for the rest of the test.
func useLocalADB(t *testing.T, fake adbRunner) {
	t.Helper()
	prev := localADB
	localADB = fake
	t.Cleanup(func() { localADB = prev })
}

// echoDeviceBackend serves running device testDeviceID with one instance whose
// ADB stream echoes.
func echoDeviceBackend() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/adb"):
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer c.CloseNow()
			echoStream(r.Context(), c)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/devices/"+testDeviceID:
			fmt.Fprintf(w, `{"id":%q,"name":"n","host_id":"h1","status":"running","instances":[{"name":"1"}]}`, testDeviceID)
		default:
			http.NotFound(w, r)
		}
	})
}

// The tunnel serves while adb connects, the passthrough command runs only
// once adb reports the device online, and stdout carries the passthrough
// command's output and nothing else.
func TestE2E_DeviceConnectServesBeforeAttachAndWaitsOnline(t *testing.T) {
	adb := &scriptedADB{t: t, states: []string{"offline", adbOnline}}
	useLocalADB(t, adb.run)

	out, err := runCLI(t, echoDeviceBackend(), "device", "connect", testDeviceID, "--", "shell", "getprop", "ro.build.id")
	if err != nil {
		t.Fatalf("device connect -- shell getprop: %v", err)
	}
	if adb.handshake != nil {
		t.Errorf("adb connect's handshake got no answer, so the tunnel was not serving during the connect: %v", adb.handshake)
	}
	want := []string{"connect", "get-state offline", "get-state device", "passthrough", "disconnect"}
	if !slices.Equal(adb.events, want) {
		t.Errorf("adb calls = %v, want %v", adb.events, want)
	}
	if out != passthroughOutput {
		t.Errorf("stdout = %q, want only the passthrough output %q", out, passthroughOutput)
	}
}

// A device that never comes online fails the foreground connect with exit 7
// while the device is running, and runs no passthrough command.
func TestE2E_DeviceConnectTimesOutOffline(t *testing.T) {
	adb := &scriptedADB{t: t, states: []string{"offline"}}
	useLocalADB(t, adb.run)

	out, err := runCLI(t, echoDeviceBackend(), "device", "connect", testDeviceID, "--wait-online", "300ms", "--", "shell", "getprop", "ro.build.id")
	if got := exitCodeFor(err, false); got != ExitTransient {
		t.Errorf("exit code = %d, want %d (error %v)", got, ExitTransient, err)
	}
	if err == nil || !strings.Contains(err.Error(), "last adb state: offline") {
		t.Errorf("err = %v, want the last adb state named", err)
	}
	if slices.Contains(adb.events, "passthrough") {
		t.Errorf("adb calls = %v, want no passthrough command", adb.events)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
}
