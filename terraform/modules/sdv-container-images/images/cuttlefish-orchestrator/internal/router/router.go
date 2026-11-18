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
	"fmt"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"

	legacyfeature "cloud-cuttlefish-orchestrator/internal/compat/legacy"
	adminfeature "cloud-cuttlefish-orchestrator/internal/features/admin"
	batchfeature "cloud-cuttlefish-orchestrator/internal/features/batch"
	buildfeature "cloud-cuttlefish-orchestrator/internal/features/build"
	devicefeature "cloud-cuttlefish-orchestrator/internal/features/device"
	gcecatalogfeature "cloud-cuttlefish-orchestrator/internal/features/gcecatalog"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	hostconfigfeature "cloud-cuttlefish-orchestrator/internal/features/hostconfig"
	machinetypefeature "cloud-cuttlefish-orchestrator/internal/features/machinetype"
	operationfeature "cloud-cuttlefish-orchestrator/internal/features/operation"
	quotafeature "cloud-cuttlefish-orchestrator/internal/features/quota"
	relayfeature "cloud-cuttlefish-orchestrator/internal/features/relay"
	storagefeature "cloud-cuttlefish-orchestrator/internal/features/storage"
	"cloud-cuttlefish-orchestrator/internal/middleware"
)

// Ensure handlers implement the HumaHandler interface at compile time.
var (
	_ HumaHandler = (*devicefeature.DeviceHandler)(nil)
	_ HumaHandler = (*hostfeature.HostHandler)(nil)
	_ HumaHandler = (*buildfeature.BuildHandler)(nil)
	_ HumaHandler = (*quotafeature.QuotaHandler)(nil)
	_ HumaHandler = (*adminfeature.AdminHandler)(nil)
	_ HumaHandler = (*adminfeature.HealthHandler)(nil)
	_ HumaHandler = (*storagefeature.StorageHandler)(nil)
	_ HumaHandler = (*machinetypefeature.MachineTypeHandler)(nil)
	_ HumaHandler = (*gcecatalogfeature.Handler)(nil)
	_ HumaHandler = (*hostconfigfeature.Handler)(nil)
	_ HumaHandler = (*relayfeature.Handler)(nil)
	_ HumaHandler = (*operationfeature.Handler)(nil)
)

// Config holds the dependencies and configuration for the HTTP router.
type Config struct {
	DeviceHandler          *devicefeature.DeviceHandler
	BatchHandler           *batchfeature.Handler
	HostHandler            *hostfeature.HostHandler
	QuotaHandler           *quotafeature.QuotaHandler
	BuildHandler           *buildfeature.BuildHandler
	StorageHandler         *storagefeature.StorageHandler
	MachineTypeHandler     *machinetypefeature.MachineTypeHandler
	GCECatalogHandler      *gcecatalogfeature.Handler
	AdminHandler           *adminfeature.AdminHandler
	OperationHandler       *operationfeature.Handler
	HealthHandler          *adminfeature.HealthHandler
	HostImageConfigHandler *hostconfigfeature.Handler

	// RelayHandler serves the TURN relay's authentication callback. Nil unless
	// a TURN shared secret is configured.
	RelayHandler *relayfeature.Handler

	// LegacyCOHandler serves the byte-exact legacy Cloud Orchestrator surface at
	// /v1. Nil disables it (the native API lives at /v2 regardless).
	LegacyCOHandler *legacyfeature.COHandler

	// RequireAuthForProbes enforces authentication for /health and /ready on
	// the main server when an isolated probe port serves unauthenticated probes.
	RequireAuthForProbes bool

	AuthConfig middleware.AuthConfig
	Logger     *slog.Logger
}

// New constructs the main HTTP handler by wiring feature handlers to the Chi router.
//
// It enforces global middleware chains and mounts the Huma OpenAPI instance
// to serve auto-generated documentation and validate API contracts.
func New(cfg Config) (http.Handler, error) {
	r := chi.NewRouter()

	// Global middleware applied to all routes. Panic recovery wraps the whole
	// edge handler in internal/app (outside this router), so SPA serving
	// and the otel wrapper are covered too.
	r.Use(middleware.RequestID())
	r.Use(middleware.ClientInfo())
	r.Use(middleware.LoggerDefault())
	// The server span itself is created at the edge by otelhttp (see
	// internal/app). This shim tags it with the matched chi route template
	// once routing resolves.
	r.Use(otelRouteTag)

	// CSRF protection for the cookie-authenticated browser path. The SPA
	// rides the ambient IAP session cookie, so unsafe-method cross-site
	// requests must be rejected. The SPA and API share one Cloud Run origin,
	// so no cross-origin exemptions are needed. Transparent to bearer-token
	// API clients.
	csrf, err := middleware.CSRFProtection(nil)
	if err != nil {
		return nil, fmt.Errorf("configuring CSRF protection: %w", err)
	}
	r.Use(csrf)

	// Authenticate every route except the Cloud Run health probes, which hit
	// the container directly (bypassing IAP) and must stay open. Everything
	// else, including /info and the Huma docs/openapi/schemas, requires a
	// verified identity. This is the single source of truth for public paths.
	// Authentication applies to all API routes except explicit unauthenticated
	// probe endpoints (unless isolated to a separate probe server).
	var publicPaths []string
	if !cfg.RequireAuthForProbes {
		publicPaths = []string{"/health", "/ready"}
	}
	r.Use(pathBasedAuth(cfg.AuthConfig, publicPaths))

	// Create Huma API for OpenAPI spec generation
	humaAPI := NewHumaAPI(r, DefaultHumaConfig())

	// Register all Huma handlers (paths include the /v2 prefix). This also
	// registers /health, /ready, and /info via HealthHandler.Register;
	// /health and /ready stay public via the pathBasedAuth allowlist above.
	registerHumaHandlers(humaAPI, cfg)

	registerMe(humaAPI)

	// Interactive API reference (Scalar). Behind auth like every other route;
	// Huma's built-in docs route is disabled in NewHumaAPIWithPrefix.
	r.Get("/docs", docsHandler(DefaultHumaConfig().Title+" Reference"))

	// The only chi-only routes under /v2, which are what Huma cannot express:
	// WebRTC signaling (the long-poll holds requests open, the WebSocket route
	// hijacks the connection) and storage object byte streams. Each slice owns
	// its raw routes.
	cfg.DeviceHandler.RegisterRaw(r)
	cfg.StorageHandler.RegisterRaw(r)

	// Legacy Cloud Orchestrator compatibility surface at the original /v1 paths,
	// behind the same authentication as everything else.
	if cfg.LegacyCOHandler != nil {
		cfg.LegacyCOHandler.Register(r)
	}

	// Huma also serves /openapi.json|yaml and /schemas/{name}. Like every
	// route except /health and /ready they require a verified identity
	// (see pathBasedAuth above).

	return r, nil
}

// registerHumaHandlers registers all Huma-compatible handlers with the API.
// This enables automatic OpenAPI spec generation and validation.
func registerHumaHandlers(api huma.API, cfg Config) {
	if cfg.HealthHandler != nil {
		cfg.HealthHandler.Register(api)
	}
	if cfg.DeviceHandler != nil {
		cfg.DeviceHandler.Register(api)
	}
	if cfg.BatchHandler != nil {
		cfg.BatchHandler.Register(api)
	}
	if cfg.HostHandler != nil {
		cfg.HostHandler.Register(api)
	}
	if cfg.BuildHandler != nil {
		cfg.BuildHandler.Register(api)
	}
	if cfg.QuotaHandler != nil {
		cfg.QuotaHandler.Register(api)
	}
	if cfg.AdminHandler != nil {
		cfg.AdminHandler.Register(api)
	}
	if cfg.StorageHandler != nil {
		cfg.StorageHandler.Register(api)
	}
	if cfg.MachineTypeHandler != nil {
		cfg.MachineTypeHandler.Register(api)
	}
	if cfg.GCECatalogHandler != nil {
		cfg.GCECatalogHandler.Register(api)
	}
	if cfg.HostImageConfigHandler != nil {
		cfg.HostImageConfigHandler.Register(api)
	}
	if cfg.RelayHandler != nil {
		cfg.RelayHandler.Register(api)
	}
	if cfg.OperationHandler != nil {
		cfg.OperationHandler.Register(api)
	}
}

// GetMeOutput is the output for the caller-identity endpoint.
type GetMeOutput struct {
	Body struct {
		Email       string `json:"email" doc:"Verified caller email"`
		DisplayName string `json:"display_name,omitempty" doc:"Human-readable display name"`
	}
}

// registerMe registers the caller-identity endpoint. With no RBAC there are
// no roles or permissions to report, so it returns the verified email alone.
func registerMe(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-me",
		Method:      http.MethodGet,
		Path:        "/v2/me",
		Summary:     "Get the authenticated caller",
		Description: "Returns the verified identity of the caller",
		Tags:        []string{"Auth"},
		Security:    []map[string][]string{{"bearerAuth": {}}},
		Errors:      []int{http.StatusUnauthorized},
	}, func(ctx context.Context, _ *struct{}) (*GetMeOutput, error) {
		principal, ok := middleware.UserFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized("Authentication required")
		}
		out := &GetMeOutput{}
		out.Body.Email = principal.Email
		out.Body.DisplayName = principal.DisplayName
		return out, nil
	})
}

// otelRouteTag upgrades the otelhttp server span (created at the edge) with the
// matched chi route template, yielding low-cardinality span names and an
// http.route attribute on both the span and the otelhttp server metrics. chi
// does not populate http.Request.Pattern, so otelhttp cannot derive the route
// itself; the pattern is only known once routing resolves, hence the post-serve
// read. Long-lived/upgrade routes are excluded from tracing upstream, so they
// carry no span and this is a no-op for them.
func otelRouteTag(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)

		rctx := chi.RouteContext(r.Context())
		if rctx == nil {
			return
		}
		pattern := rctx.RoutePattern()
		if pattern == "" {
			return
		}

		route := semconv.HTTPRoute(pattern)
		if span := trace.SpanFromContext(r.Context()); span.SpanContext().IsValid() {
			span.SetName(r.Method + " " + pattern)
			span.SetAttributes(route)
		}
		if labeler, ok := otelhttp.LabelerFromContext(r.Context()); ok {
			labeler.Add(route)
		}
	})
}

// pathBasedAuth applies authentication to every route except the given public
// paths, matched exactly (no prefix matching, so a public "/health" never
// shadows a future "/healthz" or "/health/details").
func pathBasedAuth(authConfig middleware.AuthConfig, publicPaths []string) func(http.Handler) http.Handler {
	authMiddleware := middleware.Auth(authConfig)
	public := make(map[string]bool, len(publicPaths))
	for _, p := range publicPaths {
		public[p] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if public[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			authMiddleware(next).ServeHTTP(w, r)
		})
	}
}
