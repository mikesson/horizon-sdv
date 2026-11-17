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

// Package gcecatalog provides domain types for GCE resource availability:
// zones, accelerators, regional quotas, the placement plane's zone health and
// Spot capacity advice, and the vocabulary of the admission and ranking
// verdicts built on them.
//
// What Compute Engine offers is cached in memory only. The platform adapter
// refreshes an immutable snapshot from the aggregated Compute APIs and
// every reader takes it whole. What CCO holds the only copy of stays durable:
// the operator's enabled zone set (a validated system_config key, see
// zone_policy.go) and the stockouts CCO itself suffered.
package gcecatalog
