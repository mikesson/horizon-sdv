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
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probeTestConfig is a breaker-sensitive config: a single recorded failure
// trips the breaker and it stays open for the whole test, so any recording
// leak from the probe path is immediately visible.
func probeTestConfig() ClientConfig {
	return ClientConfig{
		Timeout:                   5 * time.Second,
		MaxRetries:                0,
		CallBudget:                -1, // disabled: attempts are bounded by Timeout
		CircuitBreakerMaxFailures: 1,
		CircuitBreakerOpenTimeout: time.Hour,
	}
}

// A ready-probe cadence against a booting (connection-refusing) host must not
// teach the breaker: probes are expected to fail for tens of seconds, and if
// they recorded, the breaker would open exactly as the host comes up and
// refuse the first real call.
func TestProbeReadyDoesNotTeachTheBreaker(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // the port now refuses connections, like a booting VM

	c := NewFactory(probeTestConfig()).ClientForHost(srv.URL)
	for range 5 { // far past the 1-failure trip point
		_, err := c.ProbeCVDs(t.Context())
		require.Error(t, err, "the dead endpoint must fail the probe")
	}

	// The breaker learned nothing: a normal call still attempts the host and
	// reports it unreachable, instead of failing fast with an open breaker.
	_, err := c.ListCVDs(t.Context())
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrCircuitOpen,
		"probe failures must not count toward the breaker")
	assert.ErrorIs(t, err, ErrHostUnreachable,
		"the normal call must still reach for the host")
}

// The bypass works in the other direction too: with the breaker open from
// recorded failures, a probe still sees a recovered Host Orchestrator while
// normal calls are refused, which is what lets a ready-probe detect the
// host coming up during the open window.
func TestProbeReadySeesRecoveryThroughOpenBreaker(t *testing.T) {
	t.Parallel()
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			http.Error(w, `{"error":"starting"}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"cvds":[]}`))
	}))
	t.Cleanup(srv.Close)

	c := NewFactory(probeTestConfig()).ClientForHost(srv.URL)

	// One recorded 503 trips the single-failure breaker.
	_, err := c.ListCVDs(t.Context())
	require.Error(t, err, "the unhealthy answer must fail the call")
	_, err = c.ListCVDs(t.Context())
	require.ErrorIs(t, err, ErrCircuitOpen, "the breaker must now refuse normal calls")

	healthy.Store(true)
	_, probeErr := c.ProbeCVDs(t.Context())
	assert.NoError(t, probeErr,
		"the probe must see the recovered host through the open breaker")
}
