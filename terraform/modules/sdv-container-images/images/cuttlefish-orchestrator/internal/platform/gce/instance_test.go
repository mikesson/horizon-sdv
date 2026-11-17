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

package gce

import (
	"testing"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"
)

func TestVMTerminatesAt(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	byDuration := started.Add(14 * 24 * time.Hour)
	explicit := started.Add(24 * time.Hour)

	inst := func(status string, sched *computepb.Scheduling) *computepb.Instance {
		return &computepb.Instance{
			Status:             new(status),
			LastStartTimestamp: new("2026-10-01T01:00:00.000-07:00"),
			Scheduling:         sched,
		}
	}
	withDuration := &computepb.Scheduling{MaxRunDuration: &computepb.Duration{Seconds: new(int64(14 * 24 * 3600))}}
	withBoth := &computepb.Scheduling{
		MaxRunDuration:  &computepb.Duration{Seconds: new(int64(14 * 24 * 3600))},
		TerminationTime: new(explicit.Format(time.RFC3339)),
	}

	cases := []struct {
		name string
		inst *computepb.Instance
		want *time.Time
	}{
		{"last start plus max run duration, offset timestamp", inst("RUNNING", withDuration), &byDuration},
		{"the earlier of an explicit time and the duration", inst("RUNNING", withBoth), &explicit},
		{"no limit set", inst("RUNNING", &computepb.Scheduling{}), nil},
		{"a stopped VM has no clock running", inst("TERMINATED", withDuration), nil},
		{"a booting VM may carry the previous start", inst("STAGING", withDuration), nil},
	}
	for _, tc := range cases {
		got := VMTerminatesAt(tc.inst)
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("%s: got %v, want nil", tc.name, *got)
		case tc.want != nil && (got == nil || !got.Equal(*tc.want)):
			t.Errorf("%s: got %v, want %v", tc.name, got, *tc.want)
		}
	}
}
