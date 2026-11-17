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

package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	legacyfeature "cloud-cuttlefish-orchestrator/internal/compat/legacy"
	"cloud-cuttlefish-orchestrator/internal/config"
	"cloud-cuttlefish-orchestrator/internal/middleware"
	"cloud-cuttlefish-orchestrator/internal/router"
)

// buildServer composes the HTTP server: router, edge middleware chain, and
// listen address. Global read/write timeouts would kill the legacy :wait
// long-poll (55s) and storage streams, so per-request deadlines are applied
// by middleware instead. The server enforces only header-read and idle
// timeouts.
func buildServer(cfg *config.Config, logger *slog.Logger, f *featureSet, legacyCO *legacyfeature.COHandler, authConfig middleware.AuthConfig) (*http.Server, error) {
	routerConfig := router.Config{
		DeviceHandler:          f.deviceHandler,
		BatchHandler:           f.batchHandler,
		HostHandler:            f.hostHandler,
		QuotaHandler:           f.quotaHandler,
		BuildHandler:           f.buildHandler,
		StorageHandler:         f.storageHandler,
		MachineTypeHandler:     f.machineTypeHandler,
		GCECatalogHandler:      f.gceCatalogHandler,
		AdminHandler:           f.adminHandler,
		HealthHandler:          f.healthHandler,
		OperationHandler:       f.operationHandler,
		HostImageConfigHandler: f.hostImageConfigHandler,
		RelayHandler:           f.relayHandler,
		LegacyCOHandler:        legacyCO,
		AuthConfig:             authConfig,
		Logger:                 logger,
	}

	r, err := router.New(routerConfig)
	if err != nil {
		return nil, fmt.Errorf("creating router: %w", err)
	}
	// Edge handler, outermost first: panic recovery (covering SPA serving and
	// the otel wrapper too), per-request deadlines (streaming/long-poll routes
	// exempt), otel instrumentation, SPA/API split.
	finalHandler := middleware.Recovery(logger)(
		middleware.RequestDeadlines(requestDeadlines(cfg))(
			instrumentedHandler(withSPAHandler(r))))

	return &http.Server{
		Addr:              listenAddr(cfg),
		Handler:           finalHandler,
		ReadHeaderTimeout: time.Duration(cfg.Server.ReadHeaderTimeout),
		IdleTimeout:       time.Duration(cfg.Server.IdleTimeout),
	}, nil
}

// listenAddr resolves the listen address from config, the Cloud Run PORT env
// var, or the 8080 default.
func listenAddr(cfg *config.Config) string {
	if cfg.Server.Address != "" {
		return cfg.Server.Address
	}
	port := cmp.Or(os.Getenv("PORT"), "8080")
	return ":" + port
}

// requestDeadlines classifies each request into the deadlines it runs under.
// Three classes: streaming routes run unbounded, host-operation routes get the
// long write deadline their handler actually needs, everything else gets the
// ordinary API bound.
func requestDeadlines(cfg *config.Config) func(*http.Request) middleware.Deadlines {
	ordinary := middleware.Deadlines{
		Read:  time.Duration(cfg.Server.ReadTimeout),
		Write: time.Duration(cfg.Server.WriteTimeout),
	}
	// Both halves move: an expired read deadline cancels the request context
	// (net/http cannot tell a slow handler from a departed client), so leaving
	// Read at the ordinary bound just converts the broken connection into a
	// 500 at the same 30 seconds.
	hostOperation := middleware.Deadlines{
		Read:  time.Duration(cfg.Server.SlowRequestTimeout),
		Write: time.Duration(cfg.Server.SlowRequestTimeout),
	}
	return func(r *http.Request) middleware.Deadlines {
		switch {
		case streamingRequest(r):
			return middleware.Deadlines{}
		case hostOperationRequest(r):
			return hostOperation
		default:
			return ordinary
		}
	}
}

// streamingRequest reports whether a request is exempt from per-request
// deadlines: the legacy /v1 surface (reverse-proxied streams and the :wait
// long-poll), storage object streaming, WebRTC signaling, and the ADB bridge.
//
// A successful WebSocket upgrade clears these deadlines anyway, since net/http
// drops them on hijack, but the handler does real work before upgrading
// (device and host lookups, a live CVD listing on the host, the upstream
// dial), and a deadline that expires in there breaks the response before the
// connection is ever hijacked.
func streamingRequest(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/v1/") ||
		strings.HasPrefix(r.URL.Path, "/v2/storage/") ||
		strings.Contains(r.URL.Path, "/signaling") ||
		strings.HasSuffix(r.URL.Path, "/adb")
}

// hostOperationRequest reports whether a request's handler waits out a Host
// Orchestrator operation, which runs for minutes: relaunching or powerwashing
// a CVD boots Android again, and archiving a recording streams the file off
// the host and into storage. Under the ordinary write deadline these break
// their own response at 30s, observed live as a 503 that also recycled the
// serving instance, while the operation completed on the host regardless.
//
// Matched by path because this runs ahead of routing. Screenshot is absent
// because its own wait budget keeps it well inside the ordinary deadline.
func hostOperationRequest(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/v2/devices/") {
		return false
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/start"),
		strings.HasSuffix(r.URL.Path, "/stop"),
		strings.HasSuffix(r.URL.Path, "/restart"),
		strings.HasSuffix(r.URL.Path, "/powerwash"),
		strings.HasSuffix(r.URL.Path, "/powerbtn"),
		strings.HasSuffix(r.URL.Path, "/bugreport"):
		return true
	}
	return strings.Contains(r.URL.Path, "/recordings/")
}

// instrumentedHandler wraps the edge handler with otelhttp, producing the
// inbound server span plus http.server.* metrics. The route template is added
// downstream by the router's otelRouteTag shim. WebSocket upgrades and storage
// streaming are excluded (see traceableRequest): otelhttp passes their original
// ResponseWriter through untouched, preserving hijack/flush, and they avoid
// multi-hour spans.
func instrumentedHandler(h http.Handler) http.Handler {
	return otelhttp.NewHandler(h, "cco.http.server",
		otelhttp.WithFilter(traceableRequest),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method
		}),
	)
}

// traceableRequest reports whether a request should get a server span. It
// excludes hijacked/long-lived connections: WebSocket upgrades (browser
// signaling and the legacy /v1 Host Orchestrator connect_client passthrough)
// and storage object streaming.
func traceableRequest(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/v2/storage/") {
		return false
	}
	return true
}

func withSPAHandler(apiHandler http.Handler) http.Handler {
	return withSPADir(apiHandler, "/web/cuttlefish-console")
}

func withSPADir(apiHandler http.Handler, spaPath string) http.Handler {
	info, err := os.Stat(spaPath)
	if err != nil || !info.IsDir() {
		return apiHandler
	}
	indexPath := "index.html"

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// API routes go to the API handler
		if isAPIPath(r.URL.Path) {
			apiHandler.ServeHTTP(w, r)
			return
		}

		// All other routes are served from the main SPA
		path := r.URL.Path
		if path == "/" {
			path = "/" + indexPath
		}
		fullPath := spaPath + path
		if _, err := os.Stat(fullPath); err == nil {
			http.ServeFile(w, r, fullPath)
			return
		}
		http.ServeFile(w, r, spaPath+"/"+indexPath)
	})
}

func isAPIPath(path string) bool {
	// The legacy build-API OAuth endpoints are exact paths. A prefix match on
	// "/auth" would also capture SPA routes. Exact "/docs", "/schemas", and
	// OpenAPI spec paths avoid swallowing SPA routes such as "/docsify" or
	// "/openapi-ui".
	switch path {
	case "/auth", "/oauth2callback", "/deauth", "/health", "/ready", "/info",
		"/docs", "/schemas", "/openapi.json", "/openapi.yaml", "/openapi-3.0.json", "/openapi-3.0.yaml":
		return true
	}
	apiPrefixes := []string{
		"/api/v1/",
		"/v1/",
		"/v2/",
		"/connect/",
		"/docs/",    // Huma API documentation (Stoplight Elements / Scalar)
		"/schemas/", // JSON schemas
	}
	for _, prefix := range apiPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// buildAuthConfig assembles the auth middleware config from the env-derived
// configuration, rejecting combinations that would silently fail closed.
// onGCP reports whether the process runs on Google Cloud; it gates dev mode
// and is injectable so tests can simulate a GCP environment.
func buildAuthConfig(cfg *config.Config, onGCP func() bool) (middleware.AuthConfig, error) {
	ac := middleware.DefaultAuthConfig()
	// cfg.Auth already reflects the CCO_AUTH_MODE / CCO_IAP_AUDIENCE env
	// overrides applied by config.Load (env wins).
	ac.Mode = middleware.AccessMode(cfg.Auth.Mode)
	ac.IAPAudience = cfg.Auth.IAPAudience
	ac.DevMode = cfg.Auth.DevMode
	ac.DevFallbackEmail = cfg.Auth.DevFallbackEmail

	// DevMode cannot coexist with proxy auth mode: even off GCP, enabling
	// CCO_DEV_MODE=true must not silently bypass trusted-proxy validation.
	if ac.DevMode && ac.Mode == middleware.AccessModeProxy {
		return ac, errors.New(`dev_mode cannot be enabled when auth mode is "proxy"`)
	}

	// DevMode trusts a forgeable identity header; never allow it on GCP. The
	// env checks catch Cloud Run with an exact message; the metadata probe
	// catches everything else on Google Cloud, where a GCE VM or GKE pod sets
	// neither env var, and network mode is the sanctioned no-auth deployment
	// there.
	if ac.DevMode {
		switch {
		case os.Getenv("K_SERVICE") != "":
			return ac, errors.New("dev_mode is not allowed in Cloud Run (authentication would be forgeable)")
		case os.Getenv("GOOGLE_CLOUD_PROJECT") != "" && os.Getenv("FUNCTIONS_EMULATOR") != "true":
			return ac, errors.New("dev_mode detected in a GCP environment (security risk)")
		case onGCP():
			return ac, errors.New("dev_mode detected on Google Cloud via the metadata server (security risk); use auth=network for a credential-less deployment")
		}
		slog.Warn("dev_mode enabled - authentication headers can be forged. For development only.")
		return ac, nil
	}

	// An empty mode defaults to IAP (matching DefaultAuthConfig / the config
	// default), so callers need not set AUTH_MODE when using IAP.
	if ac.Mode == "" {
		ac.Mode = middleware.AccessModeIAP
	}

	// Without an audience for the active mode every request fails closed (401),
	// which would be a silent total outage.
	switch ac.Mode {
	case middleware.AccessModeIAP:
		if ac.IAPAudience == "" {
			return ac, errors.New("auth=iap requires an IAP audience (IAP_AUDIENCE or [auth] iap_audience)")
		}
	case middleware.AccessModeProxy:
		proxies, err := middleware.ParseTrustedProxies(cfg.Auth.TrustedProxies)
		if err != nil {
			return ac, fmt.Errorf("auth=proxy requires valid CCO_TRUSTED_PROXIES: %w", err)
		}
		ac.TrustedProxies = proxies
		if strings.IndexFunc(cfg.Auth.ProxyDefaultUser, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return ac, errors.New("CCO_DEFAULT_USER must not contain control characters")
		}
		defaultUser := strings.TrimSpace(cfg.Auth.ProxyDefaultUser)
		ac.ProxyDefaultUser = defaultUser
	case middleware.AccessModeNetwork:
		// No credential material to check: the mode reads nothing.
	default:
		return ac, fmt.Errorf("unsupported auth mode %q (want \"iap\", \"network\", or \"proxy\")", ac.Mode)
	}
	return ac, nil
}

// runningOnGCP reports whether the process can reach the GCE metadata server,
// the one signal that holds across Cloud Run, GCE and GKE. GCE_METADATA_HOST
// overrides the well-known name, as the Google client libraries honor it.
func runningOnGCP() bool {
	host := os.Getenv("GCE_METADATA_HOST")
	if host == "" {
		host = "metadata.google.internal"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// The host comes from the process's own boot environment (the standard
	// Google client-library override), never from a request, so the gosec
	// SSRF finding does not apply.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+"/computeMetadata/v1/", http.NoBody) //nolint:gosec // boot-env host, not request data
	if err != nil {
		return false
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // boot-env host, not request data
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.Header.Get("Metadata-Flavor") == "Google"
}
