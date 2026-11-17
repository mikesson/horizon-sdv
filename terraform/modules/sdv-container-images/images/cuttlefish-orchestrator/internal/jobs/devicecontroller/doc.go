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

// Package devicecontroller is the device-plane controller: a leader-gated
// loop that probes each running and starting host's Host Orchestrator and
// turns the observations into device health, device lifecycle status, host
// promotion, and queued recovery work.
//
// Each cycle (runHealthChecks):
//
//   - Probes hosts with bounded fan-out for their live CVD listing. A host
//     that is not serving records real per-device failures instead of
//     freezing health at last-known.
//   - Promotes a starting host to running on first Host Orchestrator
//     contact (MarkHOReady). This probe is what drives first-boot
//     promotion, because nothing else visits starting hosts.
//   - Derives device lifecycle status from the observed CVD state
//     (device.DerivePhase) and writes settled transitions through the
//     CAS-safe UpdateStatus. Health conditions (healthy/unhealthy/zombie
//     via consecutive-failure thresholds) are recorded per device along
//     with the raw CVD observation.
//   - Runs the CVD census: CVD groups no device row claims are leaked host
//     RAM. Detection logs and gauges them; sweeping (SweepOrphanCVDs, off
//     by default) deletes groups that stay unclaimed across the grace
//     window.
//   - Runs five suspectors that enqueue keyed recovery tasks, each capped
//     per cycle: devices stuck deleting with no live cleanup task →
//     device.reconcile; devices stuck pending/creating with no live create
//     task → device.reconcile (healed from the persisted desired_spec);
//     zombie devices (gated on ZombieRecreate) → device.reconcile; degraded
//     hosts settled to zero live devices → host.remediate (the sole
//     remediation trigger); uncaptured error devices (gated on the runtime
//     capture-on-error policy) → device.capture_bugreport.
//
// The controller detects and enqueues; actuation lives in the feature task
// handlers. Leader-gated under the devicecontroller lease; metrics land
// under cco.devicecontroller.*.
package devicecontroller
