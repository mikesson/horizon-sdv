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
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// UpdateHostFromGCEInstance updates host fields from a GCE instance: the IPs
// and the time GCE will stop the VM.
func UpdateHostFromGCEInstance(h *host.Host, inst *computepb.Instance) {
	if len(inst.GetNetworkInterfaces()) > 0 {
		ni := inst.GetNetworkInterfaces()[0]
		h.InternalIP = ni.GetNetworkIP()
		if len(ni.GetAccessConfigs()) > 0 {
			h.IPAddress = ni.GetAccessConfigs()[0].GetNatIP()
		}
	}
	h.VMTerminatesAt = VMTerminatesAt(inst)
}

// VMTerminatesAt returns when GCE will stop or delete inst under its
// scheduling limits, or nil when no limit is running. GCE publishes this as
// resourceStatus.scheduling.terminationTimestamp, which this Compute client
// version does not decode, so it is derived from the fields GCE derives it
// from: an explicit scheduling.terminationTime, and lastStartTimestamp plus
// scheduling.maxRunDuration. The earlier of the two wins. Only a RUNNING VM has
// a clock running; in any other state lastStartTimestamp may belong to the
// previous start, and the next start resets the duration anyway.
func VMTerminatesAt(inst *computepb.Instance) *time.Time {
	if inst.GetStatus() != "RUNNING" {
		return nil
	}
	sched := inst.GetScheduling()
	var at *time.Time
	if t, err := time.Parse(time.RFC3339Nano, sched.GetTerminationTime()); err == nil {
		at = &t
	}
	if d := sched.GetMaxRunDuration(); d.GetSeconds() > 0 {
		if started, err := time.Parse(time.RFC3339Nano, inst.GetLastStartTimestamp()); err == nil {
			t := started.Add(time.Duration(d.GetSeconds())*time.Second + time.Duration(d.GetNanos()))
			if at == nil || t.Before(*at) {
				at = &t
			}
		}
	}
	return at
}
