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

// Package hostfeature manages Cuttlefish host VMs: the GCE instances that
// run one or more Cuttlefish devices.
//
// # HTTP surface (handler.go, types.go)
//
//   - ListHosts: GET /v2/hosts
//   - CreateHost: POST /v2/hosts
//   - GetHost: GET /v2/hosts/{id}
//   - DeleteHost: DELETE /v2/hosts/{id}
//   - SyncHost: POST /v2/hosts/{id}/sync
//   - StartHost / StopHost: POST /v2/hosts/{id}/start | /stop
//   - RetryHost: POST /v2/hosts/{id}/retry
//   - ListHostCVDs: GET /v2/hosts/{id}/cvds (handler_cvds.go, service_cvds.go),
//     a live Host Orchestrator read of every CVD on the host
//
// # Service layer
//
// service.go owns the lifecycle logic (create/delete enqueue transition
// tasks atomically with their row writes; quota is consumed and released in
// the same commits). service_provision.go runs the queued provision flow
// against GCE, including machine-type resolution; service_placement.go
// ranks zones for auto placement and fails a provision over to the next
// ranked zone on a classified capacity error, recording zone_health;
// service_remediate.go recovers CVD-poisoned hosts via host-wide reset;
// service_stop.go waits on a user stop and settles the host and its devices.
// image_resolver.go resolves the host image (cached, singleflight);
// instance_builder.go assembles the GCE instance request.
//
// # Task handlers (queue actuators, not HTTP)
//
// handler_provision.go runs host.provision and host.deprovision;
// handler_reconcile.go verifies and heals host.reconcile suspicion;
// handler_remediate.go runs host.remediate; handler_stop.go runs
// host.stop_settle.
//
// repository.go is the Spanner persistence layer, including the atomic
// quota-coupled create/delete commits. wire.go assembles the slice
// (repository, image resolver, service, handler) for the composition root.
package hostfeature
