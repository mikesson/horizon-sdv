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
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/compat/legacy/coapi"
	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
)

// listZones serves GET /v1/zones, the zones enabled for host creation.
func (h *COHandler) listZones(w http.ResponseWriter, r *http.Request) {
	zones, err := h.zones.ListEnabledZones(r.Context())
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	resp := coapi.ListZonesResponse{Items: make([]*coapi.Zone, 0, len(zones))}
	for _, zone := range zones {
		resp.Items = append(resp.Items, &coapi.Zone{Name: zone})
	}
	h.writeJSON(w, http.StatusOK, resp)
}

// createHost serves POST /v1/zones/{zone}/hosts. The host name is server-assigned
// (the legacy field is output-only); creation is asynchronous, so the response is
// an Operation the client polls via :wait.
func (h *COHandler) createHost(w http.ResponseWriter, r *http.Request) {
	zone := chi.URLParam(r, "zone")

	// Legacy create requests are tiny; 1 MiB bounds a hostile body before the
	// unbounded JSON decode.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req coapi.CreateHostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			h.writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		// Upstream's message, verbatim. The parity recordings pin it.
		h.writeError(w, http.StatusBadRequest, "Malformed JSON in request")
		return
	}
	if req.HostInstance == nil || (req.HostInstance.GCP == nil && req.HostInstance.Docker == nil) {
		h.writeError(w, http.StatusBadRequest, "host_instance.gcp or host_instance.docker is required")
		return
	}

	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		h.writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var createReq host.CreateRequest
	if gcp := req.HostInstance.GCP; gcp != nil {
		// CCO models exactly one accelerator shape (type + count) end to end:
		// catalog validation, GPU attachment, quota. Refusing a longer list is
		// the accurate answer; this mapping used to truncate it silently.
		if len(gcp.AcceleratorConfigs) > 1 {
			h.writeError(w, http.StatusBadRequest, "multiple accelerator_configs are not supported")
			return
		}
		createReq = host.CreateRequest{
			Name:           generateHostName(),
			MachineType:    gcp.MachineType,
			MinCPUPlatform: gcp.MinCPUPlatform,
			BootDiskSizeGB: gcp.BootDiskSizeGB,
			MaxDevices:     defaultMaxDevices,
			// The same pair the docker branch stamps, for the same structural
			// reason: this host's CVDs arrive through the HO proxy with no
			// device rows, so the census must exempt it, and a census-exempt
			// host must be fenced from automatic placement. listHosts and the
			// proxy's CVD-create guard both key on the provenance, so without
			// it the client cannot see its own host or create a CVD on it.
			Exclusive:   true,
			ForeignCVDs: true,
		}
		if len(gcp.AcceleratorConfigs) == 1 {
			ac := gcp.AcceleratorConfigs[0]
			createReq.GPUType = ac.AcceleratorType
			createReq.GPUCount = ac.AcceleratorCount
		}
	} else {
		// A docker-only request comes from a client built for upstream's
		// on-premises deployment (OmniLab ATS hardcodes this shape) that
		// never reads the docker sub-message back, so it compiles to the
		// operator's GCE shape. image_name names a docker image with no CCO
		// counterpart, since the deployment has exactly one host image, so
		// it is ignored.
		shape := h.dockerHostShape(r.Context())
		createReq = host.CreateRequest{
			Name:        generateHostName(),
			MachineType: shape.MachineType,
			// This lease is the host's whole lifetime: its CVDs arrive
			// through the HO proxy with no device rows, so no activity can
			// ever extend it, and a shared policy key must not decide when
			// running lab tests die, so the TTL is this path's own.
			TTLHours: shape.TTLHours,
			// Exclusive is the fence everything here rests on: automatic
			// placement and the warm plane's walk both skip exclusive hosts,
			// which is all that keeps CCO's own devices off a host it cannot
			// census.
			// MaxDevices narrows the remaining window (explicit targeting by
			// id still works by design).
			Exclusive:   true,
			MaxDevices:  1,
			ForeignCVDs: true,
		}
	}
	// The zone token compiles to placement vocabulary. By default it is a hint:
	// the host service ranks the actual zone, so a client shipping a
	// hardcoded upstream zone CCO has never cataloged still admits and gets
	// dynamic placement. With the policy off it is the literal pin it always
	// was. Either way the token persists as requested_zone, which keeps this
	// host visible under it in listHosts wherever placement lands it.
	if h.v1ZoneIsHint(r.Context()) {
		createReq.LocationHint = zone
	} else {
		createReq.Zone = zone
		createReq.ZonePinned = true
	}

	// A legacy client asked for this host by name, so it is the caller's, not
	// the fleet's, which is the origin the native create stamps. cvdr and
	// OmniLab ATS come through here, so leaving it unstamped would draw exactly
	// the CI integrations' hosts into fleet arithmetic.
	hostVM, err := h.hosts.Create(r.Context(), createReq, principal.Email, host.OriginUser)
	if err != nil {
		// Upstream inserts the VM inside this request and answers an image GCE
		// refuses with GCE's own status and message (toAppError). A refusal
		// CCO makes itself has no upstream form and stays the unhandled 500.
		if refused, ok := errors.AsType[*hostconfig.ImageUnavailableError](err); ok && refused.Status != 0 {
			h.writeError(w, refused.Status, refused.Detail)
			return
		}
		h.writeServiceError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, coapi.Operation{
		Name: newOperationName(opActionCreate, hostVM.ID),
		Done: false,
	})
}

// listHosts serves GET /v1/zones/{zone}/hosts. The token partitions by
// requested OR physical zone: a host created under this token stays listed
// here wherever placement or failover physically lands it (so legacy
// create→list→delete flows never lose a host), while /v2-auto hosts (no
// requested zone) appear under their physical zone, which is upstream's
// behavior.
func (h *COHandler) listHosts(w http.ResponseWriter, r *http.Request) {
	// Upstream rejects an unparseable maxResults before its instance manager
	// sees the request; mirror the rejection and its message even though CCO
	// serves every host in one page and never truncates.
	if raw := r.URL.Query().Get("maxResults"); raw != "" {
		if _, err := strconv.ParseUint(raw, 10, 32); err != nil {
			h.writeError(w, http.StatusBadRequest,
				fmt.Sprintf("Invalid query parameter %q value: %q", "maxResults", raw))
			return
		}
	}

	zone := chi.URLParam(r, "zone")
	filter := host.ListFilter{RequestedOrPhysicalZone: &zone}

	hosts, err := h.hosts.List(r.Context(), filter)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	resp := coapi.ListHostsResponse{Items: make([]*coapi.HostInstance, 0, len(hosts))}
	for _, hv := range hosts {
		// Hosts this API created, not every host in the zone. A legacy client
		// lists and then creates a CVD on what it found, and that create is
		// refused on an orchestrator-owned host, so listing one offers a
		// choice that cannot be taken. Upstream's CO lists only its own hosts
		// for the structural version of the same reason: there, it is the only
		// control plane.
		if !hv.ForeignCVDs {
			continue
		}
		resp.Items = append(resp.Items, hostToInstance(hv))
	}
	h.writeJSON(w, http.StatusOK, resp)
}

// deleteHost serves DELETE /v1/zones/{zone}/hosts/{host}. Deletion is
// asynchronous, so the response is an Operation the client polls via :wait. The
// legacy {host} segment is the CCO host ID returned by create/list.
//
// It takes ANY host id, including an orchestrator-owned one, and that is a
// decision rather than the oversight it looks like beside listHosts' and
// proxyToHost's provenance checks. Two facts settle it:
//
// A legacy client cannot learn an orchestrator-owned id from this API. Only
// create and list hand out ids, create returns the caller's own foreign host,
// and list is filtered to foreign hosts. There is no single-host read here at
// all. An id from anywhere else came from outside /v1.
//
// And a caller holding one out of band can already delete it. Authentication is
// the only gate this system has, so the principal reaching /v1 reaches
// DELETE /v2/hosts/{id} too, and both land on the same HostService.Delete. A
// guard here would narrow one of two identical doors.
//
// What makes it safe to leave open is also what makes it different from the
// census trap the proxy closes. That one created state CCO could not see, a
// CVD with no device row, which the census then swept. This one cascades
// through the ordinary teardown, with the devices moving to error where anyone
// can see them.
//
// Adding a /v1 route that hands back an orchestrator-owned host id, or
// unfiltering listHosts, retires the first fact and reopens this.
func (h *COHandler) deleteHost(w http.ResponseWriter, r *http.Request) {
	hostID := chi.URLParam(r, "host")

	if err := h.hosts.Delete(r.Context(), hostID); err != nil {
		h.writeServiceError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, coapi.Operation{
		Name: newOperationName(opActionDelete, hostID),
		Done: false,
	})
}

// hostToInstance maps a CCO host onto the legacy HostInstance wire shape. The
// legacy host name is the CCO host ID, which is stable, available at create
// time, and directly resolvable by the proxy and delete paths.
func hostToInstance(h *host.Host) *coapi.HostInstance {
	gcp := &coapi.GCPInstance{
		MachineType:    h.MachineType,
		MinCPUPlatform: h.MinCPUPlatform,
		// The effective size, not the raw spec: a host created without the
		// setting runs the default, and upstream reports what the VM has.
		BootDiskSizeGB: cmp.Or(h.BootDiskSizeGB, host.DefaultBootDiskSizeGB),
	}
	if h.GPUType != "" {
		gcp.AcceleratorConfigs = []*coapi.AcceleratorConfig{{
			AcceleratorType:  h.GPUType,
			AcceleratorCount: h.GPUCount,
		}}
	}
	return &coapi.HostInstance{Name: h.ID, GCP: gcp}
}

// generateHostName returns a server-assigned host name for legacy create calls,
// matching the "starts with a letter, lowercase alphanumeric + hyphen" form.
// This is a display label, so it must stay out of the binding namespace.
// "cf-" here composed into a "cf-cf-…" instance name, and host.InstancePrefix
// is now reserved against exactly that.
func generateHostName() string {
	var b [4]byte
	// crypto/rand.Read never returns an error on the platforms we target.
	_, _ = rand.Read(b[:])
	return "host-" + hex.EncodeToString(b[:])
}
