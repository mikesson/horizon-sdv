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

// Package gcecatalogfeature validates host creation against the GCE catalog
// and ranks placement zones. It is the capacity brain behind create-time
// validation and dynamic placement.
//
// The catalog it reads has two halves with different owners. The operator's
// zone enablement is a validated system_config key this package owns
// (zone_policy.go). What Compute Engine offers (every zone it serves the
// project, which shapes and accelerators each stocks, and the live regional
// quota) comes from the in-process GCE snapshot behind ShapeCatalog, whose
// answers are three-state: a zone Compute Engine never reported is not a zone
// that offers nothing, and only a positive "not offered" may remove a
// placement candidate.
//
// Two types implement the domain Validator port as one assembled unit
// (NewCatalogValidator builds both):
//
//   - CatalogValidator (validator.go) is the admission half: imperative
//     pre-creation checks for one requested shape (zone existence and
//     enablement, machine type availability in the zone, GPU availability by
//     GPU mode, and regional quota headroom), returning structured errors
//     with suggestions and ranked alternative zones.
//   - ZoneRanker (ranking.go) is the placement half: RankZones puts every
//     enabled zone through one feasibility filter (what Compute Engine
//     structurally offers, regional quota headroom, and the stockouts CCO
//     itself suffered, each a different kind of evidence for "can I get this
//     shape here"), then scores the survivors on headroom (with an
//     operator-tunable margin), zone health, geographic proximity, and, for
//     Spot, capacity-advice obtainability, honoring operator policy (zone
//     pin, quota margin) from system_config.
//
// The two share the per-metric quota-judgment engine (quota_headroom.go:
// bindingDemand names the axes a shape draws from, judgeMetric resolves and
// judges each axis's regional row). scoring.go normalizes and weights the
// surviving candidates; geo.go derives proximity; metrics.go records
// placement decisions. wire.go assembles the validator and handler over the
// zone policy the composition root builds.
//
// The HTTP surface (handler.go):
//
//   - GET /v2/zones/availability - per-zone availability for a shape
//   - GET /v2/admin/zones - every zone Compute Engine offers, with enablement
//     and the curated shapes each stocks, for curation
//   - PUT /v2/admin/zones/enabled - replace the enabled set, validated
//     against the catalog snapshot
//   - GET /v2/admin/catalog/freshness - catalog snapshot age and the scopes
//     Compute Engine did not report
package gcecatalogfeature
