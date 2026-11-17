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
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// retryRoundTripper adds retry logic: exponential backoff for idempotent
// requests and Retry-After-driven retries for rate-limited requests.
type retryRoundTripper struct {
	next       http.RoundTripper
	maxRetries int
	// backoffBase is the first retry delay; zero means one second.
	// Tests override it to keep retries fast.
	backoffBase time.Duration
}

// RoundTrip implements http.RoundTripper, retrying failed attempts per
// retryDecision and rewinding the request body before each re-send.
func (r *retryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// An Idempotency-Key exists to make the request replay-safe (the server
	// dedupes on it), so a keyed POST retries like a GET.
	idempotent := req.Method == http.MethodGet ||
		req.Method == http.MethodDelete ||
		req.Method == http.MethodHead ||
		req.Header.Get("Idempotency-Key") != ""

	for attempt := 0; ; attempt++ {
		resp, err := r.next.RoundTrip(req)

		delay, retry := r.retryDecision(resp, err, idempotent, attempt)
		// Waiting past the context deadline (Client.Timeout or --timeout)
		// would trade the server's response for a bare timeout error;
		// surface the response instead.
		if deadline, ok := req.Context().Deadline(); ok && delay > time.Until(deadline) {
			retry = false
		}
		// A prior attempt consumed the body; a request that cannot be
		// replayed must not be re-sent (it would go out empty).
		if !retry || !rewindBody(req) {
			if err != nil {
				return nil, err
			}
			return resp, nil
		}
		if resp != nil {
			_ = resp.Body.Close()
		}

		select {
		case <-time.After(delay):
		case <-req.Context().Done():
			return nil, fmt.Errorf("canceled while waiting to retry: %w", req.Context().Err())
		}
	}
}

// retryDecision returns the delay before the next attempt and whether the
// result (resp, err) of the given zero-based attempt should be retried.
func (r *retryRoundTripper) retryDecision(resp *http.Response, err error, idempotent bool, attempt int) (time.Duration, bool) {
	// On the final attempt, return whatever we got so the caller can read the
	// response body (or the error) instead of a retried-away result.
	if attempt == r.maxRetries {
		return 0, false
	}

	// Network error: retry only idempotent requests.
	if err != nil {
		return r.backoff(attempt), idempotent
	}

	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		// 429: honor Retry-After (delta-seconds or HTTP-date) for any method.
		// Without a usable Retry-After only idempotent requests retry, with
		// the normal backoff.
		if delay, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
			return delay, true
		}
		return r.backoff(attempt), idempotent
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		// Transient server errors: retry only idempotent requests.
		return r.backoff(attempt), idempotent
	default:
		return 0, false
	}
}

// backoff returns the exponential delay following the given zero-based
// attempt: 1s, 2s, 4s at the default base, plus up to 50% jitter.
func (r *retryRoundTripper) backoff(attempt int) time.Duration {
	base := r.backoffBase
	if base <= 0 {
		base = time.Second
	}
	delay := base << attempt
	if half := delay / 2; half > 0 {
		delay += rand.N(half) //nolint:gosec // Retry jitter needs no cryptographic randomness.
	}
	return delay
}

// parseRetryAfter interprets a Retry-After header value as either
// delta-seconds or an HTTP-date (RFC 9110 section 10.2.3).
func parseRetryAfter(header string) (time.Duration, bool) {
	if header == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(header); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if date, err := http.ParseTime(header); err == nil {
		return max(time.Until(date), 0), true
	}
	return 0, false
}

// rewindBody restores req.Body for a retry after a prior attempt consumed it.
// It reports false when the request carried a body that cannot be replayed
// (GetBody unset or failing), in which case the request must not be retried.
func rewindBody(req *http.Request) bool {
	if req.GetBody == nil {
		return req.Body == nil
	}
	body, err := req.GetBody()
	if err != nil {
		return false
	}
	req.Body = body
	return true
}
