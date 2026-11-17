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

// Package faultproxy is a controllable TCP proxy for injecting network faults
// between a test harness's subprocesses and their database (or any TCP
// upstream): added latency, blackholes (connections stay up, bytes stop
// flowing), refused new connections, and hard connection cuts (the
// ambiguous-commit fault, where a request may or may not have reached the
// server when the link died). It is in-house rather than toxiproxy because
// ~150 lines buys zero external dependencies and an in-process control
// surface.
package faultproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Proxy forwards TCP connections to a fixed upstream while faults are toggled
// at runtime. All controls are safe for concurrent use.
type Proxy struct {
	ln       net.Listener
	upstream string

	latencyNs atomic.Int64
	blackhole atomic.Bool
	refuse    atomic.Bool
	closed    atomic.Bool

	mu    sync.Mutex
	conns map[net.Conn]struct{} // both legs of every live pairing
}

// Listen starts a proxy on an ephemeral loopback port forwarding to upstream.
func Listen(upstream string) (*Proxy, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listening: %w", err)
	}
	p := &Proxy{ln: ln, upstream: upstream, conns: make(map[net.Conn]struct{})}
	go p.acceptLoop()
	return p, nil
}

// Addr returns the address clients should dial instead of the upstream.
func (p *Proxy) Addr() string { return p.ln.Addr().String() }

// SetLatency adds a per-chunk forwarding delay in both directions (0 clears).
func (p *Proxy) SetLatency(d time.Duration) { p.latencyNs.Store(int64(d)) }

// SetBlackhole stops forwarding bytes while keeping connections open. To the
// client the server has gone silent mid-conversation.
func (p *Proxy) SetBlackhole(on bool) { p.blackhole.Store(on) }

// SetRefuseNew makes new connections close immediately after accept; existing
// connections are untouched.
func (p *Proxy) SetRefuseNew(on bool) { p.refuse.Store(on) }

// CutConnections severs every live connection and reports how many pairings
// died. Requests in flight, including commits, end with an unknown outcome.
func (p *Proxy) CutConnections() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.conns {
		_ = c.Close()
	}
	n := len(p.conns) / 2
	clear(p.conns)
	return n
}

// Close shuts the listener and severs all connections.
func (p *Proxy) Close() error {
	if p.closed.Swap(true) {
		return nil
	}
	err := p.ln.Close()
	p.CutConnections()
	if err != nil {
		return fmt.Errorf("closing listener: %w", err)
	}
	return nil
}

func (p *Proxy) acceptLoop() {
	for {
		client, err := p.ln.Accept()
		if err != nil {
			return // listener closed
		}
		if p.refuse.Load() {
			_ = client.Close()
			continue
		}
		go p.serve(client)
	}
}

func (p *Proxy) serve(client net.Conn) {
	var d net.Dialer
	server, err := d.DialContext(context.Background(), "tcp", p.upstream)
	if err != nil {
		_ = client.Close()
		return
	}
	p.track(client, server)
	var wg sync.WaitGroup
	wg.Go(func() { p.pump(client, server) })
	wg.Go(func() { p.pump(server, client) })
	wg.Wait()
	p.untrack(client, server)
}

// pump forwards src to dst one chunk at a time, holding at most one chunk
// while blackholed (the kernel's buffers backpressure the rest) and delaying
// each chunk by the configured latency. Any error tears down both legs so the
// peer sees the failure promptly.
func (p *Proxy) pump(dst, src net.Conn) {
	defer func() {
		_ = dst.Close()
		_ = src.Close()
	}()
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			for p.blackhole.Load() && !p.closed.Load() {
				time.Sleep(5 * time.Millisecond)
			}
			if d := time.Duration(p.latencyNs.Load()); d > 0 {
				time.Sleep(d)
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return
			}
			// Half-close: propagate EOF but keep the reverse leg flowing.
			if tc, ok := dst.(*net.TCPConn); ok {
				_ = tc.CloseWrite()
			}
			return
		}
	}
}

func (p *Proxy) track(a, b net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.conns[a] = struct{}{}
	p.conns[b] = struct{}{}
}

func (p *Proxy) untrack(a, b net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.conns, a)
	delete(p.conns, b)
}
