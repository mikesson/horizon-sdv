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

package task

import "time"

// RescheduleError signals that a task's precondition is not yet met (e.g. the
// host it targets is still provisioning): the worker returns the task to
// pending, eligible again After from now, WITHOUT counting the delivery
// against MaxAttempts. It is for waiting, never for failures, and the handler
// owns the wall-clock budget that bounds how long a task may keep waiting.
//
// The no-attempt-burn guarantee holds only when Postpone actually runs: a
// crash after Claim (which increments attempts) but before Postpone leaves the
// increment standing, so the sweeper redelivers at the higher count. This is
// harmless because the handler's wall-clock budget, not the attempt count,
// is what bounds total waiting; the two budgets can only disagree under
// repeated mid-wait crashes.
type RescheduleError struct {
	// After is the delay before the task becomes claimable again.
	After time.Duration
	// Reason describes what the task is waiting for (recorded on the task).
	Reason string
}

// Error tolerates a nil receiver: errors.As/AsType walk wrapped error trees
// calling methods on whatever they find, and a typed-nil in a tree must not
// crash the walk.
func (e *RescheduleError) Error() string {
	if e == nil {
		return "not ready: <nil>"
	}
	return "not ready: " + e.Reason
}

// Reschedule returns a RescheduleError asking the queue to redeliver the task
// after the given delay without burning an attempt.
func Reschedule(after time.Duration, reason string) *RescheduleError {
	return &RescheduleError{After: after, Reason: reason}
}

// DelayHintError wraps a failed attempt with a minimum delay before the next
// one, so that a rate-limited cloud call does not come back on the generic
// backoff curve. Unlike RescheduleError this is a real failure: the attempt
// still counts. The worker takes max(hint, backoff).
type DelayHintError struct {
	// After is the minimum delay before the next attempt.
	After time.Duration
	Err   error
}

// Error and Unwrap tolerate a nil receiver (and a nil Err): errors.As/AsType
// walk wrapped error trees calling these on whatever they find, and a
// typed-nil in a tree must not crash the walk.
func (e *DelayHintError) Error() string {
	if e == nil || e.Err == nil {
		return "retry delay hint: <nil>"
	}
	return e.Err.Error()
}

func (e *DelayHintError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
