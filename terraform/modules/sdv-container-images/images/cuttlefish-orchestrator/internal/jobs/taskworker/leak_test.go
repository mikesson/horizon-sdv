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

//go:build goexperiment.goroutineleakprofile

// Goroutine-leak assertions for the worker pool. It is the one part of the
// control plane that does not run on telemetry.Runner: it owns a WaitGroup of
// N worker goroutines directly.

package taskworker_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/task"
	"cloud-cuttlefish-orchestrator/tests/leakcheck"
)

func TestGoroutineLeakDetectorIsLive(t *testing.T) {
	leakcheck.AssertDetectorLive(t)
}

func TestGoroutineLeakIgnoresCleanExit(t *testing.T) {
	leakcheck.AssertIgnoresCleanExit(t)
}

// The pool's contract is that Stop returns only once every worker has: it
// cancels the run context and waits on the WaitGroup. A goroutine started
// outside that WaitGroup, or one parked on a channel Stop never signals,
// survives the pool and shows up here.
func TestPoolStopLeavesNoGoroutineLeak(t *testing.T) {
	w := leakcheck.Start(t)

	func() {
		q := &fakeQueue{}
		for _, id := range []string{"t1", "t2", "t3"} {
			enqueueFake(t, q, "demo", id)
		}
		h := &fakeHandler{typ: "demo"}
		p := newPool(t, fastConfig(), q, []task.Handler{h})

		p.Start(t.Context())
		require.Eventually(t, func() bool { return len(q.completedIDs()) == 3 },
			3*time.Second, 5*time.Millisecond)
		p.Stop()
	}() // Scoped so the pool and queue are unreachable before the detecting GC.

	leakcheck.AssertNone(t, w, "Pool.Stop")
}

// Stop cancels in-flight handlers rather than waiting them out, so a handler
// that only returns on cancellation must not strand its worker. This is the
// shutdown shape a long-running CVD create produces.
func TestPoolStopDuringHandlerLeavesNoGoroutineLeak(t *testing.T) {
	w := leakcheck.Start(t)

	func() {
		q := &fakeQueue{}
		enqueueFake(t, q, "demo", "t1")
		entered := make(chan struct{}, 1)
		h := &fakeHandler{typ: "demo", fn: func(ctx context.Context, _ *task.Task) error {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-ctx.Done() // Returns only because Stop cancels.
			return ctx.Err()
		}}
		p := newPool(t, fastConfig(), q, []task.Handler{h})

		p.Start(t.Context())
		select {
		case <-entered:
		case <-time.After(30 * time.Second):
			t.Fatal("no handler ran; the pool never dispatched")
		}
		p.Stop()
	}()

	leakcheck.AssertNone(t, w, "Pool.Stop with a handler in flight")
}
