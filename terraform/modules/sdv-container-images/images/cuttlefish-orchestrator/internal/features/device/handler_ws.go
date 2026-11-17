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

package devicefeature

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// WebSocket configuration constants.
const (
	// wsConnectTimeout is the maximum time to wait for upstream connection.
	wsConnectTimeout = 10 * time.Second
	// wsMessageSizeLimit is the maximum signaling message size (64KB).
	wsMessageSizeLimit = 64 * 1024

	// adbMessageSizeLimit bounds one frame on the ADB route. ADB negotiates a
	// maximum payload of 1 MiB in current clients and adds a 24-byte header on
	// top; the limit sits clear of that rather than exactly on it, because a
	// client does not recover from a frame over the limit. The connection
	// dies mid-transfer.
	adbMessageSizeLimit = 2 * 1024 * 1024
)

// WebSocketHandler bridges browser and CLI clients to the WebSocket endpoints
// a host's operator serves for a device.
//
// Two routes share it (WebRTC signaling and the ADB byte stream) because
// they are the same proxy: resolve the device to its host, dial the operator,
// relay frames until either end goes away. They differ only in which operator
// path they dial and how large a frame they admit. Neither interprets what it
// carries.
type WebSocketHandler struct {
	wsProxy

	deviceRepo      device.Getter
	hostRepo        host.Getter
	hoClientFactory *hoclient.Factory

	// allowInsecureTLS skips certificate verification when dialing the upstream
	// Host Orchestrator. Driven by the same config as the HO REST client
	// (gcp.allow_self_signed_host_ssl_cert); the HO serves self-signed certs
	// in current deployments.
	allowInsecureTLS bool
}

// WSOption configures optional WebSocketHandler behavior.
type WSOption func(*WebSocketHandler)

// WithDevOrigin accepts the local Angular dev server (localhost:4200) as a
// WebSocket origin in addition to same-host origins. Development only.
func WithDevOrigin() WSOption {
	return func(h *WebSocketHandler) { h.allowDevOrigin = true }
}

// NewWebSocketHandler creates the device WebSocket proxy.
func NewWebSocketHandler(
	deviceRepo device.Getter,
	hostRepo host.Getter,
	hoClientFactory *hoclient.Factory,
	logger *slog.Logger,
	allowInsecureTLS bool,
	opts ...WSOption,
) *WebSocketHandler {
	h := &WebSocketHandler{
		wsProxy: wsProxy{
			logger:       logger,
			pingInterval: wsPingInterval,
			pingTimeout:  wsPingTimeout,
		},
		deviceRepo:       deviceRepo,
		hostRepo:         hostRepo,
		hoClientFactory:  hoClientFactory,
		allowInsecureTLS: allowInsecureTLS,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// wsRoute is one operator endpoint this proxy bridges to.
type wsRoute struct {
	// name labels the route in logs.
	name string
	// path builds the operator path for a resolved operator device ID.
	path func(operatorDeviceID string) string
	// readLimit bounds one frame in either direction.
	readLimit int64
}

// signalingRoute carries the WebRTC signaling exchange.
var signalingRoute = wsRoute{
	name:      "signaling",
	path:      func(id string) string { return "/devices/" + id + "/connect" },
	readLimit: wsMessageSizeLimit,
}

// adbRoute carries the device's ADB stream, which the operator bridges to the
// CVD's adbd socket. It is a byte stream and not a message protocol. Frame
// boundaries carry no meaning, so this proxy forwards them exactly as it finds
// them and neither end may depend on where they fall.
var adbRoute = wsRoute{
	name:      "adb",
	path:      func(id string) string { return "/devices/" + id + "/adb" },
	readLimit: adbMessageSizeLimit,
}

// HandleSignaling proxies the WebRTC signaling exchange.
func (h *WebSocketHandler) HandleSignaling(w http.ResponseWriter, r *http.Request) {
	h.bridge(w, r, signalingRoute)
}

// HandleADB proxies the device's ADB stream, letting a client speak the ADB
// wire protocol to a device it has no route to.
func (h *WebSocketHandler) HandleADB(w http.ResponseWriter, r *http.Request) {
	h.bridge(w, r, adbRoute)
}

// bridge upgrades the client connection and relays frames to the operator
// endpoint the route names.
func (h *WebSocketHandler) bridge(w http.ResponseWriter, r *http.Request, route wsRoute) {
	ctx := r.Context()
	deviceID := chi.URLParam(r, "id")
	instance := chi.URLParam(r, "name")

	h.logger.Debug("WebSocket connection request",
		slog.String("route", route.name),
		slog.String("device_id", deviceID),
		slog.String("instance", instance),
		slog.String("remote_addr", r.RemoteAddr),
	)

	// Authenticate (authentication is the only gate, no roles or scopes)
	principal, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		h.logger.Warn("WebSocket connection rejected: no authentication",
			slog.String("route", route.name),
			slog.String("device_id", deviceID),
		)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Reject cross-origin WebSocket requests before any device lookup or
	// upstream Host Orchestrator dial so a rejected 403 never opens a transient
	// upstream connection.
	if !sameOrAllowedWSOrigin(r, h.allowDevOrigin) {
		h.logger.Warn("WebSocket connection rejected: cross-origin request",
			slog.String("route", route.name),
			slog.String("device_id", deviceID),
			slog.String("origin", r.Header.Get("Origin")),
		)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Lookup device. A deleting or deleted one is a 404 before the host is
	// asked, so a client can tell a device that is gone from a host that does
	// not answer.
	d, err := liveDevice(ctx, h.deviceRepo, deviceID)
	if err != nil {
		if errors.Is(err, device.ErrNotFound) {
			h.logger.Debug("WebSocket connection refused: device not found",
				slog.String("route", route.name),
				slog.String("device_id", deviceID),
				slog.Any("error", err),
			)
			http.Error(w, "Device not found", http.StatusNotFound)
			return
		}
		h.logger.Error("Failed to get device for WebSocket",
			slog.String("route", route.name),
			slog.String("device_id", deviceID),
			slog.Any("error", err),
		)
		http.Error(w, "Failed to load device", http.StatusInternalServerError)
		return
	}

	// Lookup host
	hostInfo, err := h.hostRepo.Get(ctx, d.HostID)
	if err != nil {
		h.logger.Error("Failed to get host for WebSocket",
			slog.String("route", route.name),
			slog.String("host_id", d.HostID),
			slog.Any("error", err),
		)
		if errors.Is(err, host.ErrNotFound) {
			http.Error(w, "Host not found", http.StatusNotFound)
		} else {
			http.Error(w, "Failed to load host", http.StatusInternalServerError)
		}
		return
	}

	// Resolve the operator device ID (WebRTC device ID). It comes off the row
	// when the create recorded it, and off the host otherwise. A failure here
	// is refused and never guessed past. A bridge built on a fabricated id
	// upgrades cleanly and then carries nothing.
	operatorDeviceID, err := resolveOperatorDeviceID(ctx,
		h.hoClientFactory.ClientForHost(hostInfo.HostOrchestratorURL()), d, instance)
	if err != nil {
		h.logger.Error("Failed to resolve the operator device id for WebSocket",
			slog.String("route", route.name),
			slog.String("device_id", deviceID),
			slog.String("instance", instance),
			slog.Any("error", err),
		)
		// A device the host does not have is a 404; anything else here is the
		// host being unreachable, an upstream fault the caller did not cause,
		// so it maps to 502. web.StatusError does not embed error, so
		// errors.As is the documented match for it.
		status := http.StatusBadGateway
		var statusErr web.StatusError
		if errors.As(err, &statusErr) {
			status = statusErr.GetStatus()
		}
		http.Error(w, "Failed to resolve device on host orchestrator", status)
		return
	}

	upstreamURL := h.buildOperatorURL(hostInfo, route.path(operatorDeviceID))

	h.logger.Info("Connecting to upstream WebSocket",
		slog.String("route", route.name),
		slog.String("device_id", deviceID),
		slog.String("upstream_url", upstreamURL),
		slog.String("host_internal_ip", hostInfo.InternalIP),
		slog.String("host_external_ip", hostInfo.IPAddress),
	)

	// Connect to upstream Host Orchestrator WebSocket. This private client is
	// a second HO transport because hoclient does not speak WebSocket, and
	// this path is a byte relay. Retries, circuit breakers, and typed error
	// mapping must not apply here.
	connectCtx, cancel := context.WithTimeout(ctx, wsConnectTimeout)
	defer cancel()

	// bodyclose cannot see the library's contract: Dial documents "You never
	// need to close resp.Body yourself" and detaches the real body internally,
	// leaving at most 1024 buffered bytes behind a NopCloser.
	//nolint:bodyclose // coder/websocket closes the handshake body itself.
	upstreamConn, resp, err := websocket.Dial(connectCtx, upstreamURL, &websocket.DialOptions{
		HTTPClient: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: h.allowInsecureTLS,
				},
			},
		},
	})
	if err != nil {
		statusCode := 0
		if resp != nil {
			statusCode = resp.StatusCode
		}
		h.logger.Error("Failed to connect to upstream WebSocket",
			slog.String("route", route.name),
			slog.String("upstream_url", upstreamURL),
			slog.Any("error", err),
			slog.Int("response_status", statusCode),
			slog.String("host_internal_ip", hostInfo.InternalIP),
		)
		http.Error(w, "Failed to connect to device", http.StatusBadGateway)
		return
	}
	defer upstreamConn.Close(websocket.StatusNormalClosure, "client disconnected")

	upstreamConn.SetReadLimit(route.readLimit)

	// Upgrade client connection to WebSocket. The default same-host Origin
	// check stays enabled for defense in depth. The ambient IAP cookie
	// authenticates this GET, so a cross-site page must not be able to open a
	// socket to a device. A non-browser client such as cfctl sends no Origin
	// and is unaffected.
	clientConn, err := websocket.Accept(w, r, h.acceptOptions())
	if err != nil {
		h.logger.Error("Failed to accept WebSocket connection",
			slog.String("route", route.name),
			slog.String("device_id", deviceID),
			slog.Any("error", err),
		)
		return
	}
	defer clientConn.Close(websocket.StatusNormalClosure, "connection closed")

	clientConn.SetReadLimit(route.readLimit)

	h.logger.Info("WebSocket connection established",
		slog.String("route", route.name),
		slog.String("device_id", deviceID),
		slog.String("user_email", principal.Email),
		slog.String("upstream_url", upstreamURL),
	)

	// Proxy messages bidirectionally
	h.proxyWebSocket(ctx, clientConn, upstreamConn, deviceID)

	h.logger.Debug("WebSocket connection closed",
		slog.String("route", route.name),
		slog.String("device_id", deviceID),
	)
}

// operatorPort is the host operator's plain-HTTP port, serving both the
// signaling WebSocket at /devices/{id}/connect and the ADB bridge at
// /devices/{id}/adb. The deb-installed operator on the cco-host image listens
// on 1080 (HTTP) and 1443 (HTTPS); persisted device webrtc_port values predate
// this and are not trusted here.
const operatorPort = 1080

// buildOperatorURL constructs the WebSocket URL for an operator endpoint.
func (h *WebSocketHandler) buildOperatorURL(hostInfo *host.Host, path string) string {
	// Use internal IP for Cloud Run VPC connectivity.
	// Cloud Run is configured with vpc-access-egress: private-ranges-only,
	// which means it can only reach private IPs (10.x.x.x), not external IPs.
	ip := hostInfo.InternalIP
	if ip == "" {
		ip = hostInfo.IPAddress
	}

	return fmt.Sprintf("ws://%s:%d%s", ip, operatorPort, path)
}

// Ping interval for keep-alive (must be less than operator's 60-second pong timeout).
const wsPingInterval = 20 * time.Second

// wsPingTimeout bounds each keep-alive ping so a half-open peer cannot block
// the ping goroutine (and leak both connections) until TCP keepalive fires.
const wsPingTimeout = 10 * time.Second

// wsProxy relays frames between an accepted client connection and an upstream
// one until either end goes away. It is transport machinery only. What the
// frames mean belongs to whichever route opened them.
type wsProxy struct {
	logger *slog.Logger

	// allowDevOrigin additionally accepts the local Angular dev server
	// (localhost:4200) as a WebSocket origin. Off by default so production
	// only accepts same-host origins.
	allowDevOrigin bool

	// pingInterval and pingTimeout drive the keep-alive loop; fields so tests
	// can shorten them.
	pingInterval time.Duration
	pingTimeout  time.Duration
}

// proxyWebSocket relays messages between client and upstream connections.
// Uses raw message proxying to avoid any JSON transformation issues.
func (h *wsProxy) proxyWebSocket(
	ctx context.Context,
	client *websocket.Conn,
	upstream *websocket.Conn,
	deviceID string,
) {
	// A context that cancels when either connection closes, so a dead peer
	// tears the whole session down instead of leaking its twin.
	proxyCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	startTime := time.Now()

	go h.keepAlive(proxyCtx, cancel, client, upstream, deviceID, startTime)

	session := wsSession{deviceID: deviceID, started: startTime}
	go func() {
		defer cancel()
		h.relay(proxyCtx, client, upstream, "client", "upstream", session)
	}()

	h.relay(proxyCtx, upstream, client, "upstream", "client", session)
}

// keepAlive pings both peers periodically. That prevents idle timeout from
// Cloud Run and keeps the operator happy, and a failed round cancels the
// session so a half-open peer cannot leak both connections.
func (h *wsProxy) keepAlive(ctx context.Context, cancel context.CancelFunc, client, upstream *websocket.Conn, deviceID string, startTime time.Time) {
	ticker := time.NewTicker(h.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := h.pingBoth(ctx, client, upstream); err != nil {
				h.logger.Debug("Keep-alive ping failed",
					slog.String("device_id", deviceID),
					slog.Any("error", err),
					slog.Duration("session_duration", time.Since(startTime)),
				)
				cancel()
				return
			}
			h.logger.Debug("Keep-alive pings sent successfully",
				slog.String("device_id", deviceID),
				slog.Duration("session_duration", time.Since(startTime)),
			)
		}
	}
}

// wsSession identifies one proxied session for the relay logs.
type wsSession struct {
	deviceID string
	started  time.Time
}

// relay pumps raw messages src -> dst until either side closes or errors,
// logging with the from/to direction labels. One relay runs per direction;
// the caller cancels the shared context when either returns.
func (h *wsProxy) relay(ctx context.Context, src, dst *websocket.Conn, from, to string, session wsSession) {
	forwarded := 0
	for {
		msgType, data, err := src.Read(ctx)
		if err != nil {
			closeStatus := websocket.CloseStatus(err)
			if !isNormalClose(err) {
				h.logger.Warn("WebSocket "+from+" read error",
					slog.String("device_id", session.deviceID),
					slog.Any("error", err),
					slog.Int("close_status", int(closeStatus)),
					slog.Int("messages_forwarded", forwarded),
					slog.Duration("session_duration", time.Since(session.started)),
				)
			} else {
				h.logger.Info("WebSocket "+from+" closed",
					slog.String("device_id", session.deviceID),
					slog.Int("close_status", int(closeStatus)),
					slog.Int("messages_forwarded", forwarded),
					slog.Duration("session_duration", time.Since(session.started)),
				)
			}
			return
		}

		// Debug-only, and only for a route that carries JSON. Naming the frame
		// costs a parse per message, so gate it on the level. ADB frames are
		// binary and never reach it.
		if msgType == websocket.MessageText && len(data) > 0 && h.logger.Enabled(ctx, slog.LevelDebug) {
			h.logger.Debug("Forwarding "+from+"->"+to+" message",
				slog.String("device_id", session.deviceID),
				slog.String("msg_type", extractMessageType(data)),
				slog.Int("msg_size", len(data)),
				slog.Int("msg_count", forwarded+1),
			)
		}

		if err := dst.Write(ctx, msgType, data); err != nil {
			if !isNormalClose(err) {
				h.logger.Warn("WebSocket "+to+" write error",
					slog.String("device_id", session.deviceID),
					slog.Any("error", err),
					slog.Int("messages_forwarded", forwarded),
					slog.Duration("session_duration", time.Since(session.started)),
				)
			}
			return
		}
		forwarded++
	}
}

// pingBoth pings both peers under a single per-round deadline so a half-open
// connection surfaces as an error instead of blocking forever.
func (h *wsProxy) pingBoth(ctx context.Context, client, upstream *websocket.Conn) error {
	pingCtx, cancel := context.WithTimeout(ctx, h.pingTimeout)
	defer cancel()
	if err := upstream.Ping(pingCtx); err != nil {
		return fmt.Errorf("upstream ping: %w", err)
	}
	if err := client.Ping(pingCtx); err != nil {
		return fmt.Errorf("client ping: %w", err)
	}
	return nil
}

// acceptOptions returns the upgrade options: same-host origins (the library
// default), plus the local Angular dev server when explicitly enabled.
func (h *wsProxy) acceptOptions() *websocket.AcceptOptions {
	opts := &websocket.AcceptOptions{}
	if h.allowDevOrigin {
		opts.OriginPatterns = []string{"localhost:4200"}
	}
	return opts
}

func sameOrAllowedWSOrigin(r *http.Request, allowDevOrigin bool) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) > 1 {
		return false
	}
	raw := strings.TrimSpace(origins[0])
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	return allowDevOrigin && strings.EqualFold(u.Host, "localhost:4200")
}

// extractMessageType attempts to extract the "type" field from a JSON message.
// Returns empty string if not found or on parse error.
func extractMessageType(data []byte) string {
	// Quick peek at the JSON to extract "type" field without full parsing
	var peek struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &peek); err == nil && peek.Type != "" {
		return peek.Type
	}
	// Check for device_info which doesn't have a type field
	if len(data) > 0 && strings.Contains(string(data[:min(100, len(data))]), "device_info") {
		return "device_info"
	}
	return "unknown"
}

// isNormalClose returns true if the error represents a normal WebSocket close.
func isNormalClose(err error) bool {
	if err == nil {
		return false
	}
	closeStatus := websocket.CloseStatus(err)
	return closeStatus == websocket.StatusNormalClosure ||
		closeStatus == websocket.StatusGoingAway ||
		strings.Contains(err.Error(), "context canceled")
}
