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

package machinetypefeature

import (
	"log/slog"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

// Deps carries the machine-type slice's optional collaborators; nil fields
// disable the corresponding capability.
type Deps struct {
	AuditLogger audit.Sink

	// Refresher rediscovers Compute Engine's shapes when curation changes.
	// Unwired, a curation write takes effect at the next structural refresh.
	Refresher ShapeRefresher
}

// WireCuration builds the curation repository alone.
//
// It comes first and separately because the catalog snapshot narrows its
// Compute Engine fetch to these families, and the composed machine-type
// catalog is then built from that snapshot: one Wire returning both would have
// to hand the snapshot a repository that does not exist yet.
func WireCuration(client *spannerplatform.Client) *CurationRepository {
	return NewCurationRepository(client)
}

// Wire creates the machine-type feature over the curation the snapshot was
// built from. The repository is returned for its cross-slice consumers
// (catalog validation and zone ranking, host shape resolution, declarative
// device placement, the Spot advice sync).
func Wire(
	curation *CurationRepository,
	discovery machinetype.Discovery,
	logger *slog.Logger,
	deps Deps,
) (*MachineTypeHandler, *MachineTypeRepository) {
	repo := NewMachineTypeRepository(curation, discovery)
	handler := NewMachineTypeHandler(repo, curation, logger)
	handler.SetAuditLogger(deps.AuditLogger)
	handler.SetShapeRefresher(deps.Refresher)
	return handler, repo
}
