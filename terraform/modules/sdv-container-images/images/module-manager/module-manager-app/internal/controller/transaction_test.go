// Copyright (c) 2024-2026 Accenture, All Rights Reserved.
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestModuleOpsMutexSerializesConcurrentCriticalSections(t *testing.T) {
	t.Parallel()

	start := make(chan struct{})
	done := make(chan struct{}, 2)
	var inCritical atomic.Int32
	var overlap atomic.Bool

	worker := func() {
		<-start
		ModuleOpsMutex.Lock()
		if inCritical.Add(1) > 1 {
			overlap.Store(true)
		}
		time.Sleep(25 * time.Millisecond)
		inCritical.Add(-1)
		ModuleOpsMutex.Unlock()
		done <- struct{}{}
	}

	go worker()
	go worker()
	close(start)
	<-done
	<-done
	if overlap.Load() {
		t.Fatal("expected ModuleOpsMutex to serialize critical sections")
	}
}
