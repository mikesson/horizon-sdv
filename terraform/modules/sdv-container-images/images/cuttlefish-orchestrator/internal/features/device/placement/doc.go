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

// Package placement resolves a declarative device create's intent into
// concrete resources: the build (registered, tracked, or the platform
// default), the host to land on (bin-packed over the fleet's placeable
// capacity), and, when nothing has room, the host-provisioning template
// (machine shape from operator policy, provisioning economics from the
// capacity intent, zone from capacity-ranked placement).
//
// Resolver (resolver.go, spec.go) owns intent resolution and depends on
// consumer-side ports it declares here (zone ranking, policy reads, the
// tracking-build registrar, the capacity store). CapacityRepository
// (capacity.go) owns the placeable-capacity queries against Spanner. The
// COMMIT of a resolution, creating the device and provisioning the host,
// stays with the device service: placement decides, it never writes.
package placement
