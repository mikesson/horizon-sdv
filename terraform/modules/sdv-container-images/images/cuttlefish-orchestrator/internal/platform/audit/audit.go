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

// Package audit provides the repository-backed implementation of the audit
// domain sink with bounded asynchronous batching.
package audit

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
)

// writeTimeout bounds a single WriteBatch attempt.
const writeTimeout = 30 * time.Second

// dropLogEvery rate-limits buffer-drop warnings: the first drop logs, then
// one warning per this many dropped entries.
const dropLogEvery = 1000

// Logger implements audit.Sink with bounded asynchronous batching.
// LogAsync feeds a fixed-capacity channel consumed by a single flusher
// goroutine; when the buffer is full or a batch exhausts its write retries,
// entries are dropped and counted rather than accumulated, so a Spanner
// outage costs the affected batches instead of unbounded memory.
type Logger struct {
	repo   audit.Repository
	logger *slog.Logger

	entries chan audit.Entry
	stop    chan struct{} // Closed by Close to stop intake and trigger the final drain.
	done    chan struct{} // Closed when the flusher goroutine exits.

	closeOnce sync.Once
	dropped   atomic.Int64

	batchSize     int
	flushInterval time.Duration
	maxRetries    int
	retryBackoff  time.Duration
	closeTimeout  time.Duration
}

// LoggerConfig holds configuration for the audit logger. Non-positive
// fields are replaced with the DefaultLoggerConfig values.
type LoggerConfig struct {
	// BatchSize is the maximum number of entries per WriteBatch.
	BatchSize int
	// FlushInterval is how often a partial batch is flushed.
	FlushInterval time.Duration
	// BufferSize is the LogAsync channel capacity; entries beyond it are dropped.
	BufferSize int
	// MaxRetries is how many times a failed batch write is retried before the
	// batch is dropped. Zero disables retries; negative selects the default.
	MaxRetries int
	// RetryBackoff is the delay before the first retry, doubling per attempt.
	RetryBackoff time.Duration
	// CloseTimeout bounds the final drain-and-flush performed by Close.
	CloseTimeout time.Duration
}

// DefaultLoggerConfig returns the default configuration.
func DefaultLoggerConfig() LoggerConfig {
	return LoggerConfig{
		BatchSize:     100,
		FlushInterval: 5 * time.Second,
		BufferSize:    4096,
		MaxRetries:    2,
		RetryBackoff:  500 * time.Millisecond,
		CloseTimeout:  5 * time.Second,
	}
}

// withDefaults replaces unset fields with the DefaultLoggerConfig values.
func (c LoggerConfig) withDefaults() LoggerConfig {
	d := DefaultLoggerConfig()
	if c.BatchSize <= 0 {
		c.BatchSize = d.BatchSize
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = d.FlushInterval
	}
	if c.BufferSize <= 0 {
		c.BufferSize = d.BufferSize
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = d.MaxRetries
	}
	if c.RetryBackoff <= 0 {
		c.RetryBackoff = d.RetryBackoff
	}
	if c.CloseTimeout <= 0 {
		c.CloseTimeout = d.CloseTimeout
	}
	return c
}

// NewLogger creates a new audit logger and starts its flusher goroutine.
// Callers must Close it to flush buffered entries and release the goroutine.
func NewLogger(
	repo audit.Repository,
	config LoggerConfig,
	logger *slog.Logger,
) *Logger {
	config = config.withDefaults()
	l := &Logger{
		repo:          repo,
		logger:        logger,
		entries:       make(chan audit.Entry, config.BufferSize),
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
		batchSize:     config.BatchSize,
		flushInterval: config.FlushInterval,
		maxRetries:    config.MaxRetries,
		retryBackoff:  config.RetryBackoff,
		closeTimeout:  config.CloseTimeout,
	}
	go l.run()
	return l
}

// LogAsync records an audit entry asynchronously. It never blocks: when the
// buffer is full or the logger is closed, the entry is dropped and counted.
func (l *Logger) LogAsync(entry audit.Entry) {
	select {
	case <-l.stop:
		l.countDrop("closed")
		return
	default:
	}
	select {
	case l.entries <- entry:
	default:
		l.countDrop("buffer full")
	}
}

// Dropped reports the total number of entries dropped since creation, whether
// from a full buffer, writes after Close, or exhausted batch retries.
func (l *Logger) Dropped() int64 {
	return l.dropped.Load()
}

// run is the single flusher goroutine: it batches entries and flushes on a
// full batch or on the flush interval, until Close triggers the final drain.
func (l *Logger) run() {
	defer close(l.done)
	ticker := time.NewTicker(l.flushInterval)
	defer ticker.Stop()

	// Flush writes are detached from any request context because audit
	// persistence must outlive the request that produced the entry.
	// Per-attempt timeouts are applied in flush.
	ctx := context.Background()
	batch := make([]audit.Entry, 0, l.batchSize)
	for {
		select {
		case entry := <-l.entries:
			batch = append(batch, entry)
			if len(batch) >= l.batchSize {
				l.flush(ctx, batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			l.flush(ctx, batch)
			batch = batch[:0]
		case <-l.stop:
			l.drain(batch)
			return
		}
	}
}

// drain consumes entries buffered at close time and performs the final
// flushes, bounded by closeTimeout so Close cannot hang on a dead repository.
func (l *Logger) drain(batch []audit.Entry) {
	ctx, cancel := context.WithTimeout(context.Background(), l.closeTimeout)
	defer cancel()
	for {
		select {
		case entry := <-l.entries:
			batch = append(batch, entry)
			if len(batch) >= l.batchSize {
				l.flush(ctx, batch)
				batch = batch[:0]
			}
		default:
			l.flush(ctx, batch)
			return
		}
	}
}

// flush writes one batch, retrying up to maxRetries times with exponential
// backoff before dropping the batch. It never requeues: a persistent outage
// drops batches at a bounded rate instead of growing memory at ingest rate.
func (l *Logger) flush(ctx context.Context, batch []audit.Entry) {
	if len(batch) == 0 {
		return
	}
	for attempt := 0; ; attempt++ {
		wctx, cancel := context.WithTimeout(ctx, writeTimeout)
		err := l.repo.WriteBatch(wctx, batch)
		cancel()
		if err == nil {
			l.logger.Debug("flushed audit entries", slog.Int("count", len(batch)))
			return
		}
		if attempt < l.maxRetries && ctx.Err() == nil {
			l.logger.Warn("audit batch write failed, retrying",
				slog.Int("count", len(batch)),
				slog.Int("attempt", attempt+1),
				slog.Any("error", err),
			)
			timer := time.NewTimer(l.retryBackoff << attempt)
			select {
			case <-timer.C:
				continue
			case <-ctx.Done():
				timer.Stop()
			}
		}
		// Retries exhausted or close deadline reached: drop the batch.
		total := l.dropped.Add(int64(len(batch)))
		l.logger.Error("dropping audit batch after failed writes",
			slog.Int("count", len(batch)),
			slog.Int("attempts", attempt+1),
			slog.Int64("total_dropped", total),
			slog.Any("error", err),
		)
		return
	}
}

// countDrop counts a single dropped entry and emits a rate-limited warning:
// the first drop logs, then one warning per dropLogEvery dropped entries.
func (l *Logger) countDrop(reason string) {
	total := l.dropped.Add(1)
	if total == 1 || total%dropLogEvery == 0 {
		l.logger.Warn("dropping audit entries",
			slog.String("reason", reason),
			slog.Int64("total_dropped", total),
		)
	}
}

// Close stops intake, flushes buffered entries (bounded by CloseTimeout), and
// joins the flusher. It is safe to call multiple times, including concurrently.
func (l *Logger) Close() {
	l.closeOnce.Do(func() { close(l.stop) })
	<-l.done
}

// Compile-time check that Logger implements audit.Sink.
var _ audit.Sink = (*Logger)(nil)
