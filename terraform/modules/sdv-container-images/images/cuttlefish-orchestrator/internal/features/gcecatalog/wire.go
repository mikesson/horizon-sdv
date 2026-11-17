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

package gcecatalogfeature

import (
	"log/slog"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// Deps carries the catalog slice's optional collaborators: the audit sink for
// zone curation and the placement-plane stores the zone ranker consults; nil
// fields rank without the corresponding signal.
type Deps struct {
	AuditLogger audit.Sink
	ZoneHealth  gcecatalog.ZoneHealthStore
	Policy      PolicyReader
	SpotAdvice  gcecatalog.SpotAdviceStore
	// Shapes is the in-process Compute Engine snapshot; nil ranks and admits
	// with every availability cell unknown, which removes no zone.
	Shapes ShapeCatalog
}

// Wire creates the GCE catalog feature: the assembled validator+ranker and
// the handler. machineTypes is the curated machine-type catalog shapes resolve
// against. The zone policy is built by the caller and passed in, because the
// GCE snapshot in Deps.Shapes scopes its quota refresh to the operator's
// enabled zones, which that policy holds. It is the neutral edge of that
// cycle. The validator is returned for its cross-slice consumers (host
// admission, device placement).
func Wire(zones *ZonePolicy, machineTypes machinetype.Catalog, config ValidatorConfig, logger *slog.Logger, deps Deps) (*Handler, *CatalogValidator) {
	validator := NewCatalogValidator(zones, machineTypes, config, logger,
		WithZoneHealthStore(deps.ZoneHealth),
		WithPolicyReader(deps.Policy),
		WithSpotAdviceStore(deps.SpotAdvice),
		WithShapeCatalog(deps.Shapes),
	)
	handler := NewHandler(zones, deps.Shapes, machineTypes)
	handler.SetAuditLogger(deps.AuditLogger)
	return handler, validator
}
