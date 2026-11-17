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

package faultproxy

import (
	"net"
	"testing"
	"time"
)

// startEcho runs a TCP echo server for the proxy to front.
func startEcho(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						if _, err := c.Write(buf[:n]); err != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	var d net.Dialer
	c, err := d.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func roundTrip(t *testing.T, c net.Conn, payload string) (string, error) {
	t.Helper()
	if _, err := c.Write([]byte(payload)); err != nil {
		return "", err
	}
	buf := make([]byte, len(payload))
	if err := c.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := c.Read(buf)
	return string(buf[:n]), err
}

func TestForwardsTraffic(t *testing.T) {
	p, err := Listen(startEcho(t))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c := dial(t, p.Addr())
	got, err := roundTrip(t, c, "hello")
	if err != nil || got != "hello" {
		t.Fatalf("roundTrip = %q, %v; want %q", got, err, "hello")
	}
}

func TestLatencyDelaysChunks(t *testing.T) {
	p, err := Listen(startEcho(t))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c := dial(t, p.Addr())

	const delay = 60 * time.Millisecond
	p.SetLatency(delay)
	start := time.Now()
	if _, err := roundTrip(t, c, "ping"); err != nil {
		t.Fatal(err)
	}
	// Both directions pay the delay.
	if elapsed := time.Since(start); elapsed < 2*delay {
		t.Errorf("round trip took %v, want >= %v", elapsed, 2*delay)
	}

	p.SetLatency(0)
	start = time.Now()
	if _, err := roundTrip(t, c, "ping"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > delay {
		t.Errorf("round trip after clearing latency took %v, want fast", elapsed)
	}
}

func TestBlackholeStallsThenResumes(t *testing.T) {
	p, err := Listen(startEcho(t))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c := dial(t, p.Addr())

	p.SetBlackhole(true)
	if _, err := c.Write([]byte("stuck")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	if err := c.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Read(buf); err == nil {
		t.Fatalf("read %d bytes through a blackhole", n)
	}

	p.SetBlackhole(false)
	if err := c.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := c.Read(buf)
	if err != nil || string(buf[:n]) != "stuck" {
		t.Fatalf("read after blackhole = %q, %v; want %q", buf[:n], err, "stuck")
	}
}

func TestCutConnectionsSeversLiveConns(t *testing.T) {
	p, err := Listen(startEcho(t))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c := dial(t, p.Addr())
	if _, err := roundTrip(t, c, "warm"); err != nil {
		t.Fatal(err)
	}

	if n := p.CutConnections(); n != 1 {
		t.Errorf("CutConnections = %d, want 1", n)
	}
	if _, err := roundTrip(t, c, "dead"); err == nil {
		t.Error("expected error on a cut connection")
	}

	// New connections still work after a cut.
	c2 := dial(t, p.Addr())
	if got, err := roundTrip(t, c2, "fresh"); err != nil || got != "fresh" {
		t.Fatalf("post-cut roundTrip = %q, %v", got, err)
	}
}

func TestRefuseNewBlocksOnlyNewConns(t *testing.T) {
	p, err := Listen(startEcho(t))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	existing := dial(t, p.Addr())
	if _, err := roundTrip(t, existing, "old"); err != nil {
		t.Fatal(err)
	}

	p.SetRefuseNew(true)
	refused := dial(t, p.Addr())
	buf := make([]byte, 1)
	if err := refused.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := refused.Read(buf); err == nil {
		t.Error("expected refused connection to close")
	}

	if got, err := roundTrip(t, existing, "old2"); err != nil || got != "old2" {
		t.Fatalf("existing conn broken by refuse-new: %q, %v", got, err)
	}
}
