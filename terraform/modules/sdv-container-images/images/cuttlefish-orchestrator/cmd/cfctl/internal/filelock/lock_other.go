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

//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly || windows)

package filelock

import (
	"errors"
	"fmt"
	"os"
)

// These platforms are not wired for a lock that dies with the process, so
// background ADB agents refuse to run and config writes go ahead unlocked.

// TryLock always fails on this platform.
func TryLock(path string) (*os.File, error) {
	return nil, fmt.Errorf("file locks on this platform: %w", errors.ErrUnsupported)
}

// Held always reports false on this platform.
func Held(string) bool { return false }

// Release closes and removes the lock file.
func Release(f *os.File, path string) {
	_ = f.Close()
	_ = os.Remove(path)
}
