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

package buildfeature

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// tagBuilds groups the build operations in the OpenAPI document.
const tagBuilds = "Builds"

// BuildHandler handles build-related requests
type BuildHandler struct {
	service *BuildService
}

// NewBuildHandler creates a new build handler
func NewBuildHandler(service *BuildService) *BuildHandler {
	return &BuildHandler{service: service}
}

// Register registers build operations with the Huma API.
func (h *BuildHandler) Register(api huma.API) {
	const pathBuildByID = "/v2/builds/{id}"

	huma.Register(api, huma.Operation{
		OperationID: "list-builds",
		Method:      http.MethodGet,
		Path:        "/v2/builds",
		Summary:     "List builds",
		Description: "Returns a list of available Android builds",
		Tags:        []string{tagBuilds},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusBadRequest},
	}, h.ListBuilds)

	huma.Register(api, huma.Operation{
		OperationID: "create-build",
		Method:      http.MethodPost,
		Path:        "/v2/builds",
		Summary:     "Register a build",
		Description: "Registers a new Android build; a request carrying an already-registered build_id returns the existing record",
		Tags:        []string{tagBuilds},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusUnauthorized},
	}, h.CreateBuild)

	huma.Register(api, huma.Operation{
		OperationID:   "start-build-upload",
		Method:        http.MethodPost,
		Path:          "/v2/builds/uploads",
		Summary:       "Start a staged-artifact upload",
		Description:   "Mints a GCS resumable-upload session for a build artifact, content-addressed by its SHA-256; an already-staged artifact returns without a session",
		Tags:          []string{tagBuilds},
		Security:      web.BearerAuth(),
		DefaultStatus: http.StatusCreated,
		Errors:        []int{http.StatusUnauthorized, http.StatusServiceUnavailable},
	}, h.StartBuildUpload)

	huma.Register(api, huma.Operation{
		OperationID: "get-build",
		Method:      http.MethodGet,
		Path:        pathBuildByID,
		Summary:     "Get a build",
		Description: "Returns details of a specific build",
		Tags:        []string{tagBuilds},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound},
	}, h.GetBuild)

	huma.Register(api, huma.Operation{
		OperationID: "update-build",
		Method:      http.MethodPatch,
		Path:        pathBuildByID,
		Summary:     "Update a build",
		Description: "Updates mutable fields of a build (name, description, default flag, artifact specs)",
		Tags:        []string{tagBuilds},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound},
	}, h.UpdateBuild)

	huma.Register(api, huma.Operation{
		OperationID: "delete-build",
		Method:      http.MethodDelete,
		Path:        pathBuildByID,
		Summary:     "Delete a build",
		Description: "Removes a build from the registry",
		Tags:        []string{tagBuilds},
		Security:    web.BearerAuth(),
		Errors:      []int{http.StatusNotFound},
	}, h.DeleteBuild)
}

// ListBuilds returns a list of builds (Huma handler).
func (h *BuildHandler) ListBuilds(ctx context.Context, input *ListBuildsInput) (*ListBuildsOutput, error) {
	cursor, err := web.DecodePageToken(input.PageToken, input.Offset)
	if err != nil {
		return nil, web.MapError(err)
	}
	// One row past the page says whether another page exists.
	filter := build.ListFilter{
		Limit:  input.Limit + 1,
		Offset: input.Offset,
	}
	if cursor != nil {
		filter.AfterCreatedAt, filter.AfterID = cursor.CreatedAt, cursor.ID
	}

	if input.Target != "" {
		filter.Target = &input.Target
	}
	if input.Branch != "" {
		filter.Branch = &input.Branch
	}
	if input.Source != "" {
		s := build.Source(input.Source)
		filter.Source = &s
	}
	builds, err := h.service.List(ctx, filter)
	if err != nil {
		return nil, web.MapError(err)
	}

	total, err := h.service.Count(ctx, filter)
	if err != nil {
		return nil, web.MapError(err)
	}

	output := &ListBuildsOutput{}
	if len(builds) > input.Limit {
		builds = builds[:input.Limit]
		last := builds[len(builds)-1]
		output.Body.NextPageToken = web.EncodePageToken(web.PageCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	output.Body.Items = builds
	output.Body.Limit = input.Limit
	output.Body.Offset = input.Offset
	output.Body.Total = int(total)
	return output, nil
}

// GetBuild returns a build by ID (Huma handler).
func (h *BuildHandler) GetBuild(ctx context.Context, input *GetBuildInput) (*GetBuildOutput, error) {
	b, err := h.service.Get(ctx, input.ID)
	if err != nil {
		return nil, web.MapError(err)
	}

	return &GetBuildOutput{Body: b}, nil
}

// CreateBuild registers a new build (Huma handler).
func (h *BuildHandler) CreateBuild(ctx context.Context, input *CreateBuildInput) (*CreateBuildOutput, error) {
	principal, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized("Authentication required")
	}

	// Convert artifact specs from input to domain types
	var artifactSpecs []build.ArtifactSpec
	if len(input.Body.ArtifactSpecs) > 0 {
		artifactSpecs = make([]build.ArtifactSpec, len(input.Body.ArtifactSpecs))
		for i, spec := range input.Body.ArtifactSpecs {
			artifactSpecs[i] = build.ArtifactSpec{
				Type: build.ArtifactSpecType(spec.Type),
				URL:  spec.URL,
				Role: build.ArtifactRole(spec.Role),
			}
		}
	}

	createReq := build.CreateRequest{
		Name:          input.Body.Name,
		BuildID:       input.Body.BuildID,
		Target:        input.Body.Target,
		Branch:        input.Body.Branch,
		Flavor:        input.Body.Flavor,
		Source:        build.Source(input.Body.Source),
		ArtifactSpecs: artifactSpecs,
		Fingerprint:   input.Body.Fingerprint,
		CreatedBy:     principal.Email,
		Description:   input.Body.Description,
		IsDefault:     input.Body.IsDefault,
		Metadata:      input.Body.Metadata,
	}

	b, err := h.service.Register(ctx, createReq)
	if err != nil {
		return nil, web.MapError(err)
	}

	return &CreateBuildOutput{Body: b}, nil
}

// StartBuildUpload mints a staged-artifact upload session (Huma handler).
func (h *BuildHandler) StartBuildUpload(ctx context.Context, input *StartBuildUploadInput) (*StartBuildUploadOutput, error) {
	principal, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized("Authentication required")
	}

	staged, err := h.service.StartUpload(ctx,
		input.Body.FileName, input.Body.SizeBytes, input.Body.SHA256,
		input.Body.ContentType, principal.Email)
	if err != nil {
		return nil, web.MapError(err)
	}

	output := &StartBuildUploadOutput{}
	output.Body.ObjectURL = staged.ObjectURL
	output.Body.SessionURL = staged.SessionURL
	output.Body.AlreadyExists = staged.AlreadyExists
	return output, nil
}

// UpdateBuild updates a build's mutable fields (Huma handler).
func (h *BuildHandler) UpdateBuild(ctx context.Context, input *UpdateBuildInput) (*UpdateBuildOutput, error) {
	req := build.UpdateRequest{
		Name:        input.Body.Name,
		Description: input.Body.Description,
		IsDefault:   input.Body.IsDefault,
	}
	if input.Body.ArtifactSpecs != nil {
		specs := make([]build.ArtifactSpec, len(input.Body.ArtifactSpecs))
		for i, spec := range input.Body.ArtifactSpecs {
			specs[i] = build.ArtifactSpec{
				Type: build.ArtifactSpecType(spec.Type),
				URL:  spec.URL,
				Role: build.ArtifactRole(spec.Role),
			}
		}
		req.ArtifactSpecs = specs
	}

	b, err := h.service.Update(ctx, input.ID, req)
	if err != nil {
		return nil, web.MapError(err)
	}

	return &UpdateBuildOutput{Body: b}, nil
}

// DeleteBuild removes a build (Huma handler).
func (h *BuildHandler) DeleteBuild(ctx context.Context, input *DeleteBuildInput) (*DeleteBuildOutput, error) {
	if err := h.service.Delete(ctx, input.ID); err != nil {
		return nil, web.MapError(err)
	}
	return &DeleteBuildOutput{}, nil
}
