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

package hoclient

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// logServer mirrors the real Host Orchestrator's logs route: a GET-only
// stdlib file server (ServeContent), so Range semantics here are the same
// code path production hits, not a mock's approximation.
func logServer(t *testing.T, content []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/cvds/") {
			// A recorded failure for any non-logs call, so the open-breaker
			// test can trip the breaker with a normal call.
			http.Error(w, `{"error":"unhealthy"}`, http.StatusServiceUnavailable)
			return
		}
		http.ServeContent(w, r, "kernel.log", time.Now(), bytes.NewReader(content))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGetCVDLogTailSuffixRange(t *testing.T) {
	t.Parallel()
	content := bytes.Repeat([]byte("0123456789"), 100) // 1000 bytes
	c := NewFactory(probeTestConfig()).ClientForHost(logServer(t, content).URL)

	tail, total, err := c.GetCVDLogTail(t.Context(), "cvdabc", "dev-1", "kernel.log", 300)
	require.NoError(t, err)
	assert.Equal(t, int64(1000), total, "Content-Range must carry the full size")
	assert.Equal(t, content[700:], tail, "the tail must be the LAST 300 bytes")
}

func TestGetCVDLogTailFileSmallerThanTail(t *testing.T) {
	t.Parallel()
	content := []byte("short file\n")
	c := NewFactory(probeTestConfig()).ClientForHost(logServer(t, content).URL)

	tail, total, err := c.GetCVDLogTail(t.Context(), "cvdabc", "dev-1", "kernel.log", 300)
	require.NoError(t, err)
	assert.Equal(t, int64(len(content)), total)
	assert.Equal(t, content, tail)
}

func TestGetCVDLogTailMissingGroupIsAnError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(http.NotFound))
	t.Cleanup(srv.Close)
	c := NewFactory(probeTestConfig()).ClientForHost(srv.URL)

	_, _, err := c.GetCVDLogTail(t.Context(), "cvdabc", "dev-1", "kernel.log", 300)
	require.Error(t, err, "an unregistered group must surface, not read as an empty log")
}

// A diagnostic read of a possibly hung host must never be what takes the
// host out: like ProbeCVDs, the tail fetch neither teaches the breaker...
func TestGetCVDLogTailDoesNotTeachTheBreaker(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // the port now refuses connections

	c := NewFactory(probeTestConfig()).ClientForHost(srv.URL)
	for range 5 { // far past the 1-failure trip point
		_, _, err := c.GetCVDLogTail(t.Context(), "cvdabc", "dev-1", "kernel.log", 300)
		require.Error(t, err, "the dead endpoint must fail the fetch")
	}

	_, err := c.ListCVDs(t.Context())
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrCircuitOpen,
		"tail-fetch failures must not count toward the breaker")
	assert.ErrorIs(t, err, ErrHostUnreachable,
		"the normal call must still reach for the host")
}

// ...nor consults it: with the breaker open, the evidence read still works.
// That matters, since a host under diagnosis is exactly the host whose
// breaker is likely to be open.
func TestGetCVDLogTailReadsThroughOpenBreaker(t *testing.T) {
	t.Parallel()
	content := []byte("Kernel panic - not syncing: proof\n")
	srv := logServer(t, content)
	c := NewFactory(probeTestConfig()).ClientForHost(srv.URL)

	// Trip the single-failure breaker with a recorded failure (the fake 404s
	// every non-logs route, and ListCVDs records its outcome).
	_, err := c.ListCVDs(t.Context())
	require.Error(t, err)
	_, err = c.ListCVDs(t.Context())
	require.ErrorIs(t, err, ErrCircuitOpen, "the breaker must now refuse normal calls")

	tail, _, tailErr := c.GetCVDLogTail(t.Context(), "cvdabc", "dev-1", "kernel.log", 300)
	require.NoError(t, tailErr, "the evidence read must pass through the open breaker")
	assert.Equal(t, content, tail)
}

// The whole-file read exists because the snapshot precheck decides on markers
// assemble_cvd writes at the HEAD: a fetch that silently answered with the tail
// would read every long-lived device as having no launch at all.
func TestFetchCVDLogFileReadsTheWholeFile(t *testing.T) {
	t.Parallel()
	content := bytes.Repeat([]byte("0123456789"), 100) // 1000 bytes
	c := NewFactory(probeTestConfig()).ClientForHost(logServer(t, content).URL)

	got, err := c.FetchCVDLogFile(t.Context(), "cvdabc", "dev-1", "launcher.log")
	require.NoError(t, err)
	assert.Equal(t, content, got)
}

// Over the cap is an error, never a truncated read: a verdict about a launch
// drawn from a prefix of its log is a verdict about bytes nobody read.
func TestFetchCVDLogFileRefusesAnOversizeLog(t *testing.T) {
	t.Parallel()
	config := probeTestConfig()
	config.MaxResponseSize = 512
	c := NewFactory(config).ClientForHost(logServer(t, bytes.Repeat([]byte("x"), 513)).URL)

	got, err := c.FetchCVDLogFile(t.Context(), "cvdabc", "dev-1", "launcher.log")
	require.Error(t, err)
	assert.Nil(t, got, "an oversize log must not come back partially read")
}

func TestContentRangeTotal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		header string
		want   int64
		ok     bool
	}{
		{"bytes 700-999/1000", 1000, true},
		{"bytes 0-10/11", 11, true},
		{"bytes 0-99/*", 0, false},
		{"", 0, false},
		{"units 0-99/1000", 0, false},
	} {
		got, ok := contentRangeTotal(tc.header)
		if got != tc.want || ok != tc.ok {
			t.Errorf("contentRangeTotal(%q) = (%d, %v), want (%d, %v)", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}
