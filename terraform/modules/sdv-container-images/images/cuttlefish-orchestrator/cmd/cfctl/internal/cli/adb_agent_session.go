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

package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
)

// agentClock is the time source of the agent's backoff and log summaries.
// Tests replace it so they never sleep.
type agentClock interface {
	Now() time.Time
	// Sleep waits for d or until ctx ends.
	Sleep(ctx context.Context, d time.Duration)
}

// wallClock is the production agentClock.
type wallClock struct{}

// Now returns the current time.
func (wallClock) Now() time.Time { return time.Now() }

// Sleep waits for d or until ctx ends.
func (wallClock) Sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

const (
	// dialBackoffStart and dialBackoffMax bound the hold after a failed dial.
	dialBackoffStart = time.Second
	dialBackoffMax   = 30 * time.Second

	// deviceCheckTimeout bounds the status check, because the agent's client
	// runs without a request timeout.
	deviceCheckTimeout = 30 * time.Second
)

// dialBackoff is the hold after the nth consecutive failed dial. It starts at
// dialBackoffStart and doubles up to dialBackoffMax.
func dialBackoff(n int) time.Duration {
	d := dialBackoffStart
	for i := 1; i < n && d < dialBackoffMax; i++ {
		d *= 2
	}
	return min(d, dialBackoffMax)
}

// agentSessions decides what the background agent does after each ADB
// session. It stops the agent once the device is gone. After a session that
// carried no data it starts a backoff step: no stream is dialed until the step
// ends, and the failed local connection stays open until then, because the
// local adb server redials as soon as the connection closes and each dial is
// an authenticated request to the orchestrator.
type agentSessions struct {
	deviceID  string
	instance  string
	getDevice func(ctx context.Context, id string) (*cco.Device, error)
	clock     agentClock
	log       *agentLog
	// stop ends the agent. It may be called more than once.
	stop func()
	// remoteEnded, when set, hears of each session the device stream closed.
	// It must not block.
	remoteEnded func()
	// watch, when set, hears of every ended session, so the attach's online
	// wait ends at the first failed stream dial.
	watch *onlineSessions

	mu sync.Mutex
	// failures counts the backoff steps since the last session that carried
	// data.
	failures int
	// retryAt is the end of the current backoff step.
	retryAt time.Time
}

// beforeDial returns once the current backoff step has ended, so connections
// adb opens during a step wait instead of dialing.
func (s *agentSessions) beforeDial(ctx context.Context) {
	s.mu.Lock()
	wait := s.retryAt.Sub(s.clock.Now())
	s.mu.Unlock()
	if wait > 0 {
		s.clock.Sleep(ctx, wait)
	}
}

// ended handles one ended session. err is nil for a clean end. It returns
// when the local connection may close.
func (s *agentSessions) ended(ctx context.Context, err error) {
	defer s.log.tick()
	if s.watch != nil {
		s.watch.record(err)
	}
	dialFailed := errors.Is(err, errStreamDial)
	if dialFailed && isNotFound(err) {
		s.gone()
		return
	}
	if !dialFailed && !errors.Is(err, errStreamSilent) {
		// The device sent data, so the stream worked.
		s.mu.Lock()
		s.failures = 0
		s.retryAt = time.Time{}
		s.mu.Unlock()
		if err != nil {
			s.log.printf("ADB session ended: %v", err)
		}
		if errors.Is(err, errRemoteEnded) && s.remoteEnded != nil {
			s.remoteEnded()
		}
		return
	}

	// A failure inside the current step came from a dial that started before
	// the step did, so it neither lengthens the backoff nor checks again.
	s.mu.Lock()
	newStep := !s.clock.Now().Before(s.retryAt)
	if newStep {
		s.failures++
		s.retryAt = s.clock.Now().Add(dialBackoff(s.failures))
	}
	s.mu.Unlock()

	s.log.printf("ADB session ended: %v", err)
	if newStep && s.deviceGone(ctx) {
		s.gone()
		return
	}
	s.beforeDial(ctx)
}

// deviceGone asks the API whether the device still exists. A device that
// returns 404, or whose status is deleting or deleted, is gone. Any other
// failure leaves the answer open, so the agent stays up.
func (s *agentSessions) deviceGone(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, deviceCheckTimeout)
	defer cancel()
	d, err := s.getDevice(ctx, s.deviceID)
	if err != nil {
		if isNotFound(err) {
			return true
		}
		s.log.printf("Checking device %s: %v", s.deviceID, err)
		return false
	}
	return d.Status == cco.StatusDeleting || d.Status == cco.StatusDeleted
}

func (s *agentSessions) gone() {
	s.log.printf("Device %s instance %s is gone; stopping.", s.deviceID, s.instance)
	s.stop()
}

// isNotFound reports whether err carries an API 404.
func isNotFound(err error) bool {
	apiErr, ok := errors.AsType[*cco.Error](err)
	return ok && apiErr.Status == http.StatusNotFound
}

const (
	// agentLogLimit is the size past which the agent truncates its log.
	agentLogLimit = 1 << 20

	// agentLogRecent is how many distinct recent messages the log counts
	// instead of writing again. A failing device check alternates two
	// messages, so one is not enough.
	agentLogRecent = 4

	// repeatSummaryEvery is how often repeated messages are counted in the log.
	repeatSummaryEvery = 5 * time.Minute
)

// agentLog writes the agent's session messages with a timestamp. A message
// identical to one of the last agentLogRecent distinct messages is counted
// instead of written again. The counts are written every repeatSummaryEvery,
// each naming its message, and when a message drops out of the recent set.
//
// The log file at path is truncated once it passes limit. The check runs
// before each write, so the newest line survives, and on every tick, so
// output the agent does not write itself, such as --verbose request traces,
// is capped too. The parent opens the log with O_APPEND, so later writes land
// at the new end of the file.
type agentLog struct {
	clock agentClock
	emit  func(line string)
	// path is the log file to cap; empty disables the cap.
	path  string
	limit int64

	mu sync.Mutex
	// recent holds the last distinct messages, oldest first.
	recent     []recentMessage
	summarized time.Time
}

// recentMessage is a message the log has written and how many times it came
// again since its last count was written.
type recentMessage struct {
	msg     string
	repeats int
}

func (l *agentLog) printf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	l.capFile(now)
	if i := slices.IndexFunc(l.recent, func(r recentMessage) bool { return r.msg == msg }); i >= 0 {
		r := l.recent[i]
		r.repeats++
		l.recent = append(slices.Delete(l.recent, i, i+1), r)
	} else {
		l.write(now, msg)
		l.recent = append(l.recent, recentMessage{msg: msg})
		if len(l.recent) > agentLogRecent {
			l.writeRepeats(now, l.recent[0])
			l.recent = slices.Delete(l.recent, 0, 1)
		}
	}
	l.summarizeIfDue(now)
}

// tick writes the counts once they are due and caps the file.
func (l *agentLog) tick() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	l.summarizeIfDue(now)
	l.capFile(now)
}

func (l *agentLog) summarizeIfDue(now time.Time) {
	if l.summarized.IsZero() {
		l.summarized = now
		return
	}
	if now.Sub(l.summarized) < repeatSummaryEvery {
		return
	}
	for i := range l.recent {
		l.writeRepeats(now, l.recent[i])
		l.recent[i].repeats = 0
	}
	l.summarized = now
}

// writeRepeats writes the count of r's repeats, if it has any.
func (l *agentLog) writeRepeats(now time.Time, r recentMessage) {
	if r.repeats > 0 {
		l.write(now, fmt.Sprintf("Repeated %d more times: %s", r.repeats, r.msg))
	}
}

func (l *agentLog) write(now time.Time, msg string) {
	l.emit(now.UTC().Format(time.RFC3339) + ": " + msg)
}

// capFile truncates the log file once it has passed limit.
func (l *agentLog) capFile(now time.Time) {
	if l.path == "" {
		return
	}
	info, err := os.Stat(l.path)
	if err != nil || info.Size() <= l.limit {
		return
	}
	if err := os.Truncate(l.path, 0); err != nil {
		return
	}
	l.write(now, fmt.Sprintf("Log truncated after it passed %d bytes", l.limit))
}
