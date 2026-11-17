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
	"os"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/web"
)

// HumaConfig contains settings for the Huma API.
type HumaConfig struct {
	Title       string
	Version     string
	Description string
}

// DefaultHumaConfig returns default Huma configuration.
func DefaultHumaConfig() HumaConfig {
	return HumaConfig{
		Title:       "Cloud Cuttlefish Orchestrator API",
		Version:     "1.0.0",
		Description: apiDescription,
	}
}

// apiDescription is the markdown landing page of the interactive docs.
const apiDescription = `Cloud Cuttlefish Orchestrator (CCO) manages virtual
Android devices ([Cuttlefish](https://source.android.com/docs/devices/cuttlefish))
on Google Cloud: GCE host VMs, the devices that run on them, and the Android
builds they boot.

## Authentication

Requests are authenticated at the edge or proxy according to the deployment's
configured ` + "`CCO_AUTH_MODE`" + ` (` + "`proxy`" + `, ` + "`iap`" + `, or ` + "`network`" + `).

- **Browser**: sign in through the configured edge proxy (` + "`oauth2-proxy`" + ` / Keycloak in ` + "`proxy`" + ` mode, or Cloud Run IAP in ` + "`iap`" + ` mode); the browser session covers this documentation's *Test Request* feature.
- **API / CLI**: in ` + "`iap`" + ` mode, authenticate using a service-account JWT sent as ` + "`Authorization: Bearer <jwt>`" + ` (` + "`cfctl`" + ` handles this automatically). In ` + "`proxy`" + ` mode behind a trusted reverse proxy or ` + "`network`" + ` mode, follow the deployment's ` + "`cfctl`" + ` configuration (` + "`--auth-none`" + ` / proxy identity settings).

## Conventions

- Errors are [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) problem
  details; each operation lists the business error codes it can return.
- Response bodies carry a ` + "`$schema`" + ` link to their JSON Schema under
  ` + "`/schemas`" + `.
- List endpoints paginate with ` + "`limit`" + `/` + "`offset`" + ` and return
  ` + "`items`" + ` + ` + "`total`" + `.

The raw spec is at [openapi.json](openapi.json) or
[openapi.yaml](openapi.yaml).`

// apiTags orders and describes the documentation's sidebar groups; operations
// reference these by name in their Tags field.
func apiTags() []*huma.Tag {
	return []*huma.Tag{
		{Name: "Devices", Description: "Cuttlefish virtual devices: lifecycle (create, start/stop, TTL, pinning), displays, and WebRTC TURN credentials."},
		{Name: "Hosts", Description: "GCE host VMs that run Cuttlefish devices: provisioning, lifecycle, retry, and sync with GCE."},
		{Name: "Builds", Description: "Android build registry: CI builds and GCS/HTTPS artifact sources that devices boot from."},
		{Name: "Storage", Description: "GCS bucket browsing and access verification for build artifacts."},
		{Name: "Machine Types", Description: "Curated GCE machine-type catalog backing host creation."},
		{Name: "GCE Catalog", Description: "Zone availability for machine types and GPUs."},
		{Name: "Quota", Description: "The single global host quota."},
		{Name: "Host Image Config", Description: "Per-machine-family host image configuration (admin)."},
		{Name: "Admin", Description: "Operational tooling: purge, platform stats, and dead-lettered task administration."},
		{Name: "Auth", Description: "Caller identity."},
		{Name: "Health", Description: "Liveness, readiness, and build info."},
	}
}

// NewHumaAPI creates a new Huma API wrapper around a chi router.
func NewHumaAPI(r chi.Router, cfg HumaConfig) huma.API {
	return NewHumaAPIWithPrefix(r, cfg, "")
}

// NewHumaAPIWithPrefix creates a new Huma API wrapper with a path prefix.
// The prefix is used for server URL in OpenAPI spec (e.g., "/v1").
func NewHumaAPIWithPrefix(r chi.Router, cfg HumaConfig, prefix string) huma.API {
	// Every API this process serves renders its 5xx bodies without the
	// internal error that caused them. It belongs here rather than in the
	// composition root so that a test or spec generator cannot construct a
	// surface the production one does not have.
	web.RedactServerErrors()

	humaConfig := huma.DefaultConfig(cfg.Title, cfg.Version)
	humaConfig.Info.Description = cfg.Description
	humaConfig.Tags = apiTags()

	// Huma serves the OpenAPI spec at /openapi.json|yaml and JSON schemas at
	// /schemas/{name}. Its built-in docs route is disabled: the router serves
	// its own Scalar page at /docs (see docs.go) because the pinned Huma
	// version cannot pass renderer configuration.
	humaConfig.DocsPath = ""

	// Configure OpenAPI settings. Default to "/" when prefix is empty so
	// compiled-in $schema examples and generated cfctl models match
	// ccogen.gen.go. Subpath deployments that strip a path prefix at the edge
	// proxy can set CCO_OPENAPI_SERVER_URL (for example "/cuttlefish-orchestrator/")
	// so Scalar "Test Request" targets the external subpath.
	serverURL := "/"
	if prefix != "" {
		serverURL = prefix
	} else if envURL := strings.TrimSpace(os.Getenv("CCO_OPENAPI_SERVER_URL")); envURL != "" {
		serverURL = envURL
	}
	humaConfig.Servers = []*huma.Server{
		{URL: serverURL, Description: "Current server"},
	}

	// Browser sessions are managed at the edge (oauth2-proxy / IAP), while
	// API/CLI callers authenticate via bearer tokens or trusted proxy headers.
	humaConfig.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearerAuth": {
			Type:         "http",
			Scheme:       "bearer",
			BearerFormat: "JWT",
		},
	}

	return humachi.New(r, humaConfig)
}

// HumaHandler is an interface that Huma-compatible handlers must implement.
type HumaHandler interface {
	// Register registers the handler's operations with the Huma API.
	Register(api huma.API)
}
