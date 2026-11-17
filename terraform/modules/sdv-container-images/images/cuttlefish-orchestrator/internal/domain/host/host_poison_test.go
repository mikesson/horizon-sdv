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

package host

import (
	"strings"
	"testing"
)

func TestRecordCreateFailureDegradesAtThreshold(t *testing.T) {
	t.Parallel()
	h := &Host{ID: "h1", Status: StatusRunning}

	// Failures below the threshold accumulate without condemning the host: a
	// couple of transient tap collisions must not fence a healthy host.
	for i := 1; i < HostPoisonThreshold; i++ {
		if newly := h.RecordCreateFailure("tap interface busy"); newly {
			t.Fatalf("failure %d degraded the host early", i)
		}
		if h.IsDegraded() {
			t.Fatalf("host degraded after %d failures, threshold is %d", i, HostPoisonThreshold)
		}
	}

	// The threshold-crossing call degrades the host and reports it once.
	if newly := h.RecordCreateFailure("tap interface busy"); !newly {
		t.Fatal("threshold-crossing failure did not report newly-degraded")
	}
	if !h.IsDegraded() {
		t.Fatal("host not degraded at threshold")
	}
	if h.ConsecutiveCreateFails != HostPoisonThreshold {
		t.Errorf("ConsecutiveCreateFails = %d, want %d", h.ConsecutiveCreateFails, HostPoisonThreshold)
	}
	degradedAt := h.DegradedAt

	// Further failures keep it degraded but must not re-report (remediation is
	// triggered once) or reset the degraded timestamp.
	if newly := h.RecordCreateFailure("tap interface busy"); newly {
		t.Error("a failure past the threshold re-reported newly-degraded")
	}
	if h.DegradedAt != degradedAt {
		t.Error("DegradedAt moved on a subsequent failure; remediation timing must be stable")
	}
}

func TestClearDegradedResetsCounterAndCondition(t *testing.T) {
	t.Parallel()
	h := &Host{ID: "h1", Status: StatusRunning}
	for range HostPoisonThreshold {
		h.RecordCreateFailure("Device Id already registered")
	}
	if !h.IsDegraded() {
		t.Fatal("precondition: host should be degraded")
	}

	h.ClearDegraded()

	if h.IsDegraded() {
		t.Error("still degraded after ClearDegraded")
	}
	if h.ConsecutiveCreateFails != 0 || h.DegradedReason != "" || h.LastCreateFailReason != "" {
		t.Errorf("condition not fully cleared: fails=%d reason=%q lastFail=%q",
			h.ConsecutiveCreateFails, h.DegradedReason, h.LastCreateFailReason)
	}
}

func TestCapReasonBoundsConditionColumns(t *testing.T) {
	t.Parallel()
	h := &Host{ID: "h1", Status: StatusRunning}
	long := strings.Repeat("x", 900)
	for range HostPoisonThreshold {
		h.RecordCreateFailure(long)
	}
	if n := len([]rune(h.LastCreateFailReason)); n > 500 {
		t.Errorf("LastCreateFailReason = %d runes, must fit the 500-char column", n)
	}
	if n := len([]rune(h.DegradedReason)); n > 500 {
		t.Errorf("DegradedReason = %d runes, must fit the 500-char column", n)
	}
}
