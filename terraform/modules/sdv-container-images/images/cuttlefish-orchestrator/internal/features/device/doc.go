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

// Package devicefeature implements the device feature following the
// split-stack architecture pattern.
//
// This package contains:
//   - Handler: HTTP handlers using the Huma framework for OpenAPI generation,
//     plus the chi-only WebRTC signaling handlers (handler_signaling*.go)
//   - Service: Business logic for device operations
//   - Repository: Spanner-based persistence layer
//   - Types: Request/response types with validation
//   - Wire: slice assembly for the composition root (wire.go)
//
// The placement and cvd sub-packages hold declarative placement resolution
// and CVD actuation; both wire through this slice.
//
// The device feature manages virtual Android devices (Cuttlefish VMs)
// including creation, deletion, start/stop operations, and health monitoring.
//
// API endpoints:
//   - GET /v2/devices - List devices
//   - POST /v2/devices - Create a device
//   - GET /v2/devices/{id} - Get device details
//   - DELETE /v2/devices/{id} - Delete a device
//   - POST /v2/devices/{id}/start - Start a device
//   - POST /v2/devices/{id}/stop - Stop a device
package devicefeature
