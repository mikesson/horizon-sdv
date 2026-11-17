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
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Snapshot semantics copied from upstream: take is an async operation whose
// stored result names the snapshot (defaulting the id to the operation name),
// the CVD stays Running throughout (the host suspends and resumes around the
// capture itself), and delete is an idempotent remove in a host-global id
// namespace. There is no list, because the real host keeps no inventory,
// which is why the platform keeps one.

// createSnapshot serves POST /cvds/{group}/{name}/snapshots.
func (s *Server) createSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SnapshotID string `json:"snapshot_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		reason := "empty body"
		if !errors.Is(err, io.EOF) {
			reason = err.Error()
		}
		writeErr(w, http.StatusBadRequest, "Malformed JSON in request", reason)
		return
	}

	ip := localIP(r)
	s.mu.Lock()
	h := s.hostLocked(ip)
	op := s.newOperationLocked(ip, nil)
	if msg := s.snapFail[ip]; msg != "" {
		s.ops[op].failure = msg
	}
	id := req.SnapshotID
	if id == "" {
		id = op
	}
	s.ops[op].value = map[string]string{"snapshot_id": id}
	// A failed take produces nothing durable, because upstream deletes the
	// half-written directory on failure.
	if s.ops[op].failure == "" {
		h.snapshots[id] = true
	}
	s.mu.Unlock()

	writeOperation(w, op)
}

// deleteSnapshot serves DELETE /snapshots/{id}. Removing an absent snapshot
// succeeds, exactly like the recursive remove it stands in for.
func (s *Server) deleteSnapshot(w http.ResponseWriter, r *http.Request) {
	ip := localIP(r)
	id := chi.URLParam(r, "id")

	s.mu.Lock()
	delete(s.hostLocked(ip).snapshots, id)
	op := s.newOperationLocked(ip, emptyResult())
	s.mu.Unlock()

	writeOperation(w, op)
}

// snapshotExistsLocked reports whether a snapshot id is present on a host.
// Callers hold s.mu.
func (s *Server) snapshotExistsLocked(ip, id string) bool {
	return s.hostLocked(ip).snapshots[id]
}

// HasSnapshot reports whether a host currently stores the snapshot. Tests use
// it to assert a delete actually removed it.
func (s *Server) HasSnapshot(ip, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotExistsLocked(ip, id)
}

// snapshotNotFound writes the upstream restore-path refusal for an id whose
// directory is gone.
func snapshotNotFound(w http.ResponseWriter, id string) {
	writeErr(w, http.StatusNotFound, "snapshot not found", fmt.Sprintf("snapshot id %q not found", id))
}
