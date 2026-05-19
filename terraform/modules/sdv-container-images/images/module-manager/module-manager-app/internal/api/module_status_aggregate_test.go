// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import "testing"

func TestMergeManagedApplicationStatuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in       []StatusResponse
		wantSync string
		wantHlth string
		wantOp   string
	}{
		{
			name:     "empty",
			in:       nil,
			wantSync: "",
			wantHlth: "",
			wantOp:   "",
		},
		{
			name: "all synced healthy",
			in: []StatusResponse{
				{SyncStatus: "Synced", HealthStatus: "Healthy", OperationPhase: "Succeeded"},
				{SyncStatus: "Synced", HealthStatus: "Healthy"},
			},
			wantSync: "Synced",
			wantHlth: "Healthy",
			wantOp:   "",
		},
		{
			name: "child progressing blocks ready",
			in: []StatusResponse{
				{SyncStatus: "Synced", HealthStatus: "Healthy"},
				{SyncStatus: "Synced", HealthStatus: "Progressing"},
			},
			wantSync: "Synced",
			wantHlth: "Progressing",
			wantOp:   "",
		},
		{
			name: "out of sync wins",
			in: []StatusResponse{
				{SyncStatus: "Synced", HealthStatus: "Healthy"},
				{SyncStatus: "OutOfSync", HealthStatus: "Healthy"},
			},
			wantSync: "OutOfSync",
			wantHlth: "Healthy",
			wantOp:   "",
		},
		{
			name: "running op",
			in: []StatusResponse{
				{SyncStatus: "Synced", HealthStatus: "Healthy", OperationPhase: "Succeeded"},
				{SyncStatus: "Synced", HealthStatus: "Healthy", OperationPhase: "Running"},
			},
			wantSync: "Synced",
			wantHlth: "Healthy",
			wantOp:   "Running",
		},
		{
			name: "pending after running check",
			in: []StatusResponse{
				{SyncStatus: "Synced", HealthStatus: "Healthy", OperationPhase: "Pending"},
			},
			wantSync: "Synced",
			wantHlth: "Healthy",
			wantOp:   "Pending",
		},
		{
			name: "degraded over progressing",
			in: []StatusResponse{
				{SyncStatus: "Synced", HealthStatus: "Progressing"},
				{SyncStatus: "Synced", HealthStatus: "Degraded"},
			},
			wantSync: "Synced",
			wantHlth: "Degraded",
			wantOp:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, h, o := mergeManagedApplicationStatuses(tc.in)
			if s != tc.wantSync {
				t.Fatalf("syncStatus: got %q want %q", s, tc.wantSync)
			}
			if h != tc.wantHlth {
				t.Fatalf("healthStatus: got %q want %q", h, tc.wantHlth)
			}
			if o != tc.wantOp {
				t.Fatalf("operationPhase: got %q want %q", o, tc.wantOp)
			}
		})
	}
}

func TestMergeSyncStatuses(t *testing.T) {
	t.Parallel()
	if got := mergeSyncStatuses([]string{"Synced", "Unknown"}); got != "Unknown" {
		t.Fatalf("got %q want Unknown", got)
	}
	if got := mergeSyncStatuses([]string{"Synced", "OutOfSync"}); got != "OutOfSync" {
		t.Fatalf("got %q want OutOfSync", got)
	}
}
