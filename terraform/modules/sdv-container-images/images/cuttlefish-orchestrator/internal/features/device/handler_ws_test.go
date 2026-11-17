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
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/device/devicetest"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/web"
)

func TestIsNormalClose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isNormalClose(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewWebSocketHandler(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.DiscardHandler)

	handler := NewWebSocketHandler(nil, nil, nil, logger, true)

	assert.NotNil(t, handler)
	assert.NotNil(t, handler.logger)
	assert.True(t, handler.allowInsecureTLS)
	assert.False(t, handler.allowDevOrigin, "dev origin must be off unless opted in")
	assert.Equal(t, wsPingInterval, handler.pingInterval)
	assert.Equal(t, wsPingTimeout, handler.pingTimeout)
}

func TestAcceptOptionsDevOriginGating(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.DiscardHandler)

	prod := NewWebSocketHandler(nil, nil, nil, logger, false)
	assert.Empty(t, prod.acceptOptions().OriginPatterns,
		"production must rely on the default same-host origin check only")

	dev := NewWebSocketHandler(nil, nil, nil, logger, false, WithDevOrigin())
	assert.Equal(t, []string{"localhost:4200"}, dev.acceptOptions().OriginPatterns)
}

// dialTestWS starts a WebSocket server whose accepted connection is handed to
// serverFn, and returns a connected client conn. Cleanup closes the client
// conn before the server so handlers unblock.
func dialTestWS(t *testing.T, serverFn func(ctx context.Context, c *websocket.Conn)) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		serverFn(r.Context(), c)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws://"+strings.TrimPrefix(srv.URL, "http://"), nil)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

// readUntilError keeps a peer healthy: reading services incoming control
// frames, so pings from the proxy get answered.
func readUntilError(ctx context.Context, c *websocket.Conn) {
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return
		}
	}
}

func TestProxyWebSocketPingTimeoutTearsDownSession(t *testing.T) {
	t.Parallel()
	h := NewWebSocketHandler(nil, nil, nil, slog.New(slog.DiscardHandler), false)
	h.pingInterval = 20 * time.Millisecond
	h.pingTimeout = 100 * time.Millisecond

	// Half-open upstream: never reads, so pings are never answered. Released
	// at cleanup (after conns close) so the handler goroutine exits.
	stallRelease := make(chan struct{})
	t.Cleanup(func() { close(stallRelease) })
	upstream := dialTestWS(t, func(context.Context, *websocket.Conn) {
		<-stallRelease
	})
	client := dialTestWS(t, readUntilError)

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.proxyWebSocket(t.Context(), client, upstream, "dev-1")
	}()

	select {
	case <-done:
		// Ping deadline fired, proxy ctx canceled, both relays exited.
	case <-time.After(5 * time.Second):
		t.Fatal("proxyWebSocket still running: a half-open peer must not block the keep-alive ping forever")
	}
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestProxyWebSocketRelaysWithoutInfoFrameLogs(t *testing.T) {
	t.Parallel()
	logs := &syncBuffer{}
	h := NewWebSocketHandler(nil, nil, nil,
		slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})), false)

	received := make(chan string, 1)
	upstream := dialTestWS(t, func(ctx context.Context, c *websocket.Conn) {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		received <- string(data)
		if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"answer"}`)); err != nil {
			return
		}
		readUntilError(ctx, c)
	})

	answered := make(chan string, 1)
	client := dialTestWS(t, func(ctx context.Context, c *websocket.Conn) {
		if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"offer"}`)); err != nil {
			return
		}
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		answered <- string(data)
		readUntilError(ctx, c)
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.proxyWebSocket(ctx, client, upstream, "dev-1")
	}()

	waitForMessage(t, received, `{"type":"offer"}`, "client->upstream relay")
	waitForMessage(t, answered, `{"type":"answer"}`, "upstream->client relay")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("proxyWebSocket did not exit on context cancel")
	}

	if strings.Contains(logs.String(), "Forwarding") {
		t.Errorf("per-frame relay logs leaked at INFO level:\n%s", logs.String())
	}
}

func waitForMessage(t *testing.T, ch <-chan string, want, direction string) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("%s = %q, want %q", direction, got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: message never arrived", direction)
	}
}

// The two routes must reach different operator endpoints on the same port.
// The ADB bridge is a sibling of the signaling socket, not a separate server.
func TestRoutesTargetTheirOperatorEndpoints(t *testing.T) {
	t.Parallel()
	h := NewWebSocketHandler(nil, nil, nil, slog.New(slog.DiscardHandler), false)
	hostInfo := &host.Host{InternalIP: "10.0.0.5"}

	assert.Equal(t, "ws://10.0.0.5:1080/devices/grp-dev/connect",
		h.buildOperatorURL(hostInfo, signalingRoute.path("grp-dev")))
	assert.Equal(t, "ws://10.0.0.5:1080/devices/grp-dev/adb",
		h.buildOperatorURL(hostInfo, adbRoute.path("grp-dev")))
}

// An ADB packet carries up to a 1 MiB payload, so the ADB route admits frames
// far larger than signaling does. Without the wider limit an `adb install`
// would die mid-transfer, which is why the two routes carry their own.
func TestADBLimitAdmitsAFullSizeADBPacket(t *testing.T) {
	t.Parallel()
	packet := make([]byte, 24+1024*1024)
	for i := range packet {
		packet[i] = byte(i)
	}

	assert.Greater(t, int64(len(packet)), int64(wsMessageSizeLimit),
		"a full-size ADB packet must exceed the signaling limit, or this split is pointless")

	got := relayOneFrame(t, adbMessageSizeLimit, packet)
	assert.Equal(t, packet, got, "the ADB byte stream must cross the proxy unmodified")
}

// The counterfactual. The same packet under the signaling limit is dropped.
// A failure here means the limits converged and the ADB route lost its headroom.
func TestSignalingLimitRejectsAFullSizeADBPacket(t *testing.T) {
	t.Parallel()
	packet := make([]byte, 24+1024*1024)

	assert.Nil(t, relayOneFrame(t, wsMessageSizeLimit, packet),
		"the signaling limit must be too small for an ADB packet")
}

// relayOneFrame sends one binary frame through a proxy whose connections carry
// the given read limit, returning what arrived (nil if nothing did).
func relayOneFrame(t *testing.T, readLimit int64, frame []byte) []byte {
	t.Helper()
	h := NewWebSocketHandler(nil, nil, nil, slog.New(slog.DiscardHandler), false)
	h.pingInterval = time.Hour // Irrelevant here; keep it out of the way.

	delivered := make(chan []byte, 1)
	upstream := dialTestWS(t, func(ctx context.Context, c *websocket.Conn) {
		c.SetReadLimit(readLimit)
		_, data, err := c.Read(ctx)
		if err != nil {
			close(delivered)
			return
		}
		delivered <- data
		readUntilError(ctx, c)
	})
	client := dialTestWS(t, func(ctx context.Context, c *websocket.Conn) {
		if err := c.Write(ctx, websocket.MessageBinary, frame); err != nil {
			return
		}
		readUntilError(ctx, c)
	})
	client.SetReadLimit(readLimit)
	upstream.SetReadLimit(readLimit)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.proxyWebSocket(ctx, client, upstream, "dev-1")
	}()

	select {
	case got := <-delivered:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("frame never arrived and the connection did not fail")
		return nil
	}
}

// fastHOClientConfig makes hoclient fail fast against unreachable hosts.
func fastHOClientConfig() hoclient.ClientConfig {
	cfg := hoclient.DefaultClientConfig()
	cfg.MaxRetries = 0
	cfg.RetryDelay = time.Millisecond
	cfg.Timeout = 2 * time.Second
	cfg.FallbackPorts = nil
	return cfg
}

// countingResolverFor stands up a fake Host Orchestrator serving the given
// /cvds body, and reports how many times it was asked for the listing.
func countingResolverFor(t *testing.T, cvdsBody string) (hoclient.Client, *atomic.Int64) {
	t.Helper()
	listings := &atomic.Int64{}
	ho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cvds" {
			http.NotFound(w, r)
			return
		}
		listings.Add(1)
		_, _ = io.WriteString(w, cvdsBody)
	}))
	t.Cleanup(ho.Close)
	factory := hoclient.NewFactory(fastHOClientConfig())
	return factory.ClientForHost(hostFor(t, ho.URL).HostOrchestratorURL()), listings
}

// resolverFor stands up a fake Host Orchestrator serving the given /cvds body.
func resolverFor(t *testing.T, cvdsBody string) hoclient.Client {
	t.Helper()
	client, _ := countingResolverFor(t, cvdsBody)
	return client
}

// The create records what the host reported, so signaling and the ADB bridge
// address the operator without asking the host at all. That lets a stream come
// up while the host is briefly unreachable, and takes a round trip off every
// signaling request.
func TestResolveOperatorDeviceIDReadsThePersistedInstance(t *testing.T) {
	t.Parallel()
	// The host would answer with a different id. Only a live lookup could
	// return it, so the assertions below fail on both count and value if one
	// happens.
	client, listings := countingResolverFor(t,
		`{"cvds":[{"group":"cvdabc","name":"alpha","webrtc_device_id":"cvdabc-alpha-9"}]}`)

	got, err := resolveOperatorDeviceID(t.Context(), client, &device.Device{
		CVDID:     "cvdabc/alpha",
		Instances: []device.Instance{{Name: "alpha", WebRTCDeviceID: "cvdabc-alpha-1"}},
	}, "alpha")

	require.NoError(t, err)
	assert.Equal(t, "cvdabc-alpha-1", got)
	assert.Zero(t, listings.Load(), "a persisted operator id must cost no host round trip")
}

// A device created before the instance array existed carries one synthesized
// instance whose operator id is empty, because the host appends a fleet-unique
// instance id that cannot be derived from the group and name. Those devices
// must still resolve through the live listing.
func TestResolveOperatorDeviceIDAsksTheHostWhenTheRowHasNoID(t *testing.T) {
	t.Parallel()
	client, listings := countingResolverFor(t,
		`{"cvds":[{"group":"cvdabc","name":"alpha","webrtc_device_id":"cvdabc-alpha-1"}]}`)

	got, err := resolveOperatorDeviceID(t.Context(), client, &device.Device{
		CVDID:     "cvdabc/alpha",
		Instances: []device.Instance{{Name: "alpha"}},
	}, "alpha")

	require.NoError(t, err)
	assert.Equal(t, "cvdabc-alpha-1", got)
	assert.Equal(t, int64(1), listings.Load(), "an empty persisted id falls back to the host")
}

func TestResolveOperatorDeviceIDTakesTheHostOrchestratorAnswer(t *testing.T) {
	t.Parallel()
	// The live shape, measured on a real deployment. cvd appends the
	// fleet-unique instance id, so the operator id is NOT "<group>-<name>".
	client := resolverFor(t,
		`{"cvds":[{"group":"cvdabc","name":"alpha","webrtc_device_id":"cvdabc-alpha-1"}]}`)

	got, err := resolveOperatorDeviceID(t.Context(), client, &device.Device{
		CVDID:     "cvdabc/alpha",
		Instances: []device.Instance{{Name: "alpha"}},
	}, "alpha")

	require.NoError(t, err)
	assert.Equal(t, "cvdabc-alpha-1", got)
	assert.NotEqual(t, "cvdabc-alpha", got,
		"the operator id is not derivable from group and name; only the host knows it")
}

// The three ways this used to answer with an id the operator never registered.
// Each one produced a stream that connected and then carried nothing, which
// reads as a broken device rather than as the host problem it is.
func TestResolveOperatorDeviceIDRefusesToGuess(t *testing.T) {
	t.Parallel()
	t.Run("host unreachable", func(t *testing.T) {
		t.Parallel()
		// Grab a port with nothing listening on it.
		l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		closedURL := "http://" + l.Addr().String()
		require.NoError(t, l.Close())

		factory := hoclient.NewFactory(fastHOClientConfig())
		_, err = resolveOperatorDeviceID(t.Context(),
			factory.ClientForHost(hostFor(t, closedURL).HostOrchestratorURL()),
			&device.Device{CVDID: "grp/dev", Instances: []device.Instance{{Name: "dev"}}}, "dev")

		require.Error(t, err)
		// This is 502 and not 404. The device may well be there while the
		// host is not answering. The two must stay distinguishable, because
		// one of them means "stop retrying" and the other means "try again".
		var statusErr web.StatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, http.StatusBadGateway, statusErr.GetStatus())
	})

	t.Run("device absent from the listing", func(t *testing.T) {
		t.Parallel()
		client := resolverFor(t,
			`{"cvds":[{"group":"grp","name":"other","webrtc_device_id":"grp-other-1"}]}`)

		_, err := resolveOperatorDeviceID(t.Context(), client,
			&device.Device{CVDID: "grp/dev", Instances: []device.Instance{{Name: "dev"}}}, "dev")

		require.Error(t, err)
		var statusErr web.StatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, http.StatusNotFound, statusErr.GetStatus())
	})

	t.Run("listed without a webrtc device id", func(t *testing.T) {
		t.Parallel()
		client := resolverFor(t, `{"cvds":[{"group":"grp","name":"dev","webrtc_device_id":""}]}`)

		_, err := resolveOperatorDeviceID(t.Context(), client,
			&device.Device{CVDID: "grp/dev", Instances: []device.Instance{{Name: "dev"}}}, "dev")

		require.Error(t, err)
		var statusErr web.StatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, http.StatusNotFound, statusErr.GetStatus())
	})
}

// hostFor builds a host whose HostOrchestratorURL points at rawURL.
func hostFor(t *testing.T, rawURL string) *host.Host {
	t.Helper()
	ip, portStr, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	require.NoError(t, err)
	port, err := strconv.ParseInt(portStr, 10, 64)
	require.NoError(t, err)
	return &host.Host{ID: "h1", InternalIP: ip, HostOrchestratorPort: port}
}

// countingHostGetter counts host lookups and finds no host.
type countingHostGetter struct {
	calls atomic.Int64
}

func (g *countingHostGetter) Get(context.Context, string) (*host.Host, error) {
	g.calls.Add(1)
	return nil, host.ErrNotFound
}

// A device that is being deleted or is deleted answers 404 on both WebSocket
// routes before the host is asked, so a client can tell a deleted device from
// an unreachable host. A running device goes on to the host lookup.
func TestWebSocketRoutesRefuseARetiredDeviceBeforeAskingTheHost(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		status      device.Status
		path        string
		wantBody    string
		hostLookups int64
	}{
		{"adb deleted", device.StatusDeleted, "/adb", "Device not found", 0},
		{"adb deleting", device.StatusDeleting, "/adb", "Device not found", 0},
		{"adb running", device.StatusRunning, "/adb", "Host not found", 1},
		{"signaling deleted", device.StatusDeleted, "/signaling/ws", "Device not found", 0},
		{"signaling deleting", device.StatusDeleting, "/signaling/ws", "Device not found", 0},
		{"signaling running", device.StatusRunning, "/signaling/ws", "Host not found", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hosts := &countingHostGetter{}
			devices := devicetest.NewFakeRepo(&device.Device{ID: "dev-1", HostID: "h1", Status: tc.status})
			h := NewWebSocketHandler(devices, hosts, nil, slog.New(slog.DiscardHandler), false)
			router := chi.NewRouter()
			router.Get("/v2/devices/{id}/instances/{name}/adb", h.HandleADB)
			router.Get("/v2/devices/{id}/instances/{name}/signaling/ws", h.HandleSignaling)

			req := httptest.NewRequestWithContext(
				auth.ContextWithPrincipal(t.Context(), &auth.Principal{Email: "user@example.com"}),
				http.MethodGet, "/v2/devices/dev-1/instances/1"+tc.path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusNotFound, rec.Code)
			assert.Contains(t, rec.Body.String(), tc.wantBody)
			assert.Equal(t, tc.hostLookups, hosts.calls.Load())
		})
	}
}
