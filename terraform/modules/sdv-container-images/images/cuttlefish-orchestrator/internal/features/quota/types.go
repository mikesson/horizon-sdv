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

import "time"

// QuotaResponse represents quota data returned by the API.
type QuotaResponse struct {
	Scope      string  `json:"scope" doc:"Quota scope (always platform)"`
	ScopeID    string  `json:"scope_id" doc:"Scope identifier (always global)"`
	MaxHosts   int64   `json:"max_hosts" doc:"Maximum hosts allowed"`
	UsedHosts  int64   `json:"used_hosts" doc:"Current host count"`
	Available  int64   `json:"available" doc:"Remaining host capacity"`
	Percentage float64 `json:"percentage" doc:"Usage percentage (0-100)"`

	HighWaterUsedHosts int64      `json:"high_water_used_hosts,omitempty" doc:"Peak host usage within the last 7 days"`
	HighWaterAt        *time.Time `json:"high_water_at,omitempty" doc:"When the peak was recorded"`
}

// GetCurrentQuotaInput is the input for getting the current user's quota.
type GetCurrentQuotaInput struct{}

// GetCurrentQuotaOutput is the output for getting the current user's quota.
type GetCurrentQuotaOutput struct {
	Body QuotaResponse
}

// GetPlatformQuotaInput is the input for getting the platform quota.
type GetPlatformQuotaInput struct{}

// GetPlatformQuotaOutput is the output for getting the platform quota.
type GetPlatformQuotaOutput struct {
	Body QuotaResponse
}

// UpdatePlatformQuotaInput is the input for updating the platform quota.
type UpdatePlatformQuotaInput struct {
	Body struct {
		MaxHosts int64 `json:"max_hosts" required:"true" minimum:"0" doc:"Maximum hosts allowed for the platform"`
	}
}

// UpdatePlatformQuotaOutput is the output for updating the platform quota.
type UpdatePlatformQuotaOutput struct {
	Body QuotaResponse
}
