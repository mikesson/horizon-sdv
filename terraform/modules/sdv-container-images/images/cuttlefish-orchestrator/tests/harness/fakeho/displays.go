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
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// onePixelPNG is a valid 1x1 PNG. The screenshot path serves real image bytes
// so a client asserting "PNG-framed and non-empty" has something that can fail.
var onePixelPNG = mustDecodeBase64(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")

// listDisplays serves GET /cvds/{group}/{name}/displays.
//
// It answers directly, with no operation. The wire shape is a MAP keyed by
// display number, with dimensions buried in mode.windowed and dpi as an array,
// which is why the client carries a custom unmarshaller for it.
func (s *Server) listDisplays(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	displays := s.hostLocked(localIP(r)).displays[cvdID(r)]
	out := make(map[string]any, len(displays))
	for number, d := range displays {
		out[strconv.Itoa(number)] = map[string]any{
			"dpi":             []int{d.dpi, d.dpi},
			"mode":            map[string]any{"windowed": []int{d.width, d.height}},
			"refresh_rate_hz": d.refreshRateHz,
		}
	}
	s.mu.Unlock()

	writeJSON(w, map[string]any{"displays": out})
}

// addDisplay serves POST /cvds/{group}/{name}/displays, directly.
func (s *Server) addDisplay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Width         int `json:"width"`
		Height        int `json:"height"`
		DPI           int `json:"dpi"`
		RefreshRateHz int `json:"refresh_rate_hz"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed DisplayAddRequest JSON in request", err.Error())
		return
	}

	s.mu.Lock()
	h := s.hostLocked(localIP(r))
	id := cvdID(r)
	if h.displays[id] == nil {
		h.displays[id] = make(map[int]display)
	}
	// The host assigns the number; the request never carries one.
	number := 0
	for ; ; number++ {
		if _, taken := h.displays[id][number]; !taken {
			break
		}
	}
	h.displays[id][number] = display{
		width: req.Width, height: req.Height, dpi: req.DPI, refreshRateHz: req.RefreshRateHz,
	}
	s.mu.Unlock()

	writeJSON(w, map[string]any{"display_number": number})
}

// removeDisplay serves DELETE /cvds/{group}/{name}/displays/{n}, directly.
func (s *Server) removeDisplay(w http.ResponseWriter, r *http.Request) {
	number, err := strconv.Atoi(chi.URLParam(r, "displayNumber"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid display number", err.Error())
		return
	}

	s.mu.Lock()
	delete(s.hostLocked(localIP(r)).displays[cvdID(r)], number)
	s.mu.Unlock()

	writeJSON(w, map[string]any{})
}

// screenshot serves GET /cvds/{group}/{name}/displays/{n}/:screenshot.
//
// This is the endpoint the async/sync split turns on. Unlike its three
// siblings it answers with an OPERATION, and the image exists only in that
// operation's result. The response body here carries no image at all, so a
// client that decodes it as one gets an empty capture served as success.
func (s *Server) screenshot(w http.ResponseWriter, r *http.Request) {
	if _, err := strconv.Atoi(chi.URLParam(r, "displayNumber")); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid display number", err.Error())
		return
	}

	ip := localIP(r)
	image := base64.StdEncoding.EncodeToString(onePixelPNG)

	s.mu.Lock()
	if s.blankShot[ip] {
		image = ""
	}
	op := s.newOperationLocked(ip, map[string]any{
		"screenshot_bytes":     image,
		"screenshot_mime_type": "image/png",
	})
	s.mu.Unlock()

	writeOperation(w, op)
}

// cvdID is the "group/name" key a CVD's sub-resources hang off.
func cvdID(r *http.Request) string {
	return chi.URLParam(r, "group") + "/" + chi.URLParam(r, "name")
}

func mustDecodeBase64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic("fakeho: bad embedded image: " + err.Error())
	}
	return b
}
