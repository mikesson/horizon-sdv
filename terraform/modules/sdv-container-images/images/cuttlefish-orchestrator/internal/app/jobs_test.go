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
	"log/slog"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/config"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// A keeper top-up must land in the fleet class, because that is the only thing
// that makes it inventory: standby is counted per class, so a host stamped any
// other way would be provisioned, billed, and then invisible to the arithmetic
// that asked for it. The keeper would top up forever against a floor it could
// never observe itself meeting.
//
// This asserts the property, not the literal. 'auto' would also derive
// fleet and would be merely a provenance loss; 'user' derives reserved and is
// the failure this exists to catch.
func TestFleetKeeperOriginDerivesFleetClass(t *testing.T) {
	t.Parallel()
	// Shared and unbatched: the shape ResolveFleetHostSpec produces for a
	// single-instance headless standby host.
	if got := host.DeriveClass(fleetKeeperOrigin, false, ""); got != host.ClassFleet {
		t.Errorf("a keeper top-up derives class %q, want %q", got, host.ClassFleet)
	}
}

// buildJobs treats a nil probe as "not wired", and startJobs and stop both
// skip it on that basis: a construction failure must yield nil
// (warn-and-degrade) rather than a partially built service, which those nil
// checks would start and join like a healthy one.
func TestNewCatalogSyncNilOnInvalidConfig(t *testing.T) {
	t.Parallel()
	// Zero config: ProjectID missing and the probe interval zero, so Validate
	// rejects.
	svc := newCatalogSync(&config.Config{}, nil, &featureSet{}, slog.Default())
	if svc != nil {
		t.Fatal("newCatalogSync must return nil on a config failure, not a partially built service")
	}
}
