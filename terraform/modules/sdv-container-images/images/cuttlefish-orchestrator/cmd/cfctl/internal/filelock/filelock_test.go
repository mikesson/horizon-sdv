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

// SPDX-License-Identifier: Apache-2.0

package filelock

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTryLockHeldAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.lock")
	if Held(path) {
		t.Fatal("Held() = true before lock file exists, want false")
	}

	f, err := TryLock(path)
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skipf("file locks unsupported here: %v", err)
	}
	if err != nil {
		t.Fatalf("TryLock() = %v, want nil", err)
	}

	released := false
	t.Cleanup(func() {
		if !released {
			Release(f, path)
		}
	})

	if !Held(path) {
		t.Error("Held() = false while lock is held, want true")
	}
	if f2, err2 := TryLock(path); !errors.Is(err2, ErrHeld) {
		if f2 != nil {
			Release(f2, path)
		}
		t.Fatalf("contended TryLock() = (%v, %v), want ErrHeld", f2, err2)
	}

	Release(f, path)
	released = true

	if Held(path) {
		t.Error("Held() = true after Release, want false")
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("lock file still exists after Release: stat err = %v", err)
	}

	f3, err := TryLock(path)
	if err != nil {
		t.Fatalf("TryLock() after Release = %v, want nil", err)
	}
	Release(f3, path)
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("lock file leaked after second Release: stat err = %v", err)
	}
}

func TestAcquireWithinTimesOutWithErrHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "held.lock")
	f, err := TryLock(path)
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skipf("file locks unsupported here: %v", err)
	}
	if err != nil {
		t.Fatalf("TryLock() = %v, want nil", err)
	}
	defer func() {
		Release(f, path)
		if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("lock file leaked after cleanup Release: %v", statErr)
		}
	}()

	if f0, err0 := AcquireWithin(path, 0); !errors.Is(err0, ErrHeld) || f0 != nil {
		if f0 != nil {
			Release(f0, path)
		}
		t.Fatalf("AcquireWithin(0) = (%v, %v), want (nil, ErrHeld)", f0, err0)
	}

	start := time.Now()
	const timeout = 60 * time.Millisecond
	fTimed, errTimed := AcquireWithin(path, timeout)
	elapsed := time.Since(start)
	if fTimed != nil || !errors.Is(errTimed, ErrHeld) {
		if fTimed != nil {
			Release(fTimed, path)
		}
		t.Fatalf("AcquireWithin(%v) = (%v, %v), want (nil, ErrHeld)", timeout, fTimed, errTimed)
	}
	if elapsed < timeout/2 {
		t.Errorf("AcquireWithin(%v) returned too early after %v", timeout, elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("AcquireWithin(%v) took %v, expected bounded timeout near %v", timeout, elapsed, timeout)
	}
}

func TestAcquireWithinSucceedsAfterRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff.lock")
	f, err := TryLock(path)
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skipf("file locks unsupported here: %v", err)
	}
	if err != nil {
		t.Fatalf("TryLock() = %v, want nil", err)
	}

	released := make(chan struct{})
	go func() {
		time.Sleep(25 * time.Millisecond)
		Release(f, path)
		close(released)
	}()

	f2, err := AcquireWithin(path, time.Second)
	<-released
	if err != nil {
		t.Fatalf("AcquireWithin() after release = %v, want nil", err)
	}
	Release(f2, path)
	if Held(path) {
		t.Error("Held() = true after final Release, want false")
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("lock file leaked after final Release: %v", err)
	}
}

func TestAcquireDelegatesWithOneSecondContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acquire.lock")
	f, err := TryLock(path)
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skipf("file locks unsupported here: %v", err)
	}
	if err != nil {
		t.Fatalf("TryLock() = %v, want nil", err)
	}

	released := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		Release(f, path)
		close(released)
	}()

	f2, err := Acquire(path)
	<-released
	if err != nil {
		t.Fatalf("Acquire() = %v, want nil", err)
	}
	Release(f2, path)
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("lock file leaked after Release: %v", err)
	}
}

func TestAcquireWithinConvergesUnderContention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contended.lock")
	probe, err := TryLock(path)
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skipf("file locks unsupported here: %v", err)
	}
	if err != nil {
		t.Fatalf("initial TryLock() = %v, want nil", err)
	}
	Release(probe, path)

	const contenders = 16
	var (
		wg        sync.WaitGroup
		inSection atomic.Int32
		completed atomic.Int32
		errs      = make(chan error, contenders)
	)
	for range contenders {
		wg.Go(func() {
			f, err := AcquireWithin(path, 5*time.Second)
			if err != nil {
				errs <- err
				return
			}
			if active := inSection.Add(1); active != 1 {
				inSection.Add(-1)
				Release(f, path)
				errs <- errors.New("concurrent holders entered critical section")
				return
			}
			time.Sleep(time.Millisecond)
			completed.Add(1)
			inSection.Add(-1)
			Release(f, path)
		})
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("contender failed: %v", err)
	}
	if got := completed.Load(); got != contenders {
		t.Errorf("completed = %d, want %d", got, contenders)
	}
	if Held(path) {
		t.Error("Held() = true after all contenders released, want false")
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("lock file leaked after contention test: %v", err)
	}
}
