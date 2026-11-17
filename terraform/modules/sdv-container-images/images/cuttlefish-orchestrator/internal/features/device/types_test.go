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

package devicefeature

import (
	"strings"
	"testing"
)

func TestCreateDeviceInputResolve(t *testing.T) {
	t.Parallel()
	base := func() *CreateDeviceInput {
		i := &CreateDeviceInput{}
		i.Body.HostID = "host-1"
		i.Body.Name = "dev-1"
		return i
	}

	tests := []struct {
		name    string
		mutate  func(i *CreateDeviceInput)
		wantErr string // substring of the expected error; empty = valid
	}{
		{
			name:   "plain ephemeral create is valid",
			mutate: func(_ *CreateDeviceInput) {},
		},
		{
			name: "ttl only is valid",
			mutate: func(i *CreateDeviceInput) {
				i.Body.TTLHours = 8
			},
		},
		{
			name: "pinned with reason is valid",
			mutate: func(i *CreateDeviceInput) {
				i.Body.Pinned = true
				i.Body.PinnedReason = "overnight_test"
			},
		},
		{
			name: "pinned without reason is rejected",
			mutate: func(i *CreateDeviceInput) {
				i.Body.Pinned = true
			},
			wantErr: "pinned_reason is required",
		},
		{
			name: "reason without pinned is rejected",
			mutate: func(i *CreateDeviceInput) {
				i.Body.PinnedReason = "overnight_test"
			},
			wantErr: "require pinned",
		},
		{
			name: "reason other requires detail",
			mutate: func(i *CreateDeviceInput) {
				i.Body.Pinned = true
				i.Body.PinnedReason = "other"
			},
			wantErr: "pinned_reason_detail is required",
		},
		{
			name: "reason other with detail is valid",
			mutate: func(i *CreateDeviceInput) {
				i.Body.Pinned = true
				i.Body.PinnedReason = "other"
				i.Body.PinnedReasonDetail = "soak test for team A"
			},
		},
		{
			name: "blank tag is rejected",
			mutate: func(i *CreateDeviceInput) {
				i.Body.Tags = []string{"ok", "  "}
			},
			wantErr: "tags must be non-empty",
		},
		{
			name: "oversize tag is rejected",
			mutate: func(i *CreateDeviceInput) {
				i.Body.Tags = []string{strings.Repeat("x", 65)}
			},
			wantErr: "at most 64",
		},
		{
			name: "valid tags pass",
			mutate: func(i *CreateDeviceInput) {
				i.Body.Tags = []string{"ci", "team-a"}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := base()
			tt.mutate(input)
			errs := input.Resolve(nil)
			if tt.wantErr == "" {
				if len(errs) != 0 {
					t.Fatalf("expected no errors, got %v", errs)
				}
				return
			}
			if len(errs) == 0 {
				t.Fatalf("expected error containing %q, got none", tt.wantErr)
			}
			found := false
			for _, err := range errs {
				if strings.Contains(err.Error(), tt.wantErr) {
					found = true
				}
			}
			if !found {
				t.Errorf("expected an error containing %q, got %v", tt.wantErr, errs)
			}
		})
	}
}
