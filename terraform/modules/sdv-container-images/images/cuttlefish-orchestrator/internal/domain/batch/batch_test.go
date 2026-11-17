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

package batch

import (
	"errors"
	"testing"
	"time"
)

func validSpec() *Spec {
	return &Spec{
		NamePrefix: "ci", Count: 10, MinReady: 10,
		FillPolicy: FillBestEffort, Placement: PlacementIsolated, TTLHours: 1,
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*Spec)
		wantErr bool
	}{
		{"valid", func(*Spec) {}, false},
		{"empty name", func(s *Spec) { s.NamePrefix = "" }, true},
		{"count zero", func(s *Spec) { s.Count = 0; s.MinReady = 0 }, true},
		{"count over max", func(s *Spec) { s.Count = MaxCount + 1; s.MinReady = 1 }, true},
		{"min_ready over count", func(s *Spec) { s.MinReady = s.Count + 1 }, true},
		{"min_ready zero", func(s *Spec) { s.MinReady = 0 }, true},
		{"bad fill policy", func(s *Spec) { s.FillPolicy = "whenever" }, true},
		{"bad placement", func(s *Spec) { s.Placement = "everywhere" }, true},
		{"negative ttl", func(s *Spec) { s.TTLHours = -1 }, true},
		{"atomic full target", func(s *Spec) { s.FillPolicy = FillAtomic }, false},
		{"atomic with timeout", func(s *Spec) { s.FillPolicy = FillAtomic; s.ReadyTimeout = 10 * time.Minute }, false},
		{"atomic below full target", func(s *Spec) { s.FillPolicy = FillAtomic; s.MinReady = 5 }, true},
		{"timeout without atomic", func(s *Spec) { s.ReadyTimeout = 10 * time.Minute }, true},
		{"negative timeout", func(s *Spec) { s.FillPolicy = FillAtomic; s.ReadyTimeout = -1 }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := validSpec()
			tc.mutate(s)
			err := s.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want no error, got %v", err)
			}
			if tc.wantErr && !errors.Is(err, ErrInvalidSpec) {
				t.Errorf("error %v is not ErrInvalidSpec", err)
			}
		})
	}
}

func TestDerivePhase(t *testing.T) {
	t.Parallel()
	const minReady = 10
	tests := []struct {
		name   string
		rollup Rollup
		want   Status
	}{
		{
			name:   "all running meets target",
			rollup: Rollup{Total: 10, Running: 10},
			want:   StatusReady,
		},
		{
			name:   "target met with some errored is degraded",
			rollup: Rollup{Total: 10, Running: 10, Errored: 2},
			want:   StatusDegraded,
		},
		{
			name:   "still coming up is provisioning",
			rollup: Rollup{Total: 10, Running: 4, Reconciling: 6},
			want:   StatusProvisioning,
		},
		{
			// A running-but-recovering member (zombie recreate in flight) counts
			// as reconciling, so it correctly un-settles the batch.
			name:   "target met but a member is recovering",
			rollup: Rollup{Total: 10, Running: 10, Reconciling: 1},
			want:   StatusProvisioning,
		},
		{
			name:   "settled below target is degraded",
			rollup: Rollup{Total: 10, Running: 7, Errored: 3, Reconciling: 0},
			want:   StatusDegraded,
		},
		{
			name:   "all members deleting is deleting",
			rollup: Rollup{Total: 10, Deleting: 10},
			want:   StatusDeleting,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DerivePhase(minReady, tc.rollup); got != tc.want {
				t.Errorf("DerivePhase = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBatchStatus(t *testing.T) {
	t.Parallel()
	now := time.Now()

	failed := &Batch{FailedAt: &now}
	// Failed is terminal even when surviving members would roll up healthy,
	// and in the common case when teardown has left no members at all.
	if got := failed.Status(Rollup{Total: 3, Running: 3}); got != StatusFailed {
		t.Errorf("failed batch Status = %q, want %q", got, StatusFailed)
	}
	if got := failed.Status(Rollup{}); got != StatusFailed {
		t.Errorf("failed empty batch Status = %q, want %q", got, StatusFailed)
	}

	live := &Batch{RequestedCount: 3}
	if got := live.Status(Rollup{Total: 3, Running: 3}); got != StatusReady {
		t.Errorf("live batch Status = %q, want %q", got, StatusReady)
	}
	// minReady is the live non-deleting count: one member tearing down does
	// not stop the remaining running members from reading as ready.
	if got := live.Status(Rollup{Total: 3, Running: 2, Deleting: 1}); got != StatusReady {
		t.Errorf("live batch with deleting member Status = %q, want %q", got, StatusReady)
	}

	if failedSettled := failed.Settled(); !failedSettled {
		t.Error("failed batch should be settled")
	}
	if readySettled := (&Batch{ReadyAt: &now}).Settled(); !readySettled {
		t.Error("ready batch should be settled")
	}
	if liveSettled := live.Settled(); liveSettled {
		t.Error("unsettled batch reports settled")
	}
}
