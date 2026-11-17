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
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// waitTestClient serves scripted per-poll responses for one resource path.
func waitTestClient(t *testing.T, path string, respond func(w http.ResponseWriter, poll int)) *Client {
	t.Helper()
	var polls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		respond(w, int(polls.Add(1)))
	}))
	t.Cleanup(srv.Close)
	return &Client{baseURL: srv.URL, httpClient: srv.Client(), pollInterval: time.Millisecond}
}

func TestWaitHost_ReachesTarget(t *testing.T) {
	t.Parallel()
	c := waitTestClient(t, "/v2/hosts/h1", func(w http.ResponseWriter, poll int) {
		status := "provisioning"
		if poll >= 3 {
			status = StatusRunning
		}
		fmt.Fprintf(w, `{"id":"h1","status":%q}`, status)
	})

	host, err := c.WaitHost(t.Context(), "h1", StatusRunning)
	if err != nil {
		t.Fatalf("WaitHost: %v", err)
	}
	if host.Status != StatusRunning {
		t.Errorf("status = %q, want running", host.Status)
	}
}

func TestWaitHost_FailureStateCarriesBackendError(t *testing.T) {
	t.Parallel()
	c := waitTestClient(t, "/v2/hosts/h1", func(w http.ResponseWriter, poll int) {
		fmt.Fprint(w, `{"id":"h1","status":"error","error_message":"quota exceeded in zone"}`)
	})

	_, err := c.WaitHost(t.Context(), "h1", StatusRunning)
	if err == nil {
		t.Fatal("expected failure-state error")
	}
	for _, want := range []string{`"error"`, `"running"`, "quota exceeded in zone"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	if wf, ok := errors.AsType[*WaitFailedError](err); !ok || wf.State != "error" || wf.Regressed {
		t.Errorf("err = %#v, want a *WaitFailedError for state error", err)
	}
}

func TestWaitHost_StoppedIsNotAFailureWhileStarting(t *testing.T) {
	t.Parallel()
	// `host start` begins from stopped; the wait must ride through it.
	c := waitTestClient(t, "/v2/hosts/h1", func(w http.ResponseWriter, poll int) {
		status := StatusStopped
		if poll >= 2 {
			status = StatusRunning
		}
		fmt.Fprintf(w, `{"id":"h1","status":%q}`, status)
	})

	if _, err := c.WaitHost(t.Context(), "h1", StatusRunning); err != nil {
		t.Fatalf("WaitHost: %v", err)
	}
}

func TestWaitDevice_DeletedTargetAcceptsNotFound(t *testing.T) {
	t.Parallel()
	c := waitTestClient(t, "/v2/devices/d1", func(w http.ResponseWriter, poll int) {
		if poll == 1 {
			fmt.Fprint(w, `{"id":"d1","status":"deleting"}`)
			return
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"title":"Not Found","status":404,"detail":"device not found"}`)
	})

	device, err := c.WaitDevice(t.Context(), "d1", StatusDeleted)
	if err != nil {
		t.Fatalf("WaitDevice: %v (404 must mean deleted)", err)
	}
	if device != nil {
		t.Errorf("device = %+v, want nil for a purged record", device)
	}
}

func TestWaitDevice_NonDeleteTargetSurfaces404(t *testing.T) {
	t.Parallel()
	c := waitTestClient(t, "/v2/devices/d1", func(w http.ResponseWriter, poll int) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"title":"Not Found","status":404}`)
	})

	_, err := c.WaitDevice(t.Context(), "d1", StatusRunning)
	apiErr, ok := errors.AsType[*Error](err)
	if !ok || apiErr.Status != http.StatusNotFound {
		t.Fatalf("err = %v, want *cco.Error with Status 404", err)
	}
}

func TestWaitHost_ToleratesTransientPollFailures(t *testing.T) {
	t.Parallel()
	// A --wait must survive the 503s of a backend rollout.
	c := waitTestClient(t, "/v2/hosts/h1", func(w http.ResponseWriter, poll int) {
		if poll <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, `{"id":"h1","status":%q}`, StatusRunning)
	})

	host, err := c.WaitHost(t.Context(), "h1", StatusRunning)
	if err != nil {
		t.Fatalf("WaitHost: %v (transient 503s must be ridden through)", err)
	}
	if host.Status != StatusRunning {
		t.Errorf("status = %q, want running", host.Status)
	}
}

func TestWaitHost_PersistentPollFailureGivesUp(t *testing.T) {
	t.Parallel()
	c := waitTestClient(t, "/v2/hosts/h1", func(w http.ResponseWriter, poll int) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	_, err := c.WaitHost(t.Context(), "h1", StatusRunning)
	if err == nil || !strings.Contains(err.Error(), "consecutive poll failures") {
		t.Fatalf("err = %v, want bounded give-up after consecutive failures", err)
	}
}

func TestWaitHost_SustainedThrottlingKeepsPolling(t *testing.T) {
	t.Parallel()
	// A 429 streak longer than maxConsecutivePollFailures must not end the
	// wait, because a throttled server is alive.
	c := waitTestClient(t, "/v2/hosts/h1", func(w http.ResponseWriter, poll int) {
		if poll <= maxConsecutivePollFailures+2 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"title":"Too Many Requests","status":429}`)
			return
		}
		fmt.Fprintf(w, `{"id":"h1","status":%q}`, StatusRunning)
	})

	host, err := c.WaitHost(t.Context(), "h1", StatusRunning)
	if err != nil {
		t.Fatalf("WaitHost: %v (sustained 429s must be ridden through)", err)
	}
	if host.Status != StatusRunning {
		t.Errorf("status = %q, want running", host.Status)
	}
}

func TestWaitDevice_Client4xxFailsFast(t *testing.T) {
	t.Parallel()
	// Polling again cannot change a 403.
	var polls atomic.Int64
	c := waitTestClient(t, "/v2/devices/d1", func(w http.ResponseWriter, poll int) {
		polls.Store(int64(poll))
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"title":"Forbidden","status":403}`)
	})

	_, err := c.WaitDevice(t.Context(), "d1", StatusRunning)
	if err == nil {
		t.Fatal("expected an error")
	}
	if polls.Load() != 1 {
		t.Errorf("polls = %d, want 1 (4xx must fail fast)", polls.Load())
	}
}

func TestWaitHost_RegressionToStoppedFails(t *testing.T) {
	t.Parallel()
	// Stopped after the start began means another actor stopped the host.
	// The default unbounded --timeout would otherwise poll forever.
	c := waitTestClient(t, "/v2/hosts/h1", func(w http.ResponseWriter, poll int) {
		status := StatusStopped
		if poll == 2 {
			status = "starting"
		}
		fmt.Fprintf(w, `{"id":"h1","status":%q}`, status)
	})

	_, err := c.WaitHost(t.Context(), "h1", StatusRunning)
	if err == nil || !strings.Contains(err.Error(), "went back") {
		t.Fatalf("err = %v, want a regressed-to-stopped failure", err)
	}
	if wf, ok := errors.AsType[*WaitFailedError](err); !ok || !wf.Regressed {
		t.Errorf("err = %#v, want a regressed *WaitFailedError", err)
	}
}

func TestWaitHost_ContextCancelInterrupts(t *testing.T) {
	t.Parallel()
	c := waitTestClient(t, "/v2/hosts/h1", func(w http.ResponseWriter, poll int) {
		fmt.Fprint(w, `{"id":"h1","status":"provisioning"}`)
	})
	c.pollInterval = time.Hour // Only cancellation can end the wait quickly.

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := c.WaitHost(ctx, "h1", StatusRunning)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled in the chain", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("cancellation must interrupt the poll wait")
	}
}
