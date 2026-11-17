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
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// waitMetrics counts :wait long-poll anomalies, the signal class that
// silently abandoned in-flight operations before it was instrumented.
type waitMetrics struct {
	pollFailures   metric.Int64Counter
	abandonments   metric.Int64Counter
	budgetExpiries metric.Int64Counter
}

// waitMetricsInstance lazily builds the shared counters on first use, which
// in production is after main has installed the meter provider. Instruments
// are no-ops without a provider, so tests need no setup.
var waitMetricsInstance = sync.OnceValue(func() *waitMetrics {
	meter := otel.Meter(telemetry.ScopeName("hoclient"))
	m := &waitMetrics{}
	m.pollFailures, _ = meter.Int64Counter("cco.hoclient.wait_poll_failures",
		metric.WithDescription("Connection-level :wait poll failures, by reason (dial = host gone, timeout = no answer within the poll deadline)."))
	m.abandonments, _ = meter.Int64Counter("cco.hoclient.wait_abandoned",
		metric.WithDescription("Operation waits abandoned after consecutive unreachable polls."))
	m.budgetExpiries, _ = meter.Int64Counter("cco.hoclient.wait_budget_expired",
		metric.WithDescription("Operation waits cut short by their verb's wait budget, by tier. Non-zero in steady state means the tier is set too low; always zero against a wedged host means the budget is not reaching the poll."))
	return m
})

// pollFailure records one unreachable :wait poll, tagged with how the
// connection failed: "dial" (the request never reached the host) vs
// "timeout" (an established connection gave no answer within the deadline,
// past the HO hold window, so anomalous).
func (m *waitMetrics) pollFailure(ctx context.Context, err error) {
	reason := "timeout"
	if requestNeverSent(err) {
		reason = "dial"
	}
	m.pollFailures.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

func (m *waitMetrics) abandoned(ctx context.Context) {
	m.abandonments.Add(ctx, 1)
}

// budgetExpired records one wait cut short by its verb's budget. Tagged by tier
// because the two tiers are answerable separately: the interactive one has no
// measured tail behind it, so this counter is how its value stops being a
// judgement.
func (m *waitMetrics) budgetExpired(ctx context.Context, tier string) {
	m.budgetExpiries.Add(ctx, 1, metric.WithAttributes(attribute.String("tier", tier)))
}

// IsTerminalOperationError reports whether a WaitCVDOperation error is the
// operation's own settled outcome (the HO's stored failure, or an unknown
// operation, for example the HO restarted and lost its in-memory operations).
// A journaled operation whose wait ends this way must be CLEARED and the
// journey restarted. Transient wait failures (unreachable, MaxWait) are not
// terminal, and the next attempt resumes the same operation.
func IsTerminalOperationError(err error) bool {
	if errors.Is(err, ErrCVDNotFound) {
		return true
	}
	if hostErr, ok := errors.AsType[*HostError](err); ok {
		return isTerminalWaitStatus(hostErr.StatusCode)
	}
	return false
}

// waitForOperation waits for an async operation to complete under b's bound.
//
// The bound is applied as a WRAPPED CONTEXT, never as the poll loop's deadline,
// which is why this wraps pollOperation instead of passing it an argument.
// The loop deadline is consulted only BETWEEN polls, while a single poll runs
// for WaitPollTimeout, necessarily 150s so that it outlives the HO's hold
// window. A deadline-shaped 8s budget therefore yields up to 150s of wait on a
// hung host, past the deadline it was derived from, while passing every test
// where the host answers. Only a context cancels mid-poll.
//
// A budget expiry is translated into ErrOperationTimeout so withExitStatus
// renders it as a 504 rather than the unclassified context abort pollOperation
// returns, which MapError would fall through to a 500, replacing a broken
// response with a masked one. A deadline the CALLER brought is passed through
// untouched: it is their cancellation, not a verdict about the host.
func (c *HostClient) waitForOperation(ctx context.Context, name string, result any, b waitBudget) error {
	ceiling := c.waitCeiling(b)
	limit := c.waitLimit(b)
	if limit <= 0 {
		return c.pollOperation(ctx, name, result, ceiling)
	}

	waitCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	err := c.pollOperation(waitCtx, name, result, ceiling)
	if err != nil && ctx.Err() == nil && waitCtx.Err() != nil {
		waitMetricsInstance().budgetExpired(ctx, b.tier)
		return withExitStatus(fmt.Errorf("%w: operation %s did not complete within its %v %s budget",
			ErrOperationTimeout, name, limit, b.tier))
	}
	return err
}

// pollOperation long-polls an operation's :wait until it settles. The HO :wait
// endpoint signals "still in progress" ONLY as 503 at the END
// of its hold window (WaitOperationDuration, 2 min upstream); an operation
// that completed with an error is reported as the stored result error, 500
// for the usual internal failure. So 503 (and 502/504/429 from proxies) keep
// polling, while 500 and definitive 4xx are terminal outcomes that abort
// immediately instead of burning the whole MaxWait window re-polling an
// operation that will never succeed.
//
// Each poll is bounded by WaitPollTimeout, which exceeds the HO hold window:
// a healthy host therefore ALWAYS answers a poll (result, 503 at hold expiry,
// or a proxy's 504) before the poll deadline, which is what makes a
// connection-level poll failure a true host-health signal. With a shorter
// per-poll timeout the 503 could never arrive and every slow operation would
// be misdiagnosed as an unreachable host and abandoned mid-flight, which is
// the adopt-race trigger (a retry then races the still-running server-side
// launch).
func (c *HostClient) pollOperation(ctx context.Context, name string, result any, ceiling time.Duration) error {
	path := "/operations/" + name + "/:wait"

	deadline := time.Now().Add(ceiling)
	delay := c.config.RetryDelay
	unreachable := 0

	for time.Now().Before(deadline) {
		err := c.doRequest(ctx, http.MethodPost, path, nil, result, c.waitPollTimeout())
		if err == nil {
			return nil
		}

		// Check if context canceled
		if ctx.Err() != nil {
			return fmt.Errorf("wait for operation aborted: %w", ctx.Err())
		}

		if hostErr, ok := errors.AsType[*HostError](err); ok && isTerminalWaitStatus(hostErr.StatusCode) {
			return fmt.Errorf("operation %s failed: %w", name, err)
		}
		if errors.Is(err, ErrCVDNotFound) {
			return fmt.Errorf("operation %s failed: %w", name, err)
		}

		// A connection-level failure is a host-health signal (the poll
		// deadline outlives the HO hold window, so a healthy host would have
		// answered, see above). The breaker learns from each completed dial
		// failure inside doRequest (recordPortFailure), so a host that dies
		// mid-operation trips its breaker (protecting other workers); here we
		// additionally give up after a few consecutive failed polls instead
		// of re-polling a corpse for the whole MaxWait window.
		if errors.Is(err, ErrHostUnreachable) {
			waitMetricsInstance().pollFailure(ctx, err)
			unreachable++
			if unreachable >= maxWaitUnreachablePolls {
				waitMetricsInstance().abandoned(ctx)
				return withExitStatus(fmt.Errorf("operation %s: host unreachable across %d polls: %w", name, unreachable, err))
			}
		} else {
			unreachable = 0
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for operation interrupted: %w", ctx.Err())
		case <-time.After(min(withJitter(delay), maxBackoff)):
			// 1.5x exponential backoff, capped.
			delay = min(delay*3/2, maxBackoff)
		}
	}

	return withExitStatus(fmt.Errorf("%w: operation %s did not complete within %v", ErrOperationTimeout, name, ceiling))
}

// defaultWaitPollTimeout bounds one :wait poll. It MUST exceed the HO's
// WaitOperationDuration (2 min upstream). See the waitForOperation contract.
const defaultWaitPollTimeout = 150 * time.Second

// waitPollTimeout returns the per-poll bound, defaulting a zero config value
// (hand-built ClientConfig literals) to a safe one rather than falling back
// to the much shorter normal-call Timeout.
func (c *HostClient) waitPollTimeout() time.Duration {
	if c.config.WaitPollTimeout > 0 {
		return c.config.WaitPollTimeout
	}
	return defaultWaitPollTimeout
}

// maxWaitUnreachablePolls bounds consecutive connection-level failures while
// long-polling an operation's :wait before giving up. It caps a single wait
// against a dead host (dial failures surface within seconds) instead of the
// whole MaxWait window; the HO's 503 in-progress signal is not a connection
// failure, so it resets the counter and polling continues.
const maxWaitUnreachablePolls = 3

// isTerminalWaitStatus reports whether a :wait response status is a terminal
// operation outcome. 500 is the operation's own stored failure (HO renders a
// done-with-error result via NewInternalError); 4xx except 429 will never
// resolve. 503 is HO's in-progress signal, and 502/504/429 are proxy/transient.
func isTerminalWaitStatus(status int) bool {
	if status == http.StatusInternalServerError {
		return true
	}
	return status >= 400 && status < 500 && status != http.StatusTooManyRequests
}
