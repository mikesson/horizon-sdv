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

package batchfeature

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/batch"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// tagBatches groups the batch operations in the OpenAPI document.
const tagBatches = "Batches"

// Handler exposes the batch API over HTTP. It parses requests and calls the
// Service; it holds no business logic.
type Handler struct {
	service *Service
}

// NewHandler creates the batch HTTP handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// Register binds the batch routes to the Huma OpenAPI router.
func (h *Handler) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "create-batch",
		Method:      http.MethodPost,
		Path:        "/v2/batches",
		Summary:     "Create a batch of devices",
		Description: "Provisions and creates a set of devices as one unit (the CI/fleet primitive). Set validate_only to plan without committing.",
		Tags:        []string{tagBatches},
		Security:    web.BearerAuth(),
		// 404: requirements.build.registered names a build that does not exist.
		// 409: a keyed retry of a batch whose atomic policy already tripped.
		// 422: an Idempotency-Key reused with a different request.
		// 503: an isolated member create lost its host capacity (real failure;
		//      only pack placement rescues capacity races).
		Errors: []int{
			http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound,
			http.StatusConflict, http.StatusUnprocessableEntity,
			http.StatusTooManyRequests, http.StatusServiceUnavailable,
		},
	}, h.CreateBatch)

	huma.Register(api, huma.Operation{
		OperationID: "list-batches",
		Method:      http.MethodGet,
		Path:        "/v2/batches",
		Summary:     "List batches",
		Description: "Returns the active batches (those with at least one non-deleted member).",
		Tags:        []string{tagBatches},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusBadRequest, http.StatusUnauthorized},
	}, h.ListBatches)

	huma.Register(api, huma.Operation{
		OperationID: "get-batch",
		Method:      http.MethodGet,
		Path:        "/v2/batches/{id}",
		Summary:     "Get a batch",
		Description: "Returns a batch's derived status rolled up over its member devices.",
		Tags:        []string{tagBatches},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound},
	}, h.GetBatch)

	huma.Register(api, huma.Operation{
		OperationID: "list-batch-devices",
		Method:      http.MethodGet,
		Path:        "/v2/batches/{id}/devices",
		Summary:     "List a batch's devices",
		Description: "Returns the batch's member devices with their connection info (the CI payload).",
		Tags:        []string{tagBatches},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound},
	}, h.ListBatchDevices)

	huma.Register(api, huma.Operation{
		OperationID: "delete-batch",
		Method:      http.MethodDelete,
		Path:        "/v2/batches/{id}",
		Summary:     "Delete a batch",
		Description: "Tears down every member device of the batch. Idempotent.",
		Tags:        []string{tagBatches},
		Security:    web.BearerAuth(),
	}, h.DeleteBatch)
}

// CreateBatch admits (or, with validate_only, plans) a batch.
func (h *Handler) CreateBatch(ctx context.Context, input *CreateBatchInput) (*CreateBatchOutput, error) {
	principal, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized("Authentication required")
	}
	spec, req := h.toSpec(input)

	if input.Body.ValidateOnly {
		res, err := h.service.Plan(ctx, spec, req, principal.Email)
		if err != nil {
			return nil, web.MapError(err)
		}
		return &CreateBatchOutput{Body: BatchSummary{
			Requested: spec.Count, Admitted: res.Admitted, NewHosts: res.NewHosts,
			Resolution: res.Resolution, Warnings: res.Warnings,
		}}, nil
	}

	res, err := h.service.Create(ctx, spec, req, principal.Email, input.IdempotencyKey)
	if err != nil {
		return nil, web.MapError(err)
	}
	// Status/counts come from the service's rollup of the returned members, so
	// an idempotent replay of a settled batch reports its real state.
	body := BatchSummary{
		BatchID: res.BatchID, Status: string(res.Status),
		Requested: spec.Count, Admitted: res.Admitted, NewHosts: res.NewHosts,
		Total: res.Rollup.Total, Running: res.Rollup.Running, Errored: res.Rollup.Errored,
		Reconciling: res.Rollup.Reconciling > 0,
		Resolution:  res.Resolution, Warnings: res.Warnings,
	}
	body.applyBatch(res.Batch)
	return &CreateBatchOutput{Body: body}, nil
}

// toSpec maps the request body to the domain spec and requirements, applying
// defaults (min_ready=count, fill_policy=best_effort, placement=isolated).
func (h *Handler) toSpec(input *CreateBatchInput) (batch.Spec, device.Requirements) {
	b := input.Body
	minReady := b.MinReady
	if minReady == 0 {
		minReady = b.Count
	}
	fill := batch.FillPolicy(b.FillPolicy)
	if fill == "" {
		fill = batch.FillBestEffort
	}
	place := batch.Placement(b.Placement)
	if place == "" {
		place = batch.PlacementIsolated
	}
	spec := batch.Spec{
		NamePrefix: b.NamePrefix, Count: b.Count, MinReady: minReady,
		FillPolicy: fill, Placement: place, TTLHours: b.TTLHours,
		ReadyTimeout: time.Duration(b.ReadyTimeout) * time.Second,
	}
	req := device.Requirements{
		Interaction:  device.Interaction(b.Requirements.Interaction),
		GPURequired:  b.Requirements.GPURequired,
		Pool:         b.Requirements.Pool,
		Capacity:     device.CapacityModel(b.Requirements.Capacity),
		Architecture: device.Architecture(b.Requirements.Architecture),
	}
	if rb := b.Requirements.Build; rb != nil {
		req.Build.Registered = rb.Registered
		if rb.Track != nil {
			req.Build.TrackBranch = rb.Track.Branch
			req.Build.TrackTarget = rb.Track.Target
		}
	}
	if loc := b.Requirements.Location; loc != nil {
		req.Location = &device.LocationRequirement{PreferredRegion: loc.PreferredRegion}
	}
	return spec, req
}

// GetBatch returns a batch's rolled-up status.
func (h *Handler) GetBatch(ctx context.Context, input *GetBatchInput) (*GetBatchOutput, error) {
	v, err := h.service.Get(ctx, input.ID)
	if err != nil {
		return nil, web.MapError(err)
	}
	return &GetBatchOutput{Body: summaryOf(v)}, nil
}

// ListBatches returns the active batches.
func (h *Handler) ListBatches(ctx context.Context, input *ListBatchesInput) (*ListBatchesOutput, error) {
	creator, err := auth.CreatorScope(ctx, input.CreatedBy, input.Mine)
	if err != nil {
		return nil, web.MapError(err)
	}
	views, err := h.service.List(ctx, creator)
	if err != nil {
		return nil, web.MapError(err)
	}
	out := &ListBatchesOutput{}
	out.Body.Items = make([]BatchSummary, 0, len(views))
	for _, v := range views {
		out.Body.Items = append(out.Body.Items, summaryOf(v))
	}
	out.Body.Total = len(out.Body.Items)
	return out, nil
}

// ListBatchDevices returns the batch's members with connection info.
func (h *Handler) ListBatchDevices(ctx context.Context, input *GetBatchInput) (*ListBatchDevicesOutput, error) {
	v, err := h.service.Get(ctx, input.ID)
	if err != nil {
		return nil, web.MapError(err)
	}
	out := &ListBatchDevicesOutput{}
	out.Body.Devices = make([]BatchMember, 0, len(v.Members))
	for _, d := range v.Members {
		out.Body.Devices = append(out.Body.Devices, BatchMember{
			DeviceID: d.ID, Name: d.Name, Status: string(d.Status), HostID: d.HostID,
			ADBSerial: adbSerial(d), WebRTCDeviceID: d.CVDID,
		})
	}
	return out, nil
}

// DeleteBatch tears down a batch. The body-less output returns 204.
func (h *Handler) DeleteBatch(ctx context.Context, input *GetBatchInput) (*struct{}, error) {
	if err := h.service.Delete(ctx, input.ID); err != nil {
		return nil, web.MapError(err)
	}
	return nil, nil //nolint:nilnil // Huma body-less output → 204 No Content.
}
