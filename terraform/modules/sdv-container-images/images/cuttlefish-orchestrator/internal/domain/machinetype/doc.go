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

// Package machinetype defines the domain model for GCP Compute Engine
// machine types used to provision host VMs.
//
// Machine types are stored in the database to allow dynamic configuration
// without code changes. The orchestrator validates host creation requests
// against the available machine types.
//
// # Categories
//
// Machine types are organized into categories:
//   - general_purpose: N1, N2, N4, C4, etc.
//   - compute_optimized: C2, C3, H3, etc.
//   - memory_optimized: M1, M2, M3, M4, X4
//   - accelerator_optimized: A2, A3, A4, G2, G4
//   - storage_optimized: Z3
//   - cost_optimized: E2
//
// # GPU Support
//
// Some machine types support attaching GPUs (e.g., N1 with T4).
// The GPUSupport field indicates capability, while DefaultGPUType
// and DefaultGPUCount specify recommended GPU configuration.
package machinetype
