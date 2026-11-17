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

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExtractClientIP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		xff        string
		xRealIP    string
		remoteAddr string
		want       string
	}{
		{
			name: "single XFF entry",
			xff:  "203.0.113.7",
			want: "203.0.113.7",
		},
		{
			// Cloud Run appends the real client as the FINAL entry; anything
			// the client supplied itself sits to the left and must be ignored.
			name: "spoofed leftmost entries ignored",
			xff:  "6.6.6.6, 203.0.113.7",
			want: "203.0.113.7",
		},
		{
			name: "many client-supplied entries",
			xff:  "1.1.1.1, 2.2.2.2, 3.3.3.3, 203.0.113.7",
			want: "203.0.113.7",
		},
		{
			name: "whitespace and trailing comma",
			xff:  "6.6.6.6 , 203.0.113.7 , ",
			want: "203.0.113.7",
		},
		{
			name:    "X-Real-IP fallback without XFF",
			xRealIP: "198.51.100.4",
			want:    "198.51.100.4",
		},
		{
			name:       "RemoteAddr fallback",
			remoteAddr: "192.0.2.9:52611",
			want:       "192.0.2.9",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.xRealIP != "" {
				r.Header.Set("X-Real-IP", tt.xRealIP)
			}
			if tt.remoteAddr != "" {
				r.RemoteAddr = tt.remoteAddr
			}
			if got := extractClientIP(r); got != tt.want {
				t.Errorf("extractClientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClientInfoPutsIPInContext(t *testing.T) {
	t.Parallel()
	var got string
	h := ClientInfo()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = MustIPFromContext(r.Context())
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.7")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got != "203.0.113.7" {
		t.Errorf("context IP = %q, want the rightmost XFF entry", got)
	}
}
