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
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBreakerOpensAfterConsecutiveFailures(t *testing.T) {
	t.Parallel()
	b := newCircuitBreaker(3, time.Minute)
	for range 2 {
		assert.True(t, b.allow())
		b.record(false)
	}
	assert.True(t, b.allow(), "still closed below the threshold")
	b.record(false) // third consecutive failure trips it open
	assert.False(t, b.allow(), "open after reaching the failure threshold")
}

func TestBreakerSuccessResetsFailures(t *testing.T) {
	t.Parallel()
	b := newCircuitBreaker(3, time.Minute)
	b.record(false)
	b.record(false)
	b.record(true) // a success resets the counter
	b.record(false)
	b.record(false)
	assert.True(t, b.allow(), "failures reset by the intervening success")
}

func TestBreakerHalfOpenClosesOnProbeSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newCircuitBreaker(2, 30*time.Second)
		b.record(false)
		b.record(false)
		assert.False(t, b.allow(), "open")

		time.Sleep(31 * time.Second)
		assert.True(t, b.allow(), "one probe allowed after the open timeout")
		assert.False(t, b.allow(), "further calls blocked while a probe is in flight")

		b.record(true)
		assert.True(t, b.allow(), "closed after a successful probe")
	})
}

func TestBreakerReopensOnProbeFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newCircuitBreaker(2, 30*time.Second)
		b.record(false)
		b.record(false)

		time.Sleep(31 * time.Second)
		assert.True(t, b.allow(), "half-open probe")
		b.record(false) // probe fails
		assert.False(t, b.allow(), "re-opens immediately")

		time.Sleep(31 * time.Second)
		assert.True(t, b.allow(), "probe again after another open timeout")
	})
}

// A probe whose outcome reflects the caller (context cancellation) is reported
// via releaseProbe rather than record; the breaker must not stay stuck half-open.
func TestBreakerReleaseProbeDoesNotWedge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newCircuitBreaker(2, 30*time.Second)
		b.record(false)
		b.record(false)

		time.Sleep(31 * time.Second)
		assert.True(t, b.allow(), "half-open probe")
		assert.False(t, b.allow(), "probe slot taken")

		b.releaseProbe() // canceled probe: no outcome recorded

		assert.True(t, b.allow(), "probe slot freed for another attempt")
	})
}

func TestFactoryBreakerCacheSharesPerHost(t *testing.T) {
	t.Parallel()
	f := NewFactory(ClientConfig{})
	b1 := f.breakerFor("http://h1:8080")
	assert.Same(t, b1, f.breakerFor("http://h1:8080"), "same host shares one breaker")
	assert.NotSame(t, b1, f.breakerFor("http://h2:8080"), "different hosts get different breakers")
}

func TestFactoryBreakerCacheBounded(t *testing.T) {
	t.Parallel()
	f := NewFactory(ClientConfig{})
	f.maxBreakers = 4
	// Distinct hosts that never recur (the churn case): each becomes cold and must
	// age out on rotation rather than accumulating forever.
	for i := range 1000 {
		f.breakerFor(fmt.Sprintf("http://host-%d:8080", i))
	}
	assert.LessOrEqual(t, len(f.active)+len(f.prev), 2*f.maxBreakers,
		"churned host breakers must age out, keeping the cache bounded")
}

func TestFactoryBreakerPromotionSurvivesRotation(t *testing.T) {
	t.Parallel()
	f := NewFactory(ClientConfig{})
	f.maxBreakers = 2
	hot := f.breakerFor("http://hot:8080")
	f.breakerFor("http://a:8080") // active: hot, a
	f.breakerFor("http://b:8080") // active full -> rotate: prev={hot,a}, active={b}
	assert.Same(t, hot, f.breakerFor("http://hot:8080"),
		"a host still in use is promoted across a rotation, not dropped")
}
