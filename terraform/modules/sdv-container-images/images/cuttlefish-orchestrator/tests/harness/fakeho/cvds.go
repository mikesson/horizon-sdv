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
	"time"

	"github.com/go-chi/chi/v5"
)

// listCVDs serves GET /cvds.
func (s *Server) listCVDs(w http.ResponseWriter, r *http.Request) {
	ip := localIP(r)
	now := time.Now()

	s.mu.Lock()
	if s.noCVDsAPI[ip] {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "Not found", "this host serves no /cvds API")
		return
	}
	h := s.hostLocked(ip)
	cvds := make([]*CVD, 0, len(h.cvds))
	for _, c := range h.cvds {
		c.refresh(now)
		cvds = append(cvds, c)
	}
	s.mu.Unlock()

	writeJSON(w, map[string]any{"cvds": cvds})
}

// listDevices serves the Cuttlefish operator's GET /devices, the listing
// hoclient falls back to when /cvds 404s. It reports no status, which is the
// reason the fallback degrades a device's health to unknown.
func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	ip := localIP(r)

	s.mu.Lock()
	h := s.hostLocked(ip)
	devices := make([]map[string]any, 0, len(h.cvds))
	for _, c := range h.cvds {
		devices = append(devices, map[string]any{
			"device_id":  c.Group + "-" + c.Name,
			"group_name": c.Group,
			"name":       c.Name,
			"adb_port":   6520,
		})
	}
	s.mu.Unlock()

	writeJSON(w, devices)
}

// createCVD serves POST /cvds: it registers the group named in the request's
// env_config (production uses the deterministic cvd<deviceID[:8]> group) and
// parks the result under an operation the follow-up :wait resolves.
func (s *Server) createCVD(w http.ResponseWriter, r *http.Request) {
	ip := localIP(r)

	var body struct {
		EnvConfig map[string]any `json:"env_config"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	group := findGroupName(body.EnvConfig)

	s.mu.Lock()
	if s.poisoned[ip] {
		s.mu.Unlock()
		// A poisoned host fails every create with the leaked-tap / stale
		// registration signature. Only /reset clears that condition.
		writeErr(w, http.StatusInternalServerError, "cvd create failed",
			"Failed to validate tap devices: cvd-mtap-01 in use; Device Id already registered")
		return
	}
	if group == "" {
		group = fmt.Sprintf("cvd-anon-%d", s.nextIDLocked())
	}
	h := s.hostLocked(ip)
	cvd := &CVD{Group: group, Name: "1", Status: statusRunning, ADB: "127.0.0.1:6520"}
	if s.loadDelay > 0 {
		cvd.Status = statusStarting
		cvd.readyAt = time.Now().Add(s.loadDelay)
	}
	h.cvds[group] = cvd
	s.created[ip] = append(s.created[ip], group)
	op := s.newCreateOperationLocked(ip, group)
	s.mu.Unlock()

	writeOperation(w, op)
}

// deleteCVD serves DELETE /cvds/{group} and /cvds/{group}/{name}.
func (s *Server) deleteCVD(w http.ResponseWriter, r *http.Request) {
	ip := localIP(r)
	group := chi.URLParam(r, "group")
	now := time.Now()

	s.mu.Lock()
	h := s.hostLocked(ip)
	if c, ok := h.cvds[group]; ok {
		c.refresh(now)
		if c.Status == statusStarting {
			s.violations = append(s.violations,
				fmt.Sprintf("DELETE of group %s on %s while its launch was in flight", group, ip))
		}
		delete(h.cvds, group)
		s.deleted[ip] = append(s.deleted[ip], group)
	}
	// HO reports absent-group delete as an op failure, not 404; the production
	// cleanup probes ListCVDs first, so a plain op suffices.
	op := s.newOperationLocked(ip, emptyResult())
	s.mu.Unlock()

	writeOperation(w, op)
}

// startInstance serves POST /cvds/{group}/{name}/:start.
//
// Upstream decodes a StartCVDRequest from the body unconditionally, so an
// absent body is a 400 rather than a default-valued start. A fake that
// tolerates the empty body hides that from every client.
func (s *Server) startInstance(w http.ResponseWriter, r *http.Request) {
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
	// Restore path: upstream resolves the id to a directory before starting
	// and 404s when it is gone. CCO's restore verb has to surface that refusal.
	if req.SnapshotID != "" {
		ip := localIP(r)
		s.mu.Lock()
		known := s.snapshotExistsLocked(ip, req.SnapshotID)
		s.mu.Unlock()
		if !known {
			snapshotNotFound(w, req.SnapshotID)
			return
		}
	}
	s.actuate(w, r, ":start", statusRunning)
}

// groupVerb serves a group-scoped verb: POST /cvds/{group}/:start and its
// :stop counterpart, which upstream routes to a handler that reads the group
// variable and decodes no body. It is NOT the instance handler with the name
// defaulted. Both are modeled so that a client which addresses one member
// cannot pass for one that addresses the group.
func (s *Server) groupVerb(verb, newStatus string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.actuate(w, r, verb, newStatus)
	}
}

// instanceVerb serves the instance verbs that take no body and leave the CVD
// running: :restart, :powerwash and :powerbtn keep it running, :stop stops it.
func (s *Server) instanceVerb(verb string) http.HandlerFunc {
	status := statusRunning
	if verb == ":stop" {
		status = statusStopped
	}
	return func(w http.ResponseWriter, r *http.Request) {
		s.actuate(w, r, verb, status)
	}
}

// actuate applies an instance verb and answers with its operation. Actuating a
// group whose launch is still in flight is the adopt-race collision: upstream
// fails it against the taps and registration the live launcher holds, and the
// attempt is ledgered as a violation.
func (s *Server) actuate(w http.ResponseWriter, r *http.Request, verb, newStatus string) {
	ip := localIP(r)
	group := chi.URLParam(r, "group")
	now := time.Now()

	s.mu.Lock()
	h := s.hostLocked(ip)
	if c, ok := h.cvds[group]; ok {
		c.refresh(now)
		if c.Status == statusStarting {
			s.violations = append(s.violations,
				fmt.Sprintf("%s of group %s on %s while its launch was in flight", verb, group, ip))
			s.mu.Unlock()
			writeErr(w, http.StatusInternalServerError, "cvd start failed",
				"Device Id already registered; tap device in use")
			return
		}
		c.Status = newStatus
	}
	op := s.newOperationLocked(ip, emptyResult())
	s.mu.Unlock()

	writeOperation(w, op)
}

// resetHost serves POST /reset. `cvd reset` removes every group and frees the
// leaked taps / stale registration, un-poisoning the host in place. That is
// the production remediation this mirrors.
func (s *Server) resetHost(w http.ResponseWriter, r *http.Request) {
	ip := localIP(r)

	s.mu.Lock()
	delete(s.poisoned, ip)
	h := s.hostLocked(ip)
	h.cvds = make(map[string]*CVD)
	op := s.newOperationLocked(ip, emptyResult())
	s.mu.Unlock()

	writeOperation(w, op)
}

// writeOperation answers a verb with its operation handle. done is false: the
// result lives behind :wait, never in this response.
func writeOperation(w http.ResponseWriter, name string) {
	writeJSON(w, map[string]any{"name": name, "done": false})
}

// emptyResult is upstream's EmptyResponse, what an instance verb's operation
// stores and therefore what its :wait returns.
func emptyResult() any { return map[string]any{} }

// findGroupName digs group_name out of an env_config, wherever it sits.
func findGroupName(v any) string {
	switch t := v.(type) {
	case map[string]any:
		if g, ok := t["group_name"].(string); ok && g != "" {
			return g
		}
		for _, child := range t {
			if g := findGroupName(child); g != "" {
				return g
			}
		}
	case []any:
		for _, child := range t {
			if g := findGroupName(child); g != "" {
				return g
			}
		}
	}
	return ""
}
