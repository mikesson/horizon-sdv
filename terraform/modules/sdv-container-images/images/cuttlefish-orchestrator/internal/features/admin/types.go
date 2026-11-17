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

import "time"

// HealthInput is the input for health check endpoints.
type HealthInput struct{}

// HealthOutput is the output for health check endpoints.
type HealthOutput struct {
	Body HealthResponse
}

// InfoInput is the input for the info endpoint.
type InfoInput struct{}

// InfoOutput is the output for the info endpoint.
type InfoOutput struct {
	Body InfoResponse
}

// StatsInput is the input for system statistics.
type StatsInput struct{}

// StatsOutput is the output for system statistics.
type StatsOutput struct {
	Body struct {
		Hosts           HostStats        `json:"hosts"`
		Devices         DeviceStats      `json:"devices"`
		DevicesByStatus map[string]int64 `json:"devices_by_status"`
		HostsByStatus   map[string]int64 `json:"hosts_by_status"`
		Timestamp       time.Time        `json:"timestamp"`
	}
}

// HostStats contains host statistics with quota information.
type HostStats struct {
	Total    int64 `json:"total"`
	Active   int64 `json:"active"`
	MaxQuota int64 `json:"max_quota"`
}

// DeviceStats contains device statistics.
type DeviceStats struct {
	Total       int64 `json:"total"`
	Active      int64 `json:"active"`
	MaxCapacity int64 `json:"max_capacity,omitempty"`
}
