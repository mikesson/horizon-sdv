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

// Goroutine-leak assertions for the Spot capacity-advice probe: Start hands
// its cycle to a telemetry.Runner, and Stop has to join it.

package catalogsync

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/tests/leakcheck"
)

func TestGoroutineLeakDetectorIsLive(t *testing.T) {
	leakcheck.AssertDetectorLive(t)
}

// Runs as a non-leader, which is the state a replica that never wins the lease
// is permanently in, and the goroutine topology is identical either way.
//
// The consequence is that this covers Stop's joining of goroutines that start
// and finish, not a cycle interrupted mid-flight; the cancel-mid-cycle shape
// is asserted against Runner directly.
func TestServiceStopLeavesNoGoroutineLeak(t *testing.T) {
	w := leakcheck.Start(t)

	func() {
		svc, err := New(validConfig(), slog.New(slog.DiscardHandler),
			leakTestDeps(), WithLeaderCheck(func() bool { return false }))
		require.NoError(t, err)

		svc.Start(t.Context())
		svc.Stop()
	}() // Scoped so the service is unreachable before the detecting GC.

	leakcheck.AssertNone(t, w, "catalogsync Stop")
}

// Stop is documented as a no-op when not running; it must not leave the
// runners started either.
func TestServiceStopWithoutStartLeavesNoGoroutineLeak(t *testing.T) {
	w := leakcheck.Start(t)

	func() {
		svc, err := New(validConfig(), slog.New(slog.DiscardHandler),
			leakTestDeps(), WithLeaderCheck(func() bool { return false }))
		require.NoError(t, err)
		svc.Stop()
	}()

	leakcheck.AssertNone(t, w, "catalogsync Stop without Start")
}

// leakTestDeps satisfies the constructor; no cycle ever runs here.
func leakTestDeps() Option {
	return WithSpotAdvice(&fakeProber{}, &fakeAdviceStore{}, &fakeMachineTypes{}, &fakeEnabledZones{})
}
