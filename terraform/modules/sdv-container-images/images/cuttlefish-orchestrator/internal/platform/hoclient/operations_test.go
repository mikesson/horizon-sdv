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
