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

package fakeho

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// createPolledConnection serves POST /polled_connections.
//
// This and the two below are the Cuttlefish operator's surface rather than the
// Host Orchestrator's; hoclient reaches them on the same base URL, so the fake
// serves them together.
func (s *Server) createPolledConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Malformed JSON in request", err.Error())
		return
	}

	s.mu.Lock()
	h := s.hostLocked(localIP(r))
	connID := fmt.Sprintf("conn-%d", s.nextIDLocked())
	h.conns[connID] = nil
	s.mu.Unlock()

	writeJSON(w, map[string]any{
		"connection_id": connID,
		"device_info":   map[string]any{"device_id": req.DeviceID},
	})
}

// forwardSignaling serves POST /polled_connections/{connID}/:forward. The
// operator wraps every client message as {"payload": …}; what comes back is
// queued for the poll below, which is how a test drives a negotiation.
func (s *Server) forwardSignaling(w http.ResponseWriter, r *http.Request) {
	connID := chi.URLParam(r, "connID")

	var msg map[string]any
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		writeErr(w, http.StatusBadRequest, "Malformed JSON in request", err.Error())
		return
	}

	s.mu.Lock()
	h := s.hostLocked(localIP(r))
	if _, ok := h.conns[connID]; !ok {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "Connection not found", connID)
		return
	}
	h.conns[connID] = append(h.conns[connID], msg["payload"])
	s.mu.Unlock()

	writeJSON(w, map[string]any{})
}

// signalingMessages serves GET /polled_connections/{connID}/messages?start=N,
// returning the queue from N onward. N is the counter a client advances so it
// never re-reads a message.
func (s *Server) signalingMessages(w http.ResponseWriter, r *http.Request) {
	connID := chi.URLParam(r, "connID")
	start := 0
	if v := r.URL.Query().Get("start"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid start", err.Error())
			return
		}
		start = parsed
	}

	s.mu.Lock()
	h := s.hostLocked(localIP(r))
	queued, ok := h.conns[connID]
	s.mu.Unlock()

	if !ok {
		writeErr(w, http.StatusNotFound, "Connection not found", connID)
		return
	}
	if start > len(queued) {
		start = len(queued)
	}
	writeJSON(w, queued[start:])
}
