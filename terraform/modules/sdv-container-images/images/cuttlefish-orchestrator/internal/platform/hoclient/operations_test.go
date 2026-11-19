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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingHO serves an empty display listing and remembers every path it was
// asked for.
type recordingHO struct {
	mu    sync.Mutex
	paths []string
}

func (h *recordingHO) served() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.paths...)
}

func newRecordingHO(t *testing.T) (*recordingHO, Client) {
	t.Helper()
	rec := &recordingHO{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.paths = append(rec.paths, r.URL.Path)
		rec.mu.Unlock()
		w.Write([]byte(`{"displays": []}`))
	}))
	t.Cleanup(srv.Close)
	return rec, NewFactory(probeTestConfig()).ClientForHost(srv.URL)
}

// A cvd id naming no group addresses no instance. The host names instances
// itself (a caller supplies them in advanced mode), so there is nothing to
// default to, and a defaulted request lists the displays of whichever instance
// happens to answer to the guess.
func TestGetDisplaysRefusesACVDIDThatNamesNoInstance(t *testing.T) {
	t.Parallel()
	rec, client := newRecordingHO(t)

	_, err := client.GetDisplays(t.Context(), "cvddeadbeef")

	require.Error(t, err)
	assert.Empty(t, rec.served(), "an unaddressable cvd id must be refused before the host is dialed")
	var statusErr interface{ GetStatus() int }
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, http.StatusNotFound, statusErr.GetStatus())

	// The addressable form still reaches the instance's own route.
	_, err = client.GetDisplays(t.Context(), "cvddeadbeef/alpha")
	require.NoError(t, err)
	assert.Equal(t, []string{"/cvds/cvddeadbeef/alpha/displays"}, rec.served())
}

func newRawPathHO(t *testing.T) (*recordingHO, Client) {
	t.Helper()
	rec := &recordingHO{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.paths = append(rec.paths, r.RequestURI)
		rec.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/messages"):
			w.Write([]byte(`[{"type":"offer"}]`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/:bugreport"):
			w.Write([]byte(`{"name":"op-bug"}`))
		case r.URL.Path == "/operations/op-bug/:wait":
			w.Write([]byte(`"bug-1"`))
		case r.URL.Path == "/operations/op-1/:wait":
			w.Write([]byte(`{"snapshot_id":"snap-1","screenshot_bytes":"cHg=","screenshot_mime_type":"image/png"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/displays"):
			w.Write([]byte(`{"display_number":1}`))
		default:
			w.Write([]byte(`{"name":"op-1","displays":[],"screen_recordings":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return rec, NewFactory(probeTestConfig()).ClientForHost(srv.URL)
}

func TestPathEscapeInSignalingAndOperations(t *testing.T) {
	t.Parallel()

	t.Run("signaling_conn_id_escapes_fragment_and_traversal", func(t *testing.T) {
		t.Parallel()
		rec, client := newRawPathHO(t)

		require.NoError(t, client.ForwardSignalingMessage(t.Context(), "a#frag", map[string]any{"sdp": "x"}))
		require.NoError(t, client.ForwardSignalingMessage(t.Context(), "../../cvds", map[string]any{"sdp": "x"}))
		_, err := client.GetSignalingMessages(t.Context(), "a#frag", 0)
		require.NoError(t, err)
		_, err = client.GetSignalingMessages(t.Context(), "../../cvds", 3)
		require.NoError(t, err)

		assert.Equal(t, []string{
			"/polled_connections/a%23frag/:forward",
			"/polled_connections/..%2F..%2Fcvds/:forward",
			"/polled_connections/a%23frag/messages?start=0",
			"/polled_connections/..%2F..%2Fcvds/messages?start=3",
		}, rec.served())
	})

	t.Run("cvd_and_display_paths_escape_dynamic_segments", func(t *testing.T) {
		t.Parallel()
		rec, client := newRawPathHO(t)

		require.NoError(t, client.DeleteCVD(t.Context(), "grp#1"))
		require.NoError(t, client.StartCVD(t.Context(), "grp#1", "inst/1"))
		require.NoError(t, client.StopCVDGroup(t.Context(), "grp#1"))
		_, err := client.GetDisplays(t.Context(), "grp#1/inst#2")
		require.NoError(t, err)
		_, err = client.AddDisplay(t.Context(), "grp", "inst#2", &DisplayAddRequest{Width: 720, Height: 1280})
		require.NoError(t, err)
		require.NoError(t, client.RemoveDisplay(t.Context(), "grp", "inst#2", 1))
		_, err = client.CreateSnapshot(t.Context(), "grp#1", "inst#2", "s1")
		require.NoError(t, err)
		_, err = client.CreateBugreport(t.Context(), "grp#1", false)
		require.NoError(t, err)
		_, err = client.ListScreenRecordings(t.Context(), "grp#1", "inst#2")
		require.NoError(t, err)
		_, err = client.GetDisplayScreenshot(t.Context(), "grp#1", "inst#2", 0)
		require.NoError(t, err)

		paths := rec.served()
		assert.Contains(t, paths, "/cvds/grp%231")
		assert.Contains(t, paths, "/cvds/grp%231/inst%2F1/:start")
		assert.Contains(t, paths, "/cvds/grp%231/:stop")
		assert.Contains(t, paths, "/cvds/grp%231/inst%232/displays")
		assert.Contains(t, paths, "/cvds/grp/inst%232/displays")
		assert.Contains(t, paths, "/cvds/grp/inst%232/displays/1")
		assert.Contains(t, paths, "/cvds/grp%231/inst%232/snapshots")
		assert.Contains(t, paths, "/cvds/grp%231/:bugreport")
		assert.Contains(t, paths, "/cvds/grp%231/inst%232/screen_recordings")
		assert.Contains(t, paths, "/cvds/grp%231/inst%232/displays/0/:screenshot")
	})
}
