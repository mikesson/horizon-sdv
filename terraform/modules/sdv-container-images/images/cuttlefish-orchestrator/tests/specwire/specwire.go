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

// Package specwire builds a router configuration with every dependency nil, so
// route registration runs and the handlers themselves never do.
//
// It exists so the spec server and the router's registration test share ONE
// wiring. Two copies would defeat the test they feed: the completeness
// assertion in internal/router checks THIS literal, and a second literal for
// the spec server could omit a slice without failing it.
package specwire

import (
	adminfeature "cloud-cuttlefish-orchestrator/internal/features/admin"
	batchfeature "cloud-cuttlefish-orchestrator/internal/features/batch"
	buildfeature "cloud-cuttlefish-orchestrator/internal/features/build"
	devicefeature "cloud-cuttlefish-orchestrator/internal/features/device"
	gcecatalogfeature "cloud-cuttlefish-orchestrator/internal/features/gcecatalog"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
	hostconfigfeature "cloud-cuttlefish-orchestrator/internal/features/hostconfig"
	machinetypefeature "cloud-cuttlefish-orchestrator/internal/features/machinetype"
	operationfeature "cloud-cuttlefish-orchestrator/internal/features/operation"
	quotafeature "cloud-cuttlefish-orchestrator/internal/features/quota"
	relayfeature "cloud-cuttlefish-orchestrator/internal/features/relay"
	storagefeature "cloud-cuttlefish-orchestrator/internal/features/storage"
	"cloud-cuttlefish-orchestrator/internal/middleware"
	"cloud-cuttlefish-orchestrator/internal/router"
)

// Config returns the router configuration for spec generation.
//
// Registering a slice here is NOT optional bookkeeping. registerHumaHandlers
// nil-checks every field, so a slice omitted from this literal is silently
// absent from /openapi.json, and the console's schema-drift check then
// compares its Zod schemas against nothing and passes by not looking.
// TestConfigRegistersEverySlice in internal/router is what holds that line.
func Config() router.Config {
	adminHandler, healthHandler := adminfeature.Wire(nil, nil, nil, adminfeature.Deps{}, "specgen", nil)
	return router.Config{
		DeviceHandler:          devicefeature.NewDeviceHandler(nil, nil, nil, nil),
		BatchHandler:           batchfeature.NewHandler(nil),
		HostHandler:            hostfeature.NewHostHandler(nil),
		QuotaHandler:           quotafeature.NewQuotaHandler(nil),
		BuildHandler:           buildfeature.NewBuildHandler(nil),
		StorageHandler:         storagefeature.NewStorageHandler(nil),
		MachineTypeHandler:     machinetypefeature.NewMachineTypeHandler(nil, nil, nil),
		GCECatalogHandler:      gcecatalogfeature.NewHandler(nil, nil, nil),
		AdminHandler:           adminHandler,
		HealthHandler:          healthHandler,
		HostImageConfigHandler: hostconfigfeature.NewHandler(nil),
		RelayHandler:           relayfeature.NewHandler(nil),
		OperationHandler:       operationfeature.NewHandler(nil),
		// Dev mode so the router authenticates every request as a throwaway
		// identity. The spec server is a build-time service, and
		// /openapi.json is otherwise behind IAP auth (which would 401 the
		// schema-drift check).
		AuthConfig: middleware.AuthConfig{
			Mode:             middleware.AccessModeIAP,
			DevMode:          true,
			DevFallbackEmail: "specgen@localhost",
		},
	}
}
