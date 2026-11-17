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

package hostfeature

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/web"
)

// ListHostCVDsInput is the input for listing the CVDs on a host.
type ListHostCVDsInput struct {
	ID string `path:"id" format:"uuid" doc:"Host ID"`
}

// ListHostCVDsOutput is the output for listing the CVDs on a host.
type ListHostCVDsOutput struct {
	Body struct {
		CVDs []HostCVD `json:"cvds" doc:"Every CVD on the host, sorted by group and name"`
	}
}

// SetCVDViewer enables the host CVD view; without one the operation answers
// 503.
func (h *HostHandler) SetCVDViewer(v *CVDViewer) {
	h.cvds = v
}

// registerCVDs registers the host CVD view.
func (h *HostHandler) registerCVDs(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-host-cvds",
		Method:      http.MethodGet,
		Path:        "/v2/hosts/{id}/cvds",
		Summary:     "List the CVDs on a host",
		Description: "Queries the host's Host Orchestrator live and returns every CVD on it, including ones created by cvdr through /v1. " +
			"device_id names the CCO device that owns the group and is empty for CVDs CCO did not create. The host must be running.",
		Tags:     []string{tagHosts},
		Security: web.BearerAuth(),
		// 409: the host is not running. 502: its Host Orchestrator did not
		// answer. 503: this deployment has no Host Orchestrator client.
		Errors: []int{http.StatusNotFound, http.StatusConflict, http.StatusBadGateway, http.StatusServiceUnavailable},
	}, h.ListHostCVDs)
}

// ListHostCVDs lists the CVDs on a host (Huma handler).
func (h *HostHandler) ListHostCVDs(ctx context.Context, input *ListHostCVDsInput) (*ListHostCVDsOutput, error) {
	if h.cvds == nil {
		return nil, huma.Error503ServiceUnavailable("the host CVD view is not configured on this server")
	}
	cvds, err := h.cvds.List(ctx, input.ID)
	if errors.Is(err, errHostOrchestrator) {
		return nil, huma.Error502BadGateway(err.Error())
	}
	if err != nil {
		return nil, web.MapError(err)
	}
	output := &ListHostCVDsOutput{}
	output.Body.CVDs = cvds
	return output, nil
}
