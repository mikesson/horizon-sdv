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

// Package hoclient provides an HTTP client for communicating with the
// Host Orchestrator service running on each cloud VM.
//
// The Host Orchestrator is a local service on each VM that manages
// Cuttlefish device instances. This client communicates with it to:
//   - Create and delete CVD instances
//   - Start and stop CVD instances
//   - Query CVD status and health
//   - Manage displays and signaling
//
// The package provides a Factory for creating clients with consistent
// configuration, including timeouts and retry policies. The Factory owns the
// per-host circuit breakers and one shared HTTP transport, so breaker state
// and pooled connections persist across the short-lived clients it hands out.
//
// Use NewFactory to create a Factory, then ClientForHost to get a client
// for a specific host.
package hoclient
