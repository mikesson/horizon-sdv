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
	"net/http"
	"strings"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/tests/harness/fakeho"
)

// The arithmetic test, and the reason it exists: a wait budget sized against a
// request deadline directly is wrong by whatever the POST ahead of it and the
// handler work after it consume. An earlier design of these tiers put the
// interactive verbs at a 20s wait inside a 30s deadline, which overruns by 10s
// once the 20s POST is counted, and no test could see it, because a stopwatch
// around the wait passes at any budget value.
//
// This is the assertion that goes red for that, and it covers the whole serial
// chain rather than the wait alone.
func TestVerbWaitBudgetsFitTheirEnvelopes(t *testing.T) {
	t.Parallel()
	client := NewHostClient("http://127.0.0.1:1", DefaultClientConfig())

	for _, b := range []waitBudget{interactiveBudget, extendedBudget} {
		t.Run(b.tier, func(t *testing.T) {
			total := client.callBudget() + client.waitLimit(b) + b.reserve
			if total > b.envelope {
				t.Errorf("%s tier overruns its envelope: POST %v + wait %v + reserve %v = %v > %v",
					b.tier, client.callBudget(), client.waitLimit(b), b.reserve, total, b.envelope)
			}
			if client.waitLimit(b) <= 0 {
				t.Errorf("%s tier derived a non-positive wait: %v", b.tier, client.waitLimit(b))
			}
		})
	}
}

// A client configured with a CallBudget wider than its own envelope cannot
// honor the envelope, because an unbounded POST cannot be reserved against, but
// it must degrade to a short wait rather than to an already-expired context,
// which would fail every verb instantly.
func TestWaitLimitFloorsRatherThanGoingNegative(t *testing.T) {
	t.Parallel()
	config := DefaultClientConfig()
	config.CallBudget = ExtendedVerbEnvelope * 2
	client := NewHostClient("http://127.0.0.1:1", config)

	if got := client.waitLimit(extendedBudget); got != minWaitLimit {
		t.Errorf("waitLimit with an oversized CallBudget = %v, want the %v floor", got, minWaitLimit)
	}
}

// A wait the queue drives answers to the worker's handle timeout, not to any
// client connection, so it takes no envelope and MaxWait stays its only bound.
// Asserted because "no budget" is a decision here, and a zero value that
// silently meant 1s instead would be invisible on the happy path.
func TestQueuedWaitsTakeNoEnvelope(t *testing.T) {
	t.Parallel()
	client := NewHostClient("http://127.0.0.1:1", DefaultClientConfig())

	if got := client.waitLimit(queuedBudget); got != 0 {
		t.Errorf("queued waitLimit = %v, want 0 (MaxWait alone)", got)
	}
}

// The budget must cancel a poll that is already in flight, which is why it is a
// wrapped context and not the poll loop's deadline. The loop deadline is
// consulted only BETWEEN polls, so a deadline-shaped implementation of the same
// number lets a hung host hold for a whole WaitPollTimeout instead, 150s in
// production, and 50x the budget here.
//
// Both tiers are covered because they reach the wait through different code:
// the interactive verbs via lifecycleCVD, and bugreport via its own inline POST
// plus wait. A budget wired only into lifecycleCVD passes the first and misses
// the second.
func TestWaitBudgetCancelsAnInFlightPoll(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tier   string
		budget waitBudget
		call   func(*HostClient) error
	}{
		{"interactive", interactiveBudget, func(c *HostClient) error {
			return c.StopCVD(t.Context(), contractGroup, contractName)
		}},
		{"extended", extendedBudget, func(c *HostClient) error {
			_, err := c.CreateBugreport(t.Context(), contractGroup, false)
			return err
		}},
	} {
		t.Run(tc.tier, func(t *testing.T) {
			t.Parallel()
			// Above minWaitLimit on purpose: at 100ms the floor supplied the
			// bound instead of the derivation, and the elapsed assertion below
			// passed without the budget ever being exercised.
			const budget = 1500 * time.Millisecond
			server, client := newContractHost(t, func(c *ClientConfig) {
				// Squeeze the derived wait to `budget` through the real
				// derivation rather than injecting a value, so the test cannot
				// pass against a budget the production path never computes.
				c.CallBudget = tc.budget.envelope - tc.budget.reserve - budget
				// Both left large on purpose: a regression that bounds the loop
				// instead of the context hangs for a poll, and one that applies
				// no budget at all hangs until MaxWait.
				c.WaitPollTimeout = 5 * time.Second
				c.MaxWait = time.Minute
			})
			server.SetOperationsWedged(fakeho.SingleHostIP, true)

			// The premise, checked: without this the test can measure a bound it
			// did not set and read the result as a pass.
			if got := client.waitLimit(tc.budget); got != budget {
				t.Fatalf("setup produced a %v wait, not the intended %v", got, budget)
			}

			start := time.Now()
			err := tc.call(client)
			elapsed := time.Since(start)

			if !errors.Is(err, ErrOperationTimeout) {
				t.Fatalf("wedged %s verb error = %v, want ErrOperationTimeout", tc.tier, err)
			}
			hostErr, ok := errors.AsType[*HostError](err)
			if !ok || hostErr.GetStatus() != http.StatusGatewayTimeout {
				t.Errorf("a budget expiry must reach the API as 504, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.tier) {
				t.Errorf("expiry does not name its tier, so the instrument cannot attribute it: %v", err)
			}
			// Three outcomes are distinguishable here: the budget applied to the
			// context ends at ~1.5s, applied as the loop deadline it ends at a
			// whole poll (5s), and not applied at all it ends at MaxWait (60s).
			if elapsed < budget {
				t.Errorf("wait ran %v, shorter than its own %v budget: something else ended it", elapsed, budget)
			}
			if elapsed > 3*time.Second {
				t.Errorf("wait ran %v on a %v budget: the bound is being applied between polls, not to the poll", elapsed, budget)
			}
			// A budget expiry is CCO giving up, not the operation settling: the
			// host is still working, so a retry must be able to resume it.
			if IsTerminalOperationError(err) {
				t.Error("a budget expiry must stay non-terminal")
			}
		})
	}
}

// A deadline the caller brought is their cancellation, not a verdict about the
// host, and it must not be relabeled as one. The distinction matters because
// only the budget's own expiry is a 504 the client should read as "the host did
// not finish in time".
func TestCallerDeadlineIsNotReportedAsABudgetExpiry(t *testing.T) {
	t.Parallel()
	server, client := newContractHost(t)
	server.SetOperationsWedged(fakeho.SingleHostIP, true)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	err := client.StopCVD(ctx, contractGroup, contractName)
	if errors.Is(err, ErrOperationTimeout) {
		t.Errorf("caller cancellation was relabeled as a host operation timeout: %v", err)
	}
}
