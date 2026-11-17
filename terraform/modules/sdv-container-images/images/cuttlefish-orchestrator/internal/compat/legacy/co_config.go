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

package legacy

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/compat/legacy/coapi"
	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/platform/turn"
)

// getConfig serves GET /v1/config.
func (h *COHandler) getConfig(w http.ResponseWriter, _ *http.Request) {
	h.writeJSON(w, http.StatusOK, coapi.Config{InstanceManagerType: legacyInstanceManagerType})
}

// getInfraConfig serves GET /v1/zones/{zone}/hosts/{host}/infra_config: the
// WebRTC ICE configuration for connections to devices on this host.
//
// The legacy CO answers this from a static config file. CCO composes it,
// flattened to the single list this wire admits, with any TURN credential
// minted for this host and this caller. The credential is scoped to the host
// rather than a device because the path names no device. It is read once per
// connection, before the client asks which device it wants.
func (h *COHandler) getInfraConfig(w http.ResponseWriter, r *http.Request) {
	hostID := chi.URLParam(r, "host")

	var email string
	// A deployment with nothing to mint stays host-independent, as upstream's
	// static answer is: the identity and the host lookup below are what
	// minting needs, not what the endpoint means.
	if h.ice.Mints() {
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			h.writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		email = principal.Email
		// Resolve the host first: a credential minted for one that does not
		// exist would be refused at its first use by the relay, with nothing
		// at that end able to say why.
		if _, err := h.hosts.Get(r.Context(), hostID); err != nil {
			h.writeServiceError(w, err)
			return
		}
	}

	servers := make([]coapi.IceServer, 0, 2)
	for _, s := range h.ice.Flatten(email, turn.HostSubject(hostID)) {
		servers = append(servers, coapi.IceServer{
			URLs:       s.URLs,
			Username:   s.Username,
			Credential: s.Credential,
		})
	}

	h.writeJSON(w, http.StatusOK, coapi.InfraConfig{IceServers: servers})
}
