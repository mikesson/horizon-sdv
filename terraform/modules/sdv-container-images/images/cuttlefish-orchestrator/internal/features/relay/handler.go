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

package relayfeature

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/platform/turn"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// Handler serves the relay authentication callback.
type Handler struct {
	service *Service
}

// NewHandler creates the relay handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// Register registers relay operations with the Huma API.
func (h *Handler) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "authorize-relay-credential",
		Method:      http.MethodPost,
		Path:        "/v2/relay/authorize",
		Summary:     "Authorize a TURN credential",
		Description: "Returns the long-term key for a TURN credential, or refuses it. " +
			"Called by a relay that delegates authentication, so the relay never holds the shared secret.",
		Tags:     []string{"Relay"},
		Security: []map[string][]string{{"bearerAuth": {}}},
		Errors:   []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound},
	}, h.Authorize)
}

// Authorize answers a relay's authentication callback.
func (h *Handler) Authorize(ctx context.Context, input *AuthorizeInput) (*AuthorizeOutput, error) {
	algorithm := turn.PasswordAlgorithm(input.Body.Algorithm)
	if algorithm == "" {
		algorithm = turn.AlgorithmMD5
	}

	auth, err := h.service.Authorize(ctx, AuthorizeRequest{
		Username:  input.Body.Username,
		Realm:     input.Body.Realm,
		Algorithm: algorithm,
	})
	if err != nil {
		return nil, mapAuthorizeError(err)
	}

	return &AuthorizeOutput{Body: AuthorizedKey{
		Key:       base64.StdEncoding.EncodeToString(auth.Key),
		ExpiresAt: auth.ExpiresAt.Unix(),
	}}, nil
}

// mapAuthorizeError maps a refusal to its status. The detail is specific
// because the only caller is an operator-run relay, and a relay that cannot
// authenticate is otherwise very hard to diagnose.
func mapAuthorizeError(err error) error {
	switch {
	case errors.Is(err, turn.ErrMalformedUsername), errors.Is(err, turn.ErrUnknownAlgorithm):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, ErrCredentialExpired), errors.Is(err, ErrRealmMismatch), errors.Is(err, ErrSubjectGone):
		return huma.Error403Forbidden(err.Error())
	default:
		return web.MapError(err)
	}
}
