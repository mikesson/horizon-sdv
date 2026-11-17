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
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// hoProxyResponseHeaderTimeout bounds how long the proxy waits for the Host
// Orchestrator's first response header. It MUST exceed the HO's operation
// long-poll window (upstream WaitOperationDuration = 2m): POST
// .../operations/{op}/:wait withholds every response header until the
// operation is done or that window elapses, so a shorter bound would abort
// each in-flight host-orchestrator operation (e.g. cvdr's CVD create, which
// the upstream client retries on 503/504 but treats 502 as fatal). Streamed
// responses (logs, artifacts, WebSocket 101) send headers immediately and are
// unaffected, since this bounds only time-to-first-header, and a hung
// HO is still cut off and mapped to 504.
const hoProxyResponseHeaderTimeout = 3 * time.Minute

// hoProxyMaxIdleConns bounds the idle pool across the WHOLE fleet, where
// MaxIdleConnsPerHost bounds it per host. The pool is an LRU: a total below the
// number of hosts one instance proxies to within IdleConnTimeout evicts exactly
// the connection the next request needs, so cyclic access across the fleet
// makes every proxied call pay a fresh TCP dial. 1024 sits above any host
// working set one instance serves while still bounding the file descriptors and
// reader goroutines it can hold idle.
const hoProxyMaxIdleConns = 1024

// hoProxyTransport is shared by every Host Orchestrator proxy request so the
// connection pool is reused across calls. There is no overall
// request timeout, so streamed bodies (logs, artifacts, WebSockets) keep
// flowing; hoProxyResponseHeaderTimeout is the only response bound. Values
// otherwise mirror hoclient's transport.
var hoProxyTransport = &http.Transport{
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	MaxIdleConns:          hoProxyMaxIdleConns,
	MaxIdleConnsPerHost:   10,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: hoProxyResponseHeaderTimeout,
	ExpectContinueTimeout: 1 * time.Second,
}

// proxyToHost serves /v1/zones/{zone}/hosts/{host}/* by reverse-proxying the
// request to the target host's on-VM Host Orchestrator verbatim, exactly as the
// legacy CO's ForwardToHost did. This exposes the entire Host Orchestrator API
// (cvds, operations, userartifacts, displays, snapshots, screen recordings, the
// connect_client WebSocket, ...) byte-for-byte without re-implementing any of it.
// The {host} segment is the CCO host ID returned by create/list.
func (h *COHandler) proxyToHost(w http.ResponseWriter, r *http.Request) {
	// The WebSocket passthrough is cookie-authenticated (ambient IAP session)
	// yet exempt from net/http's CrossOriginProtection, which skips GET
	// upgrades. Enforce a same-host Origin check here; non-browser clients
	// (cvdr, CI) send no Origin and pass.
	if isWebSocketUpgrade(r) && !sameHostOrigin(r) {
		h.writeError(w, http.StatusForbidden, "cross-origin websocket request rejected")
		return
	}

	hostID := chi.URLParam(r, "host")

	hostVM, err := h.hosts.Get(r.Context(), hostID)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}

	rawTarget := hostVM.HostOrchestratorURL()
	if rawTarget == "" {
		h.writeError(w, http.StatusServiceUnavailable, "host orchestrator not yet available")
		return
	}
	target, err := url.Parse(rawTarget)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "invalid host orchestrator url")
		return
	}

	// Everything after .../hosts/{host}/ is the Host Orchestrator path, forwarded
	// unchanged (colon verbs like :start and the query string included). The HTTP
	// reverse proxy transparently relays WebSocket upgrades and streamed bodies.
	//
	// chi routes on the raw (still-encoded) path, so the wildcard param keeps
	// its percent-escapes. URL.Path must carry the DECODED form with RawPath
	// holding the original encoding. Assigning the encoded tail to Path would
	// double-encode escaped segments on the wire (my%20image.zip would reach
	// the HO as my%2520image.zip).
	encodedTail := "/" + chi.URLParam(r, "*")
	decodedTail, err := url.PathUnescape(encodedTail)
	if err != nil {
		// Unreachable for a tail taken from a parsed request URL; forward the
		// raw form rather than failing the request.
		decodedTail = encodedTail
	}

	// A CVD created here would carry no CCO device row, and the orphan census
	// exempts a host only by provenance (devicecontroller skips ForeignCVDs
	// hosts). On a CCO-owned host the census would therefore count the new CVD
	// unclaimed and, with the sweep enabled, delete a running test's device ten
	// minutes later. Refusing the one creating verb is the whole fix: reads,
	// logs and verbs against CVDs that already have rows keep proxying, which
	// is how the live smoke reaches a CCO host's logs and how its drift check
	// starts a CVD behind CCO's back on purpose.
	if isCVDCreate(r.Method, decodedTail) && !hostVM.ForeignCVDs {
		h.writeError(w, http.StatusForbidden,
			"cannot create CVDs on an orchestrator-owned host through /v1")
		return
	}

	proxy := &httputil.ReverseProxy{
		Transport: h.hoTransport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = decodedTail
			pr.Out.URL.RawPath = ""
			if decodedTail != encodedTail {
				pr.Out.URL.RawPath = encodedTail
			}
		},
		ErrorHandler: func(rw http.ResponseWriter, _ *http.Request, perr error) {
			status := proxyErrorStatus(perr)
			h.logger.Warn("legacy HO proxy error",
				slog.String("host", hostID),
				slog.Int("status", status),
				slog.Any("error", perr))
			msg := "host orchestrator unreachable"
			if status == http.StatusGatewayTimeout {
				msg = "host orchestrator timed out"
			}
			h.writeError(rw, status, msg)
		},
	}
	proxy.ServeHTTP(w, r)
}

// isCVDCreate reports whether a proxied request is the Host Orchestrator's
// group-create route, POST /cvds. Every other CVD route addresses a group or
// instance that already exists, so this is the only verb that can leave a CVD
// on a host with no device row behind it.
func isCVDCreate(method, tail string) bool {
	return method == http.MethodPost && strings.TrimSuffix(tail, "/") == "/cvds"
}

// proxyErrorStatus maps a reverse-proxy transport failure to the status the
// legacy client should see. A timeout awaiting the Host Orchestrator's first
// response header (a long-poll :wait, or a slow upstream that outran
// hoProxyResponseHeaderTimeout) becomes 504 Gateway Timeout, which the
// upstream libhoclient/cvdr retry; every other transport error (connection
// refused, DNS failure, reset) stays 502 Bad Gateway, which they treat as
// terminal.
func proxyErrorStatus(err error) int {
	if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

// isWebSocketUpgrade reports whether r asks for a WebSocket upgrade.
func isWebSocketUpgrade(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, v := range r.Header.Values("Connection") {
		for part := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "upgrade") {
				return true
			}
		}
	}
	return false
}

// sameHostOrigin reports whether the request's Origin header is absent
// (non-browser clients) or names the same host the request was sent to,
// mirroring websocket.Accept's default origin check.
func sameHostOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}
