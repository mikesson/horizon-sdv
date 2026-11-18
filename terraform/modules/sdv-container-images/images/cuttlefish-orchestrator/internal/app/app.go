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

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	// The IANA timezone database, embedded in the binary. Fleet schedules are
	// wall-clock windows in a named zone, so time.LoadLocation has to succeed at
	// runtime, and the runtime image is distroless, where whether zoneinfo
	// exists is a property of a base image nobody here controls. Embedding it
	// costs about 450 KB and makes the answer independent of the image, which
	// matters because the failure mode is silent: a workstation has the system
	// database, so every local gate passes whatever the container ships.
	_ "time/tzdata"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	gcsstorage "cloud.google.com/go/storage"

	"cloud-cuttlefish-orchestrator/internal/config"
	"cloud-cuttlefish-orchestrator/internal/platform/audit"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// Run wires the whole application and blocks until SIGINT/SIGTERM or a server
// failure. Returning instead of exiting lets every deferred close (Spanner
// client, GCE clients, telemetry) actually run on the failure paths.
//
//nolint:gocyclo // Composition root: a linear wiring sequence, one error branch per step.
func Run() error {
	// Config is env-only: CCO_* env vars (set by Terraform) layered over the
	// code defaults, secrets read from mounted volumes, and the result is
	// validated. See internal/config.Load.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	logger := slog.New(telemetry.NewCloudLoggingHandler(os.Stdout, cfg.GCP.ProjectID, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Log GCP configuration for debugging
	logger.Info("GCP configuration loaded",
		slog.String("project_id", cfg.GCP.ProjectID),
		slog.String("network", cfg.GCP.Network),
		slog.String("host_service_account", cfg.GCP.HostServiceAccount),
		slog.String("device_ingress", string(cfg.DeviceIngress)),
		slog.Bool("use_external_ip", cfg.DeviceIngress.UsesExternalIP()),
	)

	// The lifecycle context cancels on SIGINT/SIGTERM: every background job
	// selects on ctx.Done(), so an in-flight sync/reap/claim cycle aborts
	// promptly instead of burning the termination grace period.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	stopTelemetry := initTelemetry(ctx, cfg, logger)

	// Initialize Infrastructure Clients
	var spannerClient *spanner.Client
	if cfg.Database.Type == "spanner" {
		spannerClient, err = newSpannerClient(ctx, cfg)
		if err != nil {
			return err
		}
		defer spannerClient.Close()
		if err := checkSchema(ctx, spannerClient, logger); err != nil {
			return err
		}
	}
	// Registered after the Spanner client so the LIFO defer order flushes
	// telemetry first: the final metric collection runs the queue-stats
	// callback, which reads Spanner.
	defer stopTelemetry()

	gceHTTPClient, err := newGCEHTTPClient(ctx)
	if err != nil {
		return err
	}
	gceClientSet, err := newGCEClients(ctx, gceHTTPClient)
	if err != nil {
		return err
	}
	defer gceClientSet.Close()

	gcsClient, err := gcsstorage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating GCS client: %w", err)
	}
	defer gcsClient.Close()

	metricClient, err := monitoring.NewMetricClient(ctx)
	if err != nil {
		return fmt.Errorf("creating Cloud Monitoring metric client: %w", err)
	}
	defer metricClient.Close()

	c := &clients{
		spanner: spannerClient, gceHTTP: gceHTTPClient, gce: gceClientSet,
		storage: gcsClient, metrics: metricClient,
	}

	f, err := wireFeatures(cfg, logger, c)
	if err != nil {
		return err
	}
	// Seed the platform quota once at startup so reads (e.g. admin stats)
	// never have to initialize state. It is an explicit Run step, since Wire
	// does no I/O.
	if err := f.quotaService.EnsureDefaultLimit(ctx); err != nil {
		logger.Warn("Failed to seed platform quota", slog.Any("error", err))
	}

	backgroundJobs, err := buildJobs(ctx, cfg, logger, c, f)
	if err != nil {
		return err
	}
	startJobs(ctx, backgroundJobs)

	// Auth Middleware Config. Authentication is the only gate: anyone who
	// passes IAP has full access.
	authConfig, err := buildAuthConfig(cfg, runningOnGCP)
	if err != nil {
		return fmt.Errorf("auth config: %w", err)
	}

	legacyCO := wireLegacy(logger, f)

	server, err := buildServer(cfg, logger, f, legacyCO, authConfig)
	if err != nil {
		return err
	}

	servers := []*http.Server{server}
	var probeServer *http.Server
	if cfg.Server.ProbePort > 0 {
		probeServer = buildProbeServer(cfg, logger, f.healthHandler)
		servers = append(servers, probeServer)
	}

	serverErr := make(chan error, 2)
	go func() {
		logger.Info("Starting server", slog.String("addr", server.Addr))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- fmt.Errorf("main server (%s): %w", server.Addr, err)
		}
	}()
	if probeServer != nil {
		go func() {
			logger.Info("Starting probe server", slog.String("addr", probeServer.Addr))
			if err := probeServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serverErr <- fmt.Errorf("probe server (%s): %w", probeServer.Addr, err)
			}
		}()
	}

	select {
	case err := <-serverErr:
		shutdown(servers, backgroundJobs, f.auditLogger, logger)
		return fmt.Errorf("server failed: %w", err)
	case <-ctx.Done():
	}

	// Go 1.26 NotifyContext cancels with a cause naming the received signal.
	logger.Info("Shutting down...", slog.Any("cause", context.Cause(ctx)))
	stop() // restore default signal handling: a second SIGTERM kills immediately
	shutdown(servers, backgroundJobs, f.auditLogger, logger)

	logger.Info("Server exited")
	return nil
}

// shutdown drains all HTTP servers, joins the background jobs, and flushes audit
// entries, in that order, before the deferred client closes run.
func shutdown(servers []*http.Server, j jobs, auditLogger *audit.Logger, logger *slog.Logger) {
	// 1. Stop accepting new requests and drain in-flight ones.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, server := range servers {
		if server == nil {
			continue
		}
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("Server forced to shutdown", slog.String("addr", server.Addr), slog.Any("error", err))
		}
	}

	// 2. Join the background jobs (worker pool first, see jobs.stop).
	j.stop()

	// 3. Flush buffered audit entries before the Spanner client closes.
	auditLogger.Close()
}
