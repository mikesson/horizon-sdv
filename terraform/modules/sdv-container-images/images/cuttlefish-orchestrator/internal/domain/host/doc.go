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

// Package host provides domain types and interfaces for cloud VM hosts
// that run virtual Android devices.
//
// A Host represents a Google Compute Engine (GCE) virtual machine instance
// that can host one or more Cuttlefish devices. Each host has:
//   - A unique ID and GCE instance name
//   - Zone and project information
//   - IP addresses (internal and external)
//   - Device capacity and current count
//   - Status indicating its lifecycle state
//   - Host Orchestrator port for communication
//
// The package defines the Repository interface for persistence operations
// and the Service interface for business logic.
package host
