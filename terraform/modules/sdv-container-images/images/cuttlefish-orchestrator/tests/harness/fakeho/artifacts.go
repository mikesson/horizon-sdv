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
	"bytes"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
)

// bugreportZIP is the content served for a captured bugreport: a real (empty)
// ZIP, so a client checking the magic bytes has something that can fail.
var bugreportZIP = []byte("PK\x05\x06" + string(make([]byte, 18)))

// recordingContent is the content served for a screen recording: an EBML header,
// which is how a real host frames one (Matroska/WebM, verified against a stored
// artifact). It is framed rather than arbitrary bytes for the same reason as
// the ZIP above. A fake that could not survive the production framing check
// teaches the wrong thing, and a client checking the magic needs something
// that can fail.
var recordingContent = append([]byte{0x1A, 0x45, 0xDF, 0xA3}, []byte("fake-screen-recording")...)

// createBugreport serves POST /cvds/{group}/:bugreport.
//
// The operation's stored result is the bugreport ID as a BARE JSON STRING and
// never an object, which is why the client decodes it into a string. The ZIP
// is then a separate download by that ID.
func (s *Server) createBugreport(w http.ResponseWriter, r *http.Request) {
	ip := localIP(r)
	group := chi.URLParam(r, "group")

	s.mu.Lock()
	h := s.hostLocked(ip)
	if _, ok := h.cvds[group]; !ok {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "Group not found", "unknown group "+group)
		return
	}
	// Upstream's include_adb_bugreport query parameter only enriches the
	// capture; it changes nothing on the wire, so the fake records it as part
	// of the ID rather than branching.
	id := fmt.Sprintf("br-%d", s.nextIDLocked())
	if includeADB(r) {
		id += "-adb"
	}
	h.bugreports[id] = bugreportZIP
	op := s.newOperationLocked(ip, id)
	s.mu.Unlock()

	writeOperation(w, op)
}

// includeADB reports whether the capture was asked to run a device-side adb
// bugreport. Upstream treats the parameter's mere presence as true unless it
// is explicitly "false".
func includeADB(r *http.Request) bool {
	values, ok := r.URL.Query()["include_adb_bugreport"]
	if !ok {
		return false
	}
	return len(values) == 0 || values[0] != "false"
}

// downloadBugreport serves GET /cvdbugreports/{id} as raw ZIP bytes.
func (s *Server) downloadBugreport(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	s.mu.Lock()
	content, ok := s.hostLocked(localIP(r)).bugreports[id]
	s.mu.Unlock()

	if !ok {
		writeErr(w, http.StatusNotFound, "Bugreport not found", id)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	_, _ = w.Write(content)
}

// deleteBugreport serves DELETE /cvdbugreports/{id}, directly.
func (s *Server) deleteBugreport(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	s.mu.Lock()
	h := s.hostLocked(localIP(r))
	_, ok := h.bugreports[id]
	delete(h.bugreports, id)
	s.mu.Unlock()

	if !ok {
		writeErr(w, http.StatusNotFound, "Bugreport not found", id)
		return
	}
	writeJSON(w, map[string]any{})
}

// startRecording serves POST /cvds/{group}/{name}/:start_screen_recording. The
// recording only becomes listable once it is stopped, as on a real host.
func (s *Server) startRecording(w http.ResponseWriter, r *http.Request) {
	ip := localIP(r)

	s.mu.Lock()
	h := s.hostLocked(ip)
	id := cvdID(r)
	if h.recordings[id] == nil {
		h.recordings[id] = make(map[string][]byte)
	}
	op := s.newOperationLocked(ip, emptyResult())
	s.mu.Unlock()

	writeOperation(w, op)
}

// stopRecording serves POST /cvds/{group}/{name}/:stop_screen_recording,
// publishing the file the download endpoint then serves.
func (s *Server) stopRecording(w http.ResponseWriter, r *http.Request) {
	ip := localIP(r)

	s.mu.Lock()
	h := s.hostLocked(ip)
	id := cvdID(r)
	if h.recordings[id] == nil {
		h.recordings[id] = make(map[string][]byte)
	}
	name := fmt.Sprintf("recording_%d.mp4", s.nextIDLocked())
	h.recordings[id][name] = recordingContent
	op := s.newOperationLocked(ip, emptyResult())
	s.mu.Unlock()

	writeOperation(w, op)
}

// listRecordings serves GET /cvds/{group}/{name}/screen_recordings, directly.
func (s *Server) listRecordings(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	recordings := s.hostLocked(localIP(r)).recordings[cvdID(r)]
	names := make([]string, 0, len(recordings))
	for name := range recordings {
		names = append(names, name)
	}
	s.mu.Unlock()

	// Stable order: a map iteration would make a client's assertion flaky
	// against a fake rather than against the code under test.
	sort.Strings(names)
	writeJSON(w, map[string]any{"screen_recordings": names})
}

// downloadRecording serves GET /cvds/{group}/{name}/screen_recordings/{name}
// as raw bytes.
func (s *Server) downloadRecording(w http.ResponseWriter, r *http.Request) {
	recording := chi.URLParam(r, "recording")

	s.mu.Lock()
	content, ok := s.hostLocked(localIP(r)).recordings[cvdID(r)][recording]
	s.mu.Unlock()

	if !ok {
		writeErr(w, http.StatusNotFound, "Recording not found", recording)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	_, _ = w.Write(content)
}

// serveLogFile serves GET /cvds/{group}/{name}/logs/{file} with real Range
// semantics (http.ServeContent, the same stdlib code path upstream's
// http.FileServer ends in). Upstream resolves the group through `cvd fleet`
// before touching a file, so an unknown group 404s regardless of content.
func (s *Server) serveLogFile(w http.ResponseWriter, r *http.Request) {
	group := chi.URLParam(r, "group")
	file := chi.URLParam(r, "file")

	s.mu.Lock()
	h := s.hostLocked(localIP(r))
	_, inFleet := h.cvds[group]
	content, ok := h.logs[cvdID(r)][file]
	s.mu.Unlock()

	if !inFleet {
		writeErr(w, http.StatusNotFound, "Group not found", group)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "Log file not found", file)
		return
	}
	http.ServeContent(w, r, file, time.Time{}, bytes.NewReader(content))
}
