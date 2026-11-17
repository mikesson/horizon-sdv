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

// Package device provides domain types and interfaces for virtual Android
// devices (Cuttlefish VMs) managed by the orchestrator.
//
// A Device represents a running or stopped Cuttlefish virtual device instance
// hosted on a cloud VM. Each device has:
//   - A unique ID and human-readable name
//   - An associated host (VM) that runs the device
//   - Status indicating its lifecycle state (starting, running, stopped, etc.)
//   - Health status for monitoring purposes
//   - Expiration time for automatic cleanup
//
// The package defines the Repository interface for persistence operations.
package device
