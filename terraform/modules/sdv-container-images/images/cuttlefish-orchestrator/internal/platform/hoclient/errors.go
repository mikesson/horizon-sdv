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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// Error types for categorization
var (
	// ErrHostUnreachable indicates the host orchestrator cannot be reached
	ErrHostUnreachable = errors.New("host orchestrator unreachable")

	// ErrOperationTimeout indicates an operation took too long
	ErrOperationTimeout = errors.New("operation timed out")

	// ErrCVDNotFound indicates the requested CVD does not exist
	ErrCVDNotFound = errors.New("CVD not found")

	// ErrInvalidResponse indicates the response was malformed
	ErrInvalidResponse = errors.New("invalid response from host orchestrator")

	// ErrResponseTooLarge indicates the response exceeded size limits
	ErrResponseTooLarge = errors.New("response too large")
)

// HostError wraps host orchestrator errors with additional context
type HostError struct {
	StatusCode int
	Message    string
	Details    string
	Retryable  bool
	Cause      error
}

func (e *HostError) Error() string {
	// StatusCode 0 means no HTTP exchange happened (connection failure,
	// breaker open); "status 0" would be noise.
	msg := "host orchestrator error: " + e.Message
	if e.StatusCode != 0 {
		msg = fmt.Sprintf("host orchestrator error (status %d): %s", e.StatusCode, e.Message)
	}
	if e.Details != "" {
		msg += " - " + e.Details
	}
	return msg
}

func (e *HostError) Unwrap() error {
	return e.Cause
}

// GetStatus returns the HTTP status code for Huma compatibility.
// For upstream HTTP errors (4xx/5xx), the original status is preserved.
// For other errors (network issues, etc.), returns 502 Bad Gateway.
func (e *HostError) GetStatus() int {
	if e.StatusCode >= 400 && e.StatusCode < 600 {
		return e.StatusCode
	}
	return http.StatusBadGateway
}

// IsRetryable returns true if the error is transient and can be retried
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}

	// Check for HostError
	if hostErr, ok := errors.AsType[*HostError](err); ok {
		return hostErr.Retryable
	}

	// Network errors are generally retryable
	if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
		return true
	}

	// A connection closed mid-exchange surfaces as (unexpected) EOF.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	// Remaining transient failures that only surface as message text.
	errStr := strings.ToLower(err.Error())
	retryablePatterns := []string{
		"connection reset",
		"connection refused",
		"no such host",
		"i/o timeout",
		"temporary failure",
		"tls handshake timeout",
	}
	for _, pattern := range retryablePatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	return false
}

// withExitStatus gives the non-HTTP failure classes that can leave the client
// an HTTP status by wrapping them in *HostError, so web.MapError's
// StatusError passthrough renders them accurately instead of a masked 500:
// unreachable-host chains and breaker-open memos become 502 (an open breaker
// IS an unreachable-host memo), an exhausted operation wait becomes 504.
// Errors already carrying a status, context aborts, and everything else pass
// through unchanged. Retryable mirrors what IsRetryable reported for the
// unwrapped chain, so retry decisions are unaffected; errors.Is on the
// sentinels keeps matching through Cause. Applied only at the client's exit
// boundaries, so the internal retry/port-fallback logic never sees a wrap.
func withExitStatus(err error) error {
	if err == nil {
		return nil
	}
	var statusErr interface{ GetStatus() int }
	if errors.As(err, &statusErr) {
		return err
	}
	// Details keeps the chain's own text (ports tried, dial errors, operation
	// names) so the wrap adds a status without hiding what failed.
	wrap := func(status int, message string) error {
		details := err.Error()
		if details == message {
			details = ""
		}
		return &HostError{StatusCode: status, Message: message, Details: details, Retryable: IsRetryable(err), Cause: err}
	}
	switch {
	case errors.Is(err, ErrHostUnreachable):
		return wrap(0, "host orchestrator unreachable")
	case errors.Is(err, ErrCircuitOpen):
		return wrap(0, "host orchestrator circuit breaker is open")
	case errors.Is(err, ErrOperationTimeout):
		return wrap(http.StatusGatewayTimeout, "operation timed out")
	}
	return err
}

// isIdempotent reports whether a method is safe to replay against the server.
// POST is excluded: replaying a CreateCVD/AddDisplay the server may already
// have executed makes it at-least-once.
func isIdempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodDelete, http.MethodPut:
		return true
	default:
		return false
	}
}

// requestNeverSent reports whether err guarantees the request never reached
// the server (connection was never established), making a retry safe even for
// non-idempotent methods.
func requestNeverSent(err error) bool {
	if opErr, ok := errors.AsType[*net.OpError](err); ok && opErr.Op == "dial" {
		return true
	}
	_, ok := errors.AsType[*net.DNSError](err)
	return ok
}

// canRetry reports whether a failed request may be re-issued: idempotent
// methods on any transient error, non-idempotent methods only when the request
// was never sent (an EOF or timeout after a POST reached the host could
// duplicate work the server already did).
func canRetry(method string, err error) bool {
	if !IsRetryable(err) {
		return false
	}
	return isIdempotent(method) || requestNeverSent(err)
}

// parseErrorResponse converts error responses to structured errors
func (c *HostClient) parseErrorResponse(statusCode int, respBody []byte) error {
	var errResp ErrorResponse
	if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != "" {
		// Determine if error is retryable based on status code
		retryable := statusCode == 429 || // Too Many Requests
			statusCode == 502 || // Bad Gateway
			statusCode == 503 || // Service Unavailable
			statusCode == 504 // Gateway Timeout

		return &HostError{
			StatusCode: statusCode,
			Message:    errResp.Error,
			Details:    errResp.Details,
			Retryable:  retryable,
		}
	}

	// A non-JSON 404 mentioning a CVD keeps the sentinel reachable via
	// errors.Is while carrying the status. A bare sentinel here would
	// downgrade a mappable 404 to an unclassified error.
	if statusCode == 404 {
		bodyStr := string(respBody)
		if strings.Contains(strings.ToLower(bodyStr), "cvd") {
			return &HostError{
				StatusCode: statusCode,
				Message:    "CVD not found",
				Cause:      ErrCVDNotFound,
			}
		}
	}

	retryable := statusCode == 429 || statusCode >= 500

	return &HostError{
		StatusCode: statusCode,
		Message:    string(respBody),
		Retryable:  retryable,
	}
}

// isConnectionError checks if an error is a connection-level error
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}

	errStr := strings.ToLower(err.Error())
	connectionPatterns := []string{
		"connection refused",
		"connection reset",
		"no such host",
		"network is unreachable",
		"dial tcp",
	}

	for _, pattern := range connectionPatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	return false
}
