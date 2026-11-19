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

package hoclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/tests/harness/fakeho"
)

// This file holds the tests about what the CONNECTION does: port failover,
// retries, circuit breaking, deadlines and size caps. They keep hand-rolled
// servers because they need one that misbehaves in ways a real host cannot:
// hijacking a connection mid-exchange, never answering, or counting dials.
//
// Tests about what the host SAYS live in contract_test.go, against
// tests/harness/fakeho. Adding a hand-rolled server here that serves a Host
// Orchestrator response shape is the thing that split was made to stop.
//
// The `{"cvds": []}` bodies below are the smallest valid 200, not a contract:
// no assertion here reads them, so they cannot drift into a claim about the
// host. Where an assertion DOES depend on the response, the server is the fake.

func TestHostClient_FailoverToNextURL(t *testing.T) {
	t.Parallel()
	// The live half is the fake, because the assertion below reads its answer:
	// proving the response came from the second port means parsing what that
	// port actually said.
	live, err := fakeho.New()
	if err != nil {
		t.Fatalf("starting fake host: %v", err)
	}
	defer func() { _ = live.Close() }()
	live.SeedGroup(fakeho.SingleHostIP, "g", "from-the-fallback-port", "Running")

	config := DefaultClientConfig()
	config.FallbackPorts = []int{12345, live.Port()} // First port (12345) will fail to connect

	// Create client pointing at a dead port
	client := NewHostClient("http://127.0.0.1:12345", config)

	cvds, err := client.ListCVDs(t.Context())
	if err != nil {
		t.Fatalf("expected failover to succeed, got %v", err)
	}

	if len(cvds) != 1 || cvds[0].Name != "from-the-fallback-port" {
		t.Errorf("cvds = %+v, want the fallback port's answer", cvds)
	}

	// Verify sticky routing: activeURLIndex should be updated to point to the successful URL
	if client.activeURLIndex.Load() != 1 {
		t.Errorf("expected activeURLIndex to be 1, got %d", client.activeURLIndex.Load())
	}
}

func TestHostClient_AllURLsDown(t *testing.T) {
	t.Parallel()
	config := DefaultClientConfig()
	config.FallbackPorts = []int{12345, 12346} // Dead ports
	config.MaxRetries = 0                      // Disable temporal retries

	client := NewHostClient("http://127.0.0.1:12345", config)

	_, err := client.ListCVDs(t.Context())
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	// Verify we got joined errors
	errStr := err.Error()
	if !strings.Contains(errStr, "12345") || !strings.Contains(errStr, "12346") {
		t.Errorf("expected error to mention both ports, got: %v", errStr)
	}

	if !IsRetryable(err) {
		t.Errorf("expected joined connection errors to be retryable")
	}
}

func TestHostClient_StickyRouting(t *testing.T) {
	t.Parallel()
	var ts1Calls, ts2Calls atomic.Int32

	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts1Calls.Add(1)
		// Force connection termination to simulate down server
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
		}
	}))
	defer ts1.Close()

	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts2Calls.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"cvds": []}`))
	}))
	defer ts2.Close()

	parts := strings.Split(ts2.URL, ":")
	ts2Port, _ := strconv.Atoi(parts[len(parts)-1])

	config := DefaultClientConfig()
	config.FallbackPorts = []int{ts2Port}

	client := NewHostClient(ts1.URL, config)

	// First call should hit ts1 (fail), then ts2 (succeed)
	_, err := client.ListCVDs(t.Context())
	if err != nil {
		t.Fatalf("call 1 failed: %v", err)
	}

	if ts1Calls.Load() != 1 || ts2Calls.Load() != 1 {
		t.Errorf("expected ts1=1, ts2=1, got ts1=%d, ts2=%d", ts1Calls.Load(), ts2Calls.Load())
	}

	// Second call should go straight to ts2 due to sticky routing
	_, err = client.ListCVDs(t.Context())
	if err != nil {
		t.Fatalf("call 2 failed: %v", err)
	}

	if ts1Calls.Load() != 1 || ts2Calls.Load() != 2 {
		t.Errorf("expected ts1=1, ts2=2, got ts1=%d, ts2=%d", ts1Calls.Load(), ts2Calls.Load())
	}
}

func TestFactorySharesTransportAcrossClients(t *testing.T) {
	t.Parallel()
	var newConns atomic.Int32
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"cvds": []}`))
	}))
	ts.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConns.Add(1)
		}
	}
	ts.Start()
	defer ts.Close()

	config := DefaultClientConfig()
	config.FallbackPorts = nil
	factory := NewFactory(config)

	c1, c2 := factory.ClientForHost(ts.URL).(*HostClient), factory.ClientForHost(ts.URL).(*HostClient)
	if c1.httpClient.Transport != c2.httpClient.Transport {
		t.Error("factory must hand every client the same shared transport")
	}

	for range 5 {
		if _, err := factory.ClientForHost(ts.URL).ListCVDs(t.Context()); err != nil {
			t.Fatalf("ListCVDs: %v", err)
		}
	}
	if got := newConns.Load(); got != 1 {
		t.Errorf("expected 1 TCP connection reused across factory clients, got %d", got)
	}
}

// The shared transport pools per host, but its idle pool is an LRU bounded by a
// fleet-wide total as well. A total below the number of hosts one instance
// touches within IdleConnTimeout evicts exactly the connection the next call
// needs, so the second sweep below re-dials every host instead of reusing what
// the first left idle. Reconcilers polling a fleet then pay a fresh dial per
// call.
func TestFactoryPoolsConnectionsAcrossAFleet(t *testing.T) {
	t.Parallel()
	const hosts = 16

	var newConns atomic.Int32
	servers := make([]*httptest.Server, hosts)
	for i := range servers {
		ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"cvds": []}`))
		}))
		ts.Config.ConnState = func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				newConns.Add(1)
			}
		}
		ts.Start()
		servers[i] = ts
	}
	defer func() {
		for _, ts := range servers {
			ts.Close() // Also closes the pooled idle connections client-side.
		}
	}()

	config := DefaultClientConfig()
	config.FallbackPorts = nil
	factory := NewFactory(config)

	for sweep := range 2 {
		for i, ts := range servers {
			if _, err := factory.ClientForHost(ts.URL).ListCVDs(t.Context()); err != nil {
				t.Fatalf("sweep %d, host %d: ListCVDs: %v", sweep, i, err)
			}
		}
	}

	// A connection returns to the idle pool asynchronously after body EOF, so a
	// straggler or two is expected; a thrashing pool re-dials all 16.
	if got := newConns.Load(); got > hosts+4 {
		t.Errorf("connections = %d over two sweeps of %d hosts, want at most %d: the fleet-wide idle total is evicting connections that are about to be needed",
			got, hosts, hosts+4)
	}
}

func TestPostNotRetriedAfterRequestReachesServer(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	// Kill the connection mid-exchange: the client sees EOF but the server may
	// already have executed the request.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	}))
	defer ts.Close()

	config := DefaultClientConfig()
	config.FallbackPorts = nil
	config.MaxRetries = 3
	config.RetryDelay = time.Millisecond

	client := NewHostClient(ts.URL, config)
	err := client.doRequestWithRetry(t.Context(), http.MethodPost, "/cvds", map[string]string{"k": "v"}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls.Load() != 1 {
		t.Errorf("POST that reached the server must not be retried, got %d attempts", calls.Load())
	}
}

func TestGetRetriedOnMidRequestFailure(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.Write([]byte(`{"cvds": []}`))
	}))
	defer ts.Close()

	config := DefaultClientConfig()
	config.FallbackPorts = nil
	config.MaxRetries = 3
	config.RetryDelay = time.Nanosecond // Also exercises the zero-jitter guard.

	client := NewHostClient(ts.URL, config)
	if _, err := client.ListCVDs(t.Context()); err != nil {
		t.Fatalf("expected GET retry to succeed, got %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 attempts, got %d", calls.Load())
	}
}

func TestCanRetry(t *testing.T) {
	t.Parallel()
	dialErr := &url.Error{Op: "Post", URL: "http://h", Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused"),
	}}
	eofErr := fmt.Errorf("%w: %w", ErrHostUnreachable, &url.Error{Op: "Post", URL: "http://h", Err: io.EOF})
	timeoutErr := &url.Error{Op: "Post", URL: "http://h", Err: context.DeadlineExceeded}

	tests := []struct {
		name   string
		method string
		err    error
		want   bool
	}{
		{"GET on EOF", http.MethodGet, eofErr, true},
		{"DELETE on timeout", http.MethodDelete, timeoutErr, true},
		{"POST on dial failure", http.MethodPost, dialErr, true},
		{"POST on EOF", http.MethodPost, eofErr, false},
		{"POST on timeout", http.MethodPost, timeoutErr, false},
		{"POST on retryable 503", http.MethodPost, &HostError{StatusCode: 503, Retryable: true}, false},
		{"GET on non-retryable 400", http.MethodGet, &HostError{StatusCode: 400}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canRetry(tt.method, tt.err); got != tt.want {
				t.Errorf("canRetry(%s, %v) = %v, want %v", tt.method, tt.err, got, tt.want)
			}
		})
	}
}

func TestWaitForOperationAbortsOnTerminalStatus(t *testing.T) {
	t.Parallel()
	// 4xx = the wait itself is rejected; 500 = HO's rendering of an operation
	// that completed with an error (only 503 means still-in-progress). Both
	// are terminal: polling to MaxWait would burn minutes on a settled outcome.
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				w.Write([]byte(`{"error": "operation rejected"}`))
			}))
			defer ts.Close()

			config := DefaultClientConfig()
			config.FallbackPorts = nil
			config.RetryDelay = time.Millisecond
			config.MaxWait = 5 * time.Second

			client := NewHostClient(ts.URL, config)
			err := client.waitForOperation(t.Context(), "op-1", nil, queuedBudget)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if errors.Is(err, ErrOperationTimeout) {
				t.Errorf("expected immediate abort, not a MaxWait timeout: %v", err)
			}
			var hostErr *HostError
			if !errors.As(err, &hostErr) || hostErr.StatusCode != status {
				t.Errorf("expected HostError with status %d, got: %v", status, err)
			}
			if calls.Load() != 1 {
				t.Errorf("expected 1 poll before aborting, got %d", calls.Load())
			}
		})
	}
}

func TestWaitForOperationPollsThroughServiceUnavailable(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Legacy :wait returns 503 while the operation is still in progress.
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error": "operation in progress"}`))
			return
		}
		w.Write([]byte(`{"cvds": []}`))
	}))
	defer ts.Close()

	config := DefaultClientConfig()
	config.FallbackPorts = nil
	config.RetryDelay = time.Millisecond
	config.MaxWait = 10 * time.Second

	client := NewHostClient(ts.URL, config)
	var resp CreateCVDResponse
	if err := client.waitForOperation(t.Context(), "op-1", &resp, queuedBudget); err != nil {
		t.Fatalf("expected polling through 503 to succeed, got %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("expected 3 polls, got %d", calls.Load())
	}
}

func TestWaitForOperationBailsOnUnreachableHost(t *testing.T) {
	t.Parallel()
	// A host that dies mid-operation: every :wait poll fails to connect. The
	// wait must give up after a few polls rather than re-poll a corpse until
	// MaxWait (which would burn a worker for minutes on a dead host).
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := ts.URL
	ts.Close() // nothing listens now: connection refused on every poll

	config := DefaultClientConfig()
	config.FallbackPorts = nil
	config.RetryDelay = time.Millisecond
	config.MaxWait = 30 * time.Second // large on purpose: a regression would hang ~30s

	client := NewHostClient(url, config)
	start := time.Now()
	err := client.waitForOperation(t.Context(), "op-1", nil, queuedBudget)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if errors.Is(err, ErrOperationTimeout) {
		t.Errorf("bailed via MaxWait timeout, want a fast unreachable bail: %v", err)
	}
	if !errors.Is(err, ErrHostUnreachable) {
		t.Errorf("want the error to wrap ErrHostUnreachable, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("wait took %v; expected a fast bail well under MaxWait", elapsed)
	}
}

func TestWaitForOperationSurvivesHeldLongPoll(t *testing.T) {
	t.Parallel()
	// The adopt-race trigger: the HO holds :wait for
	// its whole wait window (2 min upstream) before answering 503, so a
	// per-poll bound at the normal-call Timeout means the in-progress signal
	// can never arrive. Every poll on a busy operation dies as a fake
	// connection failure and the wait abandons a healthy in-flight operation.
	// Here the hold (300ms) exceeds Timeout (50ms) by the same shape; the wait
	// must poll through on WaitPollTimeout and succeed.
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			time.Sleep(300 * time.Millisecond) // hold the long-poll, then in-progress
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error": "operation in progress"}`))
			return
		}
		w.Write([]byte(`{"cvds": []}`))
	}))
	defer ts.Close()

	config := DefaultClientConfig()
	config.FallbackPorts = nil
	config.Timeout = 50 * time.Millisecond // normal-call bound, far below the hold
	config.WaitPollTimeout = 5 * time.Second
	config.RetryDelay = time.Millisecond
	config.MaxWait = 10 * time.Second

	client := NewHostClient(ts.URL, config)
	var resp CreateCVDResponse
	if err := client.waitForOperation(t.Context(), "op-1", &resp, queuedBudget); err != nil {
		t.Fatalf("wait abandoned a healthy held long-poll: %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("expected 3 polls, got %d", calls.Load())
	}
}

func TestWaitForOperationPollDeadlineBoundsWedgedHost(t *testing.T) {
	t.Parallel()
	// A host that accepts the connection but never answers (hung). The
	// poll deadline, which in production exceeds the HO
	// hold window so a healthy host would have answered, must still convert
	// it into a bounded unreachable bail instead of hanging until MaxWait.
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // never answer within the test
	}))
	defer ts.Close()
	defer close(block) // LIFO: unblock handlers first so Close can drain

	config := DefaultClientConfig()
	config.FallbackPorts = nil
	config.WaitPollTimeout = 100 * time.Millisecond
	config.RetryDelay = time.Millisecond
	config.MaxWait = 30 * time.Second // large on purpose: a regression would hang ~30s

	client := NewHostClient(ts.URL, config)
	start := time.Now()
	err := client.waitForOperation(t.Context(), "op-1", nil, queuedBudget)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, ErrHostUnreachable) {
		t.Errorf("want the error to wrap ErrHostUnreachable, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("wait took %v; expected a fast bounded bail", elapsed)
	}
}

func TestNormalCallBoundedByTimeout(t *testing.T) {
	t.Parallel()
	// The http.Client no longer carries a client-wide Timeout (it would cap
	// :wait long-polls); normal calls must still be bounded, by the per-call
	// context deadline in doSingleRequest.
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer ts.Close()
	defer close(block) // LIFO: unblock handlers first so Close can drain

	config := DefaultClientConfig()
	config.FallbackPorts = nil
	config.Timeout = 50 * time.Millisecond
	config.MaxRetries = 0

	client := NewHostClient(ts.URL, config)
	start := time.Now()
	if _, err := client.ListCVDs(t.Context()); err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("normal call took %v; expected the %v per-call bound to apply", elapsed, config.Timeout)
	}
}

// The call budget bounds a whole doRequestWithRetry call, covering attempts,
// port sweeps, and backoffs. Its expiry must still classify as an
// unreachable-host 502 and not a bare context deadline, so the last attempt's
// failure stays in the chain (abortErr).
func TestCallBudgetBoundsDeadHostAs502(t *testing.T) {
	t.Parallel()
	// A host that accepts and never answers: without the budget this call
	// would burn MaxRetries+1 attempts of Timeout each, plus backoffs.
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer ts.Close()
	defer close(block) // LIFO: unblock handlers first so Close can drain

	config := DefaultClientConfig()
	config.FallbackPorts = nil
	config.Timeout = 5 * time.Second
	config.RetryDelay = time.Millisecond
	config.CallBudget = 100 * time.Millisecond

	client := NewHostClient(ts.URL, config)
	start := time.Now()
	_, err := client.ListCVDs(t.Context())
	elapsed := time.Since(start)

	hostErr, ok := errors.AsType[*HostError](err)
	if !ok {
		t.Fatalf("expected *HostError at the exit boundary, got %T: %v", err, err)
	}
	if got := hostErr.GetStatus(); got != http.StatusBadGateway {
		t.Errorf("GetStatus = %d, want 502", got)
	}
	if !errors.Is(err, ErrHostUnreachable) {
		t.Error("budget expiry must keep ErrHostUnreachable in the chain")
	}
	if elapsed > 2*time.Second {
		t.Errorf("call took %v; want ~the 100ms budget, not the retry ladder", elapsed)
	}
}

// The budget also caps a mid-retry ladder: expiry during a backoff pause must
// return the prior attempt's failure, not a bare deadline.
func TestCallBudgetExpiryDuringBackoffKeepsLastError(t *testing.T) {
	t.Parallel()
	config := DefaultClientConfig()
	config.FallbackPorts = nil
	config.RetryDelay = 10 * time.Second // Expire mid-backoff, after attempt 1 failed.
	config.CallBudget = 100 * time.Millisecond

	client := NewHostClient("http://127.0.0.1:1", config) // Nothing listens on port 1.
	_, err := client.ListCVDs(t.Context())

	hostErr, ok := errors.AsType[*HostError](err)
	if !ok || hostErr.GetStatus() != http.StatusBadGateway {
		t.Fatalf("expected 502 HostError, got %v", err)
	}
	if !errors.Is(err, ErrHostUnreachable) {
		t.Error("the refused attempt must stay in the chain through the backoff abort")
	}
}

// The round-1 smoke gap: an impatient caller canceling a dead-host call must
// not erase the host-health evidence. Each port failure that completed while
// the call was live counts, so repeated canceled calls still trip the breaker
// and later calls fail fast instead of re-dialing the corpse.
func TestCanceledCallsStillTripBreaker(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // Port immediately refuses connections without waiting on remote/reserved ports.

	config := DefaultClientConfig()
	config.FallbackPorts = nil
	config.MaxRetries = 3
	config.RetryDelay = 10 * time.Second // The caller's cancel lands mid-backoff.
	config.CallBudget = -1               // The caller's own deadline is the abort under test.

	client := NewHostClient(srv.URL, config)
	client.breaker = newCircuitBreaker(3, time.Minute)

	for i := range 3 {
		ctx, cancel := context.WithCancel(t.Context())
		errCh := make(chan error, 1)
		go func() {
			_, err := client.ListCVDs(ctx)
			errCh <- err
		}()

		// Wait until attempt 0 fails and records its port failure before canceling
		// mid-backoff, eliminating 50ms wall-clock races under heavy -race load.
		deadline := time.Now().Add(5 * time.Second)
		for {
			client.breaker.mu.Lock()
			failures := client.breaker.failures
			client.breaker.mu.Unlock()
			if failures >= i+1 {
				break
			}
			if time.Now().After(deadline) {
				cancel()
				t.Fatalf("call %d: timed out waiting for port failure to record (failures=%d)", i, failures)
			}
			time.Sleep(time.Millisecond)
		}
		cancel()

		err := <-errCh
		if err == nil {
			t.Fatalf("call %d: expected error", i)
		}
		if errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("call %d: breaker tripped early", i)
		}
	}

	_, err := client.ListCVDs(t.Context())
	if !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("after 3 canceled dead-host calls, want ErrCircuitOpen fail-fast, got %v", err)
	}
}

// Each completed connection failure counts exactly once: the per-port records
// inside the call are the whole tally; the exit path must not re-count the
// joined chain.
func TestBreakerCountsConnectionFailuresExactlyOnce(t *testing.T) {
	t.Parallel()
	config := DefaultClientConfig()
	config.FallbackPorts = nil
	config.MaxRetries = 1 // Two attempts, one port each.
	config.RetryDelay = time.Millisecond

	client := NewHostClient("http://127.0.0.1:1", config)
	client.breaker = newCircuitBreaker(100, time.Minute)

	if _, err := client.ListCVDs(t.Context()); err == nil {
		t.Fatal("expected error")
	}
	client.breaker.mu.Lock()
	got := client.breaker.failures
	client.breaker.mu.Unlock()
	if got != 2 {
		t.Errorf("failures = %d, want exactly 2 (one per completed dial, no exit double-count)", got)
	}
}

// HTTP-level transient failures (a 503 is retryable) still count once, at
// exit. No port failure is involved, so nothing was pre-counted. A 500 with
// a JSON error body stays a HEALTHY signal (the HO answered): it must reset
// the counter, not feed it.
func TestBreakerRecordsHTTPOutcomesAtExit(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name         string
		status       int
		wantFailures int
	}{
		// Each case starts from one seeded failure, proving count vs reset.
		{"retryable 503 counts once", http.StatusServiceUnavailable, 2},
		{"answered 500 resets", http.StatusInternalServerError, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				w.Write([]byte(`{"error": "server error"}`))
			}))
			defer ts.Close()

			config := DefaultClientConfig()
			config.FallbackPorts = nil
			config.MaxRetries = 0

			client := NewHostClient(ts.URL, config)
			client.breaker = newCircuitBreaker(100, time.Minute)
			client.breaker.record(false) // A prior failure proves reset vs count.

			if _, err := client.ListCVDs(t.Context()); err == nil {
				t.Fatal("expected error")
			}
			client.breaker.mu.Lock()
			got := client.breaker.failures
			client.breaker.mu.Unlock()
			if got != tt.wantFailures {
				t.Errorf("failures = %d, want %d", got, tt.wantFailures)
			}
		})
	}
}

// A probe aborted before any dial completes records nothing but must free the
// probe slot: the next call goes through instead of ErrCircuitOpen forever.
func TestCanceledProbeDoesNotWedgeBreaker(t *testing.T) {
	t.Parallel()
	config := DefaultClientConfig()
	config.FallbackPorts = nil
	config.MaxRetries = 0
	client := NewHostClient("http://127.0.0.1:1", config)
	client.breaker = newCircuitBreaker(1, 10*time.Millisecond)
	client.breaker.record(false) // Trip it open.

	time.Sleep(20 * time.Millisecond) // Open timeout elapses: next call is the probe.

	canceled, cancel := context.WithCancel(t.Context())
	cancel() // The probe is aborted before its dial can complete.
	if _, err := client.ListCVDs(canceled); err == nil {
		t.Fatal("expected error from the canceled probe")
	}

	if _, err := client.ListCVDs(t.Context()); errors.Is(err, ErrCircuitOpen) {
		t.Error("canceled probe wedged the breaker half-open")
	}
}

// A host that dies mid-operation still trips its breaker through the :wait
// polls' completed dial failures (the wait loop records via doRequest, not
// manually).
func TestWaitForOperationUnreachableTripsBreaker(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := ts.URL
	ts.Close() // Nothing listens now: connection refused on every poll.

	config := DefaultClientConfig()
	config.FallbackPorts = nil
	config.RetryDelay = time.Millisecond
	config.MaxWait = 30 * time.Second

	client := NewHostClient(url, config)
	client.breaker = newCircuitBreaker(3, time.Minute)

	if err := client.waitForOperation(t.Context(), "op-1", nil, queuedBudget); err == nil {
		t.Fatal("expected error")
	}
	if client.breaker.allow() {
		t.Error("consecutive unreachable polls must trip the breaker")
	}
}

func TestCallBudgetResolution(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		cfg  time.Duration
		want time.Duration
	}{
		{"zero uses the default", 0, defaultCallBudget},
		{"explicit value wins", 5 * time.Second, 5 * time.Second},
		{"negative disables", -1, -1},
	} {
		c := &HostClient{config: ClientConfig{CallBudget: tt.cfg}}
		if got := c.callBudget(); got != tt.want {
			t.Errorf("%s: callBudget() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestBuildURLs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		hostURL       string
		fallbackPorts []int
		want          []string
	}{
		{"ipv4", "http://10.0.0.5:9999", []int{2080}, []string{"http://10.0.0.5:9999", "http://10.0.0.5:2080"}},
		{"ipv6", "http://[::1]:9999", []int{2080}, []string{"http://[::1]:9999", "http://[::1]:2080"}},
		{"no scheme", "10.0.0.5:9999", []int{2080}, []string{"10.0.0.5:9999", "http://10.0.0.5:2080"}},
		{"skips duplicate of primary", "https://h:2080", []int{2080, 2081}, []string{"https://h:2080", "https://h:2081"}},
		{"no fallbacks", "http://10.0.0.5:9999", nil, []string{"http://10.0.0.5:9999"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildURLs(tt.hostURL, tt.fallbackPorts)
			if len(got) != len(tt.want) {
				t.Fatalf("buildURLs(%q) = %v, want %v", tt.hostURL, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("buildURLs(%q)[%d] = %q, want %q", tt.hostURL, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestExtractHostIP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		hostURL string
		want    string
	}{
		{"http://10.0.0.5:2080", "10.0.0.5"},
		{"https://10.0.0.5", "10.0.0.5"},
		{"http://[::1]:2080", "::1"},
		{"10.0.0.5:2080", "10.0.0.5"},
	}
	for _, tt := range tests {
		if got := extractHostIP(tt.hostURL); got != tt.want {
			t.Errorf("extractHostIP(%q) = %q, want %q", tt.hostURL, got, tt.want)
		}
	}
}

func TestListDisplaysResponseUnmarshalMapFormat(t *testing.T) {
	t.Parallel()
	data := `{"displays": {"1": {"dpi":[320,320],"mode":{"windowed":[720,1280]},"refresh_rate_hz":60}}}`
	var r ListDisplaysResponse
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	want := Display{DisplayNumber: 1, Width: 720, Height: 1280, DPI: 320, RefreshRateHz: 60}
	if len(r.Displays) != 1 || r.Displays[0] != want {
		t.Errorf("got %+v, want [%+v]", r.Displays, want)
	}
}

func TestListDisplaysResponseUnmarshalRejectsBadKey(t *testing.T) {
	t.Parallel()
	data := `{"displays": {"not-a-number": {"dpi":[320],"mode":{"windowed":[720,1280]},"refresh_rate_hz":60}}}`
	var r ListDisplaysResponse
	err := json.Unmarshal([]byte(data), &r)
	if !errors.Is(err, ErrInvalidResponse) {
		t.Errorf("expected ErrInvalidResponse for non-numeric display key, got: %v", err)
	}
}

// Exit-boundary wrapping: connection failures leave the client as *HostError
// so web.MapError forwards an accurate 502, while errors.Is and IsRetryable
// keep seeing the chain through the wrap.
func TestExitWrapUnreachableCarries502(t *testing.T) {
	t.Parallel()
	config := DefaultClientConfig()
	config.FallbackPorts = nil
	config.MaxRetries = 0
	client := NewHostClient("http://127.0.0.1:1", config) // Nothing listens on port 1.

	_, err := client.ListCVDs(t.Context())
	hostErr, ok := errors.AsType[*HostError](err)
	if !ok {
		t.Fatalf("expected *HostError at the exit boundary, got %T: %v", err, err)
	}
	if got := hostErr.GetStatus(); got != http.StatusBadGateway {
		t.Errorf("GetStatus = %d, want 502", got)
	}
	if !errors.Is(err, ErrHostUnreachable) {
		t.Error("errors.Is(ErrHostUnreachable) must keep matching through the wrap")
	}
	if !IsRetryable(err) {
		t.Error("a refused connection must stay retryable through the wrap")
	}
}

// An open breaker is an unreachable-host memo: it must carry the same 502 and
// stay matchable as ErrCircuitOpen.
func TestExitWrapBreakerOpenCarries502(t *testing.T) {
	t.Parallel()
	config := DefaultClientConfig()
	config.FallbackPorts = nil
	client := NewHostClient("http://127.0.0.1:1", config)
	client.breaker = newCircuitBreaker(1, time.Minute)
	client.breaker.record(false) // Trip it.

	err := client.DeleteCVD(t.Context(), "cvd-1")
	hostErr, ok := errors.AsType[*HostError](err)
	if !ok || hostErr.GetStatus() != http.StatusBadGateway {
		t.Fatalf("breaker-open error = %v, want 502 HostError", err)
	}
	if !errors.Is(err, ErrCircuitOpen) {
		t.Error("errors.Is(ErrCircuitOpen) must keep matching through the wrap")
	}

	_, streamErr := client.DownloadBugreport(t.Context(), "any")
	hostErr, ok = errors.AsType[*HostError](streamErr)
	if !ok || hostErr.GetStatus() != http.StatusBadGateway {
		t.Fatalf("streamFile breaker-open error = %v, want 502 HostError", streamErr)
	}
}

// A non-JSON 404 mentioning a CVD carries the status AND the sentinel. The
// old bare-sentinel return downgraded a mappable 404 to an unclassified 500.
func TestParseErrorResponse404CVDCarriesStatus(t *testing.T) {
	t.Parallel()
	config := DefaultClientConfig()
	config.FallbackPorts = nil
	client := NewHostClient("http://127.0.0.1:1", config)

	err := client.parseErrorResponse(http.StatusNotFound, []byte("cvd not found"))
	hostErr, ok := errors.AsType[*HostError](err)
	if !ok || hostErr.GetStatus() != http.StatusNotFound {
		t.Fatalf("parseErrorResponse = %v, want 404 HostError", err)
	}
	if !errors.Is(err, ErrCVDNotFound) {
		t.Error("errors.Is(ErrCVDNotFound) must keep matching")
	}
}

// Artifact downloads are streamed, not buffered: MaxResponseSize governs
// ordinary JSON replies and must not truncate a bugreport ZIP, which routinely
// runs past it. Hand-rolled on purpose: the size is the subject, so the body
// is larger than any real fixture.
func TestStreamedDownloadsIgnoreTheResponseSizeCap(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte("z"), 3*1024*1024)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer ts.Close()

	config := DefaultClientConfig()
	config.FallbackPorts = nil
	config.MaxResponseSize = 1024 * 1024 // A third of the body below.

	client := NewHostClient(ts.URL, config)
	body, err := client.DownloadBugreport(t.Context(), "any")
	if err != nil {
		t.Fatalf("DownloadBugreport: %v", err)
	}
	got, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		t.Fatalf("reading the stream: %v", err)
	}
	if len(got) != len(payload) {
		t.Errorf("streamed %d bytes, want %d. The cap must not apply to downloads", len(got), len(payload))
	}
}
