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

// Package adminfeature is the operator surface: platform statistics, task
// queue administration, the audit feed, capacity lenses, and the
// control-plane snapshot the operations console renders.
//
// It is a pure aggregation slice with no persistence and no business logic of
// its own. Every capability is consumed through a narrow injected reader
// interface (handler.go, wire.go), so the slice composes over the rest of
// the system without importing it.
//
// # AdminHandler (authenticated)
//
//   - GET /v2/admin/stats, platform statistics (handler.go)
//   - GET /v2/admin/control-plane, the live control-plane snapshot: queue
//     depth and type×state grid, working set, leader leases, worker pool,
//     zone census with zone_health cooldown cells, regional quotas, and the
//     Spot panel (fleet census, 7d preemption KPIs from the audit log,
//     advice-sync health) (handler_controlplane.go)
//   - GET /v2/admin/tasks/failed, GET /v2/admin/tasks/stats,
//     POST /v2/admin/tasks/{id}/retry, DELETE /v2/admin/tasks/{id}
//     (handler_tasks.go)
//   - GET /v2/admin/audit, the audit log feed (handler_audit.go)
//   - GET /v2/admin/reclaimable and GET /v2/admin/provisioning-stats, the
//     reclaimable-capacity and 7d provisioning-outcome lenses
//     (handler_lenses.go)
//
// # HealthHandler (public, no auth)
//
//   - GET /health, GET /ready, GET /info (handler_health.go)
package adminfeature
