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
	"sync"
	"time"
)

// ErrCircuitOpen is returned when a host's circuit breaker is open: recent calls
// to that Host Orchestrator failed repeatedly, so further calls fail fast and the
// caller backs off instead of piling up on a hung host.
var ErrCircuitOpen = errors.New("host orchestrator circuit breaker is open")

// circuitBreaker is a per-host breaker. After maxFailures consecutive failures it
// opens for openTimeout, failing calls fast; once the timeout elapses it allows a
// single probe, which closes the breaker on success or re-opens it on failure.
// It is small and dependency-free by design.
type circuitBreaker struct {
	maxFailures int
	openTimeout time.Duration

	mu       sync.Mutex
	failures int
	openedAt time.Time
	probing  bool
}

func newCircuitBreaker(maxFailures int, openTimeout time.Duration) *circuitBreaker {
	if maxFailures < 1 {
		maxFailures = 5
	}
	if openTimeout <= 0 {
		openTimeout = 30 * time.Second
	}
	return &circuitBreaker{maxFailures: maxFailures, openTimeout: openTimeout}
}

// allow reports whether a call may proceed, transitioning open -> half-open once
// the open timeout has elapsed.
func (b *circuitBreaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures < b.maxFailures {
		return true // closed
	}
	if time.Since(b.openedAt) < b.openTimeout {
		return false // open
	}
	if b.probing {
		return false // a half-open probe is already in flight
	}
	b.probing = true // half-open: let exactly one probe through
	return true
}

// record updates the breaker with a call outcome.
func (b *circuitBreaker) record(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
	if success {
		b.failures = 0
		return
	}
	b.failures++
	if b.failures >= b.maxFailures {
		b.openedAt = time.Now()
	}
}

// releaseProbe frees the half-open probe slot without recording an outcome. The
// caller uses it when a call result reflects the caller (a context
// cancellation) rather than host health: without it, a canceled probe would
// leave probing set and hold the breaker half-open, blocking every later call.
func (b *circuitBreaker) releaseProbe() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
}
