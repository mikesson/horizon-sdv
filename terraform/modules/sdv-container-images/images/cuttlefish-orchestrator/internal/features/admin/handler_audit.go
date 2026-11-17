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

package adminfeature

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
)

// AuditReader exposes the audit-log read side; the Spanner AuditRepository
// satisfies it. Reads bypass the async write logger on purpose.
type AuditReader interface {
	Query(ctx context.Context, filter audit.ListFilter) ([]audit.Entry, error)
	Count(ctx context.Context, filter audit.ListFilter) (int64, error)
}

// AuditEntryItem is one audit-log entry on the wire.
type AuditEntryItem struct {
	ID           string         `json:"id"`
	Timestamp    time.Time      `json:"timestamp"`
	ActorEmail   string         `json:"actor_email,omitempty"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id,omitempty"`
	Changes      map[string]any `json:"changes,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// ListAuditInput filters the audit log; times are RFC 3339.
type ListAuditInput struct {
	ActorEmail   string `query:"actor_email" doc:"Only entries by this actor"`
	Action       string `query:"action" doc:"Only entries with this action (e.g. device.create)"`
	ResourceType string `query:"resource_type" enum:"host,device,quota,system,task," doc:"Only entries for this resource type"`
	ResourceID   string `query:"resource_id" doc:"Only entries for this resource"`
	Kind         string `query:"kind" enum:"system_config,host_image_config,machine_type,zone," doc:"Only entries stamped with this metadata kind (system_config is the settings changes)"`
	StartTime    string `query:"start_time" format:"date-time" doc:"Only entries at or after this time (RFC 3339)"`
	EndTime      string `query:"end_time" format:"date-time" doc:"Only entries at or before this time (RFC 3339)"`
	Limit        int    `query:"limit" minimum:"1" maximum:"200" default:"50" doc:"Maximum entries to return"`
	Offset       int    `query:"offset" minimum:"0" default:"0" doc:"Number of entries to skip"`
}

// ListAuditOutput is the paged audit-log response, newest first.
type ListAuditOutput struct {
	Body struct {
		Items  []AuditEntryItem `json:"items"`
		Total  int64            `json:"total"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}
}

// auditFilterFromInput builds the domain filter; it errors on unparseable times.
func auditFilterFromInput(input *ListAuditInput) (audit.ListFilter, error) {
	filter := audit.ListFilter{Limit: input.Limit, Offset: input.Offset}

	if input.ActorEmail != "" {
		filter.ActorEmail = &input.ActorEmail
	}
	if input.Action != "" {
		action := audit.Action(input.Action)
		filter.Action = &action
	}
	if input.ResourceType != "" {
		rt := audit.ResourceType(input.ResourceType)
		filter.ResourceType = &rt
	}
	if input.ResourceID != "" {
		filter.ResourceID = &input.ResourceID
	}
	if input.Kind != "" {
		filter.Kind = &input.Kind
	}
	if input.StartTime != "" {
		t, err := time.Parse(time.RFC3339, input.StartTime)
		if err != nil {
			return filter, huma.Error400BadRequest("invalid start_time", err)
		}
		filter.StartTime = &t
	}
	if input.EndTime != "" {
		t, err := time.Parse(time.RFC3339, input.EndTime)
		if err != nil {
			return filter, huma.Error400BadRequest("invalid end_time", err)
		}
		filter.EndTime = &t
	}

	return filter, nil
}

// ListAudit returns audit-log entries matching the filter, newest first.
func (h *AdminHandler) ListAudit(ctx context.Context, input *ListAuditInput) (*ListAuditOutput, error) {
	if h.auditReader == nil {
		return nil, huma.Error500InternalServerError("audit log is not configured")
	}

	filter, err := auditFilterFromInput(input)
	if err != nil {
		return nil, err
	}

	entries, err := h.auditReader.Query(ctx, filter)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to query audit log", err)
	}
	total, err := h.auditReader.Count(ctx, filter)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to count audit log", err)
	}

	out := &ListAuditOutput{}
	out.Body.Items = make([]AuditEntryItem, 0, len(entries))
	for _, e := range entries {
		item := AuditEntryItem{
			ID:           e.ID,
			Timestamp:    e.Timestamp,
			ActorEmail:   e.ActorEmail,
			Action:       string(e.Action),
			ResourceType: string(e.ResourceType),
			Changes:      e.Changes,
			Metadata:     e.Metadata,
		}
		if e.ResourceID != nil {
			item.ResourceID = *e.ResourceID
		}
		out.Body.Items = append(out.Body.Items, item)
	}
	out.Body.Total = total
	out.Body.Limit = input.Limit
	out.Body.Offset = input.Offset
	return out, nil
}
