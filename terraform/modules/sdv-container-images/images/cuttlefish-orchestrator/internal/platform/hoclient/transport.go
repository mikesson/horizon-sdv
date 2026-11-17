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
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// HostClient is a client for a specific host orchestrator
type HostClient struct {
	baseURLs       []string      // Ordered list of base URLs to try
	activeURLIndex atomic.Uint32 // Index of last successful URL (sticky routing)
	hostIP         string        // Extracted host IP for span attributes
	httpClient     *http.Client
	config         ClientConfig
	breaker        *circuitBreaker // Per-host breaker; nil disables (set by Factory).
}

// maxIdleConnsTotal bounds the shared transport's idle pool across the WHOLE
// fleet, where ClientConfig.MaxIdleConns bounds it per host. The pool is an
// LRU: a total below the number of hosts an instance reaches within
// IdleConnTimeout evicts exactly the connection the next call needs, so a
// reconciler cycling over the fleet pays a fresh TCP dial per call. 1024 sits
// above any host working set one instance serves while still bounding the file
// descriptors and reader goroutines it can hold idle. The legacy CO proxy's
// mirrored transport carries the same bound.
const maxIdleConnsTotal = 1024

// newOTelTransport builds the OpenTelemetry-instrumented transport used to
// reach Host Orchestrators. The Factory builds exactly one and shares it
// across all clients it hands out, so connections are pooled per host instead
// of paying a fresh TLS handshake per client.
//
// The legacy CO passthrough (compat/legacy/co_proxy.go, hoProxyTransport)
// mirrors this transport's dial/pool values by copy rather than by
// sharing, because its response-timing semantics differ by contract. When
// tuning a dial/pool value here, revisit the mirror there.
func newOTelTransport(config ClientConfig) http.RoundTripper {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: config.AllowInsecure, //nolint:gosec // Intentional: HO uses self-signed certs
		},
		MaxIdleConns:        maxIdleConnsTotal,
		MaxIdleConnsPerHost: config.MaxIdleConns,
		IdleConnTimeout:     config.IdleConnTimeout,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		// No ResponseHeaderTimeout: the HO :wait endpoint legitimately holds
		// its long-poll far past any normal-call timeout, so time bounds are
		// per-request context deadlines (doSingleRequest): config.Timeout
		// for regular calls, WaitPollTimeout for :wait polls.
		ExpectContinueTimeout: 1 * time.Second,
	}

	return otelhttp.NewTransport(transport,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return "HostOrchestrator " + r.Method + " " + r.URL.Path
		}),
	)
}

// NewHostClient creates a standalone client for a specific host with its own
// transport. Production code should obtain clients from Factory.ClientForHost,
// which shares one pooled transport across all clients.
func NewHostClient(hostURL string, config ClientConfig) *HostClient {
	return newHostClientWithTransport(hostURL, config, newOTelTransport(config))
}

// newHostClientWithTransport creates a client using the given (typically
// shared) transport.
func newHostClientWithTransport(hostURL string, config ClientConfig, transport http.RoundTripper) *HostClient {
	return &HostClient{
		baseURLs: buildURLs(hostURL, config.FallbackPorts),
		hostIP:   extractHostIP(hostURL),
		httpClient: &http.Client{
			Transport: transport,
			// No client-wide Timeout: it would cap :wait long-polls below the
			// HO's hold window. Every request is bounded by a per-call context
			// deadline in doSingleRequest instead.
		},
		config: config,
	}
}

// parseHostURL parses hostURL, assuming http when no scheme is present.
func parseHostURL(hostURL string) (*url.URL, error) {
	u, err := url.Parse(hostURL)
	if err != nil || u.Host == "" {
		u, err = url.Parse("http://" + hostURL)
		if err != nil {
			return nil, fmt.Errorf("invalid host URL %q: %w", hostURL, err)
		}
	}
	return u, nil
}

// buildURLs creates a list of URLs with different ports to try.
func buildURLs(hostURL string, fallbackPorts []int) []string {
	urls := []string{hostURL}

	u, err := parseHostURL(hostURL)
	if err != nil {
		return urls
	}

	for _, port := range fallbackPorts {
		fallbackURL := u.Scheme + "://" + net.JoinHostPort(u.Hostname(), strconv.Itoa(port))
		// Only add if it's different from the original URL
		if fallbackURL != hostURL {
			urls = append(urls, fallbackURL)
		}
	}

	return urls
}

// extractHostIP extracts the host IP from a URL for use in span attributes.
func extractHostIP(hostURL string) string {
	u, err := parseHostURL(hostURL)
	if err != nil {
		return hostURL
	}
	return u.Hostname()
}

// streamFile GETs a file-serving endpoint and hands the response body to the
// caller unread. Unlike doSingleRequest it neither buffers nor size-caps the
// body (artifact ZIPs routinely exceed MaxResponseSize), and it sets no
// per-request deadline. The transfer is bounded by ctx alone, since a body
// deadline would abort large downloads mid-stream. Connection setup still
// fails fast via the transport's dial/TLS timeouts, and the host breaker
// gates and records the attempt (connection outcome only, because a mid-stream
// caller abort is not a host-health signal).
func (c *HostClient) streamFile(ctx context.Context, path string) (io.ReadCloser, error) {
	if c.breaker != nil && !c.breaker.allow() {
		return nil, withExitStatus(ErrCircuitOpen)
	}

	startIdx := c.activeURLIndex.Load()
	n := uint32(len(c.baseURLs)) //nolint:gosec // len of URLs will never overflow uint32
	var errs []error
	for i := range n {
		idx := (startIdx + i) % n
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURLs[idx]+path, http.NoBody)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if isConnectionError(err) {
				c.recordPortFailure(ctx)
				errs = append(errs, fmt.Errorf("%w: %w", ErrHostUnreachable, err))
				continue // GET is idempotent: try the next port.
			}
			c.recordBreakerOutcome(ctx, err)
			return nil, &HostError{StatusCode: 0, Message: msgRequestFailed, Retryable: IsRetryable(err), Cause: err}
		}

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			err := c.parseErrorResponse(resp.StatusCode, body)
			c.recordBreakerOutcome(ctx, err)
			return nil, err
		}

		c.activeURLIndex.Store(idx)
		if c.breaker != nil {
			c.breaker.record(true)
		}
		return resp.Body, nil
	}

	err := errors.Join(errs...)
	c.recordBreakerOutcome(ctx, err)
	return nil, withExitStatus(err)
}

// doRequestWithRetry gates the retrying request on the host's circuit breaker
// and bounds the whole call (attempts, port sweeps, and backoffs) by the
// call budget: when the breaker is open it fails fast (ErrCircuitOpen) so the
// caller backs off, and a dead host costs at most the budget instead of the
// full retry ladder, so the server's classified 502 reaches clients before
// their own caps expire and they disconnect.
func (c *HostClient) doRequestWithRetry(ctx context.Context, method, path string, body, result any) error {
	if c.breaker != nil && !c.breaker.allow() {
		return withExitStatus(ErrCircuitOpen)
	}
	if budget := c.callBudget(); budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}
	err := c.requestWithRetries(ctx, method, path, body, result)
	c.recordBreakerOutcome(ctx, err)
	return withExitStatus(err)
}

// msgRequestFailed is the HostError message for a transport-level failure
// (no HTTP status), shared by every request path.
const msgRequestFailed = "request failed"

// defaultCallBudget bounds one doRequestWithRetry call. 20s beats the
// clients' own caps with margin (cfctl's per-request timeout is 30s), so a
// dead host produces this service's classified 502 rather than a client-side
// timeout and a raced disconnect (which Cloud Run may answer by recycling
// the instance).
const defaultCallBudget = 20 * time.Second

// Fallbacks for the config fields whose zero value is not a usable setting.
//
// ClientConfig documents a zero-means-default contract, and CallBudget,
// WaitPollTimeout and both circuit-breaker fields honor it. These three did
// not, which made a hand-built ClientConfig literal (the case
// waitPollTimeout's comment already warns about) fail in ways that look like
// host faults: a zero response cap rejects every body as too large, and a zero
// MaxWait reports every operation as timed out before polling it once.
const (
	defaultRequestTimeout = 30 * time.Second
	// defaultMaxWait is the ceiling for every wait that does not name its own.
	// A create DOES name its own, because createBudget scales it by instance
	// count and that wait covers N guests booting. This is therefore the floor
	// a single-instance create still takes, and the whole bound for verbs
	// acting on a group that already exists.
	defaultMaxWait         = device.TransientProgressWindow
	defaultMaxResponseSize = 10 * 1024 * 1024
)

// requestTimeout returns the normal-call deadline, defaulting a zero config
// value rather than leaving the request unbounded.
func (c *HostClient) requestTimeout() time.Duration {
	if c.config.Timeout > 0 {
		return c.config.Timeout
	}
	return defaultRequestTimeout
}

// maxWait returns the operation-wait ceiling, defaulting a zero config value
// rather than expiring the wait before its first poll.
func (c *HostClient) maxWait() time.Duration {
	if c.config.MaxWait > 0 {
		return c.config.MaxWait
	}
	return defaultMaxWait
}

// maxResponseSize returns the decoded-body cap, defaulting a zero config value
// rather than rejecting every response.
func (c *HostClient) maxResponseSize() int64 {
	if c.config.MaxResponseSize > 0 {
		return c.config.MaxResponseSize
	}
	return defaultMaxResponseSize
}

// callBudget returns the per-call bound, defaulting a zero config value
// (hand-built ClientConfig literals) to the safe default; < 0 disables it.
func (c *HostClient) callBudget() time.Duration {
	if c.config.CallBudget != 0 {
		return c.config.CallBudget
	}
	return defaultCallBudget
}

// recordPortFailure feeds one completed connection failure to the host
// breaker, called at the moment a port attempt fails with a connection-level
// error while the call context is still live: the failure is then the host's
// own doing (dial timeout, refusal, reset), which is real health evidence even
// if the overall call is canceled later. Recording only at call exit discarded
// exactly this evidence whenever an impatient client (or the call budget)
// canceled first, so a slow-dead host never tripped its breaker.
func (c *HostClient) recordPortFailure(ctx context.Context) {
	if c.breaker != nil && ctx.Err() == nil {
		c.breaker.record(false)
	}
}

// recordBreakerOutcome feeds a call's exit result to the host breaker.
// Connection failures were already counted per completed port attempt
// (recordPortFailure). Cancellation is monotone, so a connection-class chain
// reaching this point with a live context has recorded every one of its port
// failures, and counting the chain again would double-count. The exit path
// records what only it can see: success, HTTP-level transient failures
// (5xx/429), and healthy responses (including 4xx); a dead context releases
// any half-open probe slot so a canceled probe can't hold the breaker half-open.
func (c *HostClient) recordBreakerOutcome(ctx context.Context, err error) {
	if c.breaker == nil {
		return
	}
	switch {
	case err == nil:
		c.breaker.record(true)
	case ctx.Err() != nil:
		c.breaker.releaseProbe()
	case errors.Is(err, ErrHostUnreachable):
		// Already counted port by port as the failures completed.
	case IsRetryable(err):
		c.breaker.record(false)
	default:
		c.breaker.record(true)
	}
}

// maxBackoff caps exponential retry backoff between attempts.
const maxBackoff = 30 * time.Second

// withJitter adds up to 50% random jitter to a backoff delay.
func withJitter(delay time.Duration) time.Duration {
	if half := int64(delay) / 2; half > 0 {
		delay += time.Duration(rand.Int64N(half)) //nolint:gosec // Non-crypto use: backoff jitter
	}
	return delay
}

// requestWithRetries performs an HTTP request with retry logic for transient
// errors. Non-idempotent methods only retry when the request never reached the
// server (see canRetry).
func (c *HostClient) requestWithRetries(ctx context.Context, method, path string, body, result any) error {
	var lastErr error
	delay := c.config.RetryDelay

	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return abortErr(ctx, lastErr)
			case <-time.After(withJitter(delay)):
			}
			delay = min(delay*2, maxBackoff)
		}

		err := c.doRequest(ctx, method, path, body, result, c.requestTimeout())
		if err == nil {
			return nil
		}

		lastErr = err

		if ctx.Err() != nil {
			return abortErr(ctx, lastErr)
		}

		if !canRetry(method, err) {
			return err
		}
	}

	return fmt.Errorf("request failed after %d attempts: %w", c.config.MaxRetries+1, lastErr)
}

// abortErr renders a context abort without discarding the last attempt's
// failure: when the call budget expires on a dead host, the chain must keep
// ErrHostUnreachable so the exit wrap classifies the call as a 502 instead
// of a bare deadline. lastErr is nil when no attempt had failed yet.
func abortErr(ctx context.Context, lastErr error) error {
	if lastErr == nil {
		return fmt.Errorf("request aborted: %w", ctx.Err())
	}
	return fmt.Errorf("request aborted: %w (last attempt: %w)", ctx.Err(), lastErr)
}

// doRequest attempts to perform an HTTP request, iterating over available base
// URLs on connection failure. timeout bounds each URL attempt individually;
// <= 0 falls back to the config's normal-call timeout.
func (c *HostClient) doRequest(ctx context.Context, method, path string, body, result any, timeout time.Duration) error {
	startIdx := c.activeURLIndex.Load()
	n := uint32(len(c.baseURLs)) //nolint:gosec // len of URLs will never overflow uint32
	var errs []error

	for i := range n {
		idx := (startIdx + i) % n
		reqURL := c.baseURLs[idx] + path

		err := c.doSingleRequest(ctx, method, reqURL, body, result, timeout)
		if err == nil {
			c.activeURLIndex.Store(idx)
			return nil
		}

		// Application errors (4xx, 5xx) are not connection problems.
		// Do not try other URLs; return immediately.
		if !isConnectionError(err) && !errors.Is(err, ErrHostUnreachable) {
			return err
		}
		c.recordPortFailure(ctx)
		errs = append(errs, err)

		// A non-idempotent request that may have reached this URL must not be
		// replayed against another port of the same host.
		if !isIdempotent(method) && !requestNeverSent(err) {
			break
		}
	}

	return errors.Join(errs...)
}

// doSingleRequest performs a single HTTP request to a specific URL, bounded
// by the given timeout (<= 0 falls back to config.Timeout). The deadline
// covers headers AND body read. The http.Client carries no timeout of its
// own so that :wait long-polls can outlive the normal-call bound.
func (c *HostClient) doSingleRequest(ctx context.Context, method, reqURL string, body, result any, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = c.requestTimeout()
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Wrap connection errors
		if isConnectionError(err) {
			return fmt.Errorf("%w: %w", ErrHostUnreachable, err)
		}
		return &HostError{
			StatusCode: 0,
			Message:    msgRequestFailed,
			Retryable:  IsRetryable(err),
			Cause:      err,
		}
	}
	defer resp.Body.Close()

	// Limit response size to prevent OOM
	limitedReader := io.LimitReader(resp.Body, c.maxResponseSize()+1)
	respBody, err := io.ReadAll(limitedReader)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	// Check if response was truncated
	if int64(len(respBody)) > c.maxResponseSize() {
		return fmt.Errorf("%w: response exceeded %d bytes", ErrResponseTooLarge, c.maxResponseSize())
	}

	if resp.StatusCode >= 400 {
		return c.parseErrorResponse(resp.StatusCode, respBody)
	}

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidResponse, err)
		}
	}

	return nil
}
