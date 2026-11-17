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

package audit

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
)

// fakeRepo is an in-memory audit.Repository. failFirst fails that many
// initial WriteBatch attempts, failAll fails every attempt, and a non-nil
// gate blocks WriteBatch until the gate is closed or the context expires.
type fakeRepo struct {
	failFirst int
	failAll   bool
	gate      chan struct{}

	mu       sync.Mutex
	attempts int
	batches  [][]audit.Entry
}

func (r *fakeRepo) WriteBatch(ctx context.Context, entries []audit.Entry) error {
	if r.gate != nil {
		select {
		case <-r.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts++
	if r.failAll || r.attempts <= r.failFirst {
		return errors.New("spanner unavailable")
	}
	r.batches = append(r.batches, slices.Clone(entries))
	return nil
}

func (r *fakeRepo) writeAttempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts
}

func (r *fakeRepo) writtenBatches() [][]audit.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.batches)
}

func (r *fakeRepo) writtenCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, b := range r.batches {
		n += len(b)
	}
	return n
}

// warnCounter counts Warn-level records so tests can assert rate limiting.
type warnCounter struct {
	mu    sync.Mutex
	warns int
}

func (h *warnCounter) Enabled(context.Context, slog.Level) bool { return true }

func (h *warnCounter) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		h.mu.Lock()
		h.warns++
		h.mu.Unlock()
	}
	return nil
}

func (h *warnCounter) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *warnCounter) WithGroup(string) slog.Handler      { return h }

func (h *warnCounter) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.warns
}

func testConfig() LoggerConfig {
	return LoggerConfig{
		BatchSize:     3,
		FlushInterval: time.Minute,
		BufferSize:    16,
		MaxRetries:    2,
		RetryBackoff:  100 * time.Millisecond,
		CloseTimeout:  5 * time.Second,
	}
}

func testEntry(id string) audit.Entry {
	return audit.Entry{ID: id, Action: audit.ActionDeviceCreate, ResourceType: audit.ResourceDevice}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestFlushOnFullBatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &fakeRepo{}
		l := NewLogger(repo, testConfig(), discardLogger())

		l.LogAsync(testEntry("a"))
		l.LogAsync(testEntry("b"))
		l.LogAsync(testEntry("c"))
		synctest.Wait()

		batches := repo.writtenBatches()
		require.Len(t, batches, 1, "full batch flushes without waiting for the interval")
		assert.Len(t, batches[0], 3)
		assert.Zero(t, l.Dropped())

		l.Close()
	})
}

func TestFlushOnInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &fakeRepo{}
		cfg := testConfig()
		cfg.BatchSize = 100
		cfg.FlushInterval = 5 * time.Second
		l := NewLogger(repo, cfg, discardLogger())

		l.LogAsync(testEntry("a"))
		l.LogAsync(testEntry("b"))
		synctest.Wait()
		assert.Empty(t, repo.writtenBatches(), "partial batch waits for the interval")

		time.Sleep(6 * time.Second)
		synctest.Wait()

		batches := repo.writtenBatches()
		require.Len(t, batches, 1)
		assert.Len(t, batches[0], 2)

		l.Close()
	})
}

func TestBoundedRetryThenDropOnPersistentFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &fakeRepo{failAll: true}
		cfg := testConfig()
		cfg.BatchSize = 2
		l := NewLogger(repo, cfg, discardLogger())

		l.LogAsync(testEntry("a"))
		l.LogAsync(testEntry("b"))
		time.Sleep(time.Second) // Covers the 100ms + 200ms retry backoffs.
		synctest.Wait()

		assert.Equal(t, 3, repo.writeAttempts(), "initial attempt plus MaxRetries")
		assert.Equal(t, int64(2), l.Dropped(), "the whole batch is dropped, not requeued")
		assert.Empty(t, repo.writtenBatches())

		// The dropped batch must not come back on later ticks.
		time.Sleep(2 * cfg.FlushInterval)
		synctest.Wait()
		assert.Equal(t, 3, repo.writeAttempts())

		l.Close()
	})
}

func TestRetrySucceedsAfterTransientFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &fakeRepo{failFirst: 1}
		cfg := testConfig()
		cfg.BatchSize = 2
		l := NewLogger(repo, cfg, discardLogger())

		l.LogAsync(testEntry("a"))
		l.LogAsync(testEntry("b"))
		time.Sleep(time.Second)
		synctest.Wait()

		assert.Equal(t, 2, repo.writeAttempts())
		batches := repo.writtenBatches()
		require.Len(t, batches, 1)
		assert.Len(t, batches[0], 2)
		assert.Zero(t, l.Dropped())

		l.Close()
	})
}

func TestDropOnFullBufferCountsAndRateLimitsWarnings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		repo := &fakeRepo{gate: gate}
		warns := &warnCounter{}
		cfg := testConfig()
		cfg.BatchSize = 1
		cfg.BufferSize = 2
		l := NewLogger(repo, cfg, slog.New(warns))

		// Block the flusher inside WriteBatch so nothing drains the buffer.
		l.LogAsync(testEntry("a"))
		synctest.Wait()

		l.LogAsync(testEntry("b"))
		l.LogAsync(testEntry("c"))
		assert.Zero(t, l.Dropped(), "buffer has capacity for two entries")

		l.LogAsync(testEntry("d"))
		l.LogAsync(testEntry("e"))
		assert.Equal(t, int64(2), l.Dropped())
		assert.Equal(t, 1, warns.count(), "only the first drop in the window logs")

		close(gate)
		synctest.Wait()
		assert.Equal(t, 3, repo.writtenCount(), "buffered entries still flush")

		l.Close()
	})
}

func TestCloseFlushesRemainder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &fakeRepo{}
		cfg := testConfig()
		cfg.BatchSize = 100
		l := NewLogger(repo, cfg, discardLogger())

		l.LogAsync(testEntry("a"))
		l.LogAsync(testEntry("b"))
		l.LogAsync(testEntry("c"))
		l.Close()

		assert.Equal(t, 3, repo.writtenCount())
		assert.Zero(t, l.Dropped())
	})
}

func TestCloseBoundedByDeadlineWhenRepositoryHangs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &fakeRepo{gate: make(chan struct{})} // Never released: writes block until ctx expiry.
		cfg := testConfig()
		cfg.BatchSize = 100
		cfg.CloseTimeout = time.Second
		l := NewLogger(repo, cfg, discardLogger())

		l.LogAsync(testEntry("a"))
		l.LogAsync(testEntry("b"))
		synctest.Wait()

		start := time.Now()
		l.Close()
		assert.LessOrEqual(t, time.Since(start), 2*time.Second, "close respects CloseTimeout")
		assert.Equal(t, int64(2), l.Dropped(), "undeliverable final batch is dropped, not hung on")
	})
}

func TestCloseIdempotentAndConcurrent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &fakeRepo{}
		l := NewLogger(repo, testConfig(), discardLogger())
		l.LogAsync(testEntry("a"))

		var wg sync.WaitGroup
		for range 3 {
			wg.Go(func() {
				l.Close()
			})
		}
		wg.Wait()
		l.Close() // Close after close returns instead of hanging on the joined flusher.

		assert.Equal(t, 1, repo.writtenCount(), "remainder flushed exactly once")
	})
}

func TestLogAsyncAfterCloseDropsWithoutPanic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &fakeRepo{}
		l := NewLogger(repo, testConfig(), discardLogger())
		l.Close()

		l.LogAsync(testEntry("late"))

		assert.Equal(t, int64(1), l.Dropped())
		assert.Zero(t, repo.writtenCount())
	})
}

func TestConfigDefaultsFillUnsetFields(t *testing.T) {
	t.Parallel()
	got := LoggerConfig{BatchSize: 7, MaxRetries: -1}.withDefaults()
	want := DefaultLoggerConfig()
	want.BatchSize = 7
	assert.Equal(t, want, got)

	assert.Zero(t, LoggerConfig{}.withDefaults().MaxRetries,
		"zero MaxRetries means no retries, not the default")
}
