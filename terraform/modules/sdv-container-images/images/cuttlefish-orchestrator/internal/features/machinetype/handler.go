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

package machinetypefeature

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// Tags grouping the machine-type operations in the OpenAPI document. Curation
// joins the platform-wide Admin group rather than the read-only catalog one.
const (
	tagMachineTypes = "Machine Types"
	tagAdmin        = "Admin"
)

// ShapeRefresher rereads what Compute Engine offers. The in-memory GCE catalog
// satisfies it.
//
// Curation calls it the moment it commits. Without that, a family an operator
// just admitted has no discovered shapes for up to a structural refresh
// interval, and the catalog answers every question about it as though the
// series did not exist. From the operator's side that is indistinguishable
// from having curated it wrong.
type ShapeRefresher interface {
	RefreshStructural(ctx context.Context) error
}

// MachineTypeHandler exposes the catalog of available GCE machine types.
//
// It provides endpoints for listing and filtering machine types, including
// metadata about GPU support, architecture, and virtualization capabilities,
// and the family curation that decides which of them the fleet offers.
type MachineTypeHandler struct {
	repo        machinetype.Repository
	curation    *CurationRepository
	refresher   ShapeRefresher
	auditLogger audit.Sink
	logger      *slog.Logger
}

// NewMachineTypeHandler creates a new machine type handler.
func NewMachineTypeHandler(repo machinetype.Repository, curation *CurationRepository, logger *slog.Logger) *MachineTypeHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &MachineTypeHandler{repo: repo, curation: curation, logger: logger}
}

// SetAuditLogger enables audit logging of catalog curation (nil disables).
func (h *MachineTypeHandler) SetAuditLogger(l audit.Sink) {
	h.auditLogger = l
}

// SetShapeRefresher wires the rediscovery a curation write triggers (nil
// leaves curation to take effect on the next structural refresh).
func (h *MachineTypeHandler) SetShapeRefresher(r ShapeRefresher) {
	h.refresher = r
}

// Register registers machine type operations with the Huma API.
func (h *MachineTypeHandler) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-machine-types",
		Method:      http.MethodGet,
		Path:        "/v2/machine-types",
		Summary:     "List machine types",
		Description: "Returns a list of available GCP machine types for host VMs",
		Tags:        []string{tagMachineTypes},
		Security:    web.BearerAuth(),
	}, h.ListMachineTypes)

	huma.Register(api, huma.Operation{
		OperationID: "get-machine-type",
		Method:      http.MethodGet,
		Path:        "/v2/machine-types/{id}",
		Summary:     "Get a machine type",
		Description: "Returns details of a specific machine type",
		Tags:        []string{tagMachineTypes},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound},
	}, h.GetMachineType)

	huma.Register(api, huma.Operation{
		OperationID: "get-default-machine-type",
		Method:      http.MethodGet,
		Path:        "/v2/machine-types/default",
		Summary:     "Get default machine type",
		Description: "Returns the default machine type for new hosts",
		Tags:        []string{tagMachineTypes},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound},
	}, h.GetDefaultMachineType)

	huma.Register(api, huma.Operation{
		OperationID: "list-machine-families",
		Method:      http.MethodGet,
		Path:        "/v2/machine-families",
		Summary:     "List machine families",
		Description: "Returns a summary of available machine type families (N1, C4, G2, etc.)",
		Tags:        []string{tagMachineTypes},
		Security:    web.BearerAuth(),
	}, h.ListMachineFamilies)

	huma.Register(api, huma.Operation{
		OperationID: "admin-list-machine-families",
		Method:      http.MethodGet,
		Path:        "/v2/admin/machine-families",
		Summary:     "List machine families for curation",
		Description: "Returns every curated family with the shapes Compute Engine offers under it, including the shapes too small to hold an Android VM, which the served catalog withholds",
		Tags:        []string{tagAdmin},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusServiceUnavailable},
	}, h.ListMachineFamiliesAdmin)

	huma.Register(api, huma.Operation{
		OperationID: "admin-update-machine-type",
		Method:      http.MethodPatch,
		Path:        "/v2/admin/machine-types/{id}",
		Summary:     "Update machine type curation",
		Description: "Enables/disables a machine type, moves the catalog default, or overrides its device capacity. Display order and GPU policy are curated on the machine family.",
		Tags:        []string{tagAdmin},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusBadRequest, http.StatusNotFound},
	}, h.UpdateMachineTypeAdmin)

	huma.Register(api, huma.Operation{
		OperationID: "admin-update-machine-family",
		Method:      http.MethodPatch,
		Path:        "/v2/admin/machine-families/{family}",
		Summary:     "Curate a machine family",
		Description: "Admits a machine family and records the facts Compute Engine does not state about it. Every shape the family offers is then discovered from Compute Engine, and no shape is ever entered by hand.",
		Tags:        []string{tagAdmin},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusBadRequest},
	}, h.UpdateMachineFamilyAdmin)
}

// UpdateMachineFamilyAdmin admits a machine family, or changes what the
// catalog knows about one it already holds.
//
// This is the whole of machine curation. A family Compute Engine ships
// tomorrow is enabled by naming it here. Its shapes, every width and every
// memory variant, arrive from discovery, sized by Compute Engine, with no
// further call, no row per shape and no release of this service.
func (h *MachineTypeHandler) UpdateMachineFamilyAdmin(ctx context.Context, input *UpdateMachineFamilyAdminInput) (*UpdateMachineFamilyAdminOutput, error) {
	update, err := familyUpdateOf(input)
	if err != nil {
		return nil, err
	}
	family, err := h.curation.UpdateFamilyCuration(ctx, input.Family, update)
	if err != nil {
		return nil, web.MapError(err)
	}

	h.rediscoverShapes(ctx, "machine_family", input.Family)
	audit.LogChange(ctx, h.auditLogger, audit.ActionConfigChange, audit.ResourceSystem, input.Family,
		map[string]any{"kind": "machine_family", "changes": familyPatchChanges(input)})

	return &UpdateMachineFamilyAdminOutput{Body: family}, nil
}

// rediscoverShapes rereads what Compute Engine offers for the curated
// families, so the write that just committed is placeable now rather than at
// the next structural cycle.
//
// Synchronous, and its failure is logged, never returned. The curation is
// already committed and correct; a Compute Engine call that failed changes
// only how soon the fleet notices, and reporting it as a failed write would
// invite the operator to repeat a change that already landed.
func (h *MachineTypeHandler) rediscoverShapes(ctx context.Context, kind, id string) {
	if h.refresher == nil {
		return
	}
	// Detached from the request's cancellation and bounded well under the
	// server's write deadline: a client that walks away must not leave the
	// catalog holding curation it has not discovered shapes for, and a slow
	// Compute Engine must not cost the response.
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rediscoverTimeout)
	defer cancel()
	if err := h.refresher.RefreshStructural(refreshCtx); err != nil {
		h.logger.Warn("curation committed but rediscovery failed; the shapes it offers land at the next structural refresh",
			slog.String("kind", kind), slog.String("id", id), slog.Any("error", err))
	}
}

// rediscoverTimeout bounds the rediscovery a curation write waits on. Three
// aggregated Compute Engine calls are seconds of wall time; the server's write
// deadline is 30, and a curation PATCH must return inside it whatever Compute
// Engine is doing.
const rediscoverTimeout = 10 * time.Second

// triStateBool converts an optional "true"/"false" query value to a filter
// pointer (nil when the parameter was omitted).
func triStateBool(s string) *bool {
	if s == "" {
		return nil
	}
	v := s == "true"
	return &v
}

// ListMachineTypes returns a list of machine types.
func (h *MachineTypeHandler) ListMachineTypes(ctx context.Context, input *ListMachineTypesInput) (*ListMachineTypesOutput, error) {
	filter := machinetype.ListFilter{
		GPUSupport: triStateBool(input.GPUSupport),
		IsEnabled:  triStateBool(input.Enabled),
	}

	if input.Family != "" {
		filter.Family = &input.Family
	}
	if input.Category != "" {
		cat := machinetype.Category(input.Category)
		filter.Category = &cat
	}
	if input.Architecture != "" {
		arch := machinetype.Architecture(input.Architecture)
		filter.Architecture = &arch
	}
	if input.Zone != "" {
		filter.Zone = &input.Zone
	}

	machineTypes, err := h.repo.List(ctx, filter)
	if err != nil {
		return nil, web.MapError(err)
	}

	output := &ListMachineTypesOutput{}
	output.Body.Items = machineTypes

	return output, nil
}

// GetMachineType returns a specific machine type.
func (h *MachineTypeHandler) GetMachineType(ctx context.Context, input *GetMachineTypeInput) (*GetMachineTypeOutput, error) {
	mt, err := h.repo.Get(ctx, input.ID)
	if err != nil {
		return nil, web.MapError(err)
	}

	return &GetMachineTypeOutput{Body: mt}, nil
}

// GetDefaultMachineType returns the default machine type.
func (h *MachineTypeHandler) GetDefaultMachineType(ctx context.Context, _ *GetDefaultMachineTypeInput) (*GetDefaultMachineTypeOutput, error) {
	mt, err := h.repo.GetDefault(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}

	return &GetDefaultMachineTypeOutput{Body: mt}, nil
}

// ListMachineFamilies returns a summary of unique machine families, aggregated
// by the repository in one query.
func (h *MachineTypeHandler) ListMachineFamilies(ctx context.Context, _ *ListMachineFamiliesInput) (*ListMachineFamiliesOutput, error) {
	families, err := h.repo.ListFamilies(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}

	summaries := make([]FamilySummary, 0, len(families))
	for _, f := range families {
		summaries = append(summaries, FamilySummary{
			Family:       f.Family,
			Name:         f.Name,
			Category:     string(f.Category),
			Architecture: string(f.Architecture),
			GPUSupport:   f.GPUSupport,
			TypeCount:    int(f.TypeCount),
		})
	}

	output := &ListMachineFamiliesOutput{}
	output.Body.Families = summaries

	return output, nil
}

// ListMachineFamiliesAdmin answers the curation view of the machine families:
// every curated series, admitted or not, with the shapes discovery found under
// it.
//
// Separate from the catalog's own family listing because the two answer
// different questions. That one aggregates the entries the fleet offers, so a
// family an operator withdrew is simply absent from it, and that is exactly
// the row the curation table exists to show a switch beside.
func (h *MachineTypeHandler) ListMachineFamiliesAdmin(ctx context.Context, _ *ListMachineFamiliesAdminInput) (*ListMachineFamiliesAdminOutput, error) {
	catalogs, err := h.repo.ListFamilyCatalog(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}

	families := make([]AdminFamily, 0, len(catalogs))
	for _, catalog := range catalogs {
		families = append(families, adminFamilyOf(catalog))
	}

	output := &ListMachineFamiliesAdminOutput{}
	output.Body.Families = families

	return output, nil
}

// adminFamilyOf renders one curated family and its shapes for the wire.
func adminFamilyOf(catalog *machinetype.FamilyCatalog) AdminFamily {
	curation := catalog.Curation
	family := AdminFamily{
		Family:          curation.Family,
		Name:            curation.DisplayName(),
		Description:     curation.Description,
		Category:        string(curation.Category),
		Architecture:    string(curation.Architecture),
		CPUType:         curation.CPUType,
		BootDiskType:    curation.BootDiskType,
		GPUMode:         curation.GPUMode,
		MaxGPUs:         curation.MaxGPUs,
		DefaultGPUType:  curation.DefaultGPUType,
		DefaultGPUCount: curation.DefaultGPUCount,
		IsEnabled:       curation.IsEnabled,
		SortOrder:       curation.SortOrder,
		ShapeCount:      len(catalog.Shapes),
		ViableCount:     int(catalog.Viable),
		Shapes:          make([]FamilyShape, 0, len(catalog.Shapes)),
	}
	for _, shape := range catalog.Shapes {
		family.Shapes = append(family.Shapes, FamilyShape{
			MachineType: shape.MachineType,
			VCPUs:       shape.VCPUs,
			MemoryGB:    shape.MemoryGB,
			Fit:         shape.Fit,
			Viable:      shape.Viable,
			IsExcluded:  shape.Excluded,
			IsDefault:   shape.IsDefault,
			MaxDevicesOverride: func() int64 {
				if shape.MaxDevicesOverride != nil {
					return *shape.MaxDevicesOverride
				}
				return 0
			}(),
		})
	}
	return family
}

// UpdateMachineTypeAdmin applies operator curation changes to a catalog entry.
func (h *MachineTypeHandler) UpdateMachineTypeAdmin(ctx context.Context, input *UpdateMachineTypeAdminInput) (*UpdateMachineTypeAdminOutput, error) {
	if err := h.validateAdminPatch(ctx, input); err != nil {
		return nil, err
	}

	mt, err := h.repo.UpdateAdminSettings(ctx, input.ID, machinetype.AdminUpdate{
		IsEnabled:  input.Body.IsEnabled,
		IsDefault:  input.Body.IsDefault,
		MaxDevices: input.Body.MaxDevices,
	})
	if err != nil {
		return nil, web.MapError(err)
	}

	h.rediscoverShapes(ctx, "machine_type", input.ID)
	audit.LogChange(ctx, h.auditLogger, audit.ActionConfigChange, audit.ResourceSystem, input.ID,
		map[string]any{"kind": "machine_type", "changes": adminPatchChanges(input)})

	return &UpdateMachineTypeAdminOutput{Body: mt}, nil
}

// validateAdminPatch rejects empty patches and any patch that would leave the
// catalog defaultless. The default moves by electing another machine type
// (never cleared), and it cannot be disabled, since GetDefault requires
// enabled.
func (h *MachineTypeHandler) validateAdminPatch(ctx context.Context, input *UpdateMachineTypeAdminInput) error {
	b := &input.Body
	if b.IsEnabled == nil && b.IsDefault == nil && b.MaxDevices == nil {
		return huma.Error400BadRequest("at least one of is_enabled, is_default, max_devices is required")
	}
	if b.IsDefault != nil && !*b.IsDefault {
		return huma.Error400BadRequest("is_default cannot be set to false; set is_default=true on another machine type instead")
	}
	if b.IsEnabled == nil || *b.IsEnabled {
		return nil
	}

	existing, err := h.repo.Get(ctx, input.ID)
	if err != nil {
		return web.MapError(err)
	}
	if existing.IsDefault || (b.IsDefault != nil && *b.IsDefault) {
		return huma.Error400BadRequest("the default machine type cannot be disabled; move the default first")
	}
	return nil
}

// adminPatchChanges renders the applied patch fields for the audit trail.
func adminPatchChanges(input *UpdateMachineTypeAdminInput) map[string]any {
	b := &input.Body
	changes := map[string]any{}
	putChange(changes, "is_enabled", b.IsEnabled)
	putChange(changes, "is_default", b.IsDefault)
	putChange(changes, "max_devices", b.MaxDevices)
	return changes
}

// familyUpdateOf renders the family PATCH as a curation update, refusing the
// one that changes nothing.
func familyUpdateOf(input *UpdateMachineFamilyAdminInput) (machinetype.FamilyUpdate, error) {
	b := &input.Body
	update := machinetype.FamilyUpdate{
		Name:            b.Name,
		Description:     b.Description,
		CPUType:         b.CPUType,
		BootDiskType:    b.BootDiskType,
		GPUMode:         b.GPUMode,
		MaxGPUs:         b.MaxGPUs,
		DefaultGPUType:  b.DefaultGPUType,
		DefaultGPUCount: b.DefaultGPUCount,
		IsEnabled:       b.IsEnabled,
		SortOrder:       b.SortOrder,
	}
	if b.Category != nil {
		category := machinetype.Category(*b.Category)
		update.Category = &category
	}
	if b.Architecture != nil {
		architecture := machinetype.Architecture(*b.Architecture)
		update.Architecture = &architecture
	}
	if update == (machinetype.FamilyUpdate{}) {
		return update, huma.Error400BadRequest(
			"a family patch must name at least one of is_enabled, name, description, category, architecture, cpu_type, boot_disk_type, gpu_mode, max_gpus, default_gpu_type, default_gpu_count, sort_order")
	}
	return update, nil
}

// familyPatchChanges renders the applied patch fields for the audit trail.
func familyPatchChanges(input *UpdateMachineFamilyAdminInput) map[string]any {
	b := &input.Body
	changes := map[string]any{}
	putChange(changes, "is_enabled", b.IsEnabled)
	putChange(changes, "name", b.Name)
	putChange(changes, "description", b.Description)
	putChange(changes, "category", b.Category)
	putChange(changes, "architecture", b.Architecture)
	putChange(changes, "cpu_type", b.CPUType)
	putChange(changes, "boot_disk_type", b.BootDiskType)
	putChange(changes, "gpu_mode", b.GPUMode)
	putChange(changes, "max_gpus", b.MaxGPUs)
	putChange(changes, "default_gpu_type", b.DefaultGPUType)
	putChange(changes, "default_gpu_count", b.DefaultGPUCount)
	putChange(changes, "sort_order", b.SortOrder)
	return changes
}

// putChange records a patch field that was actually named.
func putChange[T any](changes map[string]any, key string, value *T) {
	if value != nil {
		changes[key] = *value
	}
}
