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
	"errors"
	"fmt"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/tests/harness/fakeho"
)

// ClientConfig documents a zero-means-default contract, and a hand-built literal
// is the case its own comments warn about. Three fields did not honor it, and
// both failures below LOOKED like host faults rather than misconfiguration:
//
//   - MaxResponseSize 0 rejected every response body as "too large"
//   - MaxWait 0 made the poll loop's deadline `now`, so every operation was
//     reported timed out before a single poll went out
//
// Neither was reachable from production (both factories start from
// DefaultClientConfig), which is exactly why nothing caught them.
func TestZeroValueConfigIsUsable(t *testing.T) {
	t.Parallel()
	server, err := fakeho.New()
	if err != nil {
		t.Fatalf("starting fake host: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	server.SeedGroup(fakeho.SingleHostIP, "g", "1", "Running")

	// Everything at its zero value except the port sweep, which would otherwise
	// dial two ports the fake does not serve.
	client := NewHostClient(fmt.Sprintf("http://%s:%d", fakeho.SingleHostIP, server.Port()),
		ClientConfig{FallbackPorts: nil})

	// A plain call: exercises the response cap.
	cvds, err := client.ListCVDs(t.Context())
	if err != nil {
		t.Fatalf("ListCVDs with a zero-value config: %v", err)
	}
	if len(cvds) != 1 {
		t.Errorf("got %d CVDs, want the seeded one", len(cvds))
	}

	// A lifecycle verb: exercises the operation wait.
	if err := client.StopCVD(t.Context(), "g", "1"); err != nil {
		t.Errorf("StopCVD with a zero-value config: %v", err)
	}
}

// A zero response cap rejected every body. Asserted directly because the failure
// surfaced as ErrResponseTooLarge, which reads as a host serving something huge.
func TestZeroMaxResponseSizeDoesNotRejectEveryBody(t *testing.T) {
	t.Parallel()
	client := NewHostClient("http://127.0.0.1:1", ClientConfig{})

	if got := client.maxResponseSize(); got != defaultMaxResponseSize {
		t.Errorf("maxResponseSize() = %d, want the %d default", got, defaultMaxResponseSize)
	}
	if got := client.maxResponseSize(); got <= 0 {
		t.Error("a non-positive cap rejects every response with a body")
	}
}

// A zero MaxWait made the loop deadline `now`, so waitForOperation returned
// ErrOperationTimeout without ever polling, so an operation was reported as
// timed out without ever being checked.
func TestZeroMaxWaitDoesNotExpireBeforeTheFirstPoll(t *testing.T) {
	t.Parallel()
	client := NewHostClient("http://127.0.0.1:1", ClientConfig{})

	if got := client.maxWait(); got != defaultMaxWait {
		t.Errorf("maxWait() = %v, want the %v default", got, defaultMaxWait)
	}

	// The budget derivation clamps to MaxWait, so a zero value there must not
	// collapse a tier's wait either.
	if got := client.waitLimit(interactiveBudget); got <= 0 {
		t.Errorf("interactive waitLimit with a zero MaxWait = %v, want a usable bound", got)
	}
}

// A zero Timeout left a normal call with no deadline of its own, so a host that
// accepted a connection and never answered held the caller until its context
// ended. For a worker, that is the 30-minute handle timeout.
func TestZeroTimeoutDoesNotLeaveRequestsUnbounded(t *testing.T) {
	t.Parallel()
	client := NewHostClient("http://127.0.0.1:1", ClientConfig{})

	if got := client.requestTimeout(); got != defaultRequestTimeout {
		t.Errorf("requestTimeout() = %v, want the %v default", got, defaultRequestTimeout)
	}
}

// A configured value must still win: these accessors default a zero, they do not
// override a setting the caller made.
func TestConfiguredValuesOverrideTheDefaults(t *testing.T) {
	t.Parallel()
	client := NewHostClient("http://127.0.0.1:1", ClientConfig{
		Timeout:         time.Second,
		MaxWait:         2 * time.Second,
		MaxResponseSize: 3,
	})

	if got := client.requestTimeout(); got != time.Second {
		t.Errorf("requestTimeout() = %v, want the configured 1s", got)
	}
	if got := client.maxWait(); got != 2*time.Second {
		t.Errorf("maxWait() = %v, want the configured 2s", got)
	}
	if got := client.maxResponseSize(); got != 3 {
		t.Errorf("maxResponseSize() = %d, want the configured 3", got)
	}
}

// DefaultClientConfig must agree with the fallbacks, or the two paths into the
// client disagree about what "default" means.
func TestDefaultConfigMatchesTheZeroValueFallbacks(t *testing.T) {
	t.Parallel()
	cfg := DefaultClientConfig()

	if cfg.Timeout != defaultRequestTimeout {
		t.Errorf("DefaultClientConfig().Timeout = %v, fallback is %v", cfg.Timeout, defaultRequestTimeout)
	}
	if cfg.MaxWait != defaultMaxWait {
		t.Errorf("DefaultClientConfig().MaxWait = %v, fallback is %v", cfg.MaxWait, defaultMaxWait)
	}
	if cfg.MaxResponseSize != defaultMaxResponseSize {
		t.Errorf("DefaultClientConfig().MaxResponseSize = %d, fallback is %d",
			cfg.MaxResponseSize, defaultMaxResponseSize)
	}
	if cfg.CallBudget != defaultCallBudget {
		t.Errorf("DefaultClientConfig().CallBudget = %v, fallback is %v", cfg.CallBudget, defaultCallBudget)
	}
	if cfg.WaitPollTimeout != defaultWaitPollTimeout {
		t.Errorf("DefaultClientConfig().WaitPollTimeout = %v, fallback is %v",
			cfg.WaitPollTimeout, defaultWaitPollTimeout)
	}
}

// The response cap must still reject an oversized body. The fix defaults the
// zero, it does not remove the bound.
func TestResponseCapStillRejectsAnOversizedBody(t *testing.T) {
	t.Parallel()
	server, err := fakeho.New()
	if err != nil {
		t.Fatalf("starting fake host: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	server.SeedGroup(fakeho.SingleHostIP, "g", "1", "Running")

	client := NewHostClient(fmt.Sprintf("http://%s:%d", fakeho.SingleHostIP, server.Port()),
		ClientConfig{FallbackPorts: nil, MaxResponseSize: 1})

	if _, err := client.ListCVDs(t.Context()); !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("a 1-byte cap must still reject the listing, got %v", err)
	}
}
