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

// ErrHeld reports that another open file holds the lock.
var ErrHeld = errors.New("the lock is held by another process")

// Acquire takes the lock on path, retrying for about a second because a
// concurrent liveness check or config write holds it only for an instant.
func Acquire(path string) (*os.File, error) {
	var err error
	for range 20 {
		var f *os.File
		if f, err = TryLock(path); !errors.Is(err, ErrHeld) {
			return f, err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, err
}
