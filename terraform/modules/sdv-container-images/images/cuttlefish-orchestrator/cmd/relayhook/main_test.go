// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"cloud-cuttlefish-orchestrator/internal/platform/relay/turnrs"
)

func serveHooksLoopback(t *testing.T, hook *hookService) grpc.ClientConnInterface {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen failed: %v", err)
	}
	srv := grpc.NewServer()
	turnrs.RegisterTurnHooksServiceServer(srv, hook)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient(%q) failed: %v", lis.Addr().String(), err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func swapDefaultTransport(t *testing.T, fn roundTripFunc) {
	t.Helper()
	orig := http.DefaultClient.Transport
	http.DefaultClient.Transport = fn
	t.Cleanup(func() {
		http.DefaultClient.Transport = orig
	})
}

func TestParseLevel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{in: "debug", want: slog.LevelDebug},
		{in: "INFO", want: slog.LevelInfo},
		{in: "warn", want: slog.LevelWarn},
		{in: "error", want: slog.LevelError},
		{in: "warning", wantErr: true},
		{in: "trace", wantErr: true},
		{in: "", wantErr: true},
	}

	for _, tc := range cases {
		got, err := parseLevel(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseLevel(%q) = %v, nil; want error", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parseLevel(%q) = (%v, %v), want (%v, nil)", tc.in, got, err, tc.want)
		}
	}
}

func TestMetadataServiceAccount(t *testing.T) {
	swapDefaultTransport(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "metadata.google.internal" {
			return nil, errors.New("unexpected host: " + r.URL.Host)
		}
		if r.Header.Get("Metadata-Flavor") != "Google" {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Body:       io.NopCloser(strings.NewReader("missing Metadata-Flavor")),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("  relay@proj.iam.gserviceaccount.com \n")),
		}, nil
	})

	email, err := metadataServiceAccount(t.Context())
	if err != nil {
		t.Fatalf("metadataServiceAccount failed: %v", err)
	}
	if email != "relay@proj.iam.gserviceaccount.com" {
		t.Errorf("email = %q, want relay@proj.iam.gserviceaccount.com", email)
	}

	// Non-200 status must fail.
	swapDefaultTransport(t, func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("not found")),
		}, nil
	})
	if _, err := metadataServiceAccount(t.Context()); err == nil {
		t.Error("expected error on HTTP 404 metadata response")
	}

	// Transport error must fail.
	swapDefaultTransport(t, func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("dial metadata refused")
	})
	if _, err := metadataServiceAccount(t.Context()); err == nil {
		t.Error("expected error when metadata transport fails")
	}
}

func TestRunValidationFailures(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{
			name: "missing_orchestrator",
			args: []string{"relayhook"},
		},
		{
			name: "plaintext_orchestrator",
			args: []string{"relayhook", "-orchestrator=http://cco.example"},
		},
		{
			name: "non_loopback_listen",
			args: []string{"relayhook", "-orchestrator=https://cco.example", "-listen=0.0.0.0:3001"},
		},
		{
			name: "invalid_log_level",
			args: []string{"relayhook", "-orchestrator=https://cco.example", "-log-level=verbose"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origArgs := os.Args
			origFlags := flag.CommandLine
			t.Cleanup(func() {
				os.Args = origArgs
				flag.CommandLine = origFlags
			})

			os.Args = tc.args
			flag.CommandLine = flag.NewFlagSet(tc.args[0], flag.ContinueOnError)
			flag.CommandLine.SetOutput(io.Discard)

			if err := run(); err == nil {
				t.Fatalf("run(%v) = nil, want validation error", tc.args)
			}
		})
	}
}

func TestRunSignalShutdown(t *testing.T) {
	origArgs := os.Args
	origFlags := flag.CommandLine
	t.Cleanup(func() {
		os.Args = origArgs
		flag.CommandLine = origFlags
	})

	// Point ADC at a valid local authorized_user JSON file so iamcredentials.NewService
	// constructs cleanly without network calls during startup.
	credFile := filepath.Join(t.TempDir(), "adc.json")
	if err := os.WriteFile(credFile, []byte(`{
		"type": "authorized_user",
		"client_id": "test-client",
		"client_secret": "test-secret",
		"refresh_token": "test-refresh"
	}`), 0o600); err != nil {
		t.Fatalf("write adc.json: %v", err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credFile)

	swapDefaultTransport(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "metadata.google.internal" {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("relay@proj.iam.gserviceaccount.com")),
			}, nil
		}
		return nil, errors.New("unexpected outbound HTTP call in unit test: " + r.URL.String())
	})

	probeLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	listenAddr := probeLis.Addr().String()
	_ = probeLis.Close()

	os.Args = []string{
		"relayhook",
		"-orchestrator=https://cco.example",
		"-listen=" + listenAddr,
		"-log-level=debug",
	}
	flag.CommandLine = flag.NewFlagSet("relayhook", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)

	errCh := make(chan error, 1)
	go func() {
		errCh <- run()
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		var d net.Dialer
		conn, dialErr := d.DialContext(ctx, "tcp", listenAddr)
		cancel()
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		select {
		case runErr := <-errCh:
			t.Fatalf("run() exited prematurely before accepting connections: %v", runErr)
		case <-time.After(20 * time.Millisecond):
		}
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("syscall.Kill(SIGTERM) failed: %v", err)
	}

	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("run() returned error on SIGTERM: %v", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run() graceful shutdown on SIGTERM")
	}
}
