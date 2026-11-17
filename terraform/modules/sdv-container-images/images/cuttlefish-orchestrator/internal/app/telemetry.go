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
	"log/slog"
	"time"

	"cloud-cuttlefish-orchestrator/internal/config"
	"cloud-cuttlefish-orchestrator/internal/telemetry"
)

// initTelemetry initializes tracing/metrics; the returned func shuts them
// down with a bounded deadline. Telemetry failure degrades, never aborts.
func initTelemetry(ctx context.Context, cfg *config.Config, logger *slog.Logger) func() {
	tel, shutdownTelemetry, err := telemetry.Init(ctx, telemetry.DefaultConfig(cfg.GCP.ProjectID))
	if err != nil {
		logger.Error("Failed to initialize telemetry", slog.Any("error", err))
		return func() {}
	}
	logger.Info("Telemetry initialized",
		slog.Bool("tracing", tel.TracerProvider() != nil),
		slog.Bool("metrics", tel.MeterProvider() != nil),
	)
	//nolint:contextcheck // Shutdown must outlive the canceled lifecycle context.
	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := shutdownTelemetry(shutdownCtx); err != nil {
			logger.Error("Failed to shutdown telemetry", slog.Any("error", err))
		}
	}
}
