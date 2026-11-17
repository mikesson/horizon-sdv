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
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// waitOperation serves POST /v1/zones/{zone}/operations/{operation}/:wait.
//
// Mirroring the legacy CO, this blocks until the operation completes and returns
// the resolved resource directly (the HostInstance for a create, an empty
// object for a delete), or 503 "Wait for operation timed out" if it does not
// settle within the wait window (the client then re-issues :wait).
func (h *COHandler) waitOperation(w http.ResponseWriter, r *http.Request) {
	action, hostID, ok := parseOperationName(chi.URLParam(r, "operation"))
	if !ok {
		h.writeError(w, http.StatusBadRequest, "invalid operation name")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.waitTimeout)
	defer cancel()

	result, done, err := h.pollOperation(ctx, action, hostID)
	switch {
	// The wait window can expire while checkOperation is mid-query; the legacy
	// contract wants the clean timeout 503, not a 500 from the wrapped
	// context.DeadlineExceeded.
	case err != nil && ctx.Err() != nil:
		h.writeError(w, http.StatusServiceUnavailable, "Wait for operation timed out")
	case err != nil:
		h.writeServiceError(w, err)
	case !done:
		h.writeError(w, http.StatusServiceUnavailable, "Wait for operation timed out")
	default:
		h.writeJSON(w, http.StatusOK, result)
	}
}

// pollOperation polls the operation's terminal state until it settles, the
// context expires (timeout: done=false), or an error occurs.
func (h *COHandler) pollOperation(ctx context.Context, action, hostID string) (any, bool, error) {
	ticker := time.NewTicker(h.pollInterval)
	defer ticker.Stop()

	for {
		result, done, err := h.checkOperation(ctx, action, hostID)
		if err != nil || done {
			return result, done, err
		}
		select {
		case <-ctx.Done():
			return nil, false, nil
		case <-ticker.C:
		}
	}
}

// checkOperation resolves the operation's current state from the host record.
// A nil error with done=false means "still in progress".
func (h *COHandler) checkOperation(ctx context.Context, action, hostID string) (any, bool, error) {
	switch action {
	case opActionCreate:
		hostVM, err := h.hosts.Get(ctx, hostID)
		if err != nil {
			return nil, false, fmt.Errorf("resolving create operation: %w", err)
		}
		switch hostVM.Status {
		case host.StatusRunning:
			return hostToInstance(hostVM), true, nil
		case host.StatusError:
			return nil, false, fmt.Errorf("host provisioning failed: %s", hostVM.ErrorMessage)
		default:
			return nil, false, nil
		}
	case opActionDelete:
		hostVM, err := h.hosts.Get(ctx, hostID)
		if errors.Is(err, host.ErrNotFound) {
			return struct{}{}, true, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("resolving delete operation: %w", err)
		}
		if hostVM.Status == host.StatusDeleted {
			return struct{}{}, true, nil
		}
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("unknown operation %q", action)
	}
}
