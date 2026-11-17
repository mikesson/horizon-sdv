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

// Package legacy serves the legacy Cloud (Android) Orchestrator REST surface at
// its original /v1 paths, mapping each request onto CCO's native services. It is
// a compatibility facade: unlike a feature slice it composes several features
// on purpose, so existing Google integrations keep working unchanged while
// the native API lives at /v2. Wire shapes are the verbatim coapi structs.
package legacy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/compat/legacy/coapi"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	"cloud-cuttlefish-orchestrator/internal/platform/ice"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// HostService is the subset of the host feature the legacy surface needs.
type HostService interface {
	Create(ctx context.Context, req host.CreateRequest, createdBy string, origin host.Origin) (*host.Host, error)
	List(ctx context.Context, filter host.ListFilter) ([]*host.Host, error)
	Get(ctx context.Context, id string) (*host.Host, error)
	Delete(ctx context.Context, id string) error
}

// ZoneLister enumerates the zones in which hosts may be created.
type ZoneLister interface {
	ListEnabledZones(ctx context.Context) ([]string, error)
}

// V1ZoneHintPolicy reports whether the /v1 zone token is read as a placement
// hint (dynamic ranked placement, the default) rather than a literal pin.
type V1ZoneHintPolicy func(ctx context.Context) (bool, error)

// DockerHostPolicy resolves the GCE shape a docker-only create request
// compiles to (operator-settable machine type and TTL).
type DockerHostPolicy func(ctx context.Context) (hostconfig.DockerHost, error)

// Default bounds for the blocking :wait long-poll. The legacy GCE
// ZoneOperations.Wait blocks for up to ~2 minutes; CCO polls its own host record
// instead, capping below typical 60s client/proxy timeouts so a timed-out :wait
// returns a clean 503 the client re-issues.
const (
	defaultWaitTimeout  = 55 * time.Second
	defaultPollInterval = 2 * time.Second

	// legacyInstanceManagerType is the value GET /v1/config reports. CCO is
	// always GCE-backed, which the legacy contract identifies as "GCP".
	legacyInstanceManagerType = "GCP"

	// defaultMaxDevices is the per-host device capacity for legacy CO
	// host-create requests, whose wire shape carries no such field. Matches
	// the native /v2 API default (max_devices default:"4"); the host domain
	// rejects a non-positive value.
	defaultMaxDevices = 4
)

// COHandler implements the legacy Cloud Orchestrator endpoints.
type COHandler struct {
	hosts        HostService
	zones        ZoneLister
	zoneHint     V1ZoneHintPolicy
	dockerHost   DockerHostPolicy
	ice          *ice.Composer
	logger       *slog.Logger
	waitTimeout  time.Duration
	pollInterval time.Duration
	// hoTransport carries Host Orchestrator proxy requests; a field so tests
	// can inject a short-timeout transport. Defaults to hoProxyTransport.
	hoTransport http.RoundTripper
}

// NewCOHandler constructs the legacy CO handler over the given native services.
//
// The ICE composer is not optional: infra_config answers from it in every
// topology, with or without a relay. Where there is one, upstream's static
// credential becomes an ephemeral mint. The relay module renders no static
// secret, so every authentication is delegated to the orchestrator, which
// refuses a lapsed username. The wire shape stays byte-identical; only the
// value changes.
func NewCOHandler(hosts HostService, zones ZoneLister, iceComposer *ice.Composer, logger *slog.Logger) *COHandler {
	return &COHandler{
		hosts:        hosts,
		zones:        zones,
		ice:          iceComposer,
		logger:       logger,
		waitTimeout:  defaultWaitTimeout,
		pollInterval: defaultPollInterval,
		hoTransport:  hoProxyTransport,
	}
}

// WithV1ZoneHintPolicy wires the operator policy for how the zone token is
// read; without it the token is a hint (the default).
func (h *COHandler) WithV1ZoneHintPolicy(p V1ZoneHintPolicy) *COHandler {
	h.zoneHint = p
	return h
}

// WithDockerHostPolicy wires the operator policy for the GCE shape a
// docker-only create compiles to; without it the compiled defaults apply.
func (h *COHandler) WithDockerHostPolicy(p DockerHostPolicy) *COHandler {
	h.dockerHost = p
	return h
}

// dockerHostShape resolves the docker-create shape; a missing or failing
// policy read degrades to the compiled defaults.
func (h *COHandler) dockerHostShape(ctx context.Context) hostconfig.DockerHost {
	if h.dockerHost == nil {
		return hostconfig.DefaultDockerHost()
	}
	shape, err := h.dockerHost(ctx)
	if err != nil {
		h.logger.Warn("reading docker host policy failed; using compiled defaults", slog.Any("error", err))
		return hostconfig.DefaultDockerHost()
	}
	return shape
}

// v1ZoneIsHint resolves the zone-token mode; a missing or failing policy read
// degrades to the hint default.
func (h *COHandler) v1ZoneIsHint(ctx context.Context) bool {
	if h.zoneHint == nil {
		return true
	}
	hint, err := h.zoneHint(ctx)
	if err != nil {
		h.logger.Warn("reading v1 zone-token policy failed; defaulting to hint mode", slog.Any("error", err))
		return true
	}
	return hint
}

// Register mounts the legacy CO routes on the chi router at their exact original
// paths. Host-Orchestrator passthrough, operations/:wait, and config endpoints
// are registered by their own methods on this handler.
func (h *COHandler) Register(r chi.Router) {
	r.Get("/v1/zones", h.listZones)
	r.Post("/v1/zones/{zone}/hosts", h.createHost)
	r.Get("/v1/zones/{zone}/hosts", h.listHosts)
	r.Delete("/v1/zones/{zone}/hosts/{host}", h.deleteHost)
	r.Post("/v1/zones/{zone}/operations/{operation}/:wait", h.waitOperation)
	// chi routes on the escaped path when one exists, so the percent-encoded
	// colon a strict URL encoder emits needs its own registration. Upstream's
	// gorilla/mux routes on the decoded path and serves both spellings.
	r.Post("/v1/zones/{zone}/operations/{operation}/%3Await", h.waitOperation)
	r.Get("/v1/config", h.getConfig)
	r.Get("/v1/zones/{zone}/hosts/{host}/infra_config", h.getInfraConfig)
	// Catch-all: forward every other path under a host to its Host Orchestrator.
	// Registered last; chi matches the specific routes above before this wildcard.
	r.HandleFunc("/v1/zones/{zone}/hosts/{host}/*", h.proxyToHost)
}

// writeJSON encodes v as the response body with the given status.
func (h *COHandler) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Best effort: a write failure means the client disconnected.
	_ = json.NewEncoder(w).Encode(v) //nolint:errchkjson // best effort response
}

// writeError emits the legacy error envelope ({code, error}).
func (h *COHandler) writeError(w http.ResponseWriter, status int, msg string) {
	h.writeJSON(w, status, coapi.Error{Code: status, ErrorMsg: msg})
}

// writeServiceError maps a native service error onto the legacy error envelope.
// An error no arm of statusFromError classifies answers exactly as upstream's
// unhandled-error dispatch does, {"code":0,"error":"Internal Server Error"}
// with the zero Code included, because err.Error() on that path is backend
// detail (Spanner, GCE) that upstream never leaks to legacy clients.
func (h *COHandler) writeServiceError(w http.ResponseWriter, err error) {
	status := statusFromError(err)
	if status == http.StatusInternalServerError {
		if _, ok := errors.AsType[*web.AppError](err); !ok {
			h.writeJSON(w, status, coapi.Error{ErrorMsg: "Internal Server Error"})
			return
		}
	}
	h.writeError(w, status, err.Error())
}

// statusFromError is /v1's own status contract, for the plain-HTTP legacy
// handlers that cannot return Huma errors.
//
// It is independent of web.MapError on purpose and must not be kept in step
// with it. /v1 answers clients CCO does not ship and cannot update (cvdr
// re-issues :wait on 503), so a status here is frozen, while /v2's is free to
// move with the console and cfctl. The two already differ: a missing platform
// quota row is 404 on /v2 and 500 here.
func statusFromError(err error) int {
	if appErr, ok := errors.AsType[*web.AppError](err); ok {
		return appErr.Code
	}
	if _, ok := errors.AsType[*host.CatalogValidationError](err); ok {
		return http.StatusUnprocessableEntity
	}
	if _, ok := errors.AsType[*host.BootDiskSizeError](err); ok {
		return http.StatusBadRequest
	}
	if _, ok := errors.AsType[*quota.ExceededError](err); ok {
		return http.StatusTooManyRequests
	}
	switch {
	case errors.Is(err, host.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, host.ErrInvalidState):
		return http.StatusConflict
	case errors.Is(err, host.ErrCapacityExceeded):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
