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
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/auth/authtest"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
)

// An explicit --timeout replaces the per-route bounds. Without one, each route
// class gets its default bound.
func TestNewClientTimeoutRule(t *testing.T) {
	t.Parallel()
	keyPath, _ := authtest.WriteKeyFile(t)
	cfg := &config.Config{CredentialsPath: keyPath}

	c, err := NewClient(t.Context(), "http://server.example", cfg, 0)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.requestBound != 30*time.Second || c.slowRequestBound != 300*time.Second {
		t.Errorf("default bounds = %v/%v, want 30s/5m0s", c.requestBound, c.slowRequestBound)
	}
	// A single http.Client timeout would apply one bound to every route class.
	if c.httpClient.Timeout != 0 {
		t.Errorf("httpClient.Timeout = %v, want 0 (bounds are per route class)", c.httpClient.Timeout)
	}

	c, err = NewClient(t.Context(), "http://server.example", cfg, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewClient with a command timeout: %v", err)
	}
	if c.requestBound != 0 || c.slowRequestBound != 0 {
		t.Errorf("bounds under an explicit command timeout = %v/%v, want 0/0 (the command context governs)", c.requestBound, c.slowRequestBound)
	}
}

// The client's slow class must match the server's hostOperationRequest
// classification, whose handlers run up to 290s.
func TestSlowOperationPathMirrorsServerClasses(t *testing.T) {
	t.Parallel()
	slow := []string{
		"/v2/devices/d1/start",
		"/v2/devices/d1/stop",
		"/v2/devices/d1/restart",
		"/v2/devices/d1/powerwash",
		"/v2/devices/d1/powerbtn",
		"/v2/devices/d1/bugreport",
		"/v2/devices/d1/recordings/start",
		"/v2/devices/d1/recordings/clip.webm/fetch",
	}
	ordinary := []string{
		"/v2/devices",
		"/v2/devices/d1",
		"/v2/devices/d1/recordings", // The list is a quick host query.
		"/v2/devices/d1/snapshots",  // Async verb that returns an operation.
		"/v2/devices/d1/restore",    // Async verb that returns an operation.
		"/v2/devices/d1/displays/0/screenshot",
		"/v2/hosts/h1/start", // Host verbs answer quickly; GCE work is async.
		"/v2/builds?limit=100",
	}
	for _, p := range slow {
		if !slowOperationPath(p) {
			t.Errorf("slowOperationPath(%q) = false, want true", p)
		}
	}
	for _, p := range ordinary {
		if slowOperationPath(p) {
			t.Errorf("slowOperationPath(%q) = true, want false", p)
		}
	}
}

// A host-operation call that outlasts the ordinary bound succeeds, and an
// ordinary call of the same duration times out.
func TestRequestBoundsPerRouteClass(t *testing.T) {
	t.Parallel()
	const serverDelay = 200 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(serverDelay)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"gs_uri":"gs://b/o"}`)
	}))
	t.Cleanup(srv.Close)

	c := &Client{
		baseURL:          srv.URL,
		httpClient:       srv.Client(),
		requestBound:     50 * time.Millisecond,
		slowRequestBound: time.Second,
	}

	if _, err := c.BugreportDevice(t.Context(), "d1", false); err != nil {
		t.Errorf("bugreport (slow class) under a slow server: %v, want success", err)
	}
	if _, err := c.GetDevice(t.Context(), "d1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("get (ordinary class) under a slow server: %v, want context.DeadlineExceeded", err)
	}
}

// A storage download whose body outlasts every request bound must stream to
// completion.
func TestDownloadStorageObjectOutlastsRequestBound(t *testing.T) {
	t.Parallel()
	const chunk = "0123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		for range 4 {
			fmt.Fprint(w, chunk)
			fl.Flush()
			time.Sleep(30 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)

	c := &Client{
		baseURL:      srv.URL,
		httpClient:   srv.Client(),
		streamClient: srv.Client(),
		requestBound: 50 * time.Millisecond, // The 120ms body must outlive it.
	}

	var buf strings.Builder
	if err := c.DownloadStorageObject(t.Context(), "b", "dir/o.zip", &buf); err != nil {
		t.Fatalf("DownloadStorageObject: %v", err)
	}
	if got, want := buf.String(), strings.Repeat(chunk, 4); got != want {
		t.Errorf("downloaded %q, want %q", got, want)
	}
}

// recordingServer captures every request the retrying client sends so tests
// can assert on attempt counts, replayed bodies and the keys they carried.
type recordingServer struct {
	*httptest.Server

	mu     sync.Mutex
	bodies []string
	keys   []string
}

// newRecordingServer starts a server that answers each attempt with respond,
// called with the one-based attempt number.
func newRecordingServer(t *testing.T, respond func(w http.ResponseWriter, attempt int)) *recordingServer {
	t.Helper()

	rs := &recordingServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		rs.mu.Lock()
		rs.bodies = append(rs.bodies, string(body))
		rs.keys = append(rs.keys, r.Header.Get("Idempotency-Key"))
		attempt := len(rs.bodies)
		rs.mu.Unlock()
		respond(w, attempt)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *recordingServer) attempts() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return len(rs.bodies)
}

func (rs *recordingServer) body(attempt int) string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.bodies[attempt-1]
}

// key returns the Idempotency-Key the one-based attempt carried ("" if none).
func (rs *recordingServer) key(attempt int) string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.keys[attempt-1]
}

// newTestClient builds a Client against srv with fast retries.
func newTestClient(srv *recordingServer, backoffBase time.Duration) *Client {
	return &Client{
		baseURL: srv.URL,
		httpClient: &http.Client{
			Transport: &retryRoundTripper{
				next:        http.DefaultTransport,
				maxRetries:  3,
				backoffBase: backoffBase,
			},
		},
	}
}

func TestRetry429_ResendsBodyIntact(t *testing.T) {
	t.Parallel()
	// A POST retried after a 429 must carry the original body.
	srv := newRecordingServer(t, func(w http.ResponseWriter, attempt int) {
		if attempt == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"id":"d1"}`)
	})
	c := newTestClient(srv, time.Millisecond)

	var out struct {
		ID string `json:"id"`
	}
	err := c.do(t.Context(), http.MethodPost, "/v2/devices", map[string]string{"name": "d1"}, &out)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if out.ID != "d1" {
		t.Errorf("response id = %q, want %q", out.ID, "d1")
	}
	if got := srv.attempts(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	const wantBody = `{"name":"d1"}`
	if srv.body(1) != wantBody {
		t.Errorf("first attempt body = %q, want %q", srv.body(1), wantBody)
	}
	if srv.body(2) != wantBody {
		t.Errorf("retried body = %q, want %q (body must be rewound before retry)", srv.body(2), wantBody)
	}
}

func TestRetry429_HTTPDateRetryAfter(t *testing.T) {
	t.Parallel()
	srv := newRecordingServer(t, func(w http.ResponseWriter, attempt int) {
		if attempt == 1 {
			// An HTTP-date in the past means "retry immediately".
			w.Header().Set("Retry-After", time.Now().Add(-time.Second).UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	c := newTestClient(srv, time.Millisecond)

	if err := c.do(t.Context(), http.MethodGet, "/health", nil, nil); err != nil {
		t.Fatalf("do: %v", err)
	}
	if got := srv.attempts(); got != 2 {
		t.Errorf("attempts = %d, want 2 (HTTP-date Retry-After must trigger a retry)", got)
	}
}

func TestRetry429_UnparseableRetryAfter(t *testing.T) {
	t.Parallel()
	t.Run("idempotent request retries with backoff", func(t *testing.T) {
		srv := newRecordingServer(t, func(w http.ResponseWriter, attempt int) {
			if attempt == 1 {
				w.Header().Set("Retry-After", "not-a-date")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			fmt.Fprint(w, `{}`)
		})
		c := newTestClient(srv, time.Millisecond)

		if err := c.do(t.Context(), http.MethodGet, "/health", nil, nil); err != nil {
			t.Fatalf("do: %v", err)
		}
		if got := srv.attempts(); got != 2 {
			t.Errorf("attempts = %d, want 2", got)
		}
	})

	t.Run("non-idempotent request is not retried", func(t *testing.T) {
		srv := newRecordingServer(t, func(w http.ResponseWriter, attempt int) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
		c := newTestClient(srv, time.Millisecond)

		err := c.do(t.Context(), http.MethodPost, "/v2/devices", map[string]string{"name": "d1"}, nil)
		if err == nil {
			t.Fatal("expected an error for an unretried 429")
		}
		if got := srv.attempts(); got != 1 {
			t.Errorf("attempts = %d, want 1 (POST without usable Retry-After must not retry)", got)
		}
	})
}

func TestRetry_TransientServerErrorsOnGET(t *testing.T) {
	t.Parallel()
	srv := newRecordingServer(t, func(w http.ResponseWriter, attempt int) {
		switch attempt {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			fmt.Fprint(w, `{"status":"ok"}`)
		}
	})
	c := newTestClient(srv, time.Millisecond)

	var out struct {
		Status string `json:"status"`
	}
	if err := c.do(t.Context(), http.MethodGet, "/health", nil, &out); err != nil {
		t.Fatalf("do: %v", err)
	}
	if out.Status != "ok" {
		t.Errorf("status = %q, want %q", out.Status, "ok")
	}
	if got := srv.attempts(); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

func TestRetry_POSTNotRetriedOn502(t *testing.T) {
	t.Parallel()
	srv := newRecordingServer(t, func(w http.ResponseWriter, attempt int) {
		w.WriteHeader(http.StatusBadGateway)
	})
	c := newTestClient(srv, time.Millisecond)

	err := c.do(t.Context(), http.MethodPost, "/v2/devices", map[string]string{"name": "d1"}, nil)
	if err == nil {
		t.Fatal("expected an error for a 502")
	}
	if got := srv.attempts(); got != 1 {
		t.Errorf("attempts = %d, want 1 (non-idempotent requests must not retry on 5xx)", got)
	}
}

func TestRetry_KeyedPOSTRetriedOn502(t *testing.T) {
	t.Parallel()
	// The server dedupes on the Idempotency-Key, so a keyed POST retries like
	// a GET and replays its body.
	srv := newRecordingServer(t, func(w http.ResponseWriter, attempt int) {
		if attempt == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"batch_id":"b1","status":"provisioning"}`)
	})
	c := newTestClient(srv, time.Millisecond)

	var out struct {
		BatchID string `json:"batch_id"`
	}
	err := c.doWithHeaders(t.Context(), http.MethodPost, "/v2/batches",
		map[string]string{"name_prefix": "ci"}, &out, map[string]string{"Idempotency-Key": "k-1"})
	if err != nil {
		t.Fatalf("keyed POST: %v (must retry through the 502)", err)
	}
	if got := srv.attempts(); got != 2 {
		t.Errorf("attempts = %d, want 2 (keyed POST retries)", got)
	}
	if out.BatchID != "b1" {
		t.Errorf("batch_id = %q, want b1", out.BatchID)
	}
	if b := srv.body(2); !strings.Contains(b, "ci") {
		t.Errorf("retried body = %q, want the original payload replayed", b)
	}
}

func TestRetry_NonReplayableBodyNotRetried(t *testing.T) {
	t.Parallel()
	// A request built from a plain io.Reader has no GetBody. A retry would
	// send an empty body, so even a 429 with Retry-After is not retried.
	srv := newRecordingServer(t, func(w http.ResponseWriter, attempt int) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	rt := &retryRoundTripper{next: http.DefaultTransport, maxRetries: 3, backoffBase: time.Millisecond}

	body := struct{ io.Reader }{strings.NewReader(`{"name":"d1"}`)}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, body)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	if req.GetBody != nil {
		t.Fatal("test setup: expected a request without GetBody")
	}

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429 returned to the caller", resp.StatusCode)
	}
	if got := srv.attempts(); got != 1 {
		t.Errorf("attempts = %d, want 1 (non-replayable body must not be retried)", got)
	}
}

func TestDo_DecodesProblemDetailErrors(t *testing.T) {
	t.Parallel()
	srv := newRecordingServer(t, func(w http.ResponseWriter, attempt int) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{
			"title": "Unprocessable Entity",
			"status": 422,
			"detail": "validation failed",
			"errors": [
				{"message": "expected length >= 1", "location": "body.name", "value": ""},
				{"message": "required", "location": "body.host_id"}
			]
		}`)
	})
	c := newTestClient(srv, time.Millisecond)

	err := c.do(t.Context(), http.MethodPost, "/v2/devices", map[string]string{"name": ""}, nil)
	if err == nil {
		t.Fatal("expected an error for a 422")
	}
	for _, want := range []string{
		"Unprocessable Entity: validation failed",
		"body.name: expected length >= 1",
		"body.host_id: required",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	}
}

func TestRetry_RetryAfterBeyondDeadlineReturnsResponse(t *testing.T) {
	t.Parallel()
	// A Retry-After longer than the remaining context budget returns the 429
	// and its body at once, so the server's error details are kept.
	srv := newRecordingServer(t, func(w http.ResponseWriter, attempt int) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	rt := &retryRoundTripper{next: http.DefaultTransport, maxRetries: 3, backoffBase: time.Millisecond}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}

	start := time.Now()
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v (must return the 429, not a timeout error)", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", resp.StatusCode)
	}
	if got := srv.attempts(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v; must not sleep toward an unreachable retry", elapsed)
	}
}

func TestListAll_FollowsPageTokens(t *testing.T) {
	t.Parallel()
	// The backend caps limit at 100, so listAll must follow next_page_token
	// until the server returns an empty one.
	const total = 250
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit != 100 {
			t.Errorf("limit = %d, want 100 (backend maximum)", limit)
		}
		if q.Has("offset") {
			t.Errorf("listAll sent offset=%s; a token walk must not page by offset", q.Get("offset"))
		}
		start := 0
		if tok := q.Get("page_token"); tok != "" {
			start, _ = strconv.Atoi(strings.TrimPrefix(tok, "after-"))
		}
		count := min(limit, total-start)
		items := make([]string, 0, count)
		for i := range count {
			items = append(items, fmt.Sprintf(`{"id":"h%d"}`, start+i))
		}
		next := ""
		if start+count < total {
			next = fmt.Sprintf("after-%d", start+count)
		}
		fmt.Fprintf(w, `{"items":[%s],"next_page_token":%q}`, strings.Join(items, ","), next)
	}))
	t.Cleanup(srv.Close)
	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}

	devices, err := listAll[Device](t.Context(), c, "/v2/devices", map[string][]string{})
	if err != nil {
		t.Fatalf("listAll: %v", err)
	}
	if len(devices) != total {
		t.Fatalf("got %d items, want %d (must follow the token past the first 100)", len(devices), total)
	}
	if devices[249].ID != "h249" {
		t.Errorf("last item = %q, want h249 (pages must stay ordered)", devices[249].ID)
	}
}

// A host deleted between pages must not push an unseen host past the walk.
// The fake answers both an offset walk and a token walk, so an offset walk
// returns one host fewer here.
func TestListHosts_DeleteBetweenPagesSkipsNoHost(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	rows := make([]string, 150)
	for i := range rows {
		rows[i] = fmt.Sprintf("h%03d", i)
	}
	const deleted = "h010"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		start, _ := strconv.Atoi(q.Get("offset"))
		if tok := q.Get("page_token"); tok != "" {
			// The token names the last row served; the page starts after it.
			start = 0
			for start < len(rows) && rows[start] <= strings.TrimPrefix(tok, "after-") {
				start++
			}
		}
		page := rows[min(start, len(rows)):min(start+limit, len(rows))]
		items := make([]string, len(page))
		for i, id := range page {
			items[i] = fmt.Sprintf(`{"id":%q}`, id)
		}
		next := ""
		if start+len(page) < len(rows) {
			next = "after-" + page[len(page)-1]
		}
		fmt.Fprintf(w, `{"items":[%s],"next_page_token":%q}`, strings.Join(items, ","), next)
		if start == 0 {
			// A host on the first page is deleted before the second request.
			rows = slices.DeleteFunc(rows, func(id string) bool { return id == deleted })
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}

	hosts, err := c.ListHosts(t.Context(), HostListOptions{})
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	seen := map[string]int{}
	for _, h := range hosts {
		seen[h.ID]++
	}
	for i := range 150 {
		id := fmt.Sprintf("h%03d", i)
		if id == deleted {
			continue
		}
		if seen[id] != 1 {
			t.Errorf("host %s listed %d times, want once: it existed throughout the walk", id, seen[id])
		}
	}
}

// Machine types are unpaged: one request returns the whole catalog, and a
// catalog over 100 entries must not loop.
func TestListMachineTypes_OneRequest(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		items := make([]string, 120)
		for i := range items {
			items[i] = fmt.Sprintf(`{"id":"mt-%d"}`, i)
		}
		fmt.Fprintf(w, `{"items":[%s]}`, strings.Join(items, ","))
	}))
	t.Cleanup(srv.Close)
	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}

	mts, err := c.ListMachineTypes(t.Context(), MachineTypeListOptions{})
	if err != nil {
		t.Fatalf("ListMachineTypes: %v", err)
	}
	if len(mts) != 120 || calls.Load() != 1 {
		t.Errorf("got %d machine types over %d requests, want 120 over 1", len(mts), calls.Load())
	}
}

// storage ls walks every page of an object listing.
func TestListStorageObjects_FollowsPageTokens(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("page_size") == "" {
			t.Errorf("listing sent no page_size, so the server returns everything at once")
		}
		if q.Get("prefix") != "dir/" {
			t.Errorf("prefix = %q, want dir/", q.Get("prefix"))
		}
		switch q.Get("page_token") {
		case "":
			fmt.Fprint(w, `{"objects":[{"name":"dir/a"}],"prefixes":["dir/sub/"],"next_page_token":"p2"}`)
		case "p2":
			fmt.Fprint(w, `{"objects":[{"name":"dir/b"}]}`)
		default:
			t.Errorf("unexpected page_token %q", q.Get("page_token"))
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}

	resp, err := c.ListStorageObjects(t.Context(), "bkt", "dir/")
	if err != nil {
		t.Fatalf("ListStorageObjects: %v", err)
	}
	if len(resp.Objects) != 2 || resp.Objects[1].Name != "dir/b" {
		t.Errorf("objects = %+v, want dir/a then dir/b", resp.Objects)
	}
	if len(resp.Prefixes) != 1 || resp.NextPageToken != "" {
		t.Errorf("prefixes = %v, token = %q; want one prefix and no token", resp.Prefixes, resp.NextPageToken)
	}
}

// A pool name is one path segment.
func TestDisableFleetPool_EscapesThePool(t *testing.T) {
	t.Parallel()
	var gotPath atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.EscapedPath())
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}

	if _, _, err := c.DisableFleetPool(t.Context(), "", "a/b?c"); err != nil {
		t.Fatalf("DisableFleetPool: %v", err)
	}
	if want := "/v2/admin/system-config/fleet/pools/a%2Fb%3Fc"; gotPath.Load() != want {
		t.Errorf("path = %v, want %q", gotPath.Load(), want)
	}
}

// A conditional write sends If-Match, returns the new tag, and turns a 412
// into an error that says to re-run.
func TestConfigWrite_IfMatch(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("If-Match") {
		case `"fresh"`:
			w.Header().Set("ETag", `"next"`)
			fmt.Fprint(w, `{"device_ttl_hours":2}`)
		default:
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusPreconditionFailed)
			fmt.Fprint(w, `{"title":"Precondition Failed","status":412}`)
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}

	stored, etag, err := c.UpdateLifetimes(t.Context(), `"fresh"`, UpdateLifetimesRequest{DeviceTTLHours: 2})
	if err != nil {
		t.Fatalf("UpdateLifetimes: %v", err)
	}
	if stored.DeviceTTLHours != 2 {
		t.Errorf("stored device_ttl_hours = %d, want the echoed 2", stored.DeviceTTLHours)
	}
	if etag != `"next"` {
		t.Errorf("etag = %q, want the response's ETag", etag)
	}

	_, _, err = c.UpdateLifetimes(t.Context(), `"stale"`, UpdateLifetimesRequest{DeviceTTLHours: 2})
	apiErr, ok := errors.AsType[*Error](err)
	if !ok || apiErr.Status != http.StatusPreconditionFailed {
		t.Fatalf("err = %v, want a 412 *Error", err)
	}
	if !strings.Contains(err.Error(), "changed since it was read") || !strings.Contains(err.Error(), "again") {
		t.Errorf("412 message = %q, want it to say the setting changed and to re-run", err)
	}
}

func TestRetry_ContextCanceledDuringBackoff(t *testing.T) {
	t.Parallel()
	firstRequest := make(chan struct{})
	var once sync.Once
	srv := newRecordingServer(t, func(w http.ResponseWriter, attempt int) {
		once.Do(func() { close(firstRequest) })
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	// The test only finishes quickly if cancellation interrupts the backoff.
	c := newTestClient(srv, time.Hour)

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-firstRequest
		cancel()
	}()

	start := time.Now()
	err := c.do(ctx, http.MethodGet, "/health", nil, nil)
	if err == nil {
		t.Fatal("expected an error after cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled in the chain", err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("took %v; cancellation must interrupt the backoff wait", elapsed)
	}
}
