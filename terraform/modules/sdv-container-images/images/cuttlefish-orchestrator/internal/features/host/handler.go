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

package hostfeature

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// HostHandler manages the HTTP transport layer for host operations.
//
// It translates HTTP requests into domain commands, validates input using Huma,
// and maps domain errors to appropriate HTTP status codes.
type HostHandler struct {
	service *HostService
	cvds    *CVDViewer
}

// NewHostHandler creates a new host handler.
func NewHostHandler(service *HostService) *HostHandler {
	return &HostHandler{service: service}
}

// pathHostByID is the single-host path, shared by get, delete and update.
const pathHostByID = "/v2/hosts/{id}"

// tagHosts groups the host operations in the OpenAPI document.
const tagHosts = "Hosts"

// Register registers host operations with the Huma API.
func (h *HostHandler) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-hosts",
		Method:      http.MethodGet,
		Path:        "/v2/hosts",
		Summary:     "List hosts",
		Description: "Returns a list of Cuttlefish host VMs",
		Tags:        []string{tagHosts},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusBadRequest, http.StatusUnauthorized},
	}, h.ListHosts)

	huma.Register(api, huma.Operation{
		OperationID: "create-host",
		Method:      http.MethodPost,
		Path:        "/v2/hosts",
		Summary:     "Create a host",
		Description: "Provisions a new GCE VM to run Cuttlefish devices",
		Tags:        []string{tagHosts},
		Security:    web.BearerAuth(),
		// 422: catalog validation failed, or an Idempotency-Key reused with a
		//      different request. 429: the platform host quota is exhausted.
		// (No 409: nothing on the create path produces a conflict, since host
		// IDs are generated and names are not unique.)
		Errors: []int{http.StatusUnauthorized, http.StatusUnprocessableEntity, http.StatusTooManyRequests},
	}, h.CreateHost)

	huma.Register(api, huma.Operation{
		OperationID: "get-host",
		Method:      http.MethodGet,
		Path:        pathHostByID,
		Summary:     "Get a host",
		Description: "Returns details of a specific host",
		Tags:        []string{tagHosts},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound},
	}, h.GetHost)

	huma.Register(api, huma.Operation{
		OperationID: "delete-host",
		Method:      http.MethodDelete,
		Path:        pathHostByID,
		Summary:     "Delete a host",
		Description: "Stops and removes a host VM",
		Tags:        []string{tagHosts},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound, http.StatusConflict},
	}, h.DeleteHost)

	huma.Register(api, huma.Operation{
		OperationID: "sync-host",
		Method:      http.MethodPost,
		Path:        "/v2/hosts/{id}/sync",
		Summary:     "Sync host state",
		Description: "Synchronizes host state with GCE",
		Tags:        []string{tagHosts},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound},
	}, h.SyncHost)

	huma.Register(api, huma.Operation{
		OperationID: "start-host",
		Method:      http.MethodPost,
		Path:        "/v2/hosts/{id}/start",
		Summary:     "Start a host",
		Description: "Starts a stopped GCE host VM",
		Tags:        []string{tagHosts},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound, http.StatusConflict},
	}, h.StartHost)

	huma.Register(api, huma.Operation{
		OperationID: "stop-host",
		Method:      http.MethodPost,
		Path:        "/v2/hosts/{id}/stop",
		Summary:     "Stop a host",
		Description: "Stops a running GCE host VM",
		Tags:        []string{tagHosts},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound, http.StatusConflict},
	}, h.StopHost)

	huma.Register(api, huma.Operation{
		OperationID: "retry-host",
		Method:      http.MethodPost,
		Path:        "/v2/hosts/{id}/retry",
		Summary:     "Retry a preempted or failed host",
		Description: "Recreates the GCE instance for a host that was preempted or failed. For Spot VMs, optionally fall back to standard provisioning.",
		Tags:        []string{tagHosts},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound, http.StatusConflict},
	}, h.RetryHost)

	huma.Register(api, huma.Operation{
		OperationID: "update-host",
		Method:      http.MethodPatch,
		Path:        pathHostByID,
		Summary:     "Update a host",
		Description: "Changes how many devices a live host will accept. Lowering the limit below the devices already on the host is rejected.",
		Tags:        []string{tagHosts},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict},
	}, h.UpdateHost)

	huma.Register(api, huma.Operation{
		OperationID: "extend-host",
		Method:      http.MethodPost,
		Path:        "/v2/hosts/{id}/extend",
		Summary:     "Extend a host's lease",
		Description: "Adds hours to a reserved host's lease, counted from the later of now and its current expiry. Fleet, dedicated and batch hosts, and hosts protected by pinned devices, are refused. A lease that would pass the time Compute Engine stops the VM is refused.",
		Tags:        []string{tagHosts},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound, http.StatusConflict},
	}, h.ExtendHost)

	h.registerCVDs(api)
}

// ListHosts returns a list of hosts (Huma handler).
func (h *HostHandler) ListHosts(ctx context.Context, input *ListHostsInput) (*ListHostsOutput, error) {
	cursor, err := web.DecodePageToken(input.PageToken, input.Offset)
	if err != nil {
		return nil, web.MapError(err)
	}
	// One row past the page says whether another page exists.
	filter := host.ListFilter{
		Limit:  input.Limit + 1,
		Offset: input.Offset,
	}
	if cursor != nil {
		filter.AfterCreatedAt, filter.AfterID = cursor.CreatedAt, cursor.ID
	}

	if input.Name != "" {
		filter.Name = &input.Name
	}
	if input.Zone != "" {
		filter.Zone = &input.Zone
	}
	if input.Status != "" {
		s := host.Status(input.Status)
		filter.Status = &s
	}
	if input.Class != "" {
		c := host.Class(input.Class)
		filter.Class = &c
	}
	creator, err := auth.CreatorScope(ctx, input.CreatedBy, input.Mine)
	if err != nil {
		return nil, web.MapError(err)
	}
	if creator != "" {
		filter.CreatedBy = &creator
	}

	hosts, err := h.service.List(ctx, filter)
	if err != nil {
		return nil, web.MapError(err)
	}

	total, err := h.service.Count(ctx, filter)
	if err != nil {
		return nil, web.MapError(err)
	}

	output := &ListHostsOutput{}
	if len(hosts) > input.Limit {
		hosts = hosts[:input.Limit]
		last := hosts[len(hosts)-1]
		output.Body.NextPageToken = web.EncodePageToken(web.PageCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	output.Body.Items = hosts
	output.Body.Limit = input.Limit
	output.Body.Offset = input.Offset
	output.Body.Total = int(total)
	return output, nil
}

// GetHost returns a host by ID (Huma handler).
func (h *HostHandler) GetHost(ctx context.Context, input *GetHostInput) (*GetHostOutput, error) {
	hostVM, err := h.service.Get(ctx, input.ID)
	if err != nil {
		return nil, web.MapError(err)
	}

	return &GetHostOutput{Body: hostVM}, nil
}

// CreateHost provisions a new host (Huma handler).
func (h *HostHandler) CreateHost(ctx context.Context, input *CreateHostInput) (*CreateHostOutput, error) {
	principal, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized("Authentication required")
	}

	req := host.CreateRequest{
		Name: input.Body.Name,
		Zone: input.Body.Zone,
		// An explicitly named zone is a pin: the caller asked for exactly this
		// placement, so failover retries in place instead of moving the host.
		ZonePinned:        input.Body.Zone != "",
		MachineType:       input.Body.MachineType,
		GPUType:           input.Body.GPUType,
		GPUCount:          input.Body.GPUCount,
		MinCPUPlatform:    input.Body.MinCPUPlatform,
		BootDiskSizeGB:    input.Body.BootDiskSizeGB,
		MaxDevices:        input.Body.MaxDevices,
		ProvisioningModel: host.ProvisioningModel(input.Body.ProvisioningModel),
		TTLHours:          input.Body.TTLHours,
		Exclusive:         input.Body.Exclusive,
		IdempotencyKey:    input.IdempotencyKey,
	}

	hostVM, err := h.service.Create(ctx, req, principal.Email, host.OriginUser)
	if err != nil {
		return nil, web.MapError(err)
	}

	return &CreateHostOutput{Body: hostVM}, nil
}

// DeleteHost removes a host (Huma handler).
func (h *HostHandler) DeleteHost(ctx context.Context, input *DeleteHostInput) (*DeleteHostOutput, error) {
	if err := h.service.Delete(ctx, input.ID); err != nil {
		return nil, web.MapError(err)
	}
	return &DeleteHostOutput{}, nil
}

// SyncHost synchronizes host state (Huma handler).
func (h *HostHandler) SyncHost(ctx context.Context, input *SyncHostInput) (*SyncHostOutput, error) {
	if err := h.service.Sync(ctx, input.ID); err != nil {
		return nil, web.MapError(err)
	}
	return &SyncHostOutput{}, nil
}

// StartHost starts a stopped host (Huma handler).
func (h *HostHandler) StartHost(ctx context.Context, input *StartHostInput) (*StartHostOutput, error) {
	if err := h.service.Start(ctx, input.ID); err != nil {
		return nil, web.MapError(err)
	}

	output := &StartHostOutput{}
	output.Body.Message = "Host start initiated"
	return output, nil
}

// StopHost stops a running host (Huma handler).
func (h *HostHandler) StopHost(ctx context.Context, input *StopHostInput) (*StopHostOutput, error) {
	if err := h.service.Stop(ctx, input.ID); err != nil {
		return nil, web.MapError(err)
	}

	output := &StopHostOutput{}
	output.Body.Message = "Host stop initiated"
	return output, nil
}

// UpdateHost reshapes a live host (Huma handler).
func (h *HostHandler) UpdateHost(ctx context.Context, input *UpdateHostInput) (*UpdateHostOutput, error) {
	if input.Body.MaxDevices == nil {
		return nil, huma.Error400BadRequest("max_devices is required")
	}

	hostVM, err := h.service.UpdateMaxDevices(ctx, input.ID, *input.Body.MaxDevices)
	if err != nil {
		return nil, web.MapError(err)
	}

	return &UpdateHostOutput{Body: hostVM}, nil
}

// ExtendHost raises a host's lease (Huma handler).
func (h *HostHandler) ExtendHost(ctx context.Context, input *ExtendHostInput) (*ExtendHostOutput, error) {
	hostVM, err := h.service.Extend(ctx, input.ID, input.Body.Hours)
	if err != nil {
		return nil, web.MapError(err)
	}
	return &ExtendHostOutput{Body: hostVM}, nil
}

// RetryHost retries a preempted or failed host (Huma handler).
func (h *HostHandler) RetryHost(ctx context.Context, input *RetryHostInput) (*RetryHostOutput, error) {
	hostVM, err := h.service.Retry(ctx, input.ID, input.Body.FallbackToStandard)
	if err != nil {
		return nil, web.MapError(err)
	}

	return &RetryHostOutput{Body: hostVM}, nil
}
