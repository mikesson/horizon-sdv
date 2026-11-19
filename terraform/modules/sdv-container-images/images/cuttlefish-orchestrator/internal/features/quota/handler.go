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

package quotafeature

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// tagQuota groups the quota operations in the OpenAPI document.
const tagQuota = "Quota"

// QuotaHandler handles quota-related HTTP requests.
type QuotaHandler struct {
	service     quota.Service
	auditLogger audit.Sink
}

// NewQuotaHandler creates a new quota handler.
func NewQuotaHandler(service quota.Service) *QuotaHandler {
	return &QuotaHandler{service: service}
}

// SetAuditLogger enables audit logging of quota changes (nil disables).
func (h *QuotaHandler) SetAuditLogger(l audit.Sink) {
	h.auditLogger = l
}

// Register registers quota operations with the Huma API.
func (h *QuotaHandler) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-current-quota",
		Method:      http.MethodGet,
		Path:        "/v2/quota",
		Summary:     "Get current quota",
		Description: "Returns the global host quota",
		Tags:        []string{tagQuota},
		Security:    web.BearerAuth(),
	}, h.GetCurrentQuota)

	huma.Register(api, huma.Operation{
		OperationID: "get-platform-quota",
		Method:      http.MethodGet,
		Path:        "/v2/quota/platform",
		Summary:     "Get platform quota",
		Description: "Returns the global host quota",
		Tags:        []string{tagQuota},
		Security:    web.BearerAuth(),
	}, h.GetPlatformQuota)

	huma.Register(api, huma.Operation{
		OperationID: "update-platform-quota",
		Method:      http.MethodPut,
		Path:        "/v2/quota/platform",
		Summary:     "Update platform quota",
		Description: "Updates the global host quota limit",
		Tags:        []string{tagQuota},
		Security:    web.BearerAuth(),
	}, h.UpdatePlatformQuota)
}

// globalQuotaResponse reads the one global quota for both GET endpoints
// (/v2/quota and /v2/quota/platform serve the same body).
func (h *QuotaHandler) globalQuotaResponse(ctx context.Context) (QuotaResponse, error) {
	q, err := h.service.Get(ctx, quota.ScopePlatform, quota.GlobalScopeID)
	if err != nil {
		return QuotaResponse{}, web.MapError(err)
	}
	return toQuotaResponse(q), nil
}

// GetCurrentQuota returns the global quota.
func (h *QuotaHandler) GetCurrentQuota(ctx context.Context, _ *GetCurrentQuotaInput) (*GetCurrentQuotaOutput, error) {
	body, err := h.globalQuotaResponse(ctx)
	if err != nil {
		return nil, err
	}
	return &GetCurrentQuotaOutput{Body: body}, nil
}

// GetPlatformQuota returns the global quota.
func (h *QuotaHandler) GetPlatformQuota(ctx context.Context, _ *GetPlatformQuotaInput) (*GetPlatformQuotaOutput, error) {
	body, err := h.globalQuotaResponse(ctx)
	if err != nil {
		return nil, err
	}
	return &GetPlatformQuotaOutput{Body: body}, nil
}

// UpdatePlatformQuota updates the global quota limit.
//
// The response is built from the pre-update snapshot combined with the newly
// persisted limit rather than issuing a post-write Get; otherwise a transient
// read failure after SetLimit commits could fail the HTTP request and skip the
// audit entry for an already-persisted limit change.
func (h *QuotaHandler) UpdatePlatformQuota(ctx context.Context, input *UpdatePlatformQuotaInput) (*UpdatePlatformQuotaOutput, error) {
	if input.Body.MaxHosts < 0 {
		return nil, huma.Error400BadRequest("max_hosts must be non-negative")
	}

	previous, err := h.service.Get(ctx, quota.ScopePlatform, quota.GlobalScopeID)
	if err != nil {
		return nil, web.MapError(err)
	}
	if previous == nil {
		return nil, web.MapError(&quota.NotFoundError{Scope: quota.ScopePlatform, ScopeID: quota.GlobalScopeID})
	}

	maxBefore := previous.MaxHosts
	if err := h.service.SetLimit(ctx, quota.ScopePlatform, quota.GlobalScopeID, input.Body.MaxHosts); err != nil {
		return nil, web.MapError(err)
	}

	audit.LogChange(ctx, h.auditLogger, audit.ActionQuotaUpdate, audit.ResourceQuota, quota.GlobalScopeID, map[string]any{
		"max_hosts_before": maxBefore,
		"max_hosts_after":  input.Body.MaxHosts,
	})

	updated := *previous
	updated.MaxHosts = input.Body.MaxHosts
	return &UpdatePlatformQuotaOutput{Body: toQuotaResponse(&updated)}, nil
}

// toQuotaResponse converts a domain Quota to a QuotaResponse.
func toQuotaResponse(q *quota.Quota) QuotaResponse {
	return QuotaResponse{
		Scope:              string(q.Scope),
		ScopeID:            q.ScopeID,
		MaxHosts:           q.MaxHosts,
		UsedHosts:          q.UsedHosts,
		Available:          q.Available(),
		Percentage:         q.Percentage(),
		HighWaterUsedHosts: q.HighWaterUsedHosts,
		HighWaterAt:        q.HighWaterAt,
	}
}
