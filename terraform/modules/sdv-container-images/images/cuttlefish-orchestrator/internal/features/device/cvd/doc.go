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

// Package cvd converges a device's CVD on its host. It is the actuation half
// of the device slice, driven entirely from the durable task queue.
//
// Actuator (actuator.go) owns the Host Orchestrator work: the queued create
// with its journal-resume and adopt ladder (create.go), the canonical
// env_config payload assembly (payload.go), the probe-first cleanup that
// waits out hosts whose VM still exists (cleanup.go), the two-step zombie
// recovery of in-place restart then budgeted recreate (zombie.go), and the
// host-poison attribution for leaked-tap launch failures (poison.go).
//
// The worker handlers (handler_create.go, handler_cleanup.go,
// handler_reconcile.go) adapt the actuator to the task.Handler contract,
// translating its typed waits (host not ready, host not serving, launch in
// flight) into bounded postpones. Admission, placement, and every
// user-facing operation stay with the parent device feature. This package
// receives only work the device service already committed.
package cvd
