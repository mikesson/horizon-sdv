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

package hostconfigfeature

import (
	"log/slog"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
)

// Deps carries the hostconfig slice's optional collaborators; nil fields
// disable the corresponding capability.
type Deps struct {
	AuditLogger audit.Sink

	// MachineTypes answers the catalog default the packing density is judged
	// against, and the whole catalog a shape ladder's rungs are admitted
	// against. Nil leaves the density unjudged, so it is reported and accepted
	// verbatim, which is what this slice did before the bound existed, and it
	// refuses every ladder write.
	MachineTypes MachineCatalog
}

// Wire creates all hostconfig feature components and returns the handler, the
// repository, and the service. The service is also the deployment's one host
// image resolution (the host feature's create preflight, provision task and
// arm readiness read it) and the settings source the control-plane snapshot
// reports the sweep policy from. defaultArtifactsBucket is the
// deployment-default artifacts bucket (CCO_ARTIFACTS_BUCKET) the admin
// surface reports as the fallback.
func Wire(
	spannerClient *spanner.Client,
	images Images,
	logger *slog.Logger,
	defaultArtifactsBucket string,
	deps Deps,
) (*Handler, *Repository, *Service) {
	repo := NewRepository(spannerClient)
	systemConfigRepo := spanner.NewSystemConfigRepository(spannerClient)
	svc := NewService(repo, systemConfigRepo, images, deps.MachineTypes, logger, defaultArtifactsBucket)
	handler := NewHandler(svc)
	handler.SetAuditLogger(deps.AuditLogger)
	return handler, repo, svc
}
