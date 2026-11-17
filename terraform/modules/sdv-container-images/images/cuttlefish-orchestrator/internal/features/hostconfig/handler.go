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

package hostconfigfeature

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// Registration and audit-metadata strings repeated across the surface
// (goconst).
const (
	tagAdmin           = "Admin"
	tagHostImageConfig = "Host Image Config"
	schemeBearerAuth   = "bearerAuth"
	metaKeyKind        = "kind"
	metaSystemConfig   = "system_config"
	metaHostImageKind  = "host_image_config"
	metaKeyImageFamily = "image_family"
	metaKeyPool        = "pool"
)

// Handler exposes the host image configuration management API.
type Handler struct {
	service     *Service
	auditLogger audit.Sink
}

// NewHandler creates a new host image config handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// SetAuditLogger enables audit logging of config changes (nil disables).
func (h *Handler) SetAuditLogger(l audit.Sink) {
	h.auditLogger = l
}

// auditConfigChange records a system-config change attributed to the request
// principal (every entry here is a config change on the system resource).
func (h *Handler) auditConfigChange(ctx context.Context, resourceID string, metadata map[string]any) {
	audit.LogChange(ctx, h.auditLogger, audit.ActionConfigChange, audit.ResourceSystem, resourceID, metadata)
}

// Register registers host image config operations with the Huma API.
func (h *Handler) Register(api huma.API) {
	const pathConfigByFamily = "/v2/admin/host-image-configs/{family}"

	huma.Register(api, huma.Operation{
		OperationID: "list-host-image-configs",
		Method:      http.MethodGet,
		Path:        "/v2/admin/host-image-configs",
		Summary:     "List host image configurations",
		Description: "Returns every machine family's host image configuration, each with the image it resolves to now or the reason it cannot boot a host",
		Tags:        []string{tagHostImageConfig},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
	}, h.ListHostImageConfigs)

	huma.Register(api, huma.Operation{
		OperationID: "get-host-image-config",
		Method:      http.MethodGet,
		Path:        pathConfigByFamily,
		Summary:     "Get a host image configuration",
		Description: "Returns details of a specific host image configuration",
		Tags:        []string{tagHostImageConfig},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
		Errors:      []int{http.StatusNotFound},
	}, h.GetHostImageConfig)

	huma.Register(api, huma.Operation{
		OperationID: "create-host-image-config",
		Method:      http.MethodPost,
		Path:        "/v2/admin/host-image-configs",
		Summary:     "Create a host image configuration",
		Description: "Creates a host image configuration for a machine family. An enabled configuration whose image family does not resolve to a bootable image is refused with 422",
		Tags:        []string{tagHostImageConfig},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
		Errors:      []int{http.StatusBadRequest, http.StatusUnauthorized},
	}, h.CreateHostImageConfig)

	huma.Register(api, huma.Operation{
		OperationID: "update-host-image-config",
		Method:      http.MethodPut,
		Path:        pathConfigByFamily,
		Summary:     "Update a host image configuration",
		Description: "Updates an existing host image configuration. An enabled configuration whose image family does not resolve to a bootable image is refused with 422",
		Tags:        []string{tagHostImageConfig},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
		Errors:      []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound},
	}, h.UpdateHostImageConfig)

	huma.Register(api, huma.Operation{
		OperationID: "delete-host-image-config",
		Method:      http.MethodDelete,
		Path:        pathConfigByFamily,
		Summary:     "Delete a host image configuration",
		Description: "Deletes a host image configuration for a machine family",
		Tags:        []string{tagHostImageConfig},
		Security:    []map[string][]string{{schemeBearerAuth: {}}},
		Errors:      []int{http.StatusNotFound},
	}, h.DeleteHostImageConfig)

	h.registerSettings(api)
}

// requirePrincipal returns the authenticated caller.
func (h *Handler) requirePrincipal(ctx context.Context) (*auth.Principal, error) {
	principal, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized("Authentication required")
	}
	return principal, nil
}

// ListHostImageConfigs returns a list of all host image configurations.
func (h *Handler) ListHostImageConfigs(ctx context.Context, input *ListHostImageConfigsInput) (*ListHostImageConfigsOutput, error) {
	filter := hostconfig.ListFilter{
		EnabledOnly: input.EnabledOnly,
		Limit:       int64(input.Limit),
		Offset:      int64(input.Offset),
	}

	configs, total, err := h.service.List(ctx, filter)
	if err != nil {
		return nil, web.MapError(err)
	}

	output := &ListHostImageConfigsOutput{}
	output.Body.Items = configs
	output.Body.Limit = input.Limit
	output.Body.Offset = input.Offset
	output.Body.Total = total

	return output, nil
}

// GetHostImageConfig returns a specific host image configuration.
func (h *Handler) GetHostImageConfig(ctx context.Context, input *GetHostImageConfigInput) (*GetHostImageConfigOutput, error) {
	config, err := h.service.Get(ctx, input.MachineFamily)
	if err != nil {
		return nil, web.MapError(err)
	}

	return &GetHostImageConfigOutput{Body: config}, nil
}

// CreateHostImageConfig creates a new host image configuration.
func (h *Handler) CreateHostImageConfig(ctx context.Context, input *CreateHostImageConfigInput) (*CreateHostImageConfigOutput, error) {
	principal, err := h.requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}

	enabled := true
	if input.Body.Enabled != nil {
		enabled = *input.Body.Enabled
	}

	config := &hostconfig.HostImageConfig{
		MachineFamily: input.Body.MachineFamily,
		ImageFamily:   input.Body.ImageFamily,
		SourceProject: input.Body.SourceProject,
		Description:   input.Body.Description,
		Enabled:       enabled,
		CreatedBy:     principal.Email,
		UpdatedBy:     principal.Email,
	}

	if err := h.service.Create(ctx, config); err != nil {
		return nil, web.MapError(err)
	}

	h.auditConfigChange(ctx, config.MachineFamily, map[string]any{
		metaKeyKind: metaHostImageKind, "op": "create",
		metaKeyImageFamily: config.ImageFamily, "source_project": config.SourceProject,
	})

	return &CreateHostImageConfigOutput{Body: config}, nil
}

// UpdateHostImageConfig updates an existing host image configuration.
func (h *Handler) UpdateHostImageConfig(ctx context.Context, input *UpdateHostImageConfigInput) (*UpdateHostImageConfigOutput, error) {
	principal, err := h.requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}

	// Get existing config.
	existing, err := h.service.Get(ctx, input.MachineFamily)
	if err != nil {
		return nil, web.MapError(err)
	}

	// Apply updates.
	existing.ImageFamily = input.Body.ImageFamily
	existing.SourceProject = input.Body.SourceProject
	existing.Description = input.Body.Description
	existing.UpdatedBy = principal.Email

	if input.Body.Enabled != nil {
		existing.Enabled = *input.Body.Enabled
	}

	if err := h.service.Update(ctx, existing); err != nil {
		return nil, web.MapError(err)
	}

	h.auditConfigChange(ctx, existing.MachineFamily, map[string]any{
		metaKeyKind: metaHostImageKind, "op": "update",
		metaKeyImageFamily: existing.ImageFamily, "source_project": existing.SourceProject,
		"enabled": existing.Enabled,
	})

	return &UpdateHostImageConfigOutput{Body: existing}, nil
}

// DeleteHostImageConfig deletes a host image configuration.
func (h *Handler) DeleteHostImageConfig(ctx context.Context, input *DeleteHostImageConfigInput) (*DeleteHostImageConfigOutput, error) {
	if err := h.service.Delete(ctx, input.MachineFamily); err != nil {
		return nil, web.MapError(err)
	}

	h.auditConfigChange(ctx, input.MachineFamily, map[string]any{
		metaKeyKind: metaHostImageKind, "op": "delete",
	})

	output := &DeleteHostImageConfigOutput{}
	output.Body.Message = "Host image configuration deleted successfully"

	return output, nil
}
