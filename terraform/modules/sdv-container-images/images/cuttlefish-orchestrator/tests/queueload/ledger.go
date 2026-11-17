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

package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Event kinds recorded in the per-instance ledgers. Together they form the
// operation history the post-run checkers validate: claims and outcomes come
// from the instrumented queue, effects from the synthetic handler.
const (
	evClaim    = "claim"
	evEffect   = "effect"
	evComplete = "complete"
	evFail     = "fail"
)

// event is one parsed ledger line.
type event struct {
	kind    string
	task    string
	holder  string
	attempt int
	at      time.Time
	expiry  time.Time // claim only: the lease expiry the holder wrote
	dead    bool      // fail only: dead-lettered rather than rescheduled
}

// ledger is an append-only event log, one line per queue-visible event. Writes
// are unbuffered so a SIGKILLed instance loses at most the line being written,
// because the ledger must never claim events that did not happen. Timestamps
// use the unskewed process clock (the clock-skew hook shifts only the spanner
// package), so lines from different instances stay comparable.
type ledger struct {
	mu sync.Mutex
	f  *os.File
}

func openLedger(dir string, instanceIdx, pid int) (*ledger, error) {
	path := filepath.Join(dir, fmt.Sprintf("ledger-%d-%d.log", instanceIdx, pid))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening ledger %s: %w", path, err)
	}
	return &ledger{f: f}, nil
}

func (l *ledger) claim(taskID, holder string, attempt int, expiry time.Time) {
	l.append(fmt.Sprintf("%s %s %s %d %d %d",
		evClaim, taskID, holder, attempt, time.Now().UnixMicro(), expiry.UnixMicro()))
}

// effect records a successful handling attempt and returns an error because it
// is the one event whose loss would fabricate a missed effect: the handler
// only reports success after the effect line is durably appended.
func (l *ledger) effect(taskID, holder string, attempt int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := fmt.Fprintf(l.f, "%s %s %s %d %d\n",
		evEffect, taskID, holder, attempt, time.Now().UnixMicro()); err != nil {
		return fmt.Errorf("appending effect line: %w", err)
	}
	return nil
}

func (l *ledger) complete(taskID, holder string) {
	l.append(fmt.Sprintf("%s %s %s 0 %d", evComplete, taskID, holder, time.Now().UnixMicro()))
}

func (l *ledger) fail(taskID, holder string, attempt int, dead bool) {
	verdict := "retry"
	if dead {
		verdict = "dead"
	}
	l.append(fmt.Sprintf("%s %s %s %d %d %s",
		evFail, taskID, holder, attempt, time.Now().UnixMicro(), verdict))
}

// append writes one observability event line. A SIGKILL can only sever the
// last line written, and within one process the claim line always precedes its
// handler's effect line, so a lost claim implies no effect followed, and an
// effect without a matching claim is always a real anomaly.
func (l *ledger) append(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintln(l.f, line)
}

func (l *ledger) close() error {
	if err := l.f.Close(); err != nil {
		return fmt.Errorf("closing ledger: %w", err)
	}
	return nil
}

// readEvents parses every ledger in dir into one unordered event slice.
// Truncated or unparseable lines (a SIGKILL can sever the final line) are
// skipped, not errors.
func readEvents(dir string) ([]event, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "ledger-*.log"))
	if err != nil {
		return nil, fmt.Errorf("globbing ledgers: %w", err)
	}
	var events []event
	for _, path := range matches {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("opening %s: %w", path, err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if e, ok := parseEvent(sc.Text()); ok {
				events = append(events, e)
			}
		}
		err = sc.Err()
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
	}
	return events, nil
}

func parseEvent(line string) (event, bool) {
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return event{}, false
	}
	attempt, err := strconv.Atoi(fields[3])
	if err != nil {
		return event{}, false
	}
	usec, err := strconv.ParseInt(fields[4], 10, 64)
	if err != nil {
		return event{}, false
	}
	e := event{
		kind:    fields[0],
		task:    fields[1],
		holder:  fields[2],
		attempt: attempt,
		at:      time.UnixMicro(usec),
	}
	switch e.kind {
	case evClaim:
		if len(fields) < 6 {
			return event{}, false
		}
		expUsec, err := strconv.ParseInt(fields[5], 10, 64)
		if err != nil {
			return event{}, false
		}
		e.expiry = time.UnixMicro(expUsec)
	case evFail:
		if len(fields) < 6 {
			return event{}, false
		}
		e.dead = fields[5] == "dead"
	case evEffect, evComplete:
	default:
		return event{}, false
	}
	return e, true
}
