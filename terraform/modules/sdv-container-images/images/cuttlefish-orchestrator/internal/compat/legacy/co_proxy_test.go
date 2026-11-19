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

package legacy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/compat/legacy/coapi"
	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// serveReq registers the handler and serves a pre-built request.
func serveReq(h *COHandler, req *http.Request) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	h.Register(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// authedReq builds an authenticated proxy request with the given headers.
func authedReq(t *testing.T, target string, headers map[string]string) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(
		auth.ContextWithPrincipal(t.Context(), &auth.Principal{Email: "user@example.com"}),
		http.MethodGet, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

// hostPointingAt returns a host service whose host resolves to the given fake
// Host Orchestrator server. The host carries this API's provenance, which is
// the scenario every proxy test but the CVD-create guard below is about: a
// legacy client reaching a host it created here.
func hostPointingAt(server *httptest.Server) *fakeHostService {
	return foreignHostPointingAt(server, true)
}

// foreignHostPointingAt is hostPointingAt with the provenance flag under test.
func foreignHostPointingAt(server *httptest.Server, foreign bool) *fakeHostService {
	u, _ := url.Parse(server.URL)
	port, _ := strconv.ParseInt(u.Port(), 10, 64)
	return &fakeHostService{getFn: func(_ context.Context, id string) (*host.Host, error) {
		return &host.Host{
			ID: id, InternalIP: u.Hostname(), HostOrchestratorPort: port,
			ForeignCVDs: foreign,
		}, nil
	}}
}

// fleetPointingAt returns a host service resolving host ID "h<i>" to servers[i],
// so one handler proxies to a fleet rather than to the single server
// hostPointingAt pins every ID to.
func fleetPointingAt(servers []*httptest.Server) *fakeHostService {
	return &fakeHostService{getFn: func(_ context.Context, id string) (*host.Host, error) {
		i, err := strconv.Atoi(strings.TrimPrefix(id, "h"))
		if err != nil || i < 0 || i >= len(servers) {
			return nil, host.ErrNotFound
		}
		u, _ := url.Parse(servers[i].URL)
		port, _ := strconv.ParseInt(u.Port(), 10, 64)
		return &host.Host{
			ID: id, InternalIP: u.Hostname(), HostOrchestratorPort: port,
			ForeignCVDs: true,
		}, nil
	}}
}

// TestProxy_ReusesConnectionsAcrossAFleet pins the idle pool against a fleet
// larger than the per-host pool size. net/http's idle pool is an LRU bounded by
// a fleet-wide total, so a total below the number of hosts one instance proxies
// within IdleConnTimeout evicts exactly the connection the next request needs:
// the second sweep below re-dials every host instead of reusing what the first
// left idle, and at Google-scale /v1 traffic every proxied call pays a fresh
// TCP dial.
//
// The transport is a clone, never the package var: it inherits the production
// pool constants, which is what lets this fail, while the tests running in
// parallel against hoProxyTransport keep an uninstrumented one.
func TestProxy_ReusesConnectionsAcrossAFleet(t *testing.T) {
	t.Parallel()
	const hosts = 16

	servers := make([]*httptest.Server, hosts)
	for i := range servers {
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"ho":"ok"}`)
		}))
	}
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()

	var dials atomic.Int64
	transport := hoProxyTransport.Clone()
	dial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		return dial(ctx, network, addr)
	}
	defer transport.CloseIdleConnections()

	h := newTestHandler(fleetPointingAt(servers), nil)
	h.hoTransport = transport

	for sweep := range 2 {
		for i := range hosts {
			rec := serveReq(h, authedReq(t, fmt.Sprintf("/v1/zones/z/hosts/h%d/cvds", i), nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("sweep %d, host h%d: status = %d, want 200", sweep, i, rec.Code)
			}
		}
	}

	// A connection returns to the idle pool asynchronously after body EOF, so a
	// straggler or two is expected; a thrashing pool re-dials all 16.
	if got := dials.Load(); got > hosts+4 {
		t.Errorf("dials = %d over two sweeps of %d hosts, want at most %d: the idle pool is evicting connections that are about to be needed",
			got, hosts, hosts+4)
	}
}

func TestProxy_ForwardsToHostOrchestrator(t *testing.T) {
	t.Parallel()
	var gotPath, gotMethod, gotQuery string
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotQuery = r.URL.Path, r.Method, r.URL.RawQuery
		_, _ = io.WriteString(w, `{"ho":"ok"}`)
	}))
	defer ho.Close()

	rec := serve(t, newTestHandler(hostPointingAt(ho), nil),
		http.MethodGet, "/v1/zones/z/hosts/h1/cvds?foo=bar", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotPath != "/cvds" {
		t.Errorf("HO path = %q, want /cvds (prefix stripped)", gotPath)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("HO method = %q, want GET", gotMethod)
	}
	if gotQuery != "foo=bar" {
		t.Errorf("HO query = %q, want foo=bar (preserved)", gotQuery)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"ho":"ok"}` {
		t.Errorf("body = %q, want the HO response passed through", got)
	}
}

// TestProxy_TrailingSlashReachesProxy pins the route OmniLab ATS polls as its
// host readiness check. GET .../hosts/{host}/, the catch-all with an empty
// remainder, must reach the Host Orchestrator as its root path. The client
// retries nothing here: a wrong route 404s and the whole create fails on the
// spot. Proven live; this is the in-repo pin.
func TestProxy_TrailingSlashReachesProxy(t *testing.T) {
	t.Parallel()
	var gotPath string
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, "ok")
	}))
	defer ho.Close()

	rec := serve(t, newTestHandler(hostPointingAt(ho), nil),
		http.MethodGet, "/v1/zones/z/hosts/h1/", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 relayed from the HO", rec.Code)
	}
	if gotPath != "/" {
		t.Errorf("HO path = %q, want / (empty remainder forwards as the root)", gotPath)
	}
}

func TestProxy_ForwardsColonVerbAndBody(t *testing.T) {
	t.Parallel()
	var gotPath, gotBody string
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	}))
	defer ho.Close()

	rec := serve(t, newTestHandler(hostPointingAt(ho), nil),
		http.MethodPost, "/v1/zones/z/hosts/h1/cvds/grp/:start", `{"x":1}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotPath != "/cvds/grp/:start" {
		t.Errorf("HO path = %q, want /cvds/grp/:start (colon verb preserved)", gotPath)
	}
	if gotBody != `{"x":1}` {
		t.Errorf("HO body = %q, want the request body forwarded", gotBody)
	}
}

func TestProxy_HostNotFound(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{getFn: func(_ context.Context, _ string) (*host.Host, error) {
		return nil, host.ErrNotFound
	}}
	rec := serve(t, newTestHandler(hosts, nil), http.MethodGet, "/v1/zones/z/hosts/missing/cvds", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestProxy_HostWithoutIP(t *testing.T) {
	t.Parallel()
	hosts := &fakeHostService{getFn: func(_ context.Context, id string) (*host.Host, error) {
		return &host.Host{ID: id}, nil // no IP yet (still provisioning)
	}}
	rec := serve(t, newTestHandler(hosts, nil), http.MethodGet, "/v1/zones/z/hosts/h1/cvds", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestProxy_PreservesEscapedPathSegments(t *testing.T) {
	t.Parallel()
	var gotURI, gotDecoded string
	ho := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotURI, gotDecoded = r.URL.RequestURI(), r.URL.Path
	}))
	defer ho.Close()

	rec := serve(t, newTestHandler(hostPointingAt(ho), nil),
		http.MethodPut, "/v1/zones/z/hosts/h1/userartifacts/my%20image.zip", "bytes")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// The original encoding must survive: assigning chi's still-encoded
	// wildcard to URL.Path used to double-encode it to my%2520image.zip.
	if gotURI != "/userartifacts/my%20image.zip" {
		t.Errorf("HO request URI = %q, want /userartifacts/my%%20image.zip (original encoding preserved)", gotURI)
	}
	if gotDecoded != "/userartifacts/my image.zip" {
		t.Errorf("HO decoded path = %q, want /userartifacts/my image.zip", gotDecoded)
	}
}

// wsUpgradeHeaders returns the headers of a browser WebSocket upgrade request.
func wsUpgradeHeaders(origin string) map[string]string {
	h := map[string]string{
		"Connection": "keep-alive, Upgrade",
		"Upgrade":    "websocket",
	}
	if origin != "" {
		h["Origin"] = origin
	}
	return h
}

func TestProxy_WebSocketSameOriginAllowed(t *testing.T) {
	t.Parallel()
	reachedHO := false
	ho := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reachedHO = true
	}))
	defer ho.Close()

	// httptest requests carry Host: example.com.
	rec := serveReq(newTestHandler(hostPointingAt(ho), nil),
		authedReq(t, "/v1/zones/z/hosts/h1/connect_client", wsUpgradeHeaders("http://example.com")))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a same-origin upgrade", rec.Code)
	}
	if !reachedHO {
		t.Error("same-origin upgrade must be forwarded to the Host Orchestrator")
	}
}

func TestProxy_WebSocketCrossOriginRejected(t *testing.T) {
	t.Parallel()
	ho := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("cross-origin upgrade must never reach the Host Orchestrator")
	}))
	defer ho.Close()

	rec := serveReq(newTestHandler(hostPointingAt(ho), nil),
		authedReq(t, "/v1/zones/z/hosts/h1/connect_client", wsUpgradeHeaders("https://evil.example")))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	var apiErr coapi.Error
	mustDecode(t, rec, &apiErr)
	if apiErr.Code != http.StatusForbidden {
		t.Errorf("error envelope code = %d, want 403", apiErr.Code)
	}
}

func TestProxy_WebSocketNoOriginAllowed(t *testing.T) {
	t.Parallel()
	reachedHO := false
	ho := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reachedHO = true
	}))
	defer ho.Close()

	// Non-browser clients (cvdr, CI) send no Origin header.
	rec := serveReq(newTestHandler(hostPointingAt(ho), nil),
		authedReq(t, "/v1/zones/z/hosts/h1/connect_client", wsUpgradeHeaders("")))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for an origin-less upgrade", rec.Code)
	}
	if !reachedHO {
		t.Error("origin-less upgrade must be forwarded to the Host Orchestrator")
	}
}

func TestProxy_NonUpgradeCrossOriginNotBlocked(t *testing.T) {
	t.Parallel()
	// Plain (non-upgrade) requests are covered by CrossOriginProtection at
	// the router; the proxy's origin check applies to upgrades only.
	reachedHO := false
	ho := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reachedHO = true
	}))
	defer ho.Close()

	rec := serveReq(newTestHandler(hostPointingAt(ho), nil),
		authedReq(t, "/v1/zones/z/hosts/h1/cvds", map[string]string{"Origin": "https://evil.example"}))

	if rec.Code != http.StatusOK || !reachedHO {
		t.Fatalf("status = %d, reachedHO = %t; non-upgrade requests must pass through", rec.Code, reachedHO)
	}
}

func TestProxy_HostUnreachable(t *testing.T) {
	t.Parallel()
	ho := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	hosts := hostPointingAt(ho)
	ho.Close() // nothing is listening now -> connection refused

	rec := serve(t, newTestHandler(hosts, nil), http.MethodGet, "/v1/zones/z/hosts/h1/cvds", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

// timeoutNetError is a net.Error whose Timeout() is configurable, standing in for
// the transport's "timeout awaiting response headers" error.
type timeoutNetError struct{ timeout bool }

func (e timeoutNetError) Error() string   { return "simulated net error" }
func (e timeoutNetError) Timeout() bool   { return e.timeout }
func (e timeoutNetError) Temporary() bool { return false }

func TestProxyErrorStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"net timeout -> 504", timeoutNetError{timeout: true}, http.StatusGatewayTimeout},
		{"wrapped net timeout -> 504", fmt.Errorf("proxy: %w", timeoutNetError{timeout: true}), http.StatusGatewayTimeout},
		{"non-timeout net error -> 502", timeoutNetError{timeout: false}, http.StatusBadGateway},
		{"plain error -> 502", errors.New("connection refused"), http.StatusBadGateway},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := proxyErrorStatus(tc.err); got != tc.want {
				t.Errorf("proxyErrorStatus(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestProxy_SlowHeaderWithinBoundIsRelayed pins the long-poll fix: an HO that
// withholds its response header (as operations/:wait does) is relayed verbatim
// as long as it answers within ResponseHeaderTimeout.
func TestProxy_SlowHeaderWithinBoundIsRelayed(t *testing.T) {
	t.Parallel()
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond) // header withheld, like an HO :wait
		_, _ = io.WriteString(w, `{"ho":"ok"}`)
	}))
	defer ho.Close()

	h := newTestHandler(hostPointingAt(ho), nil)
	h.hoTransport = &http.Transport{ResponseHeaderTimeout: 2 * time.Second}

	rec := serve(t, h, http.MethodPost, "/v1/zones/z/hosts/h1/operations/foo/:wait", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a slow header within the bound must be relayed)", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"ho":"ok"}` {
		t.Errorf("body = %q, want the HO response relayed", got)
	}
}

// TestProxy_HeaderTimeoutIs504 pins that a header timeout maps to 504 (which
// libhoclient/cvdr retry) rather than 502 (which they treat as fatal).
func TestProxy_HeaderTimeoutIs504(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	ho := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release // never send a response header until the test tears down
	}))
	defer ho.Close()
	defer close(release)

	h := newTestHandler(hostPointingAt(ho), nil)
	h.hoTransport = &http.Transport{ResponseHeaderTimeout: 20 * time.Millisecond}

	rec := serve(t, h, http.MethodPost, "/v1/zones/z/hosts/h1/operations/foo/:wait", "")
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504 (a header timeout must map to 504 so clients retry)", rec.Code)
	}
}

// A CVD created on an orchestrator-owned host has no device row, and the
// orphan census, which exempts a host only by provenance, would count it
// unclaimed and, with the sweep enabled, delete a running test's device.
func TestProxy_RefusesCVDCreateOnOrchestratorOwnedHost(t *testing.T) {
	t.Parallel()
	var reached bool
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = io.WriteString(w, `{"name":"op-1"}`)
	}))
	defer ho.Close()

	rec := serve(t, newTestHandler(foreignHostPointingAt(ho, false), nil),
		http.MethodPost, "/v1/zones/z/hosts/h1/cvds", "{}")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	// The refusal has to happen before the forward: a 403 synthesized after
	// the host already booted the group would close nothing.
	if reached {
		t.Error("the request reached the host orchestrator; the guard must refuse before forwarding")
	}
}

func TestProxy_RefusesCVDCreateBypassVariantsOnOrchestratorOwnedHost(t *testing.T) {
	t.Parallel()
	paths := []struct {
		name string
		path string
	}{
		{name: "trailing_double_slash", path: "/v1/zones/z/hosts/h1/cvds//"},
		{name: "leading_double_slash", path: "/v1/zones/z/hosts/h1//cvds"},
		{name: "dot_segment", path: "/v1/zones/z/hosts/h1/./cvds"},
		{name: "dotdot_traversal", path: "/v1/zones/z/hosts/h1/foo/../cvds"},
	}

	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var reached bool
			ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				_, _ = io.WriteString(w, `{"name":"op-1"}`)
			}))
			defer ho.Close()

			rec := serve(t, newTestHandler(foreignHostPointingAt(ho, false), nil),
				http.MethodPost, tc.path, "{}")

			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s: status = %d, want 403", tc.path, rec.Code)
			}
			if reached {
				t.Errorf("%s: request reached host orchestrator", tc.path)
			}
		})
	}
}

func TestProxy_AllowsCVDCreateOnForeignHost(t *testing.T) {
	t.Parallel()
	var gotPath string
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"name":"op-1"}`)
	}))
	defer ho.Close()

	rec := serve(t, newTestHandler(hostPointingAt(ho), nil),
		http.MethodPost, "/v1/zones/z/hosts/h1/cvds", "{}")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotPath != "/cvds" {
		t.Errorf("host orchestrator saw %q, want the create forwarded to /cvds", gotPath)
	}
}

// Only the creating verb is refused. Everything else addresses a CVD that
// already exists, and the live smoke depends on both shapes below against an
// orchestrator-owned host: it reads boot logs through this proxy, and its
// drift check starts a CVD behind CCO's back on purpose.
func TestProxy_AllowsNonCreateVerbsOnOrchestratorOwnedHost(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, method, path, wantPath string
	}{
		{"boot logs", http.MethodGet, "/cvds/grp/ins/logs/kernel.log", "/cvds/grp/ins/logs/kernel.log"},
		{"instance start", http.MethodPost, "/cvds/grp/ins/:start", "/cvds/grp/ins/:start"},
		{"group listing", http.MethodGet, "/cvds", "/cvds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var gotPath string
			ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				_, _ = io.WriteString(w, `{"ho":"ok"}`)
			}))
			defer ho.Close()

			rec := serve(t, newTestHandler(foreignHostPointingAt(ho, false), nil),
				tc.method, "/v1/zones/z/hosts/h1"+tc.path, "")

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if gotPath != tc.wantPath {
				t.Errorf("host orchestrator saw %q, want %q", gotPath, tc.wantPath)
			}
		})
	}
}

func TestProxy_WebSocketNullAndDuplicateOriginRejected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		host    string
		origins []string
	}{
		{name: "null_origin", host: "example.com", origins: []string{"null"}},
		{name: "null_origin_empty_host", host: "", origins: []string{"null"}},
		{name: "hostless_origin", host: "example.com", origins: []string{"about:blank"}},
		{name: "duplicate_origin", host: "example.com", origins: []string{"http://example.com", "https://evil.example"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ho := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("invalid or duplicate origin upgrade must never reach the Host Orchestrator")
			}))
			defer ho.Close()

			req := authedReq(t, "/v1/zones/z/hosts/h1/connect_client", wsUpgradeHeaders(""))
			req.Host = tc.host
			for _, o := range tc.origins {
				req.Header.Add("Origin", o)
			}
			rec := serveReq(newTestHandler(hostPointingAt(ho), nil), req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
		})
	}
}
