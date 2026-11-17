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

//go:build windows

package filelock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// errSharingViolation is ERROR_SHARING_VIOLATION.
const errSharingViolation syscall.Errno = 32

// openExclusive opens path with no sharing, which fails with a sharing
// violation while another handle holds it open.
func openExclusive(path string, disposition uint32) (syscall.Handle, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return syscall.InvalidHandle, fmt.Errorf("encoding %s: %w", path, err)
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		disposition, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return syscall.InvalidHandle, fmt.Errorf("opening %s: %w", path, err)
	}
	return h, nil
}

// TryLock holds path open with no sharing. Windows closes the handle when the
// holder dies.
func TryLock(path string) (*os.File, error) {
	h, err := openExclusive(path, syscall.OPEN_ALWAYS)
	if err != nil {
		if errors.Is(err, errSharingViolation) {
			return nil, ErrHeld
		}
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// Held reports whether another handle holds path open.
func Held(path string) bool {
	h, err := openExclusive(path, syscall.OPEN_EXISTING)
	if err != nil {
		return errors.Is(err, errSharingViolation)
	}
	_ = syscall.CloseHandle(h)
	return false
}

// Release closes the lock before removing it, because Windows refuses to
// delete a file that is open without delete sharing.
func Release(f *os.File, path string) {
	_ = f.Close()
	_ = os.Remove(path)
}
