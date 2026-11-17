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
	"sync"
)

// defaultMaxBreakers is the rotation threshold for the Factory's two-generation
// breaker cache. Breaker state is soft, because a dropped breaker just starts
// closed again and re-trips if its host is still failing, so the Factory
// evicts cold entries (breakers for churned or deleted hosts, whose URLs never
// recur) on generation rotation instead of retaining them forever. Live hosts
// stay cached via promotion, so memory tracks the live working set rather than
// cumulative churn.
const defaultMaxBreakers = 8192

// Factory creates clients for host orchestrators. It owns the per-host circuit
// breakers so breaker state persists across the short-lived clients it hands
// out, in a bounded two-generation cache (see breakerFor), and one shared HTTP
// transport so connections are pooled across those clients.
type Factory struct {
	config      ClientConfig
	transport   http.RoundTripper
	maxBreakers int

	mu     sync.Mutex
	active map[string]*circuitBreaker // current generation
	prev   map[string]*circuitBreaker // prior generation; dropped on the next rotation
}

// NewFactory creates a new client factory.
func NewFactory(config ClientConfig) *Factory {
	return &Factory{
		config:      config,
		transport:   newOTelTransport(config),
		maxBreakers: defaultMaxBreakers,
		active:      make(map[string]*circuitBreaker),
		prev:        make(map[string]*circuitBreaker),
	}
}

// ClientForHost creates a client for a specific host, wired to that host's
// shared circuit breaker and the factory's shared connection pool.
func (f *Factory) ClientForHost(hostURL string) Client {
	c := newHostClientWithTransport(hostURL, f.config, f.transport)
	c.breaker = f.breakerFor(hostURL)
	return c
}

// breakerFor returns the shared breaker for a host, creating it on first use.
// The cache is two generations: a lookup hits the active set, else promotes a
// still-used breaker from the prior generation. When the active set reaches
// maxBreakers it rotates, so the active set becomes prior and the old prior is
// dropped. Breakers for hosts that stop appearing (deleted/churned) age out
// while live hosts are retained.
func (f *Factory) breakerFor(hostURL string) *circuitBreaker {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.active[hostURL]; ok {
		return b
	}
	if b, ok := f.prev[hostURL]; ok {
		f.active[hostURL] = b // promote: keep a still-used breaker across rotations
		return b
	}
	if len(f.active) >= f.maxBreakers {
		f.prev = f.active
		f.active = make(map[string]*circuitBreaker, f.maxBreakers)
	}
	b := newCircuitBreaker(f.config.CircuitBreakerMaxFailures, f.config.CircuitBreakerOpenTimeout)
	f.active[hostURL] = b
	return b
}
