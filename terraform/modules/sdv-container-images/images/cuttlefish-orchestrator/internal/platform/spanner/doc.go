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

// Package spanner provides the managed Cloud Spanner client and the shared,
// cross-cutting repositories.
//
// # Client
//
// Client (client.go) wraps the Spanner client with pooled gRPC channels,
// keepalives, OpenTelemetry instrumentation, emulator support, and graceful
// shutdown. It exposes Single and StaleSingle (bounded-staleness reads for
// list/dashboard traffic), ReadWriteTransaction, Apply, and HealthCheck.
// clock.go supplies the server-time discipline lease comparisons rely on;
// query.go holds the package's generic query helpers.
//
// # Repositories
//
// Feature-owned entities (device, host, build, batch, machinetype, quota,
// gcecatalog, hostconfig) keep their repositories in their feature packages.
// This package holds the shared and cross-boundary ones: audit,
// idempotency, lease, system_config, the durable
// task queue (task_queue.go plus its enqueue/claim/sweep/reconcile-keys,
// admin, and steps files), zone_health, and spot_advice.
//
// # Models
//
// The models subpackage holds the row/scan structs: Spanner column mapping
// stays on these types so domain entities carry no driver tags, and
// repositories still accept and return domain types. spannertest holds the
// shared emulator bootstrap for repository tests.
package spanner
