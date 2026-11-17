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

package devicefeature

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// SignalingHandler proxies WebRTC signaling to host orchestrators.
type SignalingHandler struct {
	deviceRepo      device.Getter
	hostRepo        host.Getter
	hoClientFactory *hoclient.Factory
	logger          *slog.Logger
}

// NewSignalingHandler creates a new signaling handler.
func NewSignalingHandler(deviceRepo device.Getter, hostRepo host.Getter, hoClientFactory *hoclient.Factory, logger *slog.Logger) *SignalingHandler {
	return &SignalingHandler{
		deviceRepo:      deviceRepo,
		hostRepo:        hostRepo,
		hoClientFactory: hoClientFactory,
		logger:          logger,
	}
}

// CreateConnection creates a new signaling connection (POST /connect)
func (h *SignalingHandler) CreateConnection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	instance := chi.URLParam(r, "name")
	h.logger.Debug("Creating signaling connection",
		slog.String("device_id", id), slog.String("instance", instance))

	target, err := resolveHostClient(r.Context(), h.deviceRepo, h.hostRepo, h.hoClientFactory, id)
	if err != nil {
		web.RespondError(w, err)
		return
	}
	client := target.client

	operatorDeviceID, err := resolveOperatorDeviceID(r.Context(), client, target.device, instance)
	if err != nil {
		h.logger.Error("Failed to resolve the operator device id", slog.Any("error", err))
		web.RespondError(w, err)
		return
	}

	h.logger.Debug("Creating polled connection",
		slog.String("device_id", target.device.ID),
		slog.String("instance", instance),
		slog.String("cvd_id_db", target.device.CVDID),
		slog.String("operator_device_id", operatorDeviceID),
	)

	resp, err := client.CreatePolledConnection(r.Context(), operatorDeviceID)
	if err != nil {
		h.logger.Error("Failed to create polled connection", slog.Any("error", err))

		if hostErr, ok := errors.AsType[*hoclient.HostError](err); ok && hostErr.StatusCode == http.StatusNotFound {
			web.RespondError(w, web.NewNotFoundError("Device not found on host orchestrator"))
			return
		}

		web.RespondError(w, err)
		return
	}

	web.RespondOK(w, resp)
}

// ForwardMessage forwards a signaling message (POST /{connId})
func (h *SignalingHandler) ForwardMessage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	connID := chi.URLParam(r, "connId")

	target, err := resolveHostClient(r.Context(), h.deviceRepo, h.hostRepo, h.hoClientFactory, id)
	if err != nil {
		web.RespondError(w, err)
		return
	}
	client := target.client

	// The frontend sends the raw signaling message; hoclient wraps it in the
	// {payload: msg} envelope the operator's /:forward endpoint expects.
	var msg map[string]any
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		// A malformed body is the client's fault, so this logs at Warn. The
		// response says nothing about the decode error, which makes this log
		// line the only record of what arrived.
		h.logger.Warn("Rejecting malformed signaling message", slog.Any("error", err))
		web.RespondError(w, web.NewBadRequestError("Invalid message"))
		return
	}

	h.logForwardedMessage(id, msg)

	if err := client.ForwardSignalingMessage(r.Context(), connID, msg); err != nil {
		h.logger.Error("Failed to forward signaling message", slog.Any("error", err))

		if hostErr, ok := errors.AsType[*hoclient.HostError](err); ok && hostErr.StatusCode == http.StatusNotFound {
			web.RespondError(w, web.NewNotFoundError("Signaling connection not found (device may have disconnected)"))
			return
		}

		web.RespondError(w, err)
		return
	}

	web.RespondOK(w, "ok")
}

// logForwardedMessage records what the client sent. A request-offer without
// ice_servers is the signature of a client that will fall back to a direct
// connection, so its absence is logged louder than its presence.
func (h *SignalingHandler) logForwardedMessage(deviceID string, msg map[string]any) {
	msgType, ok := msg["type"].(string)
	if !ok {
		h.logger.Warn("Forwarding signaling message with no type", slog.Any("msg", msg))
		return
	}
	h.logger.Debug("Forwarding signaling message",
		slog.String("device_id", deviceID),
		slog.String("msg_type", msgType),
	)
	if msgType != "request-offer" {
		return
	}
	iceServers, ok := msg["ice_servers"]
	if !ok {
		h.logger.Warn("No ice_servers in request-offer")
		return
	}
	h.logger.Debug("ICE Servers in request-offer", slog.Any("ice_servers", iceServers))
}

// GetMessages retrieves signaling messages (GET /{connId})
func (h *SignalingHandler) GetMessages(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	connID := chi.URLParam(r, "connId")

	target, err := resolveHostClient(r.Context(), h.deviceRepo, h.hostRepo, h.hoClientFactory, id)
	if err != nil {
		web.RespondError(w, err)
		return
	}
	client := target.client

	start := 0
	if s := r.URL.Query().Get("start"); s != "" {
		if i, err := strconv.Atoi(s); err == nil {
			start = i
		}
	}

	resp, err := client.GetSignalingMessages(r.Context(), connID, start)
	if err != nil {
		h.logger.Error("Failed to get signaling messages", slog.Any("error", err))

		if hostErr, ok := errors.AsType[*hoclient.HostError](err); ok && hostErr.StatusCode == http.StatusNotFound {
			web.RespondError(w, web.NewNotFoundError("Signaling connection not found"))
			return
		}

		web.RespondError(w, err)
		return
	}

	web.RespondOK(w, resp)
}
