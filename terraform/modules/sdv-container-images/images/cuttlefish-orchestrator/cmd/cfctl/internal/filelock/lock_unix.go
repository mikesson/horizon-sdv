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

//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package filelock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// TryLock takes an exclusive flock on path without blocking. flock locks
// belong to the open file, so the kernel drops it when the holder dies.
func TryLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if err := syscall.Flock(fileDescriptor(f), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	// A Release may unlink path between the open and the flock. A lock on the
	// unlinked file guards nothing, so the caller retries on the new one.
	opened, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	if current, err := os.Stat(path); err != nil || !os.SameFile(opened, current) {
		_ = f.Close()
		return nil, ErrHeld
	}
	return f, nil
}

// Held reports whether another open file holds the lock on path.
func Held(path string) bool {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(fileDescriptor(f), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	_ = syscall.Flock(fileDescriptor(f), syscall.LOCK_UN)
	return false
}

// Release removes the lock file while still holding it. A process that locks
// the unlinked file in between sees it in TryLock and retries.
func Release(f *os.File, path string) {
	_ = os.Remove(path)
	_ = f.Close()
}

// fileDescriptor narrows f's descriptor to the int syscall.Flock takes.
// Descriptors are small non-negative integers.
func fileDescriptor(f *os.File) int {
	return int(f.Fd())
}
