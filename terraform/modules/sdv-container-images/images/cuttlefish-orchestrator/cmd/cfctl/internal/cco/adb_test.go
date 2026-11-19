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
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket"
)

// adbTestServer serves /v2/devices/{id}/adb, handing each accepted connection
// to serve.
func adbTestServer(t *testing.T, serve func(ctx context.Context, c *websocket.Conn)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(adbReadLimit)
		serve(r.Context(), c)
	}))
	t.Cleanup(srv.Close)
	return &Client{baseURL: srv.URL, streamClient: srv.Client()}
}

// The operator chunks the ADB stream on io.Copy's buffer, so bytes must arrive
// in order and intact whatever the framing and read buffer size.
func TestADBStreamReassemblesAcrossFrames(t *testing.T) {
	t.Parallel()
	// Odd sizes keep the chunks from aligning with any buffer.
	chunks := [][]byte{
		[]byte("CNXN"),
		make([]byte, 1),
		[]byte("host::features=shell_v2"),
		make([]byte, 5000),
	}
	var want []byte
	for i, c := range chunks {
		for j := range c {
			c[j] = byte(i*31 + j)
		}
		want = append(want, c...)
	}

	client := adbTestServer(t, func(ctx context.Context, c *websocket.Conn) {
		for _, chunk := range chunks {
			if err := c.Write(ctx, websocket.MessageBinary, chunk); err != nil {
				return
			}
		}
	})

	stream, err := client.DialADB(t.Context(), "dev-1", "inst")
	if err != nil {
		t.Fatalf("DialADB() error = %v", err)
	}
	defer stream.Close()

	// A 7-byte buffer forces the partial-frame carry on every chunk.
	got := make([]byte, 0, len(want))
	buf := make([]byte, 7)
	for len(got) < len(want) {
		n, err := stream.Read(buf)
		if err != nil {
			t.Fatalf("Read() after %d of %d bytes: %v", len(got), len(want), err)
		}
		got = append(got, buf[:n]...)
	}

	if string(got) != string(want) {
		t.Errorf("stream delivered %d bytes, want %d, and they differ", len(got), len(want))
	}
}

// A normal close by the far end must read as EOF, or the bridge reports every
// ordinary disconnect as a failure.
func TestADBStreamReportsCloseAsEOF(t *testing.T) {
	t.Parallel()
	client := adbTestServer(t, func(_ context.Context, c *websocket.Conn) {
		c.Close(websocket.StatusNormalClosure, "done")
	})

	stream, err := client.DialADB(t.Context(), "dev-1", "inst")
	if err != nil {
		t.Fatalf("DialADB() error = %v", err)
	}
	defer stream.Close()

	if _, err := stream.Read(make([]byte, 16)); !errors.Is(err, io.EOF) {
		t.Errorf("Read() error = %v, want io.EOF", err)
	}
}

func TestADBStreamWriteCloseReturnsErrStreamClosed(t *testing.T) {
	t.Parallel()
	closed := make(chan struct{})
	client := adbTestServer(t, func(_ context.Context, c *websocket.Conn) {
		_ = c.Close(websocket.StatusNormalClosure, "done")
		close(closed)
	})

	stream, err := client.DialADB(t.Context(), "dev-1", "inst")
	if err != nil {
		t.Fatalf("DialADB() error = %v", err)
	}
	defer stream.Close()
	<-closed

	_, _ = stream.Read(make([]byte, 16))

	_, werr := stream.Write([]byte("OPEN"))
	if !errors.Is(werr, ErrStreamClosed) {
		t.Fatalf("Write() error = %v, want ErrStreamClosed", werr)
	}
	if errors.Is(werr, io.EOF) {
		t.Fatalf("Write() returned io.EOF (%v); must return non-EOF ErrStreamClosed", werr)
	}
}

// A refused upgrade must surface the server's status, since the dial error
// alone cannot tell a missing device from an authentication failure.
func TestDialADBSurfacesTheHandshakeStatus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Device not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	client := &Client{baseURL: srv.URL, streamClient: srv.Client()}

	_, err := client.DialADB(t.Context(), "missing", "inst")
	if err == nil {
		t.Fatal("DialADB() succeeded against a 404")
	}
	apiErr, ok := errors.AsType[*Error](err)
	if !ok || apiErr.Status != http.StatusNotFound {
		t.Fatalf("DialADB() error = %v, want a 404 API error", err)
	}
}

func TestWebSocketURLSchemes(t *testing.T) {
	t.Parallel()
	for base, want := range map[string]string{
		"https://cco.example.com": "wss://cco.example.com/v2/devices/d1/adb",
		"http://localhost:8080":   "ws://localhost:8080/v2/devices/d1/adb",
	} {
		c := &Client{baseURL: base}
		got, err := c.webSocketURL("/v2/devices/d1/adb")
		if err != nil {
			t.Fatalf("webSocketURL(%q) error = %v", base, err)
		}
		if got != want {
			t.Errorf("webSocketURL(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestIsStreamEnd(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"wrapped context cancel", fmt.Errorf("read: %w", context.Canceled), true},
		{"wrapped closed connection", fmt.Errorf("write tcp: %w", net.ErrClosed), true},
		{"real failure", errors.New("connection reset by peer"), false},
		// Only a wrapped sentinel counts as an end.
		{"stringified cancel is NOT an end", errors.New("context canceled"), false},
	}
	for _, tc := range cases {
		if got := isStreamEnd(tc.err); got != tc.want {
			t.Errorf("%s: isStreamEnd = %v, want %v", tc.name, got, tc.want)
		}
	}
}
