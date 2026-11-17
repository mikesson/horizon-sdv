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

// Package filelock provides the exclusive file lock that cfctl's background
// ADB agents and its config file share. The operating system drops the lock
// when the holding process dies, so a held lock also proves the holder is
// alive.
package filelock

import (
	"errors"
	"os"
	"time"
)

const (
	defaultAcquireTimeout = time.Second
	fastPollInterval      = 5 * time.Millisecond
	slowPollInterval      = 50 * time.Millisecond
	fastPollWindow        = 250 * time.Millisecond
)

// ErrHeld reports that another open file holds the lock.
var ErrHeld = errors.New("the lock is held by another process")

// Acquire takes the lock on path, retrying for about a second because a
// concurrent liveness check or config write holds it only for an instant.
func Acquire(path string) (*os.File, error) {
	return AcquireWithin(path, defaultAcquireTimeout)
}

// AcquireWithin takes the lock on path, retrying until timeout elapses when
// another open file holds it.
func AcquireWithin(path string, timeout time.Duration) (*os.File, error) {
	f, err := TryLock(path)
	if !errors.Is(err, ErrHeld) || timeout <= 0 {
		return f, err
	}
	start := time.Now()
	deadline := start.Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, err
		}
		poll := fastPollInterval
		if time.Since(start) >= fastPollWindow {
			poll = slowPollInterval
		}
		time.Sleep(min(poll, remaining))
		if f, err = TryLock(path); !errors.Is(err, ErrHeld) {
			return f, err
		}
	}
}
