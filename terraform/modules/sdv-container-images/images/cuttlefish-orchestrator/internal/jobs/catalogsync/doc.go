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

// Package catalogsync probes the GCE Spot capacity-advice API into the
// durable spot_advice table that Spot zone ranking consults.
//
// It is what remains of a job that used to mirror the whole Compute Engine
// catalog into Spanner. What Compute Engine offers is now cached in memory by
// the platform adapter and never stored, so the zone, machine-type,
// accelerator, quota and image crawls are gone; a probe result is not a fact
// about Google's inventory but a rate-limited measurement CCO paid for, which
// is what earns it a row and a leader gate.
//
// One leader-gated loop, wired only when CCO_SPOT_ADVICE enables it: every
// interval each enabled (zone × machine type) cell is probed, bare-metal
// shapes skipped (GCE never offers them as Spot capacity), and the results
// upserted into the TTL'd spot_advice table. Loop metrics land under
// cco.catalogsync.*.
package catalogsync
