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

// Goroutine-leak assertions for Runner, which is the whole control plane's
// loop: reaper, hostreconciler, devicecontroller, catalogsync, taskreclaimer,
// batchsettle and leaderelection all delegate Start/Stop to it, and five of
// them spawn nothing else. A leak here would be a leak in every reconciler at
// once, which is why this file exists instead of seven near-copies.
//
// It stays outside a synctest bubble, unlike the rest of runner_test.go.
// The bubble's fake clock and durable-blocking rules are a different model from
// a GC-reachability detector, and the leak this catches is a real goroutine
// outliving a real Stop.

package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/tests/leakcheck"
)

func TestGoroutineLeakDetectorIsLive(t *testing.T) {
	leakcheck.AssertDetectorLive(t)
}

func TestGoroutineLeakIgnoresCleanExit(t *testing.T) {
	leakcheck.AssertIgnoresCleanExit(t)
}

// Stop's contract is that it cancels the loop context and waits: once it
// returns, the loop goroutine has exited. A cycle still running, or one parked
// on a channel the cancel never reaches, outlives the Runner and lands here.
func TestRunnerStopLeavesNoGoroutineLeak(t *testing.T) {
	w := leakcheck.Start(t)

	func() {
		cycles := make(chan struct{}, 16)
		r := &Runner{
			Interval:   time.Millisecond,
			RunAtStart: true,
			Cycle: func(context.Context) error {
				select {
				case cycles <- struct{}{}:
				default:
				}
				return nil
			},
		}
		r.Start(t.Context())
		<-cycles // Started for real, rather than stopped before it ever ran.
		r.Stop()
	}() // Scoped so the Runner is unreachable before the detecting GC.

	leakcheck.AssertNone(t, w, "Runner.Stop")
}

// The cancel path specifically: a cycle in flight when Stop lands must return,
// not strand its goroutine. This is the shape a reconciler hits at shutdown
// with a slow dependency mid-cycle.
func TestRunnerStopDuringCycleLeavesNoGoroutineLeak(t *testing.T) {
	w := leakcheck.Start(t)

	func() {
		// Buffered on purpose. The send below is non-blocking and this cycle
		// runs exactly once: it parks on ctx.Done() until Stop, and the loop
		// is sequential, so no later cycle retries the send. Unbuffered, the
		// send is dropped whenever the cycle goroutine reaches the select
		// before the test reaches its receive, and both sides park forever.
		entered := make(chan struct{}, 1)
		r := &Runner{
			Interval:   time.Millisecond,
			RunAtStart: true,
			Budget:     -1, // Unbounded, so only the loop context can end the cycle.
			Cycle: func(ctx context.Context) error {
				select {
				case entered <- struct{}{}:
				default:
				}
				<-ctx.Done() // Returns only because Stop cancels.
				return errors.New("canceled mid-cycle")
			},
		}
		r.Start(t.Context())
		<-entered
		r.Stop()
	}()

	leakcheck.AssertNone(t, w, "Runner.Stop with a cycle in flight")
}
