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

import (
	"context"
	"maps"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// Tags grouping the catalog operations in the OpenAPI document. The curation
// endpoints join the platform-wide Admin group rather than the read-only
// catalog one.
const (
	tagGCECatalog = "GCE Catalog"
	tagAdmin      = "Admin"
)

// ZoneEnablement is the operator's zone decision, read and written whole. The
// ZonePolicy in this package satisfies it.
type ZoneEnablement interface {
	EnabledZones
	SetEnabledZones(ctx context.Context, zones []string) error
}

// Handler manages the HTTP transport layer for GCE catalog operations.
type Handler struct {
	zones ZoneEnablement
	// shapes answers what Compute Engine offers; nil answers "unknown", which
	// the availability view renders the same way it renders "not offered".
	shapes ShapeCatalog
	// machineTypes is the curated catalog the per-zone shape availability is
	// reported against. It is the same list the snapshot's machine-type filter
	// is built from, so the curation view and the snapshot cannot disagree about
	// what "the fleet's shapes" means.
	machineTypes machinetype.Catalog
	auditLogger  audit.Sink
}

// NewHandler creates a new GCE catalog handler.
func NewHandler(zones ZoneEnablement, shapes ShapeCatalog, machineTypes machinetype.Catalog) *Handler {
	return &Handler{
		zones:        zones,
		shapes:       shapes,
		machineTypes: machineTypes,
	}
}

// SetAuditLogger enables audit logging of zone curation (nil disables).
func (h *Handler) SetAuditLogger(l audit.Sink) {
	h.auditLogger = l
}

// Register registers GCE catalog operations with the Huma API.
func (h *Handler) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-zone-availability",
		Method:      http.MethodGet,
		Path:        "/v2/zones/availability",
		Summary:     "List zone availability",
		Description: "Returns availability status for zones, optionally filtered by machine type and GPU",
		Tags:        []string{tagGCECatalog},
		Security:    web.BearerAuth(),
	}, h.ListZoneAvailability)

	huma.Register(api, huma.Operation{
		OperationID: "admin-list-zones",
		Method:      http.MethodGet,
		Path:        "/v2/admin/zones",
		Summary:     "List catalog zones",
		Description: "Returns every zone Compute Engine offers this project, with its enablement and the curated shapes it stocks",
		Tags:        []string{tagAdmin},
		Security:    web.BearerAuth(),
	}, h.ListAdminZones)

	huma.Register(api, huma.Operation{
		OperationID: "admin-set-enabled-zones",
		Method:      http.MethodPut,
		Path:        "/v2/admin/zones/enabled",
		Summary:     "Set enabled zones",
		Description: "Replaces the set of zones enabled for host creation",
		Tags:        []string{tagAdmin},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusBadRequest},
	}, h.SetEnabledZones)

	huma.Register(api, huma.Operation{
		OperationID: "admin-catalog-freshness",
		Method:      http.MethodGet,
		Path:        "/v2/admin/catalog/freshness",
		Summary:     "Get catalog freshness",
		Description: "Reports when the Compute Engine catalog snapshot was built and which scopes it is silent about",
		Tags:        []string{tagAdmin},
		Security:    web.BearerAuth(),
	}, h.CatalogFreshness)
}

// ListZoneAvailability returns the availability status of the enabled zones.
func (h *Handler) ListZoneAvailability(ctx context.Context, input *ListZoneAvailabilityInput) (*ListZoneAvailabilityOutput, error) {
	enabled, err := h.zones.ListEnabledZones(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}
	universe := h.zoneUniverse()

	output := &ListZoneAvailabilityOutput{}
	for _, zone := range enabled {
		availability := ZoneAvailability{
			Zone:      zone,
			Region:    gcecatalog.RegionFromZone(zone),
			Status:    string(universe[zone]),
			IsEnabled: true,
		}

		// Availability is reported only where Compute Engine positively offers
		// the resource: this view answers "can I place here", and an unknown
		// cell is not a promise.
		if input.MachineType != "" {
			availability.MachineTypeAvailable = h.offered(h.machineTypeAvailability(zone, input.MachineType))
		}
		if input.GPUType != "" {
			availability.GPUAvailable = h.offered(h.acceleratorAvailability(zone, input.GPUType))
			for _, a := range h.zoneAccelerators(zone) {
				if a.AcceleratorType == input.GPUType {
					availability.GPUMaxCount = a.MaxCount
					break
				}
			}
		}

		output.Body.Zones = append(output.Body.Zones, availability)
	}

	return output, nil
}

// offered narrows a three-state answer to the boolean the availability view
// publishes.
func (h *Handler) offered(a gcecatalog.Availability) bool {
	return a == gcecatalog.AvailabilityOffered
}

// machineTypeAvailability, acceleratorAvailability, zoneAccelerators and
// zoneUniverse read the Compute Engine snapshot, degrading an unwired one to
// "unknown" rather than to "not offered".
func (h *Handler) machineTypeAvailability(zone, machineType string) gcecatalog.Availability {
	if h.shapes == nil {
		return gcecatalog.AvailabilityUnknown
	}
	return h.shapes.MachineTypeAvailability(zone, machineType)
}

func (h *Handler) acceleratorAvailability(zone, acceleratorType string) gcecatalog.Availability {
	if h.shapes == nil {
		return gcecatalog.AvailabilityUnknown
	}
	return h.shapes.AcceleratorAvailability(zone, acceleratorType)
}

func (h *Handler) zoneAccelerators(zone string) []*gcecatalog.Accelerator {
	if h.shapes == nil {
		return nil
	}
	return h.shapes.ZoneAccelerators(zone)
}

func (h *Handler) zoneUniverse() map[string]gcecatalog.ZoneStatus {
	if h.shapes == nil {
		return nil
	}
	return h.shapes.Zones()
}

// ListAdminZones returns every zone Compute Engine offers this project for the
// curation view, with the enablement decision and per-zone shape availability
// beside it, which is the question an operator enabling a zone is actually
// asking.
//
// A zone an operator enabled that the snapshot does not hold is still listed:
// dropping it would hide a decision that is live in placement.
func (h *Handler) ListAdminZones(ctx context.Context, _ *ListAdminZonesInput) (*ListAdminZonesOutput, error) {
	enabled, err := h.zones.ListEnabledZones(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}
	curated, err := h.curatedShapes(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}

	universe := h.zoneUniverse()
	names := make(map[string]bool, len(universe)+len(enabled))
	for zone := range universe {
		names[zone] = true
	}
	isEnabled := make(map[string]bool, len(enabled))
	for _, zone := range enabled {
		names[zone] = true
		isEnabled[zone] = true
	}

	output := &ListAdminZonesOutput{}
	output.Body.CuratedShapes = curated
	output.Body.Zones = make([]AdminZone, 0, len(names))
	for _, zone := range slices.Sorted(maps.Keys(names)) {
		offered, reported := h.offeredShapes(zone, curated)
		output.Body.Zones = append(output.Body.Zones, AdminZone{
			Zone:           zone,
			Region:         gcecatalog.RegionFromZone(zone),
			Status:         string(universe[zone]),
			IsEnabled:      isEnabled[zone],
			OfferedShapes:  offered,
			ShapesReported: reported,
		})
	}
	return output, nil
}

// curatedShapes lists the machine types the fleet provisions, enabled or not.
// It is the same set the snapshot's machine-type filter is built from.
func (h *Handler) curatedShapes(ctx context.Context) ([]string, error) {
	rows, err := h.machineTypes.List(ctx, machinetype.ListFilter{})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, mt := range rows {
		names = append(names, mt.ID)
	}
	slices.Sort(names)
	return names, nil
}

// offeredShapes intersects the curated catalog with what a zone stocks, and
// reports whether the snapshot had an answer at all. A zone reported as
// offering none of them is a different fact from a zone nothing was reported
// about, and the curation view has to be able to show both.
func (h *Handler) offeredShapes(zone string, curated []string) ([]string, bool) {
	offered := make([]string, 0, len(curated))
	reported := false
	for _, name := range curated {
		switch h.machineTypeAvailability(zone, name) {
		case gcecatalog.AvailabilityOffered:
			reported = true
			offered = append(offered, name)
		case gcecatalog.AvailabilityNotOffered:
			reported = true
		case gcecatalog.AvailabilityUnknown:
		}
	}
	return offered, reported
}

// SetEnabledZones replaces the set of zones enabled for host creation.
//
// Validated against the catalog snapshot before it is stored, and that refusal
// is the half everything else rests on: an enabled zone Compute Engine does not
// offer can never take a host, so storing it would leave an operator with a
// zone that looks admitted and silently ranks nothing. With no snapshot only
// the name shape is checked, because a deployment must be configurable before
// its first catalog refresh lands.
func (h *Handler) SetEnabledZones(ctx context.Context, input *SetEnabledZonesInput) (*SetEnabledZonesOutput, error) {
	if err := gcecatalog.ValidateEnabledZones(input.Body.Zones, h.zoneUniverse()); err != nil {
		return nil, web.MapError(err)
	}
	if err := h.zones.SetEnabledZones(ctx, input.Body.Zones); err != nil {
		return nil, web.MapError(err)
	}

	stored, err := h.zones.ListEnabledZones(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}

	// The set goes into the entry whole rather than as a count. The config row
	// is overwritten in place, so the audit log is the only durable answer to
	// which zones the fleet could place in before somebody changed it.
	audit.LogChange(ctx, h.auditLogger, audit.ActionConfigChange, audit.ResourceSystem, "zones-enabled",
		map[string]any{"kind": attrZone, "changes": map[string]any{"zones": stored}})

	output := &SetEnabledZonesOutput{}
	output.Body.Zones = stored
	return output, nil
}

// CatalogFreshness reports the snapshot's age and the scopes it is silent
// about.
func (h *Handler) CatalogFreshness(_ context.Context, _ *CatalogFreshnessInput) (*CatalogFreshnessOutput, error) {
	output := &CatalogFreshnessOutput{}
	output.Body.Partial = []string{}
	if h.shapes == nil {
		return output, nil
	}
	if builtAt := h.shapes.BuiltAt(); !builtAt.IsZero() {
		output.Body.BuiltAt = &builtAt
	}
	if partial := h.shapes.Partial(); len(partial) > 0 {
		output.Body.Partial = partial
	}
	return output, nil
}
