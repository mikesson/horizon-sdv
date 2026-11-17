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

package gcecatalogfeature

import "time"

// ListZoneAvailabilityInput defines the query parameters for listing zone availability.
type ListZoneAvailabilityInput struct {
	MachineType string `query:"machine_type" doc:"Filter by machine type availability"`
	GPUType     string `query:"gpu_type" doc:"Filter by GPU type availability"`
}

// ListZoneAvailabilityOutput is the response for listing zone availability.
type ListZoneAvailabilityOutput struct {
	Body struct {
		Zones []ZoneAvailability `json:"zones" doc:"List of zones with availability info"`
	}
}

// ZoneAvailability represents the availability status of a zone.
type ZoneAvailability struct {
	Zone                 string `json:"zone" doc:"Zone name (e.g., us-central1-a)"`
	Region               string `json:"region" doc:"Region name (e.g., us-central1)"`
	Status               string `json:"status" doc:"GCE zone status (UP or DOWN; empty when the catalog snapshot has not reported the zone)"`
	IsEnabled            bool   `json:"is_enabled" doc:"Whether the zone is enabled for host creation"`
	MachineTypeAvailable bool   `json:"machine_type_available,omitempty" doc:"Whether the requested machine type is available"`
	GPUAvailable         bool   `json:"gpu_available,omitempty" doc:"Whether the requested GPU type is available"`
	GPUMaxCount          int64  `json:"gpu_max_count,omitempty" doc:"Maximum GPU count available"`
}

// ListAdminZonesInput is the input for the admin zone catalog list.
type ListAdminZonesInput struct{}

// AdminZone is one zone Compute Engine offers this project, with the
// operator's enablement decision and which of the fleet's curated shapes the
// zone stocks.
type AdminZone struct {
	Zone      string `json:"zone" doc:"Zone name (e.g., us-central1-a)"`
	Region    string `json:"region" doc:"Region name (e.g., us-central1)"`
	Status    string `json:"status" doc:"GCE zone status (UP or DOWN)"`
	IsEnabled bool   `json:"is_enabled" doc:"Whether the zone is enabled for host creation"`
	// OfferedShapes is the intersection of the curated catalog and what this
	// zone stocks. Empty with ShapesReported false means Compute Engine said
	// nothing about the zone, which is not the same as offering nothing.
	OfferedShapes  []string `json:"offered_shapes" doc:"Curated machine types this zone offers"`
	ShapesReported bool     `json:"shapes_reported" doc:"Whether the catalog snapshot holds an answer for this zone"`
}

// ListAdminZonesOutput is the zone curation response: every zone Compute
// Engine offers, and the curated shapes their availability is judged against
// so the caller needs no second copy of what the fleet provisions.
type ListAdminZonesOutput struct {
	Body struct {
		Zones         []AdminZone `json:"zones" doc:"Every zone Compute Engine offers this project"`
		CuratedShapes []string    `json:"curated_shapes" doc:"The machine types the fleet provisions, which per-zone availability is reported for"`
	}
}

// SetEnabledZonesInput replaces the enabled zone set whole. Its own endpoint,
// so nothing that edits another setting can clear it by writing back a stale
// read.
type SetEnabledZonesInput struct {
	Body struct {
		Zones []string `json:"zones" doc:"Zones enabled for host creation; replaces the stored set whole, and an empty array disables placement everywhere"`
	}
}

// SetEnabledZonesOutput returns the stored enabled set.
type SetEnabledZonesOutput struct {
	Body struct {
		Zones []string `json:"zones" doc:"Zones enabled for host creation, sorted"`
	}
}

// CatalogFreshnessInput is the input for the catalog freshness read.
type CatalogFreshnessInput struct{}

// CatalogFreshnessOutput reports how current the in-process Compute Engine
// snapshot is, as age and silence. The old per-region sync status reported when
// a job last ran, which never said whether any particular cell was right.
type CatalogFreshnessOutput struct {
	Body struct {
		BuiltAt *time.Time `json:"built_at,omitempty" doc:"When the catalog snapshot was built (absent until the first refresh lands)"`
		Partial []string   `json:"partial" doc:"Catalog scopes Compute Engine did not report, so the snapshot is silent about them rather than knowing they are empty"`
	}
}
