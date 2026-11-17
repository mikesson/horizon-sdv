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

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/iamcredentials/v1"
	"google.golang.org/grpc"

	"cloud-cuttlefish-orchestrator/internal/platform/relay/turnrs"
)

func main() {
	if err := run(); err != nil {
		slog.Error("relayhook failed", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	var (
		orchestrator = flag.String("orchestrator", "", "Base URL of the orchestrator (required)")
		listen       = flag.String("listen", "127.0.0.1:3001", "Loopback address to serve the hook on")
		audience     = flag.String("audience", "", "JWT audience (default <orchestrator>/*)")
		account      = flag.String("service-account", "", "Service account to sign as (default: the VM's own, from the metadata server)")
		timeout      = flag.Duration("timeout", 800*time.Millisecond, "Per-call bound on the orchestrator request")
		maxCache     = flag.Duration("max-cache", 5*time.Minute, "Ceiling on how long a key is cached")
		logLevel     = flag.String("log-level", "info", "debug, info, warn, or error")
	)
	flag.Parse()

	level, err := parseLevel(*logLevel)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	base, err := validateOrchestrator(*orchestrator)
	if err != nil {
		return err
	}
	if err := requireLoopback(*listen); err != nil {
		return err
	}

	// The relay is trusted to reach this process, not to authenticate to the
	// orchestrator. The VM's own identity does that.
	//
	// The path wildcard keeps one token valid for every API path. IAP rejects
	// the bare service URL here just as it rejects a Google-signed identity
	// token, so both halves of this matter.
	tokenAudience := *audience
	if tokenAudience == "" {
		tokenAudience = base + "/*"
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	signer := *account
	if signer == "" {
		if signer, err = metadataServiceAccount(ctx); err != nil {
			return err
		}
	}

	credentials, err := iamcredentials.NewService(ctx)
	if err != nil {
		return fmt.Errorf("creating IAM credentials client: %w", err)
	}
	source := oauth2.ReuseTokenSource(nil, &selfSignedJWT{
		ctx:      ctx,
		svc:      credentials,
		account:  signer,
		audience: tokenAudience,
	})
	client := oauth2.NewClient(ctx, source)

	service := newHookService(&authority{
		client:   client,
		endpoint: base + "/v2/relay/authorize",
		timeout:  *timeout,
	}, *maxCache, logger)

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", *listen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", *listen, err)
	}

	server := grpc.NewServer()
	turnrs.RegisterTurnHooksServiceServer(server, service)

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	logger.Info("serving relay authentication hook",
		slog.String("listen", *listen),
		slog.String("orchestrator", base),
		slog.String("audience", tokenAudience),
		slog.String("signing_as", signer))

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("serving: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutting down", slog.Any("cause", context.Cause(ctx)))
		server.GracefulStop()
		return nil
	}
}

// validateOrchestrator normalizes the base URL and refuses a plaintext one:
// the ID token is a bearer credential and must not travel in the clear.
func validateOrchestrator(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("-orchestrator is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parsing -orchestrator: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("-orchestrator must be an https URL, got %q", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

// requireLoopback refuses to expose the hook off-host. It authenticates no
// caller, so anything that can reach it can ask for keys.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parsing -listen: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("-listen must be a loopback address, got %q", addr)
	}
	return nil
}

// metadataServiceAccount asks the metadata server which account this VM runs
// as, so the deployment does not have to repeat it in configuration.
func metadataServiceAccount(ctx context.Context) (string, error) {
	const endpoint = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/email"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("building metadata request: %w", err)
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("asking the metadata server for this VM's service account: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata server returned %s for the service account", resp.Status)
	}
	email, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", fmt.Errorf("reading the service account email: %w", err)
	}
	if len(email) == 0 {
		return "", errors.New("metadata server returned no service account; run with -service-account")
	}
	return strings.TrimSpace(string(email)), nil
}

func parseLevel(name string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(name)); err != nil {
		return 0, fmt.Errorf("parsing -log-level: %w", err)
	}
	return level, nil
}
