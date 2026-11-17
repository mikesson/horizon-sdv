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

package cco

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A non-JSON error body (an IAP sign-in page, a proxy error) must not land
// in the error string whole, because do() reads up to 10 MB of it.
func TestDecodeAPIErrorTruncatesNonJSONBody(t *testing.T) {
	t.Parallel()
	page := bytes.Repeat([]byte("<html>IAP says no</html>"), 8192) // About 192 KB.

	err := decodeAPIError(401, page)
	if got := len(err.Error()); got > 4096 {
		t.Errorf("error string is %d bytes, want it capped near 2 KB", got)
	}
	if !strings.Contains(err.Error(), "... (") || !strings.Contains(err.Error(), "more bytes)") {
		t.Errorf("truncated error does not say it was truncated:\n%.200s", err.Error())
	}
	for _, r := range err.Error() {
		if r > 127 {
			t.Errorf("truncated error carries non-ASCII %q", r)
			break
		}
	}

	// Small bodies and JSON problem details stay untouched.
	if err := decodeAPIError(400, []byte(`{"title":"Bad Request","detail":"nope"}`)); !strings.Contains(err.Error(), "nope") {
		t.Errorf("problem-detail body mangled: %v", err)
	}
	if err := decodeAPIError(502, []byte("bad gateway")); !strings.Contains(err.Error(), "bad gateway") {
		t.Errorf("small body mangled: %v", err)
	}
}

// A success body past the read cap is reported as too large. A cut body would
// otherwise surface as a JSON decode error.
func TestDoRejectsOversizedBody(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":["`))
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxResponseBody))
		_, _ = w.Write([]byte(`"]}`))
	}))
	t.Cleanup(srv.Close)
	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}

	var out map[string]any
	err := c.do(t.Context(), http.MethodGet, "/v2/devices", nil, &out)
	if err == nil {
		t.Fatal("an oversized body decoded without error")
	}
	if !strings.Contains(err.Error(), "larger than") || strings.Contains(err.Error(), "decode") {
		t.Errorf("error = %v, want the size cap named instead of a decode failure", err)
	}
}
