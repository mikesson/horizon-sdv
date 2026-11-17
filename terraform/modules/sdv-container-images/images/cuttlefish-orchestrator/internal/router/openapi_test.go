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

package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
)

// OpenAPISpec represents the structure of an OpenAPI 3.1 specification.
// This is a minimal representation for validation purposes.
type OpenAPISpec struct {
	OpenAPI    string              `json:"openapi"`
	Info       OpenAPIInfo         `json:"info"`
	Servers    []OpenAPIServer     `json:"servers,omitempty"`
	Paths      map[string]PathItem `json:"paths"`
	Components OpenAPIComponents   `json:"components"`
	Tags       []OpenAPITag        `json:"tags,omitempty"`
}

// OpenAPIInfo contains API metadata.
type OpenAPIInfo struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

// OpenAPIServer describes a server.
type OpenAPIServer struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

// PathItem contains operations for a path.
type PathItem struct {
	Get    *Operation `json:"get,omitempty"`
	Post   *Operation `json:"post,omitempty"`
	Put    *Operation `json:"put,omitempty"`
	Delete *Operation `json:"delete,omitempty"`
	Patch  *Operation `json:"patch,omitempty"`
}

// Operation represents an API operation.
type Operation struct {
	OperationID string                `json:"operationId"`
	Summary     string                `json:"summary,omitempty"`
	Description string                `json:"description,omitempty"`
	Tags        []string              `json:"tags,omitempty"`
	Security    []map[string][]string `json:"security,omitempty"`
	Parameters  []Parameter           `json:"parameters,omitempty"`
	RequestBody *RequestBody          `json:"requestBody,omitempty"`
	Responses   map[string]Response   `json:"responses,omitempty"`
}

// Parameter represents an operation parameter.
type Parameter struct {
	Name        string `json:"name"`
	In          string `json:"in"`
	Required    bool   `json:"required,omitempty"`
	Schema      any    `json:"schema,omitempty"`
	Description string `json:"description,omitempty"`
}

// RequestBody represents a request body.
type RequestBody struct {
	Required    bool                 `json:"required,omitempty"`
	Content     map[string]MediaType `json:"content,omitempty"`
	Description string               `json:"description,omitempty"`
}

// Response represents an operation response.
type Response struct {
	Description string               `json:"description"`
	Content     map[string]MediaType `json:"content,omitempty"`
}

// MediaType represents a media type.
type MediaType struct {
	Schema any `json:"schema,omitempty"`
}

// OpenAPIComponents contains reusable components.
type OpenAPIComponents struct {
	Schemas         map[string]any            `json:"schemas,omitempty"`
	SecuritySchemes map[string]SecurityScheme `json:"securitySchemes,omitempty"`
}

// SecurityScheme represents a security scheme.
type SecurityScheme struct {
	Type         string `json:"type"`
	Scheme       string `json:"scheme,omitempty"`
	In           string `json:"in,omitempty"`
	Name         string `json:"name,omitempty"`
	BearerFormat string `json:"bearerFormat,omitempty"`
}

// OpenAPITag represents an API tag.
type OpenAPITag struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// mockHandler is a simple handler for testing that registers operations.
type mockHandler struct {
	operations []huma.Operation
}

// mockInputWithID is an input type with a path parameter.
type mockInputWithID struct {
	ID string `path:"id" doc:"Resource ID"`
}

// mockOutput is a simple output type.
type mockOutput struct {
	Body string `json:"body"`
}

func (h *mockHandler) Register(api huma.API) {
	for _, op := range h.operations {
		// Use appropriate input type based on whether path has {id}
		hasPathParam := strings.Contains(op.Path, "{id}")

		switch op.Method {
		case http.MethodGet:
			if hasPathParam {
				huma.Register(api, op, func(ctx context.Context, input *mockInputWithID) (*mockOutput, error) {
					return &mockOutput{Body: "ok"}, nil
				})
			} else {
				huma.Register(api, op, func(ctx context.Context, input *struct{}) (*mockOutput, error) {
					return &mockOutput{Body: "ok"}, nil
				})
			}
		case http.MethodPost:
			huma.Register(api, op, func(ctx context.Context, input *struct{ Body struct{} }) (*mockOutput, error) {
				return &mockOutput{Body: "ok"}, nil
			})
		case http.MethodDelete:
			if hasPathParam {
				huma.Register(api, op, func(ctx context.Context, input *mockInputWithID) (*struct{}, error) {
					return nil, nil
				})
			} else {
				huma.Register(api, op, func(ctx context.Context, input *struct{}) (*struct{}, error) {
					return nil, nil
				})
			}
		}
	}
}

// createTestAPI creates a test Huma API with mock handlers.
func createTestAPI(handlers ...HumaHandler) (chi.Router, huma.API) {
	r := chi.NewRouter()
	cfg := DefaultHumaConfig()
	api := NewHumaAPI(r, cfg)

	for _, h := range handlers {
		h.Register(api)
	}

	return r, api
}

func TestOpenAPISpec_ValidStructure(t *testing.T) {
	t.Parallel()
	// Create mock operations representing expected API endpoints
	handler := &mockHandler{
		operations: []huma.Operation{
			{OperationID: "list-devices", Method: http.MethodGet, Path: "/v2/devices", Tags: []string{"Devices"}},
			{OperationID: "create-device", Method: http.MethodPost, Path: "/v2/devices", Tags: []string{"Devices"}},
			{OperationID: "get-device", Method: http.MethodGet, Path: "/v2/devices/{id}", Tags: []string{"Devices"}},
			{OperationID: "list-hosts", Method: http.MethodGet, Path: "/v2/hosts", Tags: []string{"Hosts"}},
			{OperationID: "get-host", Method: http.MethodGet, Path: "/v2/hosts/{id}", Tags: []string{"Hosts"}},
		},
	}

	r, _ := createTestAPI(handler)

	// Request the OpenAPI spec
	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	// Parse the spec
	var spec OpenAPISpec
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("failed to parse OpenAPI spec: %v", err)
	}

	// Validate OpenAPI version
	if !strings.HasPrefix(spec.OpenAPI, "3.1") {
		t.Errorf("expected OpenAPI 3.1.x, got %s", spec.OpenAPI)
	}

	// Validate info
	if spec.Info.Title == "" {
		t.Error("expected non-empty title")
	}
	if spec.Info.Version == "" {
		t.Error("expected non-empty version")
	}
}

func TestOpenAPISpec_ContainsExpectedPaths(t *testing.T) {
	t.Parallel()
	handler := &mockHandler{
		operations: []huma.Operation{
			{OperationID: "list-devices", Method: http.MethodGet, Path: "/v2/devices", Tags: []string{"Devices"}},
			{OperationID: "create-device", Method: http.MethodPost, Path: "/v2/devices", Tags: []string{"Devices"}},
			{OperationID: "get-device", Method: http.MethodGet, Path: "/v2/devices/{id}", Tags: []string{"Devices"}},
			{OperationID: "delete-device", Method: http.MethodDelete, Path: "/v2/devices/{id}", Tags: []string{"Devices"}},
			{OperationID: "list-hosts", Method: http.MethodGet, Path: "/v2/hosts", Tags: []string{"Hosts"}},
			{OperationID: "create-host", Method: http.MethodPost, Path: "/v2/hosts", Tags: []string{"Hosts"}},
			{OperationID: "get-host", Method: http.MethodGet, Path: "/v2/hosts/{id}", Tags: []string{"Hosts"}},
			{OperationID: "delete-host", Method: http.MethodDelete, Path: "/v2/hosts/{id}", Tags: []string{"Hosts"}},
		},
	}

	r, _ := createTestAPI(handler)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var spec OpenAPISpec
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("failed to parse OpenAPI spec: %v", err)
	}

	expectedPaths := []string{
		"/v2/devices",
		"/v2/devices/{id}",
		"/v2/hosts",
		"/v2/hosts/{id}",
	}

	for _, path := range expectedPaths {
		if _, ok := spec.Paths[path]; !ok {
			t.Errorf("expected path %s not found in OpenAPI spec", path)
		}
	}
}

func TestOpenAPISpec_OperationsHaveOperationIDs(t *testing.T) {
	t.Parallel()
	handler := &mockHandler{
		operations: []huma.Operation{
			{OperationID: "list-devices", Method: http.MethodGet, Path: "/v2/devices", Tags: []string{"Devices"}},
			{OperationID: "create-device", Method: http.MethodPost, Path: "/v2/devices", Tags: []string{"Devices"}},
		},
	}

	r, _ := createTestAPI(handler)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var spec OpenAPISpec
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("failed to parse OpenAPI spec: %v", err)
	}

	// Check each path has operations with IDs
	for path, item := range spec.Paths {
		if item.Get != nil && item.Get.OperationID == "" {
			t.Errorf("GET %s missing operationId", path)
		}
		if item.Post != nil && item.Post.OperationID == "" {
			t.Errorf("POST %s missing operationId", path)
		}
		if item.Put != nil && item.Put.OperationID == "" {
			t.Errorf("PUT %s missing operationId", path)
		}
		if item.Delete != nil && item.Delete.OperationID == "" {
			t.Errorf("DELETE %s missing operationId", path)
		}
	}
}

func TestOpenAPISpec_OperationsHaveTags(t *testing.T) {
	t.Parallel()
	handler := &mockHandler{
		operations: []huma.Operation{
			{OperationID: "list-devices", Method: http.MethodGet, Path: "/v2/devices", Tags: []string{"Devices"}},
			{OperationID: "list-hosts", Method: http.MethodGet, Path: "/v2/hosts", Tags: []string{"Hosts"}},
		},
	}

	r, _ := createTestAPI(handler)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var spec OpenAPISpec
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("failed to parse OpenAPI spec: %v", err)
	}

	// Check operations have tags
	for path, item := range spec.Paths {
		ops := []*Operation{item.Get, item.Post, item.Put, item.Delete, item.Patch}
		for _, op := range ops {
			if op != nil && len(op.Tags) == 0 {
				t.Errorf("operation at %s missing tags", path)
			}
		}
	}
}

func TestOpenAPISpec_SecuritySchemesExist(t *testing.T) {
	t.Parallel()
	handler := &mockHandler{
		operations: []huma.Operation{
			{OperationID: "list-devices", Method: http.MethodGet, Path: "/v2/devices", Tags: []string{"Devices"}},
		},
	}

	r, _ := createTestAPI(handler)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var spec OpenAPISpec
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("failed to parse OpenAPI spec: %v", err)
	}

	// Check security schemes exist
	if spec.Components.SecuritySchemes == nil {
		t.Fatal("expected security schemes to be defined")
	}

	expectedSchemes := []string{"bearerAuth"}
	for _, scheme := range expectedSchemes {
		if _, ok := spec.Components.SecuritySchemes[scheme]; !ok {
			t.Errorf("expected security scheme %s not found", scheme)
		}
	}

	// The session cookie is managed entirely by IAP at the edge; it must
	// not appear in the API contract.
	if _, ok := spec.Components.SecuritySchemes["cookieAuth"]; ok {
		t.Error("cookieAuth scheme must not be defined (IAP owns the web session)")
	}
}

func TestOpenAPISpec_BearerAuthScheme(t *testing.T) {
	t.Parallel()
	r, _ := createTestAPI()

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var spec OpenAPISpec
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("failed to parse OpenAPI spec: %v", err)
	}

	bearer, ok := spec.Components.SecuritySchemes["bearerAuth"]
	if !ok {
		t.Fatal("bearerAuth scheme not found")
	}

	if bearer.Type != "http" {
		t.Errorf("expected type 'http', got %s", bearer.Type)
	}
	if bearer.Scheme != "bearer" {
		t.Errorf("expected scheme 'bearer', got %s", bearer.Scheme)
	}
	if bearer.BearerFormat != "JWT" {
		t.Errorf("expected bearerFormat 'JWT', got %s", bearer.BearerFormat)
	}
}

func TestOpenAPISpec_InfoFields(t *testing.T) {
	t.Parallel()
	r, _ := createTestAPI()

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var spec OpenAPISpec
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("failed to parse OpenAPI spec: %v", err)
	}

	if spec.Info.Title != "Cloud Cuttlefish Orchestrator API" {
		t.Errorf("unexpected title: %s", spec.Info.Title)
	}
	if spec.Info.Version != "1.0.0" {
		t.Errorf("unexpected version: %s", spec.Info.Version)
	}
	if spec.Info.Description == "" {
		t.Error("expected non-empty description")
	}
}

func TestOpenAPISpec_ServersConfigured(t *testing.T) {
	t.Parallel()
	r, _ := createTestAPI()

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var spec OpenAPISpec
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("failed to parse OpenAPI spec: %v", err)
	}

	if len(spec.Servers) == 0 {
		t.Error("expected at least one server")
	}
}

func TestOpenAPISpec_UniqueOperationIDs(t *testing.T) {
	t.Parallel()
	handler := &mockHandler{
		operations: []huma.Operation{
			{OperationID: "list-devices", Method: http.MethodGet, Path: "/v2/devices", Tags: []string{"Devices"}},
			{OperationID: "create-device", Method: http.MethodPost, Path: "/v2/devices", Tags: []string{"Devices"}},
			{OperationID: "get-device", Method: http.MethodGet, Path: "/v2/devices/{id}", Tags: []string{"Devices"}},
			{OperationID: "delete-device", Method: http.MethodDelete, Path: "/v2/devices/{id}", Tags: []string{"Devices"}},
			{OperationID: "list-hosts", Method: http.MethodGet, Path: "/v2/hosts", Tags: []string{"Hosts"}},
			{OperationID: "get-host", Method: http.MethodGet, Path: "/v2/hosts/{id}", Tags: []string{"Hosts"}},
		},
	}

	r, _ := createTestAPI(handler)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var spec OpenAPISpec
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("failed to parse OpenAPI spec: %v", err)
	}

	// Collect all operation IDs
	opIDs := make(map[string]bool)
	for path, item := range spec.Paths {
		ops := []*Operation{item.Get, item.Post, item.Put, item.Delete, item.Patch}
		for _, op := range ops {
			if op != nil {
				if opIDs[op.OperationID] {
					t.Errorf("duplicate operationId: %s at path %s", op.OperationID, path)
				}
				opIDs[op.OperationID] = true
			}
		}
	}
}

func TestOpenAPISpec_PathParametersExist(t *testing.T) {
	t.Parallel()
	handler := &mockHandler{
		operations: []huma.Operation{
			{OperationID: "get-device", Method: http.MethodGet, Path: "/v2/devices/{id}", Tags: []string{"Devices"}},
		},
	}

	r, _ := createTestAPI(handler)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var spec OpenAPISpec
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("failed to parse OpenAPI spec: %v", err)
	}

	// Check that paths with {id} have id parameter defined
	for path, item := range spec.Paths {
		if strings.Contains(path, "{id}") {
			ops := []*Operation{item.Get, item.Post, item.Put, item.Delete, item.Patch}
			for _, op := range ops {
				if op == nil {
					continue
				}
				hasIDParam := false
				for _, param := range op.Parameters {
					if param.Name == "id" && param.In == "path" {
						hasIDParam = true
						break
					}
				}
				if !hasIDParam {
					t.Errorf("path %s operation %s missing 'id' path parameter", path, op.OperationID)
				}
			}
		}
	}
}
