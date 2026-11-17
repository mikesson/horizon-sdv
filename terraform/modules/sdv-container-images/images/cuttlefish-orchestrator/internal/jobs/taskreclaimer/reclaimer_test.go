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

package taskreclaimer

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeQueue struct {
	mu     sync.Mutex
	sweeps int
	ret    int
	err    error
}

func (f *fakeQueue) Sweep(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweeps++
	return f.ret, f.err
}

func (f *fakeQueue) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sweeps
}

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestSweepReportsQueueError(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{err: errors.New("spanner unavailable")}
	s := New(Config{Enabled: true, Interval: time.Minute}, q, testLogger())
	assert.Error(t, s.sweep(t.Context()))
}

func TestSweepCountsReclaims(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{ret: 3}
	s := New(Config{Enabled: true, Interval: time.Minute}, q, testLogger())
	require.NoError(t, s.sweep(t.Context()))
	assert.Equal(t, 1, q.count())
}

func TestStartStopSweeps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &fakeQueue{}
		s := New(Config{Enabled: true, Interval: time.Minute}, q, testLogger(),
			WithLeaderCheck(func() bool { return true }))
		s.Start(t.Context())
		synctest.Wait()
		assert.Equal(t, 1, q.count(), "one immediate sweep at start")
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		assert.Equal(t, 3, q.count(), "one sweep per tick")
		s.Stop()
	})
}

func TestSweepSkippedWhenNotLeader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &fakeQueue{}
		s := New(Config{Enabled: true, Interval: time.Minute}, q, testLogger(),
			WithLeaderCheck(func() bool { return false }))
		s.Start(t.Context())
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		assert.Zero(t, q.count(), "non-leader does not sweep")
		s.Stop()
	})
}

func TestSweepRunsWhenNoLeaderCheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &fakeQueue{}
		s := New(Config{Enabled: true, Interval: time.Minute}, q, testLogger())
		s.Start(t.Context())
		synctest.Wait()
		assert.Equal(t, 1, q.count(), "an unset leader check always runs")
		s.Stop()
	})
}

func TestDisabledDoesNotStart(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	s := New(Config{Enabled: false, Interval: 5 * time.Millisecond}, q, testLogger())
	s.Start(t.Context())
	s.Stop()
	assert.Zero(t, q.count())
}
