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
	"net/http"
	"runtime"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// tagHealth groups the unauthenticated liveness endpoints in the OpenAPI
// document.
const tagHealth = "Health"

// HealthHandler handles health check requests.
type HealthHandler struct {
	startTime time.Time
	version   string
}

// NewHealthHandler creates a new health handler.
func NewHealthHandler(version string) *HealthHandler {
	return &HealthHandler{
		startTime: time.Now(),
		version:   version,
	}
}

// Register registers health check operations with the Huma API.
func (h *HealthHandler) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-health",
		Method:      http.MethodGet,
		Path:        "/health",
		Summary:     "Health check",
		Description: "Returns the health status of the service",
		Tags:        []string{tagHealth},
	}, h.GetHealth)

	huma.Register(api, huma.Operation{
		OperationID: "get-ready",
		Method:      http.MethodGet,
		Path:        "/ready",
		Summary:     "Readiness check",
		Description: "Returns the readiness status of the service",
		Tags:        []string{tagHealth},
	}, h.GetReady)

	huma.Register(api, huma.Operation{
		OperationID: "get-info",
		Method:      http.MethodGet,
		Path:        "/info",
		Summary:     "Service information",
		Description: "Returns runtime information about the service",
		Tags:        []string{tagHealth},
	}, h.GetInfo)
}

// GetHealth returns the health status (Huma handler).
func (h *HealthHandler) GetHealth(ctx context.Context, input *HealthInput) (*HealthOutput, error) {
	return &HealthOutput{
		Body: HealthResponse{
			Status:    "ok",
			Timestamp: time.Now().UTC(),
		},
	}, nil
}

// GetReady returns the readiness status (Huma handler).
func (h *HealthHandler) GetReady(ctx context.Context, input *HealthInput) (*HealthOutput, error) {
	// Add actual readiness checks here (database, external services, etc.)
	return &HealthOutput{
		Body: HealthResponse{
			Status:    "ok",
			Timestamp: time.Now().UTC(),
		},
	}, nil
}

// GetInfo returns service information (Huma handler).
func (h *HealthHandler) GetInfo(ctx context.Context, input *InfoInput) (*InfoOutput, error) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	return &InfoOutput{
		Body: InfoResponse{
			Version:      h.version,
			GoVersion:    runtime.Version(),
			Uptime:       time.Since(h.startTime).String(),
			NumGoroutine: runtime.NumGoroutine(),
			MemAlloc:     mem.Alloc,
			MemSys:       mem.Sys,
		},
	}, nil
}

// Response types

// HealthResponse is the response body for health check endpoints.
type HealthResponse struct {
	Status    string    `json:"status" doc:"Health status of the service"`
	Timestamp time.Time `json:"timestamp" doc:"Current server timestamp"`
}

// InfoResponse is the response body for the info endpoint.
type InfoResponse struct {
	Version      string `json:"version" doc:"Application version"`
	GoVersion    string `json:"go_version" doc:"Go runtime version"`
	Uptime       string `json:"uptime" doc:"Service uptime"`
	NumGoroutine int    `json:"num_goroutine" doc:"Number of active goroutines"`
	MemAlloc     uint64 `json:"mem_alloc_bytes" doc:"Allocated memory in bytes"`
	MemSys       uint64 `json:"mem_sys_bytes" doc:"System memory in bytes"`
}
