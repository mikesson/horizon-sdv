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

package leakcheck

import (
	"bytes"
	"fmt"
	"io"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// detectorTimeout bounds the wait for a goroutine leaked on purpose to be
// observed. It is generous because this is a liveness check on the detector
// and never a latency measurement, and a loaded CI runner may take several GC
// cycles.
const detectorTimeout = 30 * time.Second

// Profile returns the goroutineleak profile. It is registered only under the
// build experiment, so a nil lookup means the binary was built without it and
// every assertion here would be vacuous.
func Profile(t *testing.T) *pprof.Profile {
	t.Helper()
	p := pprof.Lookup("goroutineleak")
	require.NotNil(t, p, "goroutineleak profile absent despite the build tag")
	return p
}

// Detect runs a leak-detecting GC and reports what it found, with stacks.
// WriteTo is what triggers the detection; reading Count alone reports whatever
// the last such GC happened to find, which in a test is usually nothing.
//
// It returns an error rather than failing the test because it is called from
// inside polling closures, which testify runs on their own goroutine where a
// failed require cannot fail the test properly.
func Detect(p *pprof.Profile) (int, string, error) {
	var buf bytes.Buffer
	if err := p.WriteTo(&buf, 1); err != nil {
		return 0, "", fmt.Errorf("writing goroutineleak profile: %w", err)
	}
	return p.Count(), buf.String(), nil
}

// Watcher holds a leak count captured before the code under test ran.
type Watcher struct {
	profile *pprof.Profile
	before  int
}

// Start captures the baseline. Counts are compared as deltas, never against
// zero: the profile is process-wide, tests share a binary, and CI shuffles, so
// an absolute assertion would depend on what else has run.
func Start(t *testing.T) *Watcher {
	t.Helper()
	p := Profile(t)
	before, _, err := Detect(p)
	require.NoError(t, err)
	return &Watcher{profile: p, before: before}
}

// AssertNone fails if any goroutine leaked since Start, naming the stacks.
// Call it once the subject is both stopped and unreachable. A goroutine
// parked on a channel its still-live owner could signal is not a leak, and
// will not be reported as one.
func AssertNone(t *testing.T, w *Watcher, subject string) {
	t.Helper()
	after, stacks, err := Detect(w.profile)
	require.NoError(t, err)
	assert.LessOrEqual(t, after, w.before,
		"%s leaked %d goroutine(s):\n%s", subject, after-w.before, stacks)
}

// AssertDetectorLive fails unless the profile observes a goroutine leaked on
// purpose. Every package that asserts on leaks needs this once: a zero delta
// cannot tell "nothing leaked" from "nothing was looked at", and each package
// is its own test binary, so one package proving the detector works says
// nothing about the next.
//
// The check polls. Detection happens during a GC, and a goroutine not yet
// scheduled onto its blocking receive is not yet leaked. Measured once, this
// passes alone and fails under a package's parallel load.
func AssertDetectorLive(t *testing.T) {
	t.Helper()
	p := Profile(t)
	before, _, err := Detect(p)
	require.NoError(t, err)

	// The runtime's canonical leak: a receive on a channel no live goroutine
	// holds, so nothing can ever wake it.
	go func() { <-make(chan struct{}) }()

	require.Eventually(t, func() bool {
		n, _, err := Detect(p)
		return err == nil && n > before
	}, detectorTimeout, 100*time.Millisecond,
		"the goroutineleak profile never observed a goroutine leaked on purpose, "+
			"so a zero delta elsewhere in this binary proves nothing")
}

// AssertIgnoresCleanExit guards the opposite error from AssertDetectorLive: a
// detector that counted ordinary finished goroutines would fail on healthy
// code until someone switched the gate off.
func AssertIgnoresCleanExit(t *testing.T) {
	t.Helper()
	w := Start(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Discard.Write([]byte("work"))
	}()
	<-done

	after, _, err := Detect(w.profile)
	require.NoError(t, err)
	assert.LessOrEqual(t, after, w.before, "a goroutine that returned was counted as leaked")
}
