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

// Package parityfile is the codec for the legacy /v1 parity recordings in
// internal/compat/legacy/testdata/parity: self-contained request/response
// captures written by tools/legacyparity from the upstream Cloud Orchestrator
// and replayed against CCO's /v1 handler by parity_test.go. Both sides share
// this one codec so the format cannot drift between writer and reader.
//
// The format is line-oriented and byte-faithful where it matters:
//
//	# preamble comment lines
//	== request
//	METHOD /path?query
//	Header: value
//	<blank line>
//	<request body, no trailing newline>
//	== response 200
//	Header: value
//	<blank line>
//	<response body, exact bytes to end of file>
//
// The response body runs verbatim to EOF, so a trailing newline (which the
// upstream server emits on every JSON response) survives the round trip.
// The request body must not end in a newline; Marshal rejects one that does,
// which keeps the "\n== response" delimiter unambiguous.
package parityfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

const (
	requestMarker  = "== request\n"
	responseMarker = "\n== response "
)

// Recording is one parity case: the request to issue and the response the
// upstream Cloud Orchestrator produced for it.
type Recording struct {
	// Comment holds the "# "-prefixed preamble lines, verbatim.
	Comment string
	Method  string
	// Target is the request path with any raw query string.
	Target     string
	ReqHeaders http.Header
	ReqBody    []byte
	Status     int
	// RespHeaders is the full response header set minus Date, which is the
	// one volatile header either server emits.
	RespHeaders http.Header
	RespBody    []byte
}

// Marshal renders the recording in canonical form: headers sorted by name,
// one "Key: value" line per value, Date dropped.
func (r *Recording) Marshal() ([]byte, error) {
	if bytes.HasSuffix(r.ReqBody, []byte("\n")) {
		return nil, errors.New("request body ends in a newline, which the format cannot represent")
	}
	if strings.Contains(string(r.ReqBody), responseMarker) {
		return nil, errors.New("request body contains the response marker")
	}
	var b bytes.Buffer
	if r.Comment != "" {
		b.WriteString(r.Comment)
		if !strings.HasSuffix(r.Comment, "\n") {
			b.WriteString("\n")
		}
	}
	b.WriteString(requestMarker)
	fmt.Fprintf(&b, "%s %s\n", r.Method, r.Target)
	writeHeaders(&b, r.ReqHeaders)
	b.WriteString("\n")
	b.Write(r.ReqBody)
	fmt.Fprintf(&b, "%s%d\n", responseMarker, r.Status)
	writeHeaders(&b, r.RespHeaders)
	b.WriteString("\n")
	b.Write(r.RespBody)
	return b.Bytes(), nil
}

// Unmarshal parses a recording written by Marshal.
func Unmarshal(data []byte) (*Recording, error) {
	text := string(data)
	reqStart := strings.Index(text, requestMarker)
	if reqStart < 0 {
		return nil, fmt.Errorf("no %q marker", strings.TrimSpace(requestMarker))
	}
	respStart := strings.Index(text, responseMarker)
	if respStart < 0 {
		return nil, fmt.Errorf("no %q marker", strings.TrimSpace(responseMarker))
	}
	rec := &Recording{Comment: text[:reqStart]}

	req := text[reqStart+len(requestMarker) : respStart]
	line, rest, ok := strings.Cut(req, "\n")
	if !ok {
		return nil, errors.New("request block has no header/body separator")
	}
	rec.Method, rec.Target, ok = strings.Cut(line, " ")
	if !ok {
		return nil, fmt.Errorf("malformed request line %q", line)
	}
	headers, body, ok := strings.Cut(rest, "\n\n")
	if !ok {
		// A request with headers but no blank line means no body section.
		headers, body = strings.TrimSuffix(rest, "\n"), ""
	}
	var err error
	if rec.ReqHeaders, err = parseHeaders(headers); err != nil {
		return nil, fmt.Errorf("request headers: %w", err)
	}
	rec.ReqBody = []byte(body)

	resp := text[respStart+len(responseMarker):]
	line, rest, ok = strings.Cut(resp, "\n")
	if !ok {
		return nil, errors.New("response block has no status line")
	}
	if rec.Status, err = strconv.Atoi(line); err != nil {
		return nil, fmt.Errorf("response status %q: %w", line, err)
	}
	headers, body, ok = strings.Cut(rest, "\n\n")
	if !ok {
		return nil, errors.New("response block has no header/body separator")
	}
	if rec.RespHeaders, err = parseHeaders(headers); err != nil {
		return nil, fmt.Errorf("response headers: %w", err)
	}
	rec.RespBody = []byte(body)
	return rec, nil
}

func writeHeaders(b *bytes.Buffer, h http.Header) {
	for _, k := range slices.Sorted(maps.Keys(h)) {
		if k == "Date" {
			continue
		}
		for _, v := range h[k] {
			fmt.Fprintf(b, "%s: %s\n", k, v)
		}
	}
}

func parseHeaders(block string) (http.Header, error) {
	h := http.Header{}
	if block == "" {
		return h, nil
	}
	for line := range strings.Lines(block) {
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			return nil, fmt.Errorf("malformed header line %q", line)
		}
		h.Add(k, v)
	}
	return h, nil
}

// EchoHostOrchestrator is the deterministic Host Orchestrator stand-in both
// the recorder and the replay put behind the reverse proxy. It echoes what
// the proxy delivered: method, rewritten path, query, and whether the
// Authorization header survived. A recording therefore pins the proxy's
// rewrite and passthrough behavior, never the fake's content.
func EchoHostOrchestrator() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Best effort: a write failure means the proxy went away.
		_ = json.NewEncoder(w).Encode(struct {
			Method        string `json:"method"`
			Path          string `json:"path"`
			Query         string `json:"query"`
			Authorization string `json:"authorization"`
		}{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")})
	})
}
