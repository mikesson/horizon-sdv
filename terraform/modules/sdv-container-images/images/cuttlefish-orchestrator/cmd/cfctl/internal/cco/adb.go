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
	"net"
	"net/http"
	"net/url"

	"github.com/coder/websocket"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// adbReadLimit bounds one inbound frame and matches the server's limit on this
// route. An ADB packet carries up to a 1 MiB payload plus a 24-byte header.
const adbReadLimit = 2 * 1024 * 1024

// ErrStreamClosed reports that the remote ADB stream closed normally during a
// write. Returning a distinct sentinel instead of io.EOF lets callers tell a
// closed remote writer apart from a clean local EOF.
var ErrStreamClosed = errors.New("ADB stream closed")

// DialADB opens one instance's ADB stream through the orchestrator. The stream
// carries the ADB wire protocol verbatim and needs no network route to the
// host, which may have no public address.
func (c *Client) DialADB(ctx context.Context, deviceID, instance string) (io.ReadWriteCloser, error) {
	endpoint, err := c.webSocketURL(instancePath(deviceID, instance) + "/adb")
	if err != nil {
		return nil, err
	}

	output.Debug("--> WS %s", endpoint)
	conn, resp, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPClient: c.streamClient,
		HTTPHeader: http.Header{"User-Agent": {fmt.Sprintf(userAgent, Version)}},
	})
	if err != nil {
		// The handshake response tells an IAP rejection apart from a missing
		// device; the dial error does not.
		if resp != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return nil, decodeAPIError(resp.StatusCode, body)
		}
		return nil, fmt.Errorf("opening the ADB stream: %w", err)
	}
	conn.SetReadLimit(adbReadLimit)

	return &wsByteStream{conn: conn, ctx: ctx}, nil
}

// webSocketURL maps an API path onto the WebSocket scheme for this server.
func (c *Client) webSocketURL(path string) (string, error) {
	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("server URL %q has no http(s) scheme", c.baseURL)
	}
	return u.String(), nil
}

// wsByteStream presents a WebSocket as a byte stream. Frame boundaries on the
// ADB route carry no meaning, so a read may return part of a frame and keep
// the rest for the next read.
type wsByteStream struct {
	conn *websocket.Conn
	ctx  context.Context

	// pending is the unread remainder of the frame last read.
	pending []byte
}

func (s *wsByteStream) Read(p []byte) (int, error) {
	for len(s.pending) == 0 {
		_, data, err := s.conn.Read(s.ctx)
		if err != nil {
			if isStreamEnd(err) {
				return 0, io.EOF
			}
			return 0, fmt.Errorf("reading the ADB stream: %w", err)
		}
		s.pending = data
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

func (s *wsByteStream) Write(p []byte) (int, error) {
	if err := s.conn.Write(s.ctx, websocket.MessageBinary, p); err != nil {
		if isStreamEnd(err) {
			return 0, ErrStreamClosed
		}
		return 0, fmt.Errorf("writing the ADB stream: %w", err)
	}
	return len(p), nil
}

// Close tears the stream down without a closing handshake, because waiting on
// a peer that may be gone would hang the exit.
func (s *wsByteStream) Close() error {
	if err := s.conn.CloseNow(); err != nil {
		return fmt.Errorf("closing the ADB stream: %w", err)
	}
	return nil
}

// isStreamEnd reports whether an error is an ordinary close by the far end,
// which the byte stream reports as EOF.
func isStreamEnd(err error) bool {
	switch websocket.CloseStatus(err) {
	case websocket.StatusNormalClosure, websocket.StatusGoingAway, websocket.StatusNoStatusRcvd:
		return true
	default:
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed)
}
